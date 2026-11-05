package repo

import (
	"errors"
	"testing"

	"gorm.io/gorm"
)

// C1-05: ExportAll/ExportPartial 的全部段查询必须在单个快照事
// 务内完成。本组钉住机制本身: 快照回调内拿到的是事务句柄
// (写后回滚不可见), 且导出结果与逐段直查一致。

func TestC105ExportRunsInSingleTransaction(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	sawTx := false
	err = r.runExportInSnapshot(func(txRepo *Repository) error {
		// 事务句柄的 Statement.ConnPool 应为 *sql.Tx (gorm 事务型),
		// 且在其中写入后返回错误应整体回滚。
		if _, ok := interface{}(txRepo.db.Statement.ConnPool).(interface{ Commit() error }); ok {
			sawTx = true
		}
		if err := txRepo.db.Exec("INSERT INTO vite_config (name, value, time) VALUES ('c105_probe', 'x', 0)").Error; err != nil {
			return err
		}
		return errors.New("probe rollback")
	})
	if err == nil || err.Error() != "probe rollback" {
		t.Fatalf("snapshot fn error must propagate, got %v", err)
	}
	if !sawTx {
		t.Fatal("export snapshot callback did not receive a transaction handle")
	}
	var count int64
	if err := r.db.Table("vite_config").Where("name = ?", "c105_probe").Count(&count).Error; err != nil {
		t.Fatalf("count probe rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("write inside the snapshot tx must roll back with it, found %d rows", count)
	}
}

func TestC105ExportAllAndPartialStillComplete(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	all, err := r.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if all == nil || all.Version != "1.0" {
		t.Fatalf("ExportAll returned %+v", all)
	}
	// 种子管理员由 Open 落库, users 段至少含它。
	if len(all.Users) == 0 {
		t.Fatal("ExportAll users segment must include the seeded admin")
	}
	partial, err := r.ExportPartial([]string{"users", "nodes"})
	if err != nil {
		t.Fatalf("ExportPartial: %v", err)
	}
	if len(partial.Users) != len(all.Users) {
		t.Fatalf("partial users (%d) must match full export (%d)", len(partial.Users), len(all.Users))
	}
	var _ *gorm.DB = r.db
}
