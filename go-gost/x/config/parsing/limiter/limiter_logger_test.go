package limiter

import (
	"testing"

	"github.com/go-gost/x/config"
)

// R9-B-01: the Parse*Limiter functions must not panic when no default
// logger has been registered (logger.Default() is nil until SetDefault
// is called). These tests deliberately do NOT call SetDefault, so they
// only exercise the nil-default path when they run before any other
// test file registers a default logger; run them standalone
// (go test -run 'TestParse.*LimiterWithoutDefaultLogger') to hit that path.
func TestParseTrafficLimiterWithoutDefaultLogger(t *testing.T) {
	lim := ParseTrafficLimiter(&config.LimiterConfig{Name: "test"})
	if lim == nil {
		t.Fatal("ParseTrafficLimiter returned nil")
	}
}

func TestParseConnLimiterWithoutDefaultLogger(t *testing.T) {
	lim := ParseConnLimiter(&config.LimiterConfig{Name: "test"})
	if lim == nil {
		t.Fatal("ParseConnLimiter returned nil")
	}
}

func TestParseRateLimiterWithoutDefaultLogger(t *testing.T) {
	lim := ParseRateLimiter(&config.LimiterConfig{Name: "test"})
	if lim == nil {
		t.Fatal("ParseRateLimiter returned nil")
	}
}
