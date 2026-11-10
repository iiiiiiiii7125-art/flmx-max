package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// B-04: empty KernelStatus must serialize counters/users (and
// kernels) as [], never null or a missing key.

func TestStatusEmptySerializesEmptyArrays(t *testing.T) {
	m := NewManager(t.TempDir())
	b, err := json.Marshal(m.Status(""))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{`"kernels":[]`, `"counters":[]`, `"users":[]`} {
		if !strings.Contains(s, key) {
			t.Fatalf("empty Status JSON = %s, want containing %s", s, key)
		}
	}
	if strings.Contains(s, `"counters":null`) || strings.Contains(s, `"users":null`) {
		t.Fatalf("empty Status JSON = %s, must not contain null counters/users", s)
	}
}

func TestParseCountersEmptySerializesEmptyArrays(t *testing.T) {
	counters, users, err := parseCounters([]byte(`{"nftables":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if counters == nil || users == nil {
		t.Fatalf("parseCounters empty = %v / %v, want non-nil empty slices", counters, users)
	}
	b, _ := json.Marshal(Status{Kernels: []State{}, Counters: counters, Users: users})
	if !strings.Contains(string(b), `"counters":[]`) || !strings.Contains(string(b), `"users":[]`) {
		t.Fatalf("marshaled = %s, want [] counters/users", b)
	}
}

// R2-F2-23: a logs query for a kernel type with no instance on the
// node must say so (MissingLogsFor), instead of returning a Kernels
// list that simply omits the type — the omission is what the panel
// used to mistake for "installed but no logs yet".
func TestStatusMissingLogsFor(t *testing.T) {
	m := NewManager(t.TempDir())
	defer m.Shutdown()

	// No instances at all: the requested type is missing.
	if got := m.Status("sing-box").MissingLogsFor; got != "sing-box" {
		t.Fatalf("Status(sing-box).MissingLogsFor = %q, want sing-box", got)
	}
	// No logs requested (the periodic poll): no signal.
	if got := m.Status("").MissingLogsFor; got != "" {
		t.Fatalf("Status(\"\").MissingLogsFor = %q, want empty", got)
	}
	// The signal survives JSON serialization (this is the wire
	// shape the panel remarshals).
	b, err := json.Marshal(m.Status("sing-box"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"missingLogsFor":"sing-box"`) {
		t.Fatalf("Status JSON = %s, want missingLogsFor sing-box", string(b))
	}

	// With an instance of the requested type present, no signal —
	// and a different absent type is still named.
	m.mu.Lock()
	m.instances["sing-box"] = &instance{
		logs: &logRing{},
		req:  ApplyRequest{Type: "sing-box", Enabled: true},
	}
	m.mu.Unlock()
	if got := m.Status("sing-box").MissingLogsFor; got != "" {
		t.Fatalf("Status(sing-box) with an instance: MissingLogsFor = %q, want empty", got)
	}
	if got := m.Status("xray").MissingLogsFor; got != "xray" {
		t.Fatalf("Status(xray) without an instance: MissingLogsFor = %q, want xray", got)
	}
}
