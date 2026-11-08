package chain

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/connector"
	"github.com/go-gost/core/dialer"
	net_dialer "github.com/go-gost/x/internal/net/dialer"
)

type Transport struct {
	dialer    dialer.Dialer
	connector connector.Connector
	options   chain.TransportOptions
}

func NewTransport(d dialer.Dialer, c connector.Connector, opts ...chain.TransportOption) *Transport {
	tr := &Transport{
		dialer:    d,
		connector: c,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&tr.options)
		}
	}

	return tr
}

func (tr *Transport) Dial(ctx context.Context, addr string) (net.Conn, error) {
	netd := &net_dialer.Dialer{
		Interface: tr.options.IfceName,
		Netns:     tr.options.Netns,
	}
	if tr.options.SockOpts != nil {
		netd.Mark = tr.options.SockOpts.Mark
	}
	if tr.options.Route != nil && len(tr.options.Route.Nodes()) > 0 {
		netd.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return tr.options.Route.Dial(ctx, network, addr)
		}
	}
	opts := []dialer.DialOption{
		dialer.HostDialOption(tr.options.Addr),
		dialer.NetDialerDialOption(netd),
	}
	return tr.dialer.Dial(ctx, addr, opts...)
}

func (tr *Transport) Handshake(ctx context.Context, conn net.Conn) (net.Conn, error) {
	var err error
	if hs, ok := tr.dialer.(dialer.Handshaker); ok {
		conn, err = hs.Handshake(ctx, conn,
			dialer.AddrHandshakeOption(tr.options.Addr))
		if err != nil {
			return nil, err
		}
	}
	if hs, ok := tr.connector.(connector.Handshaker); ok {
		return hs.Handshake(ctx, conn)
	}
	return conn, nil
}

func (tr *Transport) Connect(ctx context.Context, conn net.Conn, network, address string) (net.Conn, error) {
	netd := &net_dialer.Dialer{
		Interface: tr.options.IfceName,
		Netns:     tr.options.Netns,
	}
	if tr.options.SockOpts != nil {
		netd.Mark = tr.options.SockOpts.Mark
	}
	return tr.connector.Connect(ctx, conn, network, address,
		connector.DialerConnectOption(netd),
	)
}

func (tr *Transport) Bind(ctx context.Context, conn net.Conn, network, address string, opts ...connector.BindOption) (net.Listener, error) {
	if binder, ok := tr.connector.(connector.Binder); ok {
		return binder.Bind(ctx, conn, network, address, opts...)
	}
	return nil, connector.ErrBindUnsupported
}

func (tr *Transport) Multiplex() bool {
	if mux, ok := tr.dialer.(dialer.Multiplexer); ok {
		return mux.Multiplex()
	}
	return false
}

func (tr *Transport) Options() *chain.TransportOptions {
	if tr != nil {
		return &tr.options
	}
	return nil
}

func (tr *Transport) Copy() chain.Transporter {
	tr2 := &Transport{}
	*tr2 = *tr
	return tr2
}

// retireSkips counts the dialers and connectors that Transport.Retire
// found without a Retire method. The skip used to be silent, which is
// how the ssh/sshd dialers leaked their sessions across chain reloads
// unnoticed (F05): a component that cannot be retired keeps its old
// generation's resources alive, so the event must at least be
// observable. Components that legitimately hold no cross-generation
// state may still appear here; the count is the signal to audit, not
// a verdict.
var retireSkips atomic.Int64

// RetireSkipCount returns the number of dialer/connector Retire
// skips observed since process start.
func RetireSkipCount() int64 {
	return retireSkips.Load()
}

// Retire prevents long-lived dialer sessions owned by an obsolete chain from
// accepting new streams while allowing existing streams to drain.
func (tr *Transport) Retire() {
	if tr == nil {
		return
	}
	if tr.dialer != nil {
		if retirer, ok := tr.dialer.(interface{ Retire() }); ok {
			retirer.Retire()
		} else {
			retireSkips.Add(1)
		}
	}
	if tr.connector != nil {
		if retirer, ok := tr.connector.(interface{ Retire() }); ok {
			retirer.Retire()
		} else {
			retireSkips.Add(1)
		}
	}
}

// Close immediately releases transport-owned dialer and connector resources.
func (tr *Transport) Close() error {
	if tr == nil {
		return nil
	}
	var errs []error
	if closer, ok := tr.dialer.(io.Closer); ok {
		errs = append(errs, closer.Close())
	}
	if closer, ok := tr.connector.(io.Closer); ok {
		errs = append(errs, closer.Close())
	}
	return errors.Join(errs...)
}
