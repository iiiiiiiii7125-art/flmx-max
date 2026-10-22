package tap

import (
	"net/url"
	"testing"

	"github.com/go-gost/core/handler"
)

// D1-H1: the TAP handler encrypts its transport with the SS cipher; it must
// fail closed when no auth is configured instead of running unencrypted.
func TestHandlerInitFailsClosedWithoutAuth(t *testing.T) {
	h := NewHandler()
	if err := h.Init(nil); err == nil {
		t.Fatal("expected Init to fail without auth (fail-closed), got nil error")
	}
}

func TestHandlerInitFailsClosedWithEmptyPassword(t *testing.T) {
	h := NewHandler(handler.AuthOption(url.UserPassword("aes-256-cfb", "")))
	if err := h.Init(nil); err == nil {
		t.Fatal("expected Init to fail with empty password, got nil error")
	}
}

func TestHandlerInitSucceedsWithAuth(t *testing.T) {
	h := NewHandler(handler.AuthOption(url.UserPassword("aes-256-cfb", "test-password")))
	if err := h.Init(nil); err != nil {
		t.Fatalf("expected Init to succeed with auth, got %v", err)
	}
}
