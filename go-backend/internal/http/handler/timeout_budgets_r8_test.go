package handler

// R8 T-GATE 常设门（Go 侧）：超时预算登记表
// （doc/audit/timeout-budgets.tsv）点名的每个 Go 常量必须与产品代码
// 同值——改常量不改登记表（或反之）即在此失败。shell 侧对账见
// scripts/check-timeout-budgets.sh（经 check-doc-consistency.sh §8 转跑）。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type r8BudgetRow struct {
	cell, call, surface    string
	feS, feSrc, feFloor    string
	svS, svSrc, beS, beSrc string
}

func r8LoadBudgetRegistry(t *testing.T) []r8BudgetRow {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "doc", "audit", "timeout-budgets.tsv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read timeout budget registry %s: %v", path, err)
	}
	var rows []r8BudgetRow
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "cell\t") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 14 {
			t.Fatalf("registry row has %d fields (<14): %q", len(f), line)
		}
		rows = append(rows, r8BudgetRow{
			cell: f[0], call: f[1], surface: f[2],
			feS: f[4], feSrc: f[6], feFloor: f[7],
			svS: f[9], svSrc: f[10], beS: f[11], beSrc: f[13],
		})
	}
	if len(rows) < 100 {
		t.Fatalf("registry suspiciously small: %d rows", len(rows))
	}
	return rows
}

// r8RegistrySeconds 返回登记表中某符号登记的秒数：符号可出现在
// sv_src（对 sv_s）、be_src（对 be_s）或 fe_src 的 const: 形态
// （对 fe_floor，空则 fe_s）。多行出现必须同值；未登记返回 ok=false。
func r8RegistrySeconds(rows []r8BudgetRow, sym string) (float64, bool) {
	var found float64
	ok := false
	set := func(s string) {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return
		}
		if ok && v != found {
			// 多行同符号异值：登记表自相矛盾，交给调用方以 found 为准报错
			found = -1
			return
		}
		found, ok = v, true
	}
	for _, r := range rows {
		if r.svSrc == sym {
			set(r.svS)
		}
		if r.beSrc == sym {
			set(r.beS)
		}
		if r.feSrc == "const:"+sym {
			if r.feFloor != "" {
				set(r.feFloor)
			} else {
				set(r.feS)
			}
		}
	}
	return found, ok
}

func TestR8TimeoutBudgetRegistryPinsGoConstants(t *testing.T) {
	rows := r8LoadBudgetRegistry(t)
	pins := map[string]time.Duration{
		// write_deadline.go 放宽常量组
		"kernelApplyWriteDeadline":      kernelApplyWriteDeadline,
		"nodeUpgradeWriteDeadline":      nodeUpgradeWriteDeadline,
		"nodeBatchUpgradeWriteDeadline": nodeBatchUpgradeWriteDeadline,
		"systemUpgradeWriteDeadline":    systemUpgradeWriteDeadline,
		"systemVersionWriteDeadline":    systemVersionWriteDeadline,
		"diagnosisWriteDeadline":        diagnosisWriteDeadline,
		"tunnelUpdateWriteDeadline":     tunnelUpdateWriteDeadline,
		"tunnelCreateWriteDeadline":     tunnelCreateWriteDeadline,
		"backupWriteDeadline":           backupWriteDeadline,
		// gh-mirror 服务放宽
		"ghMirrorServeFor": ghMirrorServeFor,
		// 后端界常量
		"kernelApplyTimeout":        kernelApplyTimeout,
		"kernelCommandTimeout":      kernelCommandTimeout,
		"upgradeTimeout":            upgradeTimeout,
		"diagnosisRequestTimeout":   diagnosisRequestTimeout,
		"defaultNodeCommandTimeout": defaultNodeCommandTimeout,
		"idemWaitTimeout":           idemWaitTimeout,
		"nodeTuneProbeTimeout":      nodeTuneProbeTimeout,
		// 联邦余量与运行时面
		"federationClientMargin":         federationClientMargin,
		"federationRuntimeClientTimeout": federationRuntimeClientTimeout,
	}
	for sym, actual := range pins {
		want, ok := r8RegistrySeconds(rows, sym)
		if !ok {
			t.Errorf("registry does not pin Go constant %s (registry row removed?)", sym)
			continue
		}
		if actual.Seconds() != want {
			t.Errorf("timeout budget drift: Go constant %s = %v, registry says %gs — update code and registry together", sym, actual, want)
		}
	}
}

func TestR8TimeoutBudgetComposites(t *testing.T) {
	rows := r8LoadBudgetRegistry(t)
	// 联邦 client = 对端命令预算 + federationClientMargin（构造性余量）。
	if got := federationClientTimeout(120 * time.Second); got != 130*time.Second {
		t.Errorf("federationClientTimeout(120s) = %v, want 130s (budget + margin)", got)
	}
	if got := federationClientTimeout(diagnosisRequestTimeout); got != diagnosisRequestTimeout+federationClientMargin {
		t.Errorf("federationClientTimeout(diagnosisRequestTimeout) = %v, want budget+margin", got)
	}
	// P002：guard join 界 = addServiceGuardJoinTimeout + 一次命令往返。
	var p002 string
	for _, r := range rows {
		if r.cell == "P002" {
			p002 = r.beS
		}
	}
	if p002 == "" {
		t.Fatal("registry row P002 missing")
	}
	wantJoin, _ := strconv.ParseFloat(p002, 64)
	if got := (addServiceGuardJoinTimeout + defaultNodeCommandTimeout).Seconds(); got != wantJoin {
		t.Errorf("guard join bound = %gs, registry P002 says %gs", got, wantJoin)
	}
	// 延迟探针命令界：2 波 × 4 ping × (2000+200)ms + 5s = 22.6s（T042/T043 分量）。
	if latencyCommandTimeout != 22600*time.Millisecond {
		t.Errorf("latencyCommandTimeout = %v, want 22.6s (probe formula components drifted)", latencyCommandTimeout)
	}
	// 版本发现逐请求界 30s（T007–T009 分量锚）。
	if c := githubVersionFetchClient(); c == nil || c.Timeout != 30*time.Second {
		t.Errorf("githubVersionFetchClient timeout = %v, want 30s", c)
	}
	// 通知复合界分量（T016）：单次 10s、重试间隔 3s。
	if notifySendTimeout != 10*time.Second || notifyRetryDelay != 3*time.Second {
		t.Errorf("notify components drifted: send=%v retry=%v, want 10s/3s", notifySendTimeout, notifyRetryDelay)
	}
	// 复合界分量直钉（登记行 be 为复合值，常量本身在此钉死）：
	// T011/T012 诊断命令腿 30s（行 be 为 120s 请求 ctx）；
	// T007–T009 版本发现总封顶 60s（行 be 为逐调用复合）；
	// A004 路由探针面板预算 20s（行 fe 即此预算，脚本 A004 另行对账）。
	if diagnosisCommandTimeout != 30*time.Second {
		t.Errorf("diagnosisCommandTimeout = %v, want 30s (T011/T012 component)", diagnosisCommandTimeout)
	}
	if secureFetchTotalTimeout != 60*time.Second {
		t.Errorf("secureFetchTotalTimeout = %v, want 60s (T007-T009 component)", secureFetchTotalTimeout)
	}
	if routeCommandTimeout != 20*time.Second {
		t.Errorf("routeCommandTimeout = %v, want 20s (A004 panel budget)", routeCommandTimeout)
	}
}
