package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// R18-B6-01: a sing-box template policy-group filter with a bad regex
// used to fail the whole subscription render (HTTP 400), while the
// save path only warned and Mihomo/Surge silently widen to all nodes.
// Render now fails open like Mihomo/Surge: the bad filter keeps every
// node, and the check preview still surfaces the FilterError.
func TestR18B601SingboxTemplateBadFilterFailsOpen(t *testing.T) {
	badTpl := strings.Replace(singboxTpl, `"港|HK"`, `"["`, 1)
	if badTpl == singboxTpl {
		t.Fatal("fixture must contain the bad-regex substitution")
	}
	names := []string{"香港 01", "HK IPLC", "美国 01"}

	// Check preview still names the bad filter (warn-only surface).
	s, err := CheckTemplate(TemplateSingbox, badTpl, names)
	if err != nil {
		t.Fatalf("check must still succeed with a bad filter: %v", err)
	}
	filterErr := ""
	for _, g := range s.Groups {
		if g.FilterError != "" {
			filterErr = g.FilterError
		}
	}
	if filterErr == "" {
		t.Fatalf("check preview must still report the bad filter, got groups %+v", s.Groups)
	}

	// Render must succeed and widen to all nodes instead of failing.
	eps := namedEndpoints(names...)
	body, _, err := Subscription(FormatSingbox, eps, SubOptions{Templates: map[string]string{TemplateSingbox: badTpl}})
	if err != nil {
		t.Fatalf("render with a bad filter must not fail the subscription: %v", err)
	}
	var cfg struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	tags := map[string]map[string]interface{}{}
	for _, o := range cfg.Outbounds {
		tags[o["tag"].(string)] = o
	}
	members := func(tag string) string {
		var out []string
		for _, m := range tags[tag]["outbounds"].([]interface{}) {
			out = append(out, m.(string))
		}
		return strings.Join(out, ",")
	}
	// "香港" group: bad include filter fails open to every node.
	if got := members("香港"); got != "香港 01,HK IPLC,美国 01" {
		t.Fatalf("bad-filter group must widen to all nodes, got %q (body:\\n%s)", got, body)
	}
}
