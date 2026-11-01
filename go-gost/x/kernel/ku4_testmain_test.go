package kernel

import (
	"os"
	"testing"

	"github.com/go-gost/x/internal/util/download"
)

// KU4 (1.7.7): kernel tests run against local httptest servers.
// The built-in mirror candidates must never be probed from unit
// tests — that would send real requests toward the public mirrors
// and make results depend on the sandbox network. Emptying the
// candidate list leaves every route plan direct-only; tests that
// exercise mirror routes substitute their own local candidates.
func TestMain(m *testing.M) {
	download.MirrorPrefixes = nil
	os.Exit(m.Run())
}
