package main

import "testing"

// C2-F07 gate: the unauthenticated pprof listener must always end
// up on loopback, whatever the configured address says.
func TestLoopbackOnlyAddr(t *testing.T) {
	cases := map[string]string{
		":6060":            "127.0.0.1:6060",
		"0.0.0.0:6060":     "127.0.0.1:6060",
		"192.0.2.10:9999":  "127.0.0.1:9999",
		"localhost:6060":   "127.0.0.1:6060",
		"127.0.0.1:6060":   "127.0.0.1:6060",
		"[::]:6060":        "127.0.0.1:6060",
		"garbage":          "127.0.0.1:6060",
		"127.0.0.1:":       "127.0.0.1:6060",
		"10.0.0.5:12345":   "127.0.0.1:12345",
		"example.com:8080": "127.0.0.1:8080",
	}
	for in, want := range cases {
		if got := loopbackOnlyAddr(in); got != want {
			t.Errorf("loopbackOnlyAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
