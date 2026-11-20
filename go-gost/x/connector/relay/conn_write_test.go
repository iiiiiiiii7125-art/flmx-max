package relay

import (
	"bytes"
	"errors"
	"net"
	"testing"

	"github.com/go-gost/core/observer/stats"
	xstats "github.com/go-gost/x/observer/stats"
	stats_wrapper "github.com/go-gost/x/observer/stats/wrapper"
)

// OPEN-4: connector-side tcpConn/udpConn.Write used to report
// n = len(b) unconditionally ("force byte length consistent"),
// whatever the underlying write actually moved — the same defect
// the handler-side tcpConn already had fixed (see
// handler/relay/conn_write_test.go). A failed flush still
// credited Transport's progress tracker with a full packet (and
// io.CopyBuffer dropped bytes it believed written), and the stats
// wrapper — which bills whatever n the conn reports — counted the
// whole packet as output bytes. These tests pin Write to the
// underlying conn's real result, including the stats composition.

var errRelayWriteFake = errors.New("fake write failure")

// scriptedWriteConn is a net.Conn whose Write follows a script;
// it records every byte it accepted.
type scriptedWriteConn struct {
	net.Conn
	writeFn func(p []byte) (int, error)
	written bytes.Buffer
}

func (c *scriptedWriteConn) Write(p []byte) (int, error) { return c.writeFn(p) }

func TestConnectorTCPConnWriteBlockedReportsZero(t *testing.T) {
	fake := &scriptedWriteConn{writeFn: func(p []byte) (int, error) {
		return 0, errRelayWriteFake // deadline-blocked on a dead peer
	}}
	c := &tcpConn{Conn: fake}
	n, err := c.Write([]byte("hello"))
	if !errors.Is(err, errRelayWriteFake) {
		t.Fatalf("Write error = %v, want the underlying error propagated", err)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0: no byte moved, so no progress may be credited", n)
	}
}

func TestConnectorTCPConnWriteFullSuccess(t *testing.T) {
	fake := &scriptedWriteConn{}
	fake.writeFn = func(p []byte) (int, error) {
		return fake.written.Write(p)
	}
	c := &tcpConn{Conn: fake}
	n, err := c.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write = (%d, %v), want (5, nil)", n, err)
	}
	if got := fake.written.String(); got != "hello" {
		t.Fatalf("wire bytes = %q, want %q", got, "hello")
	}
}

func TestConnectorTCPConnWriteHeaderFlushCountsPayloadOnly(t *testing.T) {
	fake := &scriptedWriteConn{}
	fake.writeFn = func(p []byte) (int, error) {
		return fake.written.Write(p)
	}
	c := &tcpConn{Conn: fake, wbuf: bytes.NewBufferString("HDR")}
	n, err := c.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write = (%d, %v), want (5, nil): only payload bytes count", n, err)
	}
	if got := fake.written.String(); got != "HDRhello" {
		t.Fatalf("wire bytes = %q, want %q", got, "HDRhello")
	}
}

func TestConnectorTCPConnWriteHeaderFlushFailureReportsZero(t *testing.T) {
	fake := &scriptedWriteConn{writeFn: func(p []byte) (int, error) {
		return 0, errRelayWriteFake
	}}
	c := &tcpConn{Conn: fake, wbuf: bytes.NewBufferString("HDR")}
	n, err := c.Write([]byte("hello"))
	if !errors.Is(err, errRelayWriteFake) {
		t.Fatalf("Write error = %v, want the underlying error propagated", err)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0: header flush failed, no payload byte crossed", n)
	}
}

func TestConnectorUDPConnWriteHeaderFlushFailureReportsZero(t *testing.T) {
	fake := &scriptedWriteConn{writeFn: func(p []byte) (int, error) {
		return 0, errRelayWriteFake
	}}
	c := &udpConn{Conn: fake, wbuf: bytes.NewBufferString("HDR")}
	n, err := c.Write([]byte("hello"))
	if !errors.Is(err, errRelayWriteFake) {
		t.Fatalf("Write error = %v, want the underlying error propagated", err)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0: header flush failed, no frame byte crossed", n)
	}
}

func TestConnectorUDPConnWriteLengthPrefixFailureReportsZero(t *testing.T) {
	fake := &scriptedWriteConn{writeFn: func(p []byte) (int, error) {
		return 0, errRelayWriteFake
	}}
	c := &udpConn{Conn: fake}
	n, err := c.Write([]byte("hello"))
	if !errors.Is(err, errRelayWriteFake) {
		t.Fatalf("Write error = %v, want the underlying error propagated", err)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0: length prefix never crossed, the frame did not go out", n)
	}
}

func TestConnectorUDPConnWriteFullSuccess(t *testing.T) {
	fake := &scriptedWriteConn{}
	fake.writeFn = func(p []byte) (int, error) {
		return fake.written.Write(p)
	}
	c := &udpConn{Conn: fake}
	n, err := c.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write = (%d, %v), want (5, nil)", n, err)
	}
	// Wire form: 2-byte big-endian length prefix + payload.
	if got := fake.written.Bytes(); !bytes.Equal(got, []byte{0, 5, 'h', 'e', 'l', 'l', 'o'}) {
		t.Fatalf("wire bytes = %q, want length-prefixed hello", got)
	}
}

// TestConnectorTCPConnWriteFailureBillsZero is the OPEN-4
// composition check: the stats wrapper bills whatever n the conn
// reports, so a relay conn that forges n = len(b) on a failed
// write makes the wrapper count a whole packet as output bytes.
// With the conn fixed, a failed write bills zero.
func TestConnectorTCPConnWriteFailureBillsZero(t *testing.T) {
	fake := &scriptedWriteConn{writeFn: func(p []byte) (int, error) {
		return 0, errRelayWriteFake
	}}
	pStats := xstats.Stats{}
	wrapped := stats_wrapper.WrapConn(&tcpConn{Conn: fake}, &pStats)
	n, err := wrapped.Write([]byte("hello"))
	if !errors.Is(err, errRelayWriteFake) {
		t.Fatalf("Write error = %v, want the underlying error propagated", err)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0", n)
	}
	if got := pStats.Get(stats.KindOutputBytes); got != 0 {
		t.Fatalf("billed output bytes = %d, want 0: a failed relay write must not count as sent", got)
	}
}
