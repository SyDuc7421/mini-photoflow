package main

import (
	"context"
	_ "embed"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"photoshare/monolith/auth"
)

//go:embed migrations/001_init.sql
var initSQL string

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	ctx := context.Background()
	db, err := connect(ctx, env("DATABASE_URL", "postgres://photo:photo@localhost:5432/photo"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(ctx, initSQL); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	mux := http.NewServeMux()
	auth.Register(mux, db, []byte(env("JWT_SECRET", "dev-secret-change-me")))

	log.Println("monolith listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// connect retries so the app survives starting before Postgres accepts connections.
func connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	var lastErr error
	for i := 0; i < 30; i++ {
		pool, err := pgxpool.New(ctx, url)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				return pool, nil
			}
			pool.Close()
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	return nil, lastErr
}
