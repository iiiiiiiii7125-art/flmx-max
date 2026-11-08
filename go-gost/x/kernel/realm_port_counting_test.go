package kernel

import (
	"strings"
	"testing"
)

// TestRealmPortCountersFlowThroughStatus pins the agent half of the
// realm metering chain (FIX-REALM-METER): a realm instance's port —
// InboundID = RealmCounterBase (1<<40) + forwardID, network "tcp,udp"
// exactly as the panel renders it — must flow through allPorts into
// the nft counter ruleset and back out of Status with its id intact.
// The panel half (baseline timing) is covered by the handler test
// TestRealmForwardTrafficAfterApplyIsCredited.
func TestRealmPortCountersFlowThroughStatus(t *testing.T) {
	rec := stubNft(t)
	m := NewManager(t.TempDir())
	defer m.Shutdown()

	const realmID = int64(1<<40 + 42)
	m.mu.Lock()
	m.instances["realm"] = &instance{
		logs: &logRing{},
		req: ApplyRequest{
			Type:    "realm",
			Version: "2.9.6",
			Enabled: true,
			Ports:   []Port{{InboundID: realmID, Port: 20000, Network: "tcp,udp"}},
		},
	}
	m.mu.Unlock()

	ports := m.allPorts()
	if len(ports) != 1 || ports[0].InboundID != realmID {
		t.Fatalf("allPorts = %+v", ports)
	}

	nc := newNftCounters(t.TempDir())
	if err := nc.sync(ports, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(rec.scripts) == 0 {
		t.Fatal("no nft script captured")
	}
	script := rec.scripts[len(rec.scripts)-1]
	for _, want := range []string{
		"counter in_1099511627818_tcp {}",
		"counter out_1099511627818_tcp {}",
		"counter in_1099511627818_udp {}",
		"tcp dport 20000 counter name in_1099511627818_tcp",
		"tcp sport 20000 counter name out_1099511627818_tcp",
		"udp dport 20000 counter name in_1099511627818_udp",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}

	rec.listOut = `{"nftables":[{"metainfo":{}},{"counter":{"family":"inet","name":"in_1099511627818_tcp","table":"flmx_kernel","packets":3,"bytes":1200}},{"counter":{"family":"inet","name":"out_1099511627818_tcp","table":"flmx_kernel","bytes":3400}},{"counter":{"family":"inet","name":"in_1099511627818_udp","table":"flmx_kernel","bytes":56}}]}`
	status := m.Status("realm")
	if !status.NftAvailable {
		t.Fatalf("nft not available in status: %+v", status)
	}
	if len(status.Counters) != 3 {
		t.Fatalf("counters = %+v", status.Counters)
	}
	for _, c := range status.Counters {
		if c.InboundID != realmID {
			t.Fatalf("counter inbound id = %d, want %d (%+v)", c.InboundID, realmID, c)
		}
	}
}
