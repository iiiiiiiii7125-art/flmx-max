package kernel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// R7-001 (agent side of the gate): the panel renders a private-range
// guard ({"ip_is_private": true, "action": "reject"}) as the first
// route rule of every sing-box config. The one Apply-time rewrite a
// sing-box config goes through — rule-set localization, which parses
// the config into generic maps and re-marshals it — must preserve the
// guard verbatim and in position; this test pins that so a future
// rewrite change cannot silently drop the node's private-range
// protection on configs that carry rule sets.
func TestLocalizePreservesPrivateGuard(t *testing.T) {
	payload := makeSRS(t, "geosite-cn compiled rules")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	// Start from the shared fixture (rewritten to a local rule set by
	// the call below) and put the guard ahead of its split rule.
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(configWithURL(t, srv.URL+"/geosite-cn.srs")), &root); err != nil {
		t.Fatal(err)
	}
	route := root["route"].(map[string]interface{})
	guard := map[string]interface{}{"ip_is_private": true, "action": "reject"}
	route["rules"] = append([]interface{}{guard}, route["rules"].([]interface{})...)
	content, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}

	m, inst, runDir := newRuleSetManager(t)
	req := ApplyRequest{Type: "singbox", Hash: "h1", Files: []File{{Name: "config.json", Content: string(content)}}}
	files, err := m.localizeSingboxRuleSets(inst, req, runDir)
	if err != nil {
		t.Fatalf("localize: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(files[0].Content), &out); err != nil {
		t.Fatalf("rewritten config does not parse: %v", err)
	}
	// Prove the rewrite actually ran (otherwise preservation is
	// vacuous): the rule-set definition must be localized.
	def := out["route"].(map[string]interface{})["rule_set"].([]interface{})[0].(map[string]interface{})
	if def["type"] != "local" {
		t.Fatalf("definition not localized, test would prove nothing: %v", def)
	}
	rules := out["route"].(map[string]interface{})["rules"].([]interface{})
	if len(rules) == 0 {
		t.Fatal("rules lost in localization")
	}
	first := rules[0].(map[string]interface{})
	if len(first) != 2 || first["ip_is_private"] != true || first["action"] != "reject" {
		t.Fatalf("private guard must survive localization verbatim and first: %v", first)
	}
}
