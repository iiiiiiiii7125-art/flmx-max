package handler

import (
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// connIdle mapping: row <-> kernel.Inbound round-trip. The save path
// (inboundSave) and the companion paths (create/sync via
// inboundFromModel + applyInboundToModel) all funnel through these two
// functions, so a dropped field here would silently lose the timeout
// on every save, backup restore and companion materialization.
func TestConnIdleModelRoundTrip(t *testing.T) {
	row := model.NodeInbound{
		ID: 42, Protocol: "vless", Listen: "::", Port: 443,
		Settings:        `{"uuid":"0b6e4c38-8f0e-4a3e-9d2d-7a1c2b3c4d5e"}`,
		Transport:       `{"type":"tcp"}`,
		Security:        `{"type":"none"}`,
		ConnIdleSeconds: 180,
	}
	in, err := inboundFromModel(row)
	if err != nil {
		t.Fatalf("inboundFromModel: %v", err)
	}
	if in.ConnIdleSeconds != 180 {
		t.Fatalf("from model: %d, want 180", in.ConnIdleSeconds)
	}
	if in.ConnIdleSeconds != kernel.DefaultConnIdleSeconds-120 {
		t.Fatalf("sanity: test value must differ from the default: %d", in.ConnIdleSeconds)
	}
	var back model.NodeInbound
	if err := applyInboundToModel(in, &back); err != nil {
		t.Fatalf("applyInboundToModel: %v", err)
	}
	if back.ConnIdleSeconds != 180 {
		t.Fatalf("back to model: %d, want 180", back.ConnIdleSeconds)
	}
	// Unset round-trips as 0 (never as an explicit Xray default).
	row.ConnIdleSeconds = 0
	in, err = inboundFromModel(row)
	if err != nil {
		t.Fatalf("inboundFromModel unset: %v", err)
	}
	if in.ConnIdleSeconds != 0 {
		t.Fatalf("unset from model: %d, want 0", in.ConnIdleSeconds)
	}
}
