//go:build linux

package relay

import (
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

// The relay tcpConn heads the session stack: the reaper's
// TCP_INFO probe (step: internal/net) resolves through its
// SyscallConn delegation. Prove the delegation reaches the real
// socket by reading TCP_INFO through the returned RawConn.
func TestTCPConnSyscallConnResolves(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	acceptc := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			acceptc <- c
		}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()
	server := <-acceptc
	defer server.Close()

	wrapped := &tcpConn{Conn: server}
	rc, err := wrapped.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var state uint8
	cerr := rc.Control(func(fd uintptr) {
		info, gerr := unix.GetsockoptTCPInfo(int(fd), unix.SOL_TCP, unix.TCP_INFO)
		if gerr == nil {
			state = info.State
		}
	})
	if cerr != nil {
		t.Fatalf("Control: %v", cerr)
	}
	if state != 1 { // TCP_ESTABLISHED
		t.Fatalf("TCP_INFO state = %d, want 1 (ESTABLISHED) — delegation did not reach the socket", state)
	}
}

// A wrapped conn without syscall support must surface the
// package's unsupported error, not a silent nil.
func TestTCPConnSyscallConnUnsupported(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	wrapped := &tcpConn{Conn: c1}
	if _, err := wrapped.SyscallConn(); err != errSyscallConnNotSupported {
		t.Fatalf("SyscallConn err = %v, want errSyscallConnNotSupported", err)
	}
}
