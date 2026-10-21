package ss

import (
	"net/url"
	"testing"

	"github.com/go-gost/core/connector"
)

// D1-H1: the SS connector must fail closed when no auth is configured
// instead of silently sending plaintext.
func TestConnectorInitFailsClosedWithoutAuth(t *testing.T) {
	c := NewConnector()
	if err := c.Init(nil); err == nil {
		t.Fatal("expected Init to fail without auth (fail-closed), got nil error")
	}
}

func TestConnectorInitFailsClosedWithEmptyPassword(t *testing.T) {
	c := NewConnector(connector.AuthOption(url.UserPassword("aes-256-cfb", "")))
	if err := c.Init(nil); err == nil {
		t.Fatal("expected Init to fail with empty password, got nil error")
	}
}

func TestConnectorInitSucceedsWithAuth(t *testing.T) {
	c := NewConnector(connector.AuthOption(url.UserPassword("aes-256-cfb", "test-password")))
	if err := c.Init(nil); err != nil {
		t.Fatalf("expected Init to succeed with auth, got %v", err)
	}
}
