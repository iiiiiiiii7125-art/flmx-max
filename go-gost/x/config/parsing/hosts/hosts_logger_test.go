package hosts

import (
	"testing"

	"github.com/go-gost/x/config"
)

// R9-B-01: ParseHostMapper must not panic when no default logger has
// been registered (logger.Default() is nil until SetDefault is called).
// This test deliberately does NOT call SetDefault, so it only
// exercises the nil-default path when it runs before any other test
// file registers a default logger; run it standalone
// (go test -run TestParseHostMapperWithoutDefaultLogger) to hit that path.
func TestParseHostMapperWithoutDefaultLogger(t *testing.T) {
	hm := ParseHostMapper(&config.HostsConfig{Name: "test"})
	if hm == nil {
		t.Fatal("ParseHostMapper returned nil")
	}
}
