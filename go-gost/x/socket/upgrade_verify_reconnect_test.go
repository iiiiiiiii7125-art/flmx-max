package socket

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A4-V-07 回归：升级/回退脚本旧判活只有「进程存活＋PID 稳定」，
// 新 agent 哪怕配置错误、永远连不上面板，脚本也记 restart ok、
// 不回滚。本组测试锁定新口径：回连证据＝agent 进程在 WS 连接
// 成功点写下的标记（agent-connected.state），脚本以停服后捕获
// 的 since 时刻为界轮询标记；判活函数以桩标记文件实跑覆盖关
// 键场景，脚本本体做渲染断言＋sh -n 语法核。

func TestWriteAgentConnectedMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, agentConnectedMarkerName)
	before := time.Now().Unix()
	if err := writeAgentConnectedMarker(path, "2.1.0 (debian/amd64)", time.Now()); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	after := time.Now().Unix()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("marker must carry epoch + version lines, got %q", raw)
	}
	ts, err := strconv.ParseInt(lines[0], 10, 64)
	if err != nil || ts < before || ts > after {
		t.Fatalf("marker epoch %q outside [%d, %d] (err=%v)", lines[0], before, after, err)
	}
	if lines[1] != "2.1.0 (debian/amd64)" {
		t.Fatalf("marker version line = %q", lines[1])
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("atomic write must not leave the temp file behind (err=%v)", err)
	}
}

// runReconnectCheck 实跑渲染出的 flmx_agent_reconnected_ok：
// (marker 内容预置, since, window, poll) → RECONNECTED / NOT。
func runReconnectCheck(t *testing.T, markerContent *string, since int64, windowSec, pollSec int) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, agentConnectedMarkerName)
	if markerContent != nil {
		if err := os.WriteFile(marker, []byte(*markerContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := agentReconnectCheckFunctionFor(marker, windowSec, pollSec) +
		"\nif flmx_agent_reconnected_ok " + strconv.FormatInt(since, 10) + "; then echo RECONNECTED; else echo NOT; fi"
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("reconnect check script failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestAgentReconnectCheckFreshMarker(t *testing.T) {
	since := time.Now().Unix()
	content := strconv.FormatInt(since+30, 10) + "\n2.1.0 (debian/amd64)\n"
	if got := runReconnectCheck(t, &content, since, 0, 1); got != "RECONNECTED" {
		t.Fatalf("fresh marker must satisfy the gate: got %q", got)
	}
}

func TestAgentReconnectCheckSameSecondMarker(t *testing.T) {
	// since 与标记同秒：标记只能出自停服后启动的新进程（旧进
	// 程已死），必须判已回连——严 > 会让一次拨通不再重拨的健
	// 康进程在窗口内永远等不到更晚的时间戳。
	since := time.Now().Unix()
	content := strconv.FormatInt(since, 10) + "\n2.1.0 (debian/amd64)\n"
	if got := runReconnectCheck(t, &content, since, 0, 1); got != "RECONNECTED" {
		t.Fatalf("same-second marker from the new process must satisfy the gate: got %q", got)
	}
}

func TestAgentReconnectCheckStaleMarker(t *testing.T) {
	since := time.Now().Unix()
	content := strconv.FormatInt(since-3600, 10) + "\n2.0.0 (debian/amd64)\n"
	if got := runReconnectCheck(t, &content, since, 0, 1); got != "NOT" {
		t.Fatalf("stale marker from the old process must not satisfy the gate: got %q", got)
	}
}

func TestAgentReconnectCheckMissingOrMalformedMarker(t *testing.T) {
	since := time.Now().Unix()
	if got := runReconnectCheck(t, nil, since, 0, 1); got != "NOT" {
		t.Fatalf("missing marker must not satisfy the gate: got %q", got)
	}
	bad := "not-a-timestamp\n2.1.0\n"
	if got := runReconnectCheck(t, &bad, since, 0, 1); got != "NOT" {
		t.Fatalf("malformed marker must not satisfy the gate: got %q", got)
	}
}

func TestAgentReconnectCheckDelayedConnect(t *testing.T) {
	since := time.Now().Unix()
	dir := t.TempDir()
	marker := filepath.Join(dir, agentConnectedMarkerName)
	go func() {
		time.Sleep(1200 * time.Millisecond)
		_ = os.WriteFile(marker, []byte(strconv.FormatInt(time.Now().Unix(), 10)+"\n2.1.0 (debian/amd64)\n"), 0o644)
	}()
	script := agentReconnectCheckFunctionFor(marker, 5, 1) +
		"\nif flmx_agent_reconnected_ok " + strconv.FormatInt(since, 10) + "; then echo RECONNECTED; else echo NOT; fi"
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("reconnect check script failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "RECONNECTED" {
		t.Fatalf("marker appearing inside the window must satisfy the gate: got %q", got)
	}
}

func shellSyntaxCheck(t *testing.T, script string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(f, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("rendered script must parse as POSIX sh: %v\n%s\n---\n%s", err, out, script)
	}
}

func TestUpgradeScriptReconnectGateRendering(t *testing.T) {
	// F-211-01 起判活门按目标世代渲染：本测试锁定 marker 世代
	// 目标（capable）的武装形态，经 ForCap 显式给定世代（探测
	// 驱动的包装见 TestUpgradeScriptProbeDrivenDegradation）。
	script, err := buildAgentRestartScriptForCap(serviceManagerSystemd,
		"/etc/flmx_agent/.flmx_agent.upgrade-1", "/etc/flmx_agent/flmx_agent", "/etc/flmx_agent/flmx_agent.old",
		agentTargetMarkerCapable, "2.1.1")
	if err != nil {
		t.Fatalf("buildAgentRestartScriptForCap: %v", err)
	}
	shellSyntaxCheck(t, script)
	for _, want := range []string{
		"flmx_agent_reconnected_ok",
		"/etc/flmx_agent/agent-connected.state",
		"flmx_marker_gate",
		"panel connection marker not found",
		// 主判活：存活 ∧ 回连才置 flmx_new_ok，restart ok 只在
		// 双过之后可达；没回连单独点名、不冒用 health 文案。
		"if flmx_agent_reconnect_ok; then flmx_new_ok=1",
		"agent upgrade failed: new agent process is alive but did not reconnect to the panel within 60s",
		"if [ \"$flmx_new_ok\" = \"1\" ]; then echo 'agent upgrade restart ok'",
		// 回滚腿目标恒为当前世代备份，走自己的门
		// （flmx_rb_marker_gate，只按标记机制在场与否收放）。
		"flmx_agent_rb_reconnect_ok",
		"flmx_rb_marker_gate",
		"if flmx_agent_health_ok && flmx_agent_rb_reconnect_ok; then echo 'rollback succeeded'",
		// since 在停服之后捕获（旧进程已死）。
		"flmx_reconnect_since=$(date +%s)",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("upgrade script missing %q\n---\n%s", want, script)
		}
	}
	// restart ok 在文本序上必须位于回连判据之后（防未来重构把
	// 成功行挪回无条件分支）。
	gateAt := strings.Index(script, "if flmx_agent_reconnect_ok; then flmx_new_ok=1")
	okAt := strings.Index(script, "agent upgrade restart ok")
	if gateAt < 0 || okAt < 0 || okAt < gateAt {
		t.Fatalf("'agent upgrade restart ok' must be reachable only past the reconnect gate (gate=%d ok=%d)", gateAt, okAt)
	}
}

// --- F-211-01 回归：判活门按目标世代收放 -------------------------
//
// LIVE-211 真机 2d 格：从写标记的世代回退到 2.0.0 的 .old，回退
// 实际完全成功（面板 landed、sha 正确），旧口径却因标记停在旧
// 值烧满 60s 写下假 'rollback failed'；升级路径同机制复本会把
// 已回连的目标误判失败并自动回滚。本组锁定新口径：门的启用按
// 目标二进制世代（实跑目标 -V 判定）——pre-marker 世代与不可
// 判世代一律降级为进程判活＋日志明示，绝不写回连失败。

func TestClassifyAgentTargetMarkerCapability(t *testing.T) {
	cases := []struct {
		version string
		want    agentTargetMarkerCapability
	}{
		{"2.1.1", agentTargetMarkerCapable},
		{"2.1.2", agentTargetMarkerCapable},
		{"2.2.0", agentTargetMarkerCapable},
		{"3.0.0", agentTargetMarkerCapable},
		{"2.1.1-dev.int211", agentTargetMarkerCapable},
		{"2.1.0", agentTargetMarkerIncapable},
		{"2.0.0", agentTargetMarkerIncapable},
		{"1.9.9", agentTargetMarkerIncapable},
		{"2.1.0-dev.int8", agentTargetMarkerIncapable},
		{"2.0.9 (debian/amd64)", agentTargetMarkerIncapable},
		{"", agentTargetMarkerUnknown},
		{"dev", agentTargetMarkerUnknown},
		{"2.1", agentTargetMarkerUnknown},
		{"2.1.1.1", agentTargetMarkerUnknown},
		{"2.1.x", agentTargetMarkerUnknown},
	}
	for _, c := range cases {
		if got := classifyAgentTargetMarkerCapability(c.version); got != c.want {
			t.Fatalf("classify(%q) = %d, want %d", c.version, got, c.want)
		}
	}
}

func TestProbeAgentTargetMarkerCapability(t *testing.T) {
	dir := t.TempDir()
	if cap, v := probeAgentTargetMarkerCapability(writeFakeAgentBinary(t, dir, "fake-200", "2.0.0", "probe")); cap != agentTargetMarkerIncapable || v != "2.0.0" {
		t.Fatalf("2.0.0 binary: got capability=%d version=%q", cap, v)
	}
	if cap, v := probeAgentTargetMarkerCapability(writeFakeAgentBinary(t, dir, "fake-211", "2.1.1", "probe")); cap != agentTargetMarkerCapable || v != "2.1.1" {
		t.Fatalf("2.1.1 binary: got capability=%d version=%q", cap, v)
	}
	if cap, v := probeAgentTargetMarkerCapability(filepath.Join(dir, "missing")); cap != agentTargetMarkerUnknown || v != "" {
		t.Fatalf("missing binary: got capability=%d version=%q", cap, v)
	}
}

// runGateScenario 实跑渲染出的判活门组合（setup＋门函数＋回连
// 检查，窗口压缩到 3s）：preMarker 是停服前标记内容（执行方世
// 代留下），refresh=true 时模拟新进程在窗口内刷新标记。
func runGateScenario(t *testing.T, capability agentTargetMarkerCapability, targetVersion string, preMarker *string, refresh bool) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, agentConnectedMarkerName)
	since := time.Now().Unix()
	if preMarker != nil {
		if err := os.WriteFile(marker, []byte(*preMarker), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if refresh {
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = os.WriteFile(marker, []byte(strconv.FormatInt(since+5, 10)+"\n2.1.1 (debian/amd64)\n"), 0o644)
		}()
	}
	script := agentReconnectCheckFunctionFor(marker, 3, 1) + "\n" +
		agentReconnectGateFunction() + "\n" +
		agentReconnectGateSetupFor(marker, capability, targetVersion) + "\n" +
		"flmx_reconnect_since=" + strconv.FormatInt(since, 10) + "\n" +
		"if flmx_agent_reconnect_ok; then echo GATE-PASS; else echo GATE-FAIL; fi"
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("gate scenario script failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestGateCapableTargetReconnects(t *testing.T) {
	since := time.Now().Unix()
	stale := strconv.FormatInt(since-3600, 10) + "\n2.1.1 (debian/amd64)\n"
	out := runGateScenario(t, agentTargetMarkerCapable, "2.1.1", &stale, true)
	if !strings.Contains(out, "GATE-PASS") {
		t.Fatalf("capable target reconnecting inside the window must pass the gate:\n%s", out)
	}
}

func TestGateCapableTargetNeverReconnects(t *testing.T) {
	since := time.Now().Unix()
	stale := strconv.FormatInt(since-3600, 10) + "\n2.1.1 (debian/amd64)\n"
	out := runGateScenario(t, agentTargetMarkerCapable, "2.1.1", &stale, false)
	if !strings.Contains(out, "GATE-FAIL") {
		t.Fatalf("capable target that never reconnects must fail the gate (real failure stays a failure):\n%s", out)
	}
}

func TestGatePreMarkerTargetDegrades(t *testing.T) {
	// 2d 格的机制复刻：标记是执行方世代留下的旧值、目标
	// （2.0.0）永不刷新——旧口径在此烧满窗口判 FAIL，新口径
	// 必须立即降级通过且日志明示降级原因。
	since := time.Now().Unix()
	stale := strconv.FormatInt(since-3600, 10) + "\n2.1.1 (debian/amd64)\n"
	out := runGateScenario(t, agentTargetMarkerIncapable, "2.0.0", &stale, false)
	if !strings.Contains(out, "GATE-PASS") {
		t.Fatalf("pre-marker target must degrade to a pass, never a reconnect failure:\n%s", out)
	}
	if !strings.Contains(out, "predates the panel connection marker") || !strings.Contains(out, "falls back to process liveness only") {
		t.Fatalf("degradation must be announced in the log with its generation reason:\n%s", out)
	}
}

func TestGateUnknownTargetDegrades(t *testing.T) {
	since := time.Now().Unix()
	stale := strconv.FormatInt(since-3600, 10) + "\n2.1.1 (debian/amd64)\n"
	out := runGateScenario(t, agentTargetMarkerUnknown, "", &stale, false)
	if !strings.Contains(out, "GATE-PASS") {
		t.Fatalf("undeterminable target generation must degrade to a pass (fail-safe away from false failure):\n%s", out)
	}
	if !strings.Contains(out, "could not be determined") || !strings.Contains(out, "falls back to process liveness only") {
		t.Fatalf("degradation must be announced in the log:\n%s", out)
	}
}

func TestDegradedTargetScriptRendering(t *testing.T) {
	// incapable 渲染：主门恒关（无武装段）、降级 warning 在场；
	// 升级脚本的回滚腿门仍按机制在场武装（目标是当前世代）。
	up, err := buildAgentRestartScriptForCap(serviceManagerSystemd,
		"/etc/flmx_agent/.flmx_agent.upgrade-1", "/etc/flmx_agent/flmx_agent", "/etc/flmx_agent/flmx_agent.old",
		agentTargetMarkerIncapable, "2.0.0")
	if err != nil {
		t.Fatalf("buildAgentRestartScriptForCap: %v", err)
	}
	shellSyntaxCheck(t, up)
	if !strings.Contains(up, "predates the panel connection marker") {
		t.Fatalf("upgrade script for a pre-marker target must announce the degradation:\n%s", up)
	}
	if strings.Contains(up, "then flmx_marker_gate=1") {
		t.Fatalf("upgrade script for a pre-marker target must not arm the reconnect gate:\n%s", up)
	}
	if !strings.Contains(up, "flmx_rb_marker_gate=0; if [ -f /etc/flmx_agent/agent-connected.state ]; then flmx_rb_marker_gate=1; fi") {
		t.Fatalf("auto-rollback leg gate must stay armed on mechanism presence regardless of the upgrade target generation:\n%s", up)
	}

	rb, err := buildAgentRollbackScriptForCap(serviceManagerSystemd,
		"/etc/flmx_agent/flmx_agent.old", "/etc/flmx_agent/flmx_agent",
		agentTargetMarkerIncapable, "2.0.0")
	if err != nil {
		t.Fatalf("buildAgentRollbackScriptForCap: %v", err)
	}
	shellSyntaxCheck(t, rb)
	if !strings.Contains(rb, "predates the panel connection marker") || strings.Contains(rb, "then flmx_marker_gate=1") {
		t.Fatalf("rollback script for a pre-marker backup must degrade with the warning and no armed gate:\n%s", rb)
	}
	// 'rollback restart ok' 仍只在判活（此时＝进程判活∧恒过的
	// 门）之后可达，假失败行只在门后——降级不是无条件成功。
	if !strings.Contains(rb, "if flmx_agent_reconnect_ok; then echo 'rollback restart ok'") {
		t.Fatalf("rollback script must keep the verdict behind the (degraded) gate:\n%s", rb)
	}

	unk, err := buildAgentRollbackScriptForCap(serviceManagerSystemd,
		"/etc/flmx_agent/flmx_agent.old", "/etc/flmx_agent/flmx_agent",
		agentTargetMarkerUnknown, "")
	if err != nil {
		t.Fatalf("buildAgentRollbackScriptForCap: %v", err)
	}
	shellSyntaxCheck(t, unk)
	if !strings.Contains(unk, "could not be determined") {
		t.Fatalf("rollback script for an undeterminable target must announce the degradation:\n%s", unk)
	}
}

func TestProbeDrivenScriptDegradation(t *testing.T) {
	// 包装构建器实探目标文件：假 2.0.0 备份 → 回退脚本降级；
	// 假 2.1.1 暂存 → 升级脚本武装（标记缺失警告形态）。
	dir := t.TempDir()
	backup := writeFakeAgentBinary(t, dir, "flmx_agent.old", "2.0.0", "backup")
	rb, err := buildAgentRollbackScriptFor(serviceManagerSystemd, backup, filepath.Join(dir, "flmx_agent"))
	if err != nil {
		t.Fatalf("buildAgentRollbackScriptFor: %v", err)
	}
	if !strings.Contains(rb, "predates the panel connection marker") {
		t.Fatalf("rollback script built for a probed 2.0.0 backup must degrade:\n%s", rb)
	}

	staged := writeFakeAgentBinary(t, dir, "staged", "2.1.1", "staged")
	up, err := buildAgentRestartScriptFor(serviceManagerSystemd, staged, filepath.Join(dir, "flmx_agent"), backup)
	if err != nil {
		t.Fatalf("buildAgentRestartScriptFor: %v", err)
	}
	if !strings.Contains(up, "panel connection marker not found") || strings.Contains(up, "predates the panel connection marker") {
		t.Fatalf("upgrade script built for a probed 2.1.1 target must arm the gate (marker-presence form):\n%s", up)
	}
}

func TestRollbackScriptReconnectGateRendering(t *testing.T) {
	script, err := buildAgentRollbackScriptFor(serviceManagerSystemd,
		"/etc/flmx_agent/flmx_agent.old", "/etc/flmx_agent/flmx_agent")
	if err != nil {
		t.Fatalf("buildAgentRollbackScriptFor: %v", err)
	}
	shellSyntaxCheck(t, script)
	for _, want := range []string{
		"flmx_agent_reconnected_ok",
		"/etc/flmx_agent/agent-connected.state",
		"if flmx_agent_reconnect_ok; then echo 'rollback restart ok'",
		"rollback failed: restored agent process is alive but did not reconnect to the panel within 60s",
		"flmx_reconnect_since=$(date +%s)",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("rollback script missing %q\n---\n%s", want, script)
		}
	}
}
