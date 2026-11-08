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
// 回执 data 携带预检 warnings：节点上有调优足迹在位时，预告卸
// 载将回退调优并删除哪些宿主调优文件，面板据此向用户明示处置
// 范围（既有 uninstallWarnings 呈现面）；无足迹时回执保持纯成功
// 形态。实际卸载照常执行（回退＋宿主文件清理＋逐项日志）。
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

// restoreTuningForUninstall 把系统调优整套回退到默认：拿调优操
// 作锁走新模型的 rollback（四面足迹＋第一代残留＋gai.conf 还原＋
// MSS 规则删除＋内存值复位）。回退是尽力而为：单项失败汇成
// warnings 返回供日志点名，卸载流程不因此中断；漏删的宿主文件由
// 随后的 cleanupHostTuningFiles 兜底。
func restoreTuningForUninstall() error {
	tuningOperation.Lock()
	defer tuningOperation.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if warnings := rollbackTuning(ctx); len(warnings) > 0 {
		return fmt.Errorf("部分调优回退失败：%s", strings.Join(warnings, "；"))
	}
	return nil
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
// 优文件全集（第二代模型足迹＋第一代残留兼容清理）：
//   - tuneKernelConfPath / tuneLimitsConfPath / tuneBBRConfPath：
//     第二代三件 conf，开机重放；
//   - tuneGaiBackupPath：IPv4 优先面的 gai.conf 备份（gai.conf 本
//     体是系统文件，只还原不删除）；
//   - tuneRPSUnitPath / tuneRPSScriptPath：网卡多队列持久化 unit
//     与其脚本，安装目录删除后 unit 悬空指向脚本，必须一并删；
//   - tuneKernelSnapshotPath：面③调优前 sysctl 快照（TL-02），
//     恢复步消费；漏删会把已卸载节点的快照留成孤儿文件；
//   - oldTuneConfPath / oldTuneBBRModulePath / oldTuneShapeUnitPath：
//     第一代残留（sysctl conf、tcp_bbr 开机加载、qdisc 整形 unit）；
//   - legacyTuneConfPath：改名前同作用的 sysctl conf。
//
// 除 legacy 件外按名即 FLMX 自有、无条件删；legacy 件与旧谱系
// agent 同名共用，按归属口径条件删（见 cleanupHostTuningFiles）。
// MSS 钳制规则不是文件，由 cleanupHostTuningFiles 单独删除。
func hostTuningFileSet() []string {
	set := []string{
		tuneKernelConfPath,
		tuneLimitsConfPath,
		tuneBBRConfPath,
		tuneGaiBackupPath,
		tuneRPSUnitPath,
		tuneRPSScriptPath,
		tuneKernelSnapshotPath,
		oldTuneConfPath,
		oldTuneBBRModulePath,
		oldTuneShapeUnitPath,
	}
	if legacyTuneConfPath != tuneKernelConfPath {
		set = append(set, legacyTuneConfPath)
	}
	return set
}

// cleanupHostTuningFiles 删除宿主调优文件集：unit（RPS 与第一代
// qdisc 两件）先 disable --now 再删文件、最后一并 daemon-reload
// （否则悬空启用项在下次开机仍会执行已删脚本）；MSS 钳制规则随
// 之删除；gai.conf 若恢复步未消费备份且调优行仍在，先按备份还原
// 再删备份（避免卸载后 IPv4 优先残留且原文件丢失）；legacy conf
// 在检测到旧式 gost 足迹时跳过并点名披露（可能由同机另一安装持
// 有，C4-F02 归属口径），无足迹时按本产品旧装遗留删除。逐项尽力
// 而为：单项失败只记入 Failed，不中断其余项。
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

	systemd := !uninstallInContainer() && commandExists("systemctl")
	unitRemoved := false
	for _, u := range []struct{ path, name string }{
		{tuneRPSUnitPath, tuneRPSUnitName},
		{oldTuneShapeUnitPath, oldTuneShapeUnitName},
		// 定时重启 timer 对：不随卸载残留（否则已卸载的节点仍会
		// 每日尝试重启一个不存在的服务）。
		{filepath.Join(restartScheduleUnitDir, restartTimerUnitName), restartTimerUnitName},
		{filepath.Join(restartScheduleUnitDir, restartServiceUnitName), restartServiceUnitName},
	} {
		if !exists(u.path) {
			continue
		}
		if systemd {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_, _ = tuneCommand(ctx, "systemctl", "disable", "--now", u.name)
			cancel()
		}
		before := len(rep.Removed)
		remove(u.path)
		if len(rep.Removed) > before {
			unitRemoved = true
		}
	}
	if systemd && unitRemoved {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, _ = tuneCommand(ctx, "systemctl", "daemon-reload")
		cancel()
	}
	// MSS 钳制规则（恢复步通常已删，此为兜底）。
	{
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		tuneMSSClampRemove(ctx)
		cancel()
	}
	// gai.conf 兜底还原：备份还在且调优行仍在位时按备份还原。
	if exists(tuneGaiBackupPath) && exists(tuneGaiConfPath) {
		if b, err := os.ReadFile(tuneGaiConfPath); err == nil && gaiPrecedenceActive(string(b)) {
			if bak, err := os.ReadFile(tuneGaiBackupPath); err == nil {
				if err := writeFileAtomic(tuneGaiConfPath, bak, 0o644); err != nil {
					rep.Failed = append(rep.Failed, fmt.Sprintf("%s（按备份还原失败：%v）", tuneGaiConfPath, err))
				}
			}
		}
	}
	remove(tuneKernelConfPath)
	remove(tuneLimitsConfPath)
	remove(tuneBBRConfPath)
	remove(tuneGaiBackupPath)
	remove(tuneRPSScriptPath)
	remove(tuneKernelSnapshotPath)
	remove(oldTuneConfPath)
	remove(oldTuneBBRModulePath)
	if legacyTuneConfPath != tuneKernelConfPath && exists(legacyTuneConfPath) {
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

// uninstallTuningAssessment 是回执预检结果：HostFiles 为当前在
// 位的宿主调优文件（卸载时将随清理集删除），IPv4Tuned 为
// gai.conf 的 IPv4 优先行是否在位（卸载时还原）。
type uninstallTuningAssessment struct {
	HostFiles []string
	IPv4Tuned bool
}

// assessUninstallTuning 只读盘面、不动任何状态：枚举在位的调优
// 足迹，供回执预告卸载将回退调优并删除哪些宿主文件。第二代模型
// 的回退是逐项尽力而为、没有必然失败的确定性谓词，故此处只预告
// 处置范围，实际失败由卸载日志逐项点名。
func assessUninstallTuning() uninstallTuningAssessment {
	var a uninstallTuningAssessment
	for _, path := range hostTuningFileSet() {
		if _, err := os.Stat(path); err == nil {
			a.HostFiles = append(a.HostFiles, path)
		}
	}
	if b, err := os.ReadFile(tuneGaiConfPath); err == nil && gaiPrecedenceActive(string(b)) {
		a.IPv4Tuned = true
	}
	return a
}

// warnings 把预检结论渲染成回执 warnings：没有任何调优足迹时
// 为空（回执保持既有纯成功形态）。
func (a uninstallTuningAssessment) warnings() []string {
	if len(a.HostFiles) == 0 && !a.IPv4Tuned {
		return nil
	}
	msg := "卸载将回退系统调优并恢复默认参数"
	if len(a.HostFiles) > 0 {
		msg += "；同时删除宿主调优文件：" + strings.Join(a.HostFiles, "、")
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
