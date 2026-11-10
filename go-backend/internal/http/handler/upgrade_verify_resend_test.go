package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R3-MON-01：未落地告警的待发补发格。旧实现以 onDone=nil 派发，
// 双通道在扫描窗内同败时告警永久丢失；本组覆盖：发送失败进持
// 久台账→节流内不重派→（含面板重启换 Handler）后续检查补发→
// 成功销账；持续失败保标记且受节流；状态已不成立（landed/被接
// 管/节点删除）的待发记录静默销账、绝不发过时消息。

// upgradeAlertSink 是可翻转成败的 webhook 接收端：fail 时一律
// 500（通道故障），否则捕获消息。hits 计全部请求（含失败尝试与
// 通道级重试），供节流断言区分「没有再派发」与「派发了但失败」。
type upgradeAlertSink struct {
	fail atomic.Bool
	mu   sync.Mutex
	hits int
	msgs []notifyMessage
}

func (s *upgradeAlertSink) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		s.hits++
		if s.fail.Load() {
			s.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var m notifyMessage
		_ = json.NewDecoder(req.Body).Decode(&m)
		s.msgs = append(s.msgs, m)
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
}

func (s *upgradeAlertSink) hitsCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *upgradeAlertSink) delivered() []notifyMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]notifyMessage(nil), s.msgs...)
}

func setupUpgradeAlertResendHandler(t *testing.T) (*Handler, *repo.Repository, *upgradeAlertSink) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/uv-resend.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	sink := &upgradeAlertSink{}
	srv := httptest.NewServer(sink.handler())
	t.Cleanup(srv.Close)

	now := time.Now().UnixMilli()
	for k, v := range map[string]string{"notify_enabled": "true", "notify_webhook_url": srv.URL} {
		if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, k, v, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	return New(r, "unit-test-secret"), r, sink
}

func insertOverdueVerifyRecord(t *testing.T, r *repo.Repository, nodeID int64, dispatchedAt int64) {
	t.Helper()
	if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: nodeID, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.1.0",
		FromVersion: "2.0.0 (debian/amd64)", DispatchedAt: dispatchedAt, DeadlineAt: dispatchedAt,
		CreatedTime: dispatchedAt, UpdatedTime: dispatchedAt,
	}); err != nil {
		t.Fatalf("upsert record node %d: %v", nodeID, err)
	}
}

func waitForUpgradeAlert(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// settleFailedDispatch 等一次失败派发走完（首发＋通道重试各命中
// 一次接收端）并留出回调结算时间，返回此时的命中数基线。
func settleFailedDispatch(t *testing.T, sink *upgradeAlertSink, wantHits int) {
	t.Helper()
	waitForUpgradeAlert(t, "failed dispatch attempts", func() bool { return sink.hitsCount() >= wantHits })
	time.Sleep(300 * time.Millisecond)
}

func TestUpgradeFailedAlertResentAfterChannelRecovery(t *testing.T) {
	h, r, sink := setupUpgradeAlertResendHandler(t)
	sink.fail.Store(true)
	insertUpgradeVerifyNode(t, r, 7, "node-resend", "2.0.0 (debian/amd64)")
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	insertOverdueVerifyRecord(t, r, 7, past)

	// 首轮扫描：置 failed 并派发，通道故障下发送失败（首发＋通
	// 道级重试共两次命中），告警不得送达、必须进待发台账。
	h.sweepNodeUpgradeVerifications(time.Now())
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyFailed {
		t.Fatalf("overdue record must resolve failed: %+v", rec)
	}
	settleFailedDispatch(t, sink, 2)
	if got := sink.delivered(); len(got) != 0 {
		t.Fatalf("failed dispatch must deliver nothing, got %v", got)
	}
	pending := h.loadPendingUpgradeFailedAlerts()
	if len(pending) != 1 || pending[0].NodeID != 7 || pending[0].DispatchedAt != past {
		t.Fatalf("failed alert must be ledgered verbatim: %+v", pending)
	}
	if !strings.Contains(pending[0].Message, "node-resend") || !strings.Contains(pending[0].Message, "2.1.0") {
		t.Fatalf("ledgered message must be the failure-round text: %q", pending[0].Message)
	}

	// 节流（默认 10 分钟口径）：间隔内的后续检查不得重派。
	hits := sink.hitsCount()
	h.sweepNodeUpgradeVerifications(time.Now())
	h.retryPendingUpgradeFailedAlerts(time.Now())
	time.Sleep(250 * time.Millisecond)
	if sink.hitsCount() != hits {
		t.Fatalf("retry inside the throttle interval must not redispatch: hits %d -> %d", hits, sink.hitsCount())
	}

	// 面板重启（换新 Handler、同库）＋通道恢复＋越过节流间隔：
	// 补发只依赖持久台账，不依赖原进程内存。
	old := upgradeFailedAlertRetryInterval
	upgradeFailedAlertRetryInterval = 100 * time.Millisecond
	t.Cleanup(func() { upgradeFailedAlertRetryInterval = old })
	h2 := New(r, "unit-test-secret")
	sink.fail.Store(false)
	time.Sleep(200 * time.Millisecond)
	h2.sweepNodeUpgradeVerifications(time.Now())
	waitForUpgradeAlert(t, "resent alert delivery", func() bool { return len(sink.delivered()) == 1 })
	msg := sink.delivered()[0]
	if msg.Event != notifyEventNodeUpgradeFailed || msg.Title != "节点升级未落地" {
		t.Fatalf("resent alert event/title mismatch: %+v", msg)
	}
	if msg.Message != pending[0].Message {
		t.Fatalf("resend must be verbatim:\n got %q\nwant %q", msg.Message, pending[0].Message)
	}
	// 成功销账；failed 状态是事实、不因告警送达而回滚。
	waitForUpgradeAlert(t, "pending ledger cleared", func() bool { return len(h2.loadPendingUpgradeFailedAlerts()) == 0 })
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyFailed {
		t.Fatalf("verification state must stay failed after alert delivery: %+v", rec)
	}
	h2.sweepNodeUpgradeVerifications(time.Now())
	time.Sleep(250 * time.Millisecond)
	if got := sink.delivered(); len(got) != 1 {
		t.Fatalf("delivered alert must not repeat, got %d", len(got))
	}
}

func TestUpgradeFailedAlertPersistentFailureThrottled(t *testing.T) {
	old := upgradeFailedAlertRetryInterval
	upgradeFailedAlertRetryInterval = 100 * time.Millisecond
	t.Cleanup(func() { upgradeFailedAlertRetryInterval = old })

	h, r, sink := setupUpgradeAlertResendHandler(t)
	sink.fail.Store(true)
	insertUpgradeVerifyNode(t, r, 7, "node-stilldown", "2.0.0 (debian/amd64)")
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	insertOverdueVerifyRecord(t, r, 7, past)

	h.sweepNodeUpgradeVerifications(time.Now())
	settleFailedDispatch(t, sink, 2)
	if len(h.loadPendingUpgradeFailedAlerts()) != 1 {
		t.Fatalf("pending entry must exist after first failure")
	}

	// 越过节流间隔再检查：补发被尝试（再命中两次）但仍失败——
	// 标记保留、不送达、不销账。
	time.Sleep(200 * time.Millisecond)
	h.sweepNodeUpgradeVerifications(time.Now())
	settleFailedDispatch(t, sink, 4)
	if got := sink.delivered(); len(got) != 0 {
		t.Fatalf("persistent failure must deliver nothing, got %v", got)
	}
	if len(h.loadPendingUpgradeFailedAlerts()) != 1 {
		t.Fatalf("pending entry must survive persistent failure")
	}

	// 节流门：恢复 10 分钟口径后，紧接着的检查不得再派发（不
	// 轰炸通道）。用大间隔断言是为与负载无关地钉住门本身——小
	// 间隔下「结算耗时已越过间隔」会让补发合法触发、测的就不是
	// 门而是时序。
	upgradeFailedAlertRetryInterval = 10 * time.Minute
	hits := sink.hitsCount()
	h.retryPendingUpgradeFailedAlerts(time.Now())
	time.Sleep(250 * time.Millisecond)
	if sink.hitsCount() != hits {
		t.Fatalf("retry inside the throttle interval must not redispatch: hits %d -> %d", hits, sink.hitsCount())
	}
}

func TestUpgradeFailedAlertRetryDropsStaleStates(t *testing.T) {
	old := upgradeFailedAlertRetryInterval
	upgradeFailedAlertRetryInterval = 100 * time.Millisecond
	t.Cleanup(func() { upgradeFailedAlertRetryInterval = old })

	h, r, sink := setupUpgradeAlertResendHandler(t)
	sink.fail.Store(true)
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	// 7=失败后迟到落地；8=失败后被无目标回退接管；9=失败后节点删除。
	// 三节点必须异名：告警指纹按正文计算，同名同参数会让三条派
	// 发互相去重抑制（那是平台既有语义，不是本格要测的面）。
	for _, tc := range []struct {
		id   int64
		name string
	}{{7, "node-stale-landed"}, {8, "node-stale-superseded"}, {9, "node-stale-deleted"}} {
		insertUpgradeVerifyNode(t, r, tc.id, tc.name, "2.0.0 (debian/amd64)")
		insertOverdueVerifyRecord(t, r, tc.id, past)
	}
	h.sweepNodeUpgradeVerifications(time.Now())
	settleFailedDispatch(t, sink, 6)
	if len(h.loadPendingUpgradeFailedAlerts()) != 3 {
		t.Fatalf("all three failed alerts must be ledgered")
	}

	setNodeReportedVersion(t, r, 7, "2.1.0 (debian/amd64)")
	h.verifyNodeUpgradeLanding(7)
	if rec := getVerifyRecord(t, r, 7); rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("precondition landed: %+v", rec)
	}
	if _, err := r.SupersedeNodeUpgradeVerification(8, "test takeover", time.Now().UnixMilli()); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if err := r.DB().Exec(`DELETE FROM node WHERE id = 9`).Error; err != nil {
		t.Fatalf("delete node 9: %v", err)
	}

	// 补发轮：三条所述状态均已不成立——静默销账、零重派、零送达。
	hits := sink.hitsCount()
	time.Sleep(200 * time.Millisecond)
	h.retryPendingUpgradeFailedAlerts(time.Now())
	time.Sleep(250 * time.Millisecond)
	if sink.hitsCount() != hits {
		t.Fatalf("stale entries must not be resent: hits %d -> %d", hits, sink.hitsCount())
	}
	if got := sink.delivered(); len(got) != 0 {
		t.Fatalf("stale alerts must never be delivered, got %v", got)
	}
	if len(h.loadPendingUpgradeFailedAlerts()) != 0 {
		t.Fatalf("stale entries must be dropped from the ledger")
	}
}
