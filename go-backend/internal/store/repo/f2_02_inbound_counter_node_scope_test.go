package repo

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// F2-02: inbound counter samples are scoped to the reporting node.
// Before the fix the apply API did not even take a node ID and the
// baselines were keyed globally, so any node could book against (or
// suppress, via baseline interference) any other node's inbounds.
func TestApplyInboundCounterSamplesForeignNodeDroppedF202(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "f202.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	inb := model.NodeInbound{NodeID: 2, Name: "b-in", Protocol: "vless", Port: 443}
	if err := r.DB().Create(&inb).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	sample := func(bytes int64, epoch string) InboundCounterSample {
		return InboundCounterSample{InboundID: inb.ID, Protocol: "vless", Direction: "in", Epoch: epoch, Bytes: bytes}
	}

	// Foreign node 1: baseline attempt + growth + an epoch-shuffled
	// sample that would reset the shared baseline. None may land.
	for _, s := range []InboundCounterSample{sample(100, "eA"), sample(900, "eA"), sample(50, "eB")} {
		if err := r.ApplyInboundCounterSamples(1, []InboundCounterSample{s}, 1000); err != nil {
			t.Fatalf("foreign apply: %v", err)
		}
	}
	var got model.NodeInbound
	if err := r.DB().First(&got, inb.ID).Error; err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if got.UploadBytes != 0 {
		t.Fatalf("F2-02: foreign node booked upload_bytes=%d, want 0", got.UploadBytes)
	}
	var baselines int64
	if err := r.DB().Model(&model.NodeInboundCounter{}).Count(&baselines).Error; err != nil {
		t.Fatalf("count baselines: %v", err)
	}
	if baselines != 0 {
		t.Fatalf("F2-02: foreign node left %d baseline rows, want 0", baselines)
	}

	// Owning node 2: baseline, then delta books; its baseline is its
	// own (node_id = 2).
	if err := r.ApplyInboundCounterSamples(2, []InboundCounterSample{sample(100, "eA")}, 2000); err != nil {
		t.Fatalf("owner baseline: %v", err)
	}
	if err := r.ApplyInboundCounterSamples(2, []InboundCounterSample{sample(450, "eA")}, 3000); err != nil {
		t.Fatalf("owner growth: %v", err)
	}
	if err := r.DB().First(&got, inb.ID).Error; err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if got.UploadBytes != 350 {
		t.Fatalf("F2-02: owner delta booked upload_bytes=%d, want 350", got.UploadBytes)
	}
	var row model.NodeInboundCounter
	if err := r.DB().First(&row).Error; err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if row.NodeID != 2 || row.Bytes != 450 {
		t.Fatalf("F2-02: baseline = node %d bytes %d, want node 2 bytes 450", row.NodeID, row.Bytes)
	}

	// Foreign interference after the owner baseline exists must not
	// move it: node 1 replays with a huge value, node 2's next delta
	// is still measured against 450.
	if err := r.ApplyInboundCounterSamples(1, []InboundCounterSample{sample(100000, "eA")}, 4000); err != nil {
		t.Fatalf("foreign replay: %v", err)
	}
	if err := r.ApplyInboundCounterSamples(2, []InboundCounterSample{sample(500, "eA")}, 5000); err != nil {
		t.Fatalf("owner next: %v", err)
	}
	if err := r.DB().First(&got, inb.ID).Error; err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if got.UploadBytes != 400 {
		t.Fatalf("F2-02: after foreign replay upload_bytes=%d, want 400 (350 + 50)", got.UploadBytes)
	}
}

// F2-02 migration: pre-column baselines (node_id = 0) and the legacy
// global unique index are cleared by the startup scope finisher; the
// next owner sample re-establishes the baseline with delta = 0.
func TestInboundCounterNodeScopeMigrationF202(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "f202-mig.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	legacy := model.NodeInboundCounter{NodeID: 0, InboundID: 42, Protocol: "vless", Direction: "in", Epoch: "e0", Bytes: 777, UpdatedTime: 1}
	if err := r.DB().Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy baseline: %v", err)
	}
	if err := r.DB().Exec(`CREATE UNIQUE INDEX idx_node_inbound_counter_key ON node_inbound_counter (inbound_id, protocol, direction)`).Error; err != nil {
		t.Fatalf("seed legacy index: %v", err)
	}
	if err := finishInboundCounterNodeScope(r.DB()); err != nil {
		t.Fatalf("finish scope: %v", err)
	}
	if r.DB().Migrator().HasIndex(&model.NodeInboundCounter{}, "idx_node_inbound_counter_key") {
		t.Fatal("F2-02: legacy global unique index still present")
	}
	var n int64
	if err := r.DB().Model(&model.NodeInboundCounter{}).Count(&n).Error; err != nil {
		t.Fatalf("count baselines: %v", err)
	}
	if n != 0 {
		t.Fatalf("F2-02: %d ownerless baselines survived migration, want 0", n)
	}
	// Idempotent: a second run is a no-op.
	if err := finishInboundCounterNodeScope(r.DB()); err != nil {
		t.Fatalf("finish scope (2nd): %v", err)
	}
}
