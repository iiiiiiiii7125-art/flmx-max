package socket

import (
	"strings"
	"testing"
)

// R18-D1-02 (M-2): UpgradeAgent accepted a "v"-prefixed version but
// spliced it verbatim into the release-download URL. The repo
// publishes bare-number tags only (zero "v*" tags), so
// .../releases/download/v2.1.18/gost-amd64 404'd on every route and
// burned the 4-minute download budget. The download base must be
// built from the normalized (bare) version.
func TestD1R18AgentUpgradeDownloadBaseStripsVPrefix(t *testing.T) {
	for _, version := range []string{"v2.1.18", "2.1.18"} {
		got := agentUpgradeDownloadBase(version)
		want := "https://github.com/iiiiiiiii7125-art/flmx-max/releases/download/2.1.18/"
		if got != want {
			t.Fatalf("agentUpgradeDownloadBase(%q) = %q, want %q", version, got, want)
		}
		if strings.Contains(got, "/v2.1.18/") {
			t.Fatalf("agentUpgradeDownloadBase(%q) must not contain a v-prefixed tag segment, got %q", version, got)
		}
	}
}

func TestD1R18NormalizeAgentVersion(t *testing.T) {
	cases := map[string]string{
		"v2.1.18":   "2.1.18",
		"2.1.18":    "2.1.18",
		"  v2.1.18": "2.1.18",
		"v2.1.18  ": "2.1.18",
		"dev":       "dev",
	}
	for in, want := range cases {
		if got := normalizeAgentVersion(in); got != want {
			t.Fatalf("normalizeAgentVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// Anti-overrepair: normalizing the target must not weaken the
// R18-D1-01 direction gate — a "v"-prefixed downgrade is still a
// downgrade.
func TestD1R18UpgradeAgentVPrefixedDowngradeStillRejected(t *testing.T) {
	stubUpgradeInContainer(t, false)
	reporter, _ := newD1TestReporter(t)
	reporter.version = "2.1.18"

	err := reporter.handleUpgradeAgent(map[string]interface{}{"version": "v2.1.17"})
	if err == nil || !strings.Contains(err.Error(), "低于") {
		t.Fatalf("downgrade to v2.1.17 must be rejected, got err=%v", err)
	}
}
