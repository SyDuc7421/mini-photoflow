package jwtutil

import (
	"testing"
	"time"
)

var secret = []byte("test-secret")

func TestSignVerifyRoundTrip(t *testing.T) {
	tok, err := Sign(secret, 42, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	id, err := Verify(secret, tok)
	if err != nil || id != 42 {
		t.Fatalf("id=%d err=%v, want 42 nil", id, err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	tok, _ := Sign(secret, 42, -time.Minute)
	if _, err := Verify(secret, tok); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	tok, _ := Sign(secret, 42, time.Hour)
	if _, err := Verify([]byte("other"), tok); err == nil {
		t.Fatal("token signed with other secret accepted")
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	if _, err := Verify(secret, "not-a-jwt"); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestVerifyRejectsNonPositiveSubject(t *testing.T) {
	for _, id := range []int64{0, -5} {
		tok, _ := Sign(secret, id, time.Hour)
		if _, err := Verify(secret, tok); err == nil {
			t.Fatalf("subject %d accepted", id)
		}
	}
}
