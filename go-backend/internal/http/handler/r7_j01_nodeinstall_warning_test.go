package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R7-J-01 (non-BC parts only): when nodeInstall generates a command whose
// agent address is ws:// (plaintext), the API response must carry an
// explicit plaintext warning in the envelope "warning" field, and the
// generated command string itself must stay byte-identical to the
// pre-fix behaviour. Part 1 (changing the bare-value default scheme to
// wss) was DECLINED by the user's 2026-10-29 decision ("do not change
// the default; warning + copy only"): the residual risk — a bare
// stored value plus a plain-http admin session can still generate a
// ws:// command for a TLS panel, now with an explicit plaintext
// warning at generation time — is user-accepted, so the last test in
// this file LOCKS the current normalizeNodeInstallAddr behaviour.

type r7J01Envelope struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Data    string `json:"data"`
	Warning string `json:"warning"`
}

func r7J01CallNodeInstall(t *testing.T, panelAddr string) r7J01Envelope {
	t.Helper()
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	r, err := repo.Open(t.TempDir() + "/r7j01.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, is_remote, remote_url, remote_token, remote_config)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "r7j01-node", "r7j01-secret", "10.90.80.72", "10.90.80.72", "", "31000-31099", "", "v1", 1, 1, 1, now, now, 1, "[::]", "[::]", 0, 0, "", "", "").Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	nodeID := mustLastInsertID(t, r, "r7j01-node")
	if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, "ip", panelAddr, now).Error; err != nil {
		t.Fatalf("seed vite_config ip: %v", err)
	}
	if err := r.UpsertConfig("github_proxy_enabled", "false", now); err != nil {
		t.Fatalf("disable github proxy: %v", err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"1.7.2","name":"x","draft":false,"prerelease":false}]`)
	}))
	t.Cleanup(api.Close)
	swapGitHubBasesForTest(t, api.URL, api.URL)

	h := New(r, "test-jwt-secret")
	raw, _ := json.Marshal(map[string]interface{}{"id": nodeID, "channel": "stable"})
	// Plain http management request, untrusted remote: panelRequestScheme
	// resolves to "http" (the 2026-10-29 incident shape).
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/install", bytes.NewReader(raw))
	res := httptest.NewRecorder()
	h.nodeInstall(res, req)
	var env r7J01Envelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode nodeInstall response: %v", err)
	}
	if env.Code != 0 {
		t.Fatalf("nodeInstall must succeed, got code=%d msg=%s", env.Code, env.Msg)
	}
	return env
}

func TestR7J01PlaintextInstallCarriesWarning(t *testing.T) {
	// Incident cell A: bare stored address + plain http management
	// request -> ws:// command (behaviour unchanged in this batch) AND
	// an explicit plaintext warning in the response envelope.
	env := r7J01CallNodeInstall(t, "z.0v0.dog")
	if !strings.Contains(env.Data, "-a ws://z.0v0.dog ") {
		t.Fatalf("command must keep the ws:// agent address in this batch, got %q", env.Data)
	}
	if env.Warning == "" {
		t.Fatalf("ws:// install command must carry an explicit plaintext warning, got empty warning (cmd=%q)", env.Data)
	}
	if !strings.Contains(env.Warning, "明文") || !strings.Contains(env.Warning, "ws://") {
		t.Fatalf("warning must name the plaintext ws:// scheme, got %q", env.Warning)
	}
	if !strings.Contains(env.Warning, "https://") {
		t.Fatalf("warning must point at the https:// fix for TLS panels, got %q", env.Warning)
	}
}

func TestR7J01ExplicitPlaintextInstallCarriesWarning(t *testing.T) {
	// Incident cell B: explicit ws:// stored value -> warning as well.
	env := r7J01CallNodeInstall(t, "ws://z.0v0.dog")
	if !strings.Contains(env.Data, "-a ws://z.0v0.dog ") {
		t.Fatalf("command must keep the ws:// agent address, got %q", env.Data)
	}
	if env.Warning == "" {
		t.Fatalf("explicit ws:// install must carry a plaintext warning")
	}
}

func TestR7J01TLSInstallHasNoWarning(t *testing.T) {
	env := r7J01CallNodeInstall(t, "https://z.0v0.dog")
	if !strings.Contains(env.Data, "-a wss://z.0v0.dog ") {
		t.Fatalf("https stored address must generate wss://, got %q", env.Data)
	}
	if env.Warning != "" {
		t.Fatalf("wss:// install must not carry a plaintext warning, got %q", env.Warning)
	}
}

// TestR7J01NormalizeDefaultUnchanged locks the behaviour the user
// chose to keep (2026-10-29: the bare-value default change was
// declined, warning + copy only): normalizeNodeInstallAddr
// (bare-value default + explicit-scheme handling) is byte-for-byte
// identical to baseline 69bfcc4. Changing any case here re-opens a
// decision the user has already made.
func TestR7J01NormalizeDefaultUnchanged(t *testing.T) {
	cases := []struct{ addr, scheme, want string }{
		{"z.0v0.dog", "http", "ws://z.0v0.dog"},
		{"z.0v0.dog", "https", "wss://z.0v0.dog"},
		{"z.0v0.dog:443", "http", "ws://z.0v0.dog:443"},
		{"http://z.0v0.dog", "https", "ws://z.0v0.dog"},
		{"ws://z.0v0.dog", "https", "ws://z.0v0.dog"},
		{"https://z.0v0.dog", "http", "wss://z.0v0.dog"},
		{"wss://z.0v0.dog", "http", "wss://z.0v0.dog"},
	}
	for _, tc := range cases {
		if got := normalizeNodeInstallAddr(tc.addr, tc.scheme); got != tc.want {
			t.Errorf("normalizeNodeInstallAddr(%q, %q) = %q, want %q (user-declined default change: default must stay unchanged)", tc.addr, tc.scheme, got, tc.want)
		}
	}
}
