package tap

import (
	"context"
	"net"
	"time"

	"github.com/go-gost/core/limiter"
	"github.com/go-gost/core/listener"
	"github.com/go-gost/core/logger"
	mdata "github.com/go-gost/core/metadata"
	xnet "github.com/go-gost/x/internal/net"
	"github.com/go-gost/x/internal/util/eventlog"
	traffic_limiter "github.com/go-gost/x/limiter/traffic"
	limiter_wrapper "github.com/go-gost/x/limiter/traffic/wrapper"
	mdx "github.com/go-gost/x/metadata"
	metrics "github.com/go-gost/x/metrics/wrapper"
	stats "github.com/go-gost/x/observer/stats/wrapper"
	"github.com/go-gost/x/registry"
)

func init() {
	registry.ListenerRegistry().Register("tap", NewListener)
}

type tapListener struct {
	addr    net.Addr
	cqueue  chan net.Conn
	closed  chan struct{}
	logger  logger.Logger
	md      metadata
	options listener.Options

	// events aggregates the reopen-loop failure logging, which
	// retries with no pacing when the device is absent (P1-03
	// family; rate is loop-bound, not error-bound).
	events  *eventlog.Aggregator
	openErr *eventlog.Counter
}

func NewListener(opts ...listener.Option) listener.Listener {
	options := listener.Options{}
	for _, opt := range opts {
		opt(&options)
	}
	events := eventlog.New(options.Logger, eventlog.DefaultInterval)
	return &tapListener{
		logger:  options.Logger,
		options: options,
		events:  events,
		openErr: events.DetailCounter(
			"tap listener: %d device reopen failure(s), last: %s, %d total"),
	}
}

func (l *tapListener) Init(md mdata.Metadata) (err error) {
	if err = l.parseMetadata(md); err != nil {
		return
	}

	network := "udp"
	if xnet.IsIPv4(l.options.Addr) {
		network = "udp4"
	}
	l.addr, err = net.ResolveUDPAddr(network, l.options.Addr)
	if err != nil {
		return
	}

	l.cqueue = make(chan net.Conn, 1)
	l.closed = make(chan struct{})

	go l.listenLoop()

	return
}

func (l *tapListener) listenLoop() {
	for {
		ctx, cancel := context.WithCancel(context.Background())
		err := func() error {
			ifce, name, ip, err := l.createTap()
			if err != nil {
				if ifce != nil {
					ifce.Close()
				}
				return err
			}

			itf, err := net.InterfaceByName(name)
			if err != nil {
				return err
			}

			addrs, _ := itf.Addrs()
			l.logger.Infof("name: %s, net: %s, mtu: %d, addrs: %s",
				itf.Name, ip, itf.MTU, addrs)

			var c net.Conn
			c = &conn{
				ifce:   ifce,
				laddr:  l.addr,
				raddr:  &net.IPAddr{IP: ip},
				cancel: cancel,
			}
			c = metrics.WrapConn(l.options.Service, c)
			c = stats.WrapConn(c, l.options.Stats)
			c = limiter_wrapper.WrapConn(
				c,
				l.options.TrafficLimiter,
				traffic_limiter.ServiceLimitKey,
				limiter.ScopeOption(limiter.ScopeService),
				limiter.ServiceOption(l.options.Service),
				limiter.NetworkOption(c.LocalAddr().Network()),
			)
			c = withMetadata(mdx.NewMetadata(map[string]any{
				"config": l.md.config,
			}), c)

			l.cqueue <- c

			return nil
		}()
		if err != nil {
			l.openErr.NoteErr(err)
			cancel()
		}

		select {
		case <-ctx.Done():
		case <-l.closed:
			return
		}

		time.Sleep(time.Second)
	}
}

func (l *tapListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.cqueue:
		return conn, nil
	case <-l.closed:
	}

	return nil, listener.ErrClosed
}

func (l *tapListener) Addr() net.Addr {
	return l.addr
}

func (l *tapListener) Close() error {
	l.events.Flush()
	select {
	case <-l.closed:
		return net.ErrClosed
	default:
		close(l.closed)
	}
	return nil
}
