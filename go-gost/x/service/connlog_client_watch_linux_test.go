//go:build linux

package service

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	xlogger "github.com/go-gost/x/logger"
)

// tcpPair 建一对真实 TCP 连接：watcher 要走 SyscallConn 取 fd，
// net.Pipe 没有 fd，走不到观测分支。
func tcpPair(t *testing.T) (server, client net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	select {
	case server = <-accepted:
		t.Cleanup(func() { _ = server.Close() })
		return server, client
	case <-time.After(3 * time.Second):
		t.Fatal("accept timed out")
		return nil, nil
	}
}

func TestWatchClientCloseDetectsFin(t *testing.T) {
	server, client := tcpPair(t)
	var gone atomic.Int64
	done := make(chan struct{})
	defer close(done)
	go watchClientClose(server, done, &gone)

	// 空闲 300ms：客户端还在，绝不能误报离开。
	time.Sleep(300 * time.Millisecond)
	if g := gone.Load(); g != 0 {
		t.Fatalf("idle client reported gone at %d", g)
	}
	start := time.Now()
	_ = client.Close()
	deadline := time.Now().Add(2 * time.Second)
	for gone.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if gone.Load() == 0 {
		t.Fatal("FIN not observed within 2s")
	}
	if d := gone.Load() - start.UnixMilli(); d < -100 || d > 1000 {
		t.Fatalf("gone %dms after close, want within ~1s", d)
	}
}

func TestWatchClientCloseIgnoresDataThenSeesFin(t *testing.T) {
	server, client := tcpPair(t)
	var gone atomic.Int64
	done := make(chan struct{})
	defer close(done)
	go watchClientClose(server, done, &gone)

	// 客户端发了数据但不断开：有未读数据时绝不能把「有数据」
	// 误判为离开（MSG_PEEK 方案在此会把数据当存活、把 FIN
	// 挡在数据后永远看不到；POLLRDHUP 不受未读数据影响）。
	if _, err := client.Write([]byte("HELLO-FIRST-BYTE")); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if g := gone.Load(); g != 0 {
		t.Fatalf("client with unread data reported gone at %d", g)
	}
	_ = client.Close()
	deadline := time.Now().Add(2 * time.Second)
	for gone.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if gone.Load() == 0 {
		t.Fatal("FIN after data not observed within 2s")
	}
}

// TestConnLogCloseEndUsesClientDisconnect 是 R2-CONNLOG-01 / C13
// 的 service 级回归门：客户端断开后 handler 仍被 gate 钉住
// 1.5s（模拟上游停滞时的 15s 重试死线），close 事件的时长
// 必须约等于客户端存活期（~200ms），而非 Handle 全长
// （~1.7s）。旧代码（EndMs = Handle 返回时刻）在此得约
// 1700ms，必红。
func TestConnLogCloseEndUsesClientDisconnect(t *testing.T) {
	ring := freshConnlogRing(t)
	SetConnLogEnabled(true)

	ln := newQueueListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 24006})
	h := &gateHandler{release: make(chan struct{})}
	svc := NewService("fwd_entry_c13", ln, h,
		LoggerOption(xlogger.NewLogger()), ConnLogEntryOption(true, "fwd_entry_c13"))
	go func() { _ = svc.Serve() }()
	defer func() { _ = svc.Close() }()

	server, client := tcpPair(t)
	ln.conns <- server
	open := waitConnEvents(t, ring, 1)[0]
	if open.Type != ConnEventOpen {
		t.Fatalf("first event = %q, want open", open.Type)
	}

	// 客户端在 open 后约 200ms 断开；handler 继续被钉 1.5s。
	time.Sleep(200 * time.Millisecond)
	_ = client.Close()
	time.Sleep(1500 * time.Millisecond)
	close(h.release)
	waitHandled(t, h, 1)
	cls := waitConnEvents(t, ring, 1)[0]
	if cls.Type != ConnEventClose || cls.ConnID != open.ConnID {
		t.Fatalf("close event = %+v, want close paired with %s", cls, open.ConnID)
	}
	dur := cls.EndMs - cls.StartMs
	if dur < 100 || dur > 900 {
		t.Fatalf("close duration = %dms, want ~200ms (within 100..900); old code records ~1700ms", dur)
	}
}
