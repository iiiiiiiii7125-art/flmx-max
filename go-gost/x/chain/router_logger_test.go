package chain

import (
	"testing"
)

// R8-B-01: NewRouter must not panic when no default logger has been
// registered (logger.Default() is nil until SetDefault is called).
// This test deliberately does NOT call SetDefault, so it only
// exercises the nil-default path when it runs before any other test
// file registers a default logger; run it standalone
// (go test -run TestNewRouterWithoutDefaultLogger) to hit that path.
func TestNewRouterWithoutDefaultLogger(t *testing.T) {
	r := NewRouter()
	if r == nil {
		t.Fatal("NewRouter returned nil")
	}
	if r.Options() == nil || r.Options().Logger == nil {
		t.Fatal("NewRouter left Logger nil without a registered default logger")
	}
	if got := r.Options().Logger.WithFields(map[string]any{"kind": "test"}); got == nil {
		t.Fatal("WithFields on the fallback logger returned nil")
	}
}
