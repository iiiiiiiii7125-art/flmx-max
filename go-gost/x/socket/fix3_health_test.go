package socket

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// R4-N2 回归：升级重启脚本旧健康检查在 start 后 sleep 3 单次采样
// is-active，恰撞「服务内 1s 后 exit 1」的 systemd 存活窗口（flap），
// 误记 restart ok、不回滚，随后服务撞 start-rate limit 变 failed。
// 修复口径：间隔内两次采样（is-active 且主 PID），两次都活且 PID
// 非空、非 0、两次一致才判活，否则走自动回滚。
//
// 本测试以桩命令实跑生成的 sh 函数 agentHealthCheckFunctionFor：
// _active 按调用计数在第 MAXOK 次后转失败，_pid 按序输出 PIDS 文件
// 的各行，覆盖判活的四个关键场景。

func runHealthCheck(t *testing.T, maxOK int, pids []string) string {
	t.Helper()
	dir := t.TempDir()
	cnt := filepath.Join(dir, "cnt")
	idx := filepath.Join(dir, "idx")
	pidf := filepath.Join(dir, "pids")
	if err := os.WriteFile(cnt, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idx, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidf, []byte(strings.Join(pids, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	prelude := `
CNT="` + cnt + `"; IDX="` + idx + `"; PIDS="` + pidf + `"; MAXOK=` + strconv.Itoa(maxOK) + `
_active() { n=$(cat "$CNT"); n=$((n+1)); echo "$n" > "$CNT"; [ "$n" -le "$MAXOK" ]; }
_pid() { n=$(cat "$IDX"); n=$((n+1)); echo "$n" > "$IDX"; sed -n "${n}p" "$PIDS"; }
`
	script := prelude + agentHealthCheckFunctionFor("_active", "_pid", 0) +
		"\nif flmx_agent_health_ok; then echo STABLE; else echo UNSTABLE; fi"
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("health check script failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestAgentHealthCheckStableService(t *testing.T) {
	if got := runHealthCheck(t, 99, []string{"4242", "4242"}); got != "STABLE" {
		t.Fatalf("healthy stable service: got %q, want STABLE", got)
	}
}

func TestAgentHealthCheckDiesBeforeSecondSample(t *testing.T) {
	// 首次 active、二次已死（R4-N2 的 flap 形态）：必须判不活。
	if got := runHealthCheck(t, 1, []string{"4242", "4242"}); got != "UNSTABLE" {
		t.Fatalf("service dead at second sample: got %q, want UNSTABLE", got)
	}
}

func TestAgentHealthCheckPIDChanges(t *testing.T) {
	// 两次采样间 PID 变化 = 期间已重启（flap 中）：必须判不活。
	if got := runHealthCheck(t, 99, []string{"1111", "2222"}); got != "UNSTABLE" {
		t.Fatalf("pid changed between samples: got %q, want UNSTABLE", got)
	}
}

func TestAgentHealthCheckPIDZeroOrEmpty(t *testing.T) {
	if got := runHealthCheck(t, 99, []string{"0", "0"}); got != "UNSTABLE" {
		t.Fatalf("pid 0 must not count as alive: got %q", got)
	}
	if got := runHealthCheck(t, 99, []string{}); got != "UNSTABLE" {
		t.Fatalf("missing pid must not count as alive: got %q", got)
	}
}

// 生产脚本形态：健康函数与 PID 命令须按服务管理器正确内联。
func TestAgentRestartScriptUsesStableHealthCheck(t *testing.T) {
	script, err := buildAgentRestartScriptFor(serviceManagerSystemd, "/etc/flux_agent/flux_agent.new", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent.old")
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	for _, want := range []string{
		"flmx_agent_health_ok()",
		"systemctl show -p MainPID --value flux_agent",
		"systemctl is-active --quiet flux_agent",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("systemd restart script missing %q:\n%s", want, script)
		}
	}
	if strings.Count(script, "flmx_agent_health_ok") < 3 {
		t.Fatalf("restart script should define and use the health check for upgrade and rollback verification:\n%s", script)
	}

	script, err = buildAgentRestartScriptFor(serviceManagerOpenRC, "/etc/flux_agent/flux_agent.new", "/etc/flux_agent/flux_agent", "/etc/flux_agent/flux_agent.old")
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	for _, want := range []string{
		"flmx_agent_health_ok()",
		"/run/flux_agent.pid", // install.sh 的 OpenRC pidfile 约定
		"rc-service flux_agent status",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("openrc restart script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "systemctl") {
		t.Fatalf("openrc restart script must not reference systemd:\n%s", script)
	}
}
