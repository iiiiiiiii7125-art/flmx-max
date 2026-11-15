package service

import (
	"strings"
	"testing"

	"github.com/go-gost/x/config"
)

// R9-B-01: ParseService must not panic when no default logger has been
// registered (logger.Default() is nil until SetDefault is called).
// This test deliberately does NOT call SetDefault, so it only
// exercises the nil-default path when it runs before any other test
// file registers a default logger; run it standalone
// (go test -run TestParseServiceWithoutDefaultLogger) to hit that path.
// The service may still fail later (e.g. no listener registered in
// this test binary); that is expected — the point is no panic before
// the hook check passes.
func TestParseServiceWithoutDefaultLogger(t *testing.T) {
	cfg := &config.ServiceConfig{
		Name:     "test",
		Addr:     ":0",
		Listener: &config.ListenerConfig{Type: "tcp"},
		Handler:  &config.HandlerConfig{Type: "tcp"},
	}
	_, err := ParseService(cfg)
	if err != nil && strings.Contains(err.Error(), "shell hooks") {
		t.Fatalf("service without hooks rejected by hook check: %v", err)
	}
}
