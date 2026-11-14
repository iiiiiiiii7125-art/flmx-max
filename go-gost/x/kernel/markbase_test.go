package kernel

import (
	"strings"
	"testing"
)

// F2-DBL-01: per-agent socket-mark base — the agent's nft rules must
// mint the base the panel sent, so co-located agents count only their
// own kernels' traffic.

func TestNftScriptPerAgentMarkBase(t *testing.T) {
	// Node 75 base, user 7: mark 0x464b0007 must appear in both the
	// download (ct mark) and upload (meta mark) rules; the legacy
	// mark must appear nowhere.
	s := nftScript(nftTable, nil, nil, nil, []int64{7}, 0x464B0000)
	if !strings.Contains(s, "ct mark 0x464b0007") {
		t.Fatalf("download rule must use the per-agent mark:\n%s", s)
	}
	if !strings.Contains(s, "meta mark 0x464b0007") {
		t.Fatalf("upload rule must use the per-agent mark:\n%s", s)
	}
	if strings.Contains(s, "0x46560007") {
		t.Fatalf("legacy mark must not appear with a per-agent base:\n%s", s)
	}

	// Same user, node 76 base: a different mark — the two agents'
	// tables no longer match the same packets.
	s2 := nftScript(nftTable, nil, nil, nil, []int64{7}, 0x464C0000)
	if !strings.Contains(s2, "0x464c0007") {
		t.Fatalf("node 76 mark:\n%s", s2)
	}
	if strings.Contains(s2, "0x464b0007") {
		t.Fatalf("node 76 script must not contain node 75's mark:\n%s", s2)
	}

	// Base 0 (old panel) keeps the legacy mark.
	s0 := nftScript(nftTable, nil, nil, nil, []int64{7}, 0)
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
	n.setMarkBase(0x464B0000)
	if n.markBase != 0x464B0000 {
		t.Fatalf("markBase: got 0x%x", n.markBase)
	}
	// A later 0 (old panel re-apply) must not clobber the base.
	n.setMarkBase(0)
	if n.markBase != 0x464B0000 {
		t.Fatalf("base 0 must not clobber: got 0x%x", n.markBase)
	}
}

func TestApplyRequestUserMarkBaseValidate(t *testing.T) {
	base := func(b int64) *ApplyRequest {
		return &ApplyRequest{Type: "singbox", Version: "1.12.8",
			Download: &Download{BinName: "sing-box", Archive: "raw"}, UserMarkBase: b}
	}
	valid := []int64{0, 0x46560000, 0x464B0000, 0x46FF0000}
	for _, b := range valid {
		if err := base(b).validate(); err != nil {
			t.Fatalf("base 0x%x must validate: %v", b, err)
		}
	}
	invalid := []int64{0x12345678, 0x47000000, 0x46000001, 0x46560001, -1}
	for _, b := range invalid {
		if err := base(b).validate(); err == nil {
			t.Fatalf("base 0x%x must fail validation", b)
		}
	}
}
