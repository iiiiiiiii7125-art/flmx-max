package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// F-055 (B4): 备份恢复此前无端口预检/锁——与已有入站同口的转发能被
// 静默恢复成坏配置（跨表无 DB 约束），备份内部自相冲突时也一样。

func forwardBackup(id int64, name string, nodeID int64, port int) model.ForwardBackup {
	ports := []model.ForwardPortBackup{{NodeID: nodeID, Port: port}}
	return model.ForwardBackup{
		ID: id, UserID: 1, UserName: "admin", Name: name, TunnelID: 1,
		RemoteAddr: "1.1.1.1:443", Strategy: "fifo", Status: 1,
		ForwardPorts: &ports,
	}
}

func TestBackupImportPortPrecheck(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("conflict with existing inbound aborts before writing", func(t *testing.T) {
		dest := openCascadeRepo(t, "precheck-dest.db")
		createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
		createRow(t, dest, &model.NodeInbound{ID: 21, NodeID: 1, KernelID: 11, Name: "ib", Protocol: "vless", Port: 26001, CreatedTime: now, UpdatedTime: now})

		backup := &model.BackupData{Version: "1.0", Forwards: []model.ForwardBackup{forwardBackup(51, "clash", 1, 26001)}}
		_, err := dest.Import(backup, []string{"forwards"})
		if err == nil || !strings.Contains(err.Error(), "端口冲突") {
			t.Fatalf("F-055: import colliding with an inbound must fail with a port-conflict error, got %v", err)
		}
		if n := countWhere(t, dest, "forward", "id = ?", 51); n != 0 {
			t.Fatalf("aborted import must not write the forward, found %d rows", n)
		}
	})

	t.Run("backup-internal conflict aborts", func(t *testing.T) {
		dest := openCascadeRepo(t, "precheck-internal.db")
		backup := &model.BackupData{Version: "1.0", Forwards: []model.ForwardBackup{
			forwardBackup(51, "one", 1, 26001),
			forwardBackup(52, "two", 1, 26001),
		}}
		_, err := dest.Import(backup, []string{"forwards"})
		if err == nil || !strings.Contains(err.Error(), "端口冲突") {
			t.Fatalf("F-055: self-conflicting backup must fail, got %v", err)
		}
	})

	t.Run("re-import of the same backup is idempotent", func(t *testing.T) {
		dest := openCascadeRepo(t, "precheck-idem.db")
		createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
		backup := &model.BackupData{Version: "1.0", Forwards: []model.ForwardBackup{forwardBackup(51, "fine", 1, 26001)}}
		if _, err := dest.Import(backup, []string{"forwards"}); err != nil {
			t.Fatalf("first import: %v", err)
		}
		if _, err := dest.Import(backup, []string{"forwards"}); err != nil {
			t.Fatalf("re-import of identical backup must succeed (own rows are being replaced), got %v", err)
		}
	})
}
