package udp

import (
	"io"
	"net"
)

// MaxDatagramSize is the largest possible payload of a single UDP
// datagram (64K - 1). Read buffers on datagram paths must be at least
// this large, otherwise the tail of an oversized datagram is silently
// discarded by the kernel or by a copying reader (F-UDP-5).
const MaxDatagramSize = 65535

// DatagramReader adapts a datagram-oriented reader into a byte-stream
// reader.
//
// The underlying reader is assumed to have datagram semantics: every
// Read returns at most one datagram, and any part of that datagram that
// does not fit into the caller's buffer is discarded (this is how a raw
// *net.UDPConn behaves). Stream-style consumers that issue small reads
// (io.ReadFull of a fixed-size header, then of a length-prefixed body)
// lose the remainder of a datagram that carried more than the requested
// bytes, which desynchronises length-prefixed framing protocols such as
// the relay B-frames (F-UDP-2).
//
// DatagramReader therefore fetches every datagram whole into an
// internal buffer and serves small reads from that buffer; the next
// datagram is fetched only once the buffer is drained. The frame layer
// above it keeps its usual byte-stream assumption (U1-003: the relay
// B-frame endpoints require stream semantics); this reader is what
// provides that semantics on top of a datagram transport, without
// dropping bytes at datagram boundaries.
//
// A DatagramReader is not safe for concurrent use by multiple readers.
type DatagramReader struct {
	r       io.Reader
	buf     []byte // remainder of the current datagram
	scratch []byte // fetch buffer, allocated lazily
	err     error  // sticky error, delivered once buf is drained
}

// NewDatagramReader returns a byte-stream reader over the
// datagram-oriented reader r.
func NewDatagramReader(r io.Reader) *DatagramReader {
	return &DatagramReader{r: r}
}

func (r *DatagramReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.buf) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.scratch == nil {
			r.scratch = make([]byte, MaxDatagramSize)
		}
		n, err := r.r.Read(r.scratch)
		if n > 0 {
			// Copy out of the scratch buffer: it is reused for the
			// next fetch once this datagram is drained.
			b := make([]byte, n)
			copy(b, r.scratch[:n])
			r.buf = b
		}
		if err != nil {
			r.err = err
			if n == 0 {
				return 0, err
			}
			break
		}
		if n == 0 {
			// A (0, nil) read from the underlying reader: retry
			// rather than returning (0, nil) to the caller.
			continue
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// streamConn is a net.Conn whose Read side has byte-stream semantics
// even when the underlying connection is datagram-oriented. All other
// methods (Write, deadlines, addresses, Close) delegate unchanged.
type streamConn struct {
	net.Conn
	r *DatagramReader
}

// NewStreamConn wraps c so that small stream-style reads cannot lose
// the tail of a datagram. Use it wherever a framing layer that assumes
// a byte stream runs over a datagram transport.
func NewStreamConn(c net.Conn) net.Conn {
	return &streamConn{Conn: c, r: NewDatagramReader(c)}
}

func (c *streamConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}
