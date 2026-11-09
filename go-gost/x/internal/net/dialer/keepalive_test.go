package dialer

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/go-gost/x/internal/keepalive"
	xlogger "github.com/go-gost/x/logger"
	"golang.org/x/sys/unix"
)

func readSockOpts(t *testing.T, c net.Conn) (ka, idle, intvl, cnt int) {
	t.Helper()
	tc, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatalf("conn is %T, want *net.TCPConn", c)
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	err = rc.Control(func(fd uintptr) {
		ka, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE)
		idle, _ = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPIDLE)
		intvl, _ = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPINTVL)
		cnt, _ = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPCNT)
	})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	return
}

// The central dialer is the funnel for every outbound plain-TCP
// dial in the agent (direct targets, chain-hop leaf dials, and the
// underlays of the ws/mws/mtcp/http2/pht dialers, which all dial
// through it). Its sockets must carry the keepalive policy.
func TestCentralDialerStampsKeepalive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockopt readback is Linux-specific")
	}
	keepalive.Set(33*time.Second, 9*time.Second, 2)
	defer keepalive.Set(0, 0, 0)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	d := &Dialer{Logger: xlogger.Nop()}
	conn, err := d.Dial(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ka, idle, intvl, cnt := readSockOpts(t, conn)
	if ka != 1 || idle != 33 || intvl != 9 || cnt != 2 {
		t.Fatalf("dialed sockopts = KA %d IDLE %d INTVL %d CNT %d, want 1/33/9/2", ka, idle, intvl, cnt)
	}
}

// Non-TCP dials through the same funnel must stay untouched by the
// policy: a unix-socket dial has no keepalive concept and must keep
// working exactly as before.
func TestCentralDialerUnixUnaffected(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	d := &Dialer{Logger: xlogger.Nop()}
	conn, err := d.Dial(context.Background(), "unix", sock)
	if err != nil {
		t.Fatalf("dial unix: %v", err)
	}
	conn.Close()
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("unix socket vanished: %v", err)
	}
}
