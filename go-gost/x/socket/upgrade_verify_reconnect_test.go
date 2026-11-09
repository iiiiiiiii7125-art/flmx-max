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
	script, err := buildAgentRestartScriptFor(serviceManagerSystemd,
		"/etc/flmx_agent/.flmx_agent.upgrade-1", "/etc/flmx_agent/flmx_agent", "/etc/flmx_agent/flmx_agent.old")
	if err != nil {
		t.Fatalf("buildAgentRestartScriptFor: %v", err)
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
		// 回滚腿成功判同口径。
		"if flmx_agent_health_ok && flmx_agent_reconnect_ok; then echo 'rollback succeeded'",
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
