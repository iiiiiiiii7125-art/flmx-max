package kernel

// R8 T-GATE 常设门（agent 侧）：内核下载预算必须与超时预算登记表
// （doc/audit/timeout-budgets.tsv 的 A001 行）同值，且小于面板耐心
// （kernelApplyTimeout 300s）——改预算不改登记表即在此失败。

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestR8KernelDownloadBudgetMatchesRegistry(t *testing.T) {
	path := filepath.Join("..", "..", "..", "doc", "audit", "timeout-budgets.tsv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read timeout budget registry %s: %v", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 12 || f[0] != "A001" {
			continue
		}
		want, err := strconv.ParseFloat(f[11], 64)
		if err != nil {
			t.Fatalf("registry A001 be_s %q not numeric: %v", f[11], err)
		}
		if downloadTimeout.Seconds() != want {
			t.Errorf("timeout budget drift: downloadTimeout = %v, registry A001 says %gs — update code and registry together", downloadTimeout, want)
		}
		return
	}
	t.Fatal("registry row A001 missing")
}

// L2-01 (round-6) gate: anchorCorroborateTimeout lives in two
// copies (x/kernel here, x/socket/websocket_reporter.go) and was
// registered nowhere — either copy could drift alone with every
// gate green. This test pins this copy's value, requires the
// socket twin to carry the same value, and requires registry row
// A001 to keep naming the parameter at the registered value.
func TestR8AnchorCorroborateTimeoutPinnedAcrossCopies(t *testing.T) {
	if anchorCorroborateTimeout != 20*time.Second {
		t.Fatalf("anchorCorroborateTimeout = %v, registry A001 names 20s — update code and registry together", anchorCorroborateTimeout)
	}
	raw, err := os.ReadFile(filepath.Join("..", "socket", "websocket_reporter.go"))
	if err != nil {
		t.Fatalf("read socket twin source: %v", err)
	}
	m := regexp.MustCompile(`anchorCorroborateTimeout = (\d+) \* time\.Second`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("anchorCorroborateTimeout declaration not found in x/socket/websocket_reporter.go — if its spelling changed, update this gate with it")
	}
	twin, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		t.Fatalf("parse socket anchorCorroborateTimeout: %v", err)
	}
	if twin != int64(anchorCorroborateTimeout/time.Second) {
		t.Fatalf("anchorCorroborateTimeout copies drifted: x/kernel = %v, x/socket = %ds — the two copies must stay equal", anchorCorroborateTimeout, twin)
	}

	regPath := filepath.Join("..", "..", "..", "doc", "audit", "timeout-budgets.tsv")
	reg, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read timeout budget registry %s: %v", regPath, err)
	}
	for _, line := range strings.Split(string(reg), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 18 || f[0] != "A001" {
			continue
		}
		if !strings.Contains(f[17], "anchorCorroborateTimeout 20s") {
			t.Fatal("registry A001 note no longer names anchorCorroborateTimeout 20s — the L2-01 registration must stay explicit")
		}
		return
	}
	t.Fatal("registry row A001 missing")
}
