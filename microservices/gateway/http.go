package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"photoshare/internal/apierr"
	"photoshare/internal/grpcutil"
	"photoshare/internal/jwtutil"
)

const callTimeout = 5 * time.Second

// withTimeout bounds one gRPC call. Handlers apply it per call, so slow request bodies
// (uploads) are not counted against it.
func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, callTimeout)
}

// require runs next only for a valid Bearer token. The context handed to next
// carries the user id as outgoing gRPC metadata.
func (g *gateway) require(next func(w http.ResponseWriter, r *http.Request, ctx context.Context, userID int64)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || token == "" {
			apierr.Write(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		id, err := jwtutil.Verify(g.secret, token)
		if err != nil {
			apierr.Write(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
			return
		}
		next(w, r, grpcutil.WithUser(r.Context(), id), id)
	}
}

// writeError maps a gRPC error to the HTTP status and error code of the REST contract.
func writeError(w http.ResponseWriter, err error) {
	switch status.Code(err) {
	case codes.NotFound:
		apierr.Write(w, http.StatusNotFound, "not_found", status.Convert(err).Message())
	case codes.AlreadyExists:
		apierr.Write(w, http.StatusConflict, "conflict", status.Convert(err).Message())
	case codes.InvalidArgument:
		apierr.Write(w, http.StatusBadRequest, "bad_request", status.Convert(err).Message())
	case codes.Unauthenticated:
		apierr.Write(w, http.StatusUnauthorized, "unauthorized", status.Convert(err).Message())
	case codes.ResourceExhausted:
		// Never echo the gRPC message: it can be a transport error such as "received message larger than max".
		apierr.Write(w, http.StatusRequestEntityTooLarge, "payload_too_large", "payload is too large")
	case codes.Unavailable, codes.DeadlineExceeded:
		apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "a backing service is unavailable")
	default:
		apierr.Write(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
