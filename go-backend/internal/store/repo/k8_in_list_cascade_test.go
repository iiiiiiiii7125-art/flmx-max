package repo

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R3 K8（X01-001，吸收 X04-O3 与 X01-OPT-01）永久回归门：无界 IN
// 家族在「单实体子集合」豁免名义下的残余全集——单节点/单内核的
// 全部入站、隧道/用户级联的物化集、备份预检的整单排除集、单用户
// 订阅分配集——整体塞单个 IN ? / NOT IN ? 在超 SQL 变量上限
// （SQLite 32766 / PostgreSQL 65535）后整体失败（活验：33,000
// 入站下删节点与卸载内核均回 too many SQL variables、全数未删）。
// 本文件以 33,000 量级钉死每条修复路径：删除必须成功且级联干净、
// 预检必须在大排除集下仍精确判定、读路径必须保持全局序。

const k8Bulk = 33000 // > SQLite 变量上限 32766，旧形态必失败

func k8SeedInbounds(t *testing.T, r *Repository, baseID, nodeID, kernelID int64, n, portBase int, now int64) {
	t.Helper()
	rows := make([]model.NodeInbound, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, model.NodeInbound{
			ID: baseID + int64(i), NodeID: nodeID, KernelID: kernelID,
			Name: "ib", Protocol: "vless", Port: portBase + i, // (node,port) 部分唯一索引要求同节点内核入站端口互异
			CreatedTime: now, UpdatedTime: now,
		})
	}
	if err := r.DB().CreateInBatches(rows, 1000).Error; err != nil {
		t.Fatalf("seed inbounds: %v", err)
	}
}

// k8SeedInboundAttachments 给前 attachN 个入站各挂 1 条计数行、
// 1 条 relay 行、1 条直连授权与 1 条 relay 授权（relay 的
// ForwardID 用 forwardBase+i 保持唯一索引不冲突）。
func k8SeedInboundAttachments(t *testing.T, r *Repository, inboundBase, relayBase, forwardBase int64, attachN int, now int64) {
	t.Helper()
	counters := make([]model.NodeInboundCounter, 0, attachN)
	relays := make([]model.NodeInboundRelay, 0, attachN)
	grants := make([]model.UserInbound, 0, attachN*2)
	for i := 0; i < attachN; i++ {
		inboundID := inboundBase + int64(i)
		relayID := relayBase + int64(i)
		counters = append(counters, model.NodeInboundCounter{
			InboundID: inboundID, Protocol: "tcp", Direction: "up", UpdatedTime: now,
		})
		relays = append(relays, model.NodeInboundRelay{
			ID: relayID, InboundID: inboundID, ForwardID: forwardBase + int64(i), CreatedTime: now,
		})
		grants = append(grants,
			model.UserInbound{UserID: 8, InboundID: inboundID, RelayID: 0},
			model.UserInbound{UserID: 9, InboundID: inboundID, RelayID: relayID},
		)
	}
	if err := r.DB().CreateInBatches(counters, 1000).Error; err != nil {
		t.Fatalf("seed counters: %v", err)
	}
	if err := r.DB().CreateInBatches(relays, 1000).Error; err != nil {
		t.Fatalf("seed relays: %v", err)
	}
	if err := r.DB().CreateInBatches(grants, 1000).Error; err != nil {
		t.Fatalf("seed grants: %v", err)
	}
}

func TestK8DeleteNodeKernelBeyondSQLiteVariableLimit(t *testing.T) {
	r := openCascadeRepo(t, "k8-kernel-33k.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-40000", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, r, &model.NodeKernel{ID: 11, NodeID: 1, KernelType: "xray", CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NodeKernel{ID: 12, NodeID: 1, KernelType: "sing-box", CreatedTime: now, UpdatedTime: now})

	// 目标内核 33k 入站 + 样本附属行；对照内核仅 3 入站，必须存活。
	k8SeedInbounds(t, r, 100000, 1, 11, k8Bulk, 10000, now)
	k8SeedInboundAttachments(t, r, 100000, 500000, 600000, 64, now)
	k8SeedInbounds(t, r, 200000, 1, 12, 3, 50000, now)
	k8SeedInboundAttachments(t, r, 200000, 700000, 800000, 3, now)

	if err := r.DeleteNodeKernel(11); err != nil {
		t.Fatalf("DeleteNodeKernel with %d inbounds: %v", k8Bulk, err)
	}
	if n := countWhere(t, r, "node_inbound", "kernel_id = ?", 11); n != 0 {
		t.Fatalf("kernel 11 inbounds left: %d", n)
	}
	if n := countWhere(t, r, "node_inbound_counter", "inbound_id >= ? AND inbound_id < ?", 100000, 100000+k8Bulk); n != 0 {
		t.Fatalf("kernel 11 counters left: %d", n)
	}
	if n := countWhere(t, r, "node_inbound_relay", "inbound_id >= ? AND inbound_id < ?", 100000, 100000+k8Bulk); n != 0 {
		t.Fatalf("kernel 11 relays left: %d", n)
	}
	if n := countWhere(t, r, "user_inbound", "inbound_id >= ? AND inbound_id < ?", 100000, 100000+k8Bulk); n != 0 {
		t.Fatalf("kernel 11 inbound grants left: %d", n)
	}
	if n := countWhere(t, r, "user_inbound", "relay_id >= ? AND relay_id < ?", 500000, 500064); n != 0 {
		t.Fatalf("kernel 11 relay grants left: %d", n)
	}
	if n := countWhere(t, r, "node_kernel", "id = ?", 11); n != 0 {
		t.Fatalf("kernel row 11 must be deleted")
	}
	// 对照内核毫发无损。
	if n := countWhere(t, r, "node_inbound", "kernel_id = ?", 12); n != 3 {
		t.Fatalf("kernel 12 inbounds = %d, want 3", n)
	}
	if n := countWhere(t, r, "user_inbound", "relay_id >= ? AND relay_id < ?", 700000, 700003); n != 3 {
		t.Fatalf("kernel 12 relay grants = %d, want 3", n)
	}
	if n := countWhere(t, r, "node_kernel", "id = ?", 12); n != 1 {
		t.Fatalf("kernel row 12 must survive")
	}
}

func TestK8DeleteNodeCascadeBeyondSQLiteVariableLimit(t *testing.T) {
	r := openCascadeRepo(t, "k8-node-33k.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-40000", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, r, &model.Node{ID: 2, Name: "n2", Secret: "s", ServerIP: "10.0.0.2", Port: "10000-40000", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, r, &model.NodeKernel{ID: 11, NodeID: 1, KernelType: "xray", CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NodeKernel{ID: 12, NodeID: 2, KernelType: "xray", CreatedTime: now, UpdatedTime: now})

	k8SeedInbounds(t, r, 100000, 1, 11, k8Bulk, 10000, now)
	k8SeedInboundAttachments(t, r, 100000, 500000, 600000, 64, now)
	k8SeedInbounds(t, r, 200000, 2, 12, 2, 50000, now)

	if err := r.DeleteNodeCascade(1); err != nil {
		t.Fatalf("DeleteNodeCascade with %d inbounds: %v", k8Bulk, err)
	}
	if n := countWhere(t, r, "node_inbound", "node_id = ?", 1); n != 0 {
		t.Fatalf("node 1 inbounds left: %d", n)
	}
	if n := countWhere(t, r, "node_kernel", "node_id = ?", 1); n != 0 {
		t.Fatalf("node 1 kernels left: %d", n)
	}
	if n := countWhere(t, r, "user_inbound", "inbound_id >= ? AND inbound_id < ?", 100000, 100000+k8Bulk); n != 0 {
		t.Fatalf("node 1 grants left: %d", n)
	}
	if n := countWhere(t, r, "node_inbound_relay", "inbound_id >= ? AND inbound_id < ?", 100000, 100000+k8Bulk); n != 0 {
		t.Fatalf("node 1 relays left: %d", n)
	}
	if n := countWhere(t, r, "node", "id = ?", 1); n != 0 {
		t.Fatalf("node row 1 must be deleted")
	}
	if n := countWhere(t, r, "node_inbound", "node_id = ?", 2); n != 2 {
		t.Fatalf("node 2 inbounds = %d, want 2", n)
	}
}

func TestK8DeleteTunnelCascadeBeyondSQLiteVariableLimit(t *testing.T) {
	r := openCascadeRepo(t, "k8-tunnel-33k.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Tunnel{ID: 6, Name: "t6", Type: 1, CreatedTime: now, UpdatedTime: now})

	forwards := make([]model.Forward, 0, k8Bulk)
	for i := 0; i < k8Bulk; i++ {
		forwards = append(forwards, model.Forward{
			ID: 200000 + int64(i), UserID: 7, TunnelID: 6,
			Name: "f", Status: 1, CreatedTime: now, UpdatedTime: now,
		})
	}
	if err := r.DB().CreateInBatches(forwards, 1000).Error; err != nil {
		t.Fatalf("seed forwards: %v", err)
	}
	userTunnels := make([]model.UserTunnel, 0, k8Bulk)
	for i := 0; i < k8Bulk; i++ {
		userTunnels = append(userTunnels, model.UserTunnel{
			ID: 300000 + int64(i), UserID: 1000000 + int64(i), TunnelID: 6,
			Num: 1, ExpTime: 4102444800000, Status: 1,
		})
	}
	if err := r.DB().CreateInBatches(userTunnels, 1000).Error; err != nil {
		t.Fatalf("seed user tunnels: %v", err)
	}
	// 样本附属行：前 32 条 forward 的端口/计数/relay+授权。
	const attachN = 32
	fps := make([]model.ForwardPort, 0, attachN)
	nfts := make([]model.NftCounterState, 0, attachN)
	relays := make([]model.NodeInboundRelay, 0, attachN)
	grants := make([]model.UserInbound, 0, attachN)
	for i := 0; i < attachN; i++ {
		fwdID := 200000 + int64(i)
		relayID := 500000 + int64(i)
		fps = append(fps, model.ForwardPort{ForwardID: fwdID, NodeID: 1, Port: 20000 + i})
		nfts = append(nfts, model.NftCounterState{NodeID: 1, ForwardID: fwdID, Protocol: "tcp", Direction: "in"})
		relays = append(relays, model.NodeInboundRelay{ID: relayID, InboundID: 99, ForwardID: fwdID, CreatedTime: now})
		grants = append(grants, model.UserInbound{UserID: 9, InboundID: 99, RelayID: relayID})
	}
	for name, rows := range map[string]interface{}{
		"forward ports": fps, "nft states": nfts, "relays": relays,
		"grants": grants,
	} {
		if err := r.DB().CreateInBatches(rows, 1000).Error; err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	// pending-delete 墓碑转发（X-P1-1）：行本体与 nft 台账必须随
	// 级联存活（relay 关联除外，仍按全部 forwardIDs 清）。
	createRow(t, r, &model.Forward{ID: 900001, UserID: 7, TunnelID: 6, Name: "pending", Status: model.ForwardStatusPendingDelete, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.ForwardPort{ForwardID: 900001, NodeID: 1, Port: 21000})
	createRow(t, r, &model.NftPendingDelete{ForwardID: 900001, NodeID: 1, NextRetryAt: now})

	if err := r.DeleteTunnelCascade(6); err != nil {
		t.Fatalf("DeleteTunnelCascade with %d forwards: %v", k8Bulk, err)
	}
	if n := countWhere(t, r, "forward", "tunnel_id = ? AND status <> ?", 6, model.ForwardStatusPendingDelete); n != 0 {
		t.Fatalf("tunnel forwards left: %d", n)
	}
	if n := countWhere(t, r, "user_tunnel", "tunnel_id = ?", 6); n != 0 {
		t.Fatalf("tunnel user_tunnels left: %d", n)
	}
	if n := countWhere(t, r, "tunnel", "id = ?", 6); n != 0 {
		t.Fatalf("tunnel row 6 must be deleted")
	}
	if n := countWhere(t, r, "forward_port", "forward_id >= ? AND forward_id < ?", 200000, 200000+attachN); n != 0 {
		t.Fatalf("sample forward ports left: %d", n)
	}
	if n := countWhere(t, r, "nft_counter_state", "forward_id >= ? AND forward_id < ?", 200000, 200000+attachN); n != 0 {
		t.Fatalf("sample nft states left: %d", n)
	}
	if n := countWhere(t, r, "node_inbound_relay", "forward_id >= ? AND forward_id < ?", 200000, 200000+attachN); n != 0 {
		t.Fatalf("sample relays left: %d", n)
	}
	if n := countWhere(t, r, "user_inbound", "relay_id >= ? AND relay_id < ?", 500000, 500000+attachN); n != 0 {
		t.Fatalf("sample relay grants left: %d", n)
	}
	// 墓碑语义不因分块改变。
	if n := countWhere(t, r, "forward", "id = ?", 900001); n != 1 {
		t.Fatalf("pending-delete forward must survive cascade")
	}
	if n := countWhere(t, r, "nft_pending_delete", "forward_id = ?", 900001); n != 1 {
		t.Fatalf("pending-delete ledger must survive cascade")
	}
	if n := countWhere(t, r, "forward_port", "forward_id = ?", 900001); n != 1 {
		t.Fatalf("pending-delete forward port must survive cascade")
	}
}

func TestK8DeleteUserCascadeBeyondSQLiteVariableLimit(t *testing.T) {
	r := openCascadeRepo(t, "k8-user-33k.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.User{ID: 3, User: "owner", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.User{ID: 9, User: "other", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 5, Name: "t5", Type: 1, CreatedTime: now, UpdatedTime: now})

	forwards := make([]model.Forward, 0, k8Bulk)
	for i := 0; i < k8Bulk; i++ {
		forwards = append(forwards, model.Forward{
			ID: 400000 + int64(i), UserID: 3, TunnelID: 5,
			Name: "f", Status: 1, CreatedTime: now, UpdatedTime: now,
		})
	}
	if err := r.DB().CreateInBatches(forwards, 1000).Error; err != nil {
		t.Fatalf("seed forwards: %v", err)
	}
	const attachN = 32
	relays := make([]model.NodeInboundRelay, 0, attachN)
	grants := make([]model.UserInbound, 0, attachN)
	for i := 0; i < attachN; i++ {
		relayID := 500000 + int64(i)
		relays = append(relays, model.NodeInboundRelay{ID: relayID, InboundID: 99, ForwardID: 400000 + int64(i), CreatedTime: now})
		grants = append(grants, model.UserInbound{UserID: 9, InboundID: 99, RelayID: relayID})
	}
	if err := r.DB().CreateInBatches(relays, 1000).Error; err != nil {
		t.Fatalf("seed relays: %v", err)
	}
	if err := r.DB().CreateInBatches(grants, 1000).Error; err != nil {
		t.Fatalf("seed grants: %v", err)
	}

	if err := r.DeleteUserCascade(3); err != nil {
		t.Fatalf("DeleteUserCascade with %d forwards: %v", k8Bulk, err)
	}
	if n := countWhere(t, r, "forward", "user_id = ?", 3); n != 0 {
		t.Fatalf("user forwards left: %d", n)
	}
	if n := countWhere(t, r, "node_inbound_relay", "forward_id >= ? AND forward_id < ?", 400000, 400000+attachN); n != 0 {
		t.Fatalf("user cascade relays left: %d", n)
	}
	if n := countWhere(t, r, "user_inbound", "relay_id >= ? AND relay_id < ?", 500000, 500000+attachN); n != 0 {
		t.Fatalf("other user's relay grants left: %d", n)
	}
	if n := countWhere(t, r, "user", "id = ?", 3); n != 0 {
		t.Fatalf("user row 3 must be deleted")
	}
	if n := countWhere(t, r, "user", "id = ?", 9); n != 1 {
		t.Fatalf("user row 9 must survive")
	}
}

// k8HugeBackup 组装整单排除集（33k 入站 + 33k 转发 + 可选 33k
// 隧道）与一条 (node 1, port 8443) 的转发声明。
func k8HugeBackup(withTunnels bool) (*model.BackupData, map[string]bool) {
	backup := &model.BackupData{}
	inbounds := make([]model.InboundBackup, 0, k8Bulk)
	for i := 0; i < k8Bulk; i++ {
		inbounds = append(inbounds, model.InboundBackup{ID: int64(i + 1), NodeID: 0})
	}
	backup.Inbounds = &model.InboundsBackup{Inbounds: inbounds}
	forwards := make([]model.ForwardBackup, 0, k8Bulk+1)
	for i := 0; i < k8Bulk; i++ {
		forwards = append(forwards, model.ForwardBackup{ID: int64(500000 + i)})
	}
	claimPorts := []model.ForwardPortBackup{{NodeID: 1, Port: 8443}}
	forwards = append(forwards, model.ForwardBackup{ID: 999999, Name: "claim", ForwardPorts: &claimPorts})
	backup.Forwards = forwards
	typeSet := map[string]bool{"forwards": true, "inbounds": true}
	if withTunnels {
		tunnels := make([]model.TunnelBackup, 0, k8Bulk)
		for i := 0; i < k8Bulk; i++ {
			tunnels = append(tunnels, model.TunnelBackup{ID: int64(600000 + i)})
		}
		backup.Tunnels = tunnels
		typeSet["tunnels"] = true
	}
	return backup, typeSet
}

func TestK8BackupPrecheckHugeExclusionSets(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("forward and inbound sources", func(t *testing.T) {
		r := openCascadeRepo(t, "k8-precheck-fi.db")
		// 占用行全部落在备份排除集内（将被替换的行）→ 不算冲突。
		createRow(t, r, &model.ForwardPort{ForwardID: 500123, NodeID: 1, Port: 8443})
		createRow(t, r, &model.NodeInbound{ID: 12345, NodeID: 1, KernelID: 11, Name: "ib", Protocol: "vless", Port: 8443, CreatedTime: now, UpdatedTime: now})
		backup, typeSet := k8HugeBackup(false)
		claims := backupClaims(backup, typeSet)
		if len(claims) != 1 {
			t.Fatalf("claims = %d, want 1", len(claims))
		}
		if err := r.precheckBackupPortConflicts(backup, typeSet, claims); err != nil {
			t.Fatalf("precheck with huge exclusion sets (all occupants excluded): %v", err)
		}
		// 把被排除的入站占用换成不在备份内的真实占用（同节点同口
		// 唯一索引不允许两行并存，先删被排除行）→ 必须报冲突且指名来源。
		if err := r.DB().Where("id = ?", 12345).Delete(&model.NodeInbound{}).Error; err != nil {
			t.Fatalf("delete excluded inbound occupant: %v", err)
		}
		createRow(t, r, &model.NodeInbound{ID: 700001, NodeID: 1, KernelID: 11, Name: "ib2", Protocol: "vless", Port: 8443, CreatedTime: now, UpdatedTime: now})
		err := r.precheckBackupPortConflicts(backup, typeSet, claims)
		if err == nil || !strings.Contains(err.Error(), "已有入站") {
			t.Fatalf("precheck must report inbound conflict, got: %v", err)
		}
	})

	t.Run("chain source", func(t *testing.T) {
		r := openCascadeRepo(t, "k8-precheck-chain.db")
		createRow(t, r, &model.ChainTunnel{TunnelID: 600123, ChainType: "2", NodeID: 1, Port: sql.NullInt64{Int64: 8443, Valid: true}})
		backup, typeSet := k8HugeBackup(true)
		claims := backupClaims(backup, typeSet)
		if len(claims) != 1 {
			t.Fatalf("claims = %d, want 1", len(claims))
		}
		if err := r.precheckBackupPortConflicts(backup, typeSet, claims); err != nil {
			t.Fatalf("precheck with huge tunnel exclusion set (occupant excluded): %v", err)
		}
		// 同节点同口唯一索引不允许两条链路行并存：删被排除行后
		// 换入不在备份内的链路占用 → 必须报冲突。
		if err := r.DB().Where("tunnel_id = ?", 600123).Delete(&model.ChainTunnel{}).Error; err != nil {
			t.Fatalf("delete excluded chain occupant: %v", err)
		}
		createRow(t, r, &model.ChainTunnel{TunnelID: 800001, ChainType: "2", NodeID: 1, Port: sql.NullInt64{Int64: 8443, Valid: true}})
		err := r.precheckBackupPortConflicts(backup, typeSet, claims)
		if err == nil || !strings.Contains(err.Error(), "已有隧道链路") {
			t.Fatalf("precheck must report chain conflict, got: %v", err)
		}
	})
}

func TestK8OccupancyHelpersHugeExclusionSets(t *testing.T) {
	r := openCascadeRepo(t, "k8-occupancy-huge.db")
	now := time.Now().UnixMilli()
	// 节点 1 上：入站 12345 占 8443（在排除集内）、入站 700001 占
	// 9443（不在集内）；转发 500123 占 8444（在集内）。
	createRow(t, r, &model.NodeInbound{ID: 12345, NodeID: 1, KernelID: 11, Name: "a", Protocol: "vless", Port: 8443, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NodeInbound{ID: 700001, NodeID: 1, KernelID: 11, Name: "b", Protocol: "vless", Port: 9443, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.ForwardPort{ForwardID: 500123, NodeID: 1, Port: 8444})

	inboundExcl := make([]int64, 0, k8Bulk)
	for i := 0; i < k8Bulk; i++ {
		inboundExcl = append(inboundExcl, int64(i+1))
	}
	forwardExcl := make([]int64, 0, k8Bulk)
	for i := 0; i < k8Bulk; i++ {
		forwardExcl = append(forwardExcl, int64(500000+i))
	}
	excl := PortExclusions{InboundIDs: inboundExcl, ForwardIDs: forwardExcl}

	used, err := r.UsedPortsOnNodeTx(r.DB(), 1, excl)
	if err != nil {
		t.Fatalf("UsedPortsOnNodeTx with huge exclusions: %v", err)
	}
	if _, ok := used[8443]; ok {
		t.Fatalf("port 8443 must be excluded from used set")
	}
	if _, ok := used[8444]; ok {
		t.Fatalf("port 8444 must be excluded from used set")
	}
	if _, ok := used[9443]; !ok {
		t.Fatalf("port 9443 must remain in used set")
	}

	occ, err := r.NodePortRangeOccupancyTx(r.DB(), 1, 8000, 8999, excl)
	if err != nil {
		t.Fatalf("NodePortRangeOccupancyTx with huge exclusions: %v", err)
	}
	if len(occ.Sources) != 0 {
		t.Fatalf("range 8000-8999 must be fully excluded, got sources %v", occ.Sources)
	}
	occ, err = r.NodePortRangeOccupancyTx(r.DB(), 1, 9000, 9999, excl)
	if err != nil {
		t.Fatalf("NodePortRangeOccupancyTx (occupied range): %v", err)
	}
	if !occ.Sources[PortSourceInbound] {
		t.Fatalf("range 9000-9999 must report inbound occupancy, got %v", occ.Sources)
	}
}

func TestK8ListUserSubscriptionsBeyondSQLiteVariableLimit(t *testing.T) {
	r := openCascadeRepo(t, "k8-user-subs-33k.db")
	now := time.Now().UnixMilli()
	subs := make([]model.Subscription, 0, k8Bulk)
	assignments := make([]model.UserSubscription, 0, k8Bulk)
	for i := 0; i < k8Bulk; i++ {
		id := int64(1000 + i)
		subs = append(subs, model.Subscription{
			ID: id, Name: "s", Kind: "own",
			Token: fmt.Sprintf("k8tok%06d", i),
			Inx:   i % 5, CreatedTime: now,
		})
		assignments = append(assignments, model.UserSubscription{
			UserID: 7, SubscriptionID: id, CreatedTime: now,
		})
	}
	if err := r.DB().CreateInBatches(subs, 1000).Error; err != nil {
		t.Fatalf("seed subscriptions: %v", err)
	}
	if err := r.DB().CreateInBatches(assignments, 1000).Error; err != nil {
		t.Fatalf("seed assignments: %v", err)
	}

	out, err := r.ListUserSubscriptions(7)
	if err != nil {
		t.Fatalf("ListUserSubscriptions with %d assignments: %v", k8Bulk, err)
	}
	if len(out) != k8Bulk {
		t.Fatalf("subscriptions = %d, want %d", len(out), k8Bulk)
	}
	// 全局序 (inx, id) 跨块不得乱序；首元素应为 inx=0 组最小 id。
	if out[0].Inx != 0 || out[0].ID != 1000 {
		t.Fatalf("first subscription = {inx %d, id %d}, want {0, 1000}", out[0].Inx, out[0].ID)
	}
	for i := 1; i < len(out); i++ {
		prev, cur := out[i-1], out[i]
		if cur.Inx < prev.Inx || (cur.Inx == prev.Inx && cur.ID <= prev.ID) {
			t.Fatalf("order broken at %d: {inx %d, id %d} after {inx %d, id %d}",
				i, cur.Inx, cur.ID, prev.Inx, prev.ID)
		}
	}
}
