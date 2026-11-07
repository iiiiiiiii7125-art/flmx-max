package wt

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"

	"github.com/go-gost/core/dialer"
	md "github.com/go-gost/core/metadata"
	"github.com/go-gost/x/registry"
	"github.com/quic-go/quic-go"
	wt "github.com/quic-go/webtransport-go"
)

func init() {
	registry.DialerRegistry().Register("wt", NewDialer)
}

type wtDialer struct {
	clients     map[string]*Client
	clientMutex sync.Mutex
	retired     bool
	md          metadata
	options     dialer.Options
}

func NewDialer(opts ...dialer.Option) dialer.Dialer {
	options := dialer.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &wtDialer{
		clients: make(map[string]*Client),
		options: options,
	}
}

func (d *wtDialer) Init(md md.Metadata) (err error) {
	if err = d.parseMetadata(md); err != nil {
		return
	}
	return
}

// Multiplex implements dialer.Multiplexer interface.
func (d *wtDialer) Multiplex() bool {
	return true
}

func (d *wtDialer) Dial(ctx context.Context, addr string, opts ...dialer.DialOption) (conn net.Conn, err error) {
	d.clientMutex.Lock()
	defer d.clientMutex.Unlock()

	if d.retired {
		return nil, net.ErrClosed
	}

	client := d.clients[addr]
	if client == nil {
		var options dialer.DialOptions
		for _, opt := range opts {
			opt(&options)
		}

		host := d.md.host
		if host == "" {
			host = options.Host
		}
		if h, _, _ := net.SplitHostPort(host); h != "" {
			host = h
		}

		client = &Client{
			log:    d.options.Logger,
			host:   host,
			path:   d.md.path,
			header: d.md.header,
			dialer: &wt.Dialer{
				TLSClientConfig: d.options.TLSConfig,
				DialAddr: func(ctx context.Context, adr string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
					// d.options.Logger.Infof("dial: %s, %s, %s", addr, adr, host)
					udpAddr, err := net.ResolveUDPAddr("udp", addr)
					if err != nil {
						return nil, err
					}

					udpConn, err := options.Dialer.Dial(ctx, "udp", "")
					if err != nil {
						return nil, err
					}

					return quic.DialEarly(ctx, udpConn.(net.PacketConn), udpAddr, tlsCfg, cfg)
				},
				QUICConfig: &quic.Config{
					KeepAlivePeriod:      d.md.keepAlivePeriod,
					HandshakeIdleTimeout: d.md.handshakeTimeout,
					MaxIdleTimeout:       d.md.maxIdleTimeout,
					/*
						Versions: []quic.VersionNumber{
							quic.Version1,
						},
					*/
					MaxIncomingStreams:               int64(d.md.maxStreams),
					EnableDatagrams:                  true,
					EnableStreamResetPartialDelivery: true,
				},
			},
		}
		d.clients[addr] = client
	}

	return client.Dial(ctx, addr)
}

// Retire closes every cached client of a dialer whose chain has been
// replaced, so their WebTransport sessions do not outlive the
// generation that created them (P1-08).
func (d *wtDialer) Retire() {
	for _, client := range d.detachClients() {
		client.Close()
	}
}

// Close immediately releases all cached clients.
func (d *wtDialer) Close() error {
	var errs []error
	for _, client := range d.detachClients() {
		errs = append(errs, client.Close())
	}
	return errors.Join(errs...)
}

func (d *wtDialer) detachClients() []*Client {
	if d == nil {
		return nil
	}
	d.clientMutex.Lock()
	d.retired = true
	clients := make([]*Client, 0, len(d.clients))
	for _, client := range d.clients {
		if client != nil {
			clients = append(clients, client)
		}
	}
	d.clients = make(map[string]*Client)
	d.clientMutex.Unlock()
	return clients
}
