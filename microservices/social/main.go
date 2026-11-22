// Command social is the gRPC service that owns follows, likes and comments.
package main

import (
	"context"
	_ "embed"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"photoshare/internal/dbutil"
	"photoshare/internal/grpcutil"
	"photoshare/microservices/pb/authpb"
	"photoshare/microservices/pb/postpb"
	"photoshare/microservices/pb/socialpb"
)

//go:embed schema.sql
var schema string

type server struct {
	socialpb.UnimplementedSocialServer
	db   *pgxpool.Pool
	auth authpb.AuthClient
	post postpb.PostClient
}

func main() {
	ctx := context.Background()
	db, err := dbutil.Connect(ctx, env("DATABASE_URL", "postgres://social:social@localhost:5432/social"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(ctx, schema); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	authConn, err := grpcutil.Dial(env("AUTH_ADDR", "localhost:9001"))
	if err != nil {
		log.Fatalf("auth client: %v", err)
	}
	defer authConn.Close()
	postConn, err := grpcutil.Dial(env("POST_ADDR", "localhost:9002"))
	if err != nil {
		log.Fatalf("post client: %v", err)
	}
	defer postConn.Close()

	lis, err := net.Listen("tcp", env("GRPC_ADDR", ":9003"))
	if err != nil {
		log.Fatal(err)
	}
	s := grpc.NewServer()
	socialpb.RegisterSocialServer(s, &server{db: db, auth: authpb.NewAuthClient(authConn), post: postpb.NewPostClient(postConn)})
	log.Printf("social listening on %s", lis.Addr())
	log.Fatal(s.Serve(lis))
}

func (s *server) Follow(ctx context.Context, ref *socialpb.UserRef) (*emptypb.Empty, error) {
	me, err := grpcutil.UserID(ctx)
	if err != nil {
		return nil, err
	}
	if ref.Id == me {
		return nil, status.Error(codes.InvalidArgument, "cannot follow yourself")
	}
	if err := s.requireUser(ctx, ref.Id); err != nil {
		return nil, err
	}
	// A valid token for a user auth no longer knows must not create rows (the monolith's FK gives 401).
	if err := s.requireCaller(ctx, me); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO follows (follower_id, followee_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, me, ref.Id)
	return &emptypb.Empty{}, dbErr(err)
}

func (s *server) Unfollow(ctx context.Context, ref *socialpb.UserRef) (*emptypb.Empty, error) {
	me, err := grpcutil.UserID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireUser(ctx, ref.Id); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM follows WHERE follower_id = $1 AND followee_id = $2`, me, ref.Id)
	return &emptypb.Empty{}, dbErr(err)
}

// Like checks the post only. It deliberately does not call auth, so liking keeps working
// while auth is down; the price is that a token of a deleted user can still add a like.
func (s *server) Like(ctx context.Context, ref *socialpb.PostRef) (*emptypb.Empty, error) {
	me, err := grpcutil.UserID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requirePost(ctx, ref.Id); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO likes (post_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, ref.Id, me)
	return &emptypb.Empty{}, dbErr(err)
}

func (s *server) Unlike(ctx context.Context, ref *socialpb.PostRef) (*emptypb.Empty, error) {
	me, err := grpcutil.UserID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requirePost(ctx, ref.Id); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM likes WHERE post_id = $1 AND user_id = $2`, ref.Id, me)
	return &emptypb.Empty{}, dbErr(err)
}

func (s *server) AddComment(ctx context.Context, req *socialpb.AddCommentRequest) (*socialpb.Comment, error) {
	me, err := grpcutil.UserID(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Text) == "" || strings.ContainsRune(req.Text, 0) {
		return nil, status.Error(codes.InvalidArgument, "text is required")
	}
	if err := s.requirePost(ctx, req.PostId); err != nil {
		return nil, err
	}
	names, err := s.auth.GetUsers(ctx, &authpb.GetUsersRequest{Ids: []int64{me}})
	if err != nil {
		return nil, err
	}
	name, ok := names.Names[me]
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "user no longer exists")
	}
	c := &socialpb.Comment{PostId: req.PostId, Author: name, Text: req.Text}
	var created time.Time
	err = s.db.QueryRow(ctx,
		`INSERT INTO comments (post_id, user_id, text) VALUES ($1, $2, $3) RETURNING id, created_at`,
		req.PostId, me, req.Text).Scan(&c.Id, &created)
	if err != nil {
		return nil, dbErr(err)
	}
	c.CreatedAtMicro = created.UnixMicro()
	return c, nil
}

func (s *server) ListComments(ctx context.Context, ref *socialpb.PostRef) (*socialpb.ListCommentsReply, error) {
	if err := s.requirePost(ctx, ref.Id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx,
		`SELECT id, user_id, text, created_at FROM comments WHERE post_id = $1 ORDER BY created_at, id`, ref.Id)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := &socialpb.ListCommentsReply{}
	var uids []int64 // uids[i] wrote out.Comments[i]
	for rows.Next() {
		c := &socialpb.Comment{PostId: ref.Id}
		var uid int64
		var created time.Time
		if err := rows.Scan(&c.Id, &uid, &c.Text, &created); err != nil {
			return nil, dbErr(err)
		}
		c.CreatedAtMicro = created.UnixMicro()
		out.Comments = append(out.Comments, c)
		uids = append(uids, uid)
	}
	if rows.Err() != nil {
		return nil, dbErr(rows.Err())
	}
	if len(out.Comments) == 0 {
		return out, nil
	}
	names, err := s.auth.GetUsers(ctx, &authpb.GetUsersRequest{Ids: uids})
	if err != nil {
		return nil, err
	}
	kept := out.Comments[:0]
	for i, c := range out.Comments {
		if name, ok := names.Names[uids[i]]; ok { // like the monolith's inner join, skip deleted authors
			c.Author = name
			kept = append(kept, c)
		}
	}
	out.Comments = kept
	return out, nil
}

// ListFollowing returns the ids of the users that ref.Id follows. Called by feed, not by clients.
func (s *server) ListFollowing(ctx context.Context, ref *socialpb.UserRef) (*socialpb.UserIds, error) {
	rows, err := s.db.Query(ctx, `SELECT followee_id FROM follows WHERE follower_id = $1`, ref.Id)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := &socialpb.UserIds{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, dbErr(err)
		}
		out.Ids = append(out.Ids, id)
	}
	return out, dbErr(rows.Err())
}

// CountsForPosts returns one entry per requested post, zeros for posts nobody liked or commented.
func (s *server) CountsForPosts(ctx context.Context, req *socialpb.CountsRequest) (*socialpb.CountsReply, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id,
		       (SELECT count(*) FROM likes l WHERE l.post_id = p.id),
		       (SELECT count(*) FROM comments c WHERE c.post_id = p.id),
		       EXISTS (SELECT 1 FROM likes l WHERE l.post_id = p.id AND l.user_id = $2)
		FROM unnest($1::bigint[]) AS p(id)`, req.PostIds, req.ViewerId)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := &socialpb.CountsReply{}
	for rows.Next() {
		c := &socialpb.PostCounts{}
		if err := rows.Scan(&c.PostId, &c.Likes, &c.Comments, &c.LikedByMe); err != nil {
			return nil, dbErr(err)
		}
		out.Counts = append(out.Counts, c)
	}
	return out, dbErr(rows.Err())
}

// requireUser maps a missing user to NotFound; a failing auth service passes through as Unavailable.
func (s *server) requireUser(ctx context.Context, id int64) error {
	r, err := s.auth.UserExists(ctx, &authpb.UserRef{Id: id})
	if err != nil {
		return err
	}
	if !r.Exists {
		return status.Error(codes.NotFound, "user not found")
	}
	return nil
}

func (s *server) requireCaller(ctx context.Context, me int64) error {
	r, err := s.auth.UserExists(ctx, &authpb.UserRef{Id: me})
	if err != nil {
		return err
	}
	if !r.Exists {
		return status.Error(codes.Unauthenticated, "user no longer exists")
	}
	return nil
}

func (s *server) requirePost(ctx context.Context, id int64) error {
	r, err := s.post.PostExists(ctx, &postpb.PostRef{Id: id})
	if err != nil {
		return err
	}
	if !r.Exists {
		return status.Error(codes.NotFound, "post not found")
	}
	return nil
}

func dbErr(err error) error {
	if err == nil {
		return nil
	}
	return status.Error(codes.Unavailable, "database error")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
