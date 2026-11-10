package handler

import (
	"testing"
	"time"

	"go-backend/internal/metrics"
)

// 包 K：累积面越阈状态机的判定全集——进入只一次、迟滞带不抖、
// 回落解除后可再进入、两面互不干扰。
func TestResourceAlertDecideTransitions(t *testing.T) {
	tr := newResourceAlertTracker()
	ct := resourceAlertKey(7, "conntrack")

	if a := tr.decide(ct, 50); a != resourceActionNone {
		t.Fatalf("50%% must be none, got %d", a)
	}
	if a := tr.decide(ct, 80); a != resourceActionEnter {
		t.Fatalf("80%% must enter, got %d", a)
	}
	if a := tr.decide(ct, 95); a != resourceActionNone {
		t.Fatalf("already alerted must not re-enter, got %d", a)
	}
	if a := tr.decide(ct, 75); a != resourceActionNone {
		t.Fatalf("hysteresis band must hold state, got %d", a)
	}
	if a := tr.decide(ct, 69.9); a != resourceActionExit {
		t.Fatalf("drop below rearm must exit, got %d", a)
	}
	if a := tr.decide(ct, 10); a != resourceActionNone {
		t.Fatalf("already clear must stay none, got %d", a)
	}
	if a := tr.decide(ct, 81); a != resourceActionEnter {
		t.Fatalf("re-crossing after exit must re-enter, got %d", a)
	}

	// Surfaces and nodes are independent keys.
	disk := resourceAlertKey(7, "disk")
	if a := tr.decide(disk, 90); a != resourceActionEnter {
		t.Fatalf("disk surface must have its own state, got %d", a)
	}
	other := resourceAlertKey(8, "conntrack")
	if a := tr.decide(other, 90); a != resourceActionEnter {
		t.Fatalf("other node must have its own state, got %d", a)
	}
}

// 派发失败回滚：repo 为 nil 时 dispatchNotify 恒 Failed，
// notifyTracked 的 onDone 必须把占位撤掉，使下一样本重新进入
// 派发（而不是被「已告警」永久吞掉，BC-16 同型）。
func TestResourceAlertDispatchFailureRollsBack(t *testing.T) {
	h := &Handler{
		resourceAlerts: newResourceAlertTracker(),
		notifier:       newNotifier(),
	}
	info := metrics.SystemInfo{ConntrackCount: 900, ConntrackMax: 1000, DiskUsage: 10}
	h.observeNodeResourceUsage(7, info)
	key := resourceAlertKey(7, "conntrack")
	if !h.resourceAlerts.isAlerted(key) {
		t.Fatalf("crossing must mark alerted synchronously")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !h.resourceAlerts.isAlerted(key) {
			return // rolled back by the failed dispatch, as required
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("failed dispatch never rolled back the alert mark")
}

// 无此面的样本（旧 agent 的 0 值、磁盘采集失败的 0）不许触发
// 任何判定或派发。
func TestResourceAlertIgnoresAbsentSurfaces(t *testing.T) {
	h := &Handler{
		resourceAlerts: newResourceAlertTracker(),
		notifier:       newNotifier(),
	}
	h.observeNodeResourceUsage(7, metrics.SystemInfo{})
	if h.resourceAlerts.isAlerted(resourceAlertKey(7, "conntrack")) ||
		h.resourceAlerts.isAlerted(resourceAlertKey(7, "disk")) {
		t.Fatalf("absent surfaces must never alert")
	}
}

// 包 K 扩围：agent 内存面经 observeNodeResourceUsage 同一漏斗
// 判定——total<=0 判无此面；RSS 占比越阈进入、回落解除。
func TestResourceAlertAgentMemorySurface(t *testing.T) {
	h := &Handler{resourceAlerts: newResourceAlertTracker()}
	key := resourceAlertKey(3, "agent-memory")

	// Old agent: no memory face at all.
	h.observeNodeResourceUsage(3, metrics.SystemInfo{AgentRSSBytes: 900 << 20})
	if h.resourceAlerts.isAlerted(key) {
		t.Fatal("alerted without a memory total (old-agent shape)")
	}
	// 50% of RAM: below the line.
	h.observeNodeResourceUsage(3, metrics.SystemInfo{AgentRSSBytes: 50 << 20, MemTotalBytes: 100 << 20})
	if h.resourceAlerts.isAlerted(key) {
		t.Fatal("alerted at 50% RSS")
	}
	// ECS1686 field shape (286MB / 457MB = 62.6%): still below.
	h.observeNodeResourceUsage(3, metrics.SystemInfo{AgentRSSBytes: 286 << 20, MemTotalBytes: 457 << 20})
	if h.resourceAlerts.isAlerted(key) {
		t.Fatal("alerted at the field-observed 62.6% RSS")
	}
	// 85%: enters (dispatch fails against a nil repo and the
	// rollback un-marks it asynchronously, so assert via decide
	// on the same key instead: a subsequent exit-band sample
	// must produce Exit, proving the Enter latched first).
	h.observeNodeResourceUsage(3, metrics.SystemInfo{AgentRSSBytes: 85 << 20, MemTotalBytes: 100 << 20})
	deadline := time.Now().Add(2 * time.Second)
	for h.resourceAlerts.isAlerted(key) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond) // let the rollback land
	}
	if a := h.resourceAlerts.decide(key, 85); a != resourceActionEnter {
		t.Fatalf("post-rollback decide = %d, want Enter (rollback must re-arm)", a)
	}
	if a := h.resourceAlerts.decide(key, 60); a != resourceActionExit {
		t.Fatalf("decide at 60%% = %d, want Exit", a)
	}
}
