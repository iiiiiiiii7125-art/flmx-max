package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// R18-B4-05: the relay dedup gate compared the forward's RemoteAddr
// string, so a landing node whose address changed (IP move /
// re-resolve) let a second identical relay be built for the same
// inbound, tunnel and entry port. The gate key is address-independent
// now: (tunnel, inbound, entry port).
func TestR18B405RelayDedupIgnoresLandingAddressChange(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	var kb model.NodeKernel
	if err := s.repo.DB().First(&kb, s.kernelID).Error; err != nil {
		t.Fatalf("load kernel row: %v", err)
	}
	in := seedChainInbound(t, s.repo, &kb, "b4-05-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	in.RelayWhitelist = 1
	if err := s.repo.SaveNodeInbound(in); err != nil {
		t.Fatalf("save whitelisted inbound: %v", err)
	}
	payload := map[string]interface{}{"inboundId": in.ID, "tunnelId": s.tunnelID, "inPort": 30007, "name": "b4-05-relay"}
	body := r2b2AuthPost(t, s.h.inboundRelayCreate, "/api/v1/inbound/relay/create", payload, "b405-create-1")
	var env response.R
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode first create response: %v (%s)", err, body)
	}
	if env.Code != 0 {
		t.Fatalf("first relay create must succeed, got %s", body)
	}
	// Sanity: the stored forward points at the inbound node's address.
	var fwdID int64
	if err := s.repo.DB().Raw(`SELECT forward_id FROM node_inbound_relay WHERE inbound_id = ? AND name = ?`, in.ID, "b4-05-relay").Scan(&fwdID).Error; err != nil || fwdID == 0 {
		t.Fatalf("find created relay: forward_id=%d err=%v", fwdID, err)
	}
	var remoteAddr string
	if err := s.repo.DB().Raw(`SELECT remote_addr FROM forward WHERE id = ?`, fwdID).Scan(&remoteAddr).Error; err != nil {
		t.Fatalf("read forward remote addr: %v", err)
	}
	if !strings.Contains(remoteAddr, "203.0.113.10") {
		t.Fatalf("fixture must point the first relay at the inbound node's address, got %q", remoteAddr)
	}

	// The landing node moves: its address changes, the relay is the same.
	if err := s.repo.DB().Exec(`UPDATE node SET server_ip = ? WHERE id = ?`, "203.0.113.99", s.landingID).Error; err != nil {
		t.Fatalf("move landing node: %v", err)
	}
	body = r2b2AuthPost(t, s.h.inboundRelayCreate, "/api/v1/inbound/relay/create", payload, "b405-create-2")
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode second create response: %v (%s)", err, body)
	}
	if !strings.Contains(body, "已建有经同一隧道到相同落地目标的中转") {
		t.Fatalf("second create after a landing address change must hit the dedup gate, got %s", body)
	}
	var relayCount int64
	if err := s.repo.DB().Table("node_inbound_relay").Where("inbound_id = ?", in.ID).Count(&relayCount).Error; err != nil {
		t.Fatalf("count relays: %v", err)
	}
	if relayCount != 1 {
		t.Fatalf("relay rows for the inbound = %d, want 1 (no duplicate relay)", relayCount)
	}
}
