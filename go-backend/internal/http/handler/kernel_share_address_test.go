package handler

import (
	"database/sql"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// DB-2 (upstream 3.3.33): the share address mode only picks WHERE an
// inbound's share/subscription address comes from. These tests pin the
// resolution matrix, the legacy equivalence (rows saved before the mode
// existed must resolve exactly like the old ShareHost→ServerIP rule),
// persistence through the save whitelist, and the endpoint consumers.

func shareTestNode() *model.Node {
	return &model.Node{
		ServerIP:   "192.0.2.10",
		ServerIPV4: sql.NullString{String: "192.0.2.1", Valid: true},
		ServerIPV6: sql.NullString{String: "2001:db8::1", Valid: true},
	}
}

func TestInboundShareModeCompat(t *testing.T) {
	for _, tc := range []struct{ mode, host, want string }{
		{"", "", "auto"},
		{"", "old.example.com", "custom"},     // pre-mode rows with a host keep overriding
		{"auto", "old.example.com", "custom"}, // ditto when auto was stored explicitly
		{"auto", "", "auto"},
		{"custom", "", "custom"},
		{"ipv4", "", "ipv4"},
		{"ipv6", "ignored.example.com", "ipv6"},
	} {
		row := &model.NodeInbound{ShareAddressMode: tc.mode, ShareHost: tc.host}
		if got := inboundShareMode(row); got != tc.want {
			t.Fatalf("mode=%q host=%q: got %q want %q", tc.mode, tc.host, got, tc.want)
		}
	}
}

func TestInboundShareAddressMatrix(t *testing.T) {
	node := shareTestNode()
	for _, tc := range []struct{ mode, host, want string }{
		// Legacy equivalence: the old rule was ShareHost, else node.ServerIP.
		{"", "", "192.0.2.10"},
		{"", "old.example.com", "old.example.com"},
		{"auto", "", "192.0.2.10"},
		{"auto", "old.example.com", "old.example.com"},
		{"ipv4", "", "192.0.2.1"},
		{"ipv6", "", "2001:db8::1"},
		{"custom", "entry.example.com", "entry.example.com"},
		{"custom", "[2001:db8::9]", "2001:db8::9"},
	} {
		row := &model.NodeInbound{ShareAddressMode: tc.mode, ShareHost: tc.host}
		if got := inboundShareAddress(row, node); got != tc.want {
			t.Fatalf("mode=%q host=%q: got %q want %q", tc.mode, tc.host, got, tc.want)
		}
	}
	// auto falls back ServerIP → IPv4 → IPv6 when the primary is empty.
	noMain := &model.Node{ServerIPV4: sql.NullString{String: "192.0.2.1", Valid: true}, ServerIPV6: sql.NullString{String: "2001:db8::1", Valid: true}}
	if got := inboundShareAddress(&model.NodeInbound{}, noMain); got != "192.0.2.1" {
		t.Fatalf("auto fallback to v4: got %q", got)
	}
	v6Only := &model.Node{ServerIPV6: sql.NullString{String: "2001:db8::1", Valid: true}}
	if got := inboundShareAddress(&model.NodeInbound{}, v6Only); got != "2001:db8::1" {
		t.Fatalf("auto fallback to v6: got %q", got)
	}
	if got := inboundShareAddress(&model.NodeInbound{}, nil); got != "" {
		t.Fatalf("nil node: got %q", got)
	}
	// The family follows later node edits instead of freezing a copy.
	node.ServerIPV6.String = "2001:db8::5678"
	row := &model.NodeInbound{ShareAddressMode: "ipv6"}
	if got := inboundShareAddress(row, node); got != "2001:db8::5678" {
		t.Fatalf("stale node address: got %q", got)
	}
}

func TestValidateShareAddress(t *testing.T) {
	node := shareTestNode()
	if err := validateShareAddress("auto", "", node); err != nil {
		t.Fatalf("auto: %v", err)
	}
	if err := validateShareAddress("ipv4", "", node); err != nil {
		t.Fatalf("ipv4: %v", err)
	}
	if err := validateShareAddress("custom", "entry.example.com", node); err != nil {
		t.Fatalf("custom: %v", err)
	}
	if err := validateShareAddress("custom", "", node); err == nil {
		t.Fatal("empty custom address must fail")
	}
	// A missing family must error, never silently fall back to the other one.
	v4Only := &model.Node{ServerIP: "192.0.2.1"}
	if err := validateShareAddress("ipv6", "", v4Only); err == nil {
		t.Fatal("missing IPv6 must not silently fall back to IPv4")
	}
	if err := validateShareAddress("ipv4", "", &model.Node{ServerIP: "2001:db8::1"}); err == nil {
		t.Fatal("an IPv6 literal in server_ip is not an IPv4 address")
	}
	if err := validateShareAddress("bogus", "", node); err == nil {
		t.Fatal("unknown mode must fail")
	}
}

func TestShareAddressModePersistenceAndEndpoints(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/share.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	id := seedRelayTargetNode(t, r, "dual", "192.0.2.1")
	if err := r.DB().Model(&model.Node{}).Where("id = ?", id).Update("server_ip_v6", "2001:db8::1234").Error; err != nil {
		t.Fatal(err)
	}
	node, err := r.GetNodeByID(id)
	if err != nil || node == nil {
		t.Fatalf("node: %v", err)
	}
	box := seedChainKernel(t, r, id, kernel.TypeSingbox)
	row := seedChainInbound(t, r, box, "dual-in", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Listen: "::", Port: 443}, 0)

	// Legacy row (no mode stored): the endpoint host is the old result —
	// ShareHost empty → node ServerIP.
	endpoints, _ := h.inboundEndpoints([]model.NodeInbound{*row})
	if len(endpoints) != 1 || endpoints[0].Host != "192.0.2.1" {
		t.Fatalf("legacy endpoint: %+v", endpoints)
	}

	// The mode persists through SaveNodeInbound's column whitelist and
	// the subscription endpoint follows it.
	row.ShareAddressMode = "ipv6"
	if err := r.SaveNodeInbound(row); err != nil {
		t.Fatal(err)
	}
	row, err = r.GetNodeInbound(row.ID)
	if err != nil || row == nil || row.ShareAddressMode != "ipv6" {
		t.Fatalf("mode did not persist: %+v %v", row, err)
	}
	endpoints, _ = h.inboundEndpoints([]model.NodeInbound{*row})
	if len(endpoints) != 1 || endpoints[0].Host != "2001:db8::1234" {
		t.Fatalf("ipv6 endpoint: %+v", endpoints)
	}
	link, _ := kernel.ShareURI(endpoints[0])
	if !strings.Contains(link, "@[2001:db8::1234]:443") {
		t.Fatalf("IPv6 share URI: %s", link)
	}

	// The chain next-hop resolves through the same source.
	front := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	frontBox := seedChainKernel(t, r, front, kernel.TypeSingbox)
	entry := seedChainInbound(t, r, frontBox, "entry", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Listen: "::", Port: 8443}, row.ID)
	ep, err := h.chainEndpoint(row, entry.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Host != "2001:db8::1234" {
		t.Fatalf("chain next hop: %+v", ep)
	}

	// Backup export carries the mode and import restores it (upsert path).
	backup, err := r.ExportAll()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ib := range backup.Inbounds.Inbounds {
		if ib.ID == row.ID {
			found = true
			if ib.ShareAddressMode != "ipv6" {
				t.Fatalf("backup mode: %q", ib.ShareAddressMode)
			}
		}
	}
	if !found {
		t.Fatal("inbound missing from backup")
	}
	row.ShareAddressMode = "auto"
	if err := r.SaveNodeInbound(row); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Import(backup, []string{"inbounds"}); err != nil {
		t.Fatal(err)
	}
	row, err = r.GetNodeInbound(row.ID)
	if err != nil || row == nil || row.ShareAddressMode != "ipv6" {
		t.Fatalf("mode after import: %+v %v", row, err)
	}
	_ = node
}
