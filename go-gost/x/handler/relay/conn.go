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
