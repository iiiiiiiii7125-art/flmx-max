package socket

// R8 T-GATE 常设门（agent 侧）：自升级下载预算必须与超时预算登记表
// （doc/audit/timeout-budgets.tsv 的 A002 行）同值，且小于面板耐心
// （upgradeTimeout 300s）——改预算不改登记表即在此失败。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestR8AgentUpgradeDownloadBudgetMatchesRegistry(t *testing.T) {
	path := filepath.Join("..", "..", "..", "doc", "audit", "timeout-budgets.tsv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read timeout budget registry %s: %v", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 12 || f[0] != "A002" {
			continue
		}
		want, err := strconv.ParseFloat(f[11], 64)
		if err != nil {
			t.Fatalf("registry A002 be_s %q not numeric: %v", f[11], err)
		}
		if upgradeDownloadTimeout.Seconds() != want {
			t.Errorf("timeout budget drift: upgradeDownloadTimeout = %v, registry A002 says %gs — update code and registry together", upgradeDownloadTimeout, want)
		}
		if upgradeDownloadHeaderTimeout != 60*time.Second {
			t.Errorf("upgradeDownloadHeaderTimeout = %v, want 60s (response-header cap)", upgradeDownloadHeaderTimeout)
		}
		return
	}
	t.Fatal("registry row A002 missing")
}
