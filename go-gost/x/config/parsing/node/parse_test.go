package node

import (
	"testing"

	_ "github.com/go-gost/x/connector/http"
	"github.com/go-gost/x/config"
	_ "github.com/go-gost/x/dialer/tcp"
	xlogger "github.com/go-gost/x/logger"
)

// D3-05 regression: an invalid matcher rule must fail the node parse.
// Previously the error was only logged and the node was kept with no
// matcher and priority -1, i.e. it silently served every host/path in
// its hop — the opposite of this codebase's fail-closed convention.
func TestParseNodeInvalidMatcherRuleFailsClosed(t *testing.T) {
	cfg := &config.NodeConfig{
		Name: "bad-rule-node",
		Addr: "192.0.2.1:8080",
		Matcher: &config.NodeMatcherConfig{
			Rule: "HostRegexp([invalid)",
		},
	}
	node, err := ParseNode("hop1", cfg, xlogger.Nop())
	if err == nil {
		t.Fatalf("ParseNode with invalid matcher rule returned node=%v, err=nil; want fail-closed error", node)
	}
	if node != nil {
		t.Fatalf("ParseNode returned a node alongside the error: %v", node)
	}
}

// A valid matcher rule must still parse, with the matcher attached and
// the default priority derived from the rule length.
func TestParseNodeValidMatcherRule(t *testing.T) {
	cfg := &config.NodeConfig{
		Name: "good-rule-node",
		Addr: "192.0.2.1:8080",
		Matcher: &config.NodeMatcherConfig{
			Rule: "Host(`example.com`)",
		},
	}
	node, err := ParseNode("hop1", cfg, xlogger.Nop())
	if err != nil {
		t.Fatalf("ParseNode with valid matcher rule: %v", err)
	}
	if node == nil {
		t.Fatal("ParseNode returned nil node")
	}
	if node.Options().Matcher == nil {
		t.Fatal("node has no matcher attached")
	}
	if node.Options().Priority != len("Host(`example.com`)") {
		t.Fatalf("priority = %d, want %d (rule length default)", node.Options().Priority, len("Host(`example.com`)"))
	}
}
