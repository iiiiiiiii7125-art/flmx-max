package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// W3 修复波（2.1.2 审计路 3）handler 面契约测试：同步去重谓词、导入
// 身份键、撤销分配回执、中转创建去重闸逐条钉住。

// I-003：同名不同址条目必须全部同步进来（旧实现按名去重、第二条起
// 整批吞掉），同名行经后缀消解；真重复（身份全等）仍只跳过一次。
func TestW3SyncSameNameDistinctAddressesBothImported(t *testing.T) {
	_, h, src, _ := integrityGateSetup(t)
	body := strings.Join([]string{
		"trojan://pass-a@203.0.113.60:443#dup-name",
		"trojan://pass-b@203.0.113.61:443#dup-name",
		"trojan://pass-a@203.0.113.60:443#dup-name", // exact duplicate of line 1
	}, "\n")
	res, err := h.syncRemoteSource(context.Background(), src, body)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Added != 2 {
		t.Fatalf("added = %d, want 2 (same-name distinct-address entries are distinct nodes)", res.Added)
	}
	notices := 0
	for _, e := range res.Errors {
		if strings.Contains(e, "重复条目") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("duplicate notices = %d, want exactly 1 (the true duplicate): %v", notices, res.Errors)
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, row := range rows {
		if row.SourceID == src.ID && strings.HasPrefix(row.Listen, "203.0.113.6") {
			names[row.Name] = true
		}
	}
	if !names["dup-name"] || !names["dup-name (2)"] {
		t.Fatalf("want rows named dup-name and dup-name (2), got %v", names)
	}
}

// I-003（匹配面）：服务商改名后，同一身份的条目必须经身份键更新原行
// （改名落到原行），不得新建第二行或漂移到同名行。
func TestW3SyncRenameMatchesByIdentity(t *testing.T) {
	_, h, src, _ := integrityGateSetup(t)
	body := strings.Join([]string{
		"trojan://pass-a@203.0.113.60:443#old-name",
		"trojan://pass-b@203.0.113.61:443#old-name",
	}, "\n")
	if _, err := h.syncRemoteSource(context.Background(), src, body); err != nil {
		t.Fatalf("sync 1: %v", err)
	}
	// Round 2: the .60 entry is renamed by the provider; identity is
	// unchanged, so the rename must land on the .60 row.
	body = strings.Join([]string{
		"trojan://pass-a@203.0.113.60:443#new-name",
		"trojan://pass-b@203.0.113.61:443#old-name",
	}, "\n")
	res, err := h.syncRemoteSource(context.Background(), src, body)
	if err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if res.Added != 0 {
		t.Fatalf("round 2 added = %d, want 0 (rename is an update, not a create)", res.Added)
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	byListen := map[string]string{}
	for _, row := range rows {
		if row.SourceID == src.ID && strings.HasPrefix(row.Listen, "203.0.113.6") {
			byListen[row.Listen] = row.Name
		}
	}
	if byListen["203.0.113.60"] != "new-name" || byListen["203.0.113.61"] != "old-name" {
		t.Fatalf("rename landed wrong: %v", byListen)
	}
}

// I-004：导入去重身份键含形态——同址同凭据、仅传输不同是两个节点；
// 完全相同才是重复。
func TestW3ImportDedupeIncludesShape(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/w3_import.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	importLinks := func(links string) externalImportResult {
		req := httptest.NewRequest("POST", "/api/v1/inbound/import",
			strings.NewReader(fmt.Sprintf(`{"links":%q}`, links)))
		rec := httptest.NewRecorder()
		h.inboundImport(rec, req)
		var env struct {
			Code int                  `json:"code"`
			Data externalImportResult `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode import response: %v (%s)", err, rec.Body.String())
		}
		return env.Data
	}
	uuid := "9d3b0c6a-0000-4000-8000-000000000001"
	ws := "vless://" + uuid + "@203.0.113.70:443?type=ws&path=%2Fws&security=tls&sni=a.example.com#shape-ws"
	tcp := "vless://" + uuid + "@203.0.113.70:443?type=tcp&security=tls&sni=a.example.com#shape-tcp"
	if res := importLinks(ws); len(res.Created) != 1 {
		t.Fatalf("ws import: %+v", res)
	}
	if res := importLinks(tcp); len(res.Created) != 1 {
		t.Fatalf("tcp import must not be deduped against the ws entry: %+v", res)
	}
	res := importLinks(ws)
	if len(res.Created) != 0 || len(res.Errors) == 0 || !strings.Contains(res.Errors[0], "已经导入过") {
		t.Fatalf("exact re-import must be reported as duplicate: %+v", res)
	}
}

// S-001：撤销分配的回执必须带 warning 点名被撤销订阅并提示已复制
// 链接不失效；无撤销时回执无 warning（信封逐字同旧）。
func TestW3UserSubscriptionSetRevokeWarning(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/w3_usersub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	mkSub := func(name, token string) *model.Subscription {
		sub := &model.Subscription{Name: name, Kind: model.SubscriptionShare, Token: token,
			UUID: "u", Password: "p", Enabled: 1, CreatedTime: now, UpdatedTime: now}
		if err := r.SaveSubscription(sub); err != nil {
			t.Fatal(err)
		}
		return sub
	}
	s1 := mkSub("保留订阅", strings.Repeat("b", 32))
	s2 := mkSub("被撤订阅", strings.Repeat("c", 32))
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, status) VALUES(2, 'member', 'pwd', 1, 0, 100, 0, 0, 0, 10, 1000, 1)`).Error; err != nil {
		t.Fatal(err)
	}
	callSet := func(ids []int64) (int, string) {
		body := fmt.Sprintf(`{"userId":2,"subscriptionIds":[%s]}`, joinIDs(ids))
		req := httptest.NewRequest("POST", "/api/v1/user-subscription/set", strings.NewReader(body))
		ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
			auth.Claims{Sub: "1", RoleID: 0})
		rec := httptest.NewRecorder()
		h.userSubscriptionSet(rec, req.WithContext(ctx))
		var env struct {
			Code    int    `json:"code"`
			Warning string `json:"warning"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode set response: %v", err)
		}
		return env.Code, env.Warning
	}
	if code, warning := callSet([]int64{s1.ID, s2.ID}); code != 0 || warning != "" {
		t.Fatalf("initial assign: code=%d warning=%q", code, warning)
	}
	code, warning := callSet([]int64{s1.ID})
	if code != 0 {
		t.Fatalf("revoke set: code=%d", code)
	}
	if !strings.Contains(warning, "被撤订阅") || !strings.Contains(warning, "已经复制出去的订阅链接不会失效") {
		t.Fatalf("revoke warning must name the subscription and state copied links stay valid, got %q", warning)
	}
	if code, warning := callSet([]int64{s1.ID}); code != 0 || warning != "" {
		t.Fatalf("no-op set must not warn: code=%d warning=%q", code, warning)
	}
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return strings.Join(parts, ",")
}

// IRL-3（后端半）：同一入站经同一隧道到同一目标的第二次创建（不同
// 幂等键、不同入口端口）必须被去重闸点名拒绝，只落一条中转。
func TestW3RelayCreateDuplicateGate(t *testing.T) {
	r, h := r2b2Open(t)
	if _, err := r.CreateUser("admin", "hash", 0, time.Now().Add(24*time.Hour).UnixMilli(), 1, 1, 100, 1, 0, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	nodeID := seedRelayTargetNode(t, r, "w3-relay-entry", "198.51.100.31")
	tunnelID := seedTunnelForNftables(t, h, "w3-relay-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	inbound := &model.NodeInbound{
		Name: "w3-imported", Protocol: "ss", Listen: "198.51.100.19", Port: 8388,
		Enabled: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.DB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	first := map[string]interface{}{"inboundId": inbound.ID, "tunnelId": tunnelID, "inPort": 30007, "name": "w3-relay"}
	b1 := r2b2AuthPost(t, h.inboundRelayCreate, "/api/v1/inbound/relay/create", first, "w3-relay-key-1")
	r2b2AssertCode0(t, "relay create first", b1)
	second := map[string]interface{}{"inboundId": inbound.ID, "tunnelId": tunnelID, "inPort": 30008, "name": "w3-relay-again"}
	b2 := r2b2AuthPost(t, h.inboundRelayCreate, "/api/v1/inbound/relay/create", second, "w3-relay-key-2")
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(b2), &env); err != nil {
		t.Fatalf("decode second create response: %v (%s)", err, b2)
	}
	if env.Code == 0 || !strings.Contains(env.Msg, "请勿重复创建") {
		t.Fatalf("duplicate relay create must be rejected naming the gate, got %s", b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node_inbound_relay WHERE inbound_id = ?`, inbound.ID); n != 1 {
		t.Fatalf("relay rows = %d, want 1", n)
	}
}

// P-002（下发面钉子）：直写 DB 的空安全层存量行（创建面 Normalize
// 不可达、仅豁免恢复/直写可达的毒行形状）在 buildKernelApplyPayload
// 复检必须整单拒绝并点名入站，不得再按旧豁免放行进渲染。与谓词级
// TestW3ComboSupportedEmptySecurityRejected（kernel 包）成对：谓词
// 钉死之外，下发路径本身也钉死，防止将来有人在谓词与调用点之间
// 再开豁免。
func TestW3PayloadRejectsEmptySecurityRow(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/w3_p002.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES('n', 's', '203.0.113.1', '1000-2000', ?, 1)`, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")
	if err := r.DB().Exec(`INSERT INTO node_kernel(node_id, kernel_type, version, status, created_time, updated_time) VALUES(?, 'xray', 'latest', 'pending', ?, ?)`, nodeID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	kernelID := mustLastInsertID(t, r, "kernel")
	// 毒行形状：transport/security 两列为空（直写 DB 才可达）。
	if err := r.DB().Exec(`INSERT INTO node_inbound(kernel_id, node_id, name, protocol, listen, port, settings, created_time, updated_time)
		VALUES(?, ?, 'a', 'trojan', '::', 443, '{"password":"owner-pw"}', ?, ?)`, kernelID, nodeID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	k, err := r.GetNodeKernel(kernelID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = h.buildKernelApplyPayload(k)
	if err == nil {
		t.Fatal("empty-security stored row must be rejected at payload build (P-002), got nil error")
	}
	if !strings.Contains(err.Error(), "「a」") {
		t.Fatalf("rejection must name the offending inbound, got: %v", err)
	}
}
