package net

import (
	"context"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"syscall"

	"github.com/go-gost/x/internal/keepalive"
	"github.com/vishvananda/netns"
)

type SetBuffer interface {
	SetReadBuffer(bytes int) error
	SetWriteBuffer(bytes int) error
}

type SyscallConn interface {
	SyscallConn() (syscall.RawConn, error)
}

type RemoteAddr interface {
	RemoteAddr() net.Addr
}

// tcpraw.TCPConn
type SetDSCP interface {
	SetDSCP(int) error
}

// IsIPv4 reports whether the host part of a listen address is a
// literal IPv4 address. Callers use it to choose tcp4/udp4 over the
// unspecified tcp/udp network. Only a literal IPv4 host qualifies: a
// hostname must stay on the unspecified network so the resolver picks
// the family — classifying hostnames as IPv4 (the previous first-byte
// heuristic) forced tcp4/udp4 and silently dropped the IPv6 face of
// hostname binds such as localhost (D3-09).
func IsIPv4(address string) bool {
	host := address
	if h, _, err := net.SplitHostPort(address); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() != nil
}

type ListenConfig struct {
	Netns string
	net.ListenConfig
}

func (lc *ListenConfig) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	if lc.Netns != "" {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		originNs, err := netns.Get()
		if err != nil {
			return nil, fmt.Errorf("netns.Get(): %v", err)
		}
		defer netns.Set(originNs)

		var ns netns.NsHandle
		if strings.HasPrefix(lc.Netns, "/") {
			ns, err = netns.GetFromPath(lc.Netns)
		} else {
			ns, err = netns.GetFromName(lc.Netns)
		}
		if err != nil {
			return nil, fmt.Errorf("netns.Get(%s): %v", lc.Netns, err)
		}
		defer ns.Close()

		if err := netns.Set(ns); err != nil {
			return nil, fmt.Errorf("netns.Set(%s): %v", lc.Netns, err)
		}
	}

	keepalive.ApplyListenConfig(&lc.ListenConfig)
	return lc.ListenConfig.Listen(ctx, network, address)
}

func (lc *ListenConfig) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	if lc.Netns != "" {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		originNs, err := netns.Get()
		if err != nil {
			return nil, fmt.Errorf("netns.Get(): %v", err)
		}
		defer netns.Set(originNs)

		var ns netns.NsHandle
		if strings.HasPrefix(lc.Netns, "/") {
			ns, err = netns.GetFromPath(lc.Netns)
		} else {
			ns, err = netns.GetFromName(lc.Netns)
		}
		if err != nil {
			return nil, fmt.Errorf("netns.Get(%s): %v", lc.Netns, err)
		}
		defer ns.Close()

		if err := netns.Set(ns); err != nil {
			return nil, fmt.Errorf("netns.Set(%s): %v", lc.Netns, err)
		}
	}
	return lc.ListenConfig.ListenPacket(ctx, network, address)
}

type readWriteConn struct {
	net.Conn
	r io.Reader
	w io.Writer
}

func NewReadWriteConn(r io.Reader, w io.Writer, c net.Conn) net.Conn {
	return &readWriteConn{
		Conn: c,
		r:    r,
		w:    w,
	}
}

func (c *readWriteConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func (c *readWriteConn) Write(p []byte) (int, error) {
	return c.w.Write(p)
}

// CloseWrite forwards a half-close to the wrapped conn when it
// supports one, so relay code (Transport) can propagate end-of-stream
// through this wrapper instead of silently dropping the signal.
func (c *readWriteConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errHalfCloseNotSupported
}

func (c *readWriteConn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return errHalfCloseNotSupported
}
