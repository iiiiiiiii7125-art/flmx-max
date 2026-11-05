package net

import "testing"

// D3-09 regression: IsIPv4 decides tcp4/udp4 vs tcp/udp at ~35 listener
// call sites. It must answer true only for a literal IPv4 host; a
// hostname must stay on the unspecified network so the resolver can
// pick the family (previously any non-empty, non-':'/non-'[' first byte
// — i.e. every hostname — was classified IPv4 and lost IPv6).
func TestIsIPv4(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"192.0.2.1:8080", true},
		{"0.0.0.0:53", true},
		{"127.0.0.1:6365", true},
		{"[::1]:8080", false},
		{"[2001:db8::1]:53", false},
		{":8080", false},
		{"", false},
		{"localhost:8080", false},
		{"node.example.com:8080", false},
		{"example.com:53", false},
	}
	for _, tt := range tests {
		if got := IsIPv4(tt.addr); got != tt.want {
			t.Errorf("IsIPv4(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}
