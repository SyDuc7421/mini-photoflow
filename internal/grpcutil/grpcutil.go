// Package grpcutil holds the small gRPC helpers shared by the microservices.
package grpcutil

import (
	"context"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// UserKey is the metadata key the gateway uses to pass the authenticated user id.
const UserKey = "x-user-id"

// WithUser returns ctx whose outgoing gRPC calls carry the user id.
func WithUser(ctx context.Context, userID int64) context.Context {
	return metadata.AppendToOutgoingContext(ctx, UserKey, strconv.FormatInt(userID, 10))
}

// UserID reads the caller's user id from incoming metadata.
func UserID(ctx context.Context) (int64, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get(UserKey)
	if len(vals) == 0 {
		return 0, status.Error(codes.Unauthenticated, "missing user id")
	}
	id, err := strconv.ParseInt(vals[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, status.Error(codes.Unauthenticated, "invalid user id")
	}
	return id, nil
}

// Dial returns a lazily connected plaintext client, so a service can start before its peers.
func Dial(addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Default backoff grows to 2 minutes; keep recovery after a restart short.
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.Config{BaseDelay: 200 * time.Millisecond, Multiplier: 1.6, MaxDelay: 2 * time.Second},
			MinConnectTimeout: 2 * time.Second,
		}))
}
