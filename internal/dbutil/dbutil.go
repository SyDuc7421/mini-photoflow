// Package dbutil connects to Postgres, waiting for it to accept connections.
package dbutil

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect retries for about 30 seconds so a service survives starting before Postgres.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
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
