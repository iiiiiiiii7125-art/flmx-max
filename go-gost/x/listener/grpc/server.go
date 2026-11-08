package grpc

import (
	"errors"
	"io"
	"net"
	"time"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/internal/util/eventlog"
	pb "github.com/go-gost/x/internal/util/grpc/proto"
	"google.golang.org/grpc/peer"
)

type server struct {
	cqueue    chan net.Conn
	localAddr net.Addr
	pb.UnimplementedGostTunelServer
	logger logger.Logger
	// queueFull aggregates the connection-queue-full discard
	// log (P1-03 family); owned by the listener's aggregator.
	queueFull *eventlog.Counter
}

func (s *server) Tunnel(srv pb.GostTunel_TunnelServer) error {
	c := &conn{
		s:          srv,
		localAddr:  s.localAddr,
		remoteAddr: &net.TCPAddr{},
		closed:     make(chan struct{}),
	}
	if p, ok := peer.FromContext(srv.Context()); ok {
		c.remoteAddr = p.Addr
	}

	select {
	case s.cqueue <- c:
	default:
		c.Close()
		s.queueFull.NoteDetail(c.remoteAddr.String())
	}

	<-c.closed

	return nil
}

type conn struct {
	s          pb.GostTunel_TunnelServer
	rb         []byte
	localAddr  net.Addr
	remoteAddr net.Addr
	closed     chan struct{}
}

func (c *conn) Read(b []byte) (n int, err error) {
	select {
	case <-c.s.Context().Done():
		err = c.s.Context().Err()
		return
	case <-c.closed:
		err = io.ErrClosedPipe
		return
	default:
	}

	if len(c.rb) == 0 {
		chunk, err := c.s.Recv()
		if err != nil {
			return 0, err
		}
		c.rb = chunk.Data
	}

	n = copy(b, c.rb)
	c.rb = c.rb[n:]
	return
}

func (c *conn) Write(b []byte) (n int, err error) {
	select {
	case <-c.s.Context().Done():
		err = c.s.Context().Err()
		return
	case <-c.closed:
		err = io.ErrClosedPipe
		return
	default:
	}

	if err = c.s.Send(&pb.Chunk{
		Data: b,
	}); err != nil {
		return
	}
	n = len(b)
	return
}

func (c *conn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}

	return nil
}

func (c *conn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *conn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *conn) SetDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "grpc", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}

func (c *conn) SetReadDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "grpc", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}

func (c *conn) SetWriteDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "grpc", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}
