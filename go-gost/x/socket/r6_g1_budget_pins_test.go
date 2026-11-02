package socket

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// L2-01 (round-6) gate: anchorCorroborateTimeout lives in two
// copies (x/socket here, x/kernel/install.go) and was registered
// nowhere — either copy could drift alone with every gate green.
// This test pins this copy's value, requires the kernel twin to
// carry the same value, and requires registry row A002 to keep
// naming the parameter at the registered value.

var anchorTimeoutDeclRe = regexp.MustCompile(`anchorCorroborateTimeout = (\d+) \* time\.Second`)

func anchorTimeoutSecondsIn(t *testing.T, path string) int64 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := anchorTimeoutDeclRe.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("anchorCorroborateTimeout declaration not found in %s — if its spelling changed, update this gate with it", path)
	}
	n, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		t.Fatalf("parse anchorCorroborateTimeout in %s: %v", path, err)
	}
	return n
}

func TestR6AnchorCorroborateTimeoutPinnedAcrossCopies(t *testing.T) {
	if anchorCorroborateTimeout != 20*time.Second {
		t.Fatalf("anchorCorroborateTimeout = %v, registry A002 names 20s — update code and registry together", anchorCorroborateTimeout)
	}
	twinPath := filepath.Join("..", "kernel", "install.go")
	if twin := anchorTimeoutSecondsIn(t, twinPath); twin != int64(anchorCorroborateTimeout/time.Second) {
		t.Fatalf("anchorCorroborateTimeout copies drifted: x/socket = %v, x/kernel = %ds — the two copies must stay equal", anchorCorroborateTimeout, twin)
	}

	regPath := filepath.Join("..", "..", "..", "doc", "audit", "timeout-budgets.tsv")
	raw, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read timeout budget registry %s: %v", regPath, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 18 || f[0] != "A002" {
			continue
		}
		if !strings.Contains(f[17], "anchorCorroborateTimeout 20s") {
			t.Fatalf("registry A002 note no longer names anchorCorroborateTimeout 20s — the L2-01 registration must stay explicit")
		}
		return
	}
	t.Fatal("registry row A002 missing")
}
