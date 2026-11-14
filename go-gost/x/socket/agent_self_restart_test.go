package socket

import (
	"strings"
	"testing"
)

// F-R2B-V2-05: agent 升级/回滚/卸载不认安装位置，会升级 live 安装。
// install.sh 只在 /etc/<name>/<name> 落盘并建同名 unit，故只有该
// 精确路径是服务管理安装；其余路径（scratch/手工运行）没有属于
// 自己的服务，重启绝不能走布局服务名。
func TestAgentInstallServiceManaged(t *testing.T) {
	cases := map[string]bool{
		"/etc/flmx_agent/flmx_agent":          true,
		"/etc/flux_agent/flux_agent":          true,
		"/root/scratch/flmx_agent":            false,
		"/root/scratch/flmx_agent/flmx_agent": false,
		"/opt/other/flmx_agent":               false,
		"/usr/local/bin/flmx_agent":           false,
		"/etc/flmx_agent/flux_agent":          false,
	}
	for path, want := range cases {
		if got := agentInstallServiceManaged(path); got != want {
			t.Fatalf("agentInstallServiceManaged(%q) = %v, want %v", path, got, want)
		}
	}
}

func selfCtxForTest() *agentSelfRestartContext {
	return &agentSelfRestartContext{pid: 12345, args: nil, workDir: "/root/scratch-r2b2-high"}
}

// 自重启版升级脚本：只杀、只起本进程，永不出现任何服务命令与
// /etc 安装路径；回连标记锚在本进程自己的目录。
func TestBuildAgentRestartScriptSelfManaged(t *testing.T) {
	script, err := buildAgentRestartScriptSelf(
		"/root/scratch-r2b2-high/.flmx_agent.upgrade-1",
		"/root/scratch-r2b2-high/flmx_agent",
		"/root/scratch-r2b2-high/flmx_agent.old",
		selfCtxForTest())
	if err != nil {
		t.Fatalf("build self restart script: %v", err)
	}
	for _, want := range []string{
		"kill 12345",
		"flmx_start_agent",
		"cd '/root/scratch-r2b2-high'",
		"nohup '/root/scratch-r2b2-high/flmx_agent'",
		"/root/scratch-r2b2-high/agent-connected.state",
		"/root/scratch-r2b2-high/agent-upgrade.log",
		"flmx_agent_health_ok",
		"agent upgrade restart ok",
		"rollback succeeded",
		"if mv /root/scratch-r2b2-high/.flmx_agent.upgrade-1 /root/scratch-r2b2-high/flmx_agent; then",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("self restart script missing %q:\n%s", want, script)
		}
	}
	for _, banned := range []string{"systemctl", "rc-service", "/etc/flmx_agent", "/etc/flux_agent"} {
		if strings.Contains(script, banned) {
			t.Fatalf("self restart script must not reference %q:\n%s", banned, script)
		}
	}
}

// 自重启版回退脚本：同口径——只杀、只起本进程。
func TestBuildAgentRollbackScriptSelfManaged(t *testing.T) {
	script, err := buildAgentRollbackScriptSelf(
		"/root/scratch-r2b2-high/flmx_agent.old",
		"/root/scratch-r2b2-high/flmx_agent",
		selfCtxForTest())
	if err != nil {
		t.Fatalf("build self rollback script: %v", err)
	}
	for _, want := range []string{
		"kill 12345",
		"flmx_start_agent",
		"cd '/root/scratch-r2b2-high'",
		"nohup '/root/scratch-r2b2-high/flmx_agent'",
		"/root/scratch-r2b2-high/agent-connected.state",
		"rollback restart ok",
		"rollback start failed",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("self rollback script missing %q:\n%s", want, script)
		}
	}
	for _, banned := range []string{"systemctl", "rc-service", "/etc/flmx_agent", "/etc/flux_agent"} {
		if strings.Contains(script, banned) {
			t.Fatalf("self rollback script must not reference %q:\n%s", banned, script)
		}
	}
}

// 自重启带参：启动参数原样带回新进程。
func TestBuildAgentRestartScriptSelfManagedWithArgs(t *testing.T) {
	self := &agentSelfRestartContext{pid: 12345, args: []string{"-C", "gost.json"}, workDir: "/root/scratch-r2b2-high"}
	script, err := buildAgentRestartScriptSelf(
		"/root/scratch-r2b2-high/.flmx_agent.upgrade-1",
		"/root/scratch-r2b2-high/flmx_agent",
		"/root/scratch-r2b2-high/flmx_agent.old",
		self)
	if err != nil {
		t.Fatalf("build self restart script: %v", err)
	}
	if !strings.Contains(script, "nohup '/root/scratch-r2b2-high/flmx_agent' '-C' 'gost.json'") {
		t.Fatalf("self restart script must relaunch with the original args:\n%s", script)
	}
}
