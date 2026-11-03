package repo

import (
	"database/sql"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R3 批 6 回归：备份导入生命周期与引用处置（R3X-023、卫生项
// H-S01）。（R3X-005 分组各格已随分组三件套于 1.9.6 整套移除；
// 本文件保留 relay 占位重指向与 peer share 占位清理两族。）

// H 探针 P-C / R3X-023：relay 占位删除前，备份外 user_inbound 的
// relay_id 必须重指向继任 relay 并计 ImportWarnings（红：悬空
// relay_id=99、warnings 为空）。
func TestR3B6ImportRelayPlaceholderRepointsUserInbound(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-h-pc.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dest, &model.User{ID: 7, User: "u7", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.Forward{ID: 20, UserID: 7, UserName: "u7", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.NodeInbound{ID: 10, KernelID: 0, NodeID: 1, Name: "in", Protocol: "vless", Listen: "::", Port: 443, Enabled: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.NodeInboundRelay{ID: 99, InboundID: 10, ForwardID: 20, Name: "old", Managed: 1, CreatedTime: now})
	createRow(t, dest, &model.UserInbound{ID: 30, UserID: 7, InboundID: 10, RelayID: 99})

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Relays: []model.InboundRelayBackup{
				{ID: 5, InboundID: 10, ForwardID: 20, Name: "new", Managed: 1, CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, dest, "node_inbound_relay", "id = ?", 99); n != 0 {
		t.Fatalf("placeholder relay 99 must be replaced, got %d", n)
	}
	var ui model.UserInbound
	if err := dest.DB().Where("id = ?", 30).First(&ui).Error; err != nil {
		t.Fatalf("user_inbound row: %v", err)
	}
	if ui.RelayID != 5 {
		t.Fatalf("R3X-023: user_inbound relay_id = %d, want repointed to successor relay 5", ui.RelayID)
	}
	if !b8WarningsContain(result.ImportWarnings, "中转") {
		t.Fatalf("R3X-023: repointing must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
}

// R3X-023 防过修对照：备份 relay 与目标 relay 同 id 时不得产生
// 重指向/警告，分配保持原样。
func TestR3B6ImportSameIDRelayNoRepoint(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-relay-sameid.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dest, &model.User{ID: 7, User: "u7", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.Forward{ID: 20, UserID: 7, UserName: "u7", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.NodeInbound{ID: 10, KernelID: 0, NodeID: 1, Name: "in", Protocol: "vless", Listen: "::", Port: 443, Enabled: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.NodeInboundRelay{ID: 5, InboundID: 10, ForwardID: 20, Name: "same", Managed: 1, CreatedTime: now})
	createRow(t, dest, &model.UserInbound{ID: 30, UserID: 7, InboundID: 10, RelayID: 5})

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Relays: []model.InboundRelayBackup{
				{ID: 5, InboundID: 10, ForwardID: 20, Name: "same", Managed: 1, CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var ui model.UserInbound
	if err := dest.DB().Where("id = ?", 30).First(&ui).Error; err != nil {
		t.Fatalf("user_inbound row: %v", err)
	}
	if ui.RelayID != 5 {
		t.Fatalf("same-id relay import must not touch relay_id, got %d", ui.RelayID)
	}
	if len(result.ImportWarnings) != 0 {
		t.Fatalf("same-id relay import must produce no repoint warning, got %v", result.ImportWarnings)
	}
}

// H-S01：同 token 占位 share 删除时连带删除其 runtime/resource 行
// （与 DeletePeerShare 级联口径对齐；红：两行悬空残留）。
func TestR3B6ImportPeerSharePlaceholderDeletesRuntime(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b6-hs01.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dest, &model.PeerShare{ID: 99, Name: "sh", NodeID: 1, Token: "tokX", IsActive: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.PeerShareRuntime{ShareID: 99, NodeID: 1, ReservationID: "res-1", ResourceKey: "rk-1", CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.PeerShareResource{ShareID: 99, NodeID: 1, Kind: "service", OriginalName: "orig", RuntimeName: "rt", UpdatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		PeerShares: []model.PeerShareBackup{
			{ID: 5, Name: "sh", NodeID: 1, Token: "tokX", IsActive: 1, CreatedTime: now},
		},
	}
	result, err := dest.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.PeerSharesImported != 1 {
		t.Fatalf("peer share must import, got %d", result.PeerSharesImported)
	}
	if n := countWhere(t, dest, "peer_share", "id = ?", 99); n != 0 {
		t.Fatalf("placeholder share 99 must be replaced, got %d", n)
	}
	if n := countWhere(t, dest, "peer_share_runtime", "share_id = ?", 99); n != 0 {
		t.Fatalf("H-S01: runtime rows of the placeholder share must be deleted with it, got %d", n)
	}
	if n := countWhere(t, dest, "peer_share_resource", "share_id = ?", 99); n != 0 {
		t.Fatalf("H-S01: resource rows of the placeholder share must be deleted with it, got %d", n)
	}
}

