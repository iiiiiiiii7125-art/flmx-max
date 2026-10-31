package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R3 B14-003 回归：updateConfigs 批量 payload 内两原始键归一到同一键
// 时定序——意图不一致（不同值、或一写一清）整批拒绝并点名归一键，
// 同 payload 多次调用结果确定（基线靠 map 迭代序、V6 实测 8 连提
// 两值均出现）；意图一致（同值、或同为清空）按一处生效。

func TestR3K5UpdateConfigsNormalizedKeyCollision(t *testing.T) {
	r, h, adminID := k1AdminFixture(t)
	call := func(fn func(http.ResponseWriter, *http.Request), path, body string) envelope {
		w := httptest.NewRecorder()
		fn(w, claimsCtxReq(http.MethodPost, path, "", body, adminID, 0))
		return decodeEnvelope(t, w)
	}

	// 预置规范值，冲突批次不得改动它。
	if env := call(h.updateSingleConfig, "/api/v1/config/update-single",
		`{"name":"ip","value":"192.0.2.1:8443"}`); env.Code != 0 {
		t.Fatalf("seed ip: code=%d msg=%q", env.Code, env.Msg)
	}

	// 冲突（同归一键、不同值）：8 连提全部拒绝、错误点名归一键与两
	// 原始键、落库值不变——结果确定性钉死。
	for i := 0; i < 8; i++ {
		env := call(h.updateConfigs, "/api/v1/config/update",
			`{"IP":"10.0.0.1:1","ip":"10.0.0.2:2"}`)
		if env.Code == 0 {
			t.Fatalf("round %d: conflicting payload accepted", i)
		}
		if !strings.Contains(env.Msg, "\"ip\"") || !strings.Contains(env.Msg, "\"IP\"") {
			t.Fatalf("round %d: rejection does not name the keys: %q", i, env.Msg)
		}
	}
	cfg, err := r.GetConfigByName("ip")
	if err != nil || cfg == nil || cfg.Value != "192.0.2.1:8443" {
		t.Fatalf("ip mutated by rejected batches: %v / %v", cfg, err)
	}

	// 意图一致（同值）：一处生效、规范键落库、无影子行。
	if env := call(h.updateConfigs, "/api/v1/config/update",
		`{"IP":"10.0.0.1:1","ip":"10.0.0.1:1"}`); env.Code != 0 {
		t.Fatalf("consistent duplicate rejected: code=%d msg=%q", env.Code, env.Msg)
	}
	cfg, err = r.GetConfigByName("ip")
	if err != nil || cfg == nil || cfg.Value != "10.0.0.1:1" {
		t.Fatalf("consistent duplicate not applied once: %v / %v", cfg, err)
	}
	if shadow, _ := r.GetConfigByName("IP"); shadow != nil {
		t.Fatalf("shadow row persisted for uppercase key: %+v", shadow)
	}

	// 一写一清（意图不一致）：整批拒绝。
	if env := call(h.updateSingleConfig, "/api/v1/config/update-single",
		`{"name":"notify_webhook_url","value":"https://hook.example/abc"}`); env.Code != 0 {
		t.Fatalf("seed webhook: code=%d msg=%q", env.Code, env.Msg)
	}
	if env := call(h.updateConfigs, "/api/v1/config/update",
		`{"notify_webhook_url":"","NOTIFY_WEBHOOK_URL":"https://hook.example/other"}`); env.Code == 0 {
		t.Fatal("set-vs-clear collision accepted, want whole-batch rejection")
	}
	cfg, err = r.GetConfigByName("notify_webhook_url")
	if err != nil || cfg == nil || cfg.Value != "https://hook.example/abc" {
		t.Fatalf("webhook mutated by rejected set-vs-clear batch: %v / %v", cfg, err)
	}

	// 同为清空（意图一致）：一处生效、键被删除回落默认。
	if env := call(h.updateConfigs, "/api/v1/config/update",
		`{"notify_webhook_url":"","NOTIFY_WEBHOOK_URL":""}`); env.Code != 0 {
		t.Fatalf("consistent double-clear rejected: code=%d msg=%q", env.Code, env.Msg)
	}
	if cfg, _ := r.GetConfigByName("notify_webhook_url"); cfg != nil {
		t.Fatalf("double-clear did not delete the key: %+v", cfg)
	}
}
