package socket

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 自卸载脚本的圈定范围：systemd 形态必须依次停服务、禁用自启、删
// unit、重载、删安装目录，且目标只能是安装指纹命中的目录。
func TestBuildAgentUninstallScriptSystemd(t *testing.T) {
	script, err := buildAgentUninstallScript(serviceManagerSystemd, "/etc/flux_agent/flux_agent")
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	for _, want := range []string{
		"systemctl stop flux_agent",
		"systemctl disable flux_agent",
		"rm -f '/etc/systemd/system/flux_agent.service'",
		"systemctl daemon-reload",
		"rm -rf '/etc/flux_agent'",
		"agent-uninstall.log",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
}

func TestBuildAgentUninstallScriptOpenRC(t *testing.T) {
	script, err := buildAgentUninstallScript(serviceManagerOpenRC, "/etc/flux_agent/flux_agent")
	if err != nil {
		t.Fatalf("build script: %v", err)
	}
	for _, want := range []string{
		"rc-service flux_agent stop",
		"rc-update del flux_agent default",
		"rm -f '/etc/init.d/flux_agent'",
		"rm -rf '/etc/flux_agent'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "systemctl") {
		t.Fatalf("openrc script must not reference systemctl:\n%s", script)
	}
}

// 身份指纹不命中的路径一律拒绝：共享目录、同居的其他程序、相对
// 路径都不能成为删除目标。
func TestBuildAgentUninstallScriptRejectsForeignInstalls(t *testing.T) {
	cases := []string{
		"/usr/local/bin/flux_agent",      // 共享 bin 目录
		"/etc/flux_agent/gost",           // 目录对、二进制不是 agent
		"/opt/agent/flux_agent",          // 目录名不是 agent 名
		"/etc/flux_agent/bin/flux_agent", // 二进制不在安装目录根部
		"flux_agent/flux_agent",          // 相对路径
		"/flux_agent",                    // 根目录下的散装二进制
	}
	for _, path := range cases {
		if _, err := buildAgentUninstallScript(serviceManagerSystemd, path); err == nil {
			t.Fatalf("path %q must be rejected", path)
		}
	}
	if _, err := buildAgentUninstallScript(serviceManager("bogus"), "/etc/flux_agent/flux_agent"); err == nil {
		t.Fatal("unknown service manager must be rejected")
	}
}

// 容器形态的目录清理：只留标记与二进制，配置（含 secret）与内核
// 目录必须清掉；目录指纹不命中时拒绝动手。
func TestWipeDirForUninstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flux_agent")
	if err := os.MkdirAll(filepath.Join(dir, "kernels"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"flux_agent":          "binary",
		"config.json":         `{"secret":"s"}`,
		"gost.json":           "{}",
		"kernels/state.json":  "{}",
		"flmx_agent_seq.json": "{}",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := wipeDirForUninstall(dir, "flux_agent"); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	if len(got) != 2 || !got[uninstallMarker] || !got["flux_agent"] {
		t.Fatalf("only marker and binary may survive, got %v", got)
	}

	if err := wipeDirForUninstall(filepath.Join(t.TempDir(), "other"), "flux_agent"); err == nil {
		t.Fatal("foreign directory must be rejected")
	}
	if err := wipeDirForUninstall(dir, "gost"); err == nil {
		t.Fatal("foreign binary name must be rejected")
	}
}

type uninstallHookStubs struct {
	order   []string
	script  string
	manager serviceManager
	exit    int
}

func stubUninstallHooks(t *testing.T, binaryPath string, inContainer bool) *uninstallHookStubs {
	t.Helper()
	s := &uninstallHookStubs{exit: -1}
	origRestore, origKernels, origExit := uninstallRestoreTuning, uninstallKernels, uninstallExit
	origBinary, origContainer := uninstallBinaryPath, uninstallInContainer
	origDetect, origLaunch := uninstallDetectManager, uninstallLaunchScript
	uninstallRestoreTuning = func() error { s.order = append(s.order, "restore"); return nil }
	uninstallKernels = func() error { s.order = append(s.order, "kernels"); return nil }
	uninstallExit = func(code int) { s.exit = code }
	uninstallBinaryPath = func() (string, error) { return binaryPath, nil }
	uninstallInContainer = func() bool { return inContainer }
	uninstallDetectManager = func() (serviceManager, error) { return serviceManagerSystemd, nil }
	uninstallLaunchScript = func(m serviceManager, script string) error {
		s.order = append(s.order, "launch")
		s.manager = m
		s.script = script
		return nil
	}
	t.Cleanup(func() {
		uninstallRestoreTuning, uninstallKernels, uninstallExit = origRestore, origKernels, origExit
		uninstallBinaryPath, uninstallInContainer = origBinary, origContainer
		uninstallDetectManager, uninstallLaunchScript = origDetect, origLaunch
	})
	return s
}

// 原生形态全流程顺序：先恢复调优、再清内核/nft、最后启动卸载脚本；
// 进程本身不直接退出（由脚本停服务带走）。
func TestRunAgentUninstallNativeOrder(t *testing.T) {
	s := stubUninstallHooks(t, "/etc/flux_agent/flux_agent", false)
	runAgentUninstall()
	if strings.Join(s.order, ",") != "restore,kernels,launch" {
		t.Fatalf("unexpected step order: %v", s.order)
	}
	if s.manager != serviceManagerSystemd {
		t.Fatalf("manager = %q", s.manager)
	}
	if !strings.Contains(s.script, "rm -rf '/etc/flux_agent'") {
		t.Fatalf("launched script does not remove the install dir:\n%s", s.script)
	}
	if s.exit != -1 {
		t.Fatalf("native flow must not exit directly, got exit %d", s.exit)
	}
}

// 容器形态：清目录（留标记+二进制）后以 0 退出，不启动任何脚本。
func TestRunAgentUninstallContainerWipesAndExits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flux_agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(dir, "flux_agent")
	if err := os.WriteFile(binaryPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := stubUninstallHooks(t, binaryPath, true)
	runAgentUninstall()
	if s.exit != 0 {
		t.Fatalf("container flow must exit 0, got %d (order %v)", s.exit, s.order)
	}
	if strings.Contains(strings.Join(s.order, ","), "launch") {
		t.Fatalf("container flow must not launch a script: %v", s.order)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("config.json must be wiped")
	}
	if _, err := os.Stat(filepath.Join(dir, uninstallMarker)); err != nil {
		t.Fatalf("marker must exist: %v", err)
	}
}

// 标记判定：主程序据此在配置加载前拦截已被卸载的容器 agent。
func TestAgentUninstalledMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flux_agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := uninstallBinaryPath
	uninstallBinaryPath = func() (string, error) { return filepath.Join(dir, "flux_agent"), nil }
	t.Cleanup(func() { uninstallBinaryPath = orig })

	if AgentUninstalled() {
		t.Fatal("no marker yet, must report false")
	}
	if err := os.WriteFile(filepath.Join(dir, uninstallMarker), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !AgentUninstalled() {
		t.Fatal("marker present, must report true")
	}
}
