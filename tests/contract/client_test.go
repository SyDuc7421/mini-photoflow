package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"
)

func baseURL() string {
	if u := os.Getenv("BASE_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:8080"
}

type user struct {
	ID    int64
	Name  string
	Token string
}

// call performs a request and returns status and raw body.
func call(t *testing.T, method, path, token, contentType string, body io.Reader) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, baseURL()+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func callJSON(t *testing.T, method, path, token string, v any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if v != nil {
		b, _ := json.Marshal(v)
		r = bytes.NewReader(b)
	}
	return call(t, method, path, token, "application/json", r)
}

func wantStatus(t *testing.T, got, want int, body []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("status = %d, want %d (body: %s)", got, want, body)
	}
}

func wantErrCode(t *testing.T, body []byte, code string) {
	t.Helper()
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil || e.Error.Code != code {
		t.Fatalf("error body = %s, want code %q", body, code)
	}
}

const password = "pw-12345678"

func uniqueName() string { return fmt.Sprintf("u_%d_%d", time.Now().UnixNano(), rand.Intn(1e6)) }

func newUser(t *testing.T) *user {
	t.Helper()
	name := uniqueName()
	st, body := callJSON(t, "POST", "/register", "", map[string]string{"username": name, "password": password})
	wantStatus(t, st, 201, body)
	var u struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.ID == 0 {
		t.Fatalf("register body = %s", body)
	}
	st, body = callJSON(t, "POST", "/login", "", map[string]string{"username": name, "password": password})
	wantStatus(t, st, 200, body)
	var l struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &l); err != nil || l.Token == "" {
		t.Fatalf("login body = %s", body)
	}
	return &user{ID: u.ID, Name: name, Token: l.Token}
}

func pngBytes() []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	return b.Bytes()
}

// postRaw sends multipart with an optional image part.
func postRaw(t *testing.T, token, caption string, img []byte, imgType string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("caption", caption)
	if img != nil {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="image"; filename="a.png"`)
		h.Set("Content-Type", imgType)
		part, _ := mw.CreatePart(h)
		_, _ = part.Write(img)
	}
	_ = mw.Close()
	return call(t, "POST", "/posts", token, mw.FormDataContentType(), &buf)
}

type post struct {
	ID int64 `json:"id"`
}

func createPost(t *testing.T, u *user, caption string) int64 {
	t.Helper()
	st, body := postRaw(t, u.Token, caption, pngBytes(), "image/png")
	wantStatus(t, st, 201, body)
	var p post
	if err := json.Unmarshal(body, &p); err != nil || p.ID == 0 {
		t.Fatalf("post body = %s", body)
	}
	return p.ID
}

func follow(t *testing.T, u *user, target int64) {
	t.Helper()
	st, body := call(t, "PUT", fmt.Sprintf("/users/%d/follow", target), u.Token, "", nil)
	wantStatus(t, st, 204, body)
}

func like(t *testing.T, u *user, postID int64) {
	t.Helper()
	st, body := call(t, "PUT", fmt.Sprintf("/posts/%d/like", postID), u.Token, "", nil)
	wantStatus(t, st, 204, body)
}

type feedItem struct {
	ID        int64  `json:"id"`
	Author    string `json:"author"`
	Caption   string `json:"caption"`
	ImageURL  string `json:"image_url"`
	Likes     int    `json:"likes"`
	Comments  int    `json:"comments"`
	LikedByMe bool   `json:"liked_by_me"`
	CreatedAt string `json:"created_at"`
}

type feedPage struct {
	Items json.RawMessage `json:"items"`
	Next  *string         `json:"next"`
}

func (p feedPage) items(t *testing.T) []feedItem {
	t.Helper()
	var items []feedItem
	if err := json.Unmarshal(p.Items, &items); err != nil {
		t.Fatalf("items: %v", err)
	}
	return items
}

func getFeed(t *testing.T, u *user, query string) (int, []byte) {
	t.Helper()
	path := "/feed"
	if query != "" {
		path += "?" + query
	}
	return call(t, "GET", path, u.Token, "", nil)
}

func feedPageOf(t *testing.T, u *user, query string) feedPage {
	t.Helper()
	st, body := getFeed(t, u, query)
	wantStatus(t, st, 200, body)
	var p feedPage
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("feed body = %s", body)
	}
	return p
}

// eventually retries check for up to 2s: the event-driven feed is eventually consistent.
func eventually(t *testing.T, check func() error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := check()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not satisfied within 2s: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// feedItemsE fetches a feed page without failing the test, for use inside eventually().
func feedItemsE(u *user, query string) ([]feedItem, error) {
	path := "/feed"
	if query != "" {
		path += "?" + query
	}
	req, err := http.NewRequest("GET", baseURL()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+u.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("feed status %d: %s", resp.StatusCode, data)
	}
	var p feedPage
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	var items []feedItem
	if err := json.Unmarshal(p.Items, &items); err != nil {
		return nil, err
	}
	return items, nil
}
