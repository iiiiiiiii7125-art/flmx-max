package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 流量可靠上报（spool）：待上报流量数值先落盘、面板确认后才清。
//
// 此前待确认批次只活在进程内存：agent 被升级重启、崩溃或整机断电
// 时，自上次成功上报以来累积的全部流量随进程消失，无恢复路径；
// seq 文件只保序号不保数值。现每个上报周期先把计数快照写入 spool
// 落盘（先落盘后清内存），面板回执与所发批次的 stream+sequence
// 完全一致才清 Pending 并推进序号；进程重启后从 spool 续报。
//
// 身份隔离：spool 文件名由「归一化面板地址＋节点 secret」哈希决
// 定（TrafficSpoolIdentity），换面板/换 secret 自动换文件，旧主
// 人的流量不会扣到新主人头上；同面板仅改地址写法不换文件。
// stream 是每份 spool 的随机身份：面板按 (节点, stream) 记游标，
// 序号只在同一 stream 内单调递进；面板备份恢复使游标回退时，
// 经 reset_stream 换新 stream 原字节重报（见 recoverRejected）。

// trafficEnvelope 是一个待确认批次的线上形态。
type trafficEnvelope struct {
	Stream   string              `json:"stream"`
	Sequence int64               `json:"sequence"`
	Items    []TrafficReportItem `json:"items"`
}

// trafficSpoolState 是 spool 文件的落盘形态。
type trafficSpoolState struct {
	Stream   string                `json:"stream"`
	Next     int64                 `json:"next"`
	Pending  *trafficEnvelope      `json:"pending,omitempty"`
	Buffered []TrafficReportItem   `json:"buffered,omitempty"`
	Retry    [][]TrafficReportItem `json:"retry,omitempty"`
}

type trafficSpool struct {
	path  string
	state trafficSpoolState
}

var configuredTrafficSpool *trafficSpool

// trafficSpoolFileName 是 spool 的生产命名派生（身份串的 sha256）：
// 「换面板/换 secret 不串账」的隔离语义只此一处实现，派生一改、全
// 部节点的 spool 选址都变——由 traffic_spool_identity_test 以金值
// 钉死（审计 AG-07：此前测试全走注入路径，派生裸奔无人守）。
func trafficSpoolFileName(identity string) string {
	return fmt.Sprintf("flmx_traffic_%x.json", sha256.Sum256([]byte(identity)))
}

// TrafficSpoolIdentity 派生 spool 身份串：面板地址先经与上报 URL
// 构建同源的 normalizeReportAddress 归一化（去 scheme、去路径、
// 保留 host:port），再与 secret 拼接。此前身份直接用 config.Addr
// 原文——同一面板改一下地址写法（补 scheme、加尾缀路径等）就被
// 判为「换面板」，旧 spool 的未确认流量被静默弃置（审计 A-T-04）。
// 归一化口径与 buildReportURLCandidates 逐字同源，不另写一份。
func TrafficSpoolIdentity(addr, secret string) string {
	normalized, _ := normalizeReportAddress(addr)
	if normalized == "" {
		normalized = strings.TrimSpace(addr)
	}
	return normalized + "\x00" + secret
}

// ConfigureTrafficSpool 在服务启动前选定 spool 文件（按身份隔离）。
// 由主程序在加载 config.json 后调用；失败（文件损坏等）返回错误，
// 调用方应拒绝启动——静默换新 spool 会丢掉未确认流量且无痕。
// 选定后顺带扫描同目录其他身份的 spool 文件并逐个告警（见
// findOrphanTrafficSpools）：地址归一化后仍可能因 secret 轮换等
// 留下孤儿，其未确认流量不自动并账、须人工核对。
func ConfigureTrafficSpool(dir, identity string) error {
	path := filepath.Join(dir, trafficSpoolFileName(identity))
	spool, err := openTrafficSpool(path)
	if err != nil {
		return err
	}
	configuredTrafficSpool = spool
	for _, orphan := range findOrphanTrafficSpools(dir, path) {
		fmt.Printf("⚠️ 发现其他身份的流量 spool 文件 %s：其中的未确认流量不会自动上报，如系本节点旧地址/旧 secret 遗留，请人工核对处理\n", orphan)
	}
	for _, name := range sweepRejectedTrafficArchives(dir, time.Now()) {
		fmt.Printf("🧹 流量隔离留证 %s 已超出保留上限（%d 天 / 最新 %d 份），已清理\n", name, int(rejectedArchiveMaxAge/(24*time.Hour)), rejectedArchiveMaxCount)
	}
	return nil
}

// findOrphanTrafficSpools 枚举 dir 下不属于 ownPath 的 spool 文件
// （返回带大小的点名串）。只点名不处置：孤儿批次的归属无法由本
// 进程证明，自动删除可能销毁待确认流量、自动并账可能串账。隔离
// 留证存档（.rejected-*.json）不算孤儿，不在此列——它们由
// sweepRejectedTrafficArchives 按保留上限老化清理；原子写的 .tmp
// 残留不以 .json 结尾、天然不入列。
func findOrphanTrafficSpools(dir, ownPath string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var orphans []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "flmx_traffic_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if strings.Contains(name, ".rejected-") || filepath.Join(dir, name) == ownPath {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		orphans = append(orphans, fmt.Sprintf("%s（%d 字节）", name, info.Size()))
	}
	sort.Strings(orphans)
	return orphans
}

// 隔离留证的老化上限：回执冲突时被隔离的批次存档（.rejected-*.json）
// 是排障证据，保留 30 天且至多最新 100 份；超出即清。此前它们被
// 孤儿扫描显式排除、只靠人工处置，冲突频发的节点上会无声累积
// （R2-038 实例三）。
const (
	rejectedArchiveMaxAge   = 30 * 24 * time.Hour
	rejectedArchiveMaxCount = 100
)

// sweepRejectedTrafficArchives 删除 dir 下超出保留上限的隔离留证
// 存档，返回实际删除的文件名。识别口径与孤儿扫描一致（flmx_traffic_
// 前缀＋.rejected- 标记＋.json 后缀）。只动留证存档：孤儿 spool 本体
// 仍只点名不删除。尽力而为——单个删除失败只跳过，绝不阻断启动。
func sweepRejectedTrafficArchives(dir string, now time.Time) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type archive struct {
		name string
		mod  time.Time
	}
	var archives []archive
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "flmx_traffic_") || !strings.Contains(name, ".rejected-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		archives = append(archives, archive{name: name, mod: info.ModTime()})
	}
	// Newest first: index >= cap keeps only the newest cap entries.
	sort.Slice(archives, func(i, j int) bool { return archives[i].mod.After(archives[j].mod) })
	var removed []string
	for i, a := range archives {
		if i < rejectedArchiveMaxCount && now.Sub(a.mod) <= rejectedArchiveMaxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, a.name)); err == nil {
			removed = append(removed, a.name)
		}
	}
	sort.Strings(removed)
	return removed
}

func openTrafficSpool(path string) (*trafficSpool, error) {
	s := &trafficSpool{path: path}
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(data, &s.state); err != nil {
				return nil, fmt.Errorf("invalid traffic spool: %w", err)
			}
			stream, err := hex.DecodeString(s.state.Stream)
			if err != nil || len(stream) != 16 || s.state.Next < 1 {
				return nil, errors.New("invalid traffic spool identity")
			}
			if p := s.state.Pending; p != nil && (p.Stream != s.state.Stream || p.Sequence != s.state.Next) {
				return nil, errors.New("invalid pending traffic identity")
			}
			return s, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	s.state = trafficSpoolState{Stream: hex.EncodeToString(id[:]), Next: 1}
	if err := s.save(s.state); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *trafficSpool) save(state trafficSpoolState) error {
	if s.path == "" {
		return nil
	} // 内存 spool（嵌入调用方/测试）：无落盘语义。
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeSeqFileAtomic(s.path, data)
}

// saveTrafficArchive 把隔离留证的批次写到独立文件（原子写）。
func saveTrafficArchive(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeSeqFileAtomic(path, data)
}

func mergeTrafficItems(groups ...[]TrafficReportItem) []TrafficReportItem {
	totals := map[string]TrafficReportItem{}
	for _, items := range groups {
		for _, item := range items {
			v := totals[item.N]
			v.N = item.N
			v.U += item.U
			v.D += item.D
			totals[item.N] = v
		}
	}
	out := make([]TrafficReportItem, 0, len(totals))
	for _, item := range totals {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out
}

// confirmAck 在面板确认当前待确认批次后推进 spool 状态：清批次，
// 并落盘成功后才替换内存状态（落盘失败保留 Pending，下个周期重
// 发，面板按摘要判重放不双计）。
//
// C-C2-01 自愈：回执游标 panelCursor 严格大于所发序号时，说明面
// 板已入账过本 stream 更靠后的批次、而本机 spool 状态被外部回
// 退（整机快照恢复/旧备份还原/整目录克隆双写）——继续沿旧
// stream 推进，Next 追平游标前的每个新批次都会被面板按「已入
// 账重放」只确认不入账、静默少计。故此时照常清除已确认批次
// （绝不重发——它在面板侧已有入账记录，重发即双计），但当场
// 轮换 stream：Buffered/Retry 里的未入账字节与后续新批次改走
// 新 stream、从序号 1 重新入账。轮换只影响未确认字节，与
// reset_stream 的「拒收批次原字节换流重报」本质不同，无双计
// 通道。panelCursor 为 0（旧面板回执无 cursor 字段）或不大于
// 所发序号时行为与此前完全一致（Next++）。返回是否发生了轮换。
func (s *trafficSpool) confirmAck(panelCursor int64) (bool, error) {
	pending := s.state.Pending
	if pending == nil {
		return false, nil
	}
	next := s.state
	next.Pending = nil
	rotated := false
	if panelCursor > pending.Sequence {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return false, err
		}
		next.Stream, next.Next = hex.EncodeToString(id[:]), 1
		rotated = true
	} else {
		next.Next++
	}
	if err := s.save(next); err != nil {
		return false, err
	}
	s.state = next
	return rotated, nil
}

// recoverRejected 处理面板的三路拒绝。新状态落盘成功后才替换内存
// 状态，绝不在落盘前改动待确认批次的身份。
//   - reset_stream：面板游标丢失/回退（备份恢复等）——换新 stream
//     身份，批次原字节不变重报，流量不丢、不重。
//   - invalid_batch：批次本身非法——二分，健康的一半先行入账，
//     另一半排队重试；二分到单条仍非法时按 identity_conflict 隔离。
//   - identity_conflict：同序号不同内容——争议批次隔离存档留证，
//     绝不重放（重放可能把已入账字节二次扣费）。
func (s *trafficSpool) recoverRejected(reason trafficReportRejection) error {
	pending := s.state.Pending
	if pending == nil {
		return nil
	}
	next := s.state
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	next.Stream, next.Next = hex.EncodeToString(id[:]), 1
	replacement := *pending
	replacement.Stream, replacement.Sequence = next.Stream, 1
	next.Pending = &replacement
	switch reason {
	case trafficRejectionResetStream: // 只换丢失的游标身份，字节不变。
	case trafficRejectionInvalidBatch:
		if len(pending.Items) > 1 {
			mid := len(pending.Items) / 2
			replacement.Items = pending.Items[:mid]
			next.Retry = append([][]TrafficReportItem{pending.Items[mid:]}, next.Retry...)
			break
		}
		fallthrough
	case trafficRejectionIdentityConflict:
		// 先留证再放行队列：隔离文件落盘失败时整次恢复失败，
		// Pending 原样保留，下个周期重试恢复动作本身。
		if s.path == "" {
			return errors.New("cannot quarantine traffic without a spool path")
		}
		archive := fmt.Sprintf("%s.rejected-%s-%d.json", s.path, pending.Stream, pending.Sequence)
		if err := saveTrafficArchive(archive, struct {
			Reason trafficReportRejection `json:"reason"`
			Batch  *trafficEnvelope       `json:"batch"`
		}{reason, pending}); err != nil {
			return err
		}
		fmt.Printf("⚠️ 流量批次已隔离留证: %s (%s)\n", archive, reason)
		next.Pending = nil
	default:
		return reason
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.state = next
	return nil
}
