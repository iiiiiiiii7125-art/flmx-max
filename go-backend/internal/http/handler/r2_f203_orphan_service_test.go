package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// R2-F203 回归：cleanOrphanedServices 的孤儿谓词此前只认隧道整
// 行消失——隧道仍在、节点已退出链行时，失势的 tunnel_<id> 及其
// 协议变体服务永不被对账删除，旧入口继续监听旧端口分流。现谓
// 词扩为「隧道不存在，或节点已不在该隧道运行时集（链行
// ChainType 2/3）」；查证失败按存疑保留；删除失败点名进日志。
func seedR2F203Tunnel(t *testing.T, a *cleanupAgent, chainRows map[int64]string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	tx := a.h.repo.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	tunnelID, err := a.h.repo.CreateTunnelTx(tx, "r2-f203-tunnel", 1, 1, 1, now, 1, nil, 1, "", "", 0)
	if err != nil {
		_ = tx.Rollback().Error
		t.Fatalf("create tunnel: %v", err)
	}
	for nodeID, chainType := range chainRows {
		if err := a.h.repo.CreateChainTunnelTx(tx, tunnelID, chainType, nodeID, sql.NullInt64{}, "", 1, "tls", ""); err != nil {
			_ = tx.Rollback().Error
			t.Fatalf("create chain row node=%d type=%s: %v", nodeID, chainType, err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		_ = tx.Rollback().Error
		t.Fatalf("commit tx: %v", err)
	}
	return tunnelID
}

func r2f203DeletedServices(t *testing.T, a *cleanupAgent) []string {
	t.Helper()
	var names []string
	for _, cmd := range a.commandsOfType("DeleteService") {
		var payload map[string]interface{}
		if err := json.Unmarshal(cmd.Data, &payload); err != nil {
			t.Fatalf("decode DeleteService payload %s: %v", string(cmd.Data), err)
		}
		services, _ := payload["services"].([]interface{})
		for _, s := range services {
			if name, ok := s.(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

func r2f203HasService(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestR2F203RemovedNodeTunnelServiceDeleted(t *testing.T) {
	a := newCleanupAgent(t)
	// 隧道链行只剩节点 2（ChainType 2）：节点 1 已退出运行时集。
	tunnelID := seedR2F203Tunnel(t, a, map[int64]string{2: "2"})
	a.h.cleanOrphanedServices(1, []namedConfigItem{
		{Name: fmt.Sprintf("tunnel_%d", tunnelID)},
		{Name: fmt.Sprintf("%d_tls", tunnelID)},
		{Name: fmt.Sprintf("%d_tcp", tunnelID)},
	})
	a.probe(t)
	names := r2f203DeletedServices(t, a)
	for _, want := range []string{
		fmt.Sprintf("tunnel_%d", tunnelID),
		fmt.Sprintf("%d_tls", tunnelID),
		fmt.Sprintf("%d_tcp", tunnelID),
	} {
		if !r2f203HasService(names, want) {
			t.Fatalf("removed node must lose orphaned service %q, deleted=%v", want, names)
		}
	}
}

func TestR2F203ServingNodeTunnelServiceKept(t *testing.T) {
	a := newCleanupAgent(t)
	// 节点 1 仍在链行（ChainType 2）：在役服务不得删。
	tunnelID := seedR2F203Tunnel(t, a, map[int64]string{1: "2", 2: "3"})
	a.h.cleanOrphanedServices(1, []namedConfigItem{
		{Name: fmt.Sprintf("tunnel_%d", tunnelID)},
		{Name: fmt.Sprintf("%d_tls", tunnelID)},
	})
	a.probe(t)
	if names := r2f203DeletedServices(t, a); len(names) != 0 {
		t.Fatalf("serving node must keep its tunnel services, deleted=%v", names)
	}
}

func TestR2F203EntryOnlyNodeTunnelServiceDeleted(t *testing.T) {
	a := newCleanupAgent(t)
	// 节点 1 仅为入口（ChainType 1）：入口行要链、不要服务——
	// tunnel_<id> 服务仍是孤儿，必须删。
	tunnelID := seedR2F203Tunnel(t, a, map[int64]string{1: "1", 2: "2"})
	a.h.cleanOrphanedServices(1, []namedConfigItem{
		{Name: fmt.Sprintf("tunnel_%d", tunnelID)},
	})
	a.probe(t)
	names := r2f203DeletedServices(t, a)
	if !r2f203HasService(names, fmt.Sprintf("tunnel_%d", tunnelID)) {
		t.Fatalf("entry-only node must lose the tunnel service, deleted=%v", names)
	}
}

func TestR2F203GoneTunnelServiceDeleted(t *testing.T) {
	a := newCleanupAgent(t)
	// 隧道整行已删：旧行为保留，服务删。
	a.h.cleanOrphanedServices(1, []namedConfigItem{
		{Name: "tunnel_999"},
		{Name: "999_tls"},
	})
	a.probe(t)
	names := r2f203DeletedServices(t, a)
	for _, want := range []string{"tunnel_999", "999_tls"} {
		if !r2f203HasService(names, want) {
			t.Fatalf("gone tunnel must lose service %q, deleted=%v", want, names)
		}
	}
}
