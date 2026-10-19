package repo_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// AUDIT-R1 (TST-001): 回归测试。R5-B7 在 DeleteTunnelCascade 中对
// NodeInboundRelay / NftCounterState / GroupPermissionGrant 使用了不存在的
// tunnel_id 列，导致任何隧道删除都以 "no such column" 回滚。
// 本测试覆盖全部三条 latent 路径（forward_id / user_tunnel_id 关联），
// 并断言对照隧道的数据不受影响（无过度删除）。
func TestAuditR1_TunnelCascadeCleansOrphansByCorrectColumns(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "audit-r1.db"))
	if err != nil {
		t.Fatal(err)
	}
	db := r.DB()
	mustCreate := func(v interface{}) {
		t.Helper()
		if err := db.Create(v).Error; err != nil {
			t.Fatalf("create %T: %v", v, err)
		}
	}

	node := &model.Node{Name: "audit-r1-node"}
	mustCreate(node)

	mkTunnel := func(name string) *model.Tunnel {
		tw := &model.Tunnel{Name: name, TrafficRatio: 1, Type: 1, Protocol: "tls", Status: 1}
		mustCreate(tw)
		return tw
	}
	tunnelA := mkTunnel("audit-r1-tunnel-a") // 待删除
	tunnelB := mkTunnel("audit-r1-tunnel-b") // 对照，必须存活

	// tunnelA 的全套关联行。
	fwdA := &model.Forward{UserID: 1, UserName: "admin", Name: "fwd-a", TunnelID: tunnelA.ID, RemoteAddr: "203.0.113.1:1", Status: 1}
	mustCreate(fwdA)
	mustCreate(&model.ForwardPort{ForwardID: fwdA.ID, NodeID: node.ID, Port: 20001})
	mustCreate(&model.NodeInboundRelay{InboundID: 1, ForwardID: fwdA.ID, Name: "relay-a"})
	mustCreate(&model.NftCounterState{NodeID: node.ID, ForwardID: fwdA.ID, Protocol: "tcp", Direction: "in", CreatedTime: 1})
	mustCreate(&model.NftRuleBinding{ForwardID: fwdA.ID, NodeID: node.ID, InPort: 20001, TargetAddr: "203.0.113.1:1"})
	utA := &model.UserTunnel{UserID: 1, TunnelID: tunnelA.ID, Status: 1}
	mustCreate(utA)
	mustCreate(&model.GroupPermissionGrant{UserGroupID: 1, TunnelGroupID: 1, UserTunnelID: utA.ID})
	mustCreate(&model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: tunnelA.ID})
	mustCreate(&model.SpeedLimit{Name: "sl-a", Speed: 100, TunnelID: sql.NullInt64{Int64: tunnelA.ID, Valid: true}, Status: 1})
	mustCreate(&model.TunnelMetric{TunnelID: tunnelA.ID, NodeID: node.ID, Timestamp: 1})
	mustCreate(&model.ChainTunnel{TunnelID: tunnelA.ID, ChainType: "exit", NodeID: node.ID})

	// tunnelB 的对照行（删除 tunnelA 后必须全部存活）。
	fwdB := &model.Forward{UserID: 1, UserName: "admin", Name: "fwd-b", TunnelID: tunnelB.ID, RemoteAddr: "203.0.113.2:2", Status: 1}
	mustCreate(fwdB)
	mustCreate(&model.ForwardPort{ForwardID: fwdB.ID, NodeID: node.ID, Port: 20002})
	mustCreate(&model.NodeInboundRelay{InboundID: 2, ForwardID: fwdB.ID, Name: "relay-b"})

	if err := r.DeleteTunnelCascade(tunnelA.ID); err != nil {
		t.Fatalf("DeleteTunnelCascade: %v", err)
	}

	count := func(m interface{}, q string, args ...interface{}) int64 {
		t.Helper()
		var c int64
		if err := db.Model(m).Where(q, args...).Count(&c).Error; err != nil {
			t.Fatalf("count %T: %v", m, err)
		}
		return c
	}
	assertGone := func(m interface{}, q string, args ...interface{}) {
		t.Helper()
		if c := count(m, q, args...); c != 0 {
			t.Errorf("%T orphan: %d (%s)", m, c, q)
		}
	}

	// tunnelA 的行必须全部消失。
	assertGone(&model.Tunnel{}, "id = ?", tunnelA.ID)
	assertGone(&model.Forward{}, "tunnel_id = ?", tunnelA.ID)
	assertGone(&model.ForwardPort{}, "forward_id = ?", fwdA.ID)
	assertGone(&model.NodeInboundRelay{}, "forward_id = ?", fwdA.ID)
	assertGone(&model.NftCounterState{}, "forward_id = ?", fwdA.ID)
	assertGone(&model.NftRuleBinding{}, "forward_id = ?", fwdA.ID)
	assertGone(&model.UserTunnel{}, "tunnel_id = ?", tunnelA.ID)
	assertGone(&model.GroupPermissionGrant{}, "user_tunnel_id = ?", utA.ID)
	assertGone(&model.TunnelGroupTunnel{}, "tunnel_id = ?", tunnelA.ID)
	assertGone(&model.SpeedLimit{}, "tunnel_id = ?", tunnelA.ID)
	assertGone(&model.TunnelMetric{}, "tunnel_id = ?", tunnelA.ID)
	assertGone(&model.ChainTunnel{}, "tunnel_id = ?", tunnelA.ID)

	// tunnelB 的行必须全部存活（无过度删除）。
	if c := count(&model.Tunnel{}, "id = ?", tunnelB.ID); c != 1 {
		t.Errorf("control tunnel deleted: count=%d", c)
	}
	if c := count(&model.Forward{}, "id = ?", fwdB.ID); c != 1 {
		t.Errorf("control forward deleted: count=%d", c)
	}
	if c := count(&model.NodeInboundRelay{}, "forward_id = ?", fwdB.ID); c != 1 {
		t.Errorf("control relay deleted: count=%d", c)
	}
}
