package wrapper

import (
	"errors"
	"net"
	"syscall"

	limiter "github.com/go-gost/core/limiter/conn"
	"github.com/go-gost/core/metadata"
)

var (
	errUnsupport = errors.New("unsupported operation")
)

// serverConn is a server side Conn with metrics supported.
type serverConn struct {
	net.Conn
	limiter limiter.Limiter
}

func WrapConn(limiter limiter.Limiter, c net.Conn) net.Conn {
	if limiter == nil {
		return c
	}
	return &serverConn{
		Conn:    c,
		limiter: limiter,
	}
}

func (c *serverConn) SyscallConn() (rc syscall.RawConn, err error) {
	if sc, ok := c.Conn.(syscall.Conn); ok {
		rc, err = sc.SyscallConn()
		return
	}
	err = errUnsupport
	return
}

// Raw returns the underlying connection (R18-C5-01): it lets the
// service layer reach the proxyproto.Conn underneath the wrapper
// chain to record the raw peer (PeerIP) for the connection log,
// without the wrappers having to know about PROXY at all.
func (c *serverConn) Raw() net.Conn {
	if r, ok := c.Conn.(interface{ Raw() net.Conn }); ok {
		return r.Raw()
	}
	return c.Conn
}

func (c *serverConn) Close() error {
	c.limiter.Allow(-1)
	return c.Conn.Close()
}

func (c *serverConn) Metadata() metadata.Metadata {
	if md, ok := c.Conn.(metadata.Metadatable); ok {
		return md.Metadata()
	}
	return nil
}
