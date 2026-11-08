package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R14-F08 回归：两项后端 P3（A4-001 / A2-3）。先红后绿——
// 本文件在修复前跑：A4-001 与 A2-3 五格 FAIL。
// （原 A5-08 的 Snell 链式/分流拒绝与毒化行 heal 用例已随自建
// Snell 入站整删一并删除：checkSnellVersions/healSnellPoisonedRows
// 与 Snell 内核均已不存在。）

func decodeR14F08Envelope(t *testing.T, res *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, res.Body.String())
	}
	return payload.Code, payload.Msg
}

// A4-001：批量恢复在「数据面已成功、DB 写失败」分支的失败原因必须
// 是真实错误（statusErr），不是恒 nil 的函数级 err（→「未知错误」）。
func TestR14F08BatchResumeDbWriteFailureReportsRealReason(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "r14f08-tunnel", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20001)

	now := time.Now().UnixMilli()
	if err := h.repo.UpdateForwardStatus(forward.ID, model.ForwardStatusPaused, now); err != nil {
		t.Fatalf("pause forward: %v", err)
	}
	// 恢复 reconcile 期间并发删除把行标记为 pending（R14-A4 探针同型竞态）：
	// 数据面已成功，随后 ActivateForwardIfNotPending 返回哨兵错误（statusErr）。
	manager.reconcileHook = func() {
		if err := h.repo.MarkForwardPendingDelete(forward.ID, fixture.nodeID, time.Now()); err != nil {
			t.Errorf("mark pending delete in reconcile hook: %v", err)
		}
	}

	req := newAuthenticatedJSONRequest(t, map[string]any{"ids": []int64{forward.ID}})
	res := httptest.NewRecorder()
	h.forwardBatchResume(res, req)

	var payload struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount int `json:"successCount"`
			FailCount    int `json:"failCount"`
			Failures     []struct {
				ID     int64  `json:"id"`
				Reason string `json:"reason"`
			} `json:"failures"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	if payload.Code != 0 {
		t.Fatalf("batch resume envelope code=%d body=%s", payload.Code, res.Body.String())
	}
	if payload.Data.SuccessCount != 0 || payload.Data.FailCount != 1 || len(payload.Data.Failures) != 1 {
		t.Fatalf("counts wrong: success=%d fail=%d failures=%v body=%s",
			payload.Data.SuccessCount, payload.Data.FailCount, payload.Data.Failures, res.Body.String())
	}
	if got := payload.Data.Failures[0].Reason; got != "forward is pending delete" {
		t.Fatalf("failure reason = %q, want %q (statusErr must be reported, not nil err)",
			got, "forward is pending delete")
	}
}

// A2-3（BC）：login / backupExport / updateAnnouncement 五处参数错误
// code=500 改为 ErrDefault（-1），与全仓 697 处口径一致。
func TestR14F08ParamErrorsUseErrDefault(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r14f08-a23.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	post := func(fn func(http.ResponseWriter, *http.Request), body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		res := httptest.NewRecorder()
		fn(res, req)
		return decodeR14F08Envelope(t, res)
	}

	cases := []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		body string
	}{
		{"login 非法 JSON", h.login, `not json`},
		{"login 空用户名", h.login, `{"username":"","password":"x"}`},
		{"login 空密码", h.login, `{"username":"admin","password":""}`},
		{"backupExport 非法 JSON", h.backupExport, `not json`},
		{"updateAnnouncement 非法 JSON", h.updateAnnouncement, `not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := post(tc.fn, tc.body)
			if code != -1 {
				t.Fatalf("code=%d msg=%q, want code=-1 (ErrDefault)", code, msg)
			}
		})
	}
}
