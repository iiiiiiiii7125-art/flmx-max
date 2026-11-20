package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// R18-B5-01: subscriptionPreview builds its formatSkipped map from
// req.Options.options(), which never sets SingboxSnell/SingboxNaive,
// so the sing-box predicate reports Snell/Naive nodes as unsupported
// even though sing-box 1.14+/1.13+ renders them (the list NodeCount
// path sets both flags for sing-box). Preview must use the same
// caliber as the count path.
func TestR18B501PreviewFormatSkippedSingboxIncludesNaive(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/sub_preview_b501.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "b5-01-node", "203.0.113.10")
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	seedChainInbound(t, r, box, "b5-01-naive", kernel.Inbound{Protocol: kernel.ProtocolNaive, Port: 443,
		Settings: kernel.Settings{Username: "nu", Password: "naive-pass-1"},
		Security: kernel.Security{Type: kernel.SecurityTLS, CertMode: kernel.CertModeACME, SNI: "nv.example.com", ACMEEmail: "me@example.com"}}, 0)
	seedChainInbound(t, r, box, "b5-01-trojan", kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 8443,
		Settings: kernel.Settings{Password: "trojan-secret"},
		Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "t.example.com"}}, 0)
	_ = time.Now()

	req := httptest.NewRequest("POST", "/api/v1/subscription/preview", strings.NewReader(`{"name":"preview","sourceAll":true}`))
	rec := httptest.NewRecorder()
	h.subscriptionPreview(rec, req)
	var env struct {
		Code int `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			FormatSkipped map[string][]string `json:"formatSkipped"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode preview: %v (body=%s)", err, rec.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("preview must succeed, got code=%d msg=%q body=%s", env.Code, env.Msg, rec.Body.String())
	}
	foundNaive := false
	for _, it := range env.Data.Items {
		if it.Name == "b5-01-naive" {
			foundNaive = true
		}
	}
	if !foundNaive {
		t.Fatalf("fixture must render the naive node into preview items, got %+v body=%s", env.Data.Items, rec.Body.String())
	}
	if skipped := env.Data.FormatSkipped["singbox"]; len(skipped) > 0 {
		t.Fatalf("preview must not report sing-box as skipping supported nodes, got singbox skipped=%v (full map=%v)", skipped, env.Data.FormatSkipped)
	}
}
