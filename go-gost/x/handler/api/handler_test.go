package api

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

func getConfig(t *testing.T, addr string, withAuth bool) (int, []byte, error) {
	t.Helper()
	req, err := http.NewRequest("GET", "http://"+addr+"/config", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if withAuth {
		req.SetBasicAuth("admin", "admin")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
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

// TestAPIHandlerPromptRequest: a prompt, authenticated request must get
// a real 200 + JSON body, and Handle must return promptly afterwards.
// Pre-fix this fails on the service path: curl gets an empty reply.
func TestAPIHandlerPromptRequest(t *testing.T) {
	h := newTestHandler(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	handleDone := serveOne(t, h, ln)

	code, body, err := getConfig(t, ln.Addr().String(), true)
	if err != nil {
		t.Fatalf("GET /config: %v (empty reply = truncated)", err)
	}
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", code, body)
	}
	if len(body) == 0 {
		t.Fatalf("empty body, want config JSON")
	}

	select {
	case herr := <-handleDone:
		if herr != nil {
			t.Fatalf("Handle returned error: %v", herr)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Handle did not return within 10s after the response")
	}
}

// TestAPIHandlerNoAuthReturns401: a request without Basic auth must get
// a 401 JSON answer, not a silently dropped connection (empty reply).
func TestAPIHandlerNoAuthReturns401(t *testing.T) {
	h := newTestHandler(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	handleDone := serveOne(t, h, ln)

	code, body, err := getConfig(t, ln.Addr().String(), false)
	if err != nil {
		t.Fatalf("GET /config without auth: %v (empty reply = silent drop)", err)
	}
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body %q", code, body)
	}
	if len(body) == 0 {
		t.Fatalf("empty body, want 401 JSON")
	}

	select {
	case herr := <-handleDone:
		if herr != nil {
			t.Fatalf("Handle returned error: %v", herr)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Handle did not return within 10s after the response")
	}
}

// TestAPIHandlerSlowClient: a client that takes >5s to send its first
// request bytes must still get its response. The old Serve+Shutdown
// pattern let Shutdown's closeIdleConns kill such a connection (a
// StateNew connection unread for over 5s is treated as idle), so the
// client got an empty reply.
func TestAPIHandlerSlowClient(t *testing.T) {
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

	fmt.Fprintf(conn, "GET /config HTTP/1.1\r\nHost: x\r\nAuthorization: Basic YWRtaW46YWRtaW4=\r\nConnection: close\r\n\r\n")
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read after 6s delay: %v (empty reply = dropped)", err)
	}
	if n == 0 {
		t.Fatalf("empty reply after 6s delay")
	}

	select {
	case herr := <-handleDone:
		if herr != nil {
			t.Fatalf("Handle returned error: %v", herr)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Handle did not return within 10s after the response")
	}
}
