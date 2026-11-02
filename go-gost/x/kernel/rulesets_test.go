package kernel

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeSRS builds a synthetic rule-set file in the published .srs
// layout: magic "SRS", one version byte, one zlib stream.
func makeSRS(t *testing.T, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("SRS")
	buf.WriteByte(1)
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestValidateSRSBytes(t *testing.T) {
	good := makeSRS(t, "compiled rules payload")
	if err := validateSRSBytes(good); err != nil {
		t.Fatalf("valid SRS rejected: %v", err)
	}
	cases := map[string][]byte{
		"html landing page": []byte("<!DOCTYPE html><html><body>mirror home</body></html>"),
		"truncated stream":  good[:len(good)-6],
		"bad magic":         append([]byte("XXXX"), good[4:]...),
		"bad version":       {byte('S'), byte('R'), byte('S'), 0xEE, 0x78, 0xda, 0x01},
		"empty payload":     makeSRS(t, ""),
		"too small":         []byte("SRS"),
	}
	for name, b := range cases {
		if err := validateSRSBytes(b); err == nil {
			t.Errorf("%s: expected rejection, got nil", name)
		}
	}
}

const ruleSetTestConfig = `{
  "log": {"level": "warn"},
  "inbounds": [],
  "outbounds": [{"type": "direct", "tag": "direct"}],
  "route": {
    "final": "direct",
    "rules": [{"rule_set": ["geosite-cn", "geoip-cn"], "outbound": "direct"}],
    "rule_set": [
      {"type": "remote", "tag": "geosite-cn", "format": "binary", "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs", "http_client": "rule-set"},
      {"type": "remote", "tag": "geoip-cn", "format": "binary", "url": "https://example.invalid/mirror/https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs"},
      {"type": "local", "tag": "already-local", "format": "binary", "path": "/elsewhere/x.srs"},
      {"type": "remote", "tag": "source-form", "format": "source", "url": "https://raw.githubusercontent.com/x/y.json"}
    ]
  },
  "experimental": {"cache_file": {"enabled": true, "path": "cache.db"}},
  "http_clients": [{"tag": "rule-set"}]
}`

func TestExtractRemoteRuleSets(t *testing.T) {
	_, sets, err := extractRemoteRuleSets(ruleSetTestConfig)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(sets) != 2 {
		t.Fatalf("want 2 remote binary sets, got %d", len(sets))
	}
	if sets[0].tag != "geosite-cn" || sets[1].tag != "geoip-cn" {
		t.Fatalf("unexpected tags: %q, %q", sets[0].tag, sets[1].tag)
	}
	// The panel-attached mirror prefix is normalized away; routing is
	// decided by the local plan, never by the descriptor.
	want := "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs"
	if sets[1].directURL != want {
		t.Fatalf("directURL not normalized: %q", sets[1].directURL)
	}
}

func newRuleSetManager(t *testing.T) (*Manager, *instance, string) {
	t.Helper()
	base := t.TempDir()
	m := NewManager(base)
	inst := &instance{logs: &logRing{}}
	m.instances["singbox"] = inst
	return m, inst, filepath.Join(base, "singbox", "run")
}

// configWithURL renders the test config pointing its first rule set
// at url (the second set is dropped to keep the fixture focused).
func configWithURL(t *testing.T, url string) string {
	t.Helper()
	cfg := strings.Replace(ruleSetTestConfig,
		"https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs", url, 1)
	// Remove the geoip definition (its URL is unreachable in tests).
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(cfg), &root); err != nil {
		t.Fatal(err)
	}
	route := root["route"].(map[string]interface{})
	defs := route["rule_set"].([]interface{})
	route["rule_set"] = defs[:1]
	out, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestLocalizeDownloadsAndRewrites(t *testing.T) {
	payload := makeSRS(t, "geosite-cn compiled rules")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	m, inst, runDir := newRuleSetManager(t)
	req := ApplyRequest{Type: "singbox", Hash: "h1", Files: []File{{Name: "config.json", Content: configWithURL(t, srv.URL+"/geosite-cn.srs")}}}
	files, err := m.localizeSingboxRuleSets(inst, req, runDir)
	if err != nil {
		t.Fatalf("localize: %v", err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(files[0].Content), &root); err != nil {
		t.Fatalf("rewritten config does not parse: %v", err)
	}
	def := root["route"].(map[string]interface{})["rule_set"].([]interface{})[0].(map[string]interface{})
	if def["type"] != "local" || def["format"] != "binary" || def["tag"] != "geosite-cn" {
		t.Fatalf("definition not localized: %v", def)
	}
	if _, ok := def["url"]; ok {
		t.Fatalf("remote url survived rewrite: %v", def)
	}
	if _, ok := def["http_client"]; ok {
		t.Fatalf("http_client survived rewrite: %v", def)
	}
	path, _ := def["path"].(string)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("local rule-set file missing: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("local rule-set content mismatch")
	}
	if !strings.HasPrefix(path, filepath.Join(runDir, ruleSetDirName)) {
		t.Fatalf("rule set not under run dir: %q", path)
	}
}

func TestLocalizeRejectsFakeAliveContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<!DOCTYPE html><html>landing</html>"))
	}))
	defer srv.Close()

	m, inst, runDir := newRuleSetManager(t)
	req := ApplyRequest{Type: "singbox", Hash: "h1", Files: []File{{Name: "config.json", Content: configWithURL(t, srv.URL+"/geosite-cn.srs")}}}
	if _, err := m.localizeSingboxRuleSets(inst, req, runDir); err == nil {
		t.Fatal("HTML content with no local copy must fail the localization")
	}
	entries, _ := os.ReadDir(filepath.Join(runDir, ruleSetDirName))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".srs") {
			t.Fatalf("fake content left a rule-set file behind: %s", e.Name())
		}
	}
}

func TestLocalizeKeepsCopyWhenRefreshFails(t *testing.T) {
	payload := makeSRS(t, "previous good copy")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	m, inst, runDir := newRuleSetManager(t)
	url := srv.URL + "/geosite-cn.srs"
	dir := filepath.Join(runDir, ruleSetDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	set := &remoteRuleSet{tag: "geosite-cn", directURL: url}
	if err := os.WriteFile(set.localPath(dir), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	inst.req.Hash = "old-hash" // config changed: a refresh is attempted
	req := ApplyRequest{Type: "singbox", Hash: "new-hash", Files: []File{{Name: "config.json", Content: configWithURL(t, url)}}}
	files, err := m.localizeSingboxRuleSets(inst, req, runDir)
	if err != nil {
		t.Fatalf("refresh failure with a usable copy must not fail: %v", err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(files[0].Content), &root); err != nil {
		t.Fatal(err)
	}
	def := root["route"].(map[string]interface{})["rule_set"].([]interface{})[0].(map[string]interface{})
	if def["type"] != "local" {
		t.Fatalf("definition not localized onto the kept copy: %v", def)
	}
}

func TestLocalizeUnchangedSkipsNetwork(t *testing.T) {
	payload := makeSRS(t, "on-disk copy")
	m, inst, runDir := newRuleSetManager(t)
	url := "http://127.0.0.1:1/geosite-cn.srs" // unroutable: any fetch attempt fails
	dir := filepath.Join(runDir, ruleSetDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	set := &remoteRuleSet{tag: "geosite-cn", directURL: url}
	if err := os.WriteFile(set.localPath(dir), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	inst.req.Hash = "same-hash"
	req := ApplyRequest{Type: "singbox", Hash: "same-hash", Files: []File{{Name: "config.json", Content: configWithURL(t, url)}}}
	if _, err := m.localizeSingboxRuleSets(inst, req, runDir); err != nil {
		t.Fatalf("unchanged config with copies on disk must not touch the network: %v", err)
	}
}

func TestUpdateRuleSetsStagesAndSwaps(t *testing.T) {
	v1 := makeSRS(t, "version one")
	v2 := makeSRS(t, "version two, changed")
	var current []byte = v1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(current)
	}))
	defer srv.Close()

	m, inst, runDir := newRuleSetManager(t)
	url := srv.URL + "/geosite-cn.srs"
	cfg := configWithURL(t, url)
	inst.req = ApplyRequest{Type: "singbox", Hash: "h1", Enabled: false, Files: []File{{Name: "config.json", Content: cfg}}}
	dir := filepath.Join(runDir, ruleSetDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	set := &remoteRuleSet{tag: "geosite-cn", directURL: url}
	path := set.localPath(dir)
	if err := os.WriteFile(path, v1, 0o644); err != nil {
		t.Fatal(err)
	}

	// Same content upstream: nothing staged, file untouched.
	m.UpdateRuleSets(context.Background())
	if _, err := os.Stat(path + ".new"); !os.IsNotExist(err) {
		t.Fatal("unchanged content must not be staged")
	}

	// Changed content upstream: staged, swapped in (kernel disabled,
	// so no restart machinery is exercised here).
	current = v2
	m.UpdateRuleSets(context.Background())
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, v2) {
		t.Fatal("updated rule set not swapped in")
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatal(".bak must be cleaned after a successful swap")
	}
}
