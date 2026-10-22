package handler

import (
	"bytes"
	"database/sql"
	"log"
	"strings"
	"testing"
	"time"

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

// F-A-01/C-01: the custom share address takes the import surface's
// host standard. Anything carrying URI/line structure characters —
// or the loopback/localhost/non-canonical forms the import surface
// rejects — must fail at save time; plain hostnames and routable or
// RFC1918 IPs pass.
func TestValidateShareAddressCustomShape(t *testing.T) {
	node := shareTestNode()
	for _, host := range []string{
		"entry.example.com", "192.0.2.55", "2001:db8::9", "[2001:db8::9]",
		"10.0.0.8", "node1",
	} {
		if err := validateShareAddress("custom", host, node); err != nil {
			t.Fatalf("custom %q must pass: %v", host, err)
		}
	}
	for _, host := range []string{
		"good.com@evil.com", "a b.com", "evil.com/path", "evil.com?x=1",
		"evil.com#frag", "a,b.com", "a\nb.com", "localhost", "127.0.0.1",
		"0.0.0.0", "2130706433", "169.254.169.254",
	} {
		if err := validateShareAddress("custom", host, node); err == nil {
			t.Fatalf("custom %q must be rejected", host)
		}
	}
}

func captureHandlerLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

func seedShareInbound(t *testing.T, r *repo.Repository, nodeID int64, name string) *model.NodeInbound {
	t.Helper()
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	return seedChainInbound(t, r, box, name, kernel.Inbound{Protocol: kernel.ProtocolVLESS, Listen: "::", Port: 443}, 0)
}

// F-A-01/C-01 pull-time guard: a custom ShareHost written before the
// save-side validation existed (stored bad value) must not be spliced
// into subscription endpoints — the endpoint is skipped and logged.
// A well-formed custom host keeps flowing unchanged.
func TestInboundEndpointsSkipsInvalidCustomShareHost(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/share-bad.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "badhost", "192.0.2.1")
	row := seedShareInbound(t, r, nodeID, "bad-in")
	row.ShareAddressMode = "custom"
	row.ShareHost = "good.com@evil.com"
	if err := r.SaveNodeInbound(row); err != nil {
		t.Fatal(err)
	}
	logs := captureHandlerLogs(t)
	endpoints, _ := h.inboundEndpoints([]model.NodeInbound{*row})
	if len(endpoints) != 0 {
		t.Fatalf("invalid custom share host must be skipped, got %+v", endpoints)
	}
	if !strings.Contains(logs.String(), "invalid custom share host") {
		t.Fatalf("the skip must be logged, logs: %q", logs.String())
	}
	row.ShareHost = "entry.example.com"
	if err := r.SaveNodeInbound(row); err != nil {
		t.Fatal(err)
	}
	row, err = r.GetNodeInbound(row.ID)
	if err != nil || row == nil {
		t.Fatalf("reload: %v", err)
	}
	endpoints, _ = h.inboundEndpoints([]model.NodeInbound{*row})
	if len(endpoints) != 1 || endpoints[0].Host != "entry.example.com" {
		t.Fatalf("valid custom host must flow: %+v", endpoints)
	}
}

// F-A-02: an explicit family mode whose address later disappears from
// the node resolves to no endpoint — that must be logged, not silent.
// The auto mode resolving empty (node has no address at all) stays
// quiet: nothing was ever promised there.
func TestInboundEndpointsUnresolvedFamilyModeLogged(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/share-unresolved.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	v4Only := seedRelayTargetNode(t, r, "v4only", "192.0.2.1")
	row := seedShareInbound(t, r, v4Only, "v6-in")
	row.ShareAddressMode = "ipv6"
	if err := r.SaveNodeInbound(row); err != nil {
		t.Fatal(err)
	}
	logs := captureHandlerLogs(t)
	endpoints, _ := h.inboundEndpoints([]model.NodeInbound{*row})
	if len(endpoints) != 0 {
		t.Fatalf("ipv6 mode without a node IPv6 must yield no endpoint, got %+v", endpoints)
	}
	if !strings.Contains(logs.String(), "share address unresolved") {
		t.Fatalf("the unresolved mode must be logged, logs: %q", logs.String())
	}

	bare := &model.Node{Name: "bare", Secret: "bare"}
	if err := r.DB().Create(bare).Error; err != nil {
		t.Fatal(err)
	}
	autoRow := seedShareInbound(t, r, bare.ID, "auto-in")
	logs.Reset()
	endpoints, _ = h.inboundEndpoints([]model.NodeInbound{*autoRow})
	if len(endpoints) != 0 {
		t.Fatalf("addressless node must yield no endpoint, got %+v", endpoints)
	}
	if strings.Contains(logs.String(), "share address unresolved") {
		t.Fatalf("auto mode must not log unresolved noise, logs: %q", logs.String())
	}
}

// F-A-01/C-01: the chain next hop applies the same guard — a stored
// bad custom host fails loudly instead of entering chained configs.
func TestChainEndpointRejectsInvalidCustomShareHost(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/share-chain.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	front := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	target := seedRelayTargetNode(t, r, "target", "192.0.2.20")
	row := seedShareInbound(t, r, target, "chain-target")
	row.ShareAddressMode = "custom"
	row.ShareHost = "good.com@evil.com"
	if err := r.SaveNodeInbound(row); err != nil {
		t.Fatal(err)
	}
	if _, err := h.chainEndpoint(row, front); err == nil {
		t.Fatal("invalid custom share host must fail the chain endpoint")
	}
	row.ShareHost = "exit.example.com"
	if err := r.SaveNodeInbound(row); err != nil {
		t.Fatal(err)
	}
	ep, err := h.chainEndpoint(row, front)
	if err != nil || ep.Host != "exit.example.com" {
		t.Fatalf("valid custom host must pass: %+v %v", ep, err)
	}
}

// F-A-01/C-01: a stored bad relay ShareHost falls back to the entry's
// own address — the relay endpoint is neither poisoned nor dropped.
func TestInboundEndpointsInvalidRelayShareHostFallsBack(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/share-relay.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	entry := seedRelayTargetNode(t, r, "entry", "198.51.100.10")
	landing := seedRelayTargetNode(t, r, "landing", "192.0.2.30")
	row := seedShareInbound(t, r, landing, "landing-in")
	now := time.Now().UnixMilli()
	db := r.DB()
	tunnel := model.Tunnel{Name: "line", Type: 1, Protocol: "tcp", Flow: 99999, Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	fwd := model.Forward{UserID: 1, UserName: "admin", Name: "line", TunnelID: tunnel.ID, RemoteAddr: "203.0.113.1:443", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := db.Create(&fwd).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ForwardPort{ForwardID: fwd.ID, NodeID: entry, Port: 45001}).Error; err != nil {
		t.Fatal(err)
	}
	rel := model.NodeInboundRelay{InboundID: row.ID, ForwardID: fwd.ID, Name: "line", ShareHost: "good.com@evil.com", Managed: 1, CreatedTime: now}
	if err := db.Create(&rel).Error; err != nil {
		t.Fatal(err)
	}
	logs := captureHandlerLogs(t)
	endpoints, kinds := h.inboundEndpoints([]model.NodeInbound{*row})
	var relayEP *kernel.Endpoint
	for i, e := range endpoints {
		if kinds[i] == "relay" {
			ep := e
			relayEP = &ep
		}
	}
	if relayEP == nil {
		t.Fatalf("relay endpoint must survive a bad ShareHost, got %+v", endpoints)
	}
	if relayEP.Host != "198.51.100.10" || relayEP.Port != 45001 {
		t.Fatalf("relay endpoint must fall back to the entry address, got %+v", relayEP)
	}
	if !strings.Contains(logs.String(), "invalid relay share host") {
		t.Fatalf("the fallback must be logged, logs: %q", logs.String())
	}
	rel.ShareHost = "relay.example.com"
	if err := db.Save(&rel).Error; err != nil {
		t.Fatal(err)
	}
	endpoints, kinds = h.inboundEndpoints([]model.NodeInbound{*row})
	for i, e := range endpoints {
		if kinds[i] == "relay" && e.Host != "relay.example.com" {
			t.Fatalf("valid relay ShareHost must be used, got %+v", e)
		}
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
