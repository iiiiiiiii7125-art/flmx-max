package contract_test

import (
	"testing"

	"go-backend/internal/auth"
)

// BUG-A2-1: SaveNodeInbound's Updates whitelist missed outbound_ip_strategy,
// so both edit paths that assign it (inboundSave via applyInboundToModel and
// inboundSplitSave) reported success while the DB kept the old value.
func TestInboundOutboundIPStrategyPersistContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	nodeID := seedKernelNode(t, r, "strategy")

	install := kernelContractPost(t, router, admin, "/api/v1/kernel/install",
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","version":"latest"}`)
	if install.Code == 0 {
		t.Fatalf("offline install should report dispatch failure, got %+v", install)
	}
	create := kernelContractPost(t, router, admin, "/api/v1/inbound/create",
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","name":"ss-strategy","protocol":"shadowsocks","port":24443}`)
	if create.Code != 0 {
		t.Fatalf("create: %+v", create)
	}
	id := mustQueryInt64(t, r, `SELECT id FROM node_inbound WHERE name = 'ss-strategy'`)
	if got := mustQueryString(t, r, `SELECT outbound_ip_strategy FROM node_inbound WHERE id = ?`, id); got != "auto" {
		t.Fatalf("fresh inbound strategy = %q, want auto", got)
	}

	upd := kernelContractPost(t, router, admin, "/api/v1/inbound/update",
		`{"id":`+jsonInt64(id)+`,"name":"ss-strategy","protocol":"shadowsocks","port":24443,"outboundIpStrategy":"ipv4_only"}`)
	if upd.Code != 0 {
		t.Fatalf("update: %+v", upd)
	}
	if got := mustQueryString(t, r, `SELECT outbound_ip_strategy FROM node_inbound WHERE id = ?`, id); got != "ipv4_only" {
		t.Fatalf("after /inbound/update strategy = %q, want ipv4_only (BUG-A2-1: whitelist drop)", got)
	}

	upd2 := kernelContractPost(t, router, admin, "/api/v1/inbound/update",
		`{"id":`+jsonInt64(id)+`,"name":"ss-strategy","protocol":"shadowsocks","port":24443,"outboundIpStrategy":"ipv6_only"}`)
	if upd2.Code != 0 {
		t.Fatalf("update 2: %+v", upd2)
	}
	if got := mustQueryString(t, r, `SELECT outbound_ip_strategy FROM node_inbound WHERE id = ?`, id); got != "ipv6_only" {
		t.Fatalf("after second update strategy = %q, want ipv6_only", got)
	}

	// The split editor assigns the same field through the same repository
	// method; it was equally affected.
	split := kernelContractPost(t, router, admin, "/api/v1/inbound/split",
		`{"id":`+jsonInt64(id)+`,"outboundId":0,"splitRules":[],"outboundIpStrategy":"ipv4_only"}`)
	if split.Code != 0 {
		t.Fatalf("split save: %+v", split)
	}
	if got := mustQueryString(t, r, `SELECT outbound_ip_strategy FROM node_inbound WHERE id = ?`, id); got != "ipv4_only" {
		t.Fatalf("after /inbound/split strategy = %q, want ipv4_only (BUG-A2-1 chain/split path)", got)
	}
}

// BUG-A4-1: NodeInbound.Enabled carries gorm `default:1`; db.Create dropped
// an explicit 0, so POST /inbound/create with enabled:false produced an
// enabled, immediately-dispatched inbound. Omitting the field must still
// default to enabled.
func TestInboundCreateEnabledPersistContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	nodeID := seedKernelNode(t, r, "enabled")

	install := kernelContractPost(t, router, admin, "/api/v1/kernel/install",
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","version":"latest"}`)
	if install.Code == 0 {
		t.Fatalf("offline install should report dispatch failure, got %+v", install)
	}

	off := kernelContractPost(t, router, admin, "/api/v1/inbound/create",
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","name":"ss-off","protocol":"shadowsocks","port":24451,"enabled":false}`)
	if off.Code != 0 {
		t.Fatalf("create enabled:false: %+v", off)
	}
	if got := mustQueryInt(t, r, `SELECT enabled FROM node_inbound WHERE name = 'ss-off'`); got != 0 {
		t.Fatalf("create with enabled:false stored enabled=%d, want 0 (BUG-A4-1)", got)
	}

	def := kernelContractPost(t, router, admin, "/api/v1/inbound/create",
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","name":"ss-default","protocol":"shadowsocks","port":24452}`)
	if def.Code != 0 {
		t.Fatalf("create without enabled: %+v", def)
	}
	if got := mustQueryInt(t, r, `SELECT enabled FROM node_inbound WHERE name = 'ss-default'`); got != 1 {
		t.Fatalf("create without enabled stored enabled=%d, want 1 (default)", got)
	}

	on := kernelContractPost(t, router, admin, "/api/v1/inbound/create",
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","name":"ss-on","protocol":"shadowsocks","port":24453,"enabled":true}`)
	if on.Code != 0 {
		t.Fatalf("create enabled:true: %+v", on)
	}
	if got := mustQueryInt(t, r, `SELECT enabled FROM node_inbound WHERE name = 'ss-on'`); got != 1 {
		t.Fatalf("create with enabled:true stored enabled=%d, want 1", got)
	}
}
