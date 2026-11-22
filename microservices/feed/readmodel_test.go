package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"photoshare/internal/events"
)

// newModel opens a rolled-back transaction on TEST_DATABASE_URL (the feed database; with the
// compose stack: postgres://feed:feed@localhost:55433/feed). Tests skip without it.
func newModel(t *testing.T) (*ReadModel, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	return &ReadModel{db: tx}, ctx
}

func apply(t *testing.T, m *ReadModel, ctx context.Context, subject string, payload any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(ctx, Event{Subject: subject, Data: b}); err != nil {
		t.Fatalf("Apply %s: %v", subject, err)
	}
}

// ids that cannot collide with real rows in a shared dev database
func base() int64 { return time.Now().UnixNano() % 1_000_000_000_000 * 1000 }

func post(m *ReadModel, ctx context.Context, t *testing.T, id, author int64, micro int64) {
	apply(t, m, ctx, events.SubjectPostCreated, events.PostCreated{
		PostID: id, AuthorID: author, AuthorName: fmt.Sprintf("u%d", author), Caption: "c", ImageKey: "k", CreatedAtMicro: micro,
	})
}

func feed(t *testing.T, m *ReadModel, ctx context.Context, viewer int64, before string, limit int) ([]Item, string) {
	t.Helper()
	items, next, err := m.Feed(ctx, viewer, before, limit)
	if err != nil {
		t.Fatal(err)
	}
	return items, next
}

func TestApplyIsIdempotent(t *testing.T) {
	m, ctx := newModel(t)
	b := base()
	viewer, author, pid := b+1, b+2, b+3
	for i := 0; i < 2; i++ { // every event twice
		apply(t, m, ctx, events.SubjectFollowSet, events.FollowSet{FollowerID: viewer, FolloweeID: author, Following: true})
		post(m, ctx, t, pid, author, 1_700_000_000_000_000)
		apply(t, m, ctx, events.SubjectLikeSet, events.LikeSet{PostID: pid, UserID: viewer, Liked: true})
		apply(t, m, ctx, events.SubjectCommentAdded, events.CommentAdded{PostID: pid, CommentID: b + 9})
	}
	items, next := feed(t, m, ctx, viewer, "", 20)
	if len(items) != 1 || items[0].Likes != 1 || items[0].Comments != 1 || !items[0].LikedByMe || next != "" {
		t.Fatalf("items = %+v next=%q, want one post with 1 like, 1 comment, liked_by_me", items, next)
	}
	for i := 0; i < 2; i++ {
		apply(t, m, ctx, events.SubjectLikeSet, events.LikeSet{PostID: pid, UserID: viewer, Liked: false})
		apply(t, m, ctx, events.SubjectFollowSet, events.FollowSet{FollowerID: viewer, FolloweeID: author, Following: false})
	}
	if items, _ := feed(t, m, ctx, viewer, "", 20); len(items) != 0 {
		t.Fatalf("after unfollow items = %+v, want none", items)
	}
}

func TestCountsAreDistinctAndPerViewer(t *testing.T) {
	m, ctx := newModel(t)
	b := base()
	viewer, other, author, pid := b+1, b+2, b+3, b+4
	post(m, ctx, t, pid, author, 1_700_000_000_000_000)
	apply(t, m, ctx, events.SubjectFollowSet, events.FollowSet{FollowerID: viewer, FolloweeID: author, Following: true}) // follow after the post
	apply(t, m, ctx, events.SubjectLikeSet, events.LikeSet{PostID: pid, UserID: other, Liked: true})                     // someone else's like
	apply(t, m, ctx, events.SubjectCommentAdded, events.CommentAdded{PostID: pid, CommentID: b + 7})
	apply(t, m, ctx, events.SubjectCommentAdded, events.CommentAdded{PostID: pid, CommentID: b + 8})
	items, _ := feed(t, m, ctx, viewer, "", 20)
	if len(items) != 1 || items[0].Likes != 1 || items[0].LikedByMe || items[0].Comments != 2 {
		t.Fatalf("items = %+v, want likes=1 liked_by_me=false comments=2", items)
	}
}

func TestApplyRejectsInvalidPosts(t *testing.T) {
	m, ctx := newModel(t)
	for name, e := range map[string]events.PostCreated{
		"no image key":   {PostID: 1, ImageKey: "", CreatedAtMicro: 1},
		"bad post id":    {PostID: 0, ImageKey: "k", CreatedAtMicro: 1},
		"time too early": {PostID: 1, ImageKey: "k", CreatedAtMicro: 0},
		"time too late":  {PostID: 1, ImageKey: "k", CreatedAtMicro: 253402300800000000},
	} {
		b, _ := json.Marshal(e)
		if err := m.Apply(ctx, Event{Subject: events.SubjectPostCreated, Data: b}); err == nil {
			t.Errorf("%s: Apply accepted %+v", name, e)
		}
	}
}

func TestEventsMayArriveBeforeThePost(t *testing.T) {
	m, ctx := newModel(t)
	b := base()
	viewer, author, pid := b+1, b+2, b+3
	apply(t, m, ctx, events.SubjectLikeSet, events.LikeSet{PostID: pid, UserID: viewer, Liked: true}) // like first
	apply(t, m, ctx, events.SubjectFollowSet, events.FollowSet{FollowerID: viewer, FolloweeID: author, Following: true})
	post(m, ctx, t, pid, author, 1_700_000_000_000_000) // post last
	items, _ := feed(t, m, ctx, viewer, "", 20)
	if len(items) != 1 || items[0].Likes != 1 || !items[0].LikedByMe {
		t.Fatalf("items = %+v, want the early like to count", items)
	}
}

func TestFeedFromReadModelOrder(t *testing.T) {
	m, ctx := newModel(t)
	b := base()
	viewer, a1, a2, stranger := b+1, b+2, b+3, b+4
	for _, a := range []int64{a1, a2} {
		apply(t, m, ctx, events.SubjectFollowSet, events.FollowSet{FollowerID: viewer, FolloweeID: a, Following: true})
	}
	post(m, ctx, t, b+10, a1, 1_700_000_000_000_001)
	post(m, ctx, t, b+11, a2, 1_700_000_000_000_003)
	post(m, ctx, t, b+12, stranger, 1_700_000_000_000_004) // not followed
	post(m, ctx, t, b+13, a1, 1_700_000_000_000_002)
	items, _ := feed(t, m, ctx, viewer, "", 20)
	var got []int64
	for _, it := range items {
		got = append(got, it.ID-b)
	}
	if fmt.Sprint(got) != "[11 13 10]" {
		t.Fatalf("order = %v, want [11 13 10]", got)
	}
	if items[0].Author != fmt.Sprintf("u%d", a2) || items[0].ImageKey != "k" {
		t.Fatalf("item = %+v, want the stored author name and image key", items[0])
	}
}

func TestReadModelPagingSameCreatedAt(t *testing.T) {
	m, ctx := newModel(t)
	b := base()
	viewer, author := b+1, b+2
	apply(t, m, ctx, events.SubjectFollowSet, events.FollowSet{FollowerID: viewer, FolloweeID: author, Following: true})
	for i := int64(1); i <= 5; i++ {
		post(m, ctx, t, b+10+i, author, 1_700_000_000_000_000) // identical created_at
	}
	var got []int64
	before := ""
	for pages := 0; pages < 10; pages++ {
		items, next := feed(t, m, ctx, viewer, before, 2)
		for _, it := range items {
			got = append(got, it.ID-b)
		}
		if next == "" {
			break
		}
		before = next
	}
	if fmt.Sprint(got) != "[15 14 13 12 11]" {
		t.Fatalf("paged ids = %v, want [15 14 13 12 11]", got)
	}
}

func TestNextIsEmptyOnExactMultiple(t *testing.T) {
	m, ctx := newModel(t)
	b := base()
	viewer, author := b+1, b+2
	apply(t, m, ctx, events.SubjectFollowSet, events.FollowSet{FollowerID: viewer, FolloweeID: author, Following: true})
	for i := int64(1); i <= 4; i++ {
		post(m, ctx, t, b+10+i, author, 1_700_000_000_000_000+i)
	}
	items, next := feed(t, m, ctx, viewer, "", 2)
	if len(items) != 2 || next == "" {
		t.Fatalf("first page = %d items next=%q, want 2 items and a cursor", len(items), next)
	}
	items, next = feed(t, m, ctx, viewer, next, 2)
	if len(items) != 2 || next != "" {
		t.Fatalf("last page = %d items next=%q, want 2 items and no cursor", len(items), next)
	}
	if _, _, err := m.Feed(ctx, viewer, "", 0); err == nil {
		t.Fatal("Feed accepted limit 0")
	}
}

func TestFeedRejectsBadCursor(t *testing.T) {
	m, ctx := newModel(t)
	if _, _, err := m.Feed(ctx, 1, "nonsense", 20); err == nil {
		t.Fatal("Feed accepted a bad cursor")
	}
}
