// Command gateway exposes the REST API and forwards each call to the gRPC services.
package main

import (
	"log"
	"net/http"
	"os"

	"photoshare/internal/grpcutil"
	"photoshare/microservices/pb/authpb"
)

type gateway struct {
	secret []byte
	auth   authpb.AuthClient
}

func main() {
	authConn, err := grpcutil.Dial(env("AUTH_ADDR", "localhost:9001"))
	if err != nil {
		log.Fatalf("auth client: %v", err)
	}
	defer authConn.Close()

	g := &gateway{
		secret: []byte(env("JWT_SECRET", "dev-secret-change-me")),
		auth:   authpb.NewAuthClient(authConn),
	}
	mux := http.NewServeMux()
	g.registerAuth(mux)

	log.Println("gateway listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
