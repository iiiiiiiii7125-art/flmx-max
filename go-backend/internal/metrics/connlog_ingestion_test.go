package metrics

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

func connlogTestDigest(text string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
}

func seedOpenConnlogRow(t *testing.T, r *repo.Repository, nodeID int64, connID string, startMs int64) {
	t.Helper()
	scope := fmt.Sprintf("connlog:%d:stream-%s", nodeID, connID)
	_, err := r.ApplyConnlogStreamBatch(scope, 1, connlogTestDigest("open-"+connID), time.UnixMilli(startMs), []repo.ConnlogEvent{
		{Type: "open", ConnID: connID, EntryNodeID: nodeID, EntryKind: repo.ConnlogEntryForward, EntryID: "77_2_3_tcp",
			ClientIP: "203.0.113.7", Network: "tcp", SessionKind: "tcp_conn", StartTime: startMs},
	})
	if err != nil {
		t.Fatalf("seed connlog row %s: %v", connID, err)
	}
}

func TestConnlogBootChangeInterruptsOpenRows(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	seedOpenConnlogRow(t, r, 1, "boot-victim", now-60_000)
	seedOpenConnlogRow(t, r, 2, "other-node", now-60_000)

	svc := NewIngestionService(r)
	info := SystemInfo{BootID: "boot-a", BytesReceived: 100, BytesTransmitted: 200}

	// 首报只立基线，不结清。
	svc.RecordNodeMetric(1, info)
	assertStatus := func(connID string, nodeID int64, want string) {
		t.Helper()
		all, err := r.ListNodeConnLogs(nodeID, "", 0, 10)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for i := range all {
			if all[i].ConnID == connID {
				if all[i].Status != want {
					t.Fatalf("conn %s status = %s, want %s", connID, all[i].Status, want)
				}
				return
			}
		}
		t.Fatalf("conn %s not found", connID)
	}
	assertStatus("boot-victim", 1, repo.ConnlogStatusOpen)

	// 同 boot 再报：不动。
	svc.RecordNodeMetric(1, info)
	assertStatus("boot-victim", 1, repo.ConnlogStatusOpen)

	// boot 被证明变化：该节点 open 行结清为 interrupted，
	// end_time 为最后已知时刻（>0 且不晚于当前时刻），他节点不动。
	info.BootID = "boot-b"
	svc.RecordNodeMetric(1, info)
	assertStatus("boot-victim", 1, repo.ConnlogStatusInterrupted)
	assertStatus("other-node", 2, repo.ConnlogStatusOpen)
	all, _ := r.ListNodeConnLogs(1, "", 0, 10)
	for i := range all {
		if all[i].ConnID == "boot-victim" {
			if all[i].EndTime <= 0 || all[i].EndTime > time.Now().UnixMilli() || all[i].DurationMs != 0 {
				t.Fatalf("interrupted row = %+v, want end_time=last known (0 < end <= now), no duration", all[i])
			}
		}
	}

	// 未证明的变化（新 boot 缺 id）：不结清。
	seedOpenConnlogRow(t, r, 1, "unproven", time.Now().UnixMilli())
	svc.RecordNodeMetric(1, SystemInfo{BytesReceived: 300, BytesTransmitted: 400})
	assertStatus("unproven", 1, repo.ConnlogStatusOpen)
}

func TestConnlogPruneWiredIntoHourlyPrune(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now()
	oldStart := now.Add(-8 * 24 * time.Hour).UnixMilli()
	freshStart := now.Add(-time.Hour).UnixMilli()
	seedOpenConnlogRow(t, r, 1, "old-row", oldStart)
	seedOpenConnlogRow(t, r, 1, "fresh-row", freshStart)

	svc := NewIngestionService(r)
	svc.pruneMetricsAt(now)

	rows, err := r.ListNodeConnLogs(1, "", 0, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// 默认保留 7 天：8 天前的行被时间 prune 删除；1 小时前的行
	// 保留且未被 24h 清道夫误伤（仍 open）。
	if len(rows) != 1 || rows[0].ConnID != "fresh-row" || rows[0].Status != repo.ConnlogStatusOpen {
		t.Fatalf("after prune rows = %+v, want only fresh-row still open", rows)
	}
}
