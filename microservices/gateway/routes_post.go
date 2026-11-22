package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"photoshare/internal/apierr"
	"photoshare/microservices/pb/postpb"
)

const (
	maxCaptionChars = 500
	// 5 MiB image plus room for multipart boundaries and the caption field.
	maxUploadBody = 5<<20 + 1<<20
	chunkSize     = 32 << 10
	uploadTimeout = 2 * time.Minute
)

func (g *gateway) registerPosts(mux *http.ServeMux) {
	mux.HandleFunc("POST /posts", g.require(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ int64) {
		g.createPost(w, r, ctx)
	}))

	mux.HandleFunc("GET /posts/{id}", g.require(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ int64) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			apierr.Write(w, http.StatusNotFound, "not_found", "post not found")
			return
		}
		ctx, cancel := withTimeout(ctx)
		defer cancel()
		p, err := g.post.GetPost(ctx, &postpb.PostRef{Id: id})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, postJSON(p))
	}))
}

// createPost streams the multipart image to the post service chunk by chunk, so the gateway
// never holds the whole image. The post service enforces the 5 MiB limit and stores nothing
// when it is exceeded.
func (g *gateway) createPost(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	mr, err := r.MultipartReader()
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, "bad_request", "multipart/form-data body required")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel() // cancelling an unfinished stream makes the service store nothing
	stream, err := g.post.CreatePost(ctx)
	if err != nil {
		writeError(w, err)
		return
	}

	buf := make([]byte, chunkSize)
	var seenCaption, seenImage bool
parts:
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			uploadError(w, err)
			return
		}
		switch part.FormName() {
		case "caption":
			if seenCaption {
				apierr.Write(w, http.StatusBadRequest, "bad_request", "caption sent twice")
				return
			}
			seenCaption = true
			// 500 characters are at most 4 bytes each.
			b, err := io.ReadAll(io.LimitReader(part, maxCaptionChars*4+1))
			if err != nil {
				uploadError(w, err)
				return
			}
			if len(b) > maxCaptionChars*4 {
				apierr.Write(w, http.StatusBadRequest, "bad_request", "caption is longer than 500 characters")
				return
			}
			caption := string(b)
			if !utf8.ValidString(caption) || strings.ContainsRune(caption, 0) {
				apierr.Write(w, http.StatusBadRequest, "bad_request", "caption must be valid UTF-8 text")
				return
			}
			if utf8.RuneCountInString(caption) > maxCaptionChars {
				apierr.Write(w, http.StatusBadRequest, "bad_request", "caption is longer than 500 characters")
				return
			}
			if err := stream.Send(&postpb.CreatePostRequest{Part: &postpb.CreatePostRequest_Meta{Meta: &postpb.Meta{Caption: caption}}}); err != nil {
				break parts // the service already failed; CloseAndRecv returns its status
			}
		case "image":
			if seenImage {
				apierr.Write(w, http.StatusBadRequest, "bad_request", "image sent twice")
				return
			}
			seenImage = true
			for {
				n, rerr := part.Read(buf)
				if n > 0 {
					if err := stream.Send(&postpb.CreatePostRequest{Part: &postpb.CreatePostRequest_Chunk{Chunk: buf[:n]}}); err != nil {
						break parts
					}
				}
				if rerr == io.EOF {
					break
				}
				if rerr != nil {
					uploadError(w, rerr)
					return
				}
			}
		}
	}

	p, err := stream.CloseAndRecv()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, postJSON(p))
}

// uploadError reports a failure while reading the client's multipart body.
func uploadError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		apierr.Write(w, http.StatusRequestEntityTooLarge, "payload_too_large", "image or request body is too large (image limit 5MB)")
		return
	}
	apierr.Write(w, http.StatusBadRequest, "bad_request", "malformed multipart body")
}

func postJSON(p *postpb.PostReply) map[string]any {
	return map[string]any{
		"id":         p.Id,
		"author":     p.Author,
		"caption":    p.Caption,
		"image_url":  p.ImageUrl,
		"created_at": time.UnixMicro(p.CreatedAtMicro).UTC(),
	}
}
