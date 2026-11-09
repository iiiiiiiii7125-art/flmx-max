package loader_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/loader"
	_ "github.com/go-gost/x/connector/http"
	_ "github.com/go-gost/x/dialer/tcp"
	_ "github.com/go-gost/x/handler/forward/local"
	"github.com/go-gost/x/internal/idle"
	xnet "github.com/go-gost/x/internal/net"
	_ "github.com/go-gost/x/listener/tcp"
	"github.com/go-gost/x/registry"
)

// F-212-A (agent layer): the panel writes "idleTimeout" (seconds)
// into every generated service's metadata in gost.json, and the
// agent loads that file through config.ReadFile (viper) before
// loader.Load publishes the window. The pre-fix loader scanned for
// the camelCase key on the parsed struct, but the file parse does
// not preserve that key's case, so the window never reached the
// forwarding plane: services loaded from a real file behaved as if
// the knob were off no matter what the file said (LIVE-3 PF-3a:
// config on disk with idleTimeout=60, silent session alive at
// 150s, zero teardown log lines).
//
// This test walks the production path end to end: a gost.json in
// the exact shape buildForwardServiceConfigs generates, parsed
// from disk, loaded, served; a silent client connection through
// the forward service must be torn down at about the configured
// window, and the error the handler logs for it must be the
// no-progress teardown error.
func TestIdleWindowFromConfigFileTearsDownSilentSession(t *testing.T) {
	unlock := config.LockMutation()
	defer unlock()
	original := config.Global()
	defer config.Set(original)
	oldWindow := idle.Get()
	defer idle.Set(oldWindow)
	defer func() {
		for name := range registry.ServiceRegistry().GetAll() {
			registry.ServiceRegistry().Unregister(name)
		}
	}()

	// Silent target: accepts and never speaks; the client never
	// speaks either, so the session is fully silent from birth.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("target listen: %v", err)
	}
	defer targetLn.Close()
	go func() {
		for {
			c, err := targetLn.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()

	const windowSeconds = 2
	gostJSON := fmt.Sprintf(`{
  "services": [
    {
      "name": "idle-file-fwd_tcp",
      "addr": "127.0.0.1:0",
      "handler": {"type": "tcp"},
      "listener": {"type": "tcp"},
      "forwarder": {
        "nodes": [{"name": "node_1", "addr": %q}],
        "selector": {"strategy": "fifo", "maxFails": 1, "failTimeout": "600s"}
      },
      "metadata": {"idleTimeout": %d}
    }
  ]
}`, targetLn.Addr().String(), windowSeconds)
	cfgPath := filepath.Join(t.TempDir(), "gost.json")
	if err := os.WriteFile(cfgPath, []byte(gostJSON), 0o644); err != nil {
		t.Fatalf("write gost.json: %v", err)
	}

	cfg := &config.Config{}
	if err := cfg.ReadFile(cfgPath); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := loader.Load(cfg); err != nil {
		t.Fatalf("loader.Load: %v", err)
	}
	if got := idle.Get(); got != windowSeconds*time.Second {
		t.Fatalf("window published from config file = %v, want %ds (metadata key lost between file parse and loader scan)", got, windowSeconds)
	}

	svc := registry.ServiceRegistry().Get("idle-file-fwd_tcp")
	if svc == nil {
		t.Fatal("service not registered")
	}
	go svc.Serve()
	svcAddr := svc.Addr().String()

	// The error the handler logs on teardown is Transport's return:
	// run one session through the public entry directly and require
	// the no-progress error at about the window.
	clientSide, agentSide := tcpPair(t)
	targetSide, agentTarget := tcpPair(t)
	defer clientSide.Close()
	defer targetSide.Close()
	transportErr := make(chan error, 1)
	go func() { transportErr <- xnet.Transport(agentSide, agentTarget) }()
	start := time.Now()
	select {
	case err := <-transportErr:
		if err != xnet.ErrNoProgress {
			t.Fatalf("Transport error = %v, want ErrNoProgress (the logged teardown line)", err)
		}
		if elapsed := time.Since(start); elapsed < 1500*time.Millisecond || elapsed > 8*time.Second {
			t.Fatalf("Transport tore down at %v, want ≈%ds window", elapsed, windowSeconds)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("silent Transport session not torn down within 10s (window %ds from config file)", windowSeconds)
	}

	// And through the real service: a silent client connection must
	// be closed by the agent at about the window.
	conn, err := net.DialTimeout("tcp", svcAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial service: %v", err)
	}
	defer conn.Close()
	start = time.Now()
	buf := make([]byte, 1)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, readErr := conn.Read(buf)
	elapsed := time.Since(start)
	if readErr == nil {
		t.Fatal("silent session read data from a silent target: impossible")
	}
	if nerr, ok := readErr.(net.Error); ok && nerr.Timeout() {
		t.Fatalf("silent session through file-configured service survived 10s; knob from gost.json never armed (elapsed %v)", elapsed)
	}
	if elapsed < 1500*time.Millisecond || elapsed > 8*time.Second {
		t.Fatalf("service tore down silent session at %v, want ≈%ds", elapsed, windowSeconds)
	}
}

// tcpPair returns the two ends of a loopback TCP connection: the
// first is the "peer" end the test drives, the second is the end
// handed to the code under test.
func tcpPair(t *testing.T) (peer, local net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pair listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	peer, err = net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("pair dial: %v", err)
	}
	select {
	case local = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("pair accept timeout")
	}
	return peer, local
}
