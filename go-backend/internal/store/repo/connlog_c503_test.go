package repo

import (
	"testing"

	"go-backend/internal/store/model"
)

// R18-C5-03 回归：HasRecentConnlogRows 是 readiness“已关仍有行
// 到达”探测的读面——按面板 created_time（而非 agent start_time）
// 判定最近 1 小时内是否有新行入库。
func TestHasRecentConnlogRows(t *testing.T) {
	r := newConnlogTestRepo(t)
	now := int64(1_700_000_000_000)
	hour := int64(3600_000)
	seed := []model.InboundConnLog{
		{EntryNodeID: 1, ConnID: "recent", DisplayInboundID: 10, ClientIP: "203.0.113.1", StartTime: now - 2*hour, CreatedTime: now - 1000, Status: ConnlogStatusClosed},
		{EntryNodeID: 1, ConnID: "old", DisplayInboundID: 10, ClientIP: "203.0.113.1", StartTime: now - 1000, CreatedTime: now - 2*hour, Status: ConnlogStatusClosed},
		{EntryNodeID: 2, ConnID: "other-node", DisplayInboundID: 10, ClientIP: "203.0.113.1", StartTime: now, CreatedTime: now - 1000, Status: ConnlogStatusClosed},
	}
	for i := range seed {
		if err := r.db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ConnID, err)
		}
	}
	// 节点 1 最近 1 小时内有行（recent 的 created_time 在窗内；
	// old 的 start_time 在窗内但 created_time 不在窗内，不算）。
	ok, err := r.HasRecentConnlogRows(1, now-hour)
	if err != nil || !ok {
		t.Fatalf("node 1 recent = (%v, %v), want (true, nil)", ok, err)
	}
	// 窗口收紧到最近 500ms 内：无行。
	ok, err = r.HasRecentConnlogRows(1, now-500)
	if err != nil || ok {
		t.Fatalf("node 1 tight window = (%v, %v), want (false, nil)", ok, err)
	}
	// 他节点不计入本节点。
	ok, err = r.HasRecentConnlogRows(2, now-hour)
	if err != nil || !ok {
		t.Fatalf("node 2 recent = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = r.HasRecentConnlogRows(3, now-hour)
	if err != nil || ok {
		t.Fatalf("missing node recent = (%v, %v), want (false, nil)", ok, err)
	}
}
