package kernel

import (
	"encoding/json"
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
		"direct":                            "",
		"":                                  "",
		"auth_user= => route(flmx-u)":       "",
	}
	for rule, want := range cases {
		if got := clashRuleUser(rule); got != want {
			t.Fatalf("clashRuleUser(%q) = %q, want %q", rule, got, want)
		}
	}
}

func TestClashAPIAddr(t *testing.T) {
	withAPI := `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:40900"}}}`
	if got := clashAPIAddr([]byte(withAPI)); got != "127.0.0.1:40900" {
		t.Fatalf("clashAPIAddr = %q, want 127.0.0.1:40900", got)
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
