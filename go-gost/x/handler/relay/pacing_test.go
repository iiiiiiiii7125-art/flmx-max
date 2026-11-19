package relay

import (
	"testing"
	"time"

	xmd "github.com/go-gost/x/metadata"
)

// The pacing interval resolves as env > metadata > default
// (25us), and an explicitly configured 0 disables pacing.
func TestParseMetadataUDPEgressPacing(t *testing.T) {
	t.Setenv("FLMX_UDP_EGRESS_PACING", "")

	h := NewHandler().(*relayHandler)
	if err := h.Init(xmd.NewMetadata(map[string]any{})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if h.md.udpEgressPacing != 25*time.Microsecond {
		t.Fatalf("default udpEgressPacing = %v, want 25us", h.md.udpEgressPacing)
	}

	h2 := NewHandler().(*relayHandler)
	if err := h2.Init(xmd.NewMetadata(map[string]any{"udpEgressPacing": "50us"})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if h2.md.udpEgressPacing != 50*time.Microsecond {
		t.Fatalf("udpEgressPacing = %v, want 50us", h2.md.udpEgressPacing)
	}

	h3 := NewHandler().(*relayHandler)
	if err := h3.Init(xmd.NewMetadata(map[string]any{"udpEgressPacing": "0"})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if h3.md.udpEgressPacing != 0 {
		t.Fatalf("udpEgressPacing = %v, want 0 (disabled)", h3.md.udpEgressPacing)
	}
}

func TestParseMetadataUDPEgressPacingEnvOverride(t *testing.T) {
	t.Setenv("FLMX_UDP_EGRESS_PACING", "100us")
	h := NewHandler().(*relayHandler)
	if err := h.Init(xmd.NewMetadata(map[string]any{"udpEgressPacing": "50us"})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if h.md.udpEgressPacing != 100*time.Microsecond {
		t.Fatalf("env override udpEgressPacing = %v, want 100us", h.md.udpEgressPacing)
	}

	t.Setenv("FLMX_UDP_EGRESS_PACING", "0")
	h2 := NewHandler().(*relayHandler)
	if err := h2.Init(xmd.NewMetadata(map[string]any{"udpEgressPacing": "50us"})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if h2.md.udpEgressPacing != 0 {
		t.Fatalf("env override udpEgressPacing = %v, want 0 (disabled)", h2.md.udpEgressPacing)
	}

	// An unparseable env value leaves the metadata value alone.
	t.Setenv("FLMX_UDP_EGRESS_PACING", "bogus")
	h3 := NewHandler().(*relayHandler)
	if err := h3.Init(xmd.NewMetadata(map[string]any{"udpEgressPacing": "50us"})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if h3.md.udpEgressPacing != 50*time.Microsecond {
		t.Fatalf("bad env udpEgressPacing = %v, want 50us", h3.md.udpEgressPacing)
	}
}
