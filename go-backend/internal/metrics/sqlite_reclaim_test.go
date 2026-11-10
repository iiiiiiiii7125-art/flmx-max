package metrics

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// Package K: the hourly prune cycle must hand freed pages back
// to the OS. Insert a batch of metrics, prune them all away,
// then run the reclaim step of pruneMetricsAt and require the
// freelist to be (nearly) drained on an INCREMENTAL database.
func TestReclaimSQLiteSpaceAfterPrune(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "k-reclaim.db"))
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]*model.NodeMetric, 0, 3000)
	now := time.Now().UnixMilli()
	for i := 0; i < 3000; i++ {
		rows = append(rows, &model.NodeMetric{NodeID: 1, Timestamp: now})
	}
	if err := r.InsertNodeMetricBatch(rows); err != nil {
		t.Fatal(err)
	}
	if err := r.PruneNodeMetrics(now + 1000); err != nil {
		t.Fatal(err)
	}
	stats, err := r.SQLiteSpaceStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.FreelistCount == 0 {
		t.Fatal("freelist = 0 after pruning 3000 rows; test setup did not create reclaimable space")
	}

	s := NewIngestionService(r)
	s.reclaimSQLiteSpace(time.Now())

	after, err := r.SQLiteSpaceStats()
	if err != nil {
		t.Fatal(err)
	}
	if after.FreelistCount != 0 {
		t.Fatalf("freelist = %d after reclaim, want 0", after.FreelistCount)
	}
}

// The reclaim step is a no-op (and must not error or panic) when
// there is nothing to reclaim.
func TestReclaimSQLiteSpaceNoop(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "k-noop.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := NewIngestionService(r)
	s.reclaimSQLiteSpace(time.Now())
	var nilSvc *IngestionService
	nilSvc.reclaimSQLiteSpace(time.Now())
}
