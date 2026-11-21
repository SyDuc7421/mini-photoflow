// Package post handles image posts: metadata in Postgres, image bytes in MinIO.
package post

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"

	"photoshare/internal/apierr"
	"photoshare/monolith/auth"
)

const (
	maxImageBytes   = 5 << 20
	maxCaptionChars = 500
	// Room for multipart boundaries and the caption field on top of the image.
	maxBodyBytes = maxImageBytes + 1<<20
	urlTTL       = time.Hour
)

var errTooLarge = errors.New("image too large")

type response struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	Caption   string    `json:"caption"`
	ImageURL  string    `json:"image_url"`
	CreatedAt time.Time `json:"created_at"`
}

// Register mounts POST /posts and GET /posts/{id}.
// store uploads objects over the internal network; public only signs URLs with
// the host name clients can reach, so no request is ever sent through it.
func Register(mux *http.ServeMux, db *pgxpool.Pool, secret []byte, store, public *minio.Client, bucket string) {
	mux.HandleFunc("POST /posts", auth.Require(secret, func(w http.ResponseWriter, r *http.Request, userID int64) {
		caption, image, ok := readUpload(w, r)
		if !ok {
			return
		}
		ctype := mimeOf(image)
		if ctype != "image/jpeg" && ctype != "image/png" {
			apierr.Write(w, http.StatusBadRequest, "bad_request", "image must be JPEG or PNG")
			return
		}

		// Look the author up before storing anything so no failure follows the INSERT.
		names, err := auth.Names(r.Context(), db, []int64{userID})
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "database error")
			return
		}
		key, err := newKey(userID, ctype)
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "cannot create image key")
			return
		}
		_, err = store.PutObject(r.Context(), bucket, key, bytes.NewReader(image), int64(len(image)),
			minio.PutObjectOptions{ContentType: ctype})
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "image storage error")
			return
		}

		var p response
		p.Caption = caption
		err = db.QueryRow(r.Context(),
			`INSERT INTO posts (user_id, caption, image_key) VALUES ($1, $2, $3) RETURNING id, created_at`,
			userID, caption, key).Scan(&p.ID, &p.CreatedAt)
		if err != nil {
			// Do not leave an orphan image behind; use a fresh context in case the request was cancelled.
			if rerr := store.RemoveObject(context.Background(), bucket, key, minio.RemoveObjectOptions{}); rerr != nil {
				log.Printf("post: orphan object %s left in %s: %v", key, bucket, rerr)
			}
			apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "database error")
			return
		}
		u, err := public.PresignedGetObject(r.Context(), bucket, key, urlTTL, nil)
		if err != nil {
			// The post is saved; signing is offline so this should not happen. Still return the post.
			log.Printf("post: presign %s: %v", key, err)
		} else {
			p.ImageURL = u.String()
		}
		p.Author = names[userID]
		writeJSON(w, http.StatusCreated, p)
	}))

	mux.HandleFunc("GET /posts/{id}", auth.Require(secret, func(w http.ResponseWriter, r *http.Request, _ int64) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			apierr.Write(w, http.StatusNotFound, "not_found", "post not found")
			return
		}
		var p response
		var userID int64
		var key string
		err = db.QueryRow(r.Context(),
			`SELECT id, user_id, caption, image_key, created_at FROM posts WHERE id = $1`, id).
			Scan(&p.ID, &userID, &p.Caption, &key, &p.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			apierr.Write(w, http.StatusNotFound, "not_found", "post not found")
			return
		}
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "database error")
			return
		}
		if !fill(r.Context(), db, public, bucket, &p, userID, key) {
			apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "cannot build response")
			return
		}
		writeJSON(w, http.StatusOK, p)
	}))
}

// Exists reports whether a post with this id exists.
func Exists(ctx context.Context, db *pgxpool.Pool, id int64) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts WHERE id = $1)`, id).Scan(&ok)
	return ok, err
}

// readUpload parses the multipart body, enforcing every limit before anything is stored.
func readUpload(w http.ResponseWriter, r *http.Request) (caption string, image []byte, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, "bad_request", "multipart/form-data body required")
		return "", nil, false
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, failRead(w, err)
		}
		switch part.FormName() {
		case "caption":
			// A caption of 500 characters is at most 4 bytes each.
			b, err := io.ReadAll(io.LimitReader(part, maxCaptionChars*4+1))
			if err != nil {
				return "", nil, failRead(w, err)
			}
			caption = string(b)
		case "image":
			image, err = readImage(part)
			if err != nil {
				return "", nil, failRead(w, err)
			}
		}
	}
	if !utf8.ValidString(caption) || strings.ContainsRune(caption, 0) {
		apierr.Write(w, http.StatusBadRequest, "bad_request", "caption must be valid UTF-8 text")
		return "", nil, false
	}
	if utf8.RuneCountInString(caption) > maxCaptionChars {
		apierr.Write(w, http.StatusBadRequest, "bad_request", "caption is longer than 500 characters")
		return "", nil, false
	}
	if len(image) == 0 {
		apierr.Write(w, http.StatusBadRequest, "bad_request", "image is required")
		return "", nil, false
	}
	return caption, image, true
}

func readImage(part *multipart.Part) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(part, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxImageBytes {
		return nil, errTooLarge
	}
	return b, nil
}

func failRead(w http.ResponseWriter, err error) bool {
	var tooBig *http.MaxBytesError
	if errors.Is(err, errTooLarge) || errors.As(err, &tooBig) {
		apierr.Write(w, http.StatusRequestEntityTooLarge, "payload_too_large", "image or request body is too large (image limit 5MB)")
		return false
	}
	apierr.Write(w, http.StatusBadRequest, "bad_request", "malformed multipart body")
	return false
}

// fill adds the author name and a presigned image URL.
func fill(ctx context.Context, db *pgxpool.Pool, public *minio.Client, bucket string, p *response, userID int64, key string) bool {
	names, err := auth.Names(ctx, db, []int64{userID})
	if err != nil {
		return false
	}
	u, err := public.PresignedGetObject(ctx, bucket, key, urlTTL, nil)
	if err != nil {
		return false
	}
	p.Author, p.ImageURL = names[userID], u.String()
	return true
}

func mimeOf(b []byte) string {
	return http.DetectContentType(b)
}

func newKey(userID int64, ctype string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	ext := ".png"
	if ctype == "image/jpeg" {
		ext = ".jpg"
	}
	return fmt.Sprintf("%d/%s%s", userID, hex.EncodeToString(raw[:]), ext), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
