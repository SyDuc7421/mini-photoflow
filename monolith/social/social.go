// Package social handles follows, likes and comments.
package social

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"photoshare/internal/apierr"
	"photoshare/monolith/auth"
	"photoshare/monolith/post"
)

const maxCommentBody = 64 << 10

type comment struct {
	ID        int64     `json:"id"`
	PostID    int64     `json:"post_id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// Register mounts follow, like and comment routes. All require a Bearer token.
func Register(mux *http.ServeMux, db *pgxpool.Pool, secret []byte) {
	mux.HandleFunc("PUT /users/{id}/follow", auth.Require(secret, func(w http.ResponseWriter, r *http.Request, me int64) {
		target, ok := userTarget(w, r, db, me)
		if !ok {
			return
		}
		_, err := db.Exec(r.Context(),
			`INSERT INTO follows (follower_id, followee_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, me, target)
		done(w, err)
	}))

	mux.HandleFunc("DELETE /users/{id}/follow", auth.Require(secret, func(w http.ResponseWriter, r *http.Request, me int64) {
		target, ok := userTarget(w, r, db, 0)
		if !ok {
			return
		}
		_, err := db.Exec(r.Context(),
			`DELETE FROM follows WHERE follower_id = $1 AND followee_id = $2`, me, target)
		done(w, err)
	}))

	mux.HandleFunc("PUT /posts/{id}/like", auth.Require(secret, func(w http.ResponseWriter, r *http.Request, me int64) {
		pid, ok := postTarget(w, r, db)
		if !ok {
			return
		}
		_, err := db.Exec(r.Context(),
			`INSERT INTO likes (post_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, pid, me)
		done(w, err)
	}))

	mux.HandleFunc("DELETE /posts/{id}/like", auth.Require(secret, func(w http.ResponseWriter, r *http.Request, me int64) {
		pid, ok := postTarget(w, r, db)
		if !ok {
			return
		}
		_, err := db.Exec(r.Context(), `DELETE FROM likes WHERE post_id = $1 AND user_id = $2`, pid, me)
		done(w, err)
	}))

	mux.HandleFunc("POST /posts/{id}/comments", auth.Require(secret, func(w http.ResponseWriter, r *http.Request, me int64) {
		pid, ok := postTarget(w, r, db)
		if !ok {
			return
		}
		var in struct {
			Text string `json:"text"`
		}
		// Read the raw body: json.Decoder would silently repair invalid UTF-8 and ignore trailing data.
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCommentBody))
		if err != nil || !utf8.Valid(raw) || json.Unmarshal(raw, &in) != nil ||
			strings.TrimSpace(in.Text) == "" || strings.ContainsRune(in.Text, 0) {
			apierr.Write(w, http.StatusBadRequest, "bad_request", "text is required")
			return
		}
		names, err := auth.Names(r.Context(), db, []int64{me})
		if err != nil {
			unavailable(w)
			return
		}
		c := comment{PostID: pid, Author: names[me], Text: in.Text}
		err = db.QueryRow(r.Context(),
			`INSERT INTO comments (post_id, user_id, text) VALUES ($1, $2, $3) RETURNING id, created_at`,
			pid, me, in.Text).Scan(&c.ID, &c.CreatedAt)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, c)
	}))

	mux.HandleFunc("GET /posts/{id}/comments", auth.Require(secret, func(w http.ResponseWriter, r *http.Request, _ int64) {
		pid, ok := postTarget(w, r, db)
		if !ok {
			return
		}
		rows, err := db.Query(r.Context(),
			`SELECT c.id, u.username, c.text, c.created_at
			 FROM comments c JOIN users u ON u.id = c.user_id
			 WHERE c.post_id = $1 ORDER BY c.created_at, c.id`, pid)
		if err != nil {
			unavailable(w)
			return
		}
		defer rows.Close()
		items := []comment{}
		for rows.Next() {
			c := comment{PostID: pid}
			if err := rows.Scan(&c.ID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
				unavailable(w)
				return
			}
			items = append(items, c)
		}
		if rows.Err() != nil {
			unavailable(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}))
}

// userTarget parses {id}, rejects following yourself (selfID > 0) and checks the user exists.
func userTarget(w http.ResponseWriter, r *http.Request, db *pgxpool.Pool, selfID int64) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		apierr.Write(w, http.StatusNotFound, "not_found", "user not found")
		return 0, false
	}
	if id == selfID {
		apierr.Write(w, http.StatusBadRequest, "bad_request", "cannot follow yourself")
		return 0, false
	}
	ok, err := auth.UserExists(r.Context(), db, id)
	if err != nil {
		unavailable(w)
		return 0, false
	}
	if !ok {
		apierr.Write(w, http.StatusNotFound, "not_found", "user not found")
		return 0, false
	}
	return id, true
}

// postTarget parses {id} and checks the post exists.
func postTarget(w http.ResponseWriter, r *http.Request, db *pgxpool.Pool) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		apierr.Write(w, http.StatusNotFound, "not_found", "post not found")
		return 0, false
	}
	ok, err := post.Exists(r.Context(), db, id)
	if err != nil {
		unavailable(w)
		return 0, false
	}
	if !ok {
		apierr.Write(w, http.StatusNotFound, "not_found", "post not found")
		return 0, false
	}
	return id, true
}

func done(w http.ResponseWriter, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fail maps a write error. Targets are checked beforehand, so a foreign-key
// violation means the token's user row is gone (e.g. database was wiped).
func fail(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		apierr.Write(w, http.StatusUnauthorized, "unauthorized", "user no longer exists")
		return
	}
	unavailable(w)
}

func unavailable(w http.ResponseWriter) {
	apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "database error")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
