package router

import (
	"testing"
)

// R8-B-01: NewRouter must not panic when no LoggerOption is given and
// no default logger has been registered: reload() logs unconditionally.
// This test deliberately does NOT call SetDefault.
func TestNewRouterWithoutLogger(t *testing.T) {
	r := NewRouter()
	if r == nil {
		t.Fatal("NewRouter returned nil")
	}
}
