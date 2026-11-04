package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// RR4-F3-01（BC-26 写面全集第六面）：删隧道 replace 迁移面此前无
// realm 策略门——多目标 fifo/rand 转发经 UpdateForwardTunnel 迁入
// realm 隧道后被 RenderRealm 静默改写为 roundrobin，且迁入后经更新
// 面改任何字段都会被门拦下、形成只进不改的死角。修复在 plan 层
// （planSingleTunnelDeleteForwardMigration）以有效 RemoteAddr 过
// validateRealmForwardStrategy，与批量换隧道面同口径；单删/批删两
// 入口共用 processTunnelDeleteWithForwards → plan 链，一处落门两
// 入口同覆。本文件钉死：fifo/rand 多目标迁 realm 必逐条失败点名、
// 源隧道与转发不动；round 多目标与 fifo 单目标（门前提不成立）照常
// 可迁。

type rr4F3Fixture struct {
	h *Handler
	r *repo.Repository
}

func setupRR4F3(t *testing.T) *rr4F3Fixture {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/rr4-f3.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return &rr4F3Fixture{h: New(r, "test-secret"), r: r}
}

func (fx *rr4F3Fixture) addNode(t *testing.T, name string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := fx.r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, '', '10000-40000', '', 'v1', 0, 0, 0, ?, ?, 0, '[::]', '[::]', 0)`, name, name+"-secret", "192.0.2.10", "192.0.2.10", now, now).Error; err != nil {
		t.Fatalf("seed node %s: %v", name, err)
	}
	var id int64
	if err := fx.r.DB().Raw(`SELECT id FROM node WHERE name = ?`, name).Scan(&id).Error; err != nil {
		t.Fatalf("lookup node %s: %v", name, err)
	}
	return id
}

func (fx *rr4F3Fixture) addTunnel(t *testing.T, name, engine string, entryNodeIDs ...int64) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := fx.r.DB().Exec(`
		INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx, engine)
		VALUES(?, 1.0, 2, 'tcp', 99999, ?, ?, 1, NULL, 0, ?)`, name, now, now, engine).Error; err != nil {
		t.Fatalf("seed tunnel %s: %v", name, err)
	}
	var id int64
	if err := fx.r.DB().Raw(`SELECT id FROM tunnel WHERE name = ?`, name).Scan(&id).Error; err != nil {
		t.Fatalf("lookup tunnel %s: %v", name, err)
	}
	for i, nodeID := range entryNodeIDs {
		if err := fx.r.DB().Exec(`INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol) VALUES(?, 1, ?, NULL, 'round', ?, 'tcp')`, id, nodeID, i+1).Error; err != nil {
			t.Fatalf("seed chain_tunnel for %s: %v", name, err)
		}
	}
	return id
}

func (fx *rr4F3Fixture) addForward(t *testing.T, name string, tunnelID, nodeID int64, remoteAddr, strategy string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	fwd := model.Forward{UserID: 1, UserName: "admin", Name: name, TunnelID: tunnelID, RemoteAddr: remoteAddr, Strategy: strategy, CreatedTime: now, UpdatedTime: now, Status: 1}
	if err := fx.r.DB().Create(&fwd).Error; err != nil {
		t.Fatalf("seed forward %s: %v", name, err)
	}
	if err := fx.r.DB().Create(&model.ForwardPort{ForwardID: fwd.ID, NodeID: nodeID, Port: 20000}).Error; err != nil {
		t.Fatalf("seed forward_port %s: %v", name, err)
	}
	return fwd.ID
}

func TestRR4F3TunnelDeleteMigrationRealmGatePlan(t *testing.T) {
	fx := setupRR4F3(t)
	nodeID := fx.addNode(t, "rr4f3-node")
	sourceID := fx.addTunnel(t, "rr4f3-source", "", nodeID)
	realmID := fx.addTunnel(t, "rr4f3-realm", "realm", nodeID)

	cases := []struct {
		name       string
		remoteAddr string
		strategy   string
		wantFail   bool
	}{
		{"fifo-multi", "203.0.113.10:80,203.0.113.11:80", "fifo", true},
		{"rand-multi", "203.0.113.10:80,203.0.113.11:80", "rand", true},
		{"round-multi", "203.0.113.10:80,203.0.113.11:80", "round", false},
		{"hash-multi", "203.0.113.10:80,203.0.113.11:80", "hash", false},
		{"fifo-single", "203.0.113.10:80", "fifo", false},
	}
	for _, tc := range cases {
		fwdID := fx.addForward(t, "rr4f3-"+tc.name, sourceID, nodeID, tc.remoteAddr, tc.strategy)
		plans, failures, err := fx.h.planTunnelDeleteForwardMigrations(sourceID, realmID)
		if err != nil {
			t.Fatalf("%s: plan err: %v", tc.name, err)
		}
		// plan 按源隧道全部转发成计划，逐例只断言本例转发的结果：
		// 失败例必须出现在 failures 且点名 realm 策略原因。
		var planHit, failHit bool
		for _, p := range plans {
			if p.forward != nil && p.forward.ID == fwdID {
				planHit = true
			}
		}
		for _, f := range failures {
			if f.ID == fwdID {
				failHit = true
				if !strings.Contains(f.Reason, "realm") {
					t.Fatalf("%s: failure reason %q does not name the realm strategy gate", tc.name, f.Reason)
				}
			}
		}
		if tc.wantFail && (planHit || !failHit) {
			t.Fatalf("%s: want plan rejection, plans hit=%v failures hit=%v", tc.name, planHit, failHit)
		}
		if !tc.wantFail && (!planHit || failHit) {
			t.Fatalf("%s: want plan success, plans hit=%v failures hit=%v", tc.name, planHit, failHit)
		}
		// 清理本例转发，避免污染下一例的 plan 全集。
		if err := fx.r.DB().Exec(`DELETE FROM forward_port WHERE forward_id = ?`, fwdID).Error; err != nil {
			t.Fatal(err)
		}
		if err := fx.r.DB().Exec(`DELETE FROM forward WHERE id = ?`, fwdID).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// 两入口共用链的端到端钉：processTunnelDeleteWithForwards（单删与
// 批删循环共同入口）对 fifo 多目标迁 realm 返回逐条失败、源隧道与
// 转发原样保留，不发生迁移/删除。
func TestRR4F3TunnelDeleteMigrationRealmGateProcess(t *testing.T) {
	fx := setupRR4F3(t)
	nodeID := fx.addNode(t, "rr4f3p-node")
	sourceID := fx.addTunnel(t, "rr4f3p-source", "", nodeID)
	realmID := fx.addTunnel(t, "rr4f3p-realm", "realm", nodeID)
	fwdID := fx.addForward(t, "rr4f3p-fifo-multi", sourceID, nodeID, "203.0.113.10:80,203.0.113.11:80", "fifo")

	_, failures, err := fx.h.processTunnelDeleteWithForwards(sourceID, tunnelDeleteActionReplace, realmID)
	if err != nil {
		t.Fatalf("process err: %v", err)
	}
	if len(failures) != 1 || failures[0].ID != fwdID {
		t.Fatalf("failures = %+v, want exactly the fifo forward", failures)
	}
	if !strings.Contains(failures[0].Reason, "realm") {
		t.Fatalf("failure reason %q does not name the realm strategy gate", failures[0].Reason)
	}

	stored, err := fx.h.getForwardRecord(fwdID)
	if err != nil || stored == nil {
		t.Fatalf("forward lookup after rejected migration: %v", err)
	}
	if stored.TunnelID != sourceID {
		t.Fatalf("forward tunnel = %d, want source %d (migration must not land)", stored.TunnelID, sourceID)
	}
	if _, err := fx.h.getTunnelRecord(sourceID); err != nil {
		t.Fatalf("source tunnel must survive a fully rejected migration: %v", err)
	}
}
