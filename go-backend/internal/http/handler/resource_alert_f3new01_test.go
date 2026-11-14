package handler

import (
	"testing"
	"time"

	"go-backend/internal/metrics"
)

// F3-NEW-01：三项资源告警（conntrack/disk/agent-memory high）有独立
// 开关、不走 notify_events 成员门。回归：UI 取消任一事件写回 8 键串
// 时，三项资源告警必须照常发出；关掉某项自己的开关只停那一项；
// 事件列表取 "none" 也不杀资源告警；总开关仍能停掉全部。

func f3new01SetConfig(t *testing.T, h *Handler, key, value string) {
	t.Helper()
	if err := h.repo.DB().Exec(`
		INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value, time = excluded.time`,
		key, value, time.Now().UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	// 直写 DB 绕过写入口的缓存失效钩子：测前清缓存，使本次断言
	// 读到直写值（与 r5b9 的 notify_events 现读口径等价）。
	h.invalidateNotifyConfigCache()
}

// eightKeys 是 UI 取消「每日汇总」后写回的形态：其余 8 个事件全开，
// 三项资源事件不在其中——修前三项在此形态下全被成员门静默杀死。
const f3new01EightKeys = "node_offline,node_upgrade_failed,inbound_down,kernel_error,node_expiry,latency_bad,route_change,user_quota"

func f3new01OverThreshold() metrics.SystemInfo {
	return metrics.SystemInfo{
		ConntrackCount: 900, ConntrackMax: 1000, // 90%
		DiskUsage:      85,                       // 85%
		AgentRSSBytes:  85 << 20, MemTotalBytes: 100 << 20, // 85%
	}
}

func TestF3New01ResourceAlertsSurviveEventUncheck(t *testing.T) {
	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	insertFixdNode(t, r, 7, "f3new01-node-7")
	f3new01SetConfig(t, h, "notify_events", f3new01EightKeys)

	h.observeNodeResourceUsage(7, f3new01OverThreshold())
	wh.waitCount(t, 3, 5*time.Second)

	got := wh.titles()
	want := map[string]bool{
		"节点连接跟踪表用量过高": true,
		"节点磁盘用量过高":     true,
		"节点内存用量过高":     true,
	}
	if len(got) != 3 {
		t.Fatalf("8-key events must still deliver all 3 resource alerts, got %d (%v)", len(got), got)
	}
	for _, title := range got {
		if !want[title] {
			t.Fatalf("unexpected alert title %q (got %v)", title, got)
		}
	}
}

func TestF3New01ResourceSwitchOffSuppressesOnlyItsSurface(t *testing.T) {
	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	insertFixdNode(t, r, 7, "f3new01-node-7")
	f3new01SetConfig(t, h, "notify_events", f3new01EightKeys)
	f3new01SetConfig(t, h, "notify_disk_high", "false")

	h.observeNodeResourceUsage(7, f3new01OverThreshold())
	wh.waitCount(t, 2, 5*time.Second)
	// 沉降：磁盘面必须始终不出现（去重窗 30 分钟内不会补发）。
	time.Sleep(500 * time.Millisecond)

	got := wh.titles()
	if len(got) != 2 {
		t.Fatalf("disk switch off must deliver exactly the other 2 alerts, got %d (%v)", len(got), got)
	}
	for _, title := range got {
		if title == "节点磁盘用量过高" {
			t.Fatalf("disk alert must be suppressed by its own switch, got %v", got)
		}
	}
}

func TestF3New01EventsNoneDoesNotKillResourceAlerts(t *testing.T) {
	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	insertFixdNode(t, r, 7, "f3new01-node-7")
	f3new01SetConfig(t, h, "notify_events", "none")

	h.observeNodeResourceUsage(7, metrics.SystemInfo{ConntrackCount: 900, ConntrackMax: 1000})
	wh.waitCount(t, 1, 5*time.Second)
	if got := wh.titles(); got[0] != "节点连接跟踪表用量过高" {
		t.Fatalf("events=none must not kill resource alerts, got %v", got)
	}
}

func TestF3New01MasterSwitchStillGatesResourceAlerts(t *testing.T) {
	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	insertFixdNode(t, r, 7, "f3new01-node-7")
	f3new01SetConfig(t, h, "notify_events", f3new01EightKeys)
	f3new01SetConfig(t, h, "notify_enabled", "false")

	h.observeNodeResourceUsage(7, f3new01OverThreshold())
	time.Sleep(500 * time.Millisecond)
	if got := wh.count(); got != 0 {
		t.Fatalf("master switch off must suppress resource alerts, got %d (%v)", got, wh.titles())
	}
}

func TestF3New01ResourceAlertConfigKey(t *testing.T) {
	cases := map[string]string{
		notifyEventConntrackHigh: "notify_conntrack_high",
		notifyEventDiskHigh:      "notify_disk_high",
		notifyEventAgentMemHigh:  "notify_agent_memory_high",
	}
	for event, want := range cases {
		key, ok := resourceAlertConfigKey(event)
		if !ok || key != want {
			t.Fatalf("resourceAlertConfigKey(%q) = (%q, %v), want (%q, true)", event, key, ok, want)
		}
	}
	if _, ok := resourceAlertConfigKey(notifyEventNodeOffline); ok {
		t.Fatalf("non-resource events must not map to a resource switch")
	}
}
