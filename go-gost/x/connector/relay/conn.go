package relay

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"

	"github.com/go-gost/core/common/bufpool"
	mdata "github.com/go-gost/core/metadata"
	"github.com/go-gost/relay"
	xrelay "github.com/go-gost/x/internal/util/relay"
)

type tcpConn struct {
	net.Conn
	wbuf *bytes.Buffer
	once sync.Once
	mu   sync.Mutex
}

func (c *tcpConn) Read(b []byte) (n int, err error) {
	c.once.Do(func() {
		if c.wbuf != nil {
			err = c.drainResponse()
		}
	})

	if err != nil {
		return
	}
	return c.Conn.Read(b)
}

// drainResponse completes the delayed (noDelay=false) handshake
// from the read side: the cached request header must be put on the
// wire before the response is drained. Previously Read drained a
// response to a request that had never been sent — the header left
// the buffer only when the application wrote its first payload
// byte, so a server-first peer (one that speaks first and waits
// for no client byte: game login servers, SSH/SMTP banners) never
// saw the request, the far handler never dialled the target, and
// the session sat until the far side's request-header read
// deadline killed the hop at exactly 15s (F-212-B). Flushing here
// preserves the write-side piggyback for client-first traffic
// (Write still wins the race whenever a byte is written first)
// while letting a read-first consumer drive the handshake.
func (c *tcpConn) drainResponse() error {
	if err := c.flushHeader(); err != nil {
		return err
	}
	return readResponse(c.Conn)
}

// flushHeader writes the cached request header to the wire. It
// takes the same mutex as Write so the header can never be
// reordered against, or interleaved with, a first payload write:
// whichever side runs first emits header-then-payload in order,
// the other observes an empty buffer.
func (c *tcpConn) flushHeader() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wbuf != nil && c.wbuf.Len() > 0 {
		if _, err := c.Conn.Write(c.wbuf.Bytes()); err != nil {
			return err
		}
		c.wbuf.Reset()
	}
	return nil
}

func (c *tcpConn) Write(b []byte) (n int, err error) {
	n = len(b) // force byte length consistent

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.wbuf != nil && c.wbuf.Len() > 0 {
		c.wbuf.Write(b) // append the data to the cached header
		_, err = c.Conn.Write(c.wbuf.Bytes())
		c.wbuf.Reset()
		return
	}
	_, err = c.Conn.Write(b)
	return
}

var errHalfCloseNotSupported = errors.New("half-close not supported by the underlying connection")

// CloseWrite forwards a half-close to the wrapped conn when it
// supports one. This wrapper heads the outbound relay stack, so
// without the forwarding Transport's half-close propagation
// silently no-ops on every relay session: the peer never learns
// the stream ended, both hops park on Read, and the FIN'd leg
// lingers in CLOSE-WAIT (package K census).
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
	wbuf *bytes.Buffer
	once sync.Once
	mu   sync.Mutex
	rbuf []byte // unread remainder of the current frame
}

// Read returns the payload of one B-frame per call. If the caller's
// buffer is smaller than the frame, the unread remainder is retained
// in rbuf for the next Read instead of being discarded: this conn is a
// byte-stream face for the frame layer (U1-003), and silently dropping
// the tail of a frame would desynchronise the stream.
func (c *udpConn) Read(b []byte) (n int, err error) {
	c.once.Do(func() {
		if c.wbuf != nil {
			err = c.drainResponse()
		}
	})
	if err != nil {
		return
	}

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

	buf := bufpool.Get(dlen)
	defer bufpool.Put(buf)
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

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.wbuf != nil && c.wbuf.Len() > 0 {
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

// drainResponse is the udpConn counterpart of tcpConn's: the
// cached request header (a bare, unframed relay request — data
// frames are the length-prefixed part) must reach the wire before
// the response is drained, or a read-first consumer waits for a
// response to a request that was never sent (F-212-B). UDP
// sessions are normally write-first, so this is structural parity
// with the TCP face rather than a separately observed stall.
func (c *udpConn) drainResponse() error {
	if err := c.flushHeader(); err != nil {
		return err
	}
	return readResponse(c.Conn)
}

func (c *udpConn) flushHeader() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wbuf != nil && c.wbuf.Len() > 0 {
		if _, err := c.Conn.Write(c.wbuf.Bytes()); err != nil {
			return err
		}
		c.wbuf.Reset()
	}
	return nil
}

func readResponse(r io.Reader) (err error) {
	resp := relay.Response{}
	_, err = resp.ReadFrom(r)
	if err != nil {
		return
	}

	if resp.Version != relay.Version1 {
		err = fmt.Errorf("relay connector: %w", relay.ErrBadVersion)
		return
	}

	if resp.Status != relay.StatusOK {
		err = fmt.Errorf("%d %s", resp.Status, xrelay.StatusText(resp.Status))
		return
	}
	return nil
}

type bindConn struct {
	net.Conn
	localAddr  net.Addr
	remoteAddr net.Addr
	md         mdata.Metadata
}

func (c *bindConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *bindConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

// Metadata implements metadata.Metadatable interface.
func (c *bindConn) Metadata() mdata.Metadata {
	return c.md
}

type bindUDPConn struct {
	net.Conn
	localAddr  net.Addr
	remoteAddr net.Addr
	md         mdata.Metadata
	rbuf       []byte // unread remainder of the current frame
}

func (c *bindUDPConn) Read(b []byte) (n int, err error) {
	if len(c.rbuf) > 0 {
		n = copy(b, c.rbuf)
		c.rbuf = c.rbuf[n:]
		return n, nil
	}

	// 2-byte data length header
	var bh [2]byte
	_, err = io.ReadFull(c.Conn, bh[:])
	if err != nil {
		return
	}

	dlen := int(binary.BigEndian.Uint16(bh[:]))
	if len(b) >= dlen {
		n, err = io.ReadFull(c.Conn, b[:dlen])
		return
	}

	buf := bufpool.Get(dlen)
	defer bufpool.Put(buf)

	_, err = io.ReadFull(c.Conn, buf)
	if err != nil {
		return
	}
	n = copy(b, buf)
	c.rbuf = append(c.rbuf, buf[n:dlen]...)

	return
}

func (c *bindUDPConn) Write(b []byte) (n int, err error) {
	if len(b) > math.MaxUint16 {
		err = errors.New("write: data maximum exceeded")
		return
	}

	buf := bufpool.Get(len(b) + 2)
	defer bufpool.Put(buf)

	binary.BigEndian.PutUint16(buf[:2], uint16(len(b)))
	n = copy(buf[2:], b)

	return c.Conn.Write(buf)
}

func (c *bindUDPConn) ReadFrom(b []byte) (n int, addr net.Addr, err error) {
	addr = c.remoteAddr
	n, err = c.Read(b)
	return
}

func (c *bindUDPConn) WriteTo(b []byte, addr net.Addr) (n int, err error) {
	return c.Write(b)
}

func (c *bindUDPConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *bindUDPConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

// Metadata implements metadata.Metadatable interface.
func (c *bindUDPConn) Metadata() mdata.Metadata {
	return c.md
}
