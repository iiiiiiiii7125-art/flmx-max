package kernel

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClashRuleUser(t *testing.T) {
	cases := map[string]string{
		"auth_user=u2 => route(flmx-u2)":   "u2",
		"auth_user=u10 => route(flmx-u10)": "u10",
		"auth_user=u1 => route(flmx-u1)":   "u1",
		"direct":                           "",
		"":                                 "",
		"auth_user= => route(flmx-u)":      "",
	}
	for rule, want := range cases {
		if got := clashRuleUser(rule); got != want {
			t.Fatalf("clashRuleUser(%q) = %q, want %q", rule, got, want)
		}
	}
}

func TestClashAPIAddr(t *testing.T) {
	withAPI := `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:41234"}}}`
	if got := clashAPIAddr([]byte(withAPI)); got != "127.0.0.1:41234" {
		t.Fatalf("clashAPIAddr = %q, want 127.0.0.1:41234", got)
	}
	for _, noAPI := range []string{`{}`, `{"experimental":{}}`, `not json`, ``} {
		if got := clashAPIAddr([]byte(noAPI)); got != "" {
			t.Fatalf("clashAPIAddr(%q) = %q, want empty", noAPI, got)
		}
	}
}

const clashTestOldConfig = `{
  "inbounds": [{"tag": "in-2", "type": "shadowsocks", "users": [
    {"name": "u1", "password": "p1"},
    {"name": "u2", "password": "p2"},
    {"name": "u3", "password": "p3"}]}],
  "outbounds": [
    {"type": "direct", "tag": "direct"},
    {"type": "direct", "tag": "flmx-u1", "routing_mark": 1192689665},
    {"type": "direct", "tag": "flmx-u2", "routing_mark": 1192689666},
    {"type": "direct", "tag": "flmx-u3", "routing_mark": 1192689667}],
  "route": {"rules": [
    {"auth_user": ["u1"], "outbound": "flmx-u1"},
    {"auth_user": ["u2"], "outbound": "flmx-u2"},
    {"auth_user": ["u3"], "outbound": "flmx-u3"}]}
}`

const clashTestNewConfig = `{
  "inbounds": [{"tag": "in-2", "type": "shadowsocks", "users": [
    {"name": "u1", "password": "p1"},
    {"name": "u3", "password": "p3"}]}],
  "outbounds": [
    {"type": "direct", "tag": "direct"},
    {"type": "direct", "tag": "flmx-u1", "routing_mark": 1192689665},
    {"type": "direct", "tag": "flmx-u3", "routing_mark": 1192689667}],
  "route": {"rules": [
    {"auth_user": ["u1"], "outbound": "flmx-u1"},
    {"auth_user": ["u3"], "outbound": "flmx-u3"}]}
}`

func TestRemovedSingboxUsers(t *testing.T) {
	removed, err := removedSingboxUsers([]byte(clashTestOldConfig), []byte(clashTestNewConfig))
	if err != nil {
		t.Fatalf("removedSingboxUsers: %v", err)
	}
	if len(removed) != 1 || removed[0] != "u2" {
		t.Fatalf("removed = %v, want [u2]", removed)
	}
	// No removal when nothing changed.
	removed, err = removedSingboxUsers([]byte(clashTestOldConfig), []byte(clashTestOldConfig))
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed = %v err=%v, want empty", removed, err)
	}
}

func TestIsUserOnlySingboxChange(t *testing.T) {
	userOnly, err := isUserOnlySingboxChange([]byte(clashTestOldConfig), []byte(clashTestNewConfig))
	if err != nil || !userOnly {
		t.Fatalf("user-only change = %v err=%v, want true", userOnly, err)
	}
	// A port change is NOT user-only.
	changed := strings.Replace(clashTestNewConfig, `"in-2"`, `"in-9"`, 1)
	userOnly, err = isUserOnlySingboxChange([]byte(clashTestOldConfig), []byte(changed))
	if err != nil || userOnly {
		t.Fatalf("port change = %v err=%v, want false", userOnly, err)
	}
	// Identical configs are (trivially) user-only.
	userOnly, err = isUserOnlySingboxChange([]byte(clashTestOldConfig), []byte(clashTestOldConfig))
	if err != nil || !userOnly {
		t.Fatalf("identical = %v err=%v, want true", userOnly, err)
	}
}

func TestCloseClashUserConnections(t *testing.T) {
	// Stub Clash API: two live connections (u2 and u3), DELETE records ids.
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/connections" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"connections": []map[string]string{
				{"id": "conn-u2", "rule": "auth_user=u2 => route(flmx-u2)"},
				{"id": "conn-u3", "rule": "auth_user=u3 => route(flmx-u3)"},
				{"id": "conn-direct", "rule": "direct"},
			}})
			return
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/connections/") {
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/connections/"))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	closed, err := closeClashUserConnections(addr, []string{"u2"})
	if err != nil {
		t.Fatalf("closeClashUserConnections: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed = %d, want 1", closed)
	}
	if len(deleted) != 1 || deleted[0] != "conn-u2" {
		t.Fatalf("deleted = %v, want [conn-u2]", deleted)
	}

	// Unknown user closes nothing; empty addr is a no-op.
	closed, err = closeClashUserConnections(addr, []string{"u9"})
	if err != nil || closed != 0 {
		t.Fatalf("unknown user: closed=%d err=%v, want 0/nil", closed, err)
	}
	closed, err = closeClashUserConnections("", []string{"u2"})
	if err != nil || closed != 0 {
		t.Fatalf("empty addr: closed=%d err=%v, want 0/nil", closed, err)
	}
}

func holdPort(t *testing.T, port int) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("hold port %d: %v", port, err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

// R2C-01: the agent resolves the companion's effective Clash API port.
// A running instance is sticky; a fresh (re)start prefers the previous
// port, then the rendered port, then scans the range.
func TestResolveClashAPIAddr(t *testing.T) {
	// Empty rendered address means no Clash API is configured.
	if got, err := resolveClashAPIAddr("", "", false); err != nil || got != "" {
		t.Fatalf("empty rendered: got=%q err=%v, want empty/nil", got, err)
	}
	// Sticky: a running instance keeps its port even when the panel
	// rendered a different one — and even when that port is busy
	// (it is busy because WE hold it).
	holdPort(t, 41101)
	if got, err := resolveClashAPIAddr("127.0.0.1:41100", "127.0.0.1:41101", true); err != nil || got != "127.0.0.1:41101" {
		t.Fatalf("sticky: got=%q err=%v, want 127.0.0.1:41101", got, err)
	}
	// Fresh start prefers the previously used port when it is free.
	if got, err := resolveClashAPIAddr("127.0.0.1:41102", "127.0.0.1:41103", false); err != nil || got != "127.0.0.1:41103" {
		t.Fatalf("fresh prev free: got=%q err=%v, want 127.0.0.1:41103", got, err)
	}
	// Fresh start falls through to the rendered port when the
	// previous port is busy.
	holdPort(t, 41103)
	if got, err := resolveClashAPIAddr("127.0.0.1:41102", "127.0.0.1:41103", false); err != nil || got != "127.0.0.1:41102" {
		t.Fatalf("fresh prev busy: got=%q err=%v, want 127.0.0.1:41102", got, err)
	}
	// Fresh start scans upward from the rendered port when it is busy.
	holdPort(t, 41110)
	holdPort(t, 41111)
	if got, err := resolveClashAPIAddr("127.0.0.1:41110", "", false); err != nil || got != "127.0.0.1:41112" {
		t.Fatalf("scan: got=%q err=%v, want 127.0.0.1:41112", got, err)
	}
	// A rendered port outside the range (the old 40900 from an older
	// panel) is never kept: the result is always inside the range.
	if got, err := resolveClashAPIAddr("127.0.0.1:40900", "", false); err != nil {
		t.Fatalf("old 40900: err=%v", err)
	} else if port, ok := clashAPIPort(got); !ok || !inClashAPIPortRange(port) || port == 40900 {
		t.Fatalf("old 40900: got=%q, want an in-range port", got)
	}
	// A running instance with no previous address probes the rendered
	// port (free here).
	if got, err := resolveClashAPIAddr("127.0.0.1:41104", "", true); err != nil || got != "127.0.0.1:41104" {
		t.Fatalf("running no prev: got=%q err=%v, want 127.0.0.1:41104", got, err)
	}
}

// R2C-01: rewriting the Clash API address changes only that address.
func TestRewriteClashAPIAddr(t *testing.T) {
	src := `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:40900"}},"inbounds":[]}`
	out, err := rewriteClashAPIAddr([]byte(src), "127.0.0.1:41234")
	if err != nil {
		t.Fatalf("rewriteClashAPIAddr: %v", err)
	}
	if got := clashAPIAddr(out); got != "127.0.0.1:41234" {
		t.Fatalf("rewritten addr = %q, want 127.0.0.1:41234", got)
	}
	var got, want map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("parse rewritten: %v", err)
	}
	if err := json.Unmarshal([]byte(src), &want); err != nil {
		t.Fatalf("parse source: %v", err)
	}
	delete(got["experimental"].(map[string]interface{}), "clash_api")
	delete(want["experimental"].(map[string]interface{}), "clash_api")
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("rewrite changed more than the address:\n%s\n%s", gj, wj)
	}
	// Missing experimental section is created.
	out, err = rewriteClashAPIAddr([]byte(`{"inbounds":[]}`), "127.0.0.1:41234")
	if err != nil {
		t.Fatalf("rewriteClashAPIAddr (no experimental): %v", err)
	}
	if got := clashAPIAddr(out); got != "127.0.0.1:41234" {
		t.Fatalf("rewritten addr = %q, want 127.0.0.1:41234", got)
	}
	// Invalid JSON errors.
	if _, err := rewriteClashAPIAddr([]byte(`not json`), "127.0.0.1:41234"); err == nil {
		t.Fatalf("rewriteClashAPIAddr (invalid JSON) = nil error, want error")
	}
}

func withClashAPI(t *testing.T, configJSON, addr string) string {
	t.Helper()
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if addr == "" {
		delete(cfg, "experimental")
	} else {
		cfg["experimental"] = map[string]interface{}{"clash_api": map[string]interface{}{"external_controller": addr}}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return string(b)
}

// R2C-01: the Clash API address is not user state — configs differing
// only by it (or by its presence) normalize equal, so a port change
// alone never turns a user-only change into a reload.
func TestNormalizeSingboxConfigStripsClashAPI(t *testing.T) {
	old := withClashAPI(t, clashTestOldConfig, "127.0.0.1:41100")
	newSameUsers := withClashAPI(t, clashTestOldConfig, "127.0.0.1:41101")
	userOnly, err := isUserOnlySingboxChange([]byte(old), []byte(newSameUsers))
	if err != nil || !userOnly {
		t.Fatalf("port-only change: userOnly=%v err=%v, want true", userOnly, err)
	}
	noAPI := withClashAPI(t, clashTestOldConfig, "")
	userOnly, err = isUserOnlySingboxChange([]byte(old), []byte(noAPI))
	if err != nil || !userOnly {
		t.Fatalf("API presence change: userOnly=%v err=%v, want true", userOnly, err)
	}
	// A user removal plus a port change is still user-only.
	newRemoved := withClashAPI(t, clashTestNewConfig, "127.0.0.1:41101")
	userOnly, err = isUserOnlySingboxChange([]byte(old), []byte(newRemoved))
	if err != nil || !userOnly {
		t.Fatalf("removal + port change: userOnly=%v err=%v, want true", userOnly, err)
	}
	// A non-user change is still not user-only.
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(clashTestOldConfig), &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg["outbounds"] = []interface{}{map[string]interface{}{"type": "direct", "tag": "other"}}
	b, _ := json.Marshal(cfg)
	userOnly, err = isUserOnlySingboxChange([]byte(old), b)
	if err != nil || userOnly {
		t.Fatalf("outbound change: userOnly=%v err=%v, want false", userOnly, err)
	}
}
