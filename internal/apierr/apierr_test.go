package apierr

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestWriteFormat(t *testing.T) {
	w := httptest.NewRecorder()
	Write(w, 404, "not_found", "post not found")

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "not_found" || body.Error.Message != "post not found" {
		t.Fatalf("body = %+v", body)
	}
}
