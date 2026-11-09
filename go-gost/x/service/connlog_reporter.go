package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 入站连接日志上报器（U2，设计 §5.3/§6）：驱动循环与流量可靠
// 上报同构（collectAndReport 的事件版）——每个周期先把环缓冲事
// 件落盘进 spool（先落盘后组批），再发 spool 的待确认批次；面
// 板回执确认后才清批次。节奏差异（§5.3）：含 close 的批次即时
// 发；纯 open 批次按 5s 级节流攒批；单批上限 500 条。
//
// 旁路纪律（§4.1/§4.2）：上报、序列化、落盘全在本 goroutine；
// 生产者（U1/U4）只碰 connRing，Enqueue 永不阻塞、永不报错。

const (
	defaultConnlogRingCapacity   = 4096       // §4.2 内存环缓冲条数
	defaultConnlogSpoolMaxBytes  = 16 << 20   // §4.2 落盘 spool 上限 16MB
	defaultConnlogBatchMaxEvents = 500        // 单批事件上限
	defaultConnlogTick           = time.Second
	defaultConnlogOpenThrottle   = 5 * time.Second // §5.3 open 批量节流（5s 级）
)

// 面板对 connlog v2 上报的三路拒绝（与回执 JSON 的 error 字段对
// 应，语义与流量三路拒绝逐条同源，见 connlog_spool.go）。
const (
	connlogRejectionResetStream      = connlogReportRejection("reset_stream")
	connlogRejectionIdentityConflict = connlogReportRejection("identity_conflict")
	connlogRejectionInvalidBatch     = connlogReportRejection("invalid_batch")
)

// connlogReportRejection 表示面板明确拒收了当前待确认批次（区别
// 于网络失败/回执丢失——后者原样重试，前者走 recoverRejected）。
type connlogReportRejection string

func (e connlogReportRejection) Error() string { return "connlog batch rejected: " + string(e) }

func connlogBatchDigest(events []ConnEvent) string {
	h := sha256.New()
	for _, ev := range events {
		line, _ := json.Marshal(ev)
		h.Write(line)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// connlogReportURL 把 v1 上报候选 URL 改写为 connlog v2 端点，
// 只替换尾段路径（与 reliableReportURL 同纪律）。
func connlogReportURL(parsed *url.URL) {
	if strings.HasSuffix(parsed.Path, "/flow/upload") {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/flow/upload") + "/flow/connlog/v2"
		return
	}
	parsed.Path = "/flow/connlog/v2"
}

// sendConnlogBatch 把一个 spool 批次发往 /flow/connlog/v2（与
// sendReliableTrafficReport 同契约）：返回的 int64 是确认回执带
// 的面板游标；error 为 nil 仅当回执 stream+sequence 与所发批次
// 完全一致且无 error；三路拒绝以 connlogReportRejection 返回；
// 其余失败返回普通错误，批次保留下个周期原样重发。加密沿用
// F-089 fail-closed：加密器缺失直接报错不发明文。entry_node_id
// 不入报文——面板只认 secret 定的节点身份（§3.4）。
func sendConnlogBatch(ctx context.Context, batch connlogEnvelope) (int64, error) {
	if httpReportURL == "" {
		return 0, fmt.Errorf("连接日志上报URL未设置")
	}
	jsonData, err := json.Marshal(batch)
	if err != nil {
		return 0, err
	}
	requestBody, err := encryptReportPayload(jsonData)
	if err != nil {
		return 0, err
	}

	urls := prioritizeURLs(strings.Split(httpReportURL, ","), loadPreferredURL(&preferredConnlogURL))
	var errs []string
	for _, target := range urls {
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host == "" {
			continue
		}
		connlogReportURL(parsed)
		targetURL := parsed.String()
		displayURL := sanitizeReportURL(targetURL)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(requestBody))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "FLMX-Connlog-Reporter/1.0")

		resp, err := reportDo(ctx, req, 5*time.Second)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s => 请求失败: %v", displayURL, redactReportError(err, targetURL, displayURL)))
			continue
		}
		var ack struct {
			Stream   string `json:"stream"`
			Sequence int64  `json:"sequence"`
			Cursor   int64  `json:"cursor"`
			Error    string `json:"error"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&ack)
		_ = resp.Body.Close()
		if decodeErr == nil && ack.Stream == batch.Stream && ack.Sequence == batch.Sequence {
			rejection := connlogReportRejection(ack.Error)
			switch {
			case resp.StatusCode == http.StatusConflict && (rejection == connlogRejectionResetStream || rejection == connlogRejectionIdentityConflict):
				return 0, rejection
			case resp.StatusCode == http.StatusBadRequest && rejection == connlogRejectionInvalidBatch:
				return 0, rejection
			case resp.StatusCode == http.StatusOK && ack.Error == "":
				storePreferredURL(&preferredConnlogURL, target)
				return ack.Cursor, nil
			}
		}
		errs = append(errs, fmt.Sprintf("%s => HTTP %d, 回执与批次不匹配或面板未确认", displayURL, resp.StatusCode))
	}
	return 0, fmt.Errorf("连接日志批次未获面板确认，保留待重试: %s", strings.Join(errs, " | "))
}

var preferredConnlogURL string

// connlogConfig 上报器可调参数（测试以小值/假发送覆盖）。
type connlogConfig struct {
	spool        *connlogSpool // 注入优先；nil 时取 Configure 选定的 spool，再 nil 则内存 spool
	spoolMax     int64
	batchMax     int
	tick         time.Duration
	openThrottle time.Duration
	sendFn       func(ctx context.Context, b connlogEnvelope) (int64, error)
	bootIDFn     func() string
}

func (c *connlogConfig) applyDefaults() {
	if c.spoolMax <= 0 {
		c.spoolMax = defaultConnlogSpoolMaxBytes
	}
	if c.batchMax <= 0 {
		c.batchMax = defaultConnlogBatchMaxEvents
	}
	if c.tick <= 0 {
		c.tick = defaultConnlogTick
	}
	if c.openThrottle <= 0 {
		c.openThrottle = defaultConnlogOpenThrottle
	}
	if c.sendFn == nil {
		c.sendFn = sendConnlogBatch
	}
	if c.bootIDFn == nil {
		c.bootIDFn = defaultConnlogBootID
	}
}

type connlogReporter struct {
	cfg  connlogConfig
	ring *connRing

	// ringDropped 环缓冲一级溢出的丢弃数（原子，生产者侧累加），
	// 每个周期开始时并入 spool 状态的 Dropped 持久计数。
	ringDropped int64

	mu            sync.Mutex
	spool         *connlogSpool
	stash         []ConnEvent // 已 drain 但尚未成功落盘的事件（落盘失败时保留待下周期）
	started       bool
	cancel        context.CancelFunc
	done          chan struct{}
	bufferedSince time.Time // Buffered 由空变非空的时刻（open 节流基准）
	failCount     int
	retryAt       time.Time
}

func newConnlogReporter(cfg connlogConfig) *connlogReporter {
	cfg.applyDefaults()
	return &connlogReporter{
		cfg:  cfg,
		ring: newConnRing(defaultConnlogRingCapacity),
	}
}

// Enqueue 生产者入口（实例级）：校验后入环，满丢最旧并计数。
func (r *connlogReporter) Enqueue(ev ConnEvent) {
	if ev.ConnID == "" || (ev.Type != ConnEventOpen && ev.Type != ConnEventClose) {
		return
	}
	if r.ring.Push(ev) {
		atomic.AddInt64(&r.ringDropped, 1)
	}
}

// DroppedPending 返回尚未被面板确认收到过的丢弃计数（观测/测试）。
func (r *connlogReporter) DroppedPending() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := atomic.LoadInt64(&r.ringDropped)
	if r.spool != nil {
		// state.Dropped 是未确认丢弃的持久计数（其中包含飞行中
		// Pending 批次已快照的部分，确认后才扣减）。
		n += r.spool.state.Dropped
	}
	return n
}

// prepare 解析 spool 并执行 §4.6 换代结清（Start 的非循环部分，
// 测试可单独驱动）。结清纪律：上一进程代遗留的 open 连接永远不
// 会再有 close 事件（进程重启即全灭、机器重启同理），在此批量
// 结清为 interrupted 追加到 Buffered 头部之前——它们起始最早，
// 应先于本代新事件上报；end_time 只取台账里的最后已知时刻，绝
// 不以结清时刻冒充真实断开时刻。
func (r *connlogReporter) prepare() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spool != nil {
		return nil
	}
	sp := r.cfg.spool
	if sp == nil {
		sp = configuredConnlogSpool
	}
	if sp == nil {
		var err error
		sp, err = openConnlogSpool("")
		if err != nil {
			return err
		}
	}
	r.spool = sp

	boot := r.cfg.bootIDFn()
	st := sp.state
	if len(st.Open) == 0 {
		if st.BootID != boot {
			next := st
			next.BootID = boot
			if err := sp.save(next); err == nil {
				sp.state = next
			}
		}
		return nil
	}
	leftover := make([]ConnEvent, 0, len(st.Open))
	for connID, openEv := range st.Open {
		lastKnown := st.OpenLastKnown[connID]
		if lastKnown <= 0 {
			lastKnown = openEv.StartMs
		}
		leftover = append(leftover, ConnEvent{
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
	}
	sort.Slice(leftover, func(i, j int) bool { return leftover[i].StartMs < leftover[j].StartMs })
	next := st
	next.Open = make(map[string]ConnEvent)
	next.OpenLastKnown = make(map[string]int64)
	// 结清事件插到 Buffered 最前：其 open 事件必已在更早批次送
	// 达或同在 Buffered 中（顺序由 open 先于 close 的入队序保
	// 证——open 在台账里即证明它先被 drain 过）。
	next.Buffered = append(leftover, next.Buffered...)
	next.enforceCap(r.cfg.spoolMax)
	next.BootID = boot
	if err := sp.save(next); err != nil {
		return fmt.Errorf("连接日志换代结清落盘失败: %w", err)
	}
	sp.state = next
	fmt.Printf("🧹 连接日志：上一进程代遗留 %d 条未闭合连接已结清为 interrupted（boot %s → %s）\n",
		len(leftover), st.BootID, boot)
	return nil
}

// Start 完成 prepare 并启动上报循环。重复调用无副作用；prepare
// 失败只禁用本功能（纯旁路，绝不反压 agent 启动）。
func (r *connlogReporter) Start(ctx context.Context) {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	if err := r.prepare(); err != nil {
		fmt.Printf("❌ 连接日志上报器启动失败，本功能禁用: %v\n", err)
		return
	}

	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})
	r.started = true
	sp := r.spool
	r.mu.Unlock()

	go r.loop(runCtx)
	fmt.Printf("🚀 连接日志上报器已启动（stream=%s, spool 待报 %d 条）\n",
		sp.state.Stream, len(sp.state.Buffered))
}

// Stop 停止上报循环：停机前把环缓冲尾量落盘进 spool（不在停机
// 瞬间强行发送——面板同时不可达时发送必丢，落盘续报才是真不
// 丢，与流量 Stop 同口径）。
func (r *connlogReporter) Stop() {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return
	}
	cancel := r.cancel
	done := r.done
	r.started = false
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.persistLocked()
}

func (r *connlogReporter) loop(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(r.cfg.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.cycle(ctx)
		}
	}
}

// cycle 一个上报周期：落盘 → 组批 → 发送 → 确认/自愈。
func (r *connlogReporter) cycle(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spool == nil {
		return
	}
	if !r.persistLocked() {
		return // 落盘失败：事件留在 stash/ring，下周期重试。
	}
	pending := r.spool.state.Pending
	if pending == nil {
		return
	}
	if time.Now().Before(r.retryAt) {
		return
	}
	batch := *pending
	panelCursor, err := r.cfg.sendFn(ctx, batch)
	if err != nil {
		var rejection connlogReportRejection
		if errors.As(err, &rejection) {
			if recErr := r.spool.recoverRejected(rejection); recErr != nil {
				fmt.Printf("❌ 连接日志批次自愈落盘失败: %v\n", recErr)
			} else {
				fmt.Printf("⚠️ 连接日志批次被面板拒收（%s），已自愈换流/二分/隔离，待下周期重报\n", rejection)
			}
			r.failCount = 0
			r.retryAt = time.Time{}
			return
		}
		r.failCount++
		backoff := 2 * time.Second << min(r.failCount-1, 4) // 2s..32s
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
		r.retryAt = time.Now().Add(backoff)
		fmt.Printf("❌ 连接日志上报失败: %v (批次 %d 条, stream=%s seq=%d)，%v 后整批原样重报\n",
			err, len(batch.Events), batch.Stream, batch.Sequence, backoff)
		return
	}
	rotated, err := r.spool.confirmAck(panelCursor)
	if err != nil {
		fmt.Printf("❌ 连接日志确认状态落盘失败: %v\n", err)
		return
	}
	if rotated {
		fmt.Printf("⚠️ 面板连接日志游标(%d)超前于本机序号(%d)：spool 状态疑似被外部回退，已切换新 stream %s 重新入账；本批已确认事件不重发\n",
			panelCursor, batch.Sequence, r.spool.state.Stream)
	}
	r.failCount = 0
	r.retryAt = time.Time{}
}

// persistLocked 把 stash＋环缓冲新事件落盘进 spool 并按触发条件
// 组批（先落盘后发送）。落盘失败返回 false 且不丢任何事件（新
// 事件留在 stash、未 drain 的留在 ring）。调用方须持有 r.mu 且
// r.spool 已就绪。
func (r *connlogReporter) persistLocked() bool {
	sp := r.spool
	next := sp.state

	// 环缓冲丢弃数先只读入副本，落盘成功后才从原子计数扣减——
	// 落盘失败时计数原样保留，不会凭空消失。
	ringDropped := atomic.LoadInt64(&r.ringDropped)
	next.Dropped += ringDropped
	r.stash = append(r.stash, r.ring.Drain()...)
	changed := false
	if len(r.stash) > 0 {
		changed = true
		for _, ev := range r.stash {
			next.Buffered = append(next.Buffered, ev)
			switch ev.Type {
			case ConnEventOpen:
				next.Open[ev.ConnID] = ev
				if ev.StartMs > next.OpenLastKnown[ev.ConnID] {
					next.OpenLastKnown[ev.ConnID] = ev.StartMs
				}
			case ConnEventClose:
				delete(next.Open, ev.ConnID)
				delete(next.OpenLastKnown, ev.ConnID)
			}
		}
		if dropped := next.enforceCap(r.cfg.spoolMax); dropped > 0 {
			fmt.Printf("⚠️ 连接日志 spool 超上限 %d 字节，丢弃最旧 %d 条（计数随批上行）\n", r.cfg.spoolMax, dropped)
		}
	}

	if len(next.Buffered) > 0 && r.bufferedSince.IsZero() {
		r.bufferedSince = time.Now()
	}
	if len(next.Buffered) == 0 && len(next.Retry) == 0 {
		r.bufferedSince = time.Time{}
	}

	if next.Pending == nil {
		if batch := r.formBatchLocked(&next); batch != nil {
			next.Pending = batch
			changed = true
		}
	}

	if !changed && next.Dropped == sp.state.Dropped {
		return true // 无变化，无需落盘。
	}
	if err := sp.save(next); err != nil {
		// stash 原样保留（事件还在 r.stash 中，未进 state 副本的
		// 正式状态），下周期以同一批事件重试落盘。
		fmt.Printf("❌ 连接日志 spool 落盘失败: %v\n", err)
		return false
	}
	sp.state = next
	r.stash = nil
	if ringDropped > 0 {
		atomic.AddInt64(&r.ringDropped, -ringDropped)
	}
	return true
}

// formBatchLocked 按 §5.3 节奏决定是否组批：Retry 二分队列优先；
// Buffered 组批须满足其一——含 close 事件（即时）、攒满单批上
// 限、或纯 open 已等满节流窗口。组批即从来源队列摘除本批事件
// （在传入的 state 副本上生效）。返回 nil 表示本周期不组批。
func (r *connlogReporter) formBatchLocked(st *connlogSpoolState) *connlogEnvelope {
	var events []ConnEvent
	fromRetry := false
	if len(st.Retry) > 0 {
		events = st.Retry[0]
		fromRetry = true
	} else if len(st.Buffered) > 0 {
		hasClose := false
		for _, ev := range st.Buffered {
			if ev.Type == ConnEventClose {
				hasClose = true
				break
			}
		}
		if !hasClose && len(st.Buffered) < r.cfg.batchMax &&
			(r.bufferedSince.IsZero() || time.Since(r.bufferedSince) < r.cfg.openThrottle) {
			return nil
		}
		n := min(r.cfg.batchMax, len(st.Buffered))
		events = st.Buffered[:n]
	} else {
		return nil
	}

	batch := &connlogEnvelope{
		Stream:   st.Stream,
		Sequence: st.Next,
		Digest:   connlogBatchDigest(events),
		BootID:   r.cfg.bootIDFn(),
		Dropped:  st.Dropped,
		Events:   append([]ConnEvent(nil), events...),
	}
	// 从来源队列摘除本批事件（在调用方的 state 副本上生效）。
	if fromRetry {
		st.Retry = st.Retry[1:]
	} else {
		st.Buffered = append([]ConnEvent(nil), st.Buffered[len(events):]...)
	}
	return batch
}

// ---- 包级默认实例与装配点（main.go / U1 / U4 接入面） ----

var (
	connlogMu           sync.Mutex
	connlogDefault      *connlogReporter
	connlogBootIDFn     func() string
	connlogProcessStart = time.Now()
)

// defaultConnlogBootID 机器本次开机标识：Linux 读
// /proc/sys/kernel/random/boot_id（与 socket.machineBootID 同口
// 径，service 包不能反向 import socket 故在此实现），读不到回
// 退进程启动时刻——进程代变化本身已由换代结清覆盖，boot_id 只
// 作面板侧结清的附加触发面。
func defaultConnlogBootID() string {
	if data, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			return s
		}
	}
	return "proc-" + strconv.FormatInt(connlogProcessStart.Unix(), 10)
}

// SetConnlogBootIDProvider 覆盖 boot_id 来源（测试/集成用；须在
// StartConnlogReporter 之前调用）。
func SetConnlogBootIDProvider(fn func() string) {
	connlogMu.Lock()
	defer connlogMu.Unlock()
	connlogBootIDFn = fn
}

func getDefaultConnlogReporter() *connlogReporter {
	connlogMu.Lock()
	defer connlogMu.Unlock()
	if connlogDefault != nil {
		return connlogDefault
	}
	cfg := connlogConfig{}
	if connlogBootIDFn != nil {
		cfg.bootIDFn = connlogBootIDFn
	}
	connlogDefault = newConnlogReporter(cfg)
	return connlogDefault
}

// EnqueueConnEvent 连接事件生产者统一入口（U1 gost 服务层钩子
// 与 U4 内核日志解析共用）。上报器未启动时事件先入环缓冲（有
// 界、满丢最旧并计数），启动后自动转入 spool。永不阻塞调用方。
func EnqueueConnEvent(ev ConnEvent) {
	getDefaultConnlogReporter().Enqueue(ev)
}

// StartConnlogReporter 启动默认上报器（主程序在 Configure 选定
// spool 后调用）。重复调用无副作用。
func StartConnlogReporter(ctx context.Context) {
	getDefaultConnlogReporter().Start(ctx)
}

// StopConnlogReporter 停止默认上报器并落盘尾量。
func StopConnlogReporter() {
	connlogMu.Lock()
	r := connlogDefault
	connlogMu.Unlock()
	if r != nil {
		r.Stop()
	}
}
