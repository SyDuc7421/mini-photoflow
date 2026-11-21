package feed

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFeedPagingSameCreatedAt inserts posts sharing one created_at and walks them
// with limit=2: no post may repeat or be skipped. Needs TEST_DATABASE_URL (with the
// monolith compose stack: postgres://photo:photo@localhost:55432/photo); all writes are rolled back.
func TestFeedPagingSameCreatedAt(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	var viewer, author int64
	suffix := time.Now().UnixNano()
	for _, p := range []struct {
		dst  *int64
		name string
	}{{&viewer, "viewer"}, {&author, "author"}} {
		err := tx.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1, 'x') RETURNING id`,
			fmt.Sprintf("%s-%d", p.name, suffix)).Scan(p.dst)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO follows (follower_id, followee_id) VALUES ($1, $2)`, viewer, author); err != nil {
		t.Fatal(err)
	}

	same := time.Now().Truncate(time.Microsecond)
	var want []int64 // newest first = highest id first
	for i := 0; i < 5; i++ {
		var id int64
		err := tx.QueryRow(ctx,
			`INSERT INTO posts (user_id, caption, image_key, created_at) VALUES ($1, 'p', 'k', $2) RETURNING id`,
			author, same).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		want = append([]int64{id}, want...)
	}

	var got []int64
	var before *cursor
	for pages := 0; pages < 10; pages++ {
		items, err := page(ctx, tx, viewer, 3, before) // limit 2 + 1 lookahead
		if err != nil {
			t.Fatal(err)
		}
		more := len(items) > 2
		if more {
			items = items[:2]
		}
		for _, it := range items {
			got = append(got, it.ID)
		}
		if !more {
			break
		}
		last := items[len(items)-1]
		before = &cursor{last.CreatedAt.UnixMicro(), last.ID}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("paged ids = %v, want %v", got, want)
	}
}

func TestParseCursor(t *testing.T) {
	if c, ok := parseCursor("1700000000000000_42"); !ok || c.micro != 1700000000000000 || c.id != 42 {
		t.Fatalf("parseCursor valid = %+v %v", c, ok)
	}
	for _, s := range []string{"", "abc", "1_", "_1", "-1_2", "1_-2", "1_2_3", "+1_2", "99999999999999999999_1", "9223372036854775807_1", "253402300800000000_1"} {
		if _, ok := parseCursor(s); ok {
			t.Errorf("parseCursor(%q) accepted", s)
		}
	}
}
