package handler

import (
	"context"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// XC4-B3（差集补钉）：FED-02 的到期回收行为本体有 repo 普查与清理
// 原语的既有覆盖，但「维护 tick 到点 → runFederationCleanupRetryLoop
// 调用 reclaimExpiredPeerShares → 到期共享被实际拆除」这条接线此前
// 无直测——若 tick 内的调用行被误删，普查用例仍绿、行为静默失效。
// 本用例种一个已到期且仍有 active 资源的共享，真实启动维护循环，
// 等待资源被 release（desired=deleted 且 DeleteService 已下发节点）
// 为止；接线缺失时本用例必然超时变红。

func TestXC4B3FederationCleanupTickReclaimsExpiredShare(t *testing.T) {
	a := newCleanupAgent(t)
	now := time.Now().UnixMilli()
	share := &repo.PeerShare{
		Name: "xc4b3-share", NodeID: 1, Token: "xc4b3-share", IsActive: 1,
		PortRangeStart: 31000, PortRangeEnd: 32000,
		ExpiryTime: now - 60_000, CreatedTime: now, UpdatedTime: now,
	}
	if err := a.h.repo.CreatePeerShare(share); err != nil {
		t.Fatal(err)
	}
	b606SeedResource(t, a.h, share, "service", "expiredsvc", "active", map[string]interface{}{
		"handler":  map[string]interface{}{"type": "tcp"},
		"listener": map[string]interface{}{"type": "tcp"},
	})

	h := a.h
	h.jobsWG.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	go h.runFederationCleanupRetryLoop(ctx)
	defer func() {
		cancel()
		done := make(chan struct{})
		go func() {
			h.jobsWG.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("federation cleanup loop did not stop after context cancel")
		}
	}()

	deadline := time.Now().Add(20 * time.Second)
	for {
		item, err := a.h.repo.GetPeerShareResource(share.ID, "service", "expiredsvc")
		if err != nil {
			t.Fatal(err)
		}
		if item != nil && item.DesiredState == "deleted" && len(a.commandsOfType("DeleteService")) > 0 {
			return
		}
		if time.Now().After(deadline) {
			state := "<missing>"
			if item != nil {
				state = item.DesiredState
			}
			t.Fatalf("expired share was not reclaimed by the maintenance tick within 20s: resource desired_state=%q, DeleteService commands=%d (tick wiring to reclaimExpiredPeerShares missing?)", state, len(a.commandsOfType("DeleteService")))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
