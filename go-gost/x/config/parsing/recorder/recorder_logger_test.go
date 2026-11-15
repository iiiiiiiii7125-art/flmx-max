package recorder

import (
	"testing"

	"github.com/go-gost/x/config"
)

// R9-B-01: ParseRecorder must not panic when no default logger has
// been registered (logger.Default() is nil until SetDefault is called).
// The nil-default path is the OpenFile error branch: the file path
// below cannot be created under /proc, so OpenFile fails and the old
// code called logger.Default().Warn on a nil default. This test
// deliberately does NOT call SetDefault, so it only exercises the
// nil-default path when it runs before any other test file registers
// a default logger; run it standalone
// (go test -run TestParseRecorderWithoutDefaultLogger) to hit that path.
func TestParseRecorderWithoutDefaultLogger(t *testing.T) {
	r := ParseRecorder(&config.RecorderConfig{
		Name: "test",
		File: &config.FileRecorder{Path: "/proc/no-such-dir/rec.log"},
	})
	if r == nil {
		t.Fatal("ParseRecorder returned nil")
	}
}
