package handler

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// F2-01: a node's per-user counter samples must only book against
// subscriptions that node actually serves (subscription → inbounds →
// node_id, resolved through subInboundIDs). Before the fix,
// applyUserCounters booked any agent-supplied subscription ID, so a
// foreign node could inflate an arbitrary subscription and force its
// quota cutoff (live-d scenario 4).
func TestApplyUserCountersForeignNodeDroppedF201(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/f201.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	mkNode := func(name string) int64 {
		if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES(?, ?, '203.0.113.1', '1000-2000', ?, 1)`, name, name+"-secret", now).Error; err != nil {
			t.Fatal(err)
		}
		return mustLastInsertID(t, r, "node")
	}
	nodeA := mkNode("a")
	nodeB := mkNode("b")

	inb := model.NodeInbound{NodeID: nodeB, Name: "b-in", Protocol: "vless", Listen: "::", Port: 443, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&inb).Error; err != nil {
		t.Fatal(err)
	}
	sub := &model.Subscription{Name: "victim", Kind: model.SubscriptionShare, Token: strings.Repeat("c", 32),
		UUID: "u-f201", Password: "pw", Enabled: 1, InboundIDs: fmt.Sprint(inb.ID), TrafficLimit: 1048576}
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}

	// Foreign node A reports the subscription twice (baseline, then
	// growth past nothing): nothing may be booked and no baseline row
	// may be created for (A, sub).
	h.applyUserCounters(nodeA, "eA", []agentUserCounter{{ID: sub.ID, Dir: "down", Bytes: 1000}}, now)
	h.applyUserCounters(nodeA, "eA", []agentUserCounter{{ID: sub.ID, Dir: "down", Bytes: 5000}}, now)
	got, err := r.GetSubscription(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DownloadBytes != 0 || got.UploadBytes != 0 {
		t.Fatalf("F2-01: foreign node booked traffic: up=%d down=%d, want 0/0", got.UploadBytes, got.DownloadBytes)
	}
	var foreignBaselines int64
	if err := r.DB().Model(&model.SubscriptionCounter{}).Where("node_id = ? AND subscription_id = ?", nodeA, sub.ID).Count(&foreignBaselines).Error; err != nil {
		t.Fatal(err)
	}
	if foreignBaselines != 0 {
		t.Fatalf("F2-01: foreign node left %d baseline rows, want 0", foreignBaselines)
	}

	// The owning node B reports: baseline first, then the delta books.
	h.applyUserCounters(nodeB, "eB", []agentUserCounter{{ID: sub.ID, Dir: "down", Bytes: 1000}}, now)
	h.applyUserCounters(nodeB, "eB", []agentUserCounter{{ID: sub.ID, Dir: "down", Bytes: 1600}}, now)
	got, err = r.GetSubscription(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DownloadBytes != 600 {
		t.Fatalf("F2-01: owning node delta = %d, want 600", got.DownloadBytes)
	}

	// Unknown subscription IDs are dropped, not baselined.
	h.applyUserCounters(nodeB, "eB", []agentUserCounter{{ID: 999999, Dir: "down", Bytes: 10}}, now)
	var unknownBaselines int64
	if err := r.DB().Model(&model.SubscriptionCounter{}).Where("subscription_id = ?", 999999).Count(&unknownBaselines).Error; err != nil {
		t.Fatal(err)
	}
	if unknownBaselines != 0 {
		t.Fatalf("F2-01: unknown subscription left %d baseline rows, want 0", unknownBaselines)
	}
}
