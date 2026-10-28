package kernel

import (
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// R6-L-01: when two different non-canonical spellings of "host" coexist
// with different values and no canonical key is present, the fallback of
// firstHeaderHostValue used to walk the map and pick whichever spelling
// the iteration happened to visit first, so the same stored row rendered
// a different host on different subscription generations. The verdict
// requires a deterministic tie-break: smallest key in byte order first,
// then the first element of that key's list. Canonical "Host" still wins
// over "host", and both still win over any non-canonical spelling.
func TestR6L01DualNonCanonicalDeterministic(t *testing.T) {
	// "HOST" < "hOsT" in byte order ('H' 0x48 < 'h' 0x68).
	m := Proxy{"HOST": "aaa.example.com", "hOsT": "bbb.example.com"}
	for i := 0; i < 1000; i++ {
		if got := firstHeaderHostValue(m); got != "aaa.example.com" {
			t.Fatalf("call %d: dual non-canonical pick = %q, want %q (byte-order smallest key must always win)", i, got, "aaa.example.com")
		}
	}
}

func TestR6L01DualNonCanonicalSwappedValues(t *testing.T) {
	// Winner follows the key, not the value: swapping the values must
	// not change which spelling wins.
	m := Proxy{"HOST": "bbb.example.com", "hOsT": "aaa.example.com"}
	for i := 0; i < 1000; i++ {
		if got := firstHeaderHostValue(m); got != "bbb.example.com" {
			t.Fatalf("call %d: swapped-values pick = %q, want %q", i, got, "bbb.example.com")
		}
	}
}

func TestR6L01CanonicalKeysStillWin(t *testing.T) {
	cases := []struct {
		name string
		m    Proxy
		want string
	}{
		{"host beats non-canonical", Proxy{"HOST": "aaa.example.com", "hOsT": "bbb.example.com", "host": "canon.example.com"}, "canon.example.com"},
		{"Host beats non-canonical", Proxy{"HOST": "aaa.example.com", "hOsT": "bbb.example.com", "Host": "canon.example.com"}, "canon.example.com"},
		{"Host beats host", Proxy{"Host": "upper.example.com", "host": "lower.example.com"}, "upper.example.com"},
		{"single non-canonical unchanged", Proxy{"hOsT": "solo.example.com"}, "solo.example.com"},
		{"smallest key list first element", Proxy{"HOST": []string{"first.example.com", "second.example.com"}, "hOsT": "other.example.com"}, "first.example.com"},
		{"interface list first element", Proxy{"HOST": []interface{}{"iface.example.com", "second.example.com"}, "hOsT": "other.example.com"}, "iface.example.com"},
		{"empty map", Proxy{}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for i := 0; i < 200; i++ {
				if got := firstHeaderHostValue(c.m); got != c.want {
					t.Fatalf("call %d: got %q, want %q", i, got, c.want)
				}
			}
		})
	}
}

// TestR6L01RandomCombinationDeterminism is the permanent regression gate
// for the tie-break: random subsets of the 16 case spellings of "host"
// with random distinct values, each probed 50 times. The expected winner
// is computed independently of headerHostValues (canonical priority,
// then byte-order smallest spelling with a non-empty value), so any
// map-iteration-order leak fails both the determinism and the exactness
// assertions. Fixed seed keeps the gate reproducible.
func TestR6L01RandomCombinationDeterminism(t *testing.T) {
	var spellings []string
	for mask := 0; mask < 16; mask++ {
		var b [4]byte
		for i, c := range "host" {
			if mask&(1<<i) != 0 {
				b[i] = byte(c - 32) // upper case
			} else {
				b[i] = byte(c)
			}
		}
		spellings = append(spellings, string(b[:]))
	}
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 300; iter++ {
		n := 2 + rng.Intn(4) // 2..5 keys
		perm := rng.Perm(len(spellings))[:n]
		m := Proxy{}
		for j, p := range perm {
			m[spellings[p]] = strings.Repeat("v", 1+j) + ".example.com"
		}
		// Independent expectation: canonical priority, then byte order.
		want := ""
		for _, k := range []string{"Host", "host"} {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				want = strings.TrimSpace(v)
				break
			}
		}
		if want == "" {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if k == "Host" || k == "host" {
					continue
				}
				if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
					want = strings.TrimSpace(v)
					break
				}
			}
		}
		for i := 0; i < 50; i++ {
			if got := firstHeaderHostValue(m); got != want {
				t.Fatalf("iter %d map %v call %d: got %q, want %q", iter, m, i, got, want)
			}
		}
	}
}
