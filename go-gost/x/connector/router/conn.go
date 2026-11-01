package tunnel

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"

	"github.com/go-gost/core/common/bufpool"
	"github.com/go-gost/relay"
	xrelay "github.com/go-gost/x/internal/util/relay"
)

type packetConn struct {
	net.Conn
	rbuf []byte // unread remainder of the current frame
}

func (c *packetConn) Read(b []byte) (n int, err error) {
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
	n = copy(b, buf[:dlen])
	c.rbuf = append(c.rbuf, buf[n:dlen]...)

	return
}

func (c *packetConn) Write(b []byte) (n int, err error) {
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

func readResponse(r io.Reader) (err error) {
	resp := relay.Response{}
	_, err = resp.ReadFrom(r)
	if err != nil {
		return
	}

	if resp.Version != relay.Version1 {
		err = fmt.Errorf("router connector: %w", relay.ErrBadVersion)
		return
	}

	if resp.Status != relay.StatusOK {
		err = fmt.Errorf("%d %s", resp.Status, xrelay.StatusText(resp.Status))
		return
	}
	return nil
}
