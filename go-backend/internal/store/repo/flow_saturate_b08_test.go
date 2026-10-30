package repo

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// B08-001 回归：flow 入账累加面（DB 现有余额 + 增量）必须饱和，
// 不得溢出。修复前：余额 2500 + 单条 MaxInt64 上报 → SQLite 把
// in_flow/out_flow 写成 REAL 9.223e18，forward/user 行从此扫描失败
// （属主登录与 forward/list 永久 code=-2，只能手工修库）。
// 本文件在 repo 层复刻怀疑者的复现并断言饱和语义。

func seedFlowSaturateFixture(t *testing.T, r *Repository, balance int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(2, 'u2', 'pwd', 1, 2727251700000, 99999, ?, ?, 1, 99999, ?, ?, 1)`, balance, balance, now, now).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(1, 't1', 1.0, 1, 'tls', 2, ?, ?, 1, NULL, 0)`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO user_tunnel(id, user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(10, 2, 1, NULL, 99999, 99999, ?, ?, 1, 2727251700000, 1)`, balance, balance).Error; err != nil {
		t.Fatalf("insert user_tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(20, 2, 'u2', 'f20', 1, '1.1.1.1:80', 'fifo', ?, ?, ?, ?, 1, 0)`, balance, balance, now, now).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}
}

func mustColumnType(t *testing.T, r *Repository, query string) string {
	t.Helper()
	var typ string
	if err := r.DB().Raw(query).Scan(&typ).Error; err != nil {
		t.Fatalf("typeof query %q: %v", query, err)
	}
	return typ
}

func TestFlowAccountingSaturatesInsteadOfOverflowing_B08_001(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "flow-saturate.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	seedFlowSaturateFixture(t, r, 2500)

	// 怀疑者复现：单条 MaxInt64 增量（scaled 面与 forward 的 raw 面同时给满）。
	delta := FlowUploadCounterDelta{
		ForwardID: 20, UserID: 2, UserTunnelID: 10,
		InFlow: math.MaxInt64, OutFlow: math.MaxInt64,
		RawInFlow: math.MaxInt64, RawOutFlow: math.MaxInt64, HasRaw: true,
	}
	if err := r.ApplyFlowUploadDeltasBatch([]FlowUploadCounterDelta{delta}); err != nil {
		t.Fatalf("apply extreme delta: %v", err)
	}
	// 再打一发：饱和后继续入账必须仍是饱和值，不得回绕/报错。
	if err := r.ApplyFlowUploadDeltasBatch([]FlowUploadCounterDelta{delta}); err != nil {
		t.Fatalf("apply extreme delta (2nd): %v", err)
	}

	for _, q := range []struct{ name, query string }{
		{"forward.in_flow", `SELECT in_flow FROM forward WHERE id = 20`},
		{"forward.out_flow", `SELECT out_flow FROM forward WHERE id = 20`},
		{"user.in_flow", `SELECT in_flow FROM user WHERE id = 2`},
		{"user.out_flow", `SELECT out_flow FROM user WHERE id = 2`},
		{"user_tunnel.in_flow", `SELECT in_flow FROM user_tunnel WHERE id = 10`},
		{"user_tunnel.out_flow", `SELECT out_flow FROM user_tunnel WHERE id = 10`},
	} {
		if got := mustFlowBatchCount(t, r, q.query); got != math.MaxInt64 {
			t.Fatalf("%s: expected saturated %d, got %d", q.name, int64(math.MaxInt64), got)
		}
	}
	for _, q := range []string{
		`SELECT typeof(in_flow) FROM forward WHERE id = 20`,
		`SELECT typeof(in_flow) FROM user WHERE id = 2`,
		`SELECT typeof(in_flow) FROM user_tunnel WHERE id = 10`,
	} {
		if typ := mustColumnType(t, r, q); typ != "integer" {
			t.Fatalf("%s: expected typeof integer, got %s (row poisoned to REAL)", q, typ)
		}
	}

	// 行必须仍可正常扫描（属主登录/列表在 handler 层正是靠扫描这两行）。
	var fwd model.Forward
	if err := r.DB().First(&fwd, 20).Error; err != nil {
		t.Fatalf("scan forward row after saturation: %v", err)
	}
	if fwd.InFlow != math.MaxInt64 || fwd.OutFlow != math.MaxInt64 {
		t.Fatalf("forward counters: got in=%d out=%d", fwd.InFlow, fwd.OutFlow)
	}
	var usr model.User
	if err := r.DB().First(&usr, 2).Error; err != nil {
		t.Fatalf("scan user row after saturation: %v", err)
	}
	items, err := r.ListForwards()
	if err != nil {
		t.Fatalf("ListForwards after saturation: %v", err)
	}
	if len(items) != 1 || items[0]["inFlow"] != int64(math.MaxInt64) {
		t.Fatalf("ListForwards items: %#v", items)
	}
}

func TestFlowAccountingBatchMergeSaturates_B08_001(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "flow-merge-saturate.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	seedFlowSaturateFixture(t, r, 0)

	// 两条各自合法的增量在批内合并时相加溢出：不得先在 Go 层回绕为负。
	d := FlowUploadCounterDelta{ForwardID: 20, UserID: 2, UserTunnelID: 10, InFlow: math.MaxInt64, OutFlow: 5}
	if err := r.ApplyFlowUploadDeltasBatch([]FlowUploadCounterDelta{d, d}); err != nil {
		t.Fatalf("apply merging deltas: %v", err)
	}
	if got := mustFlowBatchCount(t, r, `SELECT in_flow FROM forward WHERE id = 20`); got != math.MaxInt64 {
		t.Fatalf("forward in_flow: expected saturated %d, got %d", int64(math.MaxInt64), got)
	}
	if got := mustFlowBatchCount(t, r, `SELECT out_flow FROM forward WHERE id = 20`); got != 10 {
		t.Fatalf("forward out_flow: expected 10, got %d", got)
	}
}

func TestAddFlowSaturates_B08_001(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "addflow-saturate.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	seedFlowSaturateFixture(t, r, 2500)

	if err := r.AddFlow(20, 2, 10, math.MaxInt64, math.MaxInt64); err != nil {
		t.Fatalf("AddFlow extreme: %v", err)
	}
	if got := mustFlowBatchCount(t, r, `SELECT in_flow FROM forward WHERE id = 20`); got != math.MaxInt64 {
		t.Fatalf("forward in_flow: expected saturated %d, got %d", int64(math.MaxInt64), got)
	}
	if typ := mustColumnType(t, r, `SELECT typeof(out_flow) FROM user WHERE id = 2`); typ != "integer" {
		t.Fatalf("user out_flow typeof: expected integer, got %s", typ)
	}
}

func TestFlowAccountingQuotaUsageSaturates_B08_001(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "quota-saturate.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	seedFlowSaturateFixture(t, r, 0)

	now := time.Now()
	usage := map[int64]int64{2: math.MaxInt64}
	// 同 tx 的配额用量累加：连打两发，第二发不得把 user_quota 行溢出成 REAL。
	for i := 0; i < 2; i++ {
		if _, err := r.ApplyFlowUploadAccounting(nil, usage, now, nil); err != nil {
			t.Fatalf("ApplyFlowUploadAccounting #%d: %v", i, err)
		}
	}
	if got := mustFlowBatchCount(t, r, `SELECT daily_used_bytes FROM user_quota WHERE user_id = 2`); got != math.MaxInt64 {
		t.Fatalf("daily_used_bytes: expected saturated %d, got %d", int64(math.MaxInt64), got)
	}
	if typ := mustColumnType(t, r, `SELECT typeof(monthly_used_bytes) FROM user_quota WHERE user_id = 2`); typ != "integer" {
		t.Fatalf("monthly_used_bytes typeof: expected integer, got %s", typ)
	}
	view, err := r.GetUserQuotaView(2, now)
	if err != nil || view == nil {
		t.Fatalf("GetUserQuotaView after saturation: view=%v err=%v", view, err)
	}
	// 单条路径 AddUserQuotaUsage 同口径。
	if _, err := r.AddUserQuotaUsage(2, math.MaxInt64, now); err != nil {
		t.Fatalf("AddUserQuotaUsage extreme: %v", err)
	}
	if got := mustFlowBatchCount(t, r, `SELECT daily_used_bytes FROM user_quota WHERE user_id = 2`); got != math.MaxInt64 {
		t.Fatalf("daily_used_bytes after AddUserQuotaUsage: expected saturated %d, got %d", int64(math.MaxInt64), got)
	}
}

func TestSaturatingAddInt64_B08_001(t *testing.T) {
	cases := []struct{ a, b, want int64 }{
		{0, 0, 0},
		{2500, 100, 2600},
		{2500, math.MaxInt64, math.MaxInt64},
		{math.MaxInt64, 1, math.MaxInt64},
		{math.MaxInt64 - 5, 5, math.MaxInt64},
		{math.MaxInt64 - 5, 6, math.MaxInt64},
		{-2500, -100, -2600},
		{-2500, math.MinInt64, math.MinInt64},
		{math.MinInt64, -1, math.MinInt64},
		{5, -3, 2},
	}
	for _, c := range cases {
		if got := saturatingAddInt64(c.a, c.b); got != c.want {
			t.Fatalf("saturatingAddInt64(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// B08-005 回归：绑定回退事件持久化并经转发列表可见，显式绑定恢复
// 后清除。修复前回退只把 in_ip 清空、warning 只达当次同步调用方。
func TestForwardPortBindWarningLifecycle_B08_005(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "bind-warning.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	seedFlowSaturateFixture(t, r, 0)

	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(id, name, secret, server_ip, port, created_time, updated_time, status) VALUES(7, 'n7', 's7', '127.0.0.1', '10000-20000', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward_port(forward_id, node_id, port, in_ip) VALUES(20, 7, 5000, '203.0.113.7')`).Error; err != nil {
		t.Fatalf("insert forward_port: %v", err)
	}

	warning := "节点 n7 监听IP 203.0.113.7 不在主机网卡地址中，已自动回退为默认监听IP"
	if err := r.UpdateForwardPortBindWarning(20, 7, 5000, warning); err != nil {
		t.Fatalf("UpdateForwardPortBindWarning: %v", err)
	}
	// 模拟回退同时清空 in_ip（UpdateForwardPortBindIP 的既有行为）——
	// 告警必须独立存活，不随 in_ip 清空而消失。
	if err := r.UpdateForwardPortBindIP(20, 7, 5000, ""); err != nil {
		t.Fatalf("UpdateForwardPortBindIP: %v", err)
	}
	var fp model.ForwardPort
	if err := r.DB().Where("forward_id = ? AND node_id = ? AND port = ?", 20, 7, 5000).First(&fp).Error; err != nil {
		t.Fatalf("load forward_port: %v", err)
	}
	if fp.InIP.Valid && fp.InIP.String != "" {
		t.Fatalf("expected in_ip cleared, got %q", fp.InIP.String)
	}
	if !fp.BindWarning.Valid || fp.BindWarning.String != warning {
		t.Fatalf("expected persisted bind warning %q, got %#v", warning, fp.BindWarning)
	}

	items, err := r.ListForwards()
	if err != nil {
		t.Fatalf("ListForwards: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(items))
	}
	ws, ok := items[0]["bindWarnings"].([]string)
	if !ok || len(ws) != 1 || ws[0] != warning {
		t.Fatalf("expected bindWarnings=[%q], got %#v", warning, items[0]["bindWarnings"])
	}

	if err := r.ClearForwardPortBindWarning(20, 7, 5000); err != nil {
		t.Fatalf("ClearForwardPortBindWarning: %v", err)
	}
	items, err = r.ListForwards()
	if err != nil {
		t.Fatalf("ListForwards after clear: %v", err)
	}
	if _, present := items[0]["bindWarnings"]; present {
		t.Fatalf("expected bindWarnings absent after clear, got %#v", items[0]["bindWarnings"])
	}
}
