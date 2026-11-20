package nftables

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// R18-C3-09: the capability probe rendered a table with no BindIP
// rule, so the BindIP rendering path was only discovered on the
// first real sync with a BindIP forward — after the forward was
// already in the DB. The probe must cover the BindIP forms (v4 and
// v6, aligned with TestRenderTablePassesNftCheck) so a node that
// cannot validate them fails the capability test up front.
func TestBuildCapabilityCheckCommandCoversBindIP(t *testing.T) {
	command := buildCapabilityCheckCommand("nft", "flmx_capability_test")
	for _, want := range []string{
		// v4 BindIP rule.
		"ip daddr 192.0.2.10 tcp dport",
		"ip daddr 192.0.2.10 udp dport",
		// v6 BindIP rule.
		"ip6 daddr 2001:db8::10 tcp dport",
		// The no-BindIP probe rule stays (baseline coverage).
		"tcp dport 12345 counter dnat ip to 192.0.2.1:443",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("capability probe must contain %q:\n%s", want, command)
		}
	}
	if !strings.Contains(command, "table inet flmx_capability_test") {
		t.Fatalf("capability probe must render into the probe table:\n%s", command)
	}

	// The extended probe must still validate (check-only) end to
	// end through the fake nft, without applying anything.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	nftPath := filepath.Join(dir, "nft")
	fake := `#!/bin/sh
echo "$*" >> "` + logPath + `"
if [ "$1" = "--version" ] || [ "$1" = "-c" ]; then
  exit 0
fi
exit 1
`
	if err := os.WriteFile(nftPath, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake nft: %v", err)
	}
	command = strings.Replace(buildCapabilityCheckCommand(nftPath, "flmx_capability_test"), "command -v nft", "command -v "+nftPath, 1)
	result := exec.Command("sh", "-c", command)
	if output, err := result.CombinedOutput(); err != nil {
		t.Fatalf("capability command failed: %v: %s", err, output)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake nft calls: %v", err)
	}
	if strings.Count(string(calls), "-c -f ") != 1 || strings.Contains(string(calls), "\n-f ") {
		t.Fatalf("expected one check-only invocation, got calls:\n%s", calls)
	}
}
