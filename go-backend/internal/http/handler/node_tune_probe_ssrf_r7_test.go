package handler

// R7-C-02 (panel side): the retained probe branch must judge the
// reference host with netguard before anything is dispatched to a
// node — hard-deny layer always, private ranges rejected by
// default. Tests use IP literals only, so they do not depend on
// DNS behaviour.
//
// The speedtest-URL leg of this test file retired with the leg
// itself (2026-11, four-face tuning model): its only consumer was
// the retired BDP sizing derivation, the panel no longer accepts
// speedtestURL at all (strict decode rejects the field — see
// node_tune_faces_test.go), and validateSpeedtestURLDispatch was
// removed with it. The referenceHost leg keeps the full R7-C-02
// treatment.

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

func r7ProbeCall(t *testing.T) func(map[string]interface{}) (int, string) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "tune-probe-ssrf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "tune-ssrf-node", "198.51.100.22")
	return func(payload map[string]interface{}) (int, string) {
		payload["id"] = nodeID
		payload["action"] = "probe"
		res := httptest.NewRecorder()
		h.nodeTune(res, newAuthenticatedJSONRequest(t, payload))
		var env struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v body=%s", err, res.Body.String())
		}
		return env.Code, env.Msg
	}
}

func TestR7C02PanelProbeRejectsNonPublicReferenceHost(t *testing.T) {
	call := r7ProbeCall(t)
	for _, ref := range []string{
		"127.0.0.1:8080",
		"169.254.169.254:80",
		"10.1.2.3:443",
		"[64:ff9b::a9fe:a9fe]:80",
		"8.8.8.8:0",     // port below range
		"8.8.8.8:70000", // port above range
	} {
		code, msg := call(map[string]interface{}{"referenceHost": ref})
		if code == 0 || !strings.Contains(msg, "已拒绝") {
			t.Errorf("referenceHost %s: code=%d msg=%q, want rejection before dispatch", ref, code, msg)
		}
	}
}

func TestR7C02PanelProbeAllowsPublicTargets(t *testing.T) {
	call := r7ProbeCall(t)
	// A public literal must pass the policy gate; with no node
	// socket connected the call then fails at dispatch ("调优失败"),
	// which is exactly how we know validation let it through.
	for _, payload := range []map[string]interface{}{
		{"referenceHost": "8.8.8.8:443"},
	} {
		_, msg := call(payload)
		if strings.Contains(msg, "已拒绝") {
			t.Errorf("payload %v: public target must not be policy-rejected, msg=%q", payload, msg)
		}
	}
}
