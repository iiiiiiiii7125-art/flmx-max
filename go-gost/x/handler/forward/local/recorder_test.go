package local

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/recorder"
	"github.com/go-gost/x/logger"
	xmd "github.com/go-gost/x/metadata"
	xrecorder "github.com/go-gost/x/recorder"
)

// captureRecorder records every Record call for assertion.
type captureRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (r *captureRecorder) Record(_ context.Context, b []byte, _ ...recorder.RecordOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, append([]byte(nil), b...))
	return nil
}

func (r *captureRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

// R18-D4-03 regression: forward/local must write the handler recorder
// like every other handler (remote included). A plain TCP forward
// through this handler with a recorder configured must produce exactly
// one handler record; before the fix it produced none.
func TestLocalForwardHandlerWritesRecorder(t *testing.T) {
	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetListener.Close()

	entryListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen entry: %v", err)
	}
	defer entryListener.Close()

	cap := &captureRecorder{}
	h := NewHandler(
		handler.RouterOption(proxyProtocolTestRouter{}),
		handler.LoggerOption(logger.Nop()),
		handler.RecordersOption(recorder.RecorderObject{
			Recorder: cap,
			Record:   xrecorder.RecorderServiceHandler,
		}),
	)
	forwarder := h.(handler.Forwarder)
	forwarder.Forward(proxyProtocolTestHop{node: chain.NewNode("target", targetListener.Addr().String())})
	if err := h.Init(xmd.NewMetadata(map[string]any{})); err != nil {
		t.Fatalf("init handler: %v", err)
	}

	handleErr := make(chan error, 1)
	acceptErr := make(chan error, 1)
	go func() {
		serverConn, err := entryListener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		handleErr <- h.Handle(context.Background(), serverConn)
	}()

	clientConn, err := net.Dial("tcp", entryListener.Addr().String())
	if err != nil {
		t.Fatalf("dial entry: %v", err)
	}
	targetConn, err := targetListener.Accept()
	if err != nil {
		t.Fatalf("accept target: %v", err)
	}
	if _, err := clientConn.Write([]byte("hello")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	buf := make([]byte, 5)
	_ = targetConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := targetConn.Read(buf); err != nil {
		t.Fatalf("target read: %v", err)
	}

	_ = clientConn.Close()
	_ = targetConn.Close()
	select {
	case err := <-acceptErr:
		t.Fatalf("accept entry: %v", err)
	case <-handleErr:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after closing connections")
	}

	if n := cap.count(); n != 1 {
		t.Fatalf("recorder got %d records, want exactly 1", n)
	}
	var rec map[string]any
	if err := json.Unmarshal(cap.bodies[0], &rec); err != nil {
		t.Fatalf("record is not JSON: %v (%s)", err, cap.bodies[0])
	}
	if rec["inputBytes"] != float64(5) {
		t.Fatalf("record inputBytes = %v, want 5 (%s)", rec["inputBytes"], cap.bodies[0])
	}
	if _, ok := rec["time"]; !ok {
		t.Fatalf("record has no time field: %s", cap.bodies[0])
	}
}

// The recorder must stay a no-op (not a panic) when no recorder is
// configured — the FLMX production shape.
func TestLocalForwardHandlerWithoutRecorderIsNoop(t *testing.T) {
	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetListener.Close()

	entryListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen entry: %v", err)
	}
	defer entryListener.Close()

	h := NewHandler(
		handler.RouterOption(proxyProtocolTestRouter{}),
		handler.LoggerOption(logger.Nop()),
	)
	forwarder := h.(handler.Forwarder)
	forwarder.Forward(proxyProtocolTestHop{node: chain.NewNode("target", targetListener.Addr().String())})
	if err := h.Init(xmd.NewMetadata(map[string]any{})); err != nil {
		t.Fatalf("init handler: %v", err)
	}

	handleErr := make(chan error, 1)
	go func() {
		serverConn, err := entryListener.Accept()
		if err != nil {
			handleErr <- err
			return
		}
		handleErr <- h.Handle(context.Background(), serverConn)
	}()

	clientConn, err := net.Dial("tcp", entryListener.Addr().String())
	if err != nil {
		t.Fatalf("dial entry: %v", err)
	}
	targetConn, err := targetListener.Accept()
	if err != nil {
		t.Fatalf("accept target: %v", err)
	}
	_ = clientConn.Close()
	_ = targetConn.Close()
	select {
	case err := <-handleErr:
		if err != nil {
			t.Fatalf("Handle: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after closing connections")
	}
}
