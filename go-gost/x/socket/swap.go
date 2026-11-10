package socket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v3/disk"
)

// --- 节点一键设置 Swap（面板节点页功能，后端半） ---
//
// 面板经 SwapInfo / SwapSet 两条命令管理节点上的 /swapfile：
//   - SwapInfo 只读回全机 swap 现状（/proc/swaps 全量条目，含非
//     面板管理的分区/他文件 swap——只报不动）、磁盘剩余、总内存、
//     /swapfile 是否在场及其在盘大小（fileSizeMB）；
//   - SwapSet {sizeGB} 把 /swapfile 建到/调整到目标大小：空间校验
//     （目标＋256MB 预留）、swapoff 风险校验（现有已用量大于可用
//     内存时拒绝，防 swapoff 把换出页压回内存压死小机）、重建、
//     /etc/fstab 幂等收敛为恰好一条托管行。任一步失败回结构化
//     错误（步骤＋原因＋当前实际状态），且尽力恢复执行前状态，
//     不留「fstab 写了 swap 没启用」一类半成品。
//
// 校验边界与面板侧 nodeSwapSet 一致（面板判＋agent 复判）：
// sizeGB 为 1–32 的整数。

const (
	swapFilePath      = "/swapfile"
	swapBackupSuffix  = ".flmx-bak"
	swapFstabPath     = "/etc/fstab"
	swapProcSwapsPath = "/proc/swaps"
	swapMinSizeGB     = 1
	swapMaxSizeGB     = 32
	// swapReserveMB 是目标大小之外的磁盘预留：fallocate/dd 落盘
	// 与 mkswap 元数据之外，给系统留一口呼吸空间。
	swapReserveMB = 256
)

// swapEntryInfo 是 /proc/swaps 的一行（全机口径，含非托管条目）。
type swapEntryInfo struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	SizeMB int64  `json:"sizeMB"`
	UsedMB int64  `json:"usedMB"`
}

// swapTotals 是 before/after 的全机 swap 总量口径。
type swapTotals struct {
	TotalMB int64 `json:"totalMB"`
	UsedMB  int64 `json:"usedMB"`
}

// swapInfoReply 是 SwapInfo 的回执（契约字段名勿改）。
type swapInfoReply struct {
	TotalMB            int64           `json:"totalMB"`
	UsedMB             int64           `json:"usedMB"`
	Entries            []swapEntryInfo `json:"entries"`
	DiskFreeMB         int64           `json:"diskFreeMB"`
	MemTotalMB         int64           `json:"memTotalMB"`
	ManagedFilePresent bool            `json:"managedFilePresent"`
	// FileSizeMB 是 /swapfile 在磁盘上的实际文件大小（stat 口径，
	// MB 向下取整）；文件不在盘为 0。与启用状态无关——在盘未启用
	// 也回真实大小：SwapSet 容量校验把这块占量计入可用（重建时
	// 释放），前端预检须凭此字段逐字镜像同一谓词（R3-NODE-01）。
	FileSizeMB int64 `json:"fileSizeMB"`
}

// swapSetReply 是 SwapSet 成功回执：全机总量 before → after。
type swapSetReply struct {
	Before   swapTotals `json:"before"`
	After    swapTotals `json:"after"`
	SizeGB   int        `json:"sizeGB"`
	Warnings []string   `json:"warnings,omitempty"`
}

// swapEnv 是 Swap 命令的执行环境：文件路径与命令执行器集中于此，
// 单测以临时目录＋替身执行器整替（不触真 swapon/mkswap）。
type swapEnv struct {
	procSwapsPath string
	meminfoPath   string
	fstabPath     string
	filePath      string
	backupPath    string
	run           func(ctx context.Context, name string, args ...string) (string, error)
	diskFreeMB    func(path string) (int64, error)
	inContainer   func() bool
}

func realSwapEnv() swapEnv {
	return swapEnv{
		procSwapsPath: swapProcSwapsPath,
		meminfoPath:   tuneMeminfoPath,
		fstabPath:     swapFstabPath,
		filePath:      swapFilePath,
		backupPath:    swapFilePath + swapBackupSuffix,
		run:           runTuneCommand,
		diskFreeMB:    swapDiskFreeMB,
		inContainer:   InContainer,
	}
}

// swapDiskFreeMB 返回 path 所在文件系统的可用空间（MB）。
func swapDiskFreeMB(path string) (int64, error) {
	usage, err := disk.Usage(path)
	if err != nil {
		return 0, err
	}
	return int64(usage.Free / (1024 * 1024)), nil
}

// --- 纯函数：解析与校验 -------------------------------------------------

// parseSwaps 解析 /proc/swaps 内容。首行是表头（Filename 开头）；
// 畸形行（字段不足/数值非法）整行跳过——内核不会写出这种行，跳过
// 比整单失败更贴近「只读现状」的用途。Size/Used 单位 KiB，出口
// 统一换算为 MB。
func parseSwaps(content []byte) []swapEntryInfo {
	entries := []swapEntryInfo{}
	for _, line := range strings.Split(string(content), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] == "Filename" {
			continue
		}
		if len(f) < 4 {
			continue
		}
		sizeKiB, err1 := strconv.ParseInt(f[2], 10, 64)
		usedKiB, err2 := strconv.ParseInt(f[3], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		entries = append(entries, swapEntryInfo{
			Name:   f[0],
			Type:   f[1],
			SizeMB: sizeKiB / 1024,
			UsedMB: usedKiB / 1024,
		})
	}
	return entries
}

// parseMeminfoKB 取 /proc/meminfo 中 key（如 MemTotal）的 KiB 值。
func parseMeminfoKB(content []byte, key string) (int64, bool) {
	prefix := key + ":"
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return 0, false
		}
		v, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// isManagedFstabLine 判一行 fstab 是否为托管文件的条目行（注释行
// 不算——那是用户自己的标注，收敛时原样保留）。
func isManagedFstabLine(line string, filePath string) bool {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return false
	}
	f := strings.Fields(t)
	return len(f) > 0 && f[0] == filePath
}

// convergeFstab 把 fstab 内容收敛为恰好一条托管条目
// 「<filePath> none swap sw 0 0」：已有托管行（无论旧参数形态）
// 在原位替换为标准行、重复行删除、无则末尾追加；其他所有行
// （注释、其他文件系统、其他 swap 条目）逐字保留、顺序不变。
// 返回收敛后内容与是否发生变化。
func convergeFstab(content string, filePath string) (string, bool) {
	canonical := filePath + " none swap sw 0 0"
	body := strings.TrimSuffix(content, "\n")
	var lines []string
	if body != "" {
		lines = strings.Split(body, "\n")
	}
	out := make([]string, 0, len(lines)+1)
	seen := false
	for _, line := range lines {
		if isManagedFstabLine(line, filePath) {
			if !seen {
				out = append(out, canonical)
				seen = true
			}
			continue
		}
		out = append(out, line)
	}
	if !seen {
		out = append(out, canonical)
	}
	result := strings.Join(out, "\n") + "\n"
	return result, result != content
}

// validateSwapSizeGB 是面板/agent 共用的大小边界（两端复判）。
func validateSwapSizeGB(sizeGB int) error {
	if sizeGB < swapMinSizeGB || sizeGB > swapMaxSizeGB {
		return fmt.Errorf("Swap 大小须在 %d–%d GB 之间，收到 %d GB", swapMinSizeGB, swapMaxSizeGB, sizeGB)
	}
	return nil
}

// checkSwapCapacity 判磁盘是否装得下目标：现有 /swapfile 的占量
// 在重建时会被释放，计入可用；目标之外另留 swapReserveMB 预留。
func checkSwapCapacity(diskFreeMB int64, existingFileMB int64, sizeGB int) error {
	needMB := int64(sizeGB)*1024 + swapReserveMB
	availMB := diskFreeMB + existingFileMB
	if availMB < needMB {
		return fmt.Errorf("磁盘空间不足：设置 %d GB Swap 需要至少 %d MB 可用（目标 %d MB＋预留 %d MB），当前可用 %d MB（磁盘剩余 %d MB＋现有 /swapfile 占用 %d MB）",
			sizeGB, needMB, int64(sizeGB)*1024, swapReserveMB, availMB, diskFreeMB, existingFileMB)
	}
	return nil
}

// swapoffUnsafe 判 swapoff 风险：现有已用量大于当前可用内存时，
// swapoff 会把换出页全部压回内存，可能把小内存机当场压死。
func swapoffUnsafe(managedUsedMB int64, memAvailableMB int64) bool {
	return managedUsedMB > memAvailableMB
}

// --- 状态读取 -----------------------------------------------------------

type swapState struct {
	entries    []swapEntryInfo
	totals     swapTotals
	memTotalMB int64
	memAvailMB int64
	memAvailOK bool
	managed    *swapEntryInfo // filePath 的在用条目，未启用时为 nil
	fileOnDisk bool
	fileSizeMB int64
}

func (e swapEnv) readState() (swapState, error) {
	var st swapState
	swapsRaw, err := os.ReadFile(e.procSwapsPath)
	if err != nil {
		return st, fmt.Errorf("读取 Swap 状态失败: %v", err)
	}
	st.entries = parseSwaps(swapsRaw)
	for i := range st.entries {
		st.totals.TotalMB += st.entries[i].SizeMB
		st.totals.UsedMB += st.entries[i].UsedMB
		if st.entries[i].Name == e.filePath {
			entry := st.entries[i]
			st.managed = &entry
		}
	}
	memRaw, err := os.ReadFile(e.meminfoPath)
	if err != nil {
		return st, fmt.Errorf("读取内存信息失败: %v", err)
	}
	memTotalKiB, ok := parseMeminfoKB(memRaw, "MemTotal")
	if !ok {
		return st, fmt.Errorf("读取内存信息失败: %s 中找不到 MemTotal", e.meminfoPath)
	}
	st.memTotalMB = memTotalKiB / 1024
	if availKiB, ok := parseMeminfoKB(memRaw, "MemAvailable"); ok {
		st.memAvailMB = availKiB / 1024
		st.memAvailOK = true
	}
	if fi, err := os.Stat(e.filePath); err == nil {
		st.fileOnDisk = true
		st.fileSizeMB = fi.Size() / (1024 * 1024)
	}
	return st, nil
}

// --- SwapInfo -----------------------------------------------------------

func (e swapEnv) info() (swapInfoReply, error) {
	st, err := e.readState()
	if err != nil {
		return swapInfoReply{}, err
	}
	freeMB, err := e.diskFreeMB(filepath.Dir(e.filePath))
	if err != nil {
		return swapInfoReply{}, fmt.Errorf("读取磁盘剩余空间失败: %v", err)
	}
	return swapInfoReply{
		TotalMB:            st.totals.TotalMB,
		UsedMB:             st.totals.UsedMB,
		Entries:            st.entries,
		DiskFreeMB:         freeMB,
		MemTotalMB:         st.memTotalMB,
		ManagedFilePresent: st.fileOnDisk || st.managed != nil,
		FileSizeMB:         st.fileSizeMB,
	}, nil
}

// --- SwapSet ------------------------------------------------------------

// swapStepError 是 SwapSet 失败回执的统一形态：步骤＋原因＋当前
// 实际状态，三段缺一不可（前端原文透传给用户看）。
func swapStepError(step string, cause error, stateNote string) error {
	return fmt.Errorf("设置 Swap 失败（%s）: %v；%s", step, cause, stateNote)
}

func (e swapEnv) set(ctx context.Context, sizeGB int) (swapSetReply, error) {
	if err := validateSwapSizeGB(sizeGB); err != nil {
		return swapSetReply{}, err
	}
	if e.inContainer() {
		return swapSetReply{}, fmt.Errorf("节点 agent 运行在容器中，无法管理宿主机的 Swap")
	}
	st, err := e.readState()
	if err != nil {
		return swapSetReply{}, err
	}
	before := st.totals

	freeMB, err := e.diskFreeMB(filepath.Dir(e.filePath))
	if err != nil {
		return swapSetReply{}, fmt.Errorf("读取磁盘剩余空间失败: %v", err)
	}
	if err := checkSwapCapacity(freeMB, st.fileSizeMB, sizeGB); err != nil {
		return swapSetReply{}, err
	}

	wasActive := st.managed != nil
	if wasActive {
		if st.managed.UsedMB > 0 {
			if !st.memAvailOK {
				return swapSetReply{}, fmt.Errorf("拒绝执行：无法读取当前可用内存（MemAvailable 缺失），不能确认 swapoff 现有 Swap（已用 %d MB）是否安全；机器状态未改变", st.managed.UsedMB)
			}
			if swapoffUnsafe(st.managed.UsedMB, st.memAvailMB) {
				return swapSetReply{}, fmt.Errorf("拒绝执行：现有 /swapfile 已用 %d MB，超过当前可用内存 %d MB，此时 swapoff 会把换出页全部压回内存，可能把机器压死；请先降低内存占用或在低峰期再试。机器状态未改变", st.managed.UsedMB, st.memAvailMB)
			}
		}
		if _, err := e.run(ctx, "swapoff", e.filePath); err != nil {
			return swapSetReply{}, swapStepError("关闭现有 Swap", err, "机器状态未改变，现有 Swap 仍在启用中")
		}
	}

	// 旧文件先改名备份：重建任一步失败都以它恢复执行前状态；
	// fstab 在新 Swap 真正启用前一字不动，杜绝「fstab 已写、
	// swap 没启用」的半成品。
	backedUp := false
	if st.fileOnDisk {
		_ = os.Remove(e.backupPath) // 清理上次崩溃残留的备份（尽力）
		if err := os.Rename(e.filePath, e.backupPath); err != nil {
			return swapSetReply{}, swapStepError("备份现有 /swapfile", err, e.restoreActiveNote(wasActive))
		}
		backedUp = true
	}
	restore := func(step string, cause error) (swapSetReply, error) {
		return swapSetReply{}, swapStepError(step, cause, e.restoreFromBackup(backedUp, wasActive))
	}

	if _, err := e.run(ctx, "fallocate", "-l", fmt.Sprintf("%dG", sizeGB), e.filePath); err != nil {
		// fallocate 在个别文件系统上不受支持（overlayfs 等），
		// 回退 dd 顺序写。
		if _, derr := e.run(ctx, "dd", "if=/dev/zero", "of="+e.filePath, "bs=1M",
			fmt.Sprintf("count=%d", sizeGB*1024), "status=none"); derr != nil {
			return restore("创建 Swap 文件", fmt.Errorf("fallocate 失败（%v），dd 回退亦失败（%v）", err, derr))
		}
	}
	if err := os.Chmod(e.filePath, 0o600); err != nil {
		return restore("设置 Swap 文件权限", err)
	}
	if _, err := e.run(ctx, "mkswap", e.filePath); err != nil {
		return restore("格式化 Swap（mkswap）", err)
	}
	if _, err := e.run(ctx, "swapon", e.filePath); err != nil {
		return restore("启用 Swap（swapon）", err)
	}

	// 新 Swap 已生效：旧备份内容（换出页已在 swapoff 时回内存）
	// 不再有恢复价值，先清备份；再收敛 fstab——这一步失败不回滚
	// 已生效的 Swap，但错误里必须明示实际状态与人工补法。
	var warnings []string
	if backedUp {
		if err := os.Remove(e.backupPath); err != nil {
			warnings = append(warnings, fmt.Sprintf("旧 Swap 备份文件 %s 删除失败，可人工删除: %v", e.backupPath, err))
		}
	}
	if err := e.convergeFstabFile(); err != nil {
		return swapSetReply{}, fmt.Errorf("Swap 已按 %d GB 启用，但写入 /etc/fstab 失败: %v；当前实际状态：新 Swap 正在生效，但重启后不会自动挂载，请人工在 /etc/fstab 中补一行「%s none swap sw 0 0」", sizeGB, err, e.filePath)
	}

	after, err := e.readState()
	if err != nil {
		return swapSetReply{}, fmt.Errorf("Swap 已按 %d GB 启用，但复读状态失败: %v；请用 SwapInfo 刷新确认当前状态", sizeGB, err)
	}
	return swapSetReply{Before: before, After: after.totals, SizeGB: sizeGB, Warnings: warnings}, nil
}

// restoreActiveNote 用于「swapoff 已执行、备份改名失败」一步：
// 旧文件还在原地，尽量把 Swap 重新启用后报告实际状态。
func (e swapEnv) restoreActiveNote(wasActive bool) string {
	if !wasActive {
		return "现有 /swapfile 未被改动"
	}
	if _, err := e.run(context.Background(), "swapon", e.filePath); err != nil {
		return fmt.Sprintf("现有 /swapfile 已被关闭且重新启用失败（%v）：当前无 Swap 生效，请人工执行 swapon %s", err, e.filePath)
	}
	return "现有 Swap 已重新启用，机器状态未改变"
}

// restoreFromBackup 在重建中途失败时恢复执行前状态，返回给用户
// 的实际状态说明。恢复动作走 Background 上下文：触发失败的
// 命令预算可能已耗尽，不能连恢复一起饿死。
func (e swapEnv) restoreFromBackup(backedUp bool, wasActive bool) string {
	_ = os.Remove(e.filePath) // 清掉未完成的新文件（尽力）
	if !backedUp {
		return "已清理未完成的 Swap 文件，机器保持执行前状态"
	}
	if err := os.Rename(e.backupPath, e.filePath); err != nil {
		return fmt.Sprintf("恢复旧 Swap 文件失败（%v）：旧文件保留在 %s，当前 /swapfile 未启用，需人工处理", err, e.backupPath)
	}
	if wasActive {
		if _, err := e.run(context.Background(), "swapon", e.filePath); err != nil {
			return fmt.Sprintf("旧 Swap 文件已还原到 %s，但重新启用失败（%v）：当前无 Swap 生效，请人工执行 swapon %s", e.filePath, err, e.filePath)
		}
		return "已恢复执行前状态：旧 /swapfile 已还原并重新启用"
	}
	return "已恢复执行前状态：旧 /swapfile 文件已还原（原本未启用）"
}

// convergeFstabFile 把 /etc/fstab 收敛为恰好一条托管条目（内容
// 不变时不写盘）。托管路径以外的条目一律不碰。
func (e swapEnv) convergeFstabFile() error {
	content, err := os.ReadFile(e.fstabPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next, changed := convergeFstab(string(content), e.filePath)
	if !changed {
		return nil
	}
	return writeFileAtomic(e.fstabPath, []byte(next), 0o644)
}

// --- 命令分发 -----------------------------------------------------------

func handleSwapCommand(cmdType string, data interface{}) (interface{}, error) {
	return handleSwapCommandContext(context.Background(), cmdType, data)
}

// handleSwapCommandContext 与 handleTuneCommandContext 同口径：
// 已耗尽的命令预算直接拒绝，不替已放弃的调用方执行。
func handleSwapCommandContext(ctx context.Context, cmdType string, data interface{}) (interface{}, error) {
	return handleSwapCommandEnv(ctx, realSwapEnv(), cmdType, data)
}

func handleSwapCommandEnv(ctx context.Context, env swapEnv, cmdType string, data interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("命令执行预算已耗尽，拒绝执行: %w", err)
	}
	switch cmdType {
	case "SwapInfo":
		return env.info()
	case "SwapSet":
		var req struct {
			SizeGB int `json:"sizeGB"`
		}
		if data != nil {
			raw, err := json.Marshal(data)
			if err != nil {
				return nil, fmt.Errorf("序列化数据失败: %v", err)
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, fmt.Errorf("解析 Swap 参数失败: %v", err)
			}
		}
		return env.set(ctx, req.SizeGB)
	}
	return nil, fmt.Errorf("未知命令类型: %s", cmdType)
}
