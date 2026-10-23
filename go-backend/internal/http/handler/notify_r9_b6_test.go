package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go-backend/internal/store/repo"
)

// R9X-006：测试通知必须与正式通知同一重试语义——Webhook 桩首击 500、
// 次击 200 时，notifyTest 应在单次发送内重试一次并最终送达（共 2 击、
// 回执 code=0）。修复前 notifyTest 直调 notifier.send（单次投递、无
// 重试）：只 1 击且回执发送失败，本用例为红。
func TestR9B6NotifyTestRetriesFlakyWebhook(t *testing.T) {
	var hits atomic.Int32
	flaky := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if hits.Add(1) == 1 {
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer flaky.Close()

	r, err := repo.Open(t.TempDir() + "/notify-r9b6.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	body := `{"webhookUrl":"` + flaky.URL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/notify/test", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.notifyTest(rec, req)

	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (body %q)", err, rec.Body.String())
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("R9X-006: flaky webhook hits = %d, want 2 (initial + one retry); response %+v", got, out)
	}
	if out.Code != 0 {
		t.Fatalf("R9X-006: notifyTest must succeed after retry, got %+v", out)
	}
}

// R9X-006 回归格：恒失败端点重试一次后仍报错（共 2 击），且错误文案
// 不变、不泄漏任何凭据形态。
func TestR9B6NotifyTestStillFailsAfterRetry(t *testing.T) {
	var hits atomic.Int32
	dead := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()

	r, err := repo.Open(t.TempDir() + "/notify-r9b6-fail.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	body := `{"webhookUrl":"` + dead.URL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/notify/test", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.notifyTest(rec, req)

	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (body %q)", err, rec.Body.String())
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("R9X-006: persistent-failure webhook hits = %d, want 2 (initial + one retry)", got)
	}
	if out.Code == 0 || !strings.Contains(out.Msg, "发送失败") || !strings.Contains(out.Msg, "Webhook: HTTP 500") {
		t.Fatalf("R9X-006: persistent failure must still report the original error, got %+v", out)
	}
}
