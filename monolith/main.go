package main

import (
	"context"
	_ "embed"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"photoshare/monolith/auth"
	"photoshare/monolith/post"
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

	secret := []byte(env("JWT_SECRET", "dev-secret-change-me"))
	bucket := env("MINIO_BUCKET", "photos")
	store, public, err := minioClients()
	if err != nil {
		log.Fatalf("minio: %v", err)
	}
	if err := ensureBucket(ctx, store, bucket); err != nil {
		log.Fatalf("minio bucket: %v", err)
	}

	mux := http.NewServeMux()
	auth.Register(mux, db, secret)
	post.Register(mux, db, secret, store, public, bucket)

	log.Println("monolith listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// minioClients returns a client for the internal endpoint (uploads) and one for
// the host-reachable endpoint (presigned URLs). A fixed region keeps signing offline.
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

// ensureBucket retries so the app survives starting before MinIO is ready.
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
