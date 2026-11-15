package resolver

import (
	"testing"

	"github.com/go-gost/x/config"
)

// R9-B-01: ParseResolver must not panic when no default logger has been
// registered (logger.Default() is nil until SetDefault is called).
// This test deliberately does NOT call SetDefault, so it only
// exercises the nil-default path when it runs before any other test
// file registers a default logger; run it standalone
// (go test -run TestParseResolverWithoutDefaultLogger) to hit that path.
func TestParseResolverWithoutDefaultLogger(t *testing.T) {
	r, err := ParseResolver(&config.ResolverConfig{Name: "test"})
	if err != nil {
		t.Fatalf("ParseResolver returned error: %v", err)
	}
	if r == nil {
		t.Fatal("ParseResolver returned nil resolver")
	}
}
