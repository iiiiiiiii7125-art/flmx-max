//go:build linux

package service

import (
	"bufio"
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

// The service detectConn wraps accepted conns for protocol
// sniffing; the reaper's TCP_INFO probe resolves through its
// SyscallConn delegation. Prove the delegation reaches the real
// socket (mirrors the construction at the wrap site).
func TestDetectConnSyscallConnResolves(t *testing.T) {
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

	dc := &detectConn{Conn: server, reader: bufio.NewReader(server)}
	rc, err := dc.SyscallConn()
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
		t.Fatalf("TCP_INFO state = %d, want 1 (ESTABLISHED)", state)
	}
}
