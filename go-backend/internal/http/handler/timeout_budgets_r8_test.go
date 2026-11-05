package handler

// R8 T-GATE 常设门（Go 侧）：超时预算登记表
// （doc/audit/timeout-budgets.tsv）点名的每个 Go 常量必须与产品代码
// 同值——改常量不改登记表（或反之）即在此失败。shell 侧对账见
// scripts/check-timeout-budgets.sh（经 check-doc-consistency.sh §8 转跑）。

import (
	"os"
	"path/filepath"
	"regexp"
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
		"inboundBatchWriteDeadline":     inboundBatchWriteDeadline,
		"nodeUpgradeWriteDeadline":      nodeUpgradeWriteDeadline,
		"nodeBatchUpgradeWriteDeadline": nodeBatchUpgradeWriteDeadline,
		"systemUpgradeWriteDeadline":    systemUpgradeWriteDeadline,
		"systemVersionWriteDeadline":    systemVersionWriteDeadline,
		"diagnosisWriteDeadline":        diagnosisWriteDeadline,
		"tunnelUpdateWriteDeadline":     tunnelUpdateWriteDeadline,
		"tunnelCreateWriteDeadline":     tunnelCreateWriteDeadline,
		"backupWriteDeadline":           backupWriteDeadline,
		// nodeInstall 放宽（C059 的 sv）：RR4-C6-01 前不在本钉表内，
		// 壳门词法提取被原始字符串伪行遮蔽时全系统无人兜底——补钉后
		// 编译值与登记 180s 任一漂移即在此红。
		"nodeInstallWriteDeadline": nodeInstallWriteDeadline,
		// gh-mirror 服务放宽
		"ghMirrorServeFor": ghMirrorServeFor,
		// 后端界常量
		"kernelApplyTimeout":        kernelApplyTimeout,
		"kernelCommandTimeout":      kernelCommandTimeout,
		"diagnosisRequestTimeout":   diagnosisRequestTimeout,
		"defaultNodeCommandTimeout": defaultNodeCommandTimeout,
		"idemWaitTimeout":           idemWaitTimeout,
		"nodeTuneProbeTimeout":      nodeTuneProbeTimeout,
		// nodeTune 后端界（T019 的 be）：RR4-C6-01 同面未钉符号，
		// 与 nodeTuneProbeTimeout 同文件同批补钉（登记 30s）。
		"nodeTuneTimeout": nodeTuneTimeout,
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

// R7-005 值钉：systemUpgradeAnchorTimeout 是跨线路锚复核机制的第三
// 份拷贝（面板侧，与 agent 侧 x/kernel、x/socket 的
// anchorCorroborateTimeout 同值同口径），R6 登记时漏在门外——任一
// 侧单独改值全部旧门皆绿。本测试三面钉死：① 本包常量恒为 20s；
// ② 登记表 T004 行注记必须点名「systemUpgradeAnchorTimeout 20s」；
// ③ 以正则解析 agent 两份拷贝的声明值，必须与本包常量相等（与
// x/kernel 门的跨包钉法同形）。任一侧改值，本门或 agent 侧门必红。
func TestR7SystemUpgradeAnchorTimeoutPinnedAcrossCopies(t *testing.T) {
	if systemUpgradeAnchorTimeout != 20*time.Second {
		t.Fatalf("systemUpgradeAnchorTimeout = %v, registry T004 names 20s — update code and registry together", systemUpgradeAnchorTimeout)
	}

	regPath := filepath.Join("..", "..", "..", "..", "doc", "audit", "timeout-budgets.tsv")
	reg, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read timeout budget registry %s: %v", regPath, err)
	}
	foundT004 := false
	for _, line := range strings.Split(string(reg), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 18 || f[0] != "T004" {
			continue
		}
		foundT004 = true
		if !strings.Contains(f[17], "systemUpgradeAnchorTimeout 20s") {
			t.Fatal("registry T004 note no longer names systemUpgradeAnchorTimeout 20s — the R7-005 registration must stay explicit")
		}
	}
	if !foundT004 {
		t.Fatal("registry row T004 missing")
	}

	decl := regexp.MustCompile(`anchorCorroborateTimeout = (\d+) \* time\.Second`)
	twins := map[string]string{
		"x/kernel/install.go":            filepath.Join("..", "..", "..", "..", "go-gost", "x", "kernel", "install.go"),
		"x/socket/websocket_reporter.go": filepath.Join("..", "..", "..", "..", "go-gost", "x", "socket", "websocket_reporter.go"),
	}
	for name, path := range twins {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read agent twin %s: %v", name, err)
		}
		m := decl.FindSubmatch(raw)
		if m == nil {
			t.Fatalf("anchorCorroborateTimeout declaration not found in %s — if its spelling changed, update this gate with it", name)
		}
		twin, err := strconv.ParseInt(string(m[1]), 10, 64)
		if err != nil {
			t.Fatalf("parse %s anchorCorroborateTimeout: %v", name, err)
		}
		if twin != int64(systemUpgradeAnchorTimeout/time.Second) {
			t.Fatalf("anchor timeout copies drifted: panel systemUpgradeAnchorTimeout = %v, %s = %ds — all copies must stay equal", systemUpgradeAnchorTimeout, name, twin)
		}
	}

	// 脚本侧字面量拷贝（R7-002/R7-005 全集收口，登记于 tsv 表
	// 头段）：panel_install.sh 与 install.sh 的 corroborate 轮截止
	// `local deadline=$(( $(date +%s) + N ))`、生成安装命令内的
	// `dl=$(( $(date +%s) + N ))`。每文件登记份数固定、逐份与 Go
	// 三份拷贝同值——脚本侧单独改值或新增未登记拷贝，本门即红。
	// GATE-FIX D 登记：install.sh 有两份——corroborate_binary_anchor
	// （R7-002）与 corroborate_latest_tag（A3-03 新增的标签复核
	// 轮，同语义同值），后者落地时漏登记、本批补入；install.sh
	// 结构上去重（抽共用变量）会让两处同时脱离字面量提取面、
	// 反而缩小普查覆盖，故按登记路径处置而非去重。全集共七份。
	shellDecl := regexp.MustCompile(`(?:local deadline|dl)=\$\(\( \$\(date \+%s\) \+ (\d+) \)`)
	type shellCopySpec struct {
		path string
		want int
	}
	shellCopies := map[string]shellCopySpec{
		"panel_install.sh corroborate_download_anchor": {filepath.Join("..", "..", "..", "..", "panel_install.sh"), 1},
		"install.sh corroborate_latest_tag+corroborate_binary_anchor": {filepath.Join("..", "..", "..", "..", "install.sh"), 2},
		"mutations.go generated install command": {"mutations.go", 1},
	}
	for name, spec := range shellCopies {
		raw, err := os.ReadFile(spec.path)
		if err != nil {
			t.Fatalf("read shell anchor copy %s: %v", name, err)
		}
		ms := shellDecl.FindAllSubmatch(raw, -1)
		if len(ms) != spec.want {
			t.Fatalf("%s: found %d anchor deadline literals, want exactly %d — a new copy must be registered and pinned here first", name, len(ms), spec.want)
		}
		for _, m := range ms {
			v, err := strconv.ParseInt(string(m[1]), 10, 64)
			if err != nil {
				t.Fatalf("parse %s anchor deadline: %v", name, err)
			}
			if v != int64(systemUpgradeAnchorTimeout/time.Second) {
				t.Fatalf("anchor timeout copies drifted: panel systemUpgradeAnchorTimeout = %v, %s = %ds — all seven copies must stay equal", systemUpgradeAnchorTimeout, name, v)
			}
		}
	}
}

func TestR8TimeoutBudgetComposites(t *testing.T) {
	rows := r8LoadBudgetRegistry(t)
	// upgradeTimeout 300s（B05-002，R2-K2）：登记表 T005/T031 的 be
	// 已改为含版本发现前奏的 expr/formula 界，不再以本常量原值登记，
	// 故退出上方通用 pin 表、改在此直钉；shell 门另以 A002 行逐行
	// 对账 go_const_s upgradeTimeout == 登记 300。
	if upgradeTimeout != 300*time.Second {
		t.Errorf("upgradeTimeout = %v, want 300s (T005/T031/A002 component)", upgradeTimeout)
	}
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
