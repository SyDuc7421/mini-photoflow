// Command post is the gRPC service that owns posts and their images.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"photoshare/internal/dbutil"
	"photoshare/internal/events"
	"photoshare/internal/grpcutil"
	"photoshare/microservices/pb/authpb"
	"photoshare/microservices/pb/postpb"
)

//go:embed schema.sql
var schema string

const (
	maxImageBytes   = 5 << 20
	maxCaptionChars = 500
	maxListLimit    = 100
	urlTTL          = time.Hour
	commitTimeout   = 10 * time.Second
	// maxMicro is 9999-12-31T23:59:59.999999Z, the latest timestamp Postgres can compare.
	maxMicro = 253402300799999999
)

type server struct {
	postpb.UnimplementedPostServer
	db     *pgxpool.Pool
	store  *minio.Client // uploads, over the internal network
	public *minio.Client // only signs URLs, with the host name clients can reach
	bucket string
	auth   authpb.AuthClient
	nc     *nats.Conn // nil when NATS_URL is unset: events are off
}

func main() {
	ctx := context.Background()
	db, err := dbutil.Connect(ctx, env("DATABASE_URL", "postgres://post:post@localhost:5432/post"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(ctx, schema); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	bucket := env("MINIO_BUCKET", "photos")
	store, public, err := minioClients()
	if err != nil {
		log.Fatalf("minio: %v", err)
	}
	if err := ensureBucket(ctx, store, bucket); err != nil {
		log.Fatalf("minio bucket: %v", err)
	}

	authConn, err := grpcutil.Dial(env("AUTH_ADDR", "localhost:9001"))
	if err != nil {
		log.Fatalf("auth client: %v", err)
	}
	defer authConn.Close()

	var nc *nats.Conn
	if url := os.Getenv("NATS_URL"); url != "" {
		if nc, err = events.Connect(url); err != nil {
			log.Fatalf("nats: %v", err)
		}
		defer nc.Close()
	}

	lis, err := net.Listen("tcp", env("GRPC_ADDR", ":9002"))
	if err != nil {
		log.Fatal(err)
	}
	s := grpc.NewServer()
	postpb.RegisterPostServer(s, &server{db: db, store: store, public: public, bucket: bucket, auth: authpb.NewAuthClient(authConn), nc: nc})
	log.Printf("post listening on %s", lis.Addr())
	log.Fatal(s.Serve(lis))
}

// CreatePost buffers at most 5 MiB, validates everything, and only then writes MinIO and Postgres.
func (s *server) CreatePost(stream postpb.Post_CreatePostServer) error {
	ctx := stream.Context()
	userID, err := grpcutil.UserID(ctx)
	if err != nil {
		return err
	}

	var caption string
	var haveMeta bool
	var image bytes.Buffer
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch part := req.Part.(type) {
		case *postpb.CreatePostRequest_Meta:
			if haveMeta {
				return status.Error(codes.InvalidArgument, "meta sent twice")
			}
			haveMeta, caption = true, part.Meta.Caption
		case *postpb.CreatePostRequest_Chunk:
			if image.Len()+len(part.Chunk) > maxImageBytes {
				return status.Error(codes.ResourceExhausted, "image is larger than 5MB")
			}
			image.Write(part.Chunk)
		}
	}

	if !utf8.ValidString(caption) || strings.ContainsRune(caption, 0) {
		return status.Error(codes.InvalidArgument, "caption must be valid UTF-8 text")
	}
	if utf8.RuneCountInString(caption) > maxCaptionChars {
		return status.Error(codes.InvalidArgument, "caption is longer than 500 characters")
	}
	if image.Len() == 0 {
		return status.Error(codes.InvalidArgument, "image is required")
	}
	ctype := http.DetectContentType(image.Bytes())
	if ctype != "image/jpeg" && ctype != "image/png" {
		return status.Error(codes.InvalidArgument, "image must be JPEG or PNG")
	}

	// The upload is complete and valid. Commit under its own short deadline so a client
	// that disconnects now cannot leave an image without a row or a row without an image.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitTimeout)
	defer cancel()

	names, err := s.auth.GetUsers(ctx, &authpb.GetUsersRequest{Ids: []int64{userID}})
	if err != nil {
		return err
	}
	name, ok := names.Names[userID]
	if !ok {
		return status.Error(codes.Unauthenticated, "user no longer exists")
	}

	key, err := newKey(userID, ctype)
	if err != nil {
		return status.Error(codes.Internal, "cannot create image key")
	}
	_, err = s.store.PutObject(ctx, s.bucket, key, bytes.NewReader(image.Bytes()), int64(image.Len()),
		minio.PutObjectOptions{ContentType: ctype})
	if err != nil {
		return status.Error(codes.Unavailable, "image storage error")
	}
	var id int64
	var created time.Time
	err = s.db.QueryRow(ctx,
		`INSERT INTO posts (author_id, author_name, caption, image_key) VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		userID, name, caption, key).Scan(&id, &created)
	if err != nil {
		if rerr := s.store.RemoveObject(context.WithoutCancel(ctx), s.bucket, key, minio.RemoveObjectOptions{}); rerr != nil {
			log.Printf("post: orphan object %s left in %s: %v", key, s.bucket, rerr)
		}
		return status.Error(codes.Unavailable, "database error")
	}
	// The row is committed; a lost event must not fail the request.
	if err := events.Publish(s.nc, events.SubjectPostCreated, events.PostCreated{
		PostID: id, AuthorID: userID, AuthorName: name, Caption: caption, ImageKey: key, CreatedAtMicro: created.UnixMicro(),
	}); err != nil {
		log.Printf("post: publish %s: %v", events.SubjectPostCreated, err)
	}
	return stream.SendAndClose(s.reply(ctx, id, userID, name, caption, key, created))
}

func (s *server) GetPost(ctx context.Context, ref *postpb.PostRef) (*postpb.PostReply, error) {
	var (
		authorID           int64
		name, caption, key string
		created            time.Time
	)
	err := s.db.QueryRow(ctx,
		`SELECT author_id, author_name, caption, image_key, created_at FROM posts WHERE id = $1`, ref.Id).
		Scan(&authorID, &name, &caption, &key, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "post not found")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "database error")
	}
	return s.reply(ctx, ref.Id, authorID, name, caption, key, created), nil
}

func (s *server) PostExists(ctx context.Context, ref *postpb.PostRef) (*postpb.ExistsReply, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts WHERE id = $1)`, ref.Id).Scan(&ok)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "database error")
	}
	return &postpb.ExistsReply{Exists: ok}, nil
}

// ListPostsByAuthors returns posts strictly before the cursor, newest first. The tuple
// comparison keeps paging exact when created_at ties.
func (s *server) ListPostsByAuthors(ctx context.Context, req *postpb.ListPostsRequest) (*postpb.ListPostsReply, error) {
	if req.Limit < 1 {
		return nil, status.Error(codes.InvalidArgument, "limit must be positive")
	}
	limit := min(req.Limit, maxListLimit)
	micro, id := int64(maxMicro), int64(math.MaxInt64)
	if b := req.Before; b != nil {
		if b.CreatedAtMicro < 0 || b.CreatedAtMicro > maxMicro || b.Id < 0 {
			return nil, status.Error(codes.InvalidArgument, "before is not a valid cursor")
		}
		micro, id = b.CreatedAtMicro, b.Id
	}
	if len(req.AuthorIds) == 0 {
		return &postpb.ListPostsReply{}, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, author_id, author_name, caption, image_key, created_at
		FROM posts
		WHERE author_id = ANY($1) AND (created_at, id) < ($2::timestamptz, $3::bigint)
		ORDER BY created_at DESC, id DESC
		LIMIT $4`, req.AuthorIds, time.UnixMicro(micro).UTC(), id, limit)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "database error")
	}
	defer rows.Close()
	out := &postpb.ListPostsReply{}
	for rows.Next() {
		var (
			pid, authorID      int64
			name, caption, key string
			created            time.Time
		)
		if err := rows.Scan(&pid, &authorID, &name, &caption, &key, &created); err != nil {
			return nil, status.Error(codes.Unavailable, "database error")
		}
		out.Posts = append(out.Posts, s.reply(ctx, pid, authorID, name, caption, key, created))
	}
	if rows.Err() != nil {
		return nil, status.Error(codes.Unavailable, "database error")
	}
	return out, nil
}

// reply builds a PostReply. Signing is an offline computation, so it cannot fail for a valid client.
func (s *server) reply(ctx context.Context, id, authorID int64, name, caption, key string, created time.Time) *postpb.PostReply {
	r := &postpb.PostReply{
		Id: id, AuthorId: authorID, Author: name, Caption: caption,
		CreatedAtMicro: created.UnixMicro(), ImageKey: key,
	}
	if u, err := s.public.PresignedGetObject(ctx, s.bucket, key, urlTTL, nil); err != nil {
		log.Printf("post: presign %s: %v", key, err)
	} else {
		r.ImageUrl = u.String()
	}
	return r
}

func newKey(userID int64, ctype string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	ext := ".png"
	if ctype == "image/jpeg" {
		ext = ".jpg"
	}
	return fmt.Sprintf("%d/%s%s", userID, hex.EncodeToString(raw[:]), ext), nil
}

// minioClients returns a client for the internal endpoint (uploads) and one for the
// host-reachable endpoint (presigned URLs). A fixed region keeps signing offline.
func minioClients() (store, public *minio.Client, err error) {
	creds := credentials.NewStaticV4(env("MINIO_ACCESS_KEY", "minioadmin"), env("MINIO_SECRET_KEY", "minioadmin"), "")
	mk := func(endpoint string) (*minio.Client, error) {
		return minio.New(endpoint, &minio.Options{Creds: creds, Region: "us-east-1"})
	}
	if store, err = mk(env("MINIO_ENDPOINT", "localhost:9000")); err != nil {
		return nil, nil, err
	}
	public, err = mk(env("MINIO_PUBLIC_ENDPOINT", "localhost:9000"))
	return store, public, err
}

// ensureBucket retries so the service survives starting before MinIO is ready.
func ensureBucket(ctx context.Context, c *minio.Client, bucket string) error {
	var lastErr error
	for i := 0; i < 30; i++ {
		exists, err := c.BucketExists(ctx, bucket)
		if err == nil && !exists {
			err = c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: "us-east-1"})
		}
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	return lastErr
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
