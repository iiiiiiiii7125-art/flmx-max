package auth

import (
	"testing"

	"github.com/go-gost/x/config"
)

// R9-B-01: ParseAuther must not panic when no default logger has been
// registered (logger.Default() is nil until SetDefault is called).
// This test deliberately does NOT call SetDefault, so it only
// exercises the nil-default path when it runs before any other test
// file registers a default logger; run it standalone
// (go test -run TestParseAutherWithoutDefaultLogger) to hit that path.
func TestParseAutherWithoutDefaultLogger(t *testing.T) {
	auther := ParseAuther(&config.AutherConfig{Name: "test"})
	if auther == nil {
		t.Fatal("ParseAuther returned nil")
	}
}

// R9-B-01: ParseAutherFromAuth must not panic when no default logger
// has been registered. Same standalone-run note as above.
func TestParseAutherFromAuthWithoutDefaultLogger(t *testing.T) {
	auther := ParseAutherFromAuth(&config.AuthConfig{Username: "u", Password: "p"})
	if auther == nil {
		t.Fatal("ParseAutherFromAuth returned nil")
	}
}
