package kernel

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// F2-DBL-01: per-agent socket-mark base — the agent's nft rules must
// mint the base the panel sent, so co-located agents count only their
// own kernels' traffic.

func TestNftScriptPerAgentMarkBase(t *testing.T) {
	// Node 75 base, user 7: mark 0x474b0007 must appear in both the
	// download (ct mark) and upload (meta mark) rules; the legacy
	// mark must appear nowhere.
	s := nftScript(nftTable, nil, nil, nil, []int64{7}, 0x474B0000, nil)
	if !strings.Contains(s, "ct mark 0x474b0007") {
		t.Fatalf("download rule must use the per-agent mark:\n%s", s)
	}
	if !strings.Contains(s, "meta mark 0x474b0007") {
		t.Fatalf("upload rule must use the per-agent mark:\n%s", s)
	}
	if strings.Contains(s, "0x46560007") {
		t.Fatalf("legacy mark must not appear with a per-agent base:\n%s", s)
	}

	// Same user, node 76 base: a different mark — the two agents'
	// tables no longer match the same packets.
	s2 := nftScript(nftTable, nil, nil, nil, []int64{7}, 0x474C0000, nil)
	if !strings.Contains(s2, "0x474c0007") {
		t.Fatalf("node 76 mark:\n%s", s2)
	}
	if strings.Contains(s2, "0x474b0007") {
		t.Fatalf("node 76 script must not contain node 75's mark:\n%s", s2)
	}

	// Base 0 (old panel) keeps the legacy mark.
	s0 := nftScript(nftTable, nil, nil, nil, []int64{7}, 0, nil)
	if !strings.Contains(s0, "0x46560007") {
		t.Fatalf("base 0 must keep the legacy mark:\n%s", s0)
	}
}

func TestNftCountersSetMarkBase(t *testing.T) {
	n := &nftCounters{}
	n.setMarkBase(0)
	if n.markBase != 0 {
		t.Fatal("base 0 must be ignored")
	}
	n.setMarkBase(0x474B0000)
	if n.markBase != 0x474B0000 {
		t.Fatalf("markBase: got 0x%x", n.markBase)
	}
	// A later 0 (old panel re-apply) must not clobber the base.
	n.setMarkBase(0)
	if n.markBase != 0x474B0000 {
		t.Fatalf("base 0 must not clobber: got 0x%x", n.markBase)
	}
}

func TestApplyRequestUserMarkBaseValidatePerAgentTag47(t *testing.T) {
	base := func(b int64) *ApplyRequest {
		return &ApplyRequest{Type: "singbox", Version: "1.12.8",
			Download: &Download{BinName: "sing-box", Archive: "raw"}, UserMarkBase: b}
	}
	// F-R4-01: the 0x47 per-agent tag must validate — node 86 boundary
	// 0x47560000 is a per-agent base, never the legacy 0x46560000.
	for _, b := range []int64{0x47000000, 0x474B0000, 0x47560000, 0x47FF0000} {
		if err := base(b).validate(); err != nil {
			t.Fatalf("per-agent base 0x%x must validate: %v", b, err)
		}
	}
}

func TestApplyRequestUserMarkBaseValidate(t *testing.T) {
	base := func(b int64) *ApplyRequest {
		return &ApplyRequest{Type: "singbox", Version: "1.12.8",
			Download: &Download{BinName: "sing-box", Archive: "raw"}, UserMarkBase: b}
	}
	// 0x46 per-agent bases from old panels stay valid (transition);
	// 0x47 is the per-agent tag since F-R4-01.
	valid := []int64{0, 0x46560000, 0x464B0000, 0x46FF0000, 0x47000000, 0x474B0000, 0x47FF0000}
	for _, b := range valid {
		if err := base(b).validate(); err != nil {
			t.Fatalf("base 0x%x must validate: %v", b, err)
		}
	}
	invalid := []int64{0x12345678, 0x48000000, 0x46000001, 0x46560001, -1}
	for _, b := range invalid {
		if err := base(b).validate(); err == nil {
			t.Fatalf("base 0x%x must fail validation", b)
		}
	}
}

// E-01: residual-collision detection — the agent scans the live
// ruleset after sync for its own marks in other tables, so a 1/256
// secret-hash collision surfaces in the instance logs instead of
// counting silently.

func TestFindMarkCollisions(t *testing.T) {
	// Canned `nft list ruleset` output: our table plus a foreign
	// table sharing our user-2 mark, a foreign table with unrelated
	// marks, and the legacy table's host-global marks.
	ruleset := `table inet flmx_kernel_a1b2c3d4 {
	chain input {
		type filter hook input priority filter; policy accept;
		ct mark 0x47170002 counter name "udown_2"
		ct mark 0x47170003 counter name "udown_3"
	}
	chain output {
		type filter hook output priority filter; policy accept;
		meta mark 0x47170002 ct mark set meta mark counter name "uup_2"
	}
}
table inet flmx_kernel_99887766 {
	chain input {
		type filter hook input priority filter; policy accept;
		ct mark 0x47170002 counter name "udown_2"
		ct mark 0x47170007 counter name "udown_7"
	}
	chain output {
		type filter hook output priority filter; policy accept;
		meta mark 0x47170002 ct mark set meta mark counter name "uup_2"
	}
}
table inet flmx_kernel_legacy {
	chain input {
		type filter hook input priority filter; policy accept;
		ct mark 0x46560002 counter name "udown_2"
	}
}
`
	marks := map[int64]bool{0x47170002: true, 0x47170003: true}
	got := findMarkCollisions(ruleset, "flmx_kernel_a1b2c3d4", marks)
	// One hit: the same mark in both chains of one foreign table
	// dedups to a single collision; our own table's marks, the
	// foreign table's unrelated mark (user 7) and the legacy
	// table's marks are not collisions.
	if len(got) != 1 {
		t.Fatalf("collisions: got %v, want exactly one", got)
	}
	if got[0] != (markCollision{Table: "flmx_kernel_99887766", Mark: 0x47170002}) {
		t.Fatalf("collision: got %v", got[0])
	}
	// Own table only: no collision.
	ownOnly := `table inet flmx_kernel_a1b2c3d4 {
	chain input {
		ct mark 0x47170002 counter name "udown_2"
	}
}
`
	if got := findMarkCollisions(ownOnly, "flmx_kernel_a1b2c3d4", marks); len(got) != 0 {
		t.Fatalf("own table only: got %v, want none", got)
	}
	// No marks / empty ruleset: no collision.
	if got := findMarkCollisions(ruleset, "flmx_kernel_a1b2c3d4", nil); len(got) != 0 {
		t.Fatalf("no marks: got %v, want none", got)
	}
	if got := findMarkCollisions("", "flmx_kernel_a1b2c3d4", marks); len(got) != 0 {
		t.Fatalf("empty ruleset: got %v, want none", got)
	}
	// Hex padding must not hide a collision: the value is compared
	// numerically, not as a string.
	padded := `table inet flmx_kernel_99887766 {
	chain input {
		ct mark 0x000047170002 counter name "udown_2"
	}
}
`
	if got := findMarkCollisions(padded, "flmx_kernel_a1b2c3d4", marks); len(got) != 1 {
		t.Fatalf("padded hex: got %v, want one", got)
	}
}

func TestDetectMarkCollision(t *testing.T) {
	oldRunner := nftRunner
	t.Cleanup(func() { nftRunner = oldRunner })
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		if len(args) == 2 && args[0] == "list" && args[1] == "ruleset" {
			return []byte(`table inet flmx_kernel_a1b2c3d4 {
	chain input {
		ct mark 0x47170002 counter name "udown_2"
	}
}
table inet flmx_kernel_99887766 {
	chain input {
		ct mark 0x47170002 counter name "udown_2"
	}
}
`), nil
		}
		return nil, fmt.Errorf("unexpected nft call %v", args)
	}
	n := &nftCounters{table: "flmx_kernel_a1b2c3d4"}
	n.setMarkBase(0x47170000)
	got := n.detectMarkCollision([]int64{2, 3})
	if len(got) != 1 || got[0] != (markCollision{Table: "flmx_kernel_99887766", Mark: 0x47170002}) {
		t.Fatalf("detect: got %v, want one collision on 0x47170002", got)
	}
	// Legacy base (0) and 0x46 bases never scan: the 0x46 space
	// also holds the legacy base, so a hit there cannot distinguish
	// a collision from legacy counting.
	n2 := &nftCounters{table: "flmx_kernel_a1b2c3d4"}
	if got := n2.detectMarkCollision([]int64{2}); len(got) != 0 {
		t.Fatalf("legacy base must not scan: got %v", got)
	}
	n2.setMarkBase(0x46560000)
	if got := n2.detectMarkCollision([]int64{2}); len(got) != 0 {
		t.Fatalf("0x46 base must not scan: got %v", got)
	}
	// No users: no scan, no collision.
	n3 := &nftCounters{table: "flmx_kernel_a1b2c3d4"}
	n3.setMarkBase(0x47170000)
	if got := n3.detectMarkCollision(nil); len(got) != 0 {
		t.Fatalf("no users: got %v, want none", got)
	}
	// A failed listing is best-effort: no collision, no error.
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("nft: command not found")
	}
	if got := n.detectMarkCollision([]int64{2}); len(got) != 0 {
		t.Fatalf("failed listing: got %v, want none", got)
	}
}
