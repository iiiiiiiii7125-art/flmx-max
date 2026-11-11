package local

import (
	"testing"

	xmd "github.com/go-gost/x/metadata"
)

// The edge role arrives as service metadata (written by the
// panel for client-facing forward services as "edge") and must
// land in h.md where the Transport call site reads it.
func TestParseMetadataReapRole(t *testing.T) {
	h := NewHandler().(*forwardHandler)
	if err := h.Init(xmd.NewMetadata(map[string]any{"reapRole": "edge"})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if h.md.reapRole != "edge" {
		t.Fatalf("reapRole = %q, want \"edge\"", h.md.reapRole)
	}

	h2 := NewHandler().(*forwardHandler)
	if err := h2.Init(xmd.NewMetadata(map[string]any{})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if h2.md.reapRole != "" {
		t.Fatalf("reapRole = %q, want empty when unconfigured", h2.md.reapRole)
	}
}
