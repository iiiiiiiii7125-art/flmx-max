package repo

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// X-P3-17 (L-04-3): 批量换隧道的逐条 DB 写收敛为单事务——换隧道与换
// 端口同 tx（锁内预取占用复检），端口替换步失败时隧道归属不得先行
// 提交（旧实现 UpdateForwardTunnel 与 ReplaceForwardPorts 两次独立
// 提交，中间态靠补偿回滚兜底）。
func TestR1B8_XP317_ChangeForwardTunnelAndPortsAtomic(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "xp317.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now().UnixMilli()
	node := &model.Node{Name: "n1", Secret: "s-xp317", Status: 1}
	if err := r.DB().Create(node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	mkTunnel := func(name string) *model.Tunnel {
		tw := &model.Tunnel{Name: name, TrafficRatio: 1, Type: 1, Protocol: "tls", Status: 1, CreatedTime: now, UpdatedTime: now}
		if err := r.DB().Create(tw).Error; err != nil {
			t.Fatalf("seed tunnel: %v", err)
		}
		return tw
	}
	oldTunnel := mkTunnel("old-xp317")
	newTunnel := mkTunnel("new-xp317")
	fwd := &model.Forward{UserID: 1, UserName: "admin", Name: "fwd-xp317", TunnelID: oldTunnel.ID, RemoteAddr: "203.0.113.9:443", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(fwd).Error; err != nil {
		t.Fatalf("seed forward: %v", err)
	}
	if err := r.DB().Create(&model.ForwardPort{ForwardID: fwd.ID, NodeID: node.ID, Port: 32001}).Error; err != nil {
		t.Fatalf("seed port: %v", err)
	}
	entries := []struct {
		NodeID int64
		Port   int
		InIP   string
	}{{NodeID: node.ID, Port: 32002, InIP: ""}}

	assertState := func(wantTunnel int64, wantPort int, stage string) {
		t.Helper()
		var got model.Forward
		if err := r.DB().First(&got, fwd.ID).Error; err != nil {
			t.Fatalf("%s: reload forward: %v", stage, err)
		}
		if got.TunnelID != wantTunnel {
			t.Fatalf("%s: tunnel_id=%d, want %d（换隧道已先行提交，单 tx 收敛未生效）", stage, got.TunnelID, wantTunnel)
		}
		ports, err := r.ListForwardPorts(fwd.ID)
		if err != nil {
			t.Fatalf("%s: list ports: %v", stage, err)
		}
		if len(ports) != 1 || ports[0].Port != wantPort {
			t.Fatalf("%s: ports=%v, want single %d", stage, ports, wantPort)
		}
	}

	// 1) 端口插入步注入失败 → 整条回滚，隧道与端口均不变。
	if err := r.DB().Exec(`CREATE TRIGGER xp317_fail_port_insert BEFORE INSERT ON forward_port BEGIN SELECT RAISE(ABORT,'xp317 boom'); END`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if err := r.ChangeForwardTunnelAndPorts(fwd.ID, newTunnel.ID, "203.0.113.9:443", entries, now); err == nil {
		t.Fatalf("注入失败时期望报错")
	}
	assertState(oldTunnel.ID, 32001, "insert-failure")
	if err := r.DB().Exec(`DROP TRIGGER xp317_fail_port_insert`).Error; err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	// 2) 目标口被他人占用（锁内/ tx 内复检）→ 报错且不变。
	blocker := &model.Forward{UserID: 1, UserName: "admin", Name: "fwd-xp317-b", TunnelID: oldTunnel.ID, RemoteAddr: "203.0.113.10:443", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(blocker).Error; err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	if err := r.DB().Create(&model.ForwardPort{ForwardID: blocker.ID, NodeID: node.ID, Port: 32002}).Error; err != nil {
		t.Fatalf("seed blocker port: %v", err)
	}
	if err := r.ChangeForwardTunnelAndPorts(fwd.ID, newTunnel.ID, "203.0.113.9:443", entries, now); err == nil {
		t.Fatalf("目标口被占用时期望报错")
	}
	assertState(oldTunnel.ID, 32001, "occupied")
	if err := r.DB().Where("forward_id = ?", blocker.ID).Delete(&model.ForwardPort{}).Error; err != nil {
		t.Fatalf("clear blocker: %v", err)
	}

	// 3) 正常路径：隧道与端口同 tx 一并切换。
	if err := r.ChangeForwardTunnelAndPorts(fwd.ID, newTunnel.ID, "203.0.113.9:443", entries, now); err != nil {
		t.Fatalf("change: %v", err)
	}
	assertState(newTunnel.ID, 32002, "success")

	// 4) in_ip 落库口径与 ReplaceForwardPorts 一致（空串 → NULL）。
	var raw sql.NullString
	if err := r.DB().Raw(`SELECT in_ip FROM forward_port WHERE forward_id = ?`, fwd.ID).Scan(&raw).Error; err != nil {
		t.Fatalf("scan in_ip: %v", err)
	}
	if raw.Valid {
		t.Fatalf("in_ip 应为 NULL, got %q", raw.String)
	}
}
