package udp

import (
	"context"
	"fmt"
	"net"

	"github.com/go-gost/core/dialer"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	"github.com/go-gost/x/registry"
)

func init() {
	registry.DialerRegistry().Register("udp", NewDialer)
}

type udpDialer struct {
	md     metadata
	logger logger.Logger
}

func NewDialer(opts ...dialer.Option) dialer.Dialer {
	options := &dialer.Options{}
	for _, opt := range opts {
		opt(options)
	}

	return &udpDialer{
		logger: options.Logger,
	}
}

func (d *udpDialer) Init(md md.Metadata) (err error) {
	return d.parseMetadata(md)
}

func (d *udpDialer) Dial(ctx context.Context, addr string, opts ...dialer.DialOption) (net.Conn, error) {
	var options dialer.DialOptions
	for _, opt := range opts {
		opt(&options)
	}

	c, err := options.Dialer.Dial(ctx, "udp", addr)
	if err != nil {
		return nil, err
	}
	// The underlying dialer may return a non-*net.UDPConn conn: with a
	// multiplex front node in the route, chain/route.go returns a
	// *relay.udpConn or a *packetConn instead. An unchecked assertion
	// panics here and kills the whole chain leg on every UDP dial
	// (R18-D4-02); fail closed with the conn closed instead.
	uc, ok := c.(*net.UDPConn)
	if !ok {
		c.Close()
		return nil, fmt.Errorf("udp dialer: unexpected conn type %T", c)
	}
	return &conn{
		UDPConn: uc,
	}, nil
}
