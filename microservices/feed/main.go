// Command feed builds the home feed. In sync mode it has no data of its own: every
// request is assembled from the auth, post and social services.
package main

import (
	"context"
	"log"
	"net"
	"os"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"photoshare/internal/cursor"
	"photoshare/internal/grpcutil"
	"photoshare/microservices/pb/authpb"
	"photoshare/microservices/pb/feedpb"
	"photoshare/microservices/pb/postpb"
	"photoshare/microservices/pb/socialpb"
)

const maxLimit = 50

type server struct {
	feedpb.UnimplementedFeedServer
	auth   authpb.AuthClient
	post   postpb.PostClient
	social socialpb.SocialClient
}

func main() {
	if mode := env("FEED_MODE", "sync"); mode != "sync" {
		log.Fatalf("FEED_MODE=%q is not supported yet (only sync)", mode)
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
	s := grpc.NewServer()
	feedpb.RegisterFeedServer(s, &server{
		auth:   authpb.NewAuthClient(authConn),
		post:   postpb.NewPostClient(postConn),
		social: socialpb.NewSocialClient(socialConn),
	})
	log.Printf("feed listening on %s (mode sync)", lis.Addr())
	log.Fatal(s.Serve(lis))
}

// GetFeed asks social who the viewer follows, post for their newest posts, then social for the
// counts and auth for the author names. If any of them fails, the feed fails.
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
