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
// （/etc/flmx_agent/flmx_agent），是自卸载圈定范围的身份指纹。
const agentInstallName = "flmx_agent"

// legacyAgentInstallName 是改名前的二进制名与安装目录名
// （/etc/flux_agent/flux_agent）。存量节点在经 install.sh 更新迁
// 移到新名之前一直是这套布局，面板下发/宿主状态识别面必须双认：
// 新名优先、旧名回退，识别集本轮只加不删。
const legacyAgentInstallName = "flux_agent"

// isAgentInstallName 报告 name 是否任一世代的 agent 安装名。
func isAgentInstallName(name string) bool {
	return name == agentInstallName || name == legacyAgentInstallName
}

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
	// 宿主调优文件清理与回执预检（R2F2-D01）：清理集与 install.sh
	// 的 uninstall_flmx_agent_host_cleanup 同文件集、同口径。
	uninstallHostTuningCleanup = cleanupHostTuningFiles
	uninstallTuningPreflight   = assessUninstallTuning
	uninstallLegacyFootprint   = legacyGostFootprintPresent
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
//
// 回执 data 携带预检 warnings（R2F2-D01）：调优恢复的 fail-closed
// 早退条件（整形前队列备份丢失＋整形仍在）在下发时即可确定性判
// 定——恢复必然失败时先在回执里点名，面板据此不报纯成功；实际
// 卸载仍照常执行（恢复尝试＋宿主文件清理＋逐项日志）。
func (w *WebSocketReporter) handleUninstallAgent() (interface{}, error) {
	if !uninstallStarted.CompareAndSwap(false, true) {
		return nil, nil
	}
	var data interface{}
	if warnings := uninstallTuningPreflight().warnings(); len(warnings) > 0 {
		data = map[string]interface{}{"warnings": warnings}
	}
	go func() {
		time.Sleep(time.Second)
		runAgentUninstall()
	}()
	return data, nil
}

func runAgentUninstall() {
	fmt.Println("🗑️ 面板已删除此服务器，开始卸载 agent...")
	restoreErr := uninstallRestoreTuning()
	if restoreErr != nil {
		fmt.Printf("⚠️ 恢复调优前的系统参数失败: %v\n", restoreErr)
	}
	// 宿主调优文件删除集（R2F2-D01）：恢复成功时这批文件已由恢复
	// 路径处理，本步为空转核对；恢复失败/早退时由本步兜底删除，
	// 防止 sysctl conf 开机重放调优值、qdisc unit 在安装目录删除
	// 后悬空。逐项结果打进日志，不静默。
	cleanup := uninstallHostTuningCleanup()
	for _, path := range cleanup.Removed {
		fmt.Printf("🧹 删除调优宿主文件: %s\n", path)
	}
	for _, item := range cleanup.Skipped {
		fmt.Printf("⚠️ 跳过删除调优宿主文件: %s\n", item)
	}
	for _, item := range cleanup.Failed {
		fmt.Printf("⚠️ 删除调优宿主文件失败: %s\n", item)
	}
	if restoreErr != nil && len(cleanup.Failed) > 0 {
		fmt.Printf("⚠️ 调优恢复失败且以下宿主调优文件未能删除，卸载后仍有残留：%s\n", strings.Join(cleanup.Failed, "、"))
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
//
// 卸载语义与面板「恢复调优」功能不同（R2F2-D01）：恢复路径
// fail-closed 早退（如整形前队列备份丢失）是为保配置，而卸载时
// 安装目录（含原值备份）即将被删，错过这次 sysctl 原值就没有恢
// 复机会。故整形恢复失败后，只要原值备份还在，就单独走
// applyTuning 把 sysctl 原值写回（与 install.sh 宿主清理第 2 步
// 同口径：队列备份丢失时仍按 tuning-backup.json 恢复 sysctl）；
// 备份不在时不调 applyTuning——无可写回，且其顺带删除 legacy
// conf 的副作用会绕过宿主清理的归属守卫。
func restoreTuningForUninstall() error {
	cancelTuneJob()
	tuningOperation.Lock()
	defer tuningOperation.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err := restoreAutomaticTuning(ctx)
	if err == nil {
		return nil
	}
	if _, statErr := os.Stat(tuneBackupPath); statErr != nil {
		return err
	}
	if _, ferr := applyTuning(nil, savedTuneParams()); ferr != nil {
		return fmt.Errorf("%v；sysctl 参数单独恢复亦失败：%v", err, ferr)
	}
	return fmt.Errorf("%v（sysctl 参数已按原值备份单独恢复）", err)
}

// hostTuningCleanupReport 是宿主调优文件清理的逐项结果：Removed
// 已删除、Skipped 因归属守卫跳过（条目内含路径与原因）、Failed
// 删除失败（条目内含路径与错误）。
type hostTuningCleanupReport struct {
	Removed []string
	Skipped []string
	Failed  []string
}

// hostTuningFileSet 是 agent 写到宿主机（安装/数据目录之外）的调
// 优文件全集，与 install.sh 的 FLMX_TUNING_* 变量逐项对应：
//   - tuneConfPath：sysctl.d 调优值，开机重放；
//   - tuneBBRModule：modules-load.d 的 tcp_bbr 开机加载；
//   - tuneShapeUnitPath()：qdisc 整形 unit，ExecStart 指向安装目
//     录里的 shape.sh，目录删后即悬空；
//   - legacyTuneConfPath：改名前同作用的 sysctl conf。
//
// 前三件按名即 FLMX 自有、无条件删；legacy 件与旧谱系 agent 同
// 名共用，按归属口径条件删（见 cleanupHostTuningFiles）。
func hostTuningFileSet() []string {
	set := []string{tuneConfPath, tuneBBRModule, tuneShapeUnitPath()}
	if legacyTuneConfPath != tuneConfPath {
		set = append(set, legacyTuneConfPath)
	}
	return set
}

// cleanupHostTuningFiles 删除宿主调优文件集，与 install.sh 的
// uninstall_flmx_agent_host_cleanup 同口径：qdisc unit 先
// disable --now 再删文件并 daemon-reload（否则悬空启用项在下次
// 开机仍会执行已删的 shape.sh）；legacy conf 在检测到旧式 gost
// 足迹时跳过并点名披露（可能由同机另一安装持有，C4-F02 归属口
// 径），无足迹时按本产品旧装遗留删除。逐项尽力而为：单项失败
// 只记入 Failed，不中断其余项。
func cleanupHostTuningFiles() hostTuningCleanupReport {
	var rep hostTuningCleanupReport
	remove := func(path string) {
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				return
			}
			rep.Failed = append(rep.Failed, fmt.Sprintf("%s（%v）", path, err))
			return
		}
		rep.Removed = append(rep.Removed, path)
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	unit := tuneShapeUnitPath()
	if exists(unit) {
		systemd := !uninstallInContainer() && commandExists("systemctl")
		if systemd {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_, _ = tuneCommand(ctx, "systemctl", "disable", "--now", "flmx-tuning-qdisc.service")
			cancel()
		}
		remove(unit)
		removed := false
		for _, path := range rep.Removed {
			if path == unit {
				removed = true
			}
		}
		if systemd && removed {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_, _ = tuneCommand(ctx, "systemctl", "daemon-reload")
			cancel()
		}
	}
	remove(tuneConfPath)
	remove(tuneBBRModule)
	if legacyTuneConfPath != tuneConfPath && exists(legacyTuneConfPath) {
		if uninstallLegacyFootprint() {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s（检测到旧式 gost agent 足迹，可能由其持有；如确认属本产品旧装可人工删除）", legacyTuneConfPath))
		} else {
			remove(legacyTuneConfPath)
		}
	}
	return rep
}

// legacyGostServiceFilePaths 与 legacyGostDirPath 是旧式 gost 足
// 迹的落点（与 install.sh 的 LEGACY_GOST_SERVICE_FILE_* /
// LEGACY_GOST_CONFIG_DIR 对应），变量形态供单测指向临时树。
var (
	legacyGostServiceFilePaths = []string{
		"/etc/systemd/system/gost.service",
		"/lib/systemd/system/gost.service",
		"/usr/lib/systemd/system/gost.service",
	}
	legacyGostDirPath = "/etc/gost"
)

// legacyGostFootprintPresent 是 install.sh
// legacy_gost_footprint_present 的 agent 侧同口径实现：任一候选
// gost.service 命中指纹（WorkingDirectory 指向旧配置目录，且
// ExecStart 为旧目录二进制，或 /usr/local/bin 形态且旧配置两件
// 齐备），或旧配置目录里有 gost 二进制/连接配置，即认足迹在。
func legacyGostFootprintPresent() bool {
	fileExists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}
	for _, path := range legacyGostServiceFilePaths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s := string(b)
		if !strings.Contains(s, "WorkingDirectory="+legacyGostDirPath) {
			continue
		}
		if strings.Contains(s, "ExecStart="+filepath.Join(legacyGostDirPath, "gost")) {
			return true
		}
		if strings.Contains(s, "ExecStart=/usr/local/bin/gost") &&
			fileExists(filepath.Join(legacyGostDirPath, "config.json")) &&
			fileExists(filepath.Join(legacyGostDirPath, "gost.json")) {
			return true
		}
	}
	return fileExists(filepath.Join(legacyGostDirPath, "gost")) ||
		fileExists(filepath.Join(legacyGostDirPath, "config.json"))
}

// uninstallTuningAssessment 是回执预检结果：RestoreBlocked 为恢
// 复必然 fail-closed 早退的确定性判定，HostFiles 为当前在位的
// 宿主调优文件（卸载时将随清理集删除）。
type uninstallTuningAssessment struct {
	RestoreBlocked bool
	HostFiles      []string
}

// assessUninstallTuning 只读盘面、不动任何状态。与
// restoreAutomaticTuning 的早退同谓词：整形前队列备份
// （original-qdisc.sh）丢失、而整形脚本或整形 unit 仍在位时，
// 恢复必然在 applyTuning 之前早退——这是卸载回执唯一能确定性
// 预告的恢复失败，其余失败（写回被拒等）只能在执行后由日志点
// 名，不在此预告。
func assessUninstallTuning() uninstallTuningAssessment {
	var a uninstallTuningAssessment
	for _, path := range hostTuningFileSet() {
		if _, err := os.Stat(path); err == nil {
			a.HostFiles = append(a.HostFiles, path)
		}
	}
	if _, err := os.Stat(filepath.Join(tuneEngineDir(), "original-qdisc.sh")); os.IsNotExist(err) {
		_, shapeErr := os.Stat(filepath.Join(tuneEngineDir(), "shape.sh"))
		_, unitErr := os.Stat(tuneShapeUnitPath())
		if shapeErr == nil || unitErr == nil {
			a.RestoreBlocked = true
		}
	}
	return a
}

// warnings 把预检结论渲染成回执 warnings：恢复未被确定性阻断
// 时为空（回执保持既有纯成功形态）。
func (a uninstallTuningAssessment) warnings() []string {
	if !a.RestoreBlocked {
		return nil
	}
	msg := "调优恢复将失败：整形前的队列备份已丢失，队列整形与调优参数无法随卸载恢复（重启后随状态消散）"
	if len(a.HostFiles) > 0 {
		msg += "；卸载时会删除宿主调优文件：" + strings.Join(a.HostFiles, "、")
	}
	return []string{msg}
}

// buildAgentUninstallScript 构建自卸载脚本：停服务 → 取消自启并删
// 服务文件 → 删安装目录，每步以 ; 连接、单步失败不阻断后续清理。
// 脚本经 launchAgentControlScript 脱离 agent 自身进程组/单元启动，
// 停服务不会连脚本一起杀掉。
//
// 圈定校验（防误删）：二进制名必须是任一世代的安装名（现行
// flmx_agent 或存量 flux_agent），且安装目录必须是与二进制同目
// 录、目录名与二进制名相同的绝对路径（如 /etc/flmx_agent、存量
// /etc/flux_agent）。/usr/local/bin 这类共享目录一律拒绝。服务命
// 令按该布局自己的服务名生成，存量未迁节点删的仍是它自己的服务。
func buildAgentUninstallScript(manager serviceManager, binaryPath string) (string, error) {
	name := filepath.Base(binaryPath)
	if !isAgentInstallName(name) {
		return "", fmt.Errorf("无法识别的 agent 安装：%s", binaryPath)
	}
	dir := filepath.Dir(binaryPath)
	if !filepath.IsAbs(dir) || filepath.Base(dir) != name {
		return "", fmt.Errorf("拒绝删除无法识别的目录：%s", dir)
	}
	stop, _, _, err := serviceManagerCommandsFor(manager, name)
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
// 的数据目录与现行脚本布局同名）或 flux_agent（改名前脚本血统的
// 同名数据目录）——两认的是数据目录的布局指纹。二进制目录与二
// 进制名不作圈定条件：镜像布局下二进制在 /usr/local/bin 这类共
// 享目录里，以它为锚必然误判（且共享目录名不在认可集内，天然
// 被拒）；数据目录才是容器删不掉自己时唯一能安全圈定的自有领地。
func wipeDirForUninstall(dir, binaryPath string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("拒绝清理无法识别的目录：%s", dir)
	}
	// agentDataDirName 与现行 agentInstallName 同为 flmx_agent，
	// 连同存量 flux_agent 构成数据目录名的认可集。
	if base := filepath.Base(dir); base != agentDataDirName && !isAgentInstallName(base) {
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
