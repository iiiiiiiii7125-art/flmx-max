package handler

import (
	"strings"
	"time"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// C1-06 钉格（1.9.5 发版后审计）：凭据轮换三入口此前未接幂等——
// 同键重试/双击被当作新一轮轮换，首轮刚生成/分发的凭据立即作废
// （修复前同键第二发仍轮换＝红）。现三面同批接 idemBegin：同键
// 重发回放首轮回执、凭据不变；无键调用行为不变（仍真轮换）。
// 轮换族全集已机械核：kernel.Regenerate 唯一调用点＋
// rotateSubscriptionCredentials 唯二调用点，即此三面，无第四入口。

func c106SubToken(t *testing.T, h *Handler, id int64) string {
	t.Helper()
	s, err := h.repo.GetSubscription(id)
	if err != nil || s == nil {
		t.Fatalf("get subscription %d: %v", id, err)
	}
	return s.Token
}

func TestC106SubscriptionResetIdempotentReplay(t *testing.T) {
	r, h := r2b2Open(t)
	sub := createShareSub(t, r, &model.Subscription{
		Name: "c106-reset", Kind: model.SubscriptionShare,
		Token: strings.Repeat("c", 32), UUID: "uuid-c106", Password: "pw-c106",
		SourceAll: 1, Enabled: 1,
	})
	payload := map[string]interface{}{"id": sub.ID}

	first := r2b2AuthPost(t, h.subscriptionReset, "/api/v1/subscription/reset", payload, "c106-reset-key")
	r2b2AssertCode0(t, "first reset", first)
	tokenAfterFirst := c106SubToken(t, h, sub.ID)
	if tokenAfterFirst == sub.Token {
		t.Fatal("first reset must rotate the token")
	}

	second := r2b2AuthPost(t, h.subscriptionReset, "/api/v1/subscription/reset", payload, "c106-reset-key")
	if second != first {
		t.Fatalf("same-key retry must replay the first receipt verbatim:\nfirst:  %s\nsecond: %s", first, second)
	}
	if got := c106SubToken(t, h, sub.ID); got != tokenAfterFirst {
		t.Fatalf("same-key retry must not rotate again: token %q -> %q", tokenAfterFirst, got)
	}

	r2b2AuthPost(t, h.subscriptionReset, "/api/v1/subscription/reset", payload, "")
	if got := c106SubToken(t, h, sub.ID); got == tokenAfterFirst {
		t.Fatal("keyless call must still rotate (behavior unchanged without a key)")
	}
}

func TestC106InboundSubInfoResetIdempotentReplay(t *testing.T) {
	_, h := r2b2Open(t)
	payload := map[string]interface{}{"reset": true}

	first := r2b2AuthPost(t, h.inboundSubInfo, "/api/v1/inbound/sub/info", payload, "c106-subinfo-key")
	r2b2AssertCode0(t, "first sub/info reset", first)
	s, err := h.repo.DefaultSubscription(time.Now().UnixMilli())
	if err != nil || s == nil {
		t.Fatalf("default subscription: %v", err)
	}
	tokenAfterFirst := s.Token

	second := r2b2AuthPost(t, h.inboundSubInfo, "/api/v1/inbound/sub/info", payload, "c106-subinfo-key")
	if second != first {
		t.Fatalf("same-key retry must replay the first receipt verbatim:\nfirst:  %s\nsecond: %s", first, second)
	}
	s2, err := h.repo.DefaultSubscription(time.Now().UnixMilli())
	if err != nil || s2 == nil {
		t.Fatalf("default subscription: %v", err)
	}
	if s2.Token != tokenAfterFirst {
		t.Fatalf("same-key retry must not rotate again: token %q -> %q", tokenAfterFirst, s2.Token)
	}

	r2b2AuthPost(t, h.inboundSubInfo, "/api/v1/inbound/sub/info", payload, "")
	s3, err := h.repo.DefaultSubscription(time.Now().UnixMilli())
	if err != nil || s3 == nil {
		t.Fatalf("default subscription: %v", err)
	}
	if s3.Token == tokenAfterFirst {
		t.Fatal("keyless call must still rotate (behavior unchanged without a key)")
	}
}

func TestC106InboundRegenerateIdempotentReplay(t *testing.T) {
	r, h := r2b2Open(t)
	nodeID := seedRelayTargetNode(t, r, "c106-node", "198.51.100.42")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeXray, "latest")
	entry := seedChainInbound(t, r, k, "c106-regen", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24445,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	uuidBefore := b6ReloadInbound(t, r, entry.ID).Settings.UUID
	payload := map[string]interface{}{"id": entry.ID}

	first := r2b2AuthPost(t, h.inboundRegenerate, "/api/v1/inbound/regenerate", payload, "c106-regen-key")
	r2b2AssertCode0(t, "first regenerate", first)
	uuidAfterFirst := b6ReloadInbound(t, r, entry.ID).Settings.UUID
	if uuidAfterFirst == uuidBefore {
		t.Fatal("first regenerate must rotate the UUID")
	}

	second := r2b2AuthPost(t, h.inboundRegenerate, "/api/v1/inbound/regenerate", payload, "c106-regen-key")
	if second != first {
		t.Fatalf("same-key retry must replay the first receipt verbatim:\nfirst:  %s\nsecond: %s", first, second)
	}
	if got := b6ReloadInbound(t, r, entry.ID).Settings.UUID; got != uuidAfterFirst {
		t.Fatalf("same-key retry must not rotate again: uuid %q -> %q", uuidAfterFirst, got)
	}

	r2b2AuthPost(t, h.inboundRegenerate, "/api/v1/inbound/regenerate", payload, "")
	if got := b6ReloadInbound(t, r, entry.ID).Settings.UUID; got == uuidAfterFirst {
		t.Fatal("keyless call must still rotate (behavior unchanged without a key)")
	}
}
