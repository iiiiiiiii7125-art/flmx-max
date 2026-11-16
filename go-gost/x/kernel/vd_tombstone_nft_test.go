package kernel

import (
	"strings"
	"testing"
)

// V-D: a tombstoned user's new connection must die at the handshake,
// not drain through the 2s sweeper's graceful close. The nft reject
// rule for the tombstoned mark is the data-plane half of the
// tombstone; these tests pin the render and the name->id mapping it
// depends on.

func TestTombstoneUserID(t *testing.T) {
	cases := []struct {
		name string
		want int64
		ok   bool
	}{
		{"u1", 1, true},
		{"u2", 2, true},
		{"u65535", 65535, true},
		{"u0", 0, false},
		{"u", 0, false},
		{"u-1", 0, false},
		{"u65536", 0, false}, // above userMarkMax
		{"u1x", 0, false},
		{"x1", 0, false},
		{"", 0, false},
		{"U1", 0, false},
	}
	for _, c := range cases {
		got, ok := tombstoneUserID(c.name)
		if ok != c.ok || got != c.want {
			t.Errorf("tombstoneUserID(%q) = (%d, %v), want (%d, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestNFTScriptBlockedUserReject(t *testing.T) {
	// Users u1 (kept) and u2 (tombstoned), node base 0x47980000
	// (the V-D live case): only u2's mark is rejected, first in
	// the output chain, before any counter rule.
	s := nftScript(nftTable, nil, nil, nil, []int64{1, 2}, 0x47980000, []int64{2})
	if !strings.Contains(s, "meta mark 0x47980002 reject\n") {
		t.Fatalf("tombstoned user mark must be rejected:\n%s", s)
	}
	if strings.Contains(s, "meta mark 0x47980001 reject") {
		t.Fatalf("kept user mark must not be rejected:\n%s", s)
	}
	rejectAt := strings.Index(s, "meta mark 0x47980002 reject")
	counterAt := strings.Index(s, "counter name uup_")
	if rejectAt < 0 || counterAt < 0 || rejectAt > counterAt {
		t.Fatalf("reject rule must precede the counter rules:\n%s", s)
	}
	// The reject must sit in the output chain, not the input chain.
	outAt := strings.Index(s, "chain output")
	if rejectAt < outAt {
		t.Fatalf("reject rule must be in the output chain:\n%s", s)
	}

	// A blocked user with no live users and no port rules must
	// still render the chain (the empty-table early return must
	// not swallow the reject).
	empty := nftScript(nftTable, nil, nil, nil, nil, 0x47980000, []int64{2})
	if !strings.Contains(empty, "meta mark 0x47980002 reject") {
		t.Fatalf("blocked-only render must still carry the reject:\n%s", empty)
	}
	// And with nothing blocked, nothing is rejected.
	clean := nftScript(nftTable, nil, nil, nil, []int64{1, 2}, 0x47980000, nil)
	if strings.Contains(clean, " reject") {
		t.Fatalf("no-blocked render must not reject anything:\n%s", clean)
	}
}

func TestAllBlockedUserMarks(t *testing.T) {
	m := &Manager{instances: map[string]*instance{}, nft: &nftCounters{}}
	m.instances["sing-box"] = &instance{
		req:          ApplyRequest{Enabled: true},
		tombstones:   map[string]bool{"u9": true}, // sweeper-only, never rendered (R4B-01)
		blockedMarks: map[string]bool{"u2": true, "u5": true, "bogus": true},
	}
	m.instances["xray"] = &instance{
		req:          ApplyRequest{Enabled: false},
		blockedMarks: map[string]bool{"u9": true},
	}
	got := m.allBlockedUserMarks()
	// Enabled instance contributes u2 and u5; the unparseable name
	// is skipped; the disabled instance contributes nothing.
	want := map[int64]bool{2: true, 5: true}
	if len(got) != len(want) {
		t.Fatalf("allBlockedUserMarks() = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("allBlockedUserMarks() = %v, want %v", got, want)
		}
	}
}
