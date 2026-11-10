package relay

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net"
)

type tcpConn struct {
	net.Conn
	wbuf bytes.Buffer
}

func (c *tcpConn) Read(b []byte) (n int, err error) {
	return c.Conn.Read(b)
}

func (c *tcpConn) Write(b []byte) (n int, err error) {
	n = len(b) // force byte length consistent
	if c.wbuf.Len() > 0 {
		c.wbuf.Write(b) // append the data to the cached header
		_, err = c.wbuf.WriteTo(c.Conn)
		return
	}
	_, err = c.Conn.Write(b)
	return
}

// flushHeader writes the cached response header to the wire now
// instead of waiting for the first payload byte in this direction
// (F-212-B mirror gate): a peer connector in eager (nodelay) mode
// drains this response inside its Connect call, before any payload
// can flow, so deferring the header to the first Write deadlocked
// establishment whenever the far side was waiting for it — the
// target's first byte (which would have flushed it) itself depends
// on payload that cannot be sent until Connect returns. Callers
// flush synchronously right after caching, before the conn enters
// Transport, so no locking against Write is needed.
func (c *tcpConn) flushHeader() error {
	if c.wbuf.Len() > 0 {
		_, err := c.wbuf.WriteTo(c.Conn)
		return err
	}
	return nil
}

var errHalfCloseNotSupported = errors.New("half-close not supported by the underlying connection")

// CloseWrite forwards a half-close to the wrapped conn when it
// supports one. This wrapper heads the relay session stack, so
// without the forwarding Transport's half-close propagation
// silently no-ops on every relay session: the peer never learns
// the stream ended, both hops park on Read, and the FIN'd leg
// lingers in CLOSE-WAIT (package K census). An unflushed header
// in wbuf means no payload ever crossed in this direction; the
// FIN alone terminates the peer's wait either way, so plain
// delegation is sufficient.
func (c *tcpConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errHalfCloseNotSupported
}

func (c *tcpConn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return errHalfCloseNotSupported
}

type udpConn struct {
	net.Conn
	wbuf bytes.Buffer
	rbuf []byte // unread remainder of the current frame
}

// Read returns the payload of one B-frame per call; a remainder that
// does not fit the caller's buffer is retained for the next Read
// instead of being discarded (stream face, see connector/relay).
func (c *udpConn) Read(b []byte) (n int, err error) {
	if len(c.rbuf) > 0 {
		n = copy(b, c.rbuf)
		c.rbuf = c.rbuf[n:]
		return n, nil
	}

	var bb [2]byte
	_, err = io.ReadFull(c.Conn, bb[:])
	if err != nil {
		return
	}

	dlen := int(binary.BigEndian.Uint16(bb[:]))
	if len(b) >= dlen {
		return io.ReadFull(c.Conn, b[:dlen])
	}
	buf := make([]byte, dlen)
	_, err = io.ReadFull(c.Conn, buf)
	if err != nil {
		return
	}
	n = copy(b, buf)
	c.rbuf = append(c.rbuf, buf[n:dlen]...)

	return
}

func (c *udpConn) Write(b []byte) (n int, err error) {
	if len(b) > math.MaxUint16 {
		err = errors.New("write: data maximum exceeded")
		return
	}

	n = len(b)
	if c.wbuf.Len() > 0 {
		var bb [2]byte
		binary.BigEndian.PutUint16(bb[:], uint16(len(b)))
		c.wbuf.Write(bb[:])
		c.wbuf.Write(b) // append the data to the cached header
		_, err = c.wbuf.WriteTo(c.Conn)
		return
	}

	var bb [2]byte
	binary.BigEndian.PutUint16(bb[:], uint16(len(b)))
	_, err = c.Conn.Write(bb[:])
	if err != nil {
		return
	}
	return c.Conn.Write(b)
}

// flushHeader writes the cached response header to the wire now
// instead of piggybacking it on the first framed datagram in this
// direction — the udpConn counterpart of tcpConn.flushHeader
// (F-212-B mirror gate). The cached header is a bare relay
// response; data frames keep their length prefixes either way.
func (c *udpConn) flushHeader() error {
	if c.wbuf.Len() > 0 {
		_, err := c.wbuf.WriteTo(c.Conn)
		return err
	}
	return nil
}
