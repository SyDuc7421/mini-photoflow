// Package events defines the NATS events the post and social services publish and the
// feed service consumes. Delivery is plain NATS pub/sub: at most once, no replay.
package events

import (
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	SubjectPostCreated  = "post.created"
	SubjectFollowSet    = "follow.set"
	SubjectLikeSet      = "like.set"
	SubjectCommentAdded = "comment.added"
)

// PostCreated carries the image key, not a URL: signed URLs expire, so the consumer signs on read.
// CreatedAtMicro keeps the exact Postgres timestamp for cursor paging.
type PostCreated struct {
	PostID         int64  `json:"post_id"`
	AuthorID       int64  `json:"author_id"`
	AuthorName     string `json:"author_name"`
	Caption        string `json:"caption"`
	ImageKey       string `json:"image_key"`
	CreatedAtMicro int64  `json:"created_at_micro"`
}

// FollowSet states the new state of one follow edge, so replaying it is harmless.
type FollowSet struct {
	FollowerID int64 `json:"follower_id"`
	FolloweeID int64 `json:"followee_id"`
	Following  bool  `json:"following"`
}

// LikeSet states the new state of one like, so replaying it is harmless.
type LikeSet struct {
	PostID int64 `json:"post_id"`
	UserID int64 `json:"user_id"`
	Liked  bool  `json:"liked"`
}

// CommentAdded carries the comment id so a consumer can ignore a duplicate delivery.
type CommentAdded struct {
	PostID    int64 `json:"post_id"`
	CommentID int64 `json:"comment_id"`
}

// Publish encodes v as JSON and publishes it. Callers log a failure and carry on:
// a lost event must never fail the request that caused it.
func Publish(nc *nats.Conn, subject string, v any) error {
	if nc == nil {
		return errors.New("events: no NATS connection")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return nc.Publish(subject, b)
}

// Decode parses an event payload into v.
func Decode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// Connect returns at once even when NATS is not up yet and keeps reconnecting forever,
// so a service can start before the broker and survive its restarts.
func Connect(url string) (*nats.Conn, error) {
	return nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
		// While disconnected, Publish only buffers (up to 8 MB) and returns nil; make that visible.
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { log.Printf("events: disconnected from NATS: %v", err) }),
		nats.ReconnectHandler(func(c *nats.Conn) { log.Printf("events: reconnected to %s", c.ConnectedUrl()) }),
		// A frozen broker must not hang publishing handlers for the default minute.
		nats.FlusherTimeout(time.Second),
		nats.PingInterval(10*time.Second),
	)
}
