package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// F2-N2-01 钉格：inboundSave 同单改出站 IP 策略时，链式目标族冲突
// 校验必须读本单新策略，不得读存量旧值（分流面 inboundSplitSave 口径）。

func strPtr(s string) *string { return &s }

func TestFixAInboundSaveChainCheckUsesSubmittedStrategy(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fixa_strategy.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	front := seedRelayTargetNode(t, r, "fixa-front", "198.51.100.1")
	landing6 := seedRelayTargetNode(t, r, "fixa-v6", "2001:db8::2")
	landing4 := seedRelayTargetNode(t, r, "fixa-v4", "203.0.113.10")
	box := seedChainKernel(t, r, front, kernel.TypeSingbox)
	exit6 := seedChainKernel(t, r, landing6, kernel.TypeXray)
	exit4 := seedChainKernel(t, r, landing4, kernel.TypeXray)
	target6 := seedChainInbound(t, r, exit6, "fixa-target6", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388}, 0)
	target4 := seedChainInbound(t, r, exit4, "fixa-target4", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388}, 0)
	seed := seedChainInbound(t, r, box, "fixa-seed", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388}, 0)
	seedIn, err := inboundFromModel(*seed)
	if err != nil {
		t.Fatal(err)
	}

	create := func(name string, port int, outboundID int64, strategy *string) (int, string) {
		body := inboundRequest{
			NodeID: front, KernelType: kernel.TypeSingbox, Name: name,
			Protocol: kernel.ProtocolShadowsocks, Port: port,
			Settings: seedIn.Settings, Security: seedIn.Security,
			OutboundID: outboundID, OutboundIPStrategy: strategy,
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		out := callInboundSave(t, h, false, string(raw))
		return out.Code, out.Msg
	}

	// 同单新建：ipv4_only ＋ IPv6 字面链式目标必须在保存期被拒。
	if code, msg := create("fixa-tighten-create", 8391, target6.ID, strPtr(kernel.OutboundIPv4Only)); code == 0 {
		t.Fatalf("same-request ipv4_only + IPv6 chain target must be rejected at save, got success")
	} else if !strings.Contains(msg, "IPv6") {
		t.Fatalf("rejection must name the family conflict, got %q", msg)
	}
	// 对照：ipv6_only ＋ IPv6 目标、ipv4_only ＋ IPv4 目标均应放行。
	if code, msg := create("fixa-v6-ok", 8392, target6.ID, strPtr(kernel.OutboundIPv6Only)); code != 0 {
		t.Fatalf("ipv6_only + IPv6 target must save: code=%d msg=%q", code, msg)
	}
	if code, msg := create("fixa-v4-ok", 8393, target4.ID, strPtr(kernel.OutboundIPv4Only)); code != 0 {
		t.Fatalf("ipv4_only + IPv4 target must save: code=%d msg=%q", code, msg)
	}

	// 同单更新：存量 auto 的入站改绑 IPv6 目标后，再以一单同时提交
	// ipv4_only（目标不变）必须被拒——存量旧策略不得放行。
	if code, msg := create("fixa-tighten-update", 8394, target6.ID, nil); code != 0 {
		t.Fatalf("auto + IPv6 target must save: code=%d msg=%q", code, msg)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	var entryID int64
	for _, row := range rows {
		if row.Name == "fixa-tighten-update" {
			entryID = row.ID
		}
	}
	if entryID == 0 {
		t.Fatal("created inbound not found")
	}
	stored, err := r.GetNodeInbound(entryID)
	if err != nil || stored == nil {
		t.Fatalf("reload inbound: %v", err)
	}
	oldIn, err := inboundFromModel(*stored)
	if err != nil {
		t.Fatal(err)
	}
	upd := inboundRequest{
		ID: entryID, Name: "fixa-tighten-update", Protocol: oldIn.Protocol, Listen: oldIn.Listen,
		Port: stored.Port, Settings: oldIn.Settings, Transport: oldIn.Transport, Security: oldIn.Security,
		OutboundID: target6.ID, OutboundIPStrategy: strPtr(kernel.OutboundIPv4Only),
	}
	raw, err := json.Marshal(upd)
	if err != nil {
		t.Fatal(err)
	}
	out := callInboundSave(t, h, true, string(raw))
	if out.Code == 0 {
		t.Fatalf("same-request update tightening to ipv4_only with IPv6 chain target must be rejected, got success")
	}
	if !strings.Contains(out.Msg, "IPv6") {
		t.Fatalf("update rejection must name the family conflict, got %q", out.Msg)
	}
	// 被拒后存量策略不得被污染。
	after, err := r.GetNodeInbound(entryID)
	if err != nil || after == nil {
		t.Fatalf("reload inbound after rejection: %v", err)
	}
	if after.OutboundIPStrategy == kernel.OutboundIPv4Only {
		t.Fatalf("rejected save must not persist the tightened strategy, stored %q", after.OutboundIPStrategy)
	}
}

