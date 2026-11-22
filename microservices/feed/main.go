// Command feed builds the home feed in one of two ways, chosen by FEED_MODE:
//   - sync: no data of its own; every request is assembled from the auth, post and social services.
//   - event: a read model in its own database, kept up to date by NATS events; a request calls nobody.
package main

import (
	"context"
	_ "embed"
	"log"
	"net"
	"os"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/nats-io/nats.go"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"photoshare/internal/cursor"
	"photoshare/internal/dbutil"
	"photoshare/internal/events"
	"photoshare/internal/grpcutil"
	"photoshare/microservices/pb/authpb"
	"photoshare/microservices/pb/feedpb"
	"photoshare/microservices/pb/postpb"
	"photoshare/microservices/pb/socialpb"
)

//go:embed schema.sql
var schema string

const (
	maxLimit     = 50
	urlTTL       = time.Hour
	applyTimeout = 5 * time.Second
)

type server struct {
	feedpb.UnimplementedFeedServer
	// sync mode
	auth   authpb.AuthClient
	post   postpb.PostClient
	social socialpb.SocialClient
	// event mode (rm == nil in sync mode)
	rm     *ReadModel
	public *minio.Client // only signs image URLs; the read model stores keys, not expiring URLs
	bucket string
}

func main() {
	mode := env("FEED_MODE", "sync")
	if mode != "sync" && mode != "event" {
		log.Fatalf("FEED_MODE=%q: want sync or event", mode)
	}
	dial := func(name, addr string) *grpc.ClientConn {
		conn, err := grpcutil.Dial(addr)
		if err != nil {
			log.Fatalf("%s client: %v", name, err)
		}
		return conn
	}
	authConn := dial("auth", env("AUTH_ADDR", "localhost:9001"))
	postConn := dial("post", env("POST_ADDR", "localhost:9002"))
	socialConn := dial("social", env("SOCIAL_ADDR", "localhost:9003"))
	defer authConn.Close()
	defer postConn.Close()
	defer socialConn.Close()

	lis, err := net.Listen("tcp", env("GRPC_ADDR", ":9004"))
	if err != nil {
		log.Fatal(err)
	}
	srv := &server{
		auth:   authpb.NewAuthClient(authConn),
		post:   postpb.NewPostClient(postConn),
		social: socialpb.NewSocialClient(socialConn),
	}
	if mode == "event" {
		srv.startEventMode()
	}
	s := grpc.NewServer()
	feedpb.RegisterFeedServer(s, srv)
	log.Printf("feed listening on %s (mode %s)", lis.Addr(), mode)
	log.Fatal(s.Serve(lis))
}

// startEventMode opens the feed's own database and starts consuming events into it.
func (s *server) startEventMode() {
	ctx := context.Background()
	db, err := dbutil.Connect(ctx, env("DATABASE_URL", "postgres://feed:feed@localhost:5432/feed"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	if _, err := db.Exec(ctx, schema); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	s.rm = &ReadModel{db: db}

	s.bucket = env("MINIO_BUCKET", "photos")
	s.public, err = minio.New(env("MINIO_PUBLIC_ENDPOINT", "localhost:9000"), &minio.Options{
		Creds:  credentials.NewStaticV4(env("MINIO_ACCESS_KEY", "minioadmin"), env("MINIO_SECRET_KEY", "minioadmin"), ""),
		Region: "us-east-1", // fixed region keeps signing offline
	})
	if err != nil {
		log.Fatalf("minio: %v", err)
	}

	nc, err := events.Connect(env("NATS_URL", "nats://localhost:4222"))
	if err != nil {
		log.Fatalf("nats: %v", err)
	}
	for _, subject := range []string{events.SubjectPostCreated, events.SubjectFollowSet, events.SubjectLikeSet, events.SubjectCommentAdded} {
		_, err := nc.Subscribe(subject, func(m *nats.Msg) {
			// A hung database must not block this subscription forever.
			ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
			defer cancel()
			if err := s.rm.Apply(ctx, Event{Subject: m.Subject, Data: m.Data}); err != nil {
				log.Printf("feed: dropped %s event: %v", m.Subject, err)
			}
		})
		if err != nil {
			log.Fatalf("subscribe %s: %v", subject, err)
		}
	}
}

func (s *server) GetFeed(ctx context.Context, req *feedpb.GetFeedRequest) (*feedpb.GetFeedReply, error) {
	me, err := grpcutil.UserID(ctx)
	if err != nil {
		return nil, err
	}
	if req.Limit < 1 {
		return nil, status.Error(codes.InvalidArgument, "limit must be a positive integer")
	}
	limit := int(min(req.Limit, maxLimit))
	var before *postpb.Cursor
	if req.Before != "" {
		c, ok := cursor.Parse(req.Before)
		if !ok {
			return nil, status.Error(codes.InvalidArgument, "before is not a valid cursor")
		}
		before = &postpb.Cursor{CreatedAtMicro: c.Micro, Id: c.ID}
	}
	if s.rm != nil {
		return s.eventFeed(ctx, me, req.Before, limit)
	}
	return s.syncFeed(ctx, me, before, limit)
}

// eventFeed reads only the feed's own tables, so it keeps working while auth, post and social are down.
// The data can lag behind: it is as fresh as the last event the feed received.
func (s *server) eventFeed(ctx context.Context, me int64, before string, limit int) (*feedpb.GetFeedReply, error) {
	items, next, err := s.rm.Feed(ctx, me, before, limit)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "database error")
	}
	out := &feedpb.GetFeedReply{Next: next}
	for _, it := range items {
		u, err := s.public.PresignedGetObject(ctx, s.bucket, it.ImageKey, urlTTL, nil)
		if err != nil {
			return nil, status.Error(codes.Internal, "cannot sign image url")
		}
		out.Items = append(out.Items, &feedpb.Item{
			Id: it.ID, Author: it.Author, Caption: it.Caption, ImageUrl: u.String(),
			Likes: it.Likes, Comments: it.Comments, LikedByMe: it.LikedByMe, CreatedAtMicro: it.CreatedAtMicro,
		})
	}
	return out, nil
}

// syncFeed asks social who the viewer follows, post for their newest posts, then social for the
// counts and auth for the author names. If any of them fails, the feed fails.
func (s *server) syncFeed(ctx context.Context, me int64, before *postpb.Cursor, limit int) (*feedpb.GetFeedReply, error) {
	following, err := s.social.ListFollowing(ctx, &socialpb.UserRef{Id: me})
	if err != nil {
		return nil, err
	}
	out := &feedpb.GetFeedReply{}
	if len(following.Ids) == 0 {
		return out, nil
	}

	// One extra post tells whether another page exists.
	list, err := s.post.ListPostsByAuthors(ctx, &postpb.ListPostsRequest{
		AuthorIds: following.Ids, Before: before, Limit: int32(limit + 1),
	})
	if err != nil {
		return nil, err
	}
	posts := list.Posts
	more := len(posts) > limit
	if more {
		posts = posts[:limit]
	}
	if len(posts) == 0 {
		return out, nil
	}

	postIDs := make([]int64, len(posts))
	authorIDs := make([]int64, len(posts))
	for i, p := range posts {
		postIDs[i], authorIDs[i] = p.Id, p.AuthorId
	}
	var counts *socialpb.CountsReply
	var names *authpb.GetUsersReply
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		counts, err = s.social.CountsForPosts(gctx, &socialpb.CountsRequest{PostIds: postIDs, ViewerId: me})
		return err
	})
	g.Go(func() (err error) {
		names, err = s.auth.GetUsers(gctx, &authpb.GetUsersRequest{Ids: authorIDs})
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	byPost := make(map[int64]*socialpb.PostCounts, len(counts.Counts))
	for _, c := range counts.Counts {
		byPost[c.PostId] = c // matched by id: the reply order is not guaranteed
	}
	for _, p := range posts {
		author, ok := names.Names[p.AuthorId]
		if !ok {
			author = p.Author // the name copied when the post was created
		}
		item := &feedpb.Item{
			Id: p.Id, Author: author, Caption: p.Caption, ImageUrl: p.ImageUrl,
			CreatedAtMicro: p.CreatedAtMicro,
		}
		if c := byPost[p.Id]; c != nil {
			item.Likes, item.Comments, item.LikedByMe = c.Likes, c.Comments, c.LikedByMe
		}
		out.Items = append(out.Items, item)
	}
	if more {
		last := posts[len(posts)-1]
		out.Next = cursor.Cursor{Micro: last.CreatedAtMicro, ID: last.Id}.String()
	}
	return out, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
