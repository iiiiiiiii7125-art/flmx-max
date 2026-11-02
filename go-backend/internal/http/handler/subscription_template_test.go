package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

const tplTestClash = "proxies: []\nproxy-groups:\n  - {name: 选择, type: select, include-all: true}\nrules: [MATCH，选择]\n"

func tplTestHandler(t *testing.T) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/template.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "unit-test-secret"), r
}

func tplPost(t *testing.T, fn func(http.ResponseWriter, *http.Request), body string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	fn(rec, req)
	var env map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return rec.Code, env
}

// TestSubscriptionTemplateSaveAndUse covers the local-template flow:
// save → check → a subscription picks it → the render options carry its
// content; a wrong-kind pick is rejected at subscription save time.
func TestSubscriptionTemplateSaveAndUse(t *testing.T) {
	h, r := tplTestHandler(t)

	_, env := tplPost(t, h.subscriptionTemplateSave, `{"name":"我的模板","kind":"clash","content":`+tplJSONStr(tplTestClash)+`}`)
	if env["code"] != float64(0) {
		t.Fatalf("save: %v", env)
	}
	data := env["data"].(map[string]interface{})
	id := int64(data["id"].(float64))
	if data["groups"] != float64(1) || data["autoGroups"] != float64(1) {
		t.Fatalf("view summary: %v", data)
	}

	// An invalid template is refused at save.
	_, env = tplPost(t, h.subscriptionTemplateSave, `{"name":"坏模板","kind":"clash","content":"proxies: [a"}`)
	if env["code"] == float64(0) {
		t.Fatalf("invalid template saved: %v", env)
	}

	// Check previews the auto group.
	_, env = tplPost(t, h.subscriptionTemplateCheck, `{"kind":"clash","content":`+tplJSONStr(tplTestClash)+`}`)
	if env["code"] != float64(0) || env["data"].(map[string]interface{})["autoGroups"] != float64(1) {
		t.Fatalf("check: %v", env)
	}

	// A subscription picking the template for the clash slot loads its
	// content through subOptions; a surge slot pointing at a clash
	// template is rejected.
	now := time.Now().UnixMilli()
	s := &model.Subscription{Name: "s", Kind: model.SubscriptionShare, Token: strings.Repeat("c", 32),
		Enabled: 1, CreatedTime: now, UpdatedTime: now}
	opts, _ := json.Marshal(subPipeline{Templates: map[string]int64{"clash": id}})
	s.Options = string(opts)
	if err := r.SaveSubscription(s); err != nil {
		t.Fatal(err)
	}
	loaded := h.subOptions(parsePipeline(s.Options))
	if loaded.Templates["clash"] != tplTestClash {
		t.Fatalf("subOptions did not load the template: %q", loaded.Templates["clash"])
	}
	if err := h.checkPipelineTemplates(subPipeline{Templates: map[string]int64{"surge": id}}); err == nil {
		t.Fatal("wrong-kind template pick accepted")
	}
	if err := h.checkPipelineTemplates(subPipeline{Templates: map[string]int64{"clash": 99999}}); err == nil {
		t.Fatal("missing template pick accepted")
	}

	// A template in use cannot be deleted.
	_, env = tplPost(t, h.subscriptionTemplateDelete, `{"id":`+tplIDStr(id)+`}`)
	if env["code"] == float64(0) {
		t.Fatalf("in-use template deleted: %v", env)
	}
}

// TestSubscriptionTemplateRefreshTwoStates: a URL template refresh that
// succeeds replaces the content and clears the error; one that fails
// keeps the last good content and records the error (the subscriptions
// using it are never taken down by a broken source).
func TestSubscriptionTemplateRefreshTwoStates(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	h, r := tplTestHandler(t)

	good := tplTestClash
	var serveErr bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if serveErr {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(good))
	}))
	defer srv.Close()

	now := time.Now().UnixMilli()
	tpl := &model.SubscriptionTemplate{Name: "远程", Kind: "clash", SourceURL: srv.URL,
		CreatedTime: now, UpdatedTime: now}
	if err := r.SaveSubscriptionTemplate(tpl); err != nil {
		t.Fatal(err)
	}

	// Success: content lands, error cleared, fetch time stamped.
	if err := h.refreshTemplate(context.Background(), tpl); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetSubscriptionTemplate(tpl.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Content != good || got.LastError != "" || got.LastFetch == 0 {
		t.Fatalf("after successful refresh: %+v", got)
	}

	// Failure (HTTP 500): old content kept, error recorded.
	serveErr = true
	if err := h.refreshTemplate(context.Background(), got); err == nil {
		t.Fatal("refresh of a broken source succeeded")
	}
	got, _ = r.GetSubscriptionTemplate(tpl.ID)
	if got.Content != good || got.LastError == "" {
		t.Fatalf("after failed refresh: %+v", got)
	}

	// Failure (invalid content): old content kept, error recorded.
	serveErr = false
	good = "proxies: [a"
	if err := h.refreshTemplate(context.Background(), got); err == nil {
		t.Fatal("refresh of invalid content succeeded")
	}
	got, _ = r.GetSubscriptionTemplate(tpl.ID)
	if got.Content == good || got.LastError == "" {
		t.Fatalf("after invalid refresh: %+v", got)
	}

	// refreshDueTemplates only touches URL templates that are due.
	local := &model.SubscriptionTemplate{Name: "本地", Kind: "clash", Content: tplTestClash,
		CreatedTime: now, UpdatedTime: now}
	if err := r.SaveSubscriptionTemplate(local); err != nil {
		t.Fatal(err)
	}
	h.refreshDueTemplates(context.Background(), time.Now())
	gotLocal, _ := r.GetSubscriptionTemplate(local.ID)
	if gotLocal.LastFetch != 0 {
		t.Fatalf("local template was refreshed: %+v", gotLocal)
	}
}

func tplJSONStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func tplIDStr(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}

// TestOpenSubscriptionTemplateWithInfoNodes is the handler-layer half of
// the template × info-node weave: a subscription that both picks
// templates and enables info nodes must still lead with the expiry and
// traffic placeholders in template-rendered formats (clash + sing-box).
func TestOpenSubscriptionTemplateWithInfoNodes(t *testing.T) {
	h, r, _ := groupFixture(t)

	_, env := tplPost(t, h.subscriptionTemplateSave, `{"name":"clash模板","kind":"clash","content":`+tplJSONStr(tplTestClash)+`}`)
	if env["code"] != float64(0) {
		t.Fatalf("save clash template: %v", env)
	}
	clashID := int64(env["data"].(map[string]interface{})["id"].(float64))

	singboxTpl := `{"outbounds":[{"type":"selector","tag":"选择","outbounds":["{all}"]}]}`
	_, env = tplPost(t, h.subscriptionTemplateSave, `{"name":"sb模板","kind":"singbox","content":`+tplJSONStr(singboxTpl)+`}`)
	if env["code"] != float64(0) {
		t.Fatalf("save singbox template: %v", env)
	}
	sbID := int64(env["data"].(map[string]interface{})["id"].(float64))

	expire := time.Date(2027, 3, 4, 0, 0, 0, 0, time.Local).UnixMilli()
	opts, _ := json.Marshal(subPipeline{InfoNodes: true, Templates: map[string]int64{"clash": clashID, "singbox": sbID}})
	s := createShareSub(t, r, &model.Subscription{
		Name: "模板信息", Kind: model.SubscriptionShare, Token: strings.Repeat("d", 32), UUID: "uuid-tplinfo", Password: "pw-tplinfo",
		SourceAll: 1, Options: string(opts), Enabled: 1, ExpireTime: expire,
		TrafficLimit: 10 * 1024 * 1024 * 1024, UploadBytes: 1024 * 1024 * 1024, DownloadBytes: 2 * 1024 * 1024 * 1024,
	})

	notices := []string{"⏳ 到期 2027-03-04", "📊 剩余 7.00GB / 10.00GB"}
	code, body := fetchSub(t, h, s.Token, "clash")
	if code != http.StatusOK {
		t.Fatalf("clash fetch: %d %s", code, body)
	}
	for _, want := range append(notices, "hk-1") {
		if !strings.Contains(body, want) {
			t.Fatalf("clash template output missing %q\n%s", want, body)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub/"+s.Token+"?format=singbox", nil)
	rec := httptest.NewRecorder()
	h.openSubscription(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("singbox fetch: %d %s", rec.Code, rec.Body.String())
	}
	for _, want := range append(notices, "hk-1") {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("sing-box template output missing %q\n%s", want, rec.Body.String())
		}
	}
}

// TestSubscriptionTemplateCheckIncludesGroupNodes: the template check
// preview for a picked subscription must use the render set (group
// splice included), matching subscriptionPreview — a group-only
// subscription previously previewed with zero nodes.
func TestSubscriptionTemplateCheckIncludesGroupNodes(t *testing.T) {
	h, r, ids := groupFixture(t)
	g := saveGroup(t, r, "HK组", []int64{ids["hk-1"], ids["hk-2"]}, "")
	s := saveShareSub(t, r, "G", strings.Repeat("c", 32), []int64{g.ID}, "")

	_, env := tplPost(t, h.subscriptionTemplateCheck, `{"kind":"clash","content":`+tplJSONStr(tplTestClash)+`,"subscriptionId":`+tplIDStr(s.ID)+`}`)
	if env["code"] != float64(0) {
		t.Fatalf("check: %v", env)
	}
	data := env["data"].(map[string]interface{})
	if data["nodes"] != float64(2) {
		t.Fatalf("check sample must include the 2 group nodes, got nodes=%v (data=%v)", data["nodes"], data)
	}
	summary := data["summary"].(map[string]interface{})
	var autoNodes string
	for _, gi := range summary["groups"].([]interface{}) {
		gm := gi.(map[string]interface{})
		if gm["auto"] == true {
			autoNodes = fmt.Sprint(gm["nodes"])
		}
	}
	if !strings.Contains(autoNodes, "hk-1") || !strings.Contains(autoNodes, "hk-2") {
		t.Fatalf("auto group must preview the group nodes, got %s", autoNodes)
	}
}
