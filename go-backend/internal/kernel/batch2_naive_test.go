// Supplementary to batch 2 of the FLMX 1.7.1 post-release full audit
// (baseline main @ c2f45a6): parseURI has no naive case, so a naive://
// link — including the panel's own B-06 render output
// (naive://user:pass@host:port) — is rejected with "暂不支持", and
// the import round-trip never closes. Written red first.
package kernel

import (
	"strings"
	"testing"
)

func TestBatch2NaiveLinkImport(t *testing.T) {
	// Plain form.
	n := mustImportOne(t, "naive://alice:s3cret@203.0.113.10:443#Naive1")
	p := n.Proxy
	if p.Type() != "naive" {
		t.Errorf("type = %q, want naive", p.Type())
	}
	if got := p.str("username"); got != "alice" {
		t.Errorf("username = %q, want alice", got)
	}
	if got := p.str("password"); got != "s3cret" {
		t.Errorf("password = %q, want s3cret", got)
	}
	if n.Host != "203.0.113.10" || n.Port != 443 || n.Name != "Naive1" {
		t.Errorf("node = %+v, want host 203.0.113.10:443 name Naive1", n)
	}
	// URL-escaped form, exactly as the B-06 renderer emits it
	// (PathEscape on both userinfo parts).
	n = mustImportOne(t, "naive://alice:p%40ss%3Aw0rd@203.0.113.10:443#Naive2")
	if got := n.Proxy.str("username"); got != "alice" {
		t.Errorf("escaped username = %q, want alice", got)
	}
	if got := n.Proxy.str("password"); got != "p@ss:w0rd" {
		t.Errorf("escaped password = %q, want p@ss:w0rd", got)
	}
	// Query SNI is deliberately not wired yet (per task scope): the
	// link still imports, but no server name is recorded.
	n = mustImportOne(t, "naive://alice:s3cret@203.0.113.10:443?sni=example.com#Naive3")
	if got := n.Proxy.serverName(); got != "" {
		t.Errorf("naive query sni must not be wired yet, serverName = %q", got)
	}
}

func TestBatch2NaiveLinkRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		link string
		want string
	}{
		{"missing password", "naive://alice@203.0.113.10:443#x", "缺少用户名或密码"},
		{"missing username", "naive://:pw@203.0.113.10:443#x", "缺少用户名或密码"},
		{"no userinfo", "naive://203.0.113.10:443#x", "缺少用户名或密码"},
		{"empty-label host", "naive://alice:pw@a..b:443#x", `地址 "a..b" 不正确`},
		{"bad port", "naive://alice:pw@203.0.113.10:70000#x", "端口不正确"},
	} {
		nodes, errs := ParseImport(tc.link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 0 nodes with an error", tc.name, len(nodes), errs)
			continue
		}
		if !strings.Contains(errs[0], tc.want) {
			t.Errorf("%s: err = %q, want contains %q", tc.name, errs[0], tc.want)
		}
	}
}
