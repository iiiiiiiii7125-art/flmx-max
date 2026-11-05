package service

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/listener"
	"github.com/go-gost/core/metadata"
	xlogger "github.com/go-gost/x/logger"
)

// D3-02: the per-connection dispatch goroutine in Serve had no
// recover(), so a panic in any handler crashed the whole agent
// process. The dispatch must recover, close just that connection,
// and keep serving.

type fakePanicListener struct {
	connCh chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (f *fakePanicListener) Init(metadata.Metadata) error { return nil }

func (f *fakePanicListener) Accept() (net.Conn, error) {
	select {
	case c := <-f.connCh:
		return c, nil
	case <-f.closed:
		return nil, net.ErrClosed
	}
}

func (f *fakePanicListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (f *fakePanicListener) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

var _ listener.Listener = (*fakePanicListener)(nil)

type panickingHandler struct{}

func (panickingHandler) Init(metadata.Metadata) error { return nil }

func (panickingHandler) Handle(context.Context, net.Conn, ...handler.HandleOption) error {
	panic("handler boom")
}

var _ handler.Handler = panickingHandler{}

func TestServeRecoversHandlerPanic(t *testing.T) {
	ln := &fakePanicListener{
		connCh: make(chan net.Conn, 1),
		closed: make(chan struct{}),
	}
	svc := &defaultService{
		name:     "svc-panic",
		listener: ln,
		handler:  panickingHandler{},
		status:   &Status{},
		options:  options{logger: xlogger.NewLogger()},
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- svc.Serve()
	}()

	server, client := net.Pipe()
	defer client.Close()
	ln.connCh <- server

	// The handler panics on this connection. With the recover
	// boundary, the dispatch closes the connection (client sees
	// EOF) and the agent (this process) lives on. Before the fix
	// the panic crashed the whole test binary.
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	if _, err := client.Read(buf); err == nil {
		t.Fatal("expected the panicked connection to be closed")
	}

	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after listener close")
	}
}
