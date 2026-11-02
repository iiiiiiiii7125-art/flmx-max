package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R2-038 对账清扫回归：游标孤儿行按保留期回收、封禁集无主端口
// 按当前转发全集对账移除；清扫小时级门控、幂等、单点失败不拖累
// 维护轮（fail-soft 在卫生面是对的，见 state_sweep.go）。

func insertCursorRow(t *testing.T, h *Handler, scope string, updatedMs int64) {
	t.Helper()
	row := repo.TrafficStreamCursor{Scope: scope, Sequence: 5, Digest: "d", UpdatedTime: updatedMs}
	if err := h.repo.DB().Create(&row).Error; err != nil {
		t.Fatalf("insert cursor %s: %v", scope, err)
	}
}

func cursorExists(t *testing.T, h *Handler, scope string) bool {
	t.Helper()
	var count int64
	if err := h.repo.DB().Model(&repo.TrafficStreamCursor{}).Where("scope = ?", scope).Count(&count).Error; err != nil {
		t.Fatalf("count cursor %s: %v", scope, err)
	}
	return count > 0
}

func TestSweepStaleTrafficStreamCursors(t *testing.T) {
	h := &Handler{repo: openMaintenanceLoopRepo(t, "cursor-sweep.db")}
	now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-trafficStreamCursorRetention - time.Hour).UnixMilli()
	fresh := now.Add(-time.Hour).UnixMilli()
	insertCursorRow(t, h, "flow:1:old-stream", stale)
	insertCursorRow(t, h, "flow:1:live-stream", fresh)
	insertCursorRow(t, h, "flow:2:old-stream", stale)

	h.sweepStaleTrafficStreamCursors(now)
	if cursorExists(t, h, "flow:1:old-stream") || cursorExists(t, h, "flow:2:old-stream") {
		t.Fatal("stale cursor rows must be swept")
	}
	if !cursorExists(t, h, "flow:1:live-stream") {
		t.Fatal("active cursor row must be kept")
	}

	// 幂等：再跑一轮无新增删除、无错误。
	h.sweepStaleTrafficStreamCursors(now)
	if !cursorExists(t, h, "flow:1:live-stream") {
		t.Fatal("active cursor row must survive repeated sweeps")
	}
}

func TestSweepEventStatesHourlyGateAndFailureIsolation(t *testing.T) {
	h := &Handler{repo: openMaintenanceLoopRepo(t, "sweep-gate.db")}
	now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-trafficStreamCursorRetention - time.Hour).UnixMilli()

	insertCursorRow(t, h, "flow:1:first", stale)
	h.sweepEventStates(now)
	if cursorExists(t, h, "flow:1:first") {
		t.Fatal("first sweep run must execute")
	}

	// 门内再投：不得重复执行。
	insertCursorRow(t, h, "flow:1:second", stale)
	h.sweepEventStates(now.Add(time.Minute))
	if !cursorExists(t, h, "flow:1:second") {
		t.Fatal("sweep inside the hourly gate must be skipped")
	}

	// 过门后恢复执行。
	h.sweepEventStates(now.Add(2 * time.Hour))
	if cursorExists(t, h, "flow:1:second") {
		t.Fatal("sweep after the gate interval must execute again")
	}

	// 失败隔离：游标表不可用时清扫只记日志、不 panic、不上抛，
	// 维护轮其余环节不受影响（此处以 sweepEventStates 直接返回
	// 为证；nftablesManager 为 nil 时封禁清扫同样静默跳过）。
	db := h.repo.DB()
	if err := db.Exec("ALTER TABLE traffic_stream_cursor RENAME TO traffic_stream_cursor__hidden").Error; err != nil {
		t.Fatal(err)
	}
	h.sweepEventStates(now.Add(4 * time.Hour))
	if err := db.Exec("ALTER TABLE traffic_stream_cursor__hidden RENAME TO traffic_stream_cursor").Error; err != nil {
		t.Fatal(err)
	}
}

func TestOrphanBlockedPorts(t *testing.T) {
	cases := []struct {
		name    string
		blocked []int
		owned   []int
		want    []int
	}{
		{"orphan only", []int{100, 200}, []int{100}, []int{200}},
		{"all owned", []int{100}, []int{100, 200}, nil},
		{"none blocked", nil, []int{100}, nil},
		{"none owned", []int{300, 100}, nil, []int{100, 300}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := orphanBlockedPorts(tc.blocked, tc.owned)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestSweepNodeOrphanedBlockedPorts(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	fake := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(fake)
	tunnelID := seedTunnelForNftables(t, h, "sweep-tunnel", fixture.nodeID)
	seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.20:80", 23101)
	paused := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.21:80", 23102)
	if err := h.repo.DB().Model(&model.Forward{}).Where("id = ?", paused.ID).
		Update("status", model.ForwardStatusPaused).Error; err != nil {
		t.Fatalf("pause forward: %v", err)
	}
	cfgModel := &model.NodeSSHConfig{Host: "203.0.113.10", Port: 22, Username: "root", AuthType: "password"}

	// 节点封禁集：在用转发口（23101）、暂停中转发口（23102）、
	// 已删转发的残留口（23199）。只有残留口可清。
	fake.seedBlockedSet(23101, 23102, 23199)
	if err := h.sweepNodeOrphanedBlockedPorts(context.Background(), fixture.nodeID, cfgModel); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := fake.blockedSetPorts(); len(got) != 2 || got[0] != 23101 || got[1] != 23102 {
		t.Fatalf("owned and paused ports must stay blocked, set is now %v", got)
	}
	unblocks := 0
	for _, call := range fake.blockedCallList() {
		if call.blocked {
			t.Fatalf("sweep must never block, got %+v", call)
		}
		unblocks++
		if call.port != 23199 {
			t.Fatalf("sweep unblocked a non-orphan port: %+v", call)
		}
	}
	if unblocks != 1 {
		t.Fatalf("expected exactly one orphan unblock, got %d", unblocks)
	}

	// 幂等：第二轮无残留、无新的解除调用。
	if err := h.sweepNodeOrphanedBlockedPorts(context.Background(), fixture.nodeID, cfgModel); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := len(fake.blockedCallList()); got != 1 {
		t.Fatalf("second sweep must not unblock anything, calls=%d", got)
	}

	// 读失败：整节点跳过、错误上抛给外层记日志，封禁集不动。
	fake.listBlockedErr = errors.New("ssh down")
	fake.seedBlockedSet(23101, 23199)
	if err := h.sweepNodeOrphanedBlockedPorts(context.Background(), fixture.nodeID, cfgModel); err == nil {
		t.Fatal("list failure must surface as an error for the caller to log")
	}
	if got := fake.blockedSetPorts(); len(got) != 2 {
		t.Fatalf("failed sweep must not touch the set, got %v", got)
	}
}

func TestSweepOrphanedBlockedPortsSkipsWithoutManager(t *testing.T) {
	h := &Handler{repo: openMaintenanceLoopRepo(t, "sweep-nomanager.db")}
	// nftablesManager 为 nil：静默跳过（非 nftables 部署形态）。
	h.sweepOrphanedBlockedPorts()
}
