package socket

import (
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
// F-R2B-V2-05：布局外的可执行路径（手工拷贝/ scratch 运行）锚
// 定自身——永不兜底到另一份安装的路径；名字不可认时拒绝。
func TestResolveAgentBinaryPathFromExecutable(t *testing.T) {
	orig := uninstallBinaryPath
	t.Cleanup(func() { uninstallBinaryPath = orig })
	for _, exe := range []string{"/etc/flmx_agent/flmx_agent", "/etc/flux_agent/flux_agent"} {
		uninstallBinaryPath = func() (string, error) { return exe, nil }
		got, err := resolveAgentBinaryPath()
		if err != nil {
			t.Fatalf("resolveAgentBinaryPath with exe %q: %v", exe, err)
		}
		if got != exe {
			t.Fatalf("resolveAgentBinaryPath with exe %q = %q", exe, got)
		}
	}
	// 布局外的可执行路径（如手工拷贝/ scratch 运行）：锚定自身，
	// 不得兜底到 /etc 下另一份安装。
	for _, exe := range []string{"/root/scratch-r2b2-v2/flmx_agent", "/root/scratch/flmx_agent/flmx_agent", "/opt/other/flmx_agent"} {
		uninstallBinaryPath = func() (string, error) { return exe, nil }
		got, err := resolveAgentBinaryPath()
		if err != nil {
			t.Fatalf("resolveAgentBinaryPath with exe %q: %v", exe, err)
		}
		if got != exe {
			t.Fatalf("scratch executable must resolve to itself, got %q", got)
		}
	}
	// 名字不可认的可执行路径：拒绝，不猜布局。
	uninstallBinaryPath = func() (string, error) { return "/opt/other/tool", nil }
	if _, err := resolveAgentBinaryPath(); err == nil {
		t.Fatalf("foreign executable must be rejected, got no error")
	}
}
