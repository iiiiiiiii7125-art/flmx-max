package handler

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// B4-03: (1) 入口集变更清理的失败必须回传 warnings, 不得吞错;
// (2) 重连对账须补扫「转发仍存在、但本节点已不在其隧道入口集」
// 的残留服务; 仍是入口的节点不得误删。

func TestB403CleanupSurfacesListFailureAsWarning(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	h := New(r, "test-secret")
	// 让 listForwardsByTunnel 失败: 删掉 forward 表。
	if err := r.DB().Migrator().DropTable(&model.Forward{}); err != nil {
		t.Fatalf("drop forward table: %v", err)
	}
	warnings := h.cleanupTunnelForwardRuntimesOnRemovedEntryNodes(1, []int64{9}, []int64{8})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "对账") {
		t.Fatalf("list failure must surface as one warning naming the reconnect sweep, got %v", warnings)
	}
	// 无节点被移除时仍是无操作。
	if w := h.cleanupTunnelForwardRuntimesOnRemovedEntryNodes(1, []int64{9}, []int64{9}); len(w) != 0 {
		t.Fatalf("no removed nodes must produce no warnings, got %v", w)
	}
}

func TestB403ReconnectSweepDeletesServiceOnRemovedEntryNode(t *testing.T) {
	a := newCleanupAgent(t)
	db := a.h.repo.DB()
	if err := db.Create(&model.Tunnel{ID: 1, Name: "t", Type: 2, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	// 隧道当前入口集 = {2}; 上报服务的节点 1 已被移除。
	if err := db.Create(&model.ChainTunnel{TunnelID: 1, ChainType: "1", NodeID: 2, Inx: sql.NullInt64{Int64: 1, Valid: true}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Forward{ID: 70, UserID: 1, UserName: "local", Name: "local", TunnelID: 1, RemoteAddr: "127.0.0.1:80", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	runCleanupPath(a.h, "config", []string{"70_1_0_tcp"})
	a.probe(t)
	if commands := a.commandsOfType("DeleteService"); len(commands) != 1 {
		t.Fatalf("service on a removed entry node must be swept on reconnect, got %+v", commands)
	}
}

func TestB403ReconnectSweepKeepsServiceOnCurrentEntryNode(t *testing.T) {
	a := newCleanupAgent(t)
	db := a.h.repo.DB()
	if err := db.Create(&model.Tunnel{ID: 1, Name: "t", Type: 2, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	// 节点 1 仍在入口集内。
	if err := db.Create(&model.ChainTunnel{TunnelID: 1, ChainType: "1", NodeID: 1, Inx: sql.NullInt64{Int64: 1, Valid: true}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Forward{ID: 70, UserID: 1, UserName: "local", Name: "local", TunnelID: 1, RemoteAddr: "127.0.0.1:80", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	runCleanupPath(a.h, "config", []string{"70_1_0_tcp"})
	a.probe(t)
	if commands := a.commandsOfType("DeleteService"); len(commands) != 0 {
		t.Fatalf("service on a current entry node must be preserved, got %+v", commands)
	}
}
