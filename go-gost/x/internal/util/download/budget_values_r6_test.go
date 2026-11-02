package download

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// L2-01 (round-6) gate: the WDG-02 trickle/projection parameters
// were carried in doc/audit/timeout-budgets.tsv only as prose
// (TrickleGrace/TrickleMinRate) or not at all (TrickleRateWindow),
// and no Go test pinned their values — drifting either the code
// or the registry prose left every gate green. These assertions
// pin the values in code AND require the registry rows that own
// this chain (A001/A002) to keep naming them at those values, so
// code and registry cannot drift apart silently again.
func TestR6TrickleParameterValuesMatchRegistry(t *testing.T) {
	if TrickleGrace != 15*time.Second {
		t.Fatalf("TrickleGrace = %v, registry A001/A002 name 15s — update code and registry together", TrickleGrace)
	}
	if TrickleMinRate != 64<<10 {
		t.Fatalf("TrickleMinRate = %d B/s, registry A001/A002 name 64KiB/s — update code and registry together", TrickleMinRate)
	}
	if TrickleRateWindow != 10*time.Second {
		t.Fatalf("TrickleRateWindow = %v, registry A001/A002 name 10s — update code and registry together", TrickleRateWindow)
	}

	path := filepath.Join("..", "..", "..", "..", "..", "doc", "audit", "timeout-budgets.tsv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read timeout budget registry %s: %v", path, err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 18 || (f[0] != "A001" && f[0] != "A002") {
			continue
		}
		seen[f[0]] = true
		for _, want := range []string{"TrickleGrace 15s", "TrickleMinRate 64KiB/s", "TrickleRateWindow 10s", "anchorCorroborateTimeout 20s"} {
			if !strings.Contains(f[17], want) {
				t.Errorf("registry %s note no longer names %q — the L2-01 registration must stay explicit", f[0], want)
			}
		}
	}
	if !seen["A001"] || !seen["A002"] {
		t.Fatalf("registry rows A001/A002 missing (seen: %v)", seen)
	}
}
