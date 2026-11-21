// Package feed serves the home feed: posts of followed users, newest first.
package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"

	"photoshare/internal/apierr"
	"photoshare/monolith/auth"
	"photoshare/monolith/post"
)

const (
	defaultLimit = 20
	maxLimit     = 50
	// maxMicro is 9999-12-31T23:59:59.999999Z; later cursors are rejected so
	// Postgres never sees a timestamp it cannot represent.
	maxMicro = 253402300799999999
)

type item struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	Caption   string    `json:"caption"`
	ImageURL  string    `json:"image_url"`
	Likes     int       `json:"likes"`
	Comments  int       `json:"comments"`
	LikedByMe bool      `json:"liked_by_me"`
	CreatedAt time.Time `json:"created_at"`

	imageKey string
}

// cursor is the position of the last post of a page: "<created_at_unix_micro>_<id>".
type cursor struct {
	micro int64
	id    int64
}

func (c cursor) String() string { return fmt.Sprintf("%d_%d", c.micro, c.id) }

func parseCursor(s string) (cursor, bool) {
	a, b, ok := strings.Cut(s, "_")
	if !ok || !digits(a) || !digits(b) {
		return cursor{}, false
	}
	micro, err1 := strconv.ParseInt(a, 10, 64)
	id, err2 := strconv.ParseInt(b, 10, 64)
	return cursor{micro, id}, err1 == nil && err2 == nil && micro <= maxMicro
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Register mounts GET /feed. public only signs image URLs.
func Register(mux *http.ServeMux, db *pgxpool.Pool, secret []byte, public *minio.Client, bucket string) {
	mux.HandleFunc("GET /feed", auth.Require(secret, func(w http.ResponseWriter, r *http.Request, me int64) {
		limit, before, ok := parseQuery(w, r)
		if !ok {
			return
		}
		items, err := page(r.Context(), db, me, limit+1, before)
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "database error")
			return
		}
		var next *string
		if len(items) > limit {
			items = items[:limit]
			last := items[limit-1]
			s := cursor{last.CreatedAt.UnixMicro(), last.ID}.String()
			next = &s
		}
		for i := range items {
			u, err := post.ImageURL(r.Context(), public, bucket, items[i].imageKey)
			if err != nil {
				apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "cannot sign image url")
				return
			}
			items[i].ImageURL = u
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "next": next})
	}))
}

// parseQuery validates limit (1..50, larger values are clamped) and before.
func parseQuery(w http.ResponseWriter, r *http.Request) (limit int, before *cursor, ok bool) {
	q := r.URL.Query()
	limit = defaultLimit
	if s := q.Get("limit"); q.Has("limit") {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			apierr.Write(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return 0, nil, false
		}
		limit = min(n, maxLimit)
	}
	if s := q.Get("before"); q.Has("before") {
		c, valid := parseCursor(s)
		if !valid {
			apierr.Write(w, http.StatusBadRequest, "bad_request", "before is not a valid cursor")
			return 0, nil, false
		}
		before = &c
	}
	return limit, before, true
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// page returns up to n posts of users that viewer follows, ordered by (created_at, id) descending,
// strictly after the cursor. The tuple comparison keeps paging exact when created_at ties.
func page(ctx context.Context, db querier, viewer int64, n int, before *cursor) ([]item, error) {
	// Always filter by a cursor (the end of time when absent) so the planner can use the
	// index condition even with a cached generic plan.
	c := cursor{maxMicro, math.MaxInt64}
	if before != nil {
		c = *before
	}
	rows, err := db.Query(ctx, `
		SELECT p.id, u.username, p.caption, p.image_key, p.created_at,
		       (SELECT count(*) FROM likes l WHERE l.post_id = p.id),
		       (SELECT count(*) FROM comments c WHERE c.post_id = p.id),
		       EXISTS (SELECT 1 FROM likes l WHERE l.post_id = p.id AND l.user_id = $1)
		FROM posts p
		JOIN follows f ON f.followee_id = p.user_id AND f.follower_id = $1
		JOIN users u ON u.id = p.user_id
		WHERE (p.created_at, p.id) < ($2::timestamptz, $3::bigint)
		ORDER BY p.created_at DESC, p.id DESC
		LIMIT $4`, viewer, time.UnixMicro(c.micro).UTC(), c.id, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Author, &it.Caption, &it.imageKey, &it.CreatedAt,
			&it.Likes, &it.Comments, &it.LikedByMe); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}
