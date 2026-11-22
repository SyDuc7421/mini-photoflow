package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/emptypb"

	"photoshare/internal/apierr"
	"photoshare/microservices/pb/socialpb"
)

func (g *gateway) registerSocial(mux *http.ServeMux) {
	mux.HandleFunc("PUT /users/{id}/follow", g.userAction(func(ctx context.Context, id int64) (*emptypb.Empty, error) {
		return g.social.Follow(ctx, &socialpb.UserRef{Id: id})
	}))
	mux.HandleFunc("DELETE /users/{id}/follow", g.userAction(func(ctx context.Context, id int64) (*emptypb.Empty, error) {
		return g.social.Unfollow(ctx, &socialpb.UserRef{Id: id})
	}))
	mux.HandleFunc("PUT /posts/{id}/like", g.postAction(func(ctx context.Context, id int64) (*emptypb.Empty, error) {
		return g.social.Like(ctx, &socialpb.PostRef{Id: id})
	}))
	mux.HandleFunc("DELETE /posts/{id}/like", g.postAction(func(ctx context.Context, id int64) (*emptypb.Empty, error) {
		return g.social.Unlike(ctx, &socialpb.PostRef{Id: id})
	}))

	mux.HandleFunc("POST /posts/{id}/comments", g.require(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ int64) {
		pid, ok := pathID(w, r, "post not found")
		if !ok {
			return
		}
		// Read the raw body: json.Decoder would silently repair invalid UTF-8 and ignore trailing data.
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
		var in struct {
			Text string `json:"text"`
		}
		if err != nil || !utf8.Valid(raw) || json.Unmarshal(raw, &in) != nil {
			apierr.Write(w, http.StatusBadRequest, "bad_request", "text is required")
			return
		}
		ctx, cancel := withTimeout(ctx)
		defer cancel()
		c, err := g.social.AddComment(ctx, &socialpb.AddCommentRequest{PostId: pid, Text: in.Text})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, commentJSON(c))
	}))

	mux.HandleFunc("GET /posts/{id}/comments", g.require(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ int64) {
		pid, ok := pathID(w, r, "post not found")
		if !ok {
			return
		}
		ctx, cancel := withTimeout(ctx)
		defer cancel()
		list, err := g.social.ListComments(ctx, &socialpb.PostRef{Id: pid})
		if err != nil {
			writeError(w, err)
			return
		}
		items := make([]map[string]any, 0, len(list.Comments))
		for _, c := range list.Comments {
			items = append(items, commentJSON(c))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}))
}

// userAction and postAction build the 204 handlers that only differ in the gRPC call.
func (g *gateway) userAction(call func(ctx context.Context, id int64) (*emptypb.Empty, error)) http.HandlerFunc {
	return g.empty("user not found", call)
}

func (g *gateway) postAction(call func(ctx context.Context, id int64) (*emptypb.Empty, error)) http.HandlerFunc {
	return g.empty("post not found", call)
}

func (g *gateway) empty(notFound string, call func(ctx context.Context, id int64) (*emptypb.Empty, error)) http.HandlerFunc {
	return g.require(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ int64) {
		id, ok := pathID(w, r, notFound)
		if !ok {
			return
		}
		ctx, cancel := withTimeout(ctx)
		defer cancel()
		if _, err := call(ctx, id); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// pathID parses {id}; anything that is not a positive integer names nothing, so it is a 404.
func pathID(w http.ResponseWriter, r *http.Request, notFound string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		apierr.Write(w, http.StatusNotFound, "not_found", notFound)
		return 0, false
	}
	return id, true
}

func commentJSON(c *socialpb.Comment) map[string]any {
	return map[string]any{
		"id":         c.Id,
		"post_id":    c.PostId,
		"author":     c.Author,
		"text":       c.Text,
		"created_at": time.UnixMicro(c.CreatedAtMicro).UTC(),
	}
}
