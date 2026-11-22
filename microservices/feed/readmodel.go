package main

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"photoshare/internal/cursor"
	"photoshare/internal/events"
)

// Event is one message from NATS.
type Event struct {
	Subject string
	Data    []byte
}

// Item is one feed entry. ImageKey is signed into a URL by the caller.
type Item struct {
	ID             int64
	Author         string
	Caption        string
	ImageKey       string
	Likes          int32
	Comments       int32
	LikedByMe      bool
	CreatedAtMicro int64
}

// dbtx is what the read model needs; a pool and a transaction both satisfy it.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ReadModel is the feed's own database, kept up to date by events.
type ReadModel struct{ db dbtx }

// Apply stores one event. Applying the same event again, or events in a different order,
// leaves the same state: every write is a set operation keyed by the event's ids.
func (m *ReadModel) Apply(ctx context.Context, ev Event) error {
	switch ev.Subject {
	case events.SubjectPostCreated:
		var e events.PostCreated
		if err := events.Decode(ev.Data, &e); err != nil {
			return err
		}
		// A post outside these bounds would be hidden forever by the cursor, or sign a bucket listing URL.
		if e.PostID <= 0 || e.ImageKey == "" || e.CreatedAtMicro <= 0 || e.CreatedAtMicro > cursor.MaxMicro {
			return fmt.Errorf("invalid post.created payload for post %d", e.PostID)
		}
		_, err := m.db.Exec(ctx, `
			INSERT INTO posts (post_id, author_id, author_name, caption, image_key, created_at)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (post_id) DO NOTHING`,
			e.PostID, e.AuthorID, e.AuthorName, e.Caption, e.ImageKey, time.UnixMicro(e.CreatedAtMicro).UTC())
		return err
	case events.SubjectFollowSet:
		var e events.FollowSet
		if err := events.Decode(ev.Data, &e); err != nil {
			return err
		}
		if e.Following {
			_, err := m.db.Exec(ctx,
				`INSERT INTO follows (follower_id, followee_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, e.FollowerID, e.FolloweeID)
			return err
		}
		_, err := m.db.Exec(ctx, `DELETE FROM follows WHERE follower_id = $1 AND followee_id = $2`, e.FollowerID, e.FolloweeID)
		return err
	case events.SubjectLikeSet:
		var e events.LikeSet
		if err := events.Decode(ev.Data, &e); err != nil {
			return err
		}
		if e.Liked {
			_, err := m.db.Exec(ctx,
				`INSERT INTO likes (post_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, e.PostID, e.UserID)
			return err
		}
		_, err := m.db.Exec(ctx, `DELETE FROM likes WHERE post_id = $1 AND user_id = $2`, e.PostID, e.UserID)
		return err
	case events.SubjectCommentAdded:
		var e events.CommentAdded
		if err := events.Decode(ev.Data, &e); err != nil {
			return err
		}
		_, err := m.db.Exec(ctx,
			`INSERT INTO comments (post_id, comment_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, e.PostID, e.CommentID)
		return err
	}
	return fmt.Errorf("unknown subject %q", ev.Subject)
}

// Feed returns up to limit posts of the users userID follows, newest first, strictly after the
// cursor `before` (empty = first page), and the cursor of the next page ("" on the last page).
// The tuple comparison keeps paging exact when created_at ties. It reads only the feed's own tables.
func (m *ReadModel) Feed(ctx context.Context, userID int64, before string, limit int) ([]Item, string, error) {
	if limit < 1 {
		return nil, "", fmt.Errorf("limit must be positive")
	}
	c := cursor.Cursor{Micro: cursor.MaxMicro, ID: math.MaxInt64}
	if before != "" {
		var ok bool
		if c, ok = cursor.Parse(before); !ok {
			return nil, "", fmt.Errorf("invalid cursor %q", before)
		}
	}
	rows, err := m.db.Query(ctx, `
		SELECT p.post_id, p.author_name, p.caption, p.image_key, p.created_at,
		       (SELECT count(*) FROM likes l WHERE l.post_id = p.post_id),
		       (SELECT count(*) FROM comments c WHERE c.post_id = p.post_id),
		       EXISTS (SELECT 1 FROM likes l WHERE l.post_id = p.post_id AND l.user_id = $1)
		FROM follows f
		JOIN posts p ON p.author_id = f.followee_id
		WHERE f.follower_id = $1 AND (p.created_at, p.post_id) < ($2::timestamptz, $3::bigint)
		ORDER BY p.created_at DESC, p.post_id DESC
		LIMIT $4`, userID, time.UnixMicro(c.Micro).UTC(), c.ID, limit+1) // one extra row: is there a next page?
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		var it Item
		var created time.Time
		if err := rows.Scan(&it.ID, &it.Author, &it.Caption, &it.ImageKey, &created, &it.Likes, &it.Comments, &it.LikedByMe); err != nil {
			return nil, "", err
		}
		it.CreatedAtMicro = created.UnixMicro()
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next = cursor.Cursor{Micro: last.CreatedAtMicro, ID: last.ID}.String()
	}
	return items, next, nil
}
