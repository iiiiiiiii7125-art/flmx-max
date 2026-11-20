package relay

import (
	"bytes"
	"errors"
	"net"
	"testing"
)

// Defect: tcpConn.Write used to report n = len(b) unconditionally
// ("force byte length consistent"), whatever the underlying write
// actually moved. Transport's progress tracker credits a session
// byte-for-byte from the reported count, so a write that blocked
// to its deadline on a dead client and moved 0 bytes still
// refreshed the session's progress stamp — and io.CopyBuffer was
// told the chunk had been fully written, silently dropping it.
// These tests pin Write to the underlying conn's real result.

var errWriteFake = errors.New("fake write failure")

// scriptedWriteConn is a net.Conn whose Write follows a script;
// it records every byte it accepted.
type scriptedWriteConn struct {
	net.Conn
	writeFn func(p []byte) (int, error)
	written bytes.Buffer
}

func (c *scriptedWriteConn) Write(p []byte) (int, error) { return c.writeFn(p) }

func TestTCPConnWriteReportsUnderlyingPartialCount(t *testing.T) {
	fake := &scriptedWriteConn{writeFn: func(p []byte) (int, error) {
		return 2, nil // short write, no error
	}}
	c := &tcpConn{Conn: fake}
	n, err := c.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write error = %v, want nil", err)
	}
	if n != 2 {
		t.Fatalf("Write n = %d, want the underlying 2 (forged count reports len(b))", n)
	}
}

func TestTCPConnWriteBlockedReportsZero(t *testing.T) {
	fake := &scriptedWriteConn{writeFn: func(p []byte) (int, error) {
		return 0, errWriteFake // deadline-blocked on a dead client
	}}
	c := &tcpConn{Conn: fake}
	n, err := c.Write([]byte("hello"))
	if !errors.Is(err, errWriteFake) {
		t.Fatalf("Write error = %v, want the underlying error propagated", err)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0: no byte moved, so no progress may be credited", n)
	}
}

func TestTCPConnWriteFullSuccess(t *testing.T) {
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

func TestTCPConnWriteHeaderFlushCountsPayloadOnly(t *testing.T) {
	fake := &scriptedWriteConn{}
	fake.writeFn = func(p []byte) (int, error) {
		return fake.written.Write(p)
	}
	c := &tcpConn{Conn: fake}
	c.wbuf.WriteString("HDR") // cached response header
	n, err := c.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write = (%d, %v), want (5, nil): header bytes are not payload progress", n, err)
	}
	if got := fake.written.String(); got != "HDRhello" {
		t.Fatalf("wire bytes = %q, want header followed by payload", got)
	}
}

func TestTCPConnWriteHeaderPartialFlush(t *testing.T) {
	budget := 4 // wire moves 4 of the 8 buffered bytes, then fails
	fake := &scriptedWriteConn{}
	fake.writeFn = func(p []byte) (int, error) {
		if budget <= 0 {
			return 0, errWriteFake
		}
		k := budget
		if k > len(p) {
			k = len(p)
		}
		budget -= k
		_, _ = fake.written.Write(p[:k])
		if k < len(p) {
			return k, errWriteFake // partial write with a failure, as a dying conn does
		}
		return k, nil
	}
	c := &tcpConn{Conn: fake}
	c.wbuf.WriteString("HDR")
	n, err := c.Write([]byte("hello"))
	if !errors.Is(err, errWriteFake) {
		t.Fatalf("Write error = %v, want the flush failure propagated", err)
	}
	// 4 bytes flushed: 3 header + 1 payload.
	if n != 1 {
		t.Fatalf("Write n = %d, want 1 (only the payload bytes that actually crossed)", n)
	}
	if got := fake.written.String(); got != "HDRh" {
		t.Fatalf("wire bytes = %q, want %q", got, "HDRh")
	}
}

// OPEN-4: this udpConn kept the same forged count the tcpConn
// twin was fixed for (pre-set n = len(b), returned whatever the
// flush did). A failed header flush or a failed length-prefix
// write must report 0, and a successful flush must count only
// the frame payload — the 2-byte prefix and the cached response
// header are not payload.

func TestUDPConnWriteHeaderFlushFailureReportsZero(t *testing.T) {
	fake := &scriptedWriteConn{writeFn: func(p []byte) (int, error) {
		return 0, errWriteFake
	}}
	c := &udpConn{Conn: fake}
	c.wbuf.WriteString("HDR")
	n, err := c.Write([]byte("hello"))
	if !errors.Is(err, errWriteFake) {
		t.Fatalf("Write error = %v, want the underlying error propagated", err)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0: header flush failed, no frame byte crossed", n)
	}
}

func TestUDPConnWriteHeaderFlushCountsPayloadOnly(t *testing.T) {
	fake := &scriptedWriteConn{}
	fake.writeFn = func(p []byte) (int, error) {
		return fake.written.Write(p)
	}
	c := &udpConn{Conn: fake}
	c.wbuf.WriteString("HDR")
	n, err := c.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write = (%d, %v), want (5, nil): only payload bytes count", n, err)
	}
	// Wire form: cached header + 2-byte length prefix + payload.
	if got := fake.written.Bytes(); !bytes.Equal(got, []byte{'H', 'D', 'R', 0, 5, 'h', 'e', 'l', 'l', 'o'}) {
		t.Fatalf("wire bytes = %q, want header + length-prefixed hello", got)
	}
}

func TestUDPConnWriteLengthPrefixFailureReportsZero(t *testing.T) {
	fake := &scriptedWriteConn{writeFn: func(p []byte) (int, error) {
		return 0, errWriteFake
	}}
	c := &udpConn{Conn: fake}
	n, err := c.Write([]byte("hello"))
	if !errors.Is(err, errWriteFake) {
		t.Fatalf("Write error = %v, want the underlying error propagated", err)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0: length prefix never crossed, the frame did not go out", n)
	}
}
