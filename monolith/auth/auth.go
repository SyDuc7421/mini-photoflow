// Package auth handles registration, login and JWT-protected routes.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"photoshare/internal/apierr"
	"photoshare/internal/jwtutil"
)

const tokenTTL = 24 * time.Hour

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Register mounts POST /register and POST /login.
func Register(mux *http.ServeMux, db *pgxpool.Pool, secret []byte) {
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		c, ok := readCredentials(w, r)
		if !ok {
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, "bad_request", "password cannot be hashed")
			return
		}
		var id int64
		err = db.QueryRow(r.Context(),
			`INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id`,
			c.Username, string(hash)).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			apierr.Write(w, http.StatusConflict, "conflict", "username already taken")
			return
		}
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "database error")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id, "username": c.Username})
	})

	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		c, ok := readCredentials(w, r)
		if !ok {
			return
		}
		var id int64
		var hash string
		err := db.QueryRow(r.Context(),
			`SELECT id, password_hash FROM users WHERE username = $1`, c.Username).Scan(&id, &hash)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && bcrypt.CompareHashAndPassword([]byte(hash), []byte(c.Password)) != nil) {
			apierr.Write(w, http.StatusUnauthorized, "unauthorized", "wrong username or password")
			return
		}
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "database error")
			return
		}
		token, err := jwtutil.Sign(secret, id, tokenTTL)
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "cannot sign token")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"token": token})
	})
}

// Require wraps next so it only runs with a valid Bearer token.
func Require(secret []byte, next func(w http.ResponseWriter, r *http.Request, userID int64)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		token, found := strings.CutPrefix(h, "Bearer ")
		if !found || token == "" {
			apierr.Write(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		id, err := jwtutil.Verify(secret, token)
		if err != nil {
			apierr.Write(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
			return
		}
		next(w, r, id)
	}
}

// UserExists reports whether a user with this id exists.
func UserExists(ctx context.Context, db *pgxpool.Pool, id int64) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&ok)
	return ok, err
}

// Names maps user ids to usernames; unknown ids are absent from the result.
func Names(ctx context.Context, db *pgxpool.Pool, ids []int64) (map[int64]string, error) {
	rows, err := db.Query(ctx, `SELECT id, username FROM users WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]string, len(ids))
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func readCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var c credentials
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.Username == "" || c.Password == "" ||
		strings.ContainsRune(c.Username, 0) || strings.ContainsRune(c.Password, 0) {
		apierr.Write(w, http.StatusBadRequest, "bad_request", "username and password are required")
		return c, false
	}
	return c, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
