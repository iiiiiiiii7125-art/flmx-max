package service

import (
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/go-gost/x/internal/keepalive"
	"golang.org/x/sys/unix"
)

// LBK census: service.Listen is the shared stamped-listener
// funnel for the management-plane listeners (x/api, x/metrics)
// and the main module's profiling listener (go-gost/program.go,
// which cannot import x/internal/keepalive directly). Read the
// kernel parameters back from a live accepted connection: the
// stamp must reach the socket, not just the ListenConfig.
// Non-round probe values (42/7/4) so a fallback to any default
// (stdlib 15/15/9, sysctl 7200/75/9) cannot fake a pass.
func TestListenStampsKeepalivePolicy(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockopt readback is Linux-specific")
	}
	prev := keepalive.Config()
	prevEnabled := keepalive.Enabled()
	keepalive.Set(42*time.Second, 7*time.Second, 4)
	keepalive.SetEnabled(true)
	defer func() {
		keepalive.Set(prev.Idle, prev.Interval, prev.Count)
		keepalive.SetEnabled(prevEnabled)
	}()

	ln, err := Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c
	}()
	dialed, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer dialed.Close()
	var acc net.Conn
	select {
	case acc = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("accept timed out")
	}
	defer acc.Close()

	tc, ok := acc.(*net.TCPConn)
	if !ok {
		t.Fatalf("accepted conn is %T, want *net.TCPConn", acc)
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var enabled, idle, interval, count int
	var gerr error
	err = rc.Control(func(fd uintptr) {
		if enabled, gerr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE); gerr != nil {
			return
		}
		if idle, gerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPIDLE); gerr != nil {
			return
		}
		if interval, gerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPINTVL); gerr != nil {
			return
		}
		count, gerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPCNT)
	})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if gerr != nil {
		t.Fatalf("Getsockopt: %v", gerr)
	}
	if enabled != 1 || idle != 42 || interval != 7 || count != 4 {
		t.Fatalf("accepted sockopts = enabled %d idle %d interval %d count %d, want 1/42/7/4", enabled, idle, interval, count)
	}
}
