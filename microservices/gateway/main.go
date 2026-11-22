// Command gateway exposes the REST API and forwards each call to the gRPC services.
package main

import (
	"log"
	"net/http"
	"os"

	"photoshare/internal/grpcutil"
	"photoshare/microservices/pb/authpb"
	"photoshare/microservices/pb/postpb"
	"photoshare/microservices/pb/socialpb"
)

type gateway struct {
	secret []byte
	auth   authpb.AuthClient
	post   postpb.PostClient
	social socialpb.SocialClient
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

	g := &gateway{
		secret: []byte(env("JWT_SECRET", "dev-secret-change-me")),
		auth:   authpb.NewAuthClient(authConn),
		post:   postpb.NewPostClient(postConn),
		social: socialpb.NewSocialClient(socialConn),
	}
	mux := http.NewServeMux()
	g.registerAuth(mux)
	g.registerPosts(mux)
	g.registerSocial(mux)

	log.Println("gateway listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
