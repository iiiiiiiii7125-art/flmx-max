package kernel

import (
	"strings"
	"testing"
)

// Dedupe must not collapse same-gateway endpoints that differ only by
// credential: residential proxies serve distinct exits from one
// host:port, told apart by the username's session id. Identical
// credentials on the same address still dedupe to one.
func TestSubDedupeKeepsDistinctCredentials(t *testing.T) {
	mk := func(name, user string) Endpoint {
		return Endpoint{Name: name, Host: "198.51.100.60", Port: 1001, Proxy: Proxy{
			"type": "socks5", "server": "198.51.100.60", "port": 1001,
			"username": user, "password": "pw",
		}}
	}
	eps := []Endpoint{
		mk("ip1", "client_session-111"),
		mk("ip2", "client_session-222"),
		mk("ip1-again", "client_session-111"),
	}
	got := ApplySubOptions(eps, SubOptions{Dedupe: true})
	if names := strings.Join(endpointNames(got), ","); names != "ip1,ip2" {
		t.Fatalf("distinct sessions must survive dedupe, identical one must drop: got %q", names)
	}
}
