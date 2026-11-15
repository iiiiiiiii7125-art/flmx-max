package socket

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corelogger "github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	_ "github.com/go-gost/x/handler/auto"
	_ "github.com/go-gost/x/listener/tcp"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
)

// C-04: updateServices must mark a bind conflict (port_conflict) so the
// panel can tell an orphan listener apart from a missing service, and
// pauseServices must fail loud when the established-connection kill
// cannot run (old code discarded the error and let connections drain).
// The kill only runs when the port actually has established connections
// (scan of /proc/net): with no connections there is nothing to kill, and
// a missing killer must not fail an otherwise clean pause (R19 shape).

func c04FreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func c04CreateRealService(t *testing.T, name string, port int) {
	t.Helper()
	corelogger.SetDefault(xlogger.Nop())
	r19SeedSocketConfig(t, &config.Config{})
	registry.ServiceRegistry().Unregister(name)
	t.Cleanup(func() {
		if svc := registry.ServiceRegistry().Get(name); svc != nil {
			svc.Close()
		}
		registry.ServiceRegistry().Unregister(name)
	})
	cfg := config.ServiceConfig{Name: name, Addr: fmt.Sprintf("127.0.0.1:%d", port)}
	if err := updateServices(updateServicesRequest{Data: []config.ServiceConfig{cfg}}); err != nil {
		t.Fatalf("create service: %v", err)
	}
}

func c04WriteFakeBin(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

// c04FixedAddrService is a recording service that reports a fixed listener
// address (like a real service bound to a concrete port).
type c04FixedAddrService struct {
	closed int
	addr   net.Addr
}

func (s *c04FixedAddrService) Serve() error   { return nil }
func (s *c04FixedAddrService) Addr() net.Addr { return s.addr }
func (s *c04FixedAddrService) Close() error   { s.closed++; return nil }

// c04EstablishedListener returns a plain listener with one established
// client connection held open. Accept returns only after the handshake,
// so the server-side socket is ESTABLISHED and visible in /proc/net/tcp.
func c04EstablishedListener(t *testing.T) (net.Listener, net.Conn, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	cli, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	select {
	case srv := <-accepted:
		t.Cleanup(func() { srv.Close(); cli.Close(); ln.Close() })
		return ln, cli, port
	case <-time.After(5 * time.Second):
		ln.Close()
		cli.Close()
		t.Fatal("listener did not accept within 5s")
		return nil, nil, 0
	}
}

func c04RegisterFixedAddrService(t *testing.T, name string, addr net.Addr) {
	t.Helper()
	corelogger.SetDefault(xlogger.Nop())
	r19SeedSocketConfig(t, &config.Config{})
	svc := &c04FixedAddrService{addr: addr}
	registry.ServiceRegistry().Unregister(name)
	t.Cleanup(func() { registry.ServiceRegistry().Unregister(name) })
	if err := registry.ServiceRegistry().Register(name, svc); err != nil {
		t.Fatalf("register service: %v", err)
	}
	if !r19SafeSet(t, &config.Config{Services: []*config.ServiceConfig{
		{Name: name, Addr: addr.String()},
	}}) {
		t.Fatalf("cannot seed service config")
	}
}

func c04AssertPaused(t *testing.T, name string) {
	t.Helper()
	cfg, ok := r19SafeGlobal(t)
	if !ok {
		return
	}
	for _, s := range cfg.Services {
		if s != nil && s.Name == name {
			if s.Metadata == nil || s.Metadata["paused"] != true {
				t.Fatalf("service %s not marked paused after pause: %#v", name, s.Metadata)
			}
			return
		}
	}
	t.Fatalf("service %s missing from config after pause", name)
}

func TestC04UpdateServicesPortConflictMarked(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	corelogger.SetDefault(xlogger.Nop())
	r19SeedSocketConfig(t, &config.Config{})
	name := "c04-conflict-svc"
	registry.ServiceRegistry().Unregister(name)
	t.Cleanup(func() { registry.ServiceRegistry().Unregister(name) })

	cfg := config.ServiceConfig{Name: name, Addr: fmt.Sprintf("127.0.0.1:%d", port)}
	err = updateServices(updateServicesRequest{Data: []config.ServiceConfig{cfg}})
	if err == nil || !strings.Contains(err.Error(), "port_conflict") {
		t.Fatalf("bind conflict err = %v, want port_conflict marker", err)
	}
	if !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("bind conflict err must keep the underlying bind detail, got %v", err)
	}
}

// A-NEW: the pause must kill even when the socket is only in a
// draining state (CLOSE_WAIT here — no ESTABLISHED row at all). The
// old ESTABLISHED-only scan skipped the kill for such a socket and
// the bulk download completed. The scan is driven by a /proc/net
// fixture (procNetDir seam): the sandbox kernel rejects SOCK_DESTROY
// (ss -K returns rc 0 with "RTNETLINK answers: Invalid argument"
// and the socket survives), so a real ss cannot kill here — the
// fake ss simulates the kill landing by clearing the fixture rows,
// and the real kill is proven live on the test machine.
func TestC04PauseServicesKillsViaSS(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "ss.log")
	procDir := filepath.Join(dir, "procnet")
	if err := os.Mkdir(procDir, 0755); err != nil {
		t.Fatal(err)
	}
	const c04CloseWaitTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:7293 00000000:0000 08 00000000:001786C1 00:00000000 00000000     0        0 12347 1 0000000000000000 100 0 0 10 0
`
	if err := os.WriteFile(filepath.Join(procDir, "tcp"), []byte(c04CloseWaitTCP), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "tcp6"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	oldProcNetDir := procNetDir
	procNetDir = procDir
	t.Cleanup(func() { procNetDir = oldProcNetDir })

	// Fake ss: log args, then simulate the kill landing by clearing
	// the fixture rows (shell builtins only — PATH is restricted).
	c04WriteFakeBin(t, dir, "ss", "#!/bin/sh\necho \"$@\" >> \""+logFile+"\"\n: > \""+filepath.Join(procDir, "tcp")+"\"\n: > \""+filepath.Join(procDir, "tcp6")+"\"\nexit 0\n")
	name := "c04-pause-ss"
	c04RegisterFixedAddrService(t, name, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 29331})
	// Restrict PATH only for the pause itself.
	t.Setenv("PATH", dir)

	if err := pauseServices(pauseServicesRequest{Services: []string{name}}); err != nil {
		t.Fatalf("pause with fake ss: %v", err)
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ss was not invoked during pause: %v", err)
	}
	if !strings.Contains(string(b), "sport = :29331") {
		t.Fatalf("ss args = %q, want sport filter for port 29331", string(b))
	}
	if !strings.Contains(string(b), "dport = :29331") {
		t.Fatalf("ss args = %q, want dport filter for port 29331 (A-NEW: the peer side holds the buffered data)", string(b))
	}
	c04AssertPaused(t, name)
}

// A-NEW: a kill that reports success but leaves the socket alive
// must fail the pause loud (the old code trusted ss's exit code and
// the download drained). The fake ss exits 0 without touching the
// fixture, so the post-kill re-scan still sees the socket.
func TestC04PauseServicesKillNotLandingIsError(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "ss.log")
	procDir := filepath.Join(dir, "procnet")
	if err := os.Mkdir(procDir, 0755); err != nil {
		t.Fatal(err)
	}
	const c04EstablishedTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:7293 00000000:0000 01 00000000:00000000 00:00000000 00000000     0        0 12347 1 0000000000000000 100 0 0 10 0
`
	if err := os.WriteFile(filepath.Join(procDir, "tcp"), []byte(c04EstablishedTCP), 0600); err != nil {
		t.Fatal(err)
	}
	oldProcNetDir := procNetDir
	procNetDir = procDir
	t.Cleanup(func() { procNetDir = oldProcNetDir })

	c04WriteFakeBin(t, dir, "ss", "#!/bin/sh\necho \"$@\" >> \""+logFile+"\"\nexit 0\n")
	name := "c04-pause-noland"
	c04RegisterFixedAddrService(t, name, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 29331})
	t.Setenv("PATH", dir)

	err := pauseServices(pauseServicesRequest{Services: []string{name}})
	if err == nil || !strings.Contains(err.Error(), "still alive") {
		t.Fatalf("pause with a kill that does not land must fail loud, got %v", err)
	}
}

func TestC04PauseServicesKillFailureIsError(t *testing.T) {
	dir := t.TempDir() // empty: no ss, no tcpkill on PATH
	ln, _, _ := c04EstablishedListener(t)
	name := "c04-pause-nokill"
	c04RegisterFixedAddrService(t, name, ln.Addr())
	t.Setenv("PATH", dir)

	err := pauseServices(pauseServicesRequest{Services: []string{name}})
	if err == nil || !strings.Contains(err.Error(), "close connections") {
		t.Fatalf("pause with no killers must fail loud, got %v", err)
	}
}

// No established connections: the kill is skipped entirely, so a pause
// succeeds even with no killer on PATH (R19 :0-service shape).
func TestC04PauseServicesSkipsKillWithoutConnections(t *testing.T) {
	dir := t.TempDir() // empty: no ss, no tcpkill on PATH
	name := "c04-pause-noconn"
	port := c04FreePort(t)
	c04CreateRealService(t, name, port)
	t.Setenv("PATH", dir)

	if err := pauseServices(pauseServicesRequest{Services: []string{name}}); err != nil {
		t.Fatalf("pause without connections must succeed even with no killers: %v", err)
	}
	c04AssertPaused(t, name)
}
