package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

const b604Content = `{"outbounds": [{"type": "direct", "tag": "direct"}]}`

func seedB604Template(t *testing.T, r *repo.Repository) *model.SubscriptionTemplate {
	t.Helper()
	tpl := &model.SubscriptionTemplate{Name: "b6-04-tpl", Kind: "singbox",
		SourceURL: "http://127.0.0.1:1/b6-04-template.json", Content: b604Content, LastFetch: 111}
	if err := r.SaveSubscriptionTemplate(tpl); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	return tpl
}

func callTemplateSave(t *testing.T, h *Handler, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.subscriptionTemplateSave(rec, httptest.NewRequest("POST", "/api/v1/subscription/template/save", strings.NewReader(body)))
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode save response: %v (body=%s)", err, rec.Body.String())
	}
	return env.Code, env.Msg
}

// R18-B6-04: saving a URL template always re-downloaded, so a pure
// rename/metadata edit failed when upstream was down — and silently
// pulled remote content changes. An update that keeps the same URL
// reuses the stored content (still validated); only a URL change or
// a create downloads.
func TestR18B604SameURLSaveReusesStoredContent(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/b604-save.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	tpl := seedB604Template(t, r)

	// Pure rename, upstream down: must succeed with stored content.
	code, msg := callTemplateSave(t, h, `{"id":`+itoaB202(tpl.ID)+`,"name":"b6-04-renamed","kind":"singbox","sourceUrl":"http://127.0.0.1:1/b6-04-template.json"}`)
	if code != 0 {
		t.Fatalf("same-URL rename must succeed without downloading, got code=%d msg=%q", code, msg)
	}
	got, err := r.GetSubscriptionTemplate(tpl.ID)
	if err != nil || got == nil {
		t.Fatalf("read template after save: %+v err=%v", got, err)
	}
	if got.Name != "b6-04-renamed" || got.Content != b604Content {
		t.Fatalf("rename must update the name and keep stored content, got name=%q content=%q", got.Name, got.Content)
	}
	if got.LastFetch != 111 {
		t.Fatalf("same-URL save must not re-stamp LastFetch, got %d want 111", got.LastFetch)
	}

	// Changing the URL still downloads (and fails while upstream is down).
	code, msg = callTemplateSave(t, h, `{"id":`+itoaB202(tpl.ID)+`,"name":"b6-04-renamed","kind":"singbox","sourceUrl":"http://127.0.0.1:1/b6-04-other.json"}`)
	if code == 0 || !strings.Contains(msg, "下载模板失败") {
		t.Fatalf("URL change must still download and report the failure, got code=%d msg=%q", code, msg)
	}
}
