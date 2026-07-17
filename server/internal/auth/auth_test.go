package auth

import (
	"testing"
	"time"
)

func TestPasswordAndToken(t *testing.T) {
	hash, err := HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "hunter22") {
		t.Fatal("correct password rejected")
	}
	if CheckPassword(hash, "wrong") {
		t.Fatal("wrong password accepted")
	}

	a := New([]byte("test-secret"), time.Hour, false)
	tok, err := a.IssueToken("alice")
	if err != nil {
		t.Fatal(err)
	}
	user, err := a.VerifyToken(tok)
	if err != nil || user != "alice" {
		t.Fatalf("verify: user=%q err=%v", user, err)
	}
	if _, err := a.VerifyToken(tok + "x"); err == nil {
		t.Fatal("tampered token accepted")
	}
	other := New([]byte("other-secret"), time.Hour, false)
	if _, err := other.VerifyToken(tok); err == nil {
		t.Fatal("token from different secret accepted")
	}
}
