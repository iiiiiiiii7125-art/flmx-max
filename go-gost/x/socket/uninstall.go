package socket

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-gost/x/kernel"
)

// 面板删除服务器时下发 UninstallAgent，agent 在本机完成自卸载：
// 恢复调优前的系统参数 → 停内核并删除 agent 建的 nftables 表 →
// 停用并删除服务与安装目录。脚本安装形态走脱离会话的卸载脚本；
// 容器形态删不掉自己，改为清空数据目录并留标记后退出（见
// wipeDirForUninstall）。两种形态的清理范围都按布局指纹严格圈定，
// 绝不误删同机其他程序的文件。

// agentInstallName 是 install.sh 落盘的二进制名与安装目录名
// （/etc/flux_agent/flux_agent），是自卸载圈定范围的身份指纹。
const agentInstallName = "flux_agent"

// agentDataDirName 是容器镜像形态的数据目录名（Dockerfile 的
// WORKDIR /etc/flmx_agent，entrypoint cd 于此、config.json 在此）。
// 镜像布局下二进制在 /usr/local/bin、与数据目录分离，故容器形态
// 的圈定指纹认数据目录名，不再认二进制目录/二进制名。
const agentDataDirName = "flmx_agent"

// uninstallMarker 在容器形态下留在安装目录里：容器删不掉自己，被
// 重启策略拉起的 agent 凭它空转等待人工删除容器，不再连接面板。
const uninstallMarker = ".uninstalled"

var uninstallStarted atomic.Bool

// 测试接缝：单测替换这些钩子，绝不触碰真实安装。
var (
	uninstallRestoreTuning = restoreTuningForUninstall
	uninstallKernels       = func() error { return kernel.Default().Uninstall() }
	uninstallExit          = os.Exit
	uninstallBinaryPath    = os.Executable
	uninstallWorkingDir    = os.Getwd
	uninstallInContainer   = InContainer
	uninstallDetectManager = detectServiceManager
	uninstallLaunchScript  = launchAgentControlScript
)

// agentDataDir 返回数据目录，即 config.json 所在目录：主程序以
// 相对路径加载 ./config.json，故进程工作目录就是数据目录（脚本
// 形态的服务 WorkingDirectory、容器形态 entrypoint 的 cd 目标都
// 指向它）。容器形态的卸载标记与清理范围以此为锚。
func agentDataDir() (string, error) {
	wd, err := uninstallWorkingDir()
	if err != nil {
		return "", err
	}
	return filepath.Clean(wd), nil
}

// AgentUninstalled 报告本 agent 是否已被面板卸载（容器形态的标记
// 文件存在）。主程序在加载配置前检查：配置已随卸载清除，凭标记空
// 转即可，不应以「配置缺失」崩溃重启。
//
// 标记正本落点在数据目录（wipeDirForUninstall 写入处）；二进制同
// 目录保留为兼容查（存量同目录布局曾把标记写在二进制旁）。
func AgentUninstalled() bool {
	if dir, err := agentDataDir(); err == nil {
		if _, err := os.Stat(filepath.Join(dir, uninstallMarker)); err == nil {
			return true
		}
	}
	exe, err := uninstallBinaryPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(filepath.Dir(exe), uninstallMarker))
	return err == nil
}

// handleUninstallAgent 先返回（派发层随即回执面板），卸载在协程里
// 延迟执行，避免回执还没发出去连接就被自己的卸载动作掐断。重复
// 下发只执行一次。
func (w *WebSocketReporter) handleUninstallAgent() error {
	if !uninstallStarted.CompareAndSwap(false, true) {
		return nil
	}
	go func() {
		time.Sleep(time.Second)
		runAgentUninstall()
	}()
	return nil
}

func runAgentUninstall() {
	fmt.Println("🗑️ 面板已删除此服务器，开始卸载 agent...")
	if err := uninstallRestoreTuning(); err != nil {
		fmt.Printf("⚠️ 恢复调优前的系统参数失败: %v\n", err)
	}
	if err := uninstallKernels(); err != nil {
		fmt.Printf("⚠️ 清理内核和 nftables 规则失败: %v\n", err)
	}
	binaryPath, err := uninstallBinaryPath()
	if err != nil {
		fmt.Printf("❌ 卸载失败：无法定位 agent 二进制: %v\n", err)
		return
	}
	if uninstallInContainer() {
		if err := wipeContainerDataDir(binaryPath); err != nil {
			fmt.Printf("⚠️ 清理数据目录失败: %v\n", err)
		}
		fmt.Println("✅ 已卸载。容器无法删除自己，请在宿主机上执行 docker rm -f 删除这个容器")
		uninstallExit(0)
		return
	}
	manager, err := uninstallDetectManager()
	if err != nil {
		fmt.Printf("❌ 卸载失败: %v\n", err)
		return
	}
	script, err := buildAgentUninstallScript(manager, binaryPath)
	if err != nil {
		fmt.Printf("❌ 卸载失败: %v\n", err)
		return
	}
	if err := uninstallLaunchScript(manager, script); err != nil {
		fmt.Printf("❌ 启动卸载脚本失败: %v\n", err)
		return
	}
	fmt.Println("✅ 卸载脚本已启动，agent 即将停止")
}

// restoreTuningForUninstall 把调优改过的系统参数恢复到调优前的值：
// 先取消进行中的调优任务，再拿调优操作锁走既有恢复路径。
func restoreTuningForUninstall() error {
	cancelTuneJob()
	tuningOperation.Lock()
	defer tuningOperation.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return restoreAutomaticTuning(ctx)
}

// buildAgentUninstallScript 构建自卸载脚本：停服务 → 取消自启并删
// 服务文件 → 删安装目录，每步以 ; 连接、单步失败不阻断后续清理。
// 脚本经 launchAgentControlScript 脱离 agent 自身进程组/单元启动，
// 停服务不会连脚本一起杀掉。
//
// 圈定校验（防误删）：二进制名必须是 flux_agent，且安装目录必须
// 是与二进制同目录、目录名同为 flux_agent 的绝对路径（如
// /etc/flux_agent）。/usr/local/bin 这类共享目录一律拒绝。
func buildAgentUninstallScript(manager serviceManager, binaryPath string) (string, error) {
	name := filepath.Base(binaryPath)
	if name != agentInstallName {
		return "", fmt.Errorf("无法识别的 agent 安装：%s", binaryPath)
	}
	dir := filepath.Dir(binaryPath)
	if !filepath.IsAbs(dir) || filepath.Base(dir) != agentInstallName {
		return "", fmt.Errorf("拒绝删除无法识别的目录：%s", dir)
	}
	stop, _, _, err := serviceManagerCommands(manager)
	if err != nil {
		return "", err
	}
	q := tuneShellQuote
	logPath := filepath.Join(dir, "agent-uninstall.log")
	var b strings.Builder
	fmt.Fprintf(&b, "exec >>%s 2>&1; echo \"=== agent uninstall $(date) ===\"; sleep 1; %s; ", q(logPath), stop)
	switch manager {
	case serviceManagerSystemd:
		unit := filepath.Join("/etc/systemd/system", name+".service")
		fmt.Fprintf(&b, "systemctl disable %s; rm -f %s; systemctl daemon-reload; ", name, q(unit))
	case serviceManagerOpenRC:
		initScript := filepath.Join("/etc/init.d", name)
		fmt.Fprintf(&b, "rc-update del %s default; rm -f %s; ", name, q(initScript))
	default:
		return "", fmt.Errorf("不支持的服务管理器: %q", string(manager))
	}
	fmt.Fprintf(&b, "rm -rf %s; echo 'agent uninstall done'", q(dir))
	return b.String(), nil
}

// wipeContainerDataDir 是容器形态的清理入口：以数据目录（config
// 所在目录）为锚清空它。二进制路径只用于判断二进制是否恰在数据
// 目录内（同目录布局时保留二进制本体，见 wipeDirForUninstall）。
func wipeContainerDataDir(binaryPath string) error {
	dir, err := agentDataDir()
	if err != nil {
		return fmt.Errorf("无法定位数据目录: %v", err)
	}
	return wipeDirForUninstall(dir, binaryPath)
}

// wipeDirForUninstall 清空容器形态的数据目录：先写卸载标记，再删
// 除标记与（二进制恰在同目录时的）二进制本体外的一切（config.json
// 含 secret、gost 配置、内核目录、状态文件）。
//
// 圈定校验：目标必须是绝对路径，且目录名为 flmx_agent（镜像布局
// 的数据目录）或 flux_agent（脚本血统的同名数据目录）——两认的是
// 数据目录的布局指纹。二进制目录与二进制名不作圈定条件：镜像布
// 局下二进制在 /usr/local/bin 这类共享目录里，以它为锚必然误判
// （且共享目录名不在认可集内，天然被拒）；数据目录才是容器删不
// 掉自己时唯一能安全圈定的自有领地。
func wipeDirForUninstall(dir, binaryPath string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("拒绝清理无法识别的目录：%s", dir)
	}
	switch filepath.Base(dir) {
	case agentDataDirName, agentInstallName:
	default:
		return fmt.Errorf("拒绝清理无法识别的目录：%s", dir)
	}
	if err := os.WriteFile(filepath.Join(dir, uninstallMarker), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return err
	}
	keep := map[string]bool{uninstallMarker: true}
	if filepath.Dir(binaryPath) == dir {
		keep[filepath.Base(binaryPath)] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var firstErr error
	for _, entry := range entries {
		if keep[entry.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
