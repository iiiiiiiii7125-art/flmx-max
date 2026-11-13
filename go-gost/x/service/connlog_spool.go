package service

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
//   - CPU-S2：热路径落盘是增量 journal（快照＋WAL），不再每 tick
//     全量重写。persistLocked 每周期只把本周期增量（新事件、
//     队头丢弃、组批、丢弃计数）以一条记录追加进 <spool>.journal
//     并 fsync，代价 ∝ 本周期增量、与 spool 总字节无关；快照文
//     件只在 journal 攒满 1MB、或确认/自愈/启停等低频全量动作
//     时才重写（称 checkpoint），摊薄后单事件落盘代价有界。
//     快照带 JournalSeq（已入快照的最后一条 journal 序号），
//     快照与 journal 截断之间的崩溃窗口靠序号去重——重放时
//     跳过 seq <= JournalSeq 的旧记录；journal 尾部撕裂行
//     （写一半即崩溃）直接丢弃，只丢未 fsync 的增量，不丢已
//     确认落盘的数据。

// connlogJournalMaxBytes 是 journal 攒满即 checkpoint 的阈值
// （默认 1MB 增量才换一次 O(n) 全量快照）。

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
	Stream   string           `json:"stream"`
	Next     int64            `json:"next"`
	Pending  *connlogEnvelope `json:"pending,omitempty"`
	Buffered []ConnEvent      `json:"buffered,omitempty"`
	Retry    [][]ConnEvent    `json:"retry,omitempty"`
	// Open 未闭合连接台账：conn_id → open 事件全量（结清时拼
	// interrupted close 需要原始字段）；OpenLastKnown 为 conn_id
	// → 最后已知时刻（毫秒），interrupted 的 end_time 只许取它。
	Open          map[string]ConnEvent `json:"open,omitempty"`
	OpenLastKnown map[string]int64     `json:"open_last_known,omitempty"`
	BootID        string               `json:"boot_id,omitempty"`
	// Dropped 累计采集丢弃数中尚未被面板确认收到过的部分（随批
	// 上行、确认后扣减，§4.2）。
	Dropped int64 `json:"dropped"`
	// JournalSeq 是已入本快照的最后一条 journal 记录序号
	// （CPU-S2）：重放时跳过 seq <= 此值的旧记录，快照写盘与
	// journal 截断之间的崩溃不致双记。旧快照无此字段即为 0。
	JournalSeq int64 `json:"journal_seq,omitempty"`
}

// connlogJournalRecord 是一条 journal 增量记录（CPU-S2）：
// persistLocked 一个周期产生的全部状态变化都能表成它——
// 追加事件（含 L1-01 清道夫结清的 close）、队头丢弃数、
// 组批（Pending 信封全量，<=500 条有界）、丢弃计数增量。
// 重放按「追加→队头丢→组批摘除」顺序执行，与 persistLocked
// 内的原始顺序一致；Open 台账由追加事件本身推导（open 置入、
// close 删出台账），与原始循环逐条同规则。
type connlogJournalRecord struct {
	Seq          int64            `json:"seq"`
	DroppedDelta int64            `json:"dropped_delta,omitempty"`
	Appended     []ConnEvent      `json:"appended,omitempty"`
	DropHead     int              `json:"drop_head,omitempty"`
	Batch        *connlogEnvelope `json:"batch,omitempty"`
	FromRetry    bool             `json:"from_retry,omitempty"`
}

type connlogSpool struct {
	path  string
	state connlogSpoolState
	// journalSeq 是已落盘（快照＋journal）的最后一条记录序号；
	// journalBytes 是自上次 checkpoint 以来 journal 文件的字节
	// 数（用于攒满即 checkpoint 的阈值判断）。
	journalSeq   int64
	journalBytes int64
	// journalMaxBytes 是 journal 攒满即 checkpoint 的阈值；
	// openConnlogSpool 置默认值，测试可调小以强制 checkpoint。
	journalMaxBytes int64
}

const defaultConnlogJournalMaxBytes = 1 << 20 // 1MB 增量换一次全量快照

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
	s := &connlogSpool{path: path, journalMaxBytes: defaultConnlogJournalMaxBytes}
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
			// CPU-S2：重放快照之后落盘的 journal 增量（崩溃后续报
			// 的唯一依据）。重放失败只告警不停机——快照本身完整，
			// 丢的至多是快照后未 checkpoint 的增量，且痕迹可见。
			s.journalSeq = s.state.JournalSeq
			if n, rerr := s.replayJournal(); rerr != nil {
				fmt.Printf("⚠️ 连接日志 journal 重放中断（已应用 %d 条，其后增量丢弃）: %v\n", n, rerr)
			}
			return s, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		// 快照不存在即全新一代：同名 stale journal（旧快照被隔离/
		// 删除后残留）绝不能重放到新 stream 上，先行清除。
		_ = os.Remove(s.journalPath())
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

// journalPath 返回 journal 文件路径（快照路径＋.journal 后缀，
// 不以 .json 结尾，天然不入孤儿 spool 扫描）。
func (s *connlogSpool) journalPath() string {
	if s.path == "" {
		return ""
	}
	return s.path + ".journal"
}

// replayJournal 把 journal 文件里的记录按序应用到 s.state（CPU-S2）：
// 只应用 seq > 快照 JournalSeq 的记录；序号断档或中间行损坏即停
// （其后记录可能依赖缺失的增量，不可再信）；尾部无换行残行是写
// 一半即崩溃的撕裂行，直接丢弃。返回已应用条数与中断原因
// （nil 表示读完整个文件、含无 journal 文件）。
func (s *connlogSpool) replayJournal() (int, error) {
	jpath := s.journalPath()
	if jpath == "" {
		return 0, nil
	}
	data, err := os.ReadFile(jpath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	s.journalBytes = int64(len(data))
	applied := 0
	rest := data
	for len(rest) > 0 {
		i := 0
		for i < len(rest) && rest[i] != '\n' {
			i++
		}
		if i == len(rest) {
			// 尾部无换行残行：上一条记录写一半即崩溃，丢弃。
			if len(bytes.TrimSpace(rest)) > 0 {
				return applied, fmt.Errorf("journal 尾部撕裂行已丢弃（%d 字节未换行）", len(rest))
			}
			break
		}
		line := rest[:i]
		rest = rest[i+1:]
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec connlogJournalRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return applied, fmt.Errorf("journal 第 %d 条损坏，停止重放: %w", applied+1, err)
		}
		if rec.Seq <= s.journalSeq {
			continue // 已入快照（快照写盘与 journal 截断之间崩溃），跳过防双记。
		}
		if rec.Seq != s.journalSeq+1 {
			return applied, fmt.Errorf("journal 序号断档：快照后应为 %d，实见 %d，停止重放", s.journalSeq+1, rec.Seq)
		}
		s.state.applyJournalRecord(&rec)
		s.journalSeq = rec.Seq
		applied++
	}
	return applied, nil
}

// applyJournalRecord 把一条 journal 记录应用到状态上（CPU-S2）：
// 顺序与 persistLocked 原始顺序一致——追加事件（含 Open 台账
// 推导）→队头丢弃→组批摘除。调用方保证序号连续。
func (st *connlogSpoolState) applyJournalRecord(rec *connlogJournalRecord) {
	st.Dropped += rec.DroppedDelta
	for _, ev := range rec.Appended {
		st.Buffered = append(st.Buffered, ev)
		switch ev.Type {
		case ConnEventOpen:
			if st.Open == nil {
				st.Open = make(map[string]ConnEvent)
			}
			if st.OpenLastKnown == nil {
				st.OpenLastKnown = make(map[string]int64)
			}
			st.Open[ev.ConnID] = ev
			if ev.StartMs > st.OpenLastKnown[ev.ConnID] {
				st.OpenLastKnown[ev.ConnID] = ev.StartMs
			}
		case ConnEventClose:
			delete(st.Open, ev.ConnID)
			delete(st.OpenLastKnown, ev.ConnID)
		}
	}
	if rec.DropHead > 0 {
		if rec.DropHead >= len(st.Buffered) {
			st.Buffered = nil
		} else {
			st.Buffered = st.Buffered[rec.DropHead:]
		}
	}
	if rec.Batch != nil {
		st.Pending = rec.Batch
		if rec.FromRetry {
			if len(st.Retry) > 0 {
				st.Retry = st.Retry[1:]
			}
		} else if n := len(rec.Batch.Events); n > 0 {
			if n >= len(st.Buffered) {
				st.Buffered = nil
			} else {
				st.Buffered = st.Buffered[n:]
			}
		}
	}
}

// appendJournal 把一条记录以单行追加进 journal 并 fsync（CPU-S2
// 热路径，代价 ∝ 记录字节）：写盘/同步失败时把文件截回追加前
// 的长度再报错——已写一半或未同步的记录绝不留在盘上，否则
// 调用方按失败重试会双记。内存 spool（path 为空）无落盘语义。
func (s *connlogSpool) appendJournal(line []byte) error {
	jpath := s.journalPath()
	if jpath == "" {
		return nil
	}
	f, err := os.OpenFile(jpath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	oldSize, _ := f.Seek(0, io.SeekEnd)
	if _, err := f.Write(line); err != nil {
		f.Close()
		_ = os.Truncate(jpath, oldSize)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Truncate(jpath, oldSize)
		return err
	}
	return f.Close()
}

// truncateJournal 在 checkpoint 后清空 journal（CPU-S2）：快照已
// 含全部状态，journal 归零。截断失败只告警——快照 JournalSeq
// 已推进，重放会跳过旧记录，绝不双记。
func (s *connlogSpool) truncateJournal() {
	jpath := s.journalPath()
	if jpath == "" {
		return
	}
	f, err := os.OpenFile(jpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		fmt.Printf("⚠️ 连接日志 journal 截断失败（旧记录靠序号去重，不双记）: %v\n", err)
		return
	}
	if err := f.Sync(); err != nil {
		fmt.Printf("⚠️ 连接日志 journal 截断同步失败（旧记录靠序号去重，不双记）: %v\n", err)
	}
	f.Close()
	s.journalBytes = 0
}

func (st *connlogSpoolState) normalize() {
	if st.Open == nil {
		st.Open = make(map[string]ConnEvent)
	}
	if st.OpenLastKnown == nil {
		st.OpenLastKnown = make(map[string]int64)
	}
}

// save 是 checkpoint（CPU-S2）：全量快照落盘＋journal 截断。
// 只走低频路径（确认/自愈/启停/换代结清、journal 攒满）；热路
// 径 persistLocked 走 appendJournal，不调 save。快照先写盘
// （原子写＋fsync，JournalSeq 记为当前最后一条记录序号），
// 再截 journal——两者之间崩溃时重放靠序号跳过旧记录，不双记。
// 截断失败只告警不报错（快照本身已是完整新状态）。
func (s *connlogSpool) save(state connlogSpoolState) error {
	if s.path == "" {
		return nil
	} // 内存 spool（嵌入调用方/测试）：无落盘语义。
	state.JournalSeq = s.journalSeq
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := writeSeqFileAtomic(s.path, data); err != nil {
		return err
	}
	s.truncateJournal()
	return nil
}

// connlogEventSize 返回单个事件的序列化字节（len(json.Marshal(ev))+1
// 换行），即 §4.2 容量口径的单条分量。尺寸缓存在事件自身的 size
// 字段（json:"-"，不进线上/落盘格式）；事件建成后字段不可变，缓
// 存永不失效。落盘重载的事件 size 为 0，首次量尺寸时惰性重算一
// 次——此后每周期只做整数累加，不再重 marshal。
func connlogEventSize(ev *ConnEvent) int64 {
	if ev.size > 0 {
		return ev.size
	}
	line, _ := json.Marshal(ev)
	ev.size = int64(len(line)) + 1
	return ev.size
}

// connlogEventsBytes 一组事件的序列化字节（容量口径）。首次调用
// 为每个未缓存尺寸的事件各 marshal 一次；此后全为整数累加。
func connlogEventsBytes(events []ConnEvent) int64 {
	var n int64
	for i := range events {
		n += connlogEventSize(&events[i])
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

// sweepStaleOpen 结清超 TTL 未闭合的 Open 台账条目（L1-01）：
// Xray event_only 会话永无 close 事件，Open 条目不靠本轮清理
// 会永久占 spool 文件（约 270B/条），只在进程换代时结清。
// 超 TTL（默认 24h，与面板侧 24h 僵尸 open 清道夫同口径）的
// 条目在此结清为 interrupted 追加到 Buffered 尾部（其 open
// 事件必在更早周期已入队，顺序上 open 先于 close），EndMs 取
// 最后已知时刻，绝不以清扫时刻冒充断开时刻（§4.6 同纪律）。
// 返回结清条数；ttl <= 0 时不扫。
func (st *connlogSpoolState) sweepStaleOpen(ttl time.Duration, nowMs int64) int {
	if ttl <= 0 || len(st.Open) == 0 {
		return 0
	}
	ttlMs := ttl.Milliseconds()
	swept := 0
	for connID, openEv := range st.Open {
		lastKnown := st.OpenLastKnown[connID]
		if lastKnown <= 0 {
			lastKnown = openEv.StartMs
		}
		if nowMs-lastKnown <= ttlMs {
			continue
		}
		st.Buffered = append(st.Buffered, ConnEvent{
			Type:        ConnEventClose,
			ConnID:      connID,
			EntryKind:   openEv.EntryKind,
			EntryID:     openEv.EntryID,
			ClientIP:    openEv.ClientIP,
			ClientPort:  openEv.ClientPort,
			Network:     openEv.Network,
			SessionKind: openEv.SessionKind,
			StartMs:     openEv.StartMs,
			EndMs:       lastKnown,
			Status:      ConnStatusInterrupted,
		})
		delete(st.Open, connID)
		delete(st.OpenLastKnown, connID)
		swept++
	}
	return swept
}

// enforceCap 在 state 副本上执行容量上限：只从 Buffered 头部丢
// 最旧，丢弃数累加进 state.Dropped（随批上行）。Pending/Retry
// 不动——它们是已组批承诺送达的事件。返回丢弃条数。
//
// 2.1.12：总量只算一次，丢弃时按已缓存的单条尺寸做减法。旧版每
// 丢一条就把整个剩余 spool 全量重 marshal 一遍（O(n²)），73k 条
// spool 下每周期烧掉一整个核。
func (st *connlogSpoolState) enforceCap(maxBytes int64) int {
	dropped := 0
	total := st.queuedBytes()
	for total > maxBytes && len(st.Buffered) > 0 {
		total -= connlogEventSize(&st.Buffered[0])
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
