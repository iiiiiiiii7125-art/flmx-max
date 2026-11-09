package net

import (
	"context"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/go-gost/x/internal/keepalive"
	"golang.org/x/sys/unix"
)

// The gost ListenConfig wrapper is the accept funnel for the bind
// paths (relay bind, socks bind/mbind). Sockets accepted through
// it must carry the keepalive policy even though the wrapper's
// callers never mention keepalive.
func TestListenConfigFunnelStampsKeepalive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockopt readback is Linux-specific")
	}
	keepalive.Set(35*time.Second, 8*time.Second, 3)
	defer keepalive.Set(0, 0, 0)

	lc := &ListenConfig{}
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			defer c.Close()
			time.Sleep(50 * time.Millisecond)
		}
	}()
	accepted, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer accepted.Close()

	tc := accepted.(*net.TCPConn)
	rc, err := tc.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var ka, idle, intvl, cnt int
	err = rc.Control(func(fd uintptr) {
		ka, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE)
		idle, _ = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPIDLE)
		intvl, _ = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPINTVL)
		cnt, _ = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPCNT)
	})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if ka != 1 || idle != 35 || intvl != 8 || cnt != 3 {
		t.Fatalf("accepted sockopts = KA %d IDLE %d INTVL %d CNT %d, want 1/35/8/3", ka, idle, intvl, cnt)
	}
}

// Packet (UDP) listens through the same wrapper have no keepalive
// concept; stamping the policy must not break them.
func TestListenConfigPacketUnaffected(t *testing.T) {
	lc := &ListenConfig{}
	pc, err := lc.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen packet: %v", err)
	}
	pc.Close()
}
