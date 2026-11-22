package events

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestEventPayloadRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   any
		out  any
	}{
		{"post.created", PostCreated{PostID: 7, AuthorID: 3, AuthorName: "an", Caption: "xin chào ế", ImageKey: "3/ab.png", CreatedAtMicro: 1700000000123456}, new(PostCreated)},
		{"follow.set", FollowSet{FollowerID: 1, FolloweeID: 2, Following: false}, new(FollowSet)},
		{"like.set", LikeSet{PostID: 9, UserID: 4, Liked: true}, new(LikeSet)},
		{"comment.added", CommentAdded{PostID: 9, CommentID: 12}, new(CommentAdded)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := marshal(c.in)
			if err != nil {
				t.Fatal(err)
			}
			if err := Decode(b, c.out); err != nil {
				t.Fatal(err)
			}
			if got := reflect.ValueOf(c.out).Elem().Interface(); !reflect.DeepEqual(got, c.in) {
				t.Fatalf("round trip = %+v, want %+v", got, c.in)
			}
		})
	}
}

func TestPublishWithoutConnectionIsAnError(t *testing.T) {
	if err := Publish(nil, SubjectLikeSet, LikeSet{}); err == nil {
		t.Fatal("Publish(nil) returned nil")
	}
}

// TestPublishDelivers needs a NATS server; set TEST_NATS_URL to run it.
func TestPublishDelivers(t *testing.T) {
	url := testNATS(t)
	nc, err := Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	got := make(chan []byte, 1)
	sub, err := nc.Subscribe(SubjectLikeSet, func(m *nats.Msg) { got <- m.Data })
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()
	nc.Flush()
	want := LikeSet{PostID: 5, UserID: 6, Liked: true}
	if err := Publish(nc, SubjectLikeSet, want); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-got:
		var ev LikeSet
		if err := Decode(data, &ev); err != nil || ev != want {
			t.Fatalf("event = %+v, %v", ev, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event within 2s")
	}
}

func marshal(v any) ([]byte, error) { return json.Marshal(v) }

func testNATS(t *testing.T) string {
	t.Helper()
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	return url
}
