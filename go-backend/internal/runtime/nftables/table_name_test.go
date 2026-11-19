package nftables

import (
	"regexp"
	"strings"
	"testing"
)

// P1-1: the panel-side nft table is derived from the panel install's
// instance key, never the shared plain name.

func TestTableNameForInstance(t *testing.T) {
	if got := TableNameForInstance(""); got != "flmx_e3b0c442" {
		t.Fatalf("default instance table must be flmx_e3b0c442, got %q", got)
	}
	if got := TableNameForInstance("blue"); got != "flmx_16477688" {
		t.Fatalf("instance %q table must be flmx_16477688, got %q", "blue", got)
	}
	if TableNameForInstance("") == TableNameForInstance("blue") {
		t.Fatal("different instances must get different tables")
	}
	if again := TableNameForInstance("blue"); again != TableNameForInstance("blue") {
		t.Fatalf("table derivation must be stable: %q", again)
	}
	for _, instance := range []string{"", "blue", "a-b_c9"} {
		got := TableNameForInstance(instance)
		if got == plainTableName || got == legacyTableName {
			t.Fatalf("instance %q must not use a plain table, got %q", instance, got)
		}
		if matched, _ := regexp.MatchString(`^[a-z0-9_]+$`, got); !matched {
			t.Fatalf("table must be a valid nft identifier: %q", got)
		}
	}
	if got := (NodePlan{}).TableName(); got != TableNameForInstance("") {
		t.Fatalf("zero plan must render into the default instance table, got %q", got)
	}
	if got := (NodePlan{InstanceID: "blue"}).TableName(); got != "flmx_16477688" {
		t.Fatalf("plan table must follow its instance, got %q", got)
	}
}

func TestRenderTableUsesInstanceTable(t *testing.T) {
	plan := NodePlan{
		NodeID:     7,
		InstanceID: "blue",
		Rules:      []Rule{{ForwardID: 42, InPort: 24000, TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp", "udp"}}},
	}
	script := RenderTable(plan)
	for _, want := range []string{
		"table inet flmx_16477688 {",
		`flmx_16477688 forward:42 dnat tcp`,
		`masquerade comment "flmx_16477688 masquerade"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	for _, plain := range []string{"table inet flmx {", "table inet flmx\n", "table inet flvx {", "comment \"flmx forward:"} {
		if strings.Contains(script, plain) {
			t.Fatalf("script must not mention the plain table (%q):\n%s", plain, script)
		}
	}

	// The default instance renders into its own hashed table too.
	def := RenderTable(NodePlan{NodeID: 7, Rules: plan.Rules})
	if !strings.Contains(def, "table inet flmx_e3b0c442 {") {
		t.Fatalf("default plan must render into flmx_e3b0c442:\n%s", def)
	}
}

// A listing from another instance's table (or a comment stamped
// with another instance's table) must not be counted as ours.
func TestParseCounterSamplesRejectsOtherInstanceTable(t *testing.T) {
	raw := []byte(`{
		"nftables": [
			{"rule": {
				"family": "inet",
				"table": "flmx_16477688",
				"chain": "forward",
				"comment": "flmx_16477688 forward:42 to-target tcp",
				"expr": [
					{"counter": {"packets": 7, "bytes": 4096}}
				]
			}},
			{"rule": {
				"family": "inet",
				"table": "flmx",
				"chain": "forward",
				"comment": "flmx forward:42 to-target tcp",
				"expr": [
					{"counter": {"packets": 9, "bytes": 8192}}
				]
			}}
		]
	}`)
	samples, err := ParseCounterSamples(raw, "flmx_e3b0c442")
	if err != nil {
		t.Fatalf("ParseCounterSamples: %v", err)
	}
	if len(samples) != 0 {
		t.Fatalf("expected 0 samples from other instances' tables, got %+v", samples)
	}
	if _, ok := ParseCounterComment("flmx_16477688 forward:42 to-target tcp", "flmx_e3b0c442"); ok {
		t.Fatal("a comment stamped with another instance's table must be rejected")
	}
}
