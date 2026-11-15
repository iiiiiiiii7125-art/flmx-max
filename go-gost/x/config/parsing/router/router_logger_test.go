package router

import (
	"testing"

	"github.com/go-gost/x/config"
)

// R9-B-01: ParseRouter must not panic when no default logger has been
// registered (logger.Default() is nil until SetDefault is called).
// This test deliberately does NOT call SetDefault, so it only
// exercises the nil-default path when it runs before any other test
// file registers a default logger; run it standalone
// (go test -run TestParseRouterWithoutDefaultLogger) to hit that path.
func TestParseRouterWithoutDefaultLogger(t *testing.T) {
	r := ParseRouter(&config.RouterConfig{Name: "test"})
	if r == nil {
		t.Fatal("ParseRouter returned nil")
	}
}
