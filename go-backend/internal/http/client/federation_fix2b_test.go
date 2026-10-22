package client

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// FIX2-B（V5-3）：Connect 携带本方 panelVersion（X-Panel-Version 头），
// 并解析提供方回执的 panelVersion / versionWarning——旧消费方 + 新
// 提供方的静默断裂格从此在协议面可见。
func TestFix2BConnectCarriesPanelVersion(t *testing.T) {
	var gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("X-Panel-Version")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"msg":  "ok",
			"data": map[string]interface{}{
				"shareId": 7, "nodeName": "peer", "serverIp": "203.0.113.1", "status": 1,
				"panelVersion":   "1.3.4",
				"versionWarning": "consumer panel version is below 1.3.4",
			},
		})
	}))
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	stubFederationLookup(t, map[string][]net.IP{
		"provider.test": {net.ParseIP("127.0.0.1")},
	})
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	t.Setenv("FLUX_VERSION", "1.3.4")

	fc := NewFederationClient()
	info, err := fc.Connect("http://provider.test:"+strconv.Itoa(port), "token", "")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if gotVersion != "1.3.4" {
		t.Fatalf("V5-3: X-Panel-Version header = %q, want %q", gotVersion, "1.3.4")
	}
	if info.PanelVersion != "1.3.4" {
		t.Fatalf("V5-3: info.PanelVersion = %q, want %q", info.PanelVersion, "1.3.4")
	}
	if info.VersionWarning != "consumer panel version is below 1.3.4" {
		t.Fatalf("V5-3: info.VersionWarning = %q", info.VersionWarning)
	}
}
