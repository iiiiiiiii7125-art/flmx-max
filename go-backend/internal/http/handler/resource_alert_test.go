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
