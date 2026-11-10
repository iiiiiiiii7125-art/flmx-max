package socket

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// --- 宿主机累积面保底（包 K）：conntrack 保底＋journald 上限 ---
//
// 事故链（ECS1686，2026-11-10）：457MB 入口机的 nf_conntrack_max
// 是发行版按内存算出的 4096、TCP established 条目默认留 5 天，
// 转发会话只进不出——开机 26 分钟表满，内核全包皆丢，机器看似
// 关机实则网络溺亡，一天内反复四次；同机 /var/log journal 无上
// 限涨到 474M、根盘 95%，同为「无界累积→撞墙即死、无预警」一
// 族。装机流早有小内存 swap 防线（ensure_small_host_swap），但
// conntrack 与 journald 此前全仓零管理。
//
// 落点选 agent 启动自检而非装机脚本，理由与 swap 的登记缺口同
// 型：脚本只覆盖新装/脚本更新，面板内升级 agent（换二进制重
// 启）不经脚本——启动自检在每次 agent 启动时收敛，新装、面板
// 内升级、手工重启全覆盖；周期指标上报里另有惰性再检，兜住
// nf_conntrack 模块在 agent 启动后才加载的情形。
//
// 收敛语义（只往安全区单向收敛、幂等）：
//   - nf_conntrack_max 低于下限才抬高，已更高不动；
//   - 各超时高于上限才压低，已更低不动（运维有意调得更保守
//     时不许反向放宽）；
//   - 运行时值写 /proc/sys 即时生效，并把收敛后全量值写入
//     /etc/sysctl.d/99-flmx-conntrack.conf 持久化（开机由
//     systemd-sysctl 恢复）；
//   - journald 写 drop-in（SystemMaxUse 按根盘 5% 夹在
//     100–500M、RuntimeMaxUse 64M——易失 journal 在 tmpfs 里
//     吃的是内存，小机口径从紧），变更时重启 systemd-journald
//     使其即时生效；无 systemd/journald 环境（OpenRC 形态）
//     跳过。
// 任一步失败只产出诚实说明行（hostGuardResult.Lines），绝不阻
// 断 agent 启动、绝不静默。退出开关与 swap 防线同口径：
// FLMX_SKIP_CONNTRACK / FLMX_SKIP_JOURNALD_CAP（1/true/yes，
// 大小写不敏感）。

const (
	// conntrackFloorMax 是 nf_conntrack_max 的下限：ECS1686 现场
	// 人工修复值（4096→131072 后 count 稳定在数千量级）。满表时
	// 内核内存量级约 131072×300B≈40MB，小机可承受。
	conntrackFloorMax = 131072
	// conntrackTCPTimeoutEstablishedCap：默认 432000s（5 天）是
	// 死会话攒积的主通道，压到 1200s（现场修复值）。
	conntrackTCPTimeoutEstablishedCap = 1200
	// conntrackTCPTimeoutTimeWaitCap：默认 120s，现场修复取 60s。
	conntrackTCPTimeoutTimeWaitCap = 60
	// conntrackUDPTimeoutCap / StreamCap：默认 30s/120s 已不宽，
	// 上限只防被外部调大（现场修复取 60/180 的收敛带，此处取
	// 「不高于默认值太多」的封顶口径：未应答 60、已应答 180）。
	conntrackUDPTimeoutCap       = 60
	conntrackUDPTimeoutStreamCap = 180

	conntrackPersistPathDefault = "/etc/sysctl.d/99-flmx-conntrack.conf"
	journaldDropinPathDefault   = "/etc/systemd/journald.conf.d/99-flmx.conf"

	// journaldCapFloorMB/CeilMB/DefaultMB：SystemMaxUse 的夹取
	// 带与实读失败时的回退值（回退值＝ECS1686 现场人工设定）。
	journaldCapFloorMB   = 100
	journaldCapCeilMB    = 500
	journaldCapDefaultMB = 200
	// journaldRuntimeMaxUseMB：/run/log/journal 在 tmpfs（内存）
	// 上，易失侧从紧封顶。
	journaldRuntimeMaxUseMB = 64
)

// hostGuardEnv 是宿主机守卫的执行环境：路径、命令执行器、系统
// 调用集中于此，单测以临时目录＋替身整替，不触真 /proc/sys。
type hostGuardEnv struct {
	netfilterDir         string // /proc/sys/net/netfilter
	conntrackPersistPath string
	journaldDropinPath   string
	journaldConfPath     string // /etc/systemd/journald.conf，在位＝有 journald 形态
	systemdRunDir        string // /run/systemd/system，在位＝systemd 正在管理
	statfsTotal          func(path string) (uint64, error)
	runCmd               func(ctx context.Context, name string, args ...string) (string, error)
	getenv               func(string) string
	euid                 func() int
}

func defaultHostGuardEnv() *hostGuardEnv {
	return &hostGuardEnv{
		netfilterDir:         "/proc/sys/net/netfilter",
		conntrackPersistPath: conntrackPersistPathDefault,
		journaldDropinPath:   journaldDropinPathDefault,
		journaldConfPath:     "/etc/systemd/journald.conf",
		systemdRunDir:        "/run/systemd/system",
		statfsTotal: func(path string) (uint64, error) {
			var st syscall.Statfs_t
			if err := syscall.Statfs(path, &st); err != nil {
				return 0, err
			}
			return st.Blocks * uint64(st.Bsize), nil
		},
		runCmd: func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return string(out), err
		},
		getenv: os.Getenv,
		euid:   os.Geteuid,
	}
}

// ConntrackState 是 conntrack 当前实读快照（上报面消费）。
// Available=false 表示 nf_conntrack 未加载或读不到：Count/Max
// 无意义，上报面以 0 呈现、面板不据此告警。
type ConntrackState struct {
	Available bool
	Count     int64
	Max       int64
}

// ReadConntrackState 实读 count/max（默认环境）。
func ReadConntrackState() ConntrackState {
	return readConntrackState(defaultHostGuardEnv())
}

func readConntrackState(env *hostGuardEnv) ConntrackState {
	count, errCount := readSysctlInt(filepath.Join(env.netfilterDir, "nf_conntrack_count"))
	max, errMax := readSysctlInt(filepath.Join(env.netfilterDir, "nf_conntrack_max"))
	if errCount != nil || errMax != nil || max <= 0 {
		return ConntrackState{}
	}
	return ConntrackState{Available: true, Count: count, Max: max}
}

func readSysctlInt(path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
}

func writeSysctlInt(path string, v int64) error {
	return os.WriteFile(path, []byte(strconv.FormatInt(v, 10)+"\n"), 0644)
}

// hostGuardResult 是一次守卫执行的回执：Lines 是给人看的说明
// （启动路径全量打印、周期路径只在有动作/失败时打印）；Changed
// 表示有运行时值或持久化文件被改动；Failed 表示有动作失败
// （失败必在 Lines 里点名，不许静默）。
type hostGuardResult struct {
	Lines   []string
	Changed bool
	Failed  bool
}

func (r *hostGuardResult) line(format string, args ...interface{}) {
	r.Lines = append(r.Lines, fmt.Sprintf(format, args...))
}

func hostGuardSkipped(getenv func(string) string, name string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(name))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// ConntrackBelowFloor 判定实读 max 是否低于保底下限：指标上报
// 惰性再检的触发条件（模块晚加载时启动自检无面可收，靠周期
// 上报发现 max 仍是发行版默认值后补跑收敛）。
func ConntrackBelowFloor(s ConntrackState) bool {
	return s.Available && s.Max < conntrackFloorMax
}

// hostGuardMu 串行化全部守卫执行：启动路径与指标上报的惰性再
// 检可能并发进入，/proc/sys 写入与 drop-in 落盘不许交错。
var hostGuardMu sync.Mutex

// EnsureConntrackFloor 执行 conntrack 保底收敛（默认环境）。
// verbose=true 时达标也产出一行实读摘要（启动路径）；false 时
// 只在有动作或失败时产出行（周期路径，防日志刷屏）。
func EnsureConntrackFloor(verbose bool) hostGuardResult {
	return ensureConntrackFloor(defaultHostGuardEnv(), verbose)
}

func ensureConntrackFloor(env *hostGuardEnv, verbose bool) hostGuardResult {
	hostGuardMu.Lock()
	defer hostGuardMu.Unlock()
	res := &hostGuardResult{}

	if runtime.GOOS != "linux" {
		res.line("conntrack 保底：非 Linux 环境，跳过")
		return *res
	}
	if hostGuardSkipped(env.getenv, "FLMX_SKIP_CONNTRACK") {
		res.line("conntrack 保底：FLMX_SKIP_CONNTRACK 已设置，跳过（自检: cat /proc/sys/net/netfilter/nf_conntrack_max 与 nf_conntrack_tcp_timeout_established）")
		return *res
	}

	// 键 → 安全区收敛目标：targetFor 给出当前值应收敛到的目标
	// （已在安全区则等于当前值）。count 是只读计数不在此列。
	type ctKey struct {
		name      string
		targetFor func(cur int64) int64
		floorNote string
	}
	keys := []ctKey{
		{"nf_conntrack_max", func(cur int64) int64 {
			if cur < conntrackFloorMax {
				return conntrackFloorMax
			}
			return cur
		}, fmt.Sprintf("下限 %d", conntrackFloorMax)},
		{"nf_conntrack_tcp_timeout_established", func(cur int64) int64 {
			if cur > conntrackTCPTimeoutEstablishedCap {
				return conntrackTCPTimeoutEstablishedCap
			}
			return cur
		}, fmt.Sprintf("上限 %ds", conntrackTCPTimeoutEstablishedCap)},
		{"nf_conntrack_tcp_timeout_time_wait", func(cur int64) int64 {
			if cur > conntrackTCPTimeoutTimeWaitCap {
				return conntrackTCPTimeoutTimeWaitCap
			}
			return cur
		}, fmt.Sprintf("上限 %ds", conntrackTCPTimeoutTimeWaitCap)},
		{"nf_conntrack_udp_timeout", func(cur int64) int64 {
			if cur > conntrackUDPTimeoutCap {
				return conntrackUDPTimeoutCap
			}
			return cur
		}, fmt.Sprintf("上限 %ds", conntrackUDPTimeoutCap)},
		{"nf_conntrack_udp_timeout_stream", func(cur int64) int64 {
			if cur > conntrackUDPTimeoutStreamCap {
				return conntrackUDPTimeoutStreamCap
			}
			return cur
		}, fmt.Sprintf("上限 %ds", conntrackUDPTimeoutStreamCap)},
	}

	current := make(map[string]int64, len(keys))
	for _, k := range keys {
		v, err := readSysctlInt(filepath.Join(env.netfilterDir, k.name))
		if err != nil {
			// 任一键读不到即视为 conntrack 面不在场（模块未加载
			// 时整目录缺失）：不写、不持久化，留待模块加载后由
			// 指标上报的惰性再检收敛。
			res.line("conntrack 保底：读 %s 失败（%v），nf_conntrack 可能尚未加载，本次跳过；模块加载后将由周期再检收敛", k.name, err)
			return *res
		}
		current[k.name] = v
	}

	target := make(map[string]int64, len(keys))
	var changes []string
	for _, k := range keys {
		target[k.name] = k.targetFor(current[k.name])
		if target[k.name] != current[k.name] {
			changes = append(changes, fmt.Sprintf("%s %d→%d（%s）", k.name, current[k.name], target[k.name], k.floorNote))
		}
	}

	if len(changes) > 0 && env.euid() != 0 {
		res.Failed = true
		res.line("conntrack 保底：当前值越界（%s），但 agent 非 root 运行、无权调 sysctl，本次未调整；请以 root 执行 sysctl -w 或等 agent 以 root 重启后自动收敛", strings.Join(changes, "；"))
		return *res
	}

	for _, k := range keys {
		if target[k.name] == current[k.name] {
			continue
		}
		path := filepath.Join(env.netfilterDir, k.name)
		if err := writeSysctlInt(path, target[k.name]); err != nil {
			res.Failed = true
			res.line("conntrack 保底：写 %s=%d 失败: %v（运行时值未改，持久化仍按目标登记、开机时由 sysctl 服务再试）", k.name, target[k.name], err)
			continue
		}
		res.Changed = true
	}
	if len(changes) > 0 {
		if res.Changed {
			res.line("conntrack 保底：已收敛 %s", strings.Join(changes, "；"))
		}
	} else if verbose {
		count, _ := readSysctlInt(filepath.Join(env.netfilterDir, "nf_conntrack_count"))
		res.line("conntrack 保底：实读 max=%d、established 超时=%ds、count=%d，均在安全区，未调整", current["nf_conntrack_max"], current["nf_conntrack_tcp_timeout_established"], count)
	}

	// 持久化收敛后的目标全量：写入仅当内容不同（幂等）。非 root
	// 在 /etc 不可写时失败点名，不假装已持久化。
	var b strings.Builder
	b.WriteString("# Managed by flmx_agent host guard (package K): conntrack floor\n")
	b.WriteString("# converged at agent startup; values only ever move toward the\n")
	b.WriteString("# safe zone (max raised to the floor, timeouts lowered to caps).\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "net.netfilter.%s=%d\n", k.name, target[k.name])
	}
	written, err := writeFileIfChanged(env.conntrackPersistPath, b.String())
	if err != nil {
		res.Failed = true
		res.line("conntrack 保底：持久化 %s 写入失败: %v（运行时值已生效，但重启后不保证恢复，请人工核对该文件）", env.conntrackPersistPath, err)
	} else if written {
		res.Changed = true
		if len(changes) == 0 {
			res.line("conntrack 保底：运行时值已达标，补写持久化 %s（开机恢复收敛值）", env.conntrackPersistPath)
		}
	}
	return *res
}

// writeFileIfChanged 仅在内容不同（或文件缺失）时落盘，返回是
// 否实写。目录不存在时创建。
func writeFileIfChanged(path, content string) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
		return false, nil
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, err
		}
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureJournaldCap 执行 journald 上限收敛（默认环境）。
func EnsureJournaldCap() hostGuardResult {
	return ensureJournaldCap(defaultHostGuardEnv())
}

func ensureJournaldCap(env *hostGuardEnv) hostGuardResult {
	hostGuardMu.Lock()
	defer hostGuardMu.Unlock()
	res := &hostGuardResult{}

	if runtime.GOOS != "linux" {
		res.line("journald 上限：非 Linux 环境，跳过")
		return *res
	}
	if hostGuardSkipped(env.getenv, "FLMX_SKIP_JOURNALD_CAP") {
		res.line("journald 上限：FLMX_SKIP_JOURNALD_CAP 已设置，跳过（自检: journalctl --disk-usage 与 /etc/systemd/journald.conf.d/99-flmx.conf）")
		return *res
	}
	if _, err := os.Stat(env.journaldConfPath); err != nil {
		// 无 journald 形态（OpenRC/Alpine 等）：机器日志不走
		// journald，本面不适用，明确跳过而非报错。
		res.line("journald 上限：未见 %s，本机无 journald 形态，跳过", env.journaldConfPath)
		return *res
	}

	capMB := journaldCapMB(env)
	content := fmt.Sprintf("# Managed by flmx_agent host guard (package K): journald size cap\n"+
		"# so log growth cannot fill the root disk (field case: 474M of\n"+
		"# journal on a 4.8G disk). RuntimeMaxUse stays small because the\n"+
		"# volatile journal lives on tmpfs (RAM).\n[Journal]\nSystemMaxUse=%dM\nRuntimeMaxUse=%dM\n",
		capMB, journaldRuntimeMaxUseMB)

	existing, readErr := os.ReadFile(env.journaldDropinPath)
	if readErr == nil && string(existing) == content {
		res.line("journald 上限：drop-in 已在位（SystemMaxUse=%dM），未改动", capMB)
		return *res
	}
	if env.euid() != 0 {
		res.Failed = true
		res.line("journald 上限：drop-in 缺失/过期且 agent 非 root，无权写入 %s，本次未设置；请以 root 重启 agent 或人工放置该 drop-in 后 systemctl restart systemd-journald", env.journaldDropinPath)
		return *res
	}
	if _, err := writeFileIfChanged(env.journaldDropinPath, content); err != nil {
		res.Failed = true
		res.line("journald 上限：写 %s 失败: %v（journal 上限未设置，根盘被日志涨满的风险未解除）", env.journaldDropinPath, err)
		return *res
	}
	res.Changed = true
	res.line("journald 上限：已写 %s（SystemMaxUse=%dM、RuntimeMaxUse=%dM）", env.journaldDropinPath, capMB, journaldRuntimeMaxUseMB)

	// 即时生效：重启 systemd-journald 使其按新上限回收存量。
	// 无 systemd 运行环境（容器形态等）时只落持久化并明示。
	if _, err := os.Stat(env.systemdRunDir); err != nil {
		res.line("journald 上限：未见 systemd 运行环境，本次仅落持久化，新上限将在 journald 下次启动时生效")
		return *res
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := env.runCmd(ctx, "systemctl", "restart", "systemd-journald")
	if err != nil {
		res.Failed = true
		res.line("journald 上限：drop-in 已持久化，但 systemctl restart systemd-journald 失败: %v（%s）；新上限将在下次开机/journald 重启后生效，当前存量 journal 未即时回收", err, strings.TrimSpace(out))
		return *res
	}
	res.line("journald 上限：systemd-journald 已重启，新上限即时生效")
	return *res
}

// journaldCapMB 按 journal 所在文件系统总量取 5%、夹在
// [100M, 500M]；实读失败回退现场值 200M。
func journaldCapMB(env *hostGuardEnv) int64 {
	total, err := env.statfsTotal("/var/log")
	if err != nil || total == 0 {
		return journaldCapDefaultMB
	}
	mb := int64(total) / 20 / (1024 * 1024)
	if mb < journaldCapFloorMB {
		return journaldCapFloorMB
	}
	if mb > journaldCapCeilMB {
		return journaldCapCeilMB
	}
	return mb
}
