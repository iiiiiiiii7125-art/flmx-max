package keepalive

import (
	"context"
	"net"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// sockOpts is the kernel-level keepalive state read back from a
// live socket: the only evidence that counts for "the parameters
// actually reached the socket".
type sockOpts struct {
	enabled  int
	idle     int
	interval int
	count    int
}

func readSockOpts(t *testing.T, c net.Conn) sockOpts {
	t.Helper()
	tc, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatalf("conn is %T, want *net.TCPConn", c)
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var out sockOpts
	var gerr error
	err = rc.Control(func(fd uintptr) {
		if out.enabled, gerr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE); gerr != nil {
			return
		}
		if out.idle, gerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPIDLE); gerr != nil {
			return
		}
		if out.interval, gerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPINTVL); gerr != nil {
			return
		}
		out.count, gerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPCNT)
	})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if gerr != nil {
		t.Fatalf("Getsockopt: %v", gerr)
	}
	return out
}

// loopbackPair builds a connected pair through a ListenConfig and
// a Dialer that both went through the package's Apply helpers.
func loopbackPair(t *testing.T) (accepted, dialed net.Conn) {
	t.Helper()

	lc := &net.ListenConfig{}
	ApplyListenConfig(lc)
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		d := &net.Dialer{}
		ApplyDialer(d)
		c, err := d.Dial("tcp", ln.Addr().String())
		ch <- res{c, err}
	}()
	accepted, err = ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatalf("dial: %v", r.err)
	}
	dialed = r.c
	t.Cleanup(func() { accepted.Close(); dialed.Close() })
	return accepted, dialed
}

func TestSocketReadbackCustomParams(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockopt readback is Linux-specific")
	}
	defer restoreDefaults()
	// Deliberately non-round values so a fall back to any default
	// (stdlib 15/15/9, sysctl 7200/75/9) cannot fake a pass.
	Set(42*time.Second, 7*time.Second, 4)

	accepted, dialed := loopbackPair(t)
	for label, c := range map[string]net.Conn{"accept": accepted, "dial": dialed} {
		got := readSockOpts(t, c)
		want := sockOpts{enabled: 1, idle: 42, interval: 7, count: 4}
		if got != want {
			t.Fatalf("%s side sockopts = %+v, want %+v", label, got, want)
		}
	}
}

func TestSocketReadbackDisabled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockopt readback is Linux-specific")
	}
	defer restoreDefaults()
	SetEnabled(false)

	accepted, dialed := loopbackPair(t)
	for label, c := range map[string]net.Conn{"accept": accepted, "dial": dialed} {
		got := readSockOpts(t, c)
		if got.enabled != 0 {
			t.Fatalf("%s side SO_KEEPALIVE = %d, want 0 (probing off)", label, got.enabled)
		}
	}
}
