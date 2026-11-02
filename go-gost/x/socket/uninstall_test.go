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

// 容器形态的数据目录清理（镜像布局）：数据目录与二进制目录分离
// （/etc/flmx_agent vs /usr/local/bin），以数据目录为锚清空——只留
// 标记，配置（含 secret）与内核目录必须清掉，二进制本体在目录外
// 不受影响。
func TestWipeDirForUninstallImageLayout(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "etc", "flmx_agent")
	binPath := filepath.Join(root, "usr", "local", "bin", "flmx-agent")
	if err := os.MkdirAll(filepath.Join(dir, "kernels"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"config.json":         `{"secret":"s"}`,
		"gost.json":           "{}",
		"kernels/state.json":  "{}",
		"flmx_agent_seq.json": "{}",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := wipeDirForUninstall(dir, binPath); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != uninstallMarker {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("only the marker may survive in the data dir, got %v", names)
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("binary outside the data dir must be untouched: %v", err)
	}
}

// 同目录布局（二进制恰在数据目录内）：保留二进制本体，其余清掉。
func TestWipeDirForUninstallCoLocatedBinary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flux_agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(dir, "flux_agent")
	for name, content := range map[string]string{
		"flux_agent":  "binary",
		"config.json": `{"secret":"s"}`,
		"gost.json":   "{}",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := wipeDirForUninstall(dir, binPath); err != nil {
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
}

// 数据目录指纹不命中时拒绝动手：共享 bin 目录、任意其他目录名、
// 相对路径都不能成为删除目标。
func TestWipeDirForUninstallRejectsForeignDirs(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), "usr", "local", "bin", "flmx-agent")
	for _, dir := range []string{
		filepath.Join(t.TempDir(), "bin"),   // 共享 bin 目录
		filepath.Join(t.TempDir(), "other"), // 无法识别的目录名
		"flmx_agent",                        // 相对路径
	} {
		if err := wipeDirForUninstall(dir, binPath); err == nil {
			t.Fatalf("dir %q must be rejected", dir)
		}
	}
}

type uninstallHookStubs struct {
	order   []string
	script  string
	manager serviceManager
	exit    int
}

func stubUninstallHooks(t *testing.T, binaryPath string, inContainer bool) *uninstallHookStubs {
	return stubUninstallHooksWithDataDir(t, binaryPath, "", inContainer)
}

// stubUninstallHooksWithDataDir 在 stubUninstallHooks 之外还把进程
// 工作目录（数据目录锚点）桩到 dataDir——容器形态测试据此模拟
// entrypoint 已 cd 到数据目录的真实运行环境。
func stubUninstallHooksWithDataDir(t *testing.T, binaryPath, dataDir string, inContainer bool) *uninstallHookStubs {
	t.Helper()
	s := &uninstallHookStubs{exit: -1}
	origRestore, origKernels, origExit := uninstallRestoreTuning, uninstallKernels, uninstallExit
	origBinary, origContainer := uninstallBinaryPath, uninstallInContainer
	origWorkingDir := uninstallWorkingDir
	origDetect, origLaunch := uninstallDetectManager, uninstallLaunchScript
	uninstallRestoreTuning = func() error { s.order = append(s.order, "restore"); return nil }
	uninstallKernels = func() error { s.order = append(s.order, "kernels"); return nil }
	uninstallExit = func(code int) { s.exit = code }
	uninstallBinaryPath = func() (string, error) { return binaryPath, nil }
	uninstallWorkingDir = func() (string, error) { return dataDir, nil }
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
		uninstallWorkingDir = origWorkingDir
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

// 容器形态（镜像真实布局）：二进制在 usr/local/bin、数据目录是
// 独立的 flmx_agent（entrypoint 的 cd 目标，config.json 所在处）。
// 卸载须清空数据目录（留标记）后以 0 退出，不启动任何脚本；被重
// 启策略拉起后 AgentUninstalled 须凭数据目录里的标记命中。
func TestRunAgentUninstallContainerWipesAndExits(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "etc", "flmx_agent")
	binaryPath := filepath.Join(root, "usr", "local", "bin", "flmx-agent")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"config.json": `{"addr":"wss://panel:443","secret":"s"}`,
		"gost.json":   "{}",
	} {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := stubUninstallHooksWithDataDir(t, binaryPath, dataDir, true)
	runAgentUninstall()
	if s.exit != 0 {
		t.Fatalf("container flow must exit 0, got %d (order %v)", s.exit, s.order)
	}
	if strings.Contains(strings.Join(s.order, ","), "launch") {
		t.Fatalf("container flow must not launch a script: %v", s.order)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("config.json must be wiped")
	}
	if _, err := os.Stat(filepath.Join(dataDir, uninstallMarker)); err != nil {
		t.Fatalf("marker must exist in the data dir: %v", err)
	}
	if _, err := os.Stat(binaryPath); err != nil {
		t.Fatalf("binary must be untouched: %v", err)
	}
	// 重启拉起后的启动拦截：同一组钩子仍在位，标记须命中。
	if !AgentUninstalled() {
		t.Fatal("after container uninstall, AgentUninstalled must report true")
	}
}

// 容器形态但数据目录指纹不命中（工作目录不是 flmx_agent/
// flux_agent）：拒绝清理、工作目录内容不动，仍以 0 退出——调优恢
// 复与内核清理已先行完成，卸载语义不因目录异常回滚，但不许误删
// 无法识别的目录。
func TestRunAgentUninstallContainerRejectsForeignDataDir(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "srv", "other")
	binaryPath := filepath.Join(root, "usr", "local", "bin", "flmx-agent")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := stubUninstallHooksWithDataDir(t, binaryPath, dataDir, true)
	runAgentUninstall()
	if s.exit != 0 {
		t.Fatalf("container flow must still exit 0, got %d", s.exit)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "config.json")); err != nil {
		t.Fatal("foreign data dir contents must not be touched")
	}
	if _, err := os.Stat(filepath.Join(dataDir, uninstallMarker)); !os.IsNotExist(err) {
		t.Fatal("marker must not be written into a foreign dir")
	}
}

// 标记判定：主程序据此在配置加载前拦截已被卸载的容器 agent。
// 正本落点是数据目录；二进制同目录的标记（存量同目录布局）仍认。
func TestAgentUninstalledMarker(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "etc", "flmx_agent")
	binDir := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origBinary, origWorkingDir := uninstallBinaryPath, uninstallWorkingDir
	uninstallBinaryPath = func() (string, error) { return filepath.Join(binDir, "flmx-agent"), nil }
	uninstallWorkingDir = func() (string, error) { return dataDir, nil }
	t.Cleanup(func() {
		uninstallBinaryPath = origBinary
		uninstallWorkingDir = origWorkingDir
	})

	if AgentUninstalled() {
		t.Fatal("no marker yet, must report false")
	}
	// 数据目录落点（镜像布局的正本）。
	if err := os.WriteFile(filepath.Join(dataDir, uninstallMarker), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !AgentUninstalled() {
		t.Fatal("marker in data dir, must report true")
	}
	if err := os.Remove(filepath.Join(dataDir, uninstallMarker)); err != nil {
		t.Fatal(err)
	}
	if AgentUninstalled() {
		t.Fatal("marker removed, must report false")
	}
	// 二进制同目录落点（存量同目录布局兼容）。
	if err := os.WriteFile(filepath.Join(binDir, uninstallMarker), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !AgentUninstalled() {
		t.Fatal("marker next to binary, must report true")
	}
}
