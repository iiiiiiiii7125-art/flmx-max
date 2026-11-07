package http2

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	xio "github.com/go-gost/x/internal/io"
)

// P1-01: the HTTP/2 CONNECT endpoint must expose io.Closer so
// Transport's no-progress teardown can unblock the copy goroutine
// parked on the request body, and must deliberately NOT expose
// stream deadline methods (Go's HTTP/2 stream deadlines break the
// body pipe stickily; see streamReadWriter in conn.go).

var errFakeBodyClosed = errors.New("fake body closed")

type fakeBody struct {
	closed chan struct{}
	once   sync.Once
}

func newFakeBody() *fakeBody {
	return &fakeBody{closed: make(chan struct{})}
}

func (b *fakeBody) Read(p []byte) (int, error) {
	<-b.closed
	return 0, errFakeBodyClosed
}

func (b *fakeBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestStreamReadWriterCloseUnblocksRead(t *testing.T) {
	body := newFakeBody()
	var out bytes.Buffer
	// Same construction as the handler: base adapter over the
	// body, streamReadWriter re-exposing the body's Close.
	rw := &streamReadWriter{
		ReadWriter: xio.NewReadWriter(body, &out),
		body:       body,
	}

	if _, ok := any(rw).(io.Closer); !ok {
		t.Fatal("streamReadWriter does not implement io.Closer")
	}
	// Deliberate design (conn.go): no destructive stream deadlines.
	if _, ok := any(rw).(interface{ SetReadDeadline(time.Time) error }); ok {
		t.Fatal("streamReadWriter unexpectedly implements SetReadDeadline")
	}
	if _, ok := any(rw).(interface{ SetWriteDeadline(time.Time) error }); ok {
		t.Fatal("streamReadWriter unexpectedly implements SetWriteDeadline")
	}

	readc := make(chan error, 1)
	go func() {
		_, err := rw.Read(make([]byte, 16))
		readc <- err
	}()

	// Read is parked on the body; only Close can release it.
	select {
	case err := <-readc:
		t.Fatalf("Read returned before Close: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	closer, _ := any(rw).(io.Closer)
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-readc:
		if !errors.Is(err, errFakeBodyClosed) {
			t.Fatalf("Read err = %v, want the body-closed error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock the parked Read")
	}

	// Write passes through to the wrapped writer.
	if _, err := rw.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if out.String() != "ping" {
		t.Fatalf("Write delivered %q, want %q", out.String(), "ping")
	}
}
