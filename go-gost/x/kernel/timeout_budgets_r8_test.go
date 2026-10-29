package kernel

// R8 T-GATE 常设门（agent 侧）：内核下载预算必须与超时预算登记表
// （doc/audit/timeout-budgets.tsv 的 A001 行）同值，且小于面板耐心
// （kernelApplyTimeout 300s）——改预算不改登记表即在此失败。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
