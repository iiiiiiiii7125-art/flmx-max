package repo

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func TestNftCounterStateUpsertUpdatesExistingKey(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	first := []NftCounterStateInput{
		{
			NodeID:        11,
			ForwardID:     42,
			Protocol:      "tcp",
			Direction:     "to-target",
			RuleHash:      "hash-a",
			Bytes:         100,
			Packets:       10,
			CollectedTime: 1000,
		},
		{
			NodeID:    0,
			ForwardID: 42,
			Protocol:  "tcp",
			Direction: "to-target",
			Bytes:     999,
		},
	}
	if err := r.UpsertNftCounterStates(first, 2000); err != nil {
		t.Fatalf("first UpsertNftCounterStates: %v", err)
	}

	second := []NftCounterStateInput{
		{
			NodeID:        11,
			ForwardID:     42,
			Protocol:      "tcp",
			Direction:     "to-target",
			RuleHash:      "hash-b",
			Bytes:         250,
			Packets:       25,
			CollectedTime: 3000,
		},
	}
	if err := r.UpsertNftCounterStates(second, 4000); err != nil {
		t.Fatalf("second UpsertNftCounterStates: %v", err)
	}

	rows, err := r.GetNftCounterStatesByNode(11)
	if err != nil {
		t.Fatalf("GetNftCounterStatesByNode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one counter state row, got %d: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.ForwardID != 42 || got.Protocol != "tcp" || got.Direction != "to-target" {
		t.Fatalf("unexpected counter state key: %+v", got)
	}
	if got.RuleHash != "hash-b" || got.Bytes != 250 || got.Packets != 25 || got.CollectedTime != 3000 {
		t.Fatalf("counter state was not updated: %+v", got)
	}
	if got.CreatedTime != 2000 || got.UpdatedTime != 4000 {
		t.Fatalf("unexpected timestamps after upsert: %+v", got)
	}
}

func TestNftCounterStateDeleteByForwardRemovesOnlyMatchingRows(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	inputs := []NftCounterStateInput{
		{NodeID: 11, ForwardID: 42, Protocol: "tcp", Direction: "to-target", RuleHash: "a", Bytes: 100, Packets: 10, CollectedTime: 1000},
		{NodeID: 11, ForwardID: 43, Protocol: "udp", Direction: "from-target", RuleHash: "b", Bytes: 200, Packets: 20, CollectedTime: 1000},
		{NodeID: 12, ForwardID: 42, Protocol: "tcp", Direction: "to-target", RuleHash: "c", Bytes: 300, Packets: 30, CollectedTime: 1000},
	}
	if err := r.UpsertNftCounterStates(inputs, 2000); err != nil {
		t.Fatalf("UpsertNftCounterStates: %v", err)
	}
	if err := r.DeleteNftCounterStatesByForward(42); err != nil {
		t.Fatalf("DeleteNftCounterStatesByForward: %v", err)
	}

	node11, err := r.GetNftCounterStatesByNode(11)
	if err != nil {
		t.Fatalf("GetNftCounterStatesByNode(11): %v", err)
	}
	if len(node11) != 1 || node11[0].ForwardID != 43 {
		t.Fatalf("expected only forward 43 for node 11, got %+v", node11)
	}
	node12, err := r.GetNftCounterStatesByNode(12)
	if err != nil {
		t.Fatalf("GetNftCounterStatesByNode(12): %v", err)
	}
	if len(node12) != 0 {
		t.Fatalf("expected forward 42 state removed from node 12, got %+v", node12)
	}
}

func TestDeleteForwardCascadeRemovesNftCounterStateOnlyForDeletedForward(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	forwards := []model.Forward{
		{ID: 42, UserID: 1, UserName: "admin", Name: "forward-a", TunnelID: 10, RemoteAddr: "203.0.113.1:80", Strategy: "fifo", CreatedTime: now, UpdatedTime: now, Status: 1},
		{ID: 43, UserID: 1, UserName: "admin", Name: "forward-b", TunnelID: 10, RemoteAddr: "203.0.113.2:80", Strategy: "fifo", CreatedTime: now, UpdatedTime: now, Status: 1},
	}
	if err := r.DB().Create(&forwards).Error; err != nil {
		t.Fatalf("seed forwards: %v", err)
	}
	if err := r.UpsertNftCounterStates([]NftCounterStateInput{
		{NodeID: 11, ForwardID: 42, Protocol: "tcp", Direction: "to-target", RuleHash: "a", Bytes: 100, Packets: 10, CollectedTime: now},
		{NodeID: 11, ForwardID: 43, Protocol: "udp", Direction: "from-target", RuleHash: "b", Bytes: 200, Packets: 20, CollectedTime: now},
	}, now); err != nil {
		t.Fatalf("UpsertNftCounterStates: %v", err)
	}

	if err := r.DeleteForwardCascade(42); err != nil {
		t.Fatalf("DeleteForwardCascade: %v", err)
	}

	rows, err := r.GetNftCounterStatesByNode(11)
	if err != nil {
		t.Fatalf("GetNftCounterStatesByNode: %v", err)
	}
	if len(rows) != 1 || rows[0].ForwardID != 43 {
		t.Fatalf("expected only forward 43 counter state to remain, got %+v", rows)
	}
	var deletedForwardCount int64
	if err := r.DB().Model(&model.Forward{}).Where("id = ?", int64(42)).Count(&deletedForwardCount).Error; err != nil {
		t.Fatalf("count deleted forward: %v", err)
	}
	if deletedForwardCount != 0 {
		t.Fatalf("expected forward 42 deleted, count=%d", deletedForwardCount)
	}
}

func TestNftCounterStateUpsertSkipsInvalidProtocolAndDirection(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	inputs := []NftCounterStateInput{
		{NodeID: 11, ForwardID: 42, Protocol: "icmp", Direction: "to-target", RuleHash: "bad-protocol", Bytes: 100, Packets: 10, CollectedTime: 1000},
		{NodeID: 11, ForwardID: 43, Protocol: "tcp", Direction: "sideways", RuleHash: "bad-direction", Bytes: 200, Packets: 20, CollectedTime: 1000},
		{NodeID: 11, ForwardID: 44, Protocol: " UDP ", Direction: " FROM-TARGET ", RuleHash: "valid", Bytes: 300, Packets: 30, CollectedTime: 1000},
	}
	if err := r.UpsertNftCounterStates(inputs, 2000); err != nil {
		t.Fatalf("UpsertNftCounterStates: %v", err)
	}

	rows, err := r.GetNftCounterStatesByNode(11)
	if err != nil {
		t.Fatalf("GetNftCounterStatesByNode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected only the valid counter state row, got %d: %+v", len(rows), rows)
	}
	if rows[0].ForwardID != 44 || rows[0].Protocol != "udp" || rows[0].Direction != "from-target" {
		t.Fatalf("unexpected valid counter state row: %+v", rows[0])
	}
}

func TestNftCounterStateUpsertSkipsCountersAboveInt64(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	inputs := []NftCounterStateInput{
		{NodeID: 11, ForwardID: 42, Protocol: "tcp", Direction: "to-target", RuleHash: "too-large", Bytes: uint64(math.MaxInt64) + 1, Packets: 10, CollectedTime: 1000},
		{NodeID: 11, ForwardID: 43, Protocol: "udp", Direction: "from-target", RuleHash: "valid", Bytes: 300, Packets: 30, CollectedTime: 1000},
	}
	if err := r.UpsertNftCounterStates(inputs, 2000); err != nil {
		t.Fatalf("UpsertNftCounterStates: %v", err)
	}

	rows, err := r.GetNftCounterStatesByNode(11)
	if err != nil {
		t.Fatalf("GetNftCounterStatesByNode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected only the valid counter state row, got %d: %+v", len(rows), rows)
	}
	if rows[0].ForwardID != 43 || rows[0].Bytes != 300 || rows[0].Packets != 30 {
		t.Fatalf("unexpected valid counter state row: %+v", rows[0])
	}
}

func TestListNftablesNodesForCollectionReturnsActiveNftablesWithSSHOrdered(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "nft-collection.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	seedCollectionNode(t, r, 1, "agent", 1, now)
	seedCollectionNode(t, r, 2, " nftables ", 1, now)
	seedCollectionNode(t, r, 3, "NFTABLES", 0, now)
	seedCollectionNode(t, r, 4, "nftables", 1, now)
	seedCollectionNode(t, r, 5, "nftables", 1, now)

	if err := r.UpsertNodeSSHConfig(4, NftSSHConfigInput{
		Host:     "203.0.113.4",
		Port:     2222,
		Username: "root",
		AuthType: "password",
		Password: "secret-4",
		SudoMode: "none",
	}, now); err != nil {
		t.Fatalf("upsert ssh config 4: %v", err)
	}
	if err := r.UpsertNodeSSHConfig(2, NftSSHConfigInput{
		Host:     "203.0.113.2",
		Port:     22,
		Username: "admin",
		AuthType: "private_key",
		SudoMode: "sudo",
	}, now); err != nil {
		t.Fatalf("upsert ssh config 2: %v", err)
	}
	// DEFECT-NFT-COLLECT-01：节点 2 钉入指纹、节点 4 不钉——采集查询
	// 必须把已钉入的指纹原样带回（漏投影时两者的指纹都为空、无法区分
	// 「未钉入」与「投影丢失」，这正是原缺陷在存量测试下隐身的原因）。
	if err := r.UpdateNodeSSHHostKeyFingerprint(2, "SHA256:node2-pinned-fingerprint", now); err != nil {
		t.Fatalf("pin fingerprint node 2: %v", err)
	}

	nodes, err := r.ListNftablesNodesForCollection()
	if err != nil {
		t.Fatalf("ListNftablesNodesForCollection: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 collection nodes, got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].NodeID != 2 || nodes[1].NodeID != 4 {
		t.Fatalf("expected nodes ordered by id [2 4], got [%d %d]", nodes[0].NodeID, nodes[1].NodeID)
	}
	if nodes[0].Config.NodeID != 2 || nodes[0].Config.Host != "203.0.113.2" || nodes[0].Config.Username != "admin" {
		t.Fatalf("unexpected first config: %+v", nodes[0].Config)
	}
	if nodes[0].Config.HostKeyFingerprint != "SHA256:node2-pinned-fingerprint" {
		t.Fatalf("pinned fingerprint lost in collection projection: %+v", nodes[0].Config)
	}
	if nodes[0].Config.AuthType != "private_key" || nodes[0].Config.SudoMode != "sudo" {
		t.Fatalf("unexpected first config auth/sudo: %+v", nodes[0].Config)
	}
	if nodes[1].Config.NodeID != 4 || nodes[1].Config.Port != 2222 || nodes[1].Config.Password.String != "secret-4" {
		t.Fatalf("unexpected second config: %+v", nodes[1].Config)
	}
	if nodes[1].Config.HostKeyFingerprint != "" {
		t.Fatalf("unpinned node must report empty fingerprint, got %q", nodes[1].Config.HostKeyFingerprint)
	}
}

// TestListNftablesNodesForCollectionProjectsEverySSHConfigField 是
// DEFECT-NFT-COLLECT-01 的字段级回归钉：种子配置行 13 个字段全部填
// 互异非零值，采集查询返回的 Config 必须逐字段等值——投影/组装漏
// 任一列（或列序错位串值）都会在此转红。原缺陷即漏投影
// host_key_fingerprint 一列：采集器与 blocked-port sweep 每轮拿到
// 空指纹、SSH runner fail-closed，nft 转发永不入账。
func TestListNftablesNodesForCollectionProjectsEverySSHConfigField(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	seedCollectionNode(t, r, 7, "nftables", 1, now)

	want := model.NodeSSHConfig{
		ID:                 77,
		NodeID:             7,
		Host:               "192.0.2.77",
		Port:               2207,
		Username:           "collector-user",
		AuthType:           "private_key",
		Password:           sql.NullString{String: "pw-distinct", Valid: true},
		PrivateKey:         sql.NullString{String: "pk-distinct", Valid: true},
		Passphrase:         sql.NullString{String: "pp-distinct", Valid: true},
		SudoMode:           "sudo",
		HostKeyFingerprint: "SHA256:fp-distinct-0123456789",
		CreatedTime:        1700000000123,
		UpdatedTime:        1700000000456,
	}
	if err := r.DB().Create(&want).Error; err != nil {
		t.Fatalf("seed ssh config: %v", err)
	}

	nodes, err := r.ListNftablesNodesForCollection()
	if err != nil {
		t.Fatalf("ListNftablesNodesForCollection: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 collection node, got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].NodeID != want.NodeID {
		t.Fatalf("wrapper node id: got %d want %d", nodes[0].NodeID, want.NodeID)
	}
	got := nodes[0].Config
	if got.ID != want.ID {
		t.Errorf("ID: got %d want %d", got.ID, want.ID)
	}
	if got.NodeID != want.NodeID {
		t.Errorf("NodeID: got %d want %d", got.NodeID, want.NodeID)
	}
	if got.Host != want.Host {
		t.Errorf("Host: got %q want %q", got.Host, want.Host)
	}
	if got.Port != want.Port {
		t.Errorf("Port: got %d want %d", got.Port, want.Port)
	}
	if got.Username != want.Username {
		t.Errorf("Username: got %q want %q", got.Username, want.Username)
	}
	if got.AuthType != want.AuthType {
		t.Errorf("AuthType: got %q want %q", got.AuthType, want.AuthType)
	}
	if got.Password != want.Password {
		t.Errorf("Password: got %+v want %+v", got.Password, want.Password)
	}
	if got.PrivateKey != want.PrivateKey {
		t.Errorf("PrivateKey: got %+v want %+v", got.PrivateKey, want.PrivateKey)
	}
	if got.Passphrase != want.Passphrase {
		t.Errorf("Passphrase: got %+v want %+v", got.Passphrase, want.Passphrase)
	}
	if got.SudoMode != want.SudoMode {
		t.Errorf("SudoMode: got %q want %q", got.SudoMode, want.SudoMode)
	}
	if got.HostKeyFingerprint != want.HostKeyFingerprint {
		t.Errorf("HostKeyFingerprint: got %q want %q", got.HostKeyFingerprint, want.HostKeyFingerprint)
	}
	if got.CreatedTime != want.CreatedTime {
		t.Errorf("CreatedTime: got %d want %d", got.CreatedTime, want.CreatedTime)
	}
	if got.UpdatedTime != want.UpdatedTime {
		t.Errorf("UpdatedTime: got %d want %d", got.UpdatedTime, want.UpdatedTime)
	}
}

func seedCollectionNode(t *testing.T, r *Repository, id int64, forwardMode string, status int, now int64) {
	t.Helper()
	if err := r.DB().Exec(`
		INSERT INTO node(id, name, secret, server_ip, port, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, forward_mode)
		VALUES(?, ?, 'secret', ?, '1000-2000', ?, ?, ?, '[::]', '[::]', 0, ?)
	`, id, "node", "198.51.100.1", now, now, status, forwardMode).Error; err != nil {
		t.Fatalf("insert node %d: %v", id, err)
	}
}
