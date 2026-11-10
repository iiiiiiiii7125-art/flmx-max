package contract_test

// AUDIT-R3 修复波·包 C（R3-GEN-01～04）合同格：W2 泛化时被吞的
// 定型校验/行动指引文案经守卫/漏斗分流恢复后，逐点断言回执原文
// 与码值。触发输入全部走真实 HTTP 入口 + 种子数据构造。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/store/repo"
)

// r3genNode seeds a status=1 node (tunnel create/update paths reject
// offline nodes in prepare, so GEN-03/GEN-02④ need online rows).
func r3genNode(t *testing.T, r *repo.Repository, name, ip, portRange string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, '', ?, '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)
	`, name, name+"-secret", ip, ip, portRange, now, now).Error; err != nil {
		t.Fatalf("insert node %s: %v", name, err)
	}
	return mustLastInsertID(t, r, name)
}

func r3genTunnel(t *testing.T, r *repo.Repository, name string, typ int, engine string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx, engine)
		VALUES(?, 1.0, ?, 'tls', 99999, ?, ?, 1, NULL, 0, ?)`, name, typ, now, now, engine).Error; err != nil {
		t.Fatalf("insert tunnel %s: %v", name, err)
	}
	return mustLastInsertID(t, r, name)
}

func r3genChainEntry(t *testing.T, r *repo.Repository, tunnelID, nodeID int64, inx int) {
	t.Helper()
	if err := r.DB().Exec(`INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol) VALUES(?, 1, ?, NULL, 'round', ?, 'tls')`, tunnelID, nodeID, inx).Error; err != nil {
		t.Fatalf("insert chain entry: %v", err)
	}
}

func r3genForward(t *testing.T, r *repo.Repository, userID, tunnelID int64, name string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO forward(user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
		VALUES(?, 'gen-user', ?, ?, '203.0.113.10:443', 'fifo', 0, 0, ?, ?, 1, 0)`, userID, name, tunnelID, now, now).Error; err != nil {
		t.Fatalf("insert forward %s: %v", name, err)
	}
	return mustLastInsertID(t, r, name)
}

func r3genUser(t *testing.T, r *repo.Repository, id int64, name string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status)
		VALUES(?, ?, 'x', 1, 0, 0, 0, 0, 0, 0, ?, ?, 1)`, id, name, now, now).Error; err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
}

// R3-GEN-01：中转创建的端口规格/占用定型校验原文回（基线
// ErrDefault 口径），不再被泛化为 Internal error。
func TestR3Gen01RelayCreateTypedValidationContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	landing := seedKernelNode(t, r, "gen01-landing")
	relayNode := seedKernelNode(t, r, "gen01-relay")
	inboundID := seedInboundOnNode(t, router, r, admin, landing,
		`{"nodeId":`+jsonInt64(landing)+`,"kernelType":"xray","name":"gen01-pub","protocol":"vless","port":443,"security":{"type":"reality"}}`)
	tunnelID, _ := seedRelayForward(t, r, 1, relayNode, 0, 0, 0)

	// 规格外显式口（节点范围 30000-30010，给 30011）：定型文案回。
	out := kernelContractPost(t, router, admin, "/api/v1/inbound/relay/create",
		`{"inboundId":`+jsonInt64(inboundID)+`,"tunnelId":`+jsonInt64(tunnelID)+`,"inPort":30011}`)
	if out.Code != -1 || !strings.Contains(out.Msg, "不在节点") || !strings.Contains(out.Msg, "允许范围") {
		t.Fatalf("GEN-01 out-of-spec port must return the typed spec message, got code=%d msg=%q", out.Code, out.Msg)
	}

	// 已占用口（同节点同口已有转发入口）：占用定型文案回。
	occupier := r3genForward(t, r, 1, tunnelID, "gen01-occupier")
	if err := r.DB().Exec(`INSERT INTO forward_port(forward_id, node_id, port) VALUES(?, ?, 30005)`, occupier, relayNode).Error; err != nil {
		t.Fatal(err)
	}
	out = kernelContractPost(t, router, admin, "/api/v1/inbound/relay/create",
		`{"inboundId":`+jsonInt64(inboundID)+`,"tunnelId":`+jsonInt64(tunnelID)+`,"inPort":30005}`)
	if out.Code != -1 || !strings.Contains(out.Msg, "已被") || !strings.Contains(out.Msg, "占用") {
		t.Fatalf("GEN-01 occupied port must return the typed conflict message, got code=%d msg=%q", out.Code, out.Msg)
	}

	// 两格被拒后不得留下中转转发（仅占用者一行）。
	if got := mustQueryInt(t, r, `SELECT COUNT(*) FROM forward`); got != 1 {
		t.Fatalf("rejected relay creates must not leave forwards behind, forward rows=%d", got)
	}
}

// R3-GEN-02①：backupImport 的备份校验错（未知段类型／管理员不变
// 式）以基线「导入失败: %v」＋码 -2 原文回，不再泛化。
func TestR3Gen02BackupImportValidationContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	_ = r

	out := requestContractEnvelope(t, router, admin, "/api/v1/backup/import",
		json.RawMessage(`{"types":["bogus-type"],"version":"1.0"}`))
	if out.Code != -2 || !strings.Contains(out.Msg, "导入失败") || !strings.Contains(out.Msg, "未知备份类型") {
		t.Fatalf("GEN-02① unknown backup type must be named with the 导入失败 prefix, got code=%d msg=%q", out.Code, out.Msg)
	}

	// 管理员不变式：users 段把 id=1 写成非管理员 → 整单拒绝文案回。
	out = requestContractEnvelope(t, router, admin, "/api/v1/backup/import",
		json.RawMessage(`{"types":["users"],"version":"1.0","users":[{"id":1,"user":"admin@user","pwd":"$2a$10$abcdefghijklmnopqrstuuVzvFEqB7ZkLmNoPqRsTuVwXyZ0123456789ab","roleId":1,"status":1,"num":0,"createdTime":1}]}`))
	if out.Code != -2 || !strings.Contains(out.Msg, "导入失败") || !strings.Contains(out.Msg, "备份恢复被拒绝") {
		t.Fatalf("GEN-02① admin-invariant rejection must be named with the 导入失败 prefix, got code=%d msg=%q", out.Code, out.Msg)
	}
	// 拒绝后现行管理员不得被改动（事务回滚）。
	if got := mustQueryInt(t, r, `SELECT role_id FROM user WHERE id = 1`); got != 0 {
		t.Fatalf("rejected import must not demote the live admin, role_id=%d", got)
	}
}

// R3-GEN-02②：中转删除撞 mixed 状态非强制删除时，「可强制删除」
// 行动出路随定型文案回（基线 ErrDefault 口径），删除被中止。
func TestR3Gen02RelayDeleteForceHintContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	landing := seedKernelNode(t, r, "gen02b-landing")
	relayNode := seedKernelNode(t, r, "gen02b-relay")
	extraNode := seedKernelNode(t, r, "gen02b-extra")
	if err := r.DB().Exec(`UPDATE node SET forward_mode = 'nftables' WHERE id = ?`, relayNode).Error; err != nil {
		t.Fatal(err)
	}
	inboundID := seedInboundOnNode(t, router, r, admin, landing,
		`{"nodeId":`+jsonInt64(landing)+`,"kernelType":"xray","name":"gen02b-pub","protocol":"vless","port":443,"security":{"type":"reality"}}`)
	tunnelID, _ := seedRelayForward(t, r, 1, relayNode, 0, 30007, inboundID)
	relayID := mustQueryInt64(t, r, `SELECT id FROM node_inbound_relay WHERE inbound_id = ?`, inboundID)
	// 第二入口 → 隧道成 mixed 状态（nft 节点 + 多入口）。
	r3genChainEntry(t, r, tunnelID, extraNode, 2)

	out := kernelContractPost(t, router, admin, "/api/v1/inbound/relay/delete",
		`{"id":`+jsonInt64(relayID)+`,"force":false}`)
	if out.Code != -1 || !strings.Contains(out.Msg, "删除中转转发失败") || !strings.Contains(out.Msg, "可强制删除") {
		t.Fatalf("GEN-02② non-force relay delete must carry the force-delete hint, got code=%d msg=%q", out.Code, out.Msg)
	}
	if !strings.Contains(out.Msg, "请先编辑隧道去掉多余入口") {
		t.Fatalf("GEN-02② receipt must embed the mixed-state reason, got %q", out.Msg)
	}
	if got := mustQueryInt(t, r, `SELECT COUNT(*) FROM node_inbound_relay WHERE id = ?`, relayID); got != 1 {
		t.Fatal("rejected relay delete must keep the relay row")
	}
}

// R3-GEN-02③：userUpdate 改最大连接数后下发失败，基线定型前缀
// 「最大连接数下发失败: …」恢复（触发源为无入口端口转发的定型
// 同步错「转发入口端口不存在」）。
func TestR3Gen02UserUpdateMaxConnPrefixContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	r3genUser(t, r, 77, "gen02c-user")
	tunnelID := r3genTunnel(t, r, "gen02c-tunnel", 1, "")
	// 无 forward_port 行 → 同步必撞定型错「转发入口端口不存在」。
	r3genForward(t, r, 77, tunnelID, "gen02c-fwd")

	out := requestContractEnvelope(t, router, admin, "/api/v1/user/update",
		map[string]interface{}{"id": 77, "user": "gen02c-user", "maxConn": 7})
	if out.Code != -1 || !strings.Contains(out.Msg, "最大连接数下发失败") || !strings.Contains(out.Msg, "转发入口端口不存在") {
		t.Fatalf("GEN-02③ max-conn sync failure must carry the typed prefix and reason, got code=%d msg=%q", out.Code, out.Msg)
	}
}

// R3-GEN-02④：tunnelUpdate（引擎未变、realm）同步失败细节恢复
// 原文回——哪节点 realm 下发失败须在回执点名，不再仅日志可见。
// 触发：入口节点名下 realm 转发的目标地址为空集 → 渲染期定型
// 失败（转发 %d 缺少监听或目标地址），apply 在触碰节点前返回、
// 不被离线警告分支吞掉，syncRealmNodes 以节点名包装上抛。
func TestR3Gen02TunnelUpdateRealmSyncDetailContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	node := r3genNode(t, r, "gen02d-node", "10.60.0.1", "30000-30010")
	tunnelID := r3genTunnel(t, r, "gen02d-tunnel", 1, "realm")
	r3genChainEntry(t, r, tunnelID, node, 1)
	fwd := r3genForward(t, r, 1, tunnelID, "gen02d-fwd")
	if err := r.DB().Exec(`UPDATE forward SET remote_addr = '' WHERE id = ?`, fwd).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`INSERT INTO forward_port(forward_id, node_id, port) VALUES(?, ?, 30001)`, fwd, node).Error; err != nil {
		t.Fatal(err)
	}

	out := requestContractEnvelope(t, router, admin, "/api/v1/tunnel/update",
		json.RawMessage(`{"id":`+jsonInt64(tunnelID)+`,"name":"gen02d-tunnel","type":1,"flow":1,"status":1,"engine":"realm","inNodeId":[{"nodeId":`+jsonInt64(node)+`,"protocol":"tls"}]}`))
	if out.Code != -1 || !strings.Contains(out.Msg, "gen02d-node") || !strings.Contains(out.Msg, "realm 下发失败") {
		t.Fatalf("GEN-02④ realm sync failure must name the node and the failure, got code=%d msg=%q", out.Code, out.Msg)
	}
	if !strings.Contains(out.Msg, "缺少监听或目标地址") {
		t.Fatalf("GEN-02④ receipt must carry the render failure detail, got %q", out.Msg)
	}
}

// R3-GEN-03：tunnelCreate 三点定型恢复——realm 仅支持端口转发、
// nftables 缺 SSH 配置、realm 撞 nftables 入口。
func TestR3Gen03TunnelCreateTypedContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	entry := r3genNode(t, r, "gen03-entry", "10.40.0.1", "30000-30010")
	exit := r3genNode(t, r, "gen03-exit", "10.40.0.2", "32000-32010")

	create := func(payload string) (int, string) {
		out := requestContractEnvelope(t, router, admin, "/api/v1/tunnel/create", json.RawMessage(payload))
		return out.Code, out.Msg
	}

	// realm + type-2：realm 三定型之一（验证段 validateRealmTunnelTx）。
	code, msg := create(`{"name":"gen03-realm-t2","type":2,"flow":1,"status":1,"engine":"realm",` +
		`"inNodeId":[{"nodeId":` + jsonInt64(entry) + `,"protocol":"tls"}],` +
		`"outNodeId":[{"nodeId":` + jsonInt64(exit) + `,"protocol":"tls","port":32001}]}`)
	if code != -1 || !strings.Contains(msg, "realm 引擎仅支持端口转发隧道") {
		t.Fatalf("GEN-03 realm type-2 must return the typed realm message, got code=%d msg=%q", code, msg)
	}

	// nftables 入口缺 SSH 配置：验证段 collect 定型。
	nftNode := r3genNode(t, r, "gen03-nft", "10.40.0.3", "30000-30010")
	if err := r.DB().Exec(`UPDATE node SET forward_mode = 'nftables' WHERE id = ?`, nftNode).Error; err != nil {
		t.Fatal(err)
	}
	code, msg = create(`{"name":"gen03-nft-nocfg","type":1,"flow":1,"status":1,` +
		`"inNodeId":[{"nodeId":` + jsonInt64(nftNode) + `,"protocol":"tls"}]}`)
	if code != -1 || !strings.Contains(msg, "nftables 节点缺少 SSH 配置") {
		t.Fatalf("GEN-03 nft entry without SSH config must return the typed collect message, got code=%d msg=%q", code, msg)
	}

	// realm + nftables 入口（SSH 配置齐备、collect 放行）：realm 定型。
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node_ssh_config(node_id, host, port, username, auth_type, sudo_mode, host_key_fingerprint, created_time, updated_time)
		VALUES(?, '192.0.2.1', 22, 'root', 'password', 'none', '', ?, ?)`, nftNode, now, now).Error; err != nil {
		t.Fatal(err)
	}
	code, msg = create(`{"name":"gen03-realm-nft","type":1,"flow":1,"status":1,"engine":"realm",` +
		`"inNodeId":[{"nodeId":` + jsonInt64(nftNode) + `,"protocol":"tls"}]}`)
	if code != -1 || !strings.Contains(msg, "nftables 模式，不能使用 realm 引擎") {
		t.Fatalf("GEN-03 realm over nft entry must return the typed realm message, got code=%d msg=%q", code, msg)
	}

	// 三格被拒后不得留下隧道行。
	if got := mustQueryInt(t, r, `SELECT COUNT(*) FROM tunnel`); got != 0 {
		t.Fatalf("rejected tunnel creates must not leave tunnels behind, rows=%d", got)
	}
}

// R3-GEN-04：删隧道/删用户撞 mixed 状态时，披露漏斗原样回定型
// 拒绝文案（码 -2，与漏斗披露支同口径），删除被中止。
func TestR3Gen04DeleteMixedNftContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)

	seedMixed := func(name string, ownerID int64) int64 {
		nftNode := r3genNode(t, r, name+"-nft", "10.50.0.1", "30000-30010")
		plainNode := r3genNode(t, r, name+"-plain", "10.50.0.2", "30000-30010")
		if err := r.DB().Exec(`UPDATE node SET forward_mode = 'nftables' WHERE id = ?`, nftNode).Error; err != nil {
			t.Fatal(err)
		}
		tunnelID := r3genTunnel(t, r, name+"-tunnel", 1, "")
		r3genChainEntry(t, r, tunnelID, nftNode, 1)
		r3genChainEntry(t, r, tunnelID, plainNode, 2)
		r3genForward(t, r, ownerID, tunnelID, name+"-fwd")
		return tunnelID
	}

	// 删隧道面。
	tunnelID := seedMixed("gen04a", 1)
	out := requestContractEnvelope(t, router, admin, "/api/v1/tunnel/delete",
		map[string]interface{}{"id": tunnelID})
	if out.Code != -2 || !strings.Contains(out.Msg, "请先编辑隧道去掉多余入口") {
		t.Fatalf("GEN-04 tunnel delete over mixed state must return the typed rejection, got code=%d msg=%q", out.Code, out.Msg)
	}
	if got := mustQueryInt(t, r, `SELECT COUNT(*) FROM tunnel WHERE id = ?`, tunnelID); got != 1 {
		t.Fatal("rejected tunnel delete must keep the tunnel row")
	}

	// 删用户面（转发归属该用户、所在隧道为 mixed 状态）。
	r3genUser(t, r, 88, "gen04-user")
	seedMixed("gen04b", 88)
	out = requestContractEnvelope(t, router, admin, "/api/v1/user/delete",
		map[string]interface{}{"id": 88})
	if out.Code != -2 || !strings.Contains(out.Msg, "请先编辑隧道去掉多余入口") {
		t.Fatalf("GEN-04 user delete over mixed state must return the typed rejection, got code=%d msg=%q", out.Code, out.Msg)
	}
	if got := mustQueryInt(t, r, `SELECT COUNT(*) FROM user WHERE id = 88`); got != 1 {
		t.Fatal("rejected user delete must keep the user row")
	}
}
