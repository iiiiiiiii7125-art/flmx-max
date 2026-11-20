package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R18-B6-03: a manual template refresh whose upstream is down always
// came back as generic "Internal error", hiding the real reason.
// Only storage errors take the generic path; fetch/validation
// failures return their fixed wording.
func TestR18B603RefreshFailureSurfacesRealReason(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/b603-refresh-err.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	tpl := &model.SubscriptionTemplate{Name: "b6-03-tpl", Kind: "clash",
		SourceURL: "http://127.0.0.1:1/b6-03-template.yaml", Content: "proxies: []\n"}
	if err := r.SaveSubscriptionTemplate(tpl); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	rec := httptest.NewRecorder()
	h.subscriptionTemplateRefresh(rec, httptest.NewRequest("POST", "/api/v1/subscription/template/refresh",
		strings.NewReader(`{"id":`+itoaB202(tpl.ID)+`}`)))
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode refresh response: %v (body=%s)", err, rec.Body.String())
	}
	if env.Code == 0 {
		t.Fatalf("refresh with upstream down must fail, got code=%d msg=%q", env.Code, env.Msg)
	}
	if env.Msg == "Internal error" || !strings.Contains(env.Msg, "下载模板失败") {
		t.Fatalf("refresh failure must carry the real reason, got msg=%q", env.Msg)
	}
}
