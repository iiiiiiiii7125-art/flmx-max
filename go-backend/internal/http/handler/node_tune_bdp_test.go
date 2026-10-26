package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Bad requests return before any node lookup or command send, so a bare
// Handler{} is enough here (same shape as upstream 3.3.42's node_tune
// test, plus this fork's allowlist case and probe validation cases).
func TestNodeTuneBdpRejectsBadRequests(t *testing.T) {
	for body, want := range map[string]string{
		`{"id":0}`:                                                      "参数错误",
		`{"id":1,"action":"apply"}`:                                     "至少选择",
		`{"id":1,"action":"reboot"}`:                                    "未知操作",
		`{"id":1,"extra":true}`:                                         "参数错误",
		`{"id":1,"bandwidthMbps":1000,"rttMs":0}`:                       "延迟需在",
		`{"id":1,"bandwidthMbps":0,"rttMs":150}`:                        "带宽需在",
		`{"id":1,"bandwidthMbps":100001,"rttMs":150}`:                   "带宽需在",
		`{"id":1,"bandwidthMbps":1000,"rttMs":2001}`:                    "延迟需在",
		`{"id":1,"action":"apply","groups":["rm-rf"]}`:                  "未知调优项",
		`{"id":1,"action":"probe","speedtestURL":"ftp://x/f"}`:          "测速地址须为",
		`{"id":1,"action":"probe","speedtestURL":"file:///etc/passwd"}`: "测速地址须为",
		`{"id":1,"action":"probe","referenceHost":"example.com"}`:       "host:port",
		// Deep-scan budget (upstream 3.3.43 TuneStart mode "deep"):
		// 0 < maxTrafficGB <= 1000, checked before any node traffic.
		`{"id":1,"action":"deep","maxTrafficGB":0}`:    "流量预算",
		`{"id":1,"action":"deep","maxTrafficGB":-5}`:   "流量预算",
		`{"id":1,"action":"deep","maxTrafficGB":1001}`: "流量预算",
	} {
		rec := httptest.NewRecorder()
		(&Handler{}).nodeTune(rec, httptest.NewRequest(http.MethodPost, "/api/v1/node/tune", strings.NewReader(body)))
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %s", body, rec.Body.String())
		}
	}
}

// Capability probe (the gate, replacing B2's hard 1.4.5 version
// comparison, which was deleted): support is judged only by whether
// the agent's TuneStatus response carries a params field, with exactly
// upstream's lineSupported = Boolean(status?.params) semantics. No
// agent version string is consulted anywhere in this path, so there is
// no version table to maintain: an old agent (no params key) and a new
// agent (params object, even an empty one, which is truthy in JS) are
// told apart by the response shape alone.
func TestTuneLineSupported(t *testing.T) {
	for _, c := range []struct {
		name string
		data map[string]interface{}
		want bool
	}{
		{"new agent with line", map[string]interface{}{"params": map[string]interface{}{"bandwidthMbps": 1000, "rttMs": 150}}, true},
		{"new agent empty params object is truthy", map[string]interface{}{"params": map[string]interface{}{}}, true},
		{"old agent without params key", map[string]interface{}{"kernel": "6.1.0", "groups": []interface{}{}}, false},
		{"params explicitly null", map[string]interface{}{"params": nil}, false},
		{"nil response", nil, false},
		{"empty response", map[string]interface{}{}, false},
	} {
		if got := tuneLineSupported(c.data); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
