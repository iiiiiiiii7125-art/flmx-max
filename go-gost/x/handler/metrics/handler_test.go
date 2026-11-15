package metrics

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/go-gost/core/handler"
	xlogger "github.com/go-gost/x/logger"
	xmd "github.com/go-gost/x/metadata"
)

// serveOne accepts a single connection on ln and runs h.Handle on it,
// reporting Handle's return value. It mirrors the gost service path:
// one accepted TCP connection per Handle call.
func serveOne(t *testing.T, h handler.Handler, ln net.Listener) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		done <- h.Handle(context.Background(), conn)
	}()
	return done
}

func newTestHandler(t *testing.T) handler.Handler {
	t.Helper()
	h := NewHandler(handler.LoggerOption(xlogger.Nop()))
	if err := h.Init(xmd.NewMetadata(map[string]any{})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return h
}

func getMetrics(t *testing.T, addr string) (int, []byte, error) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("http://" + addr + DefaultPath)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func waitHandleDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Handle did not return within 10s after the response")
	}
}

// TestMetricsHandlerPromptRequest: a prompt request must get a real 200
// + metrics body, and Handle must return promptly afterwards. Pre-fix
// this failed: curl got an empty reply.
func TestMetricsHandlerPromptRequest(t *testing.T) {
	h := newTestHandler(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	handleDone := serveOne(t, h, ln)

	code, body, err := getMetrics(t, ln.Addr().String())
	if err != nil {
		t.Fatalf("GET /metrics: %v (empty reply = truncated)", err)
	}
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", code, body)
	}
	if len(body) == 0 {
		t.Fatalf("empty body, want metrics exposition")
	}

	waitHandleDone(t, handleDone)
}

// TestMetricsHandlerSlowClient: a client that takes >5s to send its
// first request bytes must still get its response. The old
// Serve+Shutdown pattern let Shutdown's closeIdleConns kill such a
// connection (a StateNew connection unread for over 5s is treated as
// idle), so the client got an empty reply.
func TestMetricsHandlerSlowClient(t *testing.T) {
	h := newTestHandler(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	handleDone := serveOne(t, h, ln)

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 10*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// Hold the connection open without sending anything for 6s.
	time.Sleep(6 * time.Second)

	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", DefaultPath)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read after 6s delay: %v (empty reply = dropped)", err)
	}
	if n == 0 {
		t.Fatalf("empty reply after 6s delay")
	}

	waitHandleDone(t, handleDone)
}
