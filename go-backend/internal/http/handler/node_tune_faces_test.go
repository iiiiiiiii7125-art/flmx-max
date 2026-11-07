package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

// Four-face model (vpsing tcp.sh lineage): the panel endpoint
// accepts the status read, the five closed-enum face actions and
// the narrowed line probe. Every retired action of the old model
// (manual line apply with groups, auto/deep jobs, cancel, both
// restores) and every retired request field (strict decodeJSON)
// must be rejected before any node traffic — bad requests return
// before any node lookup or command send, so a bare Handler{} is
// enough for the rejection cases.
func TestNodeTuneRejectsRetiredActionsAndFields(t *testing.T) {
	for body, want := range map[string]string{
		`{"id":0}`: "参数错误",
		// decodeJSON is strict (DisallowUnknownFields): any field
		// outside {id, action, referenceHost} — including every
		// retired old-model field — is a parameter error before
		// any dispatch. This is also what retires the probe's
		// speed leg at the panel boundary: withSpeed /
		// speedtestURL no longer parse at all.
		`{"id":1,"extra":true}`:                                            "参数错误",
		`{"id":1,"action":"status","groups":["bbr"]}`:                      "参数错误",
		`{"id":1,"action":"status","bandwidthMbps":1000,"rttMs":150}`:      "参数错误",
		`{"id":1,"action":"probe","withSpeed":true}`:                       "参数错误",
		`{"id":1,"action":"probe","speedtestURL":"https://8.8.8.8/f.bin"}`: "参数错误",
		// Retired actions of the old model (bare form: with the
		// retired groups field attached they fail even earlier, at
		// strict decode — covered above).
		`{"id":1,"action":"apply"}`:                       "未知操作",
		`{"id":1,"action":"auto"}`:                        "未知操作",
		`{"id":1,"action":"deep","maxTrafficGB":10}`:      "参数错误", // maxTrafficGB itself is a retired field
		`{"id":1,"action":"deep"}`:                        "未知操作",
		`{"id":1,"action":"cancel"}`:                      "未知操作",
		`{"id":1,"action":"restore"}`:                     "未知操作",
		`{"id":1,"action":"restoreAuto"}`:                 "未知操作",
		`{"id":1,"action":"reboot"}`:                      "未知操作",
		`{"id":1,"action":"apply-ipv4","faceAction":"x"}`: "参数错误", // faceAction is panel-internal, never a request field
	} {
		rec := httptest.NewRecorder()
		(&Handler{}).nodeTune(rec, httptest.NewRequest(http.MethodPost, "/api/v1/node/tune", strings.NewReader(body)))
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: got %s, want substring %q", body, rec.Body.String(), want)
		}
	}
}

// The five face actions, the status read and the probe must pass
// local validation and reach dispatch: with a seeded node but no
// agent socket connected, the call fails at dispatch ("调优失败"),
// which is exactly how we know the action was accepted. (The wire
// payload itself — "TuneFaces" + {"faceAction": action}, "TuneProbe"
// + optional {"referenceHost"} — is the T1 contract; what is
// asserted at this boundary is that exactly the contracted actions
// get this far and nothing else does, see the rejection test.)
func TestNodeTuneActionsReachDispatch(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "tune-faces.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "tune-faces-node", "198.51.100.23")

	call := func(payload map[string]interface{}) (int, string) {
		payload["id"] = nodeID
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

	for _, payload := range []map[string]interface{}{
		{}, // empty action = status read
		{"action": "status"},
		{"action": "apply-ipv4"},
		{"action": "apply-bbr"},
		{"action": "apply-kernel"},
		{"action": "apply-nic"},
		{"action": "rollback"},
		{"action": "probe"}, // no referenceHost: agent auto-fills the panel address
		{"action": "probe", "referenceHost": "8.8.8.8:443"}, // public literal passes R7-C-02
	} {
		code, msg := call(payload)
		if code == 0 {
			t.Errorf("payload %v: unexpectedly succeeded without a connected agent", payload)
		}
		if strings.Contains(msg, "未知操作") || strings.Contains(msg, "请求参数错误") || strings.Contains(msg, "已拒绝") {
			t.Errorf("payload %v: rejected locally instead of dispatched: code=%d msg=%q", payload, code, msg)
		}
		if !strings.Contains(msg, "调优失败") {
			t.Errorf("payload %v: want dispatch failure 调优失败, got code=%d msg=%q", payload, code, msg)
		}
	}
}

// PORT-REVIEW D4U2 successor: the old "restore" (TuneApply
// restore=true, sysctl-only, kept the shaper) and "restoreAuto"
// (TuneStart mode "restore") are both retired actions now. The
// four-face model's rollback is a different operation (delete the
// tuning files, force cubic/pfifo_fast, remove the MSS clamp,
// zero RPS, restore gai.conf) and is dispatched as a face action —
// the reach-dispatch test above pins that it is accepted.
func TestNodeTuneRestoreActionsRetired(t *testing.T) {
	for _, action := range []string{"restore", "restoreAuto"} {
		rec := httptest.NewRecorder()
		(&Handler{}).nodeTune(rec, httptest.NewRequest(http.MethodPost, "/api/v1/node/tune",
			strings.NewReader(`{"id":1,"action":"`+action+`"}`)))
		if !strings.Contains(rec.Body.String(), "未知操作") {
			t.Errorf("action %s: got %s, want 未知操作 rejection", action, rec.Body.String())
		}
	}
}
