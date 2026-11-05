package http

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-gost/core/auth"
	"github.com/go-gost/core/handler"
	xlogger "github.com/go-gost/x/logger"
	mdx "github.com/go-gost/x/metadata"
)

// F1-02 regression: the probe-resistance "web" decoy is fetched on
// unauthenticated requests. The fetch must be time-bounded — a
// hanging decoy must not pin the handler goroutine forever (it did:
// http.Get on http.DefaultClient has no timeout).

type rejectAuther struct{}

// bufferConn is a no-op net.Conn that discards writes; authenticate
// writes the final response to it.
type bufferConn struct{ bytes.Buffer }

func (c *bufferConn) Close() error                       { return nil }
func (c *bufferConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (c *bufferConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (c *bufferConn) SetDeadline(t time.Time) error      { return nil }
func (c *bufferConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *bufferConn) SetWriteDeadline(t time.Time) error { return nil }
func (c *bufferConn) Read(b []byte) (int, error)         { return 0, io.EOF }

func (rejectAuther) Authenticate(ctx context.Context, user, password string, opts ...auth.Option) (string, bool) {
	return "", false
}

func TestProbeDecoyFetchIsTimeBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A decoy that never answers.
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer srv.CloseClientConnections()
	defer srv.Close()

	h := NewHandler(
		handler.AutherOption(rejectAuther{}),
		handler.LoggerOption(xlogger.Nop()),
	)
	if err := h.Init(mdx.NewMetadata(map[string]any{
		"probeResist": "web:" + srv.URL,
	})); err != nil {
		t.Fatalf("Init: %v", err)
	}

	req := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Host: "knock-mismatch.example"},
		Header: http.Header{},
	}
	resp := &http.Response{Header: http.Header{}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.(*httpHandler).authenticate(context.Background(), &bufferConn{}, req, resp, xlogger.Nop())
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("decoy fetch is not time-bounded: authenticate still blocked after 15s against a hanging decoy")
	}
	// The fetch failed (timeout), so the response stays at the
	// default probe-resistance status code.
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("resp.StatusCode = %d, want %d (default probe response after a failed decoy fetch)", resp.StatusCode, http.StatusServiceUnavailable)
	}
}

// The shared decoy client itself: 5s timeout, redirect chain capped
// at 3 (the standard client would follow 10 with no timeout at all).
func TestProbeDecoyClientBounds(t *testing.T) {
	if probeDecoyClient.Timeout != 5*time.Second {
		t.Fatalf("probeDecoyClient.Timeout = %v, want 5s", probeDecoyClient.Timeout)
	}
	req := &http.Request{}
	if err := probeDecoyClient.CheckRedirect(req, make([]*http.Request, 2)); err != nil {
		t.Fatalf("CheckRedirect with 2 via requests: %v", err)
	}
	if err := probeDecoyClient.CheckRedirect(req, make([]*http.Request, 3)); err == nil {
		t.Fatal("CheckRedirect with 3 via requests returned nil, want a cap error")
	}
}
