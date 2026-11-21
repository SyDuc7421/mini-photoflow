package contract

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ---- auth ----

func TestRegisterDuplicate409(t *testing.T) {
	u := newUser(t)
	st, body := callJSON(t, "POST", "/register", "", map[string]string{"username": u.Name, "password": password})
	wantStatus(t, st, 409, body)
	wantErrCode(t, body, "conflict")
}

func TestLoginWrongPassword401(t *testing.T) {
	u := newUser(t)
	st, body := callJSON(t, "POST", "/login", "", map[string]string{"username": u.Name, "password": "wrong-password"})
	wantStatus(t, st, 401, body)
	wantErrCode(t, body, "unauthorized")
}

func TestNoTokenIs401(t *testing.T) {
	for _, r := range [][2]string{{"GET", "/feed"}, {"POST", "/posts"}, {"PUT", "/users/1/follow"}, {"PUT", "/posts/1/like"}} {
		st, body := call(t, r[0], r[1], "", "", nil)
		wantStatus(t, st, 401, body)
		wantErrCode(t, body, "unauthorized")
	}
	st, body := call(t, "GET", "/feed", "garbage.token.value", "", nil)
	wantStatus(t, st, 401, body)
}

// ---- posts ----

func TestPostRequiresImage(t *testing.T) {
	u := newUser(t)
	st, body := postRaw(t, u.Token, "no image here", nil, "")
	wantStatus(t, st, 400, body)
	wantErrCode(t, body, "bad_request")
}

func TestPostOver5MB413(t *testing.T) {
	u, follower := newUser(t), newUser(t)
	follow(t, follower, u.ID)
	big := append(pngBytes(), make([]byte, 5*1024*1024+1024)...)
	st, body := postRaw(t, u.Token, "too big", big, "image/png")
	wantStatus(t, st, 413, body)
	wantErrCode(t, body, "payload_too_large")

	time.Sleep(300 * time.Millisecond) // a late event would show a stored post
	p := feedPageOf(t, follower, "")
	if strings.TrimSpace(string(p.Items)) != "[]" {
		t.Fatalf("rejected upload left a post behind: %s", p.Items)
	}
}

func TestCaptionOver500Is400(t *testing.T) {
	u := newUser(t)
	st, body := postRaw(t, u.Token, strings.Repeat("x", 501), pngBytes(), "image/png")
	wantStatus(t, st, 400, body)
	wantErrCode(t, body, "bad_request")

	st, body = postRaw(t, u.Token, strings.Repeat("x", 500), pngBytes(), "image/png")
	wantStatus(t, st, 201, body)

	// the limit counts characters, not bytes
	st, body = postRaw(t, u.Token, strings.Repeat("ế", 500), pngBytes(), "image/png")
	wantStatus(t, st, 201, body)
	st, body = postRaw(t, u.Token, strings.Repeat("ế", 501), pngBytes(), "image/png")
	wantStatus(t, st, 400, body)
}

// ---- social ----

func TestFollowSelf400(t *testing.T) {
	u := newUser(t)
	st, body := call(t, "PUT", fmt.Sprintf("/users/%d/follow", u.ID), u.Token, "", nil)
	wantStatus(t, st, 400, body)
	wantErrCode(t, body, "bad_request")
}

func TestFollowMissingUser404(t *testing.T) {
	u := newUser(t)
	st, body := call(t, "PUT", "/users/999999999/follow", u.Token, "", nil)
	wantStatus(t, st, 404, body)
	wantErrCode(t, body, "not_found")
}

func TestFollowTwiceIdempotent(t *testing.T) {
	a, b := newUser(t), newUser(t)
	follow(t, a, b.ID)
	follow(t, a, b.ID)
	pid := createPost(t, b, "once")
	eventually(t, func() error {
		items, err := feedItemsE(a, "")
		if err != nil {
			return err
		}
		if len(items) != 1 || items[0].ID != pid {
			return fmt.Errorf("items = %+v, want exactly post %d once", items, pid)
		}
		return nil
	})
}

func TestUnfollowNotFollowingIs204(t *testing.T) {
	a, b := newUser(t), newUser(t)
	st, body := call(t, "DELETE", fmt.Sprintf("/users/%d/follow", b.ID), a.Token, "", nil)
	wantStatus(t, st, 204, body)
}

func TestLikeMissingPost404(t *testing.T) {
	u := newUser(t)
	st, body := call(t, "PUT", "/posts/999999999/like", u.Token, "", nil)
	wantStatus(t, st, 404, body)
	wantErrCode(t, body, "not_found")
}

func TestLikeTwiceCountsOnce(t *testing.T) {
	a, b := newUser(t), newUser(t)
	follow(t, a, b.ID)
	pid := createPost(t, b, "like me")
	like(t, a, pid)
	like(t, a, pid)

	likes := func() error {
		items, err := feedItemsE(a, "")
		if err != nil {
			return err
		}
		if len(items) != 1 || items[0].Likes != 1 {
			return fmt.Errorf("items = %+v, want one item with likes=1", items)
		}
		return nil
	}
	eventually(t, likes)
	time.Sleep(300 * time.Millisecond) // a double count would arrive late
	if err := likes(); err != nil {
		t.Fatal(err)
	}
}

func TestUnlikeIsIdempotent(t *testing.T) {
	a, b := newUser(t), newUser(t)
	follow(t, a, b.ID)
	pid := createPost(t, b, "unlike me")
	like(t, a, pid)
	for i := 0; i < 2; i++ {
		st, body := call(t, "DELETE", fmt.Sprintf("/posts/%d/like", pid), a.Token, "", nil)
		wantStatus(t, st, 204, body)
	}
	eventually(t, func() error {
		items, err := feedItemsE(a, "")
		if err != nil {
			return err
		}
		if len(items) != 1 || items[0].Likes != 0 || items[0].LikedByMe {
			return fmt.Errorf("items = %+v, want likes=0 liked_by_me=false", items)
		}
		return nil
	})
}

func TestLikedByMeIsPerViewer(t *testing.T) {
	a, b, c := newUser(t), newUser(t), newUser(t)
	follow(t, a, b.ID)
	follow(t, c, b.ID)
	pid := createPost(t, b, "per viewer")
	like(t, a, pid)
	eventually(t, func() error {
		ia, err := feedItemsE(a, "")
		if err != nil {
			return err
		}
		ic, err := feedItemsE(c, "")
		if err != nil {
			return err
		}
		if len(ia) != 1 || len(ic) != 1 || ia[0].Likes != 1 || ic[0].Likes != 1 || !ia[0].LikedByMe || ic[0].LikedByMe {
			return fmt.Errorf("a=%+v c=%+v, want likes=1 for both, liked_by_me a=true c=false", ia, ic)
		}
		return nil
	})
}

func TestCommentAndList(t *testing.T) {
	a, b := newUser(t), newUser(t)
	pid := createPost(t, b, "comment me")

	st, body := callJSON(t, "POST", fmt.Sprintf("/posts/%d/comments", pid), a.Token, map[string]string{"text": "hi"})
	wantStatus(t, st, 201, body)

	st, body = call(t, "GET", fmt.Sprintf("/posts/%d/comments", pid), b.Token, "", nil)
	wantStatus(t, st, 200, body)
	var list struct {
		Items []struct {
			Author string `json:"author"`
			Text   string `json:"text"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Text != "hi" || list.Items[0].Author != a.Name {
		t.Fatalf("comments = %s", body)
	}
}

// ---- feed ----

func TestFeedOnlyFollowedNewestFirst(t *testing.T) {
	a, b, c := newUser(t), newUser(t), newUser(t)
	follow(t, a, b.ID)
	p1 := createPost(t, b, "first")
	p2 := createPost(t, b, "second")
	createPost(t, c, "not followed")

	eventually(t, func() error {
		items, err := feedItemsE(a, "")
		if err != nil {
			return err
		}
		if len(items) != 2 || items[0].ID != p2 || items[1].ID != p1 {
			return fmt.Errorf("items = %+v, want ids [%d %d]", items, p2, p1)
		}
		return nil
	})
}

func TestFeedEmptyIsEmptyArray(t *testing.T) {
	u := newUser(t)
	p := feedPageOf(t, u, "")
	if strings.TrimSpace(string(p.Items)) != "[]" {
		t.Fatalf("items = %s, want []", p.Items)
	}
	if p.Next != nil {
		t.Fatalf("next = %q, want null", *p.Next)
	}
}

func TestFeedItemFields(t *testing.T) {
	a, b := newUser(t), newUser(t)
	follow(t, a, b.ID)
	pid := createPost(t, b, "fields")
	like(t, a, pid)
	st, body := callJSON(t, "POST", fmt.Sprintf("/posts/%d/comments", pid), a.Token, map[string]string{"text": "nice"})
	wantStatus(t, st, 201, body)

	eventually(t, func() error {
		items, err := feedItemsE(a, "")
		if err != nil {
			return err
		}
		if len(items) != 1 {
			return fmt.Errorf("items = %+v", items)
		}
		it := items[0]
		if it.ID != pid || it.Author != b.Name || it.Caption != "fields" || it.ImageURL == "" ||
			it.Likes != 1 || it.Comments != 1 || !it.LikedByMe || it.CreatedAt == "" {
			return fmt.Errorf("item = %+v", it)
		}
		return nil
	})
}

// Same-timestamp ties cannot be forced through the API, so this creates a burst of posts
// and walks the whole feed with small pages: no post may repeat or be skipped.
func TestFeedCursorNoDupesWithSameTimestamp(t *testing.T) {
	a, b := newUser(t), newUser(t)
	follow(t, a, b.ID)
	const n = 7
	want := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		want = append([]int64{createPost(t, b, fmt.Sprintf("p%d", i))}, want...) // newest first
	}

	eventually(t, func() error {
		items, err := feedItemsE(a, "limit=50")
		if err != nil {
			return err
		}
		if len(items) != n {
			return fmt.Errorf("feed has %d items, want %d", len(items), n)
		}
		return nil
	})

	var got []int64
	query := "limit=2"
	for pages := 0; pages < n+2; pages++ {
		p := feedPageOf(t, a, query)
		for _, it := range p.items(t) {
			got = append(got, it.ID)
		}
		if p.Next == nil {
			break
		}
		query = "limit=2&before=" + url.QueryEscape(*p.Next)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("paged ids = %v, want %v", got, want)
	}
}

func TestFeedBadLimitOrCursor400(t *testing.T) {
	u := newUser(t)
	for _, q := range []string{"limit=abc", "limit=0", "limit=-1", "before=not-a-cursor"} {
		st, body := getFeed(t, u, q)
		wantStatus(t, st, 400, body)
		wantErrCode(t, body, "bad_request")
	}
}
