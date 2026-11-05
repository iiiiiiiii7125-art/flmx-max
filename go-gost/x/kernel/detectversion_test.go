package kernel

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// D2-04 regression: detectVersion ran CombinedOutput on a child
// goroutine and, on the 5s timeout path, read cmd.Process with no
// synchronisation (a data race, visible under -race), and if Start
// itself was delayed past 5s the "timeout" never fired and the
// caller — Apply, holding applyMu — blocked for the process's full
// natural life. The probe must be bounded by a context deadline
// and must not touch cmd.Process from another goroutine.

func writeVersionScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is unix-only")
	}
	path := filepath.Join(t.TempDir(), "fake-kernel")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectVersionReturnsFirstLine(t *testing.T) {
	bin := writeVersionScript(t, "#!/bin/sh\necho 'fake-kernel v1.2.3 extra'\necho 'second line'\n")
	if got := detectVersion(bin, []string{"--version"}); got != "fake-kernel v1.2.3 extra" {
		t.Fatalf("detectVersion = %q, want first line only", got)
	}
}

func TestDetectVersionEmptyArgs(t *testing.T) {
	if got := detectVersion("/bin/true", nil); got != "" {
		t.Fatalf("detectVersion with no args = %q, want empty", got)
	}
}

func TestDetectVersionHangingBinaryIsBounded(t *testing.T) {
	bin := writeVersionScript(t, "#!/bin/sh\nsleep 60\n")
	start := time.Now()
	got := detectVersion(bin, []string{"--version"})
	elapsed := time.Since(start)
	if elapsed > 20*time.Second {
		t.Fatalf("detectVersion blocked for %v on a hanging binary; the 5s probe budget must bound it", elapsed)
	}
	if got != "" {
		t.Fatalf("detectVersion on a hanging binary = %q, want empty", got)
	}
}
