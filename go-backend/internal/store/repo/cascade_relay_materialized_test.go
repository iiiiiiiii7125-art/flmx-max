package repo

import (
	"database/sql"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// FIX132-C（A3-02 + X2-01）：级联删除 relay 行时，必须先物化被删 relay
// 的 id 集合，再按 relay_id 清 user_inbound 授权行。
// 旧 deleteNodeKernelsTx 把「入站 id 集合」直接套到 relay_id 列上：
// 两条自增序列数值域大面积重叠，删节点会静默误删其它节点中转的授权
// （X2 探针 X2-A 坐实）；删转发/删隧道/删用户三条级联则完全不清
// relay 授权行，留下孤儿（X2 探针 X2-C/C2 坐实）。
// 本文件数据全部满足不变式 I：grant.RelayID>0 ⇒ 其 relay 行的
// InboundID == grant.InboundID（写入侧 SetUserInboundAssignments 强制）。

func seedNodeWithInbound(t *testing.T, r *Repository, nodeID, kernelID, inboundID int64, now int64) {
	t.Helper()
	createRow(t, r, &model.Node{ID: nodeID, Name: "n", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, r, &model.NodeKernel{ID: kernelID, NodeID: nodeID, KernelType: "xray", CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NodeInbound{ID: inboundID, NodeID: nodeID, KernelID: kernelID, Name: "ib", Protocol: "vless", Port: 443, CreatedTime: now, UpdatedTime: now})
}

func TestFix132DeleteNodeCascadeUsesMaterializedRelayIDs(t *testing.T) {
	r := openCascadeRepo(t, "node-relay-materialized.db")
	now := time.Now().UnixMilli()
	// 节点 A：inbound 21，relay 77（属于 inbound 21）
	seedNodeWithInbound(t, r, 1, 11, 21, now)
	createRow(t, r, &model.NodeInboundRelay{ID: 77, InboundID: 21, ForwardID: 500})
	createRow(t, r, &model.UserInbound{ID: 31, UserID: 8, InboundID: 21, RelayID: 77})
	createRow(t, r, &model.UserInbound{ID: 32, UserID: 9, InboundID: 21, RelayID: 0})
	// 节点 B：inbound 99，relay 21 —— relay id 21 与 A 的 inbound id 21 数值碰撞
	seedNodeWithInbound(t, r, 2, 12, 99, now)
	createRow(t, r, &model.NodeInboundRelay{ID: 21, InboundID: 99, ForwardID: 501})
	createRow(t, r, &model.UserInbound{ID: 33, UserID: 7, InboundID: 99, RelayID: 21})
	// A3-10：节点级联应一并清 node_latency_sample 与 node_seq
	createRow(t, r, &model.NodeLatencySample{NodeID: 1, CreatedTime: now})
	createRow(t, r, &NodeSeq{NodeID: 1, LastSeq: 5, UpdatedTime: now})

	if err := r.DeleteNodeCascade(1); err != nil {
		t.Fatalf("DeleteNodeCascade: %v", err)
	}
	if n := countWhere(t, r, "node_latency_sample", "node_id = ?", 1); n != 0 {
		t.Fatalf("A3-10: node cascade left %d latency samples", n)
	}
	if n := countWhere(t, r, "node_seq", "node_id = ?", 1); n != 0 {
		t.Fatalf("A3-10: node cascade left %d node_seq rows", n)
	}
	// A 侧授权（直连 + 中转）必须清干净
	if n := countWhere(t, r, "user_inbound", "inbound_id = ?", 21); n != 0 {
		t.Fatalf("node A grants must be deleted, left %d", n)
	}
	// B 侧 relay 本体必须存活
	if n := countWhere(t, r, "node_inbound_relay", "id = ?", 21); n != 1 {
		t.Fatalf("node B relay row must survive, count=%d", n)
	}
	// B 侧授权绝不能被误删（旧代码 relay_id IN (inbound ids) 会命中 21）
	if n := countWhere(t, r, "user_inbound", "id = ?", 33); n != 1 {
		t.Fatalf("A3-02: grant on surviving node B relay was wrongly deleted")
	}
}

func TestFix132DeleteForwardCascadeClearsRelayGrants(t *testing.T) {
	r := openCascadeRepo(t, "forward-relay-grants.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Tunnel{ID: 5, Name: "t5", Type: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.Forward{ID: 51, UserID: 7, TunnelID: 5, Name: "f51", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NodeInboundRelay{ID: 88, InboundID: 99, ForwardID: 51})
	createRow(t, r, &model.UserInbound{ID: 41, UserID: 9, InboundID: 99, RelayID: 88})

	if err := r.DeleteForwardCascade(51); err != nil {
		t.Fatalf("DeleteForwardCascade: %v", err)
	}
	if n := countWhere(t, r, "node_inbound_relay", "id = ?", 88); n != 0 {
		t.Fatalf("relay row must be deleted, left %d", n)
	}
	if n := countWhere(t, r, "user_inbound", "relay_id = ?", 88); n != 0 {
		t.Fatalf("X2-01/A3-03: forward cascade left %d orphan relay grants", n)
	}
}

func TestFix132DeleteTunnelCascadeClearsRelayGrants(t *testing.T) {
	r := openCascadeRepo(t, "tunnel-relay-grants.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Tunnel{ID: 6, Name: "t6", Type: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.Forward{ID: 52, UserID: 7, TunnelID: 6, Name: "f52", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NodeInboundRelay{ID: 88, InboundID: 99, ForwardID: 52})
	createRow(t, r, &model.UserInbound{ID: 41, UserID: 9, InboundID: 99, RelayID: 88})

	if err := r.DeleteTunnelCascade(6); err != nil {
		t.Fatalf("DeleteTunnelCascade: %v", err)
	}
	if n := countWhere(t, r, "node_inbound_relay", "id = ?", 88); n != 0 {
		t.Fatalf("relay row must be deleted, left %d", n)
	}
	if n := countWhere(t, r, "user_inbound", "relay_id = ?", 88); n != 0 {
		t.Fatalf("X2-01: tunnel cascade left %d orphan relay grants", n)
	}
}

func TestFix132DeleteUserCascadeClearsOtherUsersRelayGrants(t *testing.T) {
	r := openCascadeRepo(t, "user-relay-grants.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.User{ID: 3, User: "owner", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.User{ID: 9, User: "other", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 5, Name: "t5", Type: 1, CreatedTime: now, UpdatedTime: now})
	// forward 属于被删用户 3；relay 挂在其上；授权属于另一个用户 9
	createRow(t, r, &model.Forward{ID: 51, UserID: 3, TunnelID: 5, Name: "f51", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NodeInboundRelay{ID: 21, InboundID: 99, ForwardID: 51})
	createRow(t, r, &model.UserInbound{ID: 41, UserID: 9, InboundID: 99, RelayID: 21})

	if err := r.DeleteUserCascade(3); err != nil {
		t.Fatalf("DeleteUserCascade: %v", err)
	}
	if n := countWhere(t, r, "node_inbound_relay", "id = ?", 21); n != 0 {
		t.Fatalf("relay row must be deleted, left %d", n)
	}
	if n := countWhere(t, r, "user_inbound", "relay_id = ?", 21); n != 0 {
		t.Fatalf("X2-01: user cascade left %d orphan relay grants of another user", n)
	}
}

func TestFix132DeleteNodeInboundClearsRelayGrantsByRelaySet(t *testing.T) {
	r := openCascadeRepo(t, "inbound-relay-grants.db")
	now := time.Now().UnixMilli()
	seedNodeWithInbound(t, r, 1, 11, 21, now)
	createRow(t, r, &model.NodeInboundRelay{ID: 77, InboundID: 21, ForwardID: 500})
	createRow(t, r, &model.UserInbound{ID: 31, UserID: 8, InboundID: 21, RelayID: 77})

	if err := r.DeleteNodeInbound(21); err != nil {
		t.Fatalf("DeleteNodeInbound: %v", err)
	}
	if n := countWhere(t, r, "user_inbound", "relay_id = ?", 77); n != 0 {
		t.Fatalf("inbound delete left %d relay grants", n)
	}
}

func TestFix132DeleteNodeKernelClearsRelayGrantsByRelaySet(t *testing.T) {
	r := openCascadeRepo(t, "kernel-relay-grants.db")
	now := time.Now().UnixMilli()
	seedNodeWithInbound(t, r, 1, 11, 21, now)
	createRow(t, r, &model.NodeInboundRelay{ID: 77, InboundID: 21, ForwardID: 500})
	createRow(t, r, &model.UserInbound{ID: 31, UserID: 8, InboundID: 21, RelayID: 77})

	if err := r.DeleteNodeKernel(11); err != nil {
		t.Fatalf("DeleteNodeKernel: %v", err)
	}
	if n := countWhere(t, r, "user_inbound", "relay_id = ?", 77); n != 0 {
		t.Fatalf("kernel delete left %d relay grants", n)
	}
}
