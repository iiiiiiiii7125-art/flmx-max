package ingress

import (
	"testing"

	"github.com/go-gost/x/config"
)

// R9-B-01: ParseIngress must not panic when no default logger has been
// registered (logger.Default() is nil until SetDefault is called).
// This test deliberately does NOT call SetDefault, so it only
// exercises the nil-default path when it runs before any other test
// file registers a default logger; run it standalone
// (go test -run TestParseIngressWithoutDefaultLogger) to hit that path.
func TestParseIngressWithoutDefaultLogger(t *testing.T) {
	ing := ParseIngress(&config.IngressConfig{Name: "test"})
	if ing == nil {
		t.Fatal("ParseIngress returned nil")
	}
}
