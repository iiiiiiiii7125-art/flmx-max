package socket

import (
	"path/filepath"
	"strings"
	"testing"
)

// 改名双认（RN-2）：面板下发的升级/回退/自卸载脚本必须按节点实际
// 布局生成服务命令——现行布局（flmx_agent）用新服务名，存量未迁
// 布局（flux_agent）仍用它自己的旧服务名，两个世代互不串扰。

func TestAgentServiceNameFor(t *testing.T) {
	cases := map[string]string{
		"/etc/flmx_agent/flmx_agent": agentInstallName,
		"/etc/flux_agent/flux_agent": legacyAgentInstallName,
		"/usr/local/bin/flmx-agent":  agentInstallName, // 容器形态二进制名不是布局名 → 现行默认
		"/opt/other/tool":            agentInstallName,
	}
	for path, want := range cases {
		if got := agentServiceNameFor(path); got != want {
			t.Fatalf("agentServiceNameFor(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestBuildAgentRestartScriptNewLayout(t *testing.T) {
	script, err := buildAgentRestartScriptFor(serviceManagerSystemd,
		"/etc/flmx_agent/.flmx_agent.upgrade-1", "/etc/flmx_agent/flmx_agent", "/etc/flmx_agent/flmx_agent.old")
	if err != nil {
		t.Fatalf("build restart script: %v", err)
	}
	for _, want := range []string{
		"systemctl stop flmx_agent",
		"systemctl start flmx_agent",
		"systemctl reset-failed flmx_agent",
		"systemctl show -p MainPID --value flmx_agent",
		"systemctl is-active --quiet flmx_agent",
		".flmx_agent.upgrade-rollback-",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("new-layout script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "flux_agent") {
		t.Fatalf("new-layout script must not reference the legacy service:\n%s", script)
	}
}

func TestBuildAgentRestartScriptNewLayoutOpenRC(t *testing.T) {
	script, err := buildAgentRestartScriptFor(serviceManagerOpenRC,
		"/etc/flmx_agent/.flmx_agent.upgrade-1", "/etc/flmx_agent/flmx_agent", "/etc/flmx_agent/flmx_agent.old")
	if err != nil {
		t.Fatalf("build restart script: %v", err)
	}
	for _, want := range []string{
		"rc-service flmx_agent stop",
		"rc-service flmx_agent start",
		"/run/flmx_agent.pid",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("new-layout openrc script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "flux_agent") {
		t.Fatalf("new-layout openrc script must not reference the legacy service:\n%s", script)
	}
}

func TestBuildAgentRollbackScriptNewLayout(t *testing.T) {
	script, err := buildAgentRollbackScriptFor(serviceManagerSystemd,
		"/etc/flmx_agent/flmx_agent.old", "/etc/flmx_agent/flmx_agent")
	if err != nil {
		t.Fatalf("build rollback script: %v", err)
	}
	for _, want := range []string{
		"systemctl stop flmx_agent",
		"systemctl reset-failed flmx_agent",
		".flmx_agent.rollback-",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("new-layout rollback script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "flux_agent") {
		t.Fatalf("new-layout rollback script must not reference the legacy service:\n%s", script)
	}
}

func TestBuildAgentUninstallScriptNewLayout(t *testing.T) {
	script, err := buildAgentUninstallScript(serviceManagerSystemd, "/etc/flmx_agent/flmx_agent")
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	for _, want := range []string{
		"systemctl stop flmx_agent",
		"systemctl disable flmx_agent",
		"rm -f '/etc/systemd/system/flmx_agent.service'",
		"rm -rf '/etc/flmx_agent'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("new-layout uninstall script missing %q:\n%s", want, script)
		}
	}
}

// 世代错配的路径不是任何真实布局（目录名与二进制名必须同世代），
// 一律拒绝，防止把别代的目录当成本代的删除目标。
func TestBuildAgentUninstallScriptRejectsMixedLayouts(t *testing.T) {
	for _, path := range []string{
		"/etc/flmx_agent/flux_agent",
		"/etc/flux_agent/flmx_agent",
		"/usr/local/bin/flmx_agent", // 名字对、共享目录
	} {
		if _, err := buildAgentUninstallScript(serviceManagerSystemd, path); err == nil {
			t.Fatalf("path %q must be rejected", path)
		}
	}
}

// resolveAgentBinaryPath 以本进程的可执行路径为准锚定布局：
// 两个世代的自路径都必须原样命中（面板原地升级不换布局）。
func TestResolveAgentBinaryPathFromExecutable(t *testing.T) {
	orig := uninstallBinaryPath
	t.Cleanup(func() { uninstallBinaryPath = orig })
	for _, exe := range []string{"/etc/flmx_agent/flmx_agent", "/etc/flux_agent/flux_agent"} {
		uninstallBinaryPath = func() (string, error) { return exe, nil }
		if got := resolveAgentBinaryPath(); got != exe {
			t.Fatalf("resolveAgentBinaryPath with exe %q = %q", exe, got)
		}
	}
	// 布局外的可执行路径（如手工拷贝运行）：不得锚到布局外路径，
	// 结果只能是某个受认布局路径。
	uninstallBinaryPath = func() (string, error) { return "/opt/other/tool", nil }
	got := resolveAgentBinaryPath()
	if filepath.Dir(got) == "/opt/other" || !isAgentInstallName(filepath.Base(got)) {
		t.Fatalf("foreign executable must resolve to a recognized layout, got %q", got)
	}
}
