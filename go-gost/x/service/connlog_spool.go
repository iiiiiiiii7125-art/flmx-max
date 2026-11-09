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
	"time"
)

// 入站连接日志可靠上报 spool（U2）：形态与纪律全仿流量 spool v2
// （traffic_spool.go），差异只在载荷是连接事件而非计数：
//
//   - 待确认批次先落盘、面板回执与所发批次的 stream+sequence 完
//     全一致才清 Pending 并推进序号；进程重启/崩溃后从 spool 续
//     报，未确认事件不丢（设计 §4.4）。
//   - spool 文件名按「归一化面板地址＋节点 secret」身份隔离
//     （TrafficSpoolIdentity 同源派生，前缀 flmx_connlog_）：换面
//     板/换 secret 自动换文件，旧主人的连接记录不会报到新主人。
//   - stream 是每份 spool 的持久随机身份，面板按 (节点, stream)
//     记游标，序号只在同一 stream 内单调递进；面板游标回退走
//     reset_stream 换流重报、游标超前走 confirmAck 轮换自愈。
//   - 容量上限 16MB（§4.2）：统计口径为 Pending+Retry+Buffered
//     的事件序列化字节；溢出只许动 Buffered（待确认批次与二分
//     重试队列是已承诺要送达的，不许丢），丢最旧并计数。
//   - Open/OpenLastKnown 是 §4.6 结清台账：记录已见 open 未见
//     close 的连接及其最后已知时刻，随 spool 一起落盘，进程换
//     代时据此批量结清 interrupted。

// connlogEnvelope 是一个待确认批次的线上形态（§5.3：stream+
// sequence+digest 游标幂等骨架；boot_id 与 dropped 随批附带）。
type connlogEnvelope struct {
	Stream   string      `json:"stream"`
	Sequence int64       `json:"sequence"`
	Digest   string      `json:"digest"`
	BootID   string      `json:"boot_id"`
	Dropped  int64       `json:"dropped"`
	Events   []ConnEvent `json:"events"`
}

// connlogSpoolState 是 spool 文件的落盘形态。
type connlogSpoolState struct {
	Stream  string            `json:"stream"`
	Next    int64             `json:"next"`
	Pending *connlogEnvelope  `json:"pending,omitempty"`
	Buffered []ConnEvent      `json:"buffered,omitempty"`
	Retry   [][]ConnEvent     `json:"retry,omitempty"`
	// Open 未闭合连接台账：conn_id → open 事件全量（结清时拼
	// interrupted close 需要原始字段）；OpenLastKnown 为 conn_id
	// → 最后已知时刻（毫秒），interrupted 的 end_time 只许取它。
	Open          map[string]ConnEvent `json:"open,omitempty"`
	OpenLastKnown map[string]int64     `json:"open_last_known,omitempty"`
	BootID        string               `json:"boot_id,omitempty"`
	// Dropped 累计采集丢弃数中尚未被面板确认收到过的部分（随批
	// 上行、确认后扣减，§4.2）。
	Dropped int64 `json:"dropped"`
}

type connlogSpool struct {
	path  string
	state connlogSpoolState
}

var configuredConnlogSpool *connlogSpool

// connlogSpoolFileName 与流量 spool 同派生纪律：身份串的 sha256，
// 前缀区分功能面。派生一改、全部节点选址都变，慎改。
func connlogSpoolFileName(identity string) string {
	return fmt.Sprintf("flmx_connlog_%x.json", sha256.Sum256([]byte(identity)))
}

// ConfigureConnlogSpool 在服务启动前选定 spool 文件（按身份隔
// 离）。与流量 spool 的关键口径差异：连接日志是纯旁路，初始化
// 失败绝不拒绝 agent 启动——存量文件损坏时先把损坏文件改名留
// 证（.corrupt-<unix>）再建新 spool 并点名告警，丢失的只是无法
// 解析的历史未报事件且痕迹可见；仅当连新文件都建不出来时才返
// 回错误，调用方记日志后上报器自动回退内存 spool。
func ConfigureConnlogSpool(dir, identity string) error {
	path := filepath.Join(dir, connlogSpoolFileName(identity))
	spool, err := openConnlogSpool(path)
	if err != nil {
		quarantine := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		if renErr := os.Rename(path, quarantine); renErr == nil {
			fmt.Printf("⚠️ 连接日志 spool 文件损坏，已留证至 %s 并新建 spool（其中未报事件已丢失）: %v\n", quarantine, err)
			spool, err = openConnlogSpool(path)
		}
		if err != nil {
			return err
		}
	}
	configuredConnlogSpool = spool
	return nil
}

func openConnlogSpool(path string) (*connlogSpool, error) {
	s := &connlogSpool{path: path}
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(data, &s.state); err != nil {
				return nil, fmt.Errorf("invalid connlog spool: %w", err)
			}
			stream, err := hex.DecodeString(s.state.Stream)
			if err != nil || len(stream) != 16 || s.state.Next < 1 {
				return nil, errors.New("invalid connlog spool identity")
			}
			if p := s.state.Pending; p != nil && (p.Stream != s.state.Stream || p.Sequence != s.state.Next) {
				return nil, errors.New("invalid pending connlog identity")
			}
			s.state.normalize()
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
	s.state = connlogSpoolState{Stream: hex.EncodeToString(id[:]), Next: 1}
	s.state.normalize()
	if err := s.save(s.state); err != nil {
		return nil, err
	}
	return s, nil
}

func (st *connlogSpoolState) normalize() {
	if st.Open == nil {
		st.Open = make(map[string]ConnEvent)
	}
	if st.OpenLastKnown == nil {
		st.OpenLastKnown = make(map[string]int64)
	}
}

func (s *connlogSpool) save(state connlogSpoolState) error {
	if s.path == "" {
		return nil
	} // 内存 spool（嵌入调用方/测试）：无落盘语义。
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeSeqFileAtomic(s.path, data)
}

// connlogEventsBytes 估算一组事件的序列化字节（容量口径）。
func connlogEventsBytes(events []ConnEvent) int64 {
	var n int64
	for _, ev := range events {
		line, _ := json.Marshal(ev)
		n += int64(len(line)) + 1
	}
	return n
}

// queuedBytes 当前全部未确认事件（Pending+Retry+Buffered）的
// 序列化字节，即 §4.2 的 spool 容量占用口径。
func (st *connlogSpoolState) queuedBytes() int64 {
	var n int64
	if st.Pending != nil {
		n += connlogEventsBytes(st.Pending.Events)
	}
	for _, group := range st.Retry {
		n += connlogEventsBytes(group)
	}
	n += connlogEventsBytes(st.Buffered)
	return n
}

// enforceCap 在 state 副本上执行容量上限：只从 Buffered 头部丢
// 最旧，丢弃数累加进 state.Dropped（随批上行）。Pending/Retry
// 不动——它们是已组批承诺送达的事件。返回丢弃条数。
func (st *connlogSpoolState) enforceCap(maxBytes int64) int {
	dropped := 0
	for st.queuedBytes() > maxBytes && len(st.Buffered) > 0 {
		st.Buffered = st.Buffered[1:]
		dropped++
	}
	if dropped > 0 {
		st.Dropped += int64(dropped)
	}
	return dropped
}

// confirmAck 在面板确认当前待确认批次后推进 spool 状态：清批
// 次、扣减已确认的丢弃计数，并落盘成功后才替换内存状态（落盘
// 失败保留 Pending，下个周期重发，面板按 conn_id 判重不双记）。
//
// 游标自愈与流量 confirmAck 同口径（C-C2-01）：回执 cursor 严
// 格大于所发序号，说明面板已入账更靠后的批次而本机状态被外部
// 回退——照常清批次（绝不重发已入账事件），但当场轮换 stream，
// 未入账事件改走新 stream 从序号 1 重新入账。cursor 为 0（旧
// 面板无 cursor）或不大于所发序号时仅 Next++。返回是否轮换。
func (s *connlogSpool) confirmAck(panelCursor int64) (bool, error) {
	pending := s.state.Pending
	if pending == nil {
		return false, nil
	}
	next := s.state
	next.Pending = nil
	if pending.Dropped > 0 {
		next.Dropped -= pending.Dropped
		if next.Dropped < 0 {
			next.Dropped = 0
		}
	}
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

// recoverRejected 处理面板的三路拒绝（与流量 recoverRejected 同
// 纪律）：新状态落盘成功后才替换内存状态，绝不在落盘前改动待
// 确认批次的身份。
//   - reset_stream：面板游标丢失/回退——换新 stream 身份，批次
//     事件原样重报，不丢不重。
//   - invalid_batch：批次本身非法——二分，健康的一半先行入账，
//     另一半排队重试；二分到单条仍非法时按 identity_conflict
//     隔离。
//   - identity_conflict：同序号不同内容——争议批次隔离存档留
//     证，绝不重放；被隔离的事件计入 Dropped（弹窗页脚可见，
//     不假装没丢）。
func (s *connlogSpool) recoverRejected(reason connlogReportRejection) error {
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
	replacement.Digest = connlogBatchDigest(replacement.Events)
	next.Pending = &replacement
	switch reason {
	case connlogRejectionResetStream: // 只换丢失的游标身份，事件不变。
	case connlogRejectionInvalidBatch:
		if len(pending.Events) > 1 {
			mid := len(pending.Events) / 2
			replacement.Events = pending.Events[:mid]
			replacement.Digest = connlogBatchDigest(replacement.Events)
			next.Pending = &replacement
			next.Retry = append([][]ConnEvent{pending.Events[mid:]}, next.Retry...)
			break
		}
		fallthrough
	case connlogRejectionIdentityConflict:
		// 先留证再放行队列：隔离文件落盘失败时整次恢复失败，
		// Pending 原样保留，下个周期重试恢复动作本身。
		if s.path == "" {
			return errors.New("cannot quarantine connlog without a spool path")
		}
		archive := fmt.Sprintf("%s.rejected-%s-%d.json", s.path, pending.Stream, pending.Sequence)
		if err := saveTrafficArchive(archive, struct {
			Reason connlogReportRejection `json:"reason"`
			Batch  *connlogEnvelope       `json:"batch"`
		}{reason, pending}); err != nil {
			return err
		}
		fmt.Printf("⚠️ 连接日志批次已隔离留证: %s (%s)\n", archive, reason)
		next.Pending = nil
		next.Dropped += int64(len(pending.Events))
	default:
		return reason
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.state = next
	return nil
}
