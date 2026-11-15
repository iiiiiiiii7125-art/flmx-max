package bypass

import (
	"testing"

	"github.com/go-gost/x/config"
)

// R9-B-01: ParseBypass must not panic when no default logger has been
// registered (logger.Default() is nil until SetDefault is called).
// This test deliberately does NOT call SetDefault, so it only
// exercises the nil-default path when it runs before any other test
// file registers a default logger; run it standalone
// (go test -run TestParseBypassWithoutDefaultLogger) to hit that path.
func TestParseBypassWithoutDefaultLogger(t *testing.T) {
	bp := ParseBypass(&config.BypassConfig{Name: "test"})
	if bp == nil {
		t.Fatal("ParseBypass returned nil")
	}
}
