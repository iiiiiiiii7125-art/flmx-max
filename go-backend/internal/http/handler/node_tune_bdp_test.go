package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Bad requests return before any node lookup or command send, so a bare
// Handler{} is enough here (same shape as upstream 3.3.42's node_tune
// test, plus this fork's allowlist case).
func TestNodeTuneBdpRejectsBadRequests(t *testing.T) {
	for body, want := range map[string]string{
		`{"id":0}`:                                     "参数错误",
		`{"id":1,"action":"apply"}`:                    "至少选择",
		`{"id":1,"action":"reboot"}`:                   "未知操作",
		`{"id":1,"extra":true}`:                        "参数错误",
		`{"id":1,"bandwidthMbps":1000,"rttMs":0}`:      "延迟需在",
		`{"id":1,"bandwidthMbps":0,"rttMs":150}`:       "带宽需在",
		`{"id":1,"bandwidthMbps":100001,"rttMs":150}`:  "带宽需在",
		`{"id":1,"bandwidthMbps":1000,"rttMs":2001}`:   "延迟需在",
		`{"id":1,"action":"apply","groups":["rm-rf"]}`: "未知调优项",
	} {
		rec := httptest.NewRecorder()
		(&Handler{}).nodeTune(rec, httptest.NewRequest(http.MethodPost, "/api/v1/node/tune", strings.NewReader(body)))
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %s", body, rec.Body.String())
		}
	}
}

// The version gate is deliberately NOT upstream's 3.3.42: our agent line
// ships with 1.4.5, so that is the floor. Unknown / unparseable / dev
// versions fail closed (no BDP values sent), mirroring
// agentSupportsNoPrivateBypass.
func TestAgentSupportsBdpTuning(t *testing.T) {
	for _, c := range []struct {
		version string
		want    bool
	}{
		{"1.4.5", true},
		{"1.4.5 (debian/amd64)", true},
		{"v1.4.5", true},
		{"1.4.6", true},
		{"1.5.0", true},
		{"2.0.0", true},
		{"1.4.4", false},
		{"1.4.4 (debian/amd64)", false},
		{"1.3.4", false},
		{"3.3.42", true}, // numerically above the floor; the gate is ours, not upstream's
		{"", false},
		{"dev", false},
		{"dev (linux/amd64)", false},
		{"1.4.5-beta1", false},
		{"garbage", false},
	} {
		if got := agentSupportsBdpTuning(c.version); got != c.want {
			t.Errorf("%q: got %v want %v", c.version, got, c.want)
		}
	}
}
