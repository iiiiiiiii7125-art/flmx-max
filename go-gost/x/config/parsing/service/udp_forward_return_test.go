package service_test

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-gost/x/config"
	parsing_service "github.com/go-gost/x/config/parsing/service"
	_ "github.com/go-gost/x/connector/direct"
	_ "github.com/go-gost/x/connector/forward"
	_ "github.com/go-gost/x/connector/http"
	_ "github.com/go-gost/x/connector/relay"
	_ "github.com/go-gost/x/connector/router"
	_ "github.com/go-gost/x/connector/socks/v5"
	_ "github.com/go-gost/x/connector/ss"
	_ "github.com/go-gost/x/connector/tcp"
	_ "github.com/go-gost/x/connector/tunnel"
	_ "github.com/go-gost/x/dialer/direct"
	_ "github.com/go-gost/x/dialer/tcp"
	_ "github.com/go-gost/x/dialer/tls"
	_ "github.com/go-gost/x/dialer/udp"
	_ "github.com/go-gost/x/dialer/ws"
	_ "github.com/go-gost/x/handler/forward/local"
	_ "github.com/go-gost/x/handler/http"
	_ "github.com/go-gost/x/handler/relay"
	_ "github.com/go-gost/x/handler/router"
	_ "github.com/go-gost/x/handler/socks/v5"
	_ "github.com/go-gost/x/handler/tunnel"
	_ "github.com/go-gost/x/listener/tcp"
	_ "github.com/go-gost/x/listener/tls"
	_ "github.com/go-gost/x/listener/udp"
	_ "github.com/go-gost/x/listener/ws"
)

// Regression contract for the DEFECT-UDP-RETURN-01 investigation
// (2026-11-09): a panel-shaped direct forward service (handler and
// listener type = protocol, listener metadata keepAlive+ttl, single
// forwarder node) must return UDP payloads to the client. The defect
// was not reproducible at the investigated tip; this test pins the
// healthy behaviour so a future regression is caught in CI rather
// than on a live batch. Environments whose loopback UDP replies are
// blocked by a sandbox egress guard are detected up front and skip.

func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func startUDPEcho(t *testing.T, port int) (count func() int) {
	t.Helper()
	pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	n := 0
	go func() {
		buf := make([]byte, 65535)
		for {
			m, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			n++
			if _, err := pc.WriteTo(buf[:m], addr); err != nil {
				println("echo target WriteTo failed:", err.Error())
			}
		}
	}()
	return func() int { return n }
}

func startTCPEcho(t *testing.T, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 65535)
				for {
					m, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, err := c.Write(buf[:m]); err != nil {
						return
					}
				}
			}(c)
		}
	}()
}

// requireLoopbackUDP skips the test when this environment cannot
// complete a plain loopback UDP round trip (some sandboxes block
// replies to ephemeral ports at the syscall level, which would
// masquerade as a product defect).
func requireLoopbackUDP(t *testing.T, echoPort int) {
	t.Helper()
	if !probeUDP(t, echoPort, "env-self-check", 1500*time.Millisecond) {
		t.Skip("environment cannot complete a direct loopback UDP round trip; UDP return path is not verifiable here")
	}
}

func buildForwardService(t *testing.T, name, proto string, listenPort, targetPort int) {
	t.Helper()
	raw := fmt.Sprintf(`{
		"name": %q,
		"addr": "127.0.0.1:%d",
		"handler": {"type": %q},
		"listener": {"type": %q},
		"forwarder": {
			"nodes": [{"name": "node_1", "addr": "127.0.0.1:%d"}]
		}
	}`, name, listenPort, proto, proto, targetPort)
	if proto == "udp" {
		raw = strings.Replace(raw, `"listener": {"type": "udp"}`,
			`"listener": {"type": "udp", "metadata": {"keepAlive": true, "ttl": "30s"}}`, 1)
	}
	var cfg config.ServiceConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal service config: %v", err)
	}
	svc, err := parsing_service.ParseService(&cfg)
	if err != nil {
		t.Fatalf("ParseService: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	go svc.Serve()
}

// probeUDP sends one datagram from a fresh client socket and reports
// whether the identical payload came back within timeout.
func probeUDP(t *testing.T, svcPort int, payload string, timeout time.Duration) bool {
	t.Helper()
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: svcPort})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 65535)
	n, err := c.Read(buf)
	if err != nil {
		return false
	}
	return string(buf[:n]) == payload
}

func TestUDPForwardReturnsPayload(t *testing.T) {
	targetPort := freeUDPPort(t)
	echoCount := startUDPEcho(t, targetPort)
	requireLoopbackUDP(t, targetPort)

	svcPort := freeUDPPort(t)
	buildForwardService(t, "contract_udp", "udp", svcPort, targetPort)

	// readiness: sacrificial probes until the service answers or 3s pass
	deadline := time.Now().Add(3 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if probeUDP(t, svcPort, "ready-check", 500*time.Millisecond) {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("UDP forward never returned a payload (target echo count=%d)", echoCount())
	}

	// real probes, each on a fresh association
	for i := 0; i < 3; i++ {
		payload := fmt.Sprintf("probe-%d-%s", i, strings.Repeat("x", 20))
		if !probeUDP(t, svcPort, payload, 2*time.Second) {
			t.Fatalf("probe %d got no echo back (target count=%d)", i, echoCount())
		}
	}
}

func TestTCPForwardReturnsPayload(t *testing.T) {
	targetPort := freeTCPPort(t)
	startTCPEcho(t, targetPort)
	svcPort := freeTCPPort(t)
	buildForwardService(t, "contract_tcp", "tcp", svcPort, targetPort)

	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", svcPort), 300*time.Millisecond)
		if err == nil {
			payload := "tcp-control-payload"
			c.SetDeadline(time.Now().Add(2 * time.Second))
			if _, err := c.Write([]byte(payload)); err != nil {
				t.Fatalf("tcp write: %v", err)
			}
			buf := make([]byte, 65535)
			n, err := c.Read(buf)
			c.Close()
			if err != nil {
				t.Fatalf("tcp read: %v", err)
			}
			if string(buf[:n]) != payload {
				t.Fatalf("tcp echo mismatch: %q", buf[:n])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tcp service never accepted: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
