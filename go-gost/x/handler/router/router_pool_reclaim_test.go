package router

import (
	"io"
	"testing"

	"github.com/go-gost/relay"
)

// R2-P1-F04 regression: ConnectorPool.Del must reclaim the Router
// entry (and the per-host key inside it) once a router's last
// connector is removed. Before the fix, Del only spliced the
// connector out of its host slice, leaving an empty Router shell
// in the pool map forever — one shell per historical router ID,
// with no idle sweep to bound the growth.

func reclaimTestIDs() (relay.TunnelID, relay.ConnectorID, relay.ConnectorID) {
	rid := relay.NewTunnelID([]byte("0123456789abcdefghij"))
	cid1 := relay.NewConnectorID([]byte("connector-id-00001"))
	cid2 := relay.NewConnectorID([]byte("connector-id-00002"))
	return rid, cid1, cid2
}

func TestPoolDelReclaimsEmptyRouter(t *testing.T) {
	rid, cid1, _ := reclaimTestIDs()
	p := NewConnectorPool("node")

	p.Add(rid, NewConnector(rid, cid1, "host-a", io.Discard, nil))
	if got := len(p.routers); got != 1 {
		t.Fatalf("after Add: len(routers) = %d, want 1", got)
	}
	if c := p.Get(rid, "host-a"); c == nil {
		t.Fatal("after Add: Get returned nil")
	}

	p.Del(rid, "host-a", cid1)
	if got := len(p.routers); got != 0 {
		t.Fatalf("after Del of last connector: len(routers) = %d, want 0 (shell reclaimed)", got)
	}
	if c := p.Get(rid, "host-a"); c != nil {
		t.Fatalf("after reclaim: Get returned %v, want nil", c)
	}

	// A later association for the same rid must create a fresh
	// router and route normally.
	p.Add(rid, NewConnector(rid, cid1, "host-a", io.Discard, nil))
	if c := p.Get(rid, "host-a"); c == nil {
		t.Fatal("after re-Add: Get returned nil")
	}
}

func TestPoolDelKeepsRouterWithRemainingHosts(t *testing.T) {
	rid, cid1, cid2 := reclaimTestIDs()
	p := NewConnectorPool("node")

	p.Add(rid, NewConnector(rid, cid1, "host-a", io.Discard, nil))
	p.Add(rid, NewConnector(rid, cid2, "host-b", io.Discard, nil))

	p.Del(rid, "host-a", cid1)
	r := p.routers[rid]
	if r == nil {
		t.Fatal("router reclaimed while host-b connector remains")
	}
	if _, ok := r.connectors["host-a"]; ok {
		t.Fatal("empty host-a key retained after its last connector was deleted")
	}
	if c := p.Get(rid, "host-b"); c == nil {
		t.Fatal("host-b connector lost after host-a deletion")
	}

	p.Del(rid, "host-b", cid2)
	if got := len(p.routers); got != 0 {
		t.Fatalf("after Del of final connector: len(routers) = %d, want 0", got)
	}
}

func TestPoolDelUnknownIsNoop(t *testing.T) {
	rid, cid1, _ := reclaimTestIDs()
	p := NewConnectorPool("node")

	p.Del(rid, "host-a", cid1) // no router at all
	p.Add(rid, NewConnector(rid, cid1, "host-a", io.Discard, nil))
	p.Del(rid, "other-host", cid1) // router exists, host does not
	if got := len(p.routers); got != 1 {
		t.Fatalf("Del with unknown host removed the router: len(routers) = %d, want 1", got)
	}
	if c := p.Get(rid, "host-a"); c == nil {
		t.Fatal("connector lost after Del against an unknown host")
	}
}
