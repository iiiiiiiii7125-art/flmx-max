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
