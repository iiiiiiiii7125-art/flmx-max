package repo

import (
	"testing"

	"go-backend/internal/store/model"
)

// R18-C5-02 回归（剪枝侧纵深）：时间剪枝、行数帽与僵尸清道夫此前
// 全按 agent 声称的 start_time 执行——时钟快 30 天的行永不满足
// 剪枝条件、且在行数帽里被当成“最新”保留，合法行反被挤出。修复
// 后三者一律改按面板摄入时刻 created_time 执行，start_time 只用
// 于展示与时长计算。

func TestConnlogPruneUsesPanelCreatedTimeNotAgentStartTime(t *testing.T) {
	r := newConnlogTestRepo(t)
	base := int64(1_700_000_000_000)
	day := int64(24 * 3600 * 1000)
	seed := []model.InboundConnLog{
		// 时钟快 30 天的行：面板 30 天前就已摄入，早该被时间剪枝删掉。
		{EntryNodeID: 1, ConnID: "future-start", DisplayInboundID: 10, ClientIP: "203.0.113.1", StartTime: base + 30*day, CreatedTime: base, Status: ConnlogStatusClosed},
		// 时钟慢 30 天的行：面板 2 天前才摄入，保留期内不得被提前删掉。
		{EntryNodeID: 1, ConnID: "slow-clock", DisplayInboundID: 10, ClientIP: "203.0.113.1", StartTime: base - 30*day, CreatedTime: base + 2*day, Status: ConnlogStatusClosed},
		{EntryNodeID: 1, ConnID: "normal-old", DisplayInboundID: 10, ClientIP: "203.0.113.1", StartTime: base, CreatedTime: base, Status: ConnlogStatusClosed},
	}
	for i := range seed {
		if err := r.db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ConnID, err)
		}
	}
	deleted, err := r.PruneInboundConnLogsBefore(base + day)
	if err != nil || deleted != 2 {
		t.Fatalf("time prune = (%d, %v), want (2, nil): future-start + normal-old by created_time", deleted, err)
	}
	rows := connlogRows(t, r)
	if len(rows) != 1 || rows[0].ConnID != "slow-clock" {
		t.Fatalf("remaining = %v, want only [slow-clock]", rows)
	}
}

func TestConnlogCapKeepsNewestByCreatedTime(t *testing.T) {
	r := newConnlogTestRepo(t)
	base := int64(1_700_000_000_000)
	seed := []model.InboundConnLog{
		// start_time 最“新”（未来）但面板摄入最旧：帽剪枝时最先被删。
		{EntryNodeID: 1, ConnID: "a-future", DisplayInboundID: 10, ClientIP: "203.0.113.1", StartTime: base + 30*24*3600*1000, CreatedTime: base, Status: ConnlogStatusClosed},
		{EntryNodeID: 1, ConnID: "b", DisplayInboundID: 10, ClientIP: "203.0.113.1", StartTime: base + 1000, CreatedTime: base + 2000, Status: ConnlogStatusClosed},
		{EntryNodeID: 1, ConnID: "c", DisplayInboundID: 10, ClientIP: "203.0.113.1", StartTime: base + 2000, CreatedTime: base + 3000, Status: ConnlogStatusClosed},
	}
	for i := range seed {
		if err := r.db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ConnID, err)
		}
	}
	deleted, err := r.PruneInboundConnLogsOverCap(2)
	if err != nil || deleted != 1 {
		t.Fatalf("cap prune = (%d, %v), want (1, nil)", deleted, err)
	}
	rows := connlogRows(t, r)
	if len(rows) != 2 || rows[0].ConnID != "b" || rows[1].ConnID != "c" {
		t.Fatalf("remaining = %v, want [b c] (newest by created_time, future-start evicted)", rows)
	}
}

func TestConnlogStaleSweepUsesCreatedTime(t *testing.T) {
	r := newConnlogTestRepo(t)
	now := int64(1_700_100_000_000)
	day := int64(24 * 3600 * 1000)
	seed := []model.InboundConnLog{
		// 未来 start 的 open 行：面板 2 天前摄入，早该被清道夫结清。
		{EntryNodeID: 1, ConnID: "future-open", StartTime: now + 30*day, CreatedTime: now - 2*day, Status: ConnlogStatusOpen},
		// 慢时钟 open 行：面板 1 小时前摄入，未超 24h，不得结清。
		{EntryNodeID: 1, ConnID: "slow-open", StartTime: now - 2*day, CreatedTime: now - 3600_000, Status: ConnlogStatusOpen},
	}
	for i := range seed {
		if err := r.db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ConnID, err)
		}
	}
	n, err := r.InterruptStaleOpenInboundConnLogs(now - day)
	if err != nil || n != 1 {
		t.Fatalf("stale sweep = (%d, %v), want (1, nil): only future-open by created_time", n, err)
	}
	byConn := map[string]model.InboundConnLog{}
	for _, row := range connlogRows(t, r) {
		byConn[row.ConnID] = row
	}
	if row := byConn["future-open"]; row.Status != ConnlogStatusInterrupted {
		t.Fatalf("future-open = %+v, want interrupted", row)
	}
	if row := byConn["slow-open"]; row.Status != ConnlogStatusOpen {
		t.Fatalf("slow-open = %+v, want still open", row)
	}
}
