package logger

import (
	"testing"

	"github.com/go-gost/x/config"
)

// R9-B-01: ParseLogger must not panic when no default logger has been
// registered (logger.Default() is nil until SetDefault is called).
// The nil-default path is the OpenFile error branch: the output path
// below cannot be created under /proc, so OpenFile fails and the old
// code called logger.Default().Warn on a nil default. This test
// deliberately does NOT call SetDefault, so it only exercises the
// nil-default path when it runs before any other test file registers
// a default logger; run it standalone
// (go test -run TestParseLoggerWithoutDefaultLogger) to hit that path.
func TestParseLoggerWithoutDefaultLogger(t *testing.T) {
	lg := ParseLogger(&config.LoggerConfig{
		Name: "test",
		Log:  &config.LogConfig{Output: "/proc/no-such-dir/out.log"},
	})
	if lg == nil {
		t.Fatal("ParseLogger returned nil")
	}
}
