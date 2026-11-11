package relay

import (
	"testing"

	xmd "github.com/go-gost/x/metadata"
)

// The reaper role arrives as service metadata (written by the
// panel for relay segment services as "backstop") and must land
// in h.md where the connect/forward call sites read it.
func TestParseMetadataReapRole(t *testing.T) {
	h := NewHandler().(*relayHandler)
	if err := h.Init(xmd.NewMetadata(map[string]any{"reapRole": "backstop"})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if h.md.reapRole != "backstop" {
		t.Fatalf("reapRole = %q, want \"backstop\"", h.md.reapRole)
	}

	h2 := NewHandler().(*relayHandler)
	if err := h2.Init(xmd.NewMetadata(map[string]any{})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if h2.md.reapRole != "" {
		t.Fatalf("reapRole = %q, want empty when unconfigured", h2.md.reapRole)
	}
}

// The entrypoint tcpHandler receives the role plumbed from the
// relay handler (its own Init gets no metadata).
func TestTCPHandlerReapRolePlumbing(t *testing.T) {
	h := newTCPHandler(nil, "backstop").(*tcpHandler)
	if h.reapRole != "backstop" {
		t.Fatalf("tcpHandler.reapRole = %q, want \"backstop\"", h.reapRole)
	}
}
