package socket

import (
	"strings"
	"testing"
)

// R18-D1-01 (UpgradeAgent half): a replayed UpgradeAgent envelope
// must not downgrade the node — the panel-side direction gate
// (upgrade.go, O9-005) never runs on the replay path, so the agent
// enforces the direction itself, with the panel's calibre (unknown
// current version does not block, uncomparable fails closed).

func TestD1R18UpgradeAgentRejectsDowngrade(t *testing.T) {
	old := upgradeInContainer
	upgradeInContainer = func() bool { return false }
	t.Cleanup(func() { upgradeInContainer = old })

	reporter, _ := newD1TestReporter(t)
	reporter.version = "2.1.18"

	for _, target := range []string{"2.1.17", "v2.1.17", "2.0.0"} {
		err := reporter.handleUpgradeAgent(map[string]interface{}{"version": target})
		if err == nil || !strings.Contains(err.Error(), "低于") {
			t.Fatalf("downgrade to %s must be rejected, got err=%v", target, err)
		}
	}

	// Uncomparable running version fails closed instead of guessing.
	reporter.version = "bogus"
	err := reporter.handleUpgradeAgent(map[string]interface{}{"version": "2.1.19"})
	if err == nil || !strings.Contains(err.Error(), "无法比较") {
		t.Fatalf("uncomparable current version must fail closed, got err=%v", err)
	}
}

func TestD1R18CompareAgentVersions(t *testing.T) {
	cases := []struct {
		target, current string
		want            int
		wantErr         bool
	}{
		{"2.1.19", "2.1.18", 1, false},
		{"2.1.17", "2.1.18", -1, false},
		{"2.1.18", "2.1.18", 0, false},
		{"v2.1.17", "2.1.18", -1, false},
		{"2.1.18", "v2.1.18", 0, false},
		{"2.1.18", "dev", 1, false},
		{"dev", "2.1.18", -1, false},
		{"dev", "dev", 0, false},
		// R18-R3-D1-01: production dev binaries report the full
		// "dev (distro/arch)" form (main.go fullVersion) — the
		// bare-"dev" calibre must still apply after the head cut.
		{"2.1.19", "dev (debian/amd64)", 1, false},
		{"2.1.19", "dev (ubuntu/arm64)", 1, false},
		{"dev (debian/amd64)", "2.1.18", -1, false},
		{"dev", "dev (debian/amd64)", 0, false},
		{"dev (debian/amd64)", "dev (debian/amd64)", 0, false},
		{"2.1.18", "2.1.18 (debian/amd64)", 0, false},
		{"2.1.19", "2.1.18 (debian/amd64)", 1, false},
		{"2.1.18", "bogus", 0, true},
		{"bogus", "2.1.18", 0, true},
		{"2.1", "2.1.18", 0, true},
	}
	for _, c := range cases {
		got, err := compareAgentVersions(c.target, c.current)
		if c.wantErr {
			if err == nil {
				t.Fatalf("compareAgentVersions(%q, %q) must error", c.target, c.current)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("compareAgentVersions(%q, %q) = %d, %v; want %d, nil", c.target, c.current, got, err, c.want)
		}
	}
}

func TestD1R18UpgradeDirectionUnknownCurrentAllows(t *testing.T) {
	// Panel calibre (O9-005): an unknown running version does not
	// block the upgrade; only a known-lower target is refused.
	if err := checkUpgradeAgentVersionDirection("2.1.17", ""); err != nil {
		t.Fatalf("unknown current version must not block, got %v", err)
	}
	if err := checkUpgradeAgentVersionDirection("2.1.19", "dev"); err != nil {
		t.Fatalf("release target above a dev build must not block, got %v", err)
	}
	// R18-R3-D1-01: same calibre for the production dev report
	// form "dev (debian/amd64)" (main.go fullVersion).
	if err := checkUpgradeAgentVersionDirection("2.1.19", "dev (debian/amd64)"); err != nil {
		t.Fatalf("release target above a dev build reporting its distro/arch must not block, got %v", err)
	}
	if err := checkUpgradeAgentVersionDirection("2.1.17", "2.1.18"); err == nil {
		t.Fatal("target below the running version must be refused")
	}
}
