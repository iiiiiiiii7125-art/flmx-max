package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// G-UPDATE-01（P3）：用户编辑时 PropagateUserFlowToTunnels 失败只记
// 日志仍回成功，隧道级限额留旧值。修复：与同函数 syncUserMaxConnForwards
// （:415-419）同口径，传播失败即上抛错误。先红后绿：注入传播失败时
// 修复前回 code 0（测试失败），修复后返回错误；成功路径回归通过，
// 且隧道行同步更新。

func decodeEnvelopeGUpdate(t *testing.T, res *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	return env.Code, env.Msg
}

func gUpdateFixture(t *testing.T) (*Handler, *repo.Repository, int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/g-update01.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nowMs := time.Now().UnixMilli()
	exp := nowMs + 30*86400000
	userID, err := r.CreateUser("gupd-user", "hash", 1, exp, 1, 1, 100, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	// 隧道级旧限额行：传播成功后 flow 须与用户新值一致。
	if err := r.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(?, 1, 3, 5, 512, 0, 0, 7, ?, 1)`, userID, exp).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	return h, r, userID
}

func TestGUpdate01PropagateFailureReturnsError(t *testing.T) {
	// 注入传播失败：删 user_tunnel 表，PropagateUserFlowToTunnels
	// 必错；修复前 userUpdate 仍回 code 0（红），修复后须返回错误（绿）。
	h, r, userID := gUpdateFixture(t)
	if err := r.DB().Exec(`DROP TABLE user_tunnel`).Error; err != nil {
		t.Fatalf("drop user_tunnel: %v", err)
	}
	res := postJSONToHandler(t, h.userUpdate, map[string]interface{}{
		"id": userID, "user": "gupd-user", "flow": 200,
	})
	code, msg := decodeEnvelopeGUpdate(t, res)
	if code == 0 {
		t.Fatalf("userUpdate must surface tunnel propagation failure, got code 0 (silent stale tunnel quota)")
	}
	if msg == "" {
		t.Fatalf("error response must carry a message, got empty msg")
	}
}

func TestGUpdate01PropagateSuccessStillUpdatesTunnel(t *testing.T) {
	// 成功路径回归：编辑成功且隧道行同步到新限额（不得把成功路径改坏）。
	h, r, userID := gUpdateFixture(t)
	res := postJSONToHandler(t, h.userUpdate, map[string]interface{}{
		"id": userID, "user": "gupd-user", "flow": 200,
	})
	if code, msg := decodeEnvelopeGUpdate(t, res); code != 0 {
		t.Fatalf("userUpdate success path must still succeed, got code=%d msg=%s", code, msg)
	}
	u, err := r.GetUserByID(userID)
	if err != nil || u == nil {
		t.Fatalf("get user: %v", err)
	}
	var tunnelFlow int64
	if err := r.DB().Raw(`SELECT flow FROM user_tunnel WHERE user_id = ?`, userID).Row().Scan(&tunnelFlow); err != nil {
		t.Fatalf("read user_tunnel flow: %v", err)
	}
	if tunnelFlow != u.Flow {
		t.Fatalf("tunnel quota must follow user flow: user flow=%d, tunnel flow=%d", u.Flow, tunnelFlow)
	}
}

// C-(6)①：仅改 dailyQuotaGB 时隧道授权行五项全不动——旧实现把用户
// 行合并后的 flow/flow_mib/num/exp_time/flow_reset_time 全量覆写到
// 隧道行，仅改配额也把 flowMiB 5→0（夹具行 512→0）。
func TestGUpdate01DailyQuotaOnlyLeavesTunnelFlow(t *testing.T) {
	h, r, userID := gUpdateFixture(t)
	res := postJSONToHandler(t, h.userUpdate, map[string]interface{}{
		"id": userID, "user": "gupd-user", "dailyQuotaGB": 10,
	})
	if code, msg := decodeEnvelopeGUpdate(t, res); code != 0 {
		t.Fatalf("dailyQuotaGB-only update must succeed, got code=%d msg=%s", code, msg)
	}
	var row struct {
		Flow, FlowMiB, Num, ExpTime, FlowResetTime int64
	}
	if err := r.DB().Raw(`SELECT flow, flow_mib, num, exp_time, flow_reset_time FROM user_tunnel WHERE user_id = ?`, userID).Row().Scan(&row.Flow, &row.FlowMiB, &row.Num, &row.ExpTime, &row.FlowResetTime); err != nil {
		t.Fatalf("read user_tunnel: %v", err)
	}
	if row.Flow != 5 || row.FlowMiB != 512 || row.Num != 3 || row.FlowResetTime != 7 {
		t.Fatalf("tunnel grant must be untouched by a quota-only edit: got flow=%d flow_mib=%d num=%d flow_reset_time=%d, want 5/512/3/7", row.Flow, row.FlowMiB, row.Num, row.FlowResetTime)
	}
}

// C-(6)① 字段级传播：只传 flow 时只改隧道 flow，flow_mib 保持原值。
func TestGUpdate01FlowOnlyLeavesTunnelFlowMiB(t *testing.T) {
	h, r, userID := gUpdateFixture(t)
	res := postJSONToHandler(t, h.userUpdate, map[string]interface{}{
		"id": userID, "user": "gupd-user", "flow": 200,
	})
	if code, msg := decodeEnvelopeGUpdate(t, res); code != 0 {
		t.Fatalf("flow-only update must succeed, got code=%d msg=%s", code, msg)
	}
	var flow, flowMiB int64
	if err := r.DB().Raw(`SELECT flow, flow_mib FROM user_tunnel WHERE user_id = ?`, userID).Row().Scan(&flow, &flowMiB); err != nil {
		t.Fatalf("read user_tunnel: %v", err)
	}
	if flow != 200 {
		t.Fatalf("tunnel flow must follow supplied flow: got %d, want 200", flow)
	}
	if flowMiB != 512 {
		t.Fatalf("tunnel flow_mib must keep its own value when flowMiB not supplied: got %d, want 512", flowMiB)
	}
}
