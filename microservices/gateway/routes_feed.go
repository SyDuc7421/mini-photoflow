package main

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"photoshare/internal/apierr"
	"photoshare/internal/cursor"
	"photoshare/microservices/pb/feedpb"
)

const (
	defaultFeedLimit = 20
	maxFeedLimit     = 50
)

func (g *gateway) registerFeed(mux *http.ServeMux) {
	mux.HandleFunc("GET /feed", g.require(func(w http.ResponseWriter, r *http.Request, ctx context.Context, _ int64) {
		q := r.URL.Query()
		limit := defaultFeedLimit
		if q.Has("limit") {
			n, err := strconv.Atoi(q.Get("limit"))
			if err != nil || n < 1 {
				apierr.Write(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
				return
			}
			limit = min(n, maxFeedLimit)
		}
		before := q.Get("before")
		if q.Has("before") {
			if _, ok := cursor.Parse(before); !ok {
				apierr.Write(w, http.StatusBadRequest, "bad_request", "before is not a valid cursor")
				return
			}
		}
		ctx, cancel := withTimeout(ctx)
		defer cancel()
		reply, err := g.feed.GetFeed(ctx, &feedpb.GetFeedRequest{Before: before, Limit: int32(limit)})
		if err != nil {
			writeError(w, err)
			return
		}
		items := make([]map[string]any, 0, len(reply.Items))
		for _, it := range reply.Items {
			items = append(items, map[string]any{
				"id": it.Id, "author": it.Author, "caption": it.Caption, "image_url": it.ImageUrl,
				"likes": it.Likes, "comments": it.Comments, "liked_by_me": it.LikedByMe,
				"created_at": time.UnixMicro(it.CreatedAtMicro).UTC(),
			})
		}
		var next *string
		if reply.Next != "" {
			next = &reply.Next
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "next": next})
	}))
}
