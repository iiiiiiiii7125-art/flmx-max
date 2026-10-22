package handler

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// X-P3-31 (V-08): gost 入账端到端——
//  1. metas 查询失败 → 整批拒收（非 ok）、不入账、seq 未推进；
//  2. 入账段失败（quota 注入）→ 非 ok、总量不落账、seq 未推进；
//  3. 故障消除后同 seq 重报 → ok 且入账；再重报同 seq → ok 但不重复入账。
//
// 修复前：seq 在入账前被 CheckAndUpdateSeq 消费、metas 失败置空仍回
// ok、两段分开写一段失败另一段继续——1/2 均回 ok 且 seq 已推进，
// 同 seq 重报被判重放丢账。
func TestR1B8_XP331_FlowUploadRejectsWithoutSeqAdvance(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp331.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	nowMs := now.UnixMilli()

	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES('xp331-node', 'xp331-secret', '198.51.100.34', '198.51.100.34', '', '', '', 'v1', 0, 0, 0, ?, ?, 1, '[::]', '[::]', 0)`, nowMs, nowMs).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")
	if err := r.DB().Create(&model.User{ID: 2, User: "xp331-user", Pwd: "pwd", RoleID: 1, ExpTime: 2727251700000, Flow: 99999, Num: 99999, CreatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := r.DB().Create(&model.Tunnel{ID: 1, Name: "tunnel-1", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	if err := r.DB().Create(&model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Num: 99999, Flow: 0, ExpTime: 2727251700000, Status: 1}).Error; err != nil {
		t.Fatalf("seed user tunnel: %v", err)
	}
	if err := r.DB().Create(&model.Forward{ID: 20, UserID: 2, UserName: "xp331-user", Name: "forward-20", TunnelID: 1, RemoteAddr: "1.1.1.1:80", Strategy: "fifo", CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed forward: %v", err)
	}
	if err := r.DB().Create(&model.ForwardPort{ForwardID: 20, NodeID: nodeID, Port: 32001}).Error; err != nil {
		t.Fatalf("seed forward port: %v", err)
	}

	post := func() string {
		t.Helper()
		body := `{"seq":7,"items":[{"n":"20_2_10","u":100,"d":50}]}`
		req := httptest.NewRequest("POST", "/flow/upload?secret=xp331-secret", strings.NewReader(body))
		res := httptest.NewRecorder()
		h.flowUpload(res, req)
		return strings.TrimSpace(res.Body.String())
	}
	forwardInFlow := func() int {
		t.Helper()
		return mustQueryInt(t, r, `SELECT in_flow FROM forward WHERE id = 20`)
	}
	seqAdvanced := func() bool {
		t.Helper()
		seq := int64(7)
		isReplay, _, err := r.CheckSeq(nodeID, &seq)
		if err != nil {
			t.Fatal(err)
		}
		return isReplay
	}

	// 1) metas 失败（user_tunnel 改名使 metas 联表查询报错）→ 非 ok。
	if err := r.DB().Exec(`ALTER TABLE user_tunnel RENAME TO user_tunnel_xp331`).Error; err != nil {
		t.Fatal(err)
	}
	if got := post(); got == "ok" {
		t.Fatalf("metas 失败必须整批拒收（非 ok），got %q", got)
	}
	if err := r.DB().Exec(`ALTER TABLE user_tunnel_xp331 RENAME TO user_tunnel`).Error; err != nil {
		t.Fatal(err)
	}
	if got := forwardInFlow(); got != 0 {
		t.Fatalf("metas 失败不得入账, in_flow=%d", got)
	}
	if seqAdvanced() {
		t.Fatalf("metas 失败后 seq 不得推进")
	}

	// 2) 入账段失败（quota insert 注入）→ 非 ok、总量不落账、seq 不动。
	if err := r.DB().Exec(`CREATE TRIGGER xp331_fail_quota_insert BEFORE INSERT ON user_quota BEGIN SELECT RAISE(FAIL, 'xp331 quota blocked'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if got := post(); got == "ok" {
		t.Fatalf("入账失败必须返非 ok，got %q", got)
	}
	if got := forwardInFlow(); got != 0 {
		t.Fatalf("入账失败后总量不得落账（单 tx）, in_flow=%d", got)
	}
	if seqAdvanced() {
		t.Fatalf("入账失败后 seq 不得推进")
	}
	if err := r.DB().Exec(`DROP TRIGGER xp331_fail_quota_insert`).Error; err != nil {
		t.Fatal(err)
	}

	// 3) 同 seq 重报 → ok 且入账（D=入=50）；再报同 seq → ok 不重复入账。
	if got := post(); got != "ok" {
		t.Fatalf("同 seq 重报应入账并回 ok, got %q", got)
	}
	if got := forwardInFlow(); got != 50 {
		t.Fatalf("重报后 in_flow=%d, want 50", got)
	}
	if !seqAdvanced() {
		t.Fatalf("入账成功后 seq 应已推进")
	}
	if got := post(); got != "ok" {
		t.Fatalf("重放应回 ok（终止 agent 重试）, got %q", got)
	}
	if got := forwardInFlow(); got != 50 {
		t.Fatalf("重放不得重复入账, in_flow=%d", got)
	}
}
