// Command gateway exposes the REST API and forwards each call to the gRPC services.
package main

import (
	"log"
	"net/http"
	"os"

	"photoshare/internal/grpcutil"
	"photoshare/microservices/pb/authpb"
	"photoshare/microservices/pb/feedpb"
	"photoshare/microservices/pb/postpb"
	"photoshare/microservices/pb/socialpb"
)

type gateway struct {
	secret []byte
	auth   authpb.AuthClient
	post   postpb.PostClient
	social socialpb.SocialClient
	feed   feedpb.FeedClient
}

func main() {
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

	socialConn, err := grpcutil.Dial(env("SOCIAL_ADDR", "localhost:9003"))
	if err != nil {
		log.Fatalf("social client: %v", err)
	}
	defer socialConn.Close()

	feedConn, err := grpcutil.Dial(env("FEED_ADDR", "localhost:9004"))
	if err != nil {
		log.Fatalf("feed client: %v", err)
	}
	defer feedConn.Close()

	g := &gateway{
		secret: []byte(env("JWT_SECRET", "dev-secret-change-me")),
		auth:   authpb.NewAuthClient(authConn),
		post:   postpb.NewPostClient(postConn),
		social: socialpb.NewSocialClient(socialConn),
		feed:   feedpb.NewFeedClient(feedConn),
	}
	mux := http.NewServeMux()
	g.registerAuth(mux)
	g.registerPosts(mux)
	g.registerSocial(mux)
	g.registerFeed(mux)

	log.Println("gateway listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
