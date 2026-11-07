package h2

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/go-gost/core/dialer"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	"github.com/go-gost/x/registry"
	"golang.org/x/net/http2"
)

func init() {
	registry.DialerRegistry().Register("h2", NewTLSDialer)
	registry.DialerRegistry().Register("h2c", NewDialer)
}

type h2Dialer struct {
	clients     map[string]*http.Client
	clientMutex sync.Mutex
	retired     bool
	h2c         bool
	logger      logger.Logger
	md          metadata
	options     dialer.Options
}

func NewDialer(opts ...dialer.Option) dialer.Dialer {
	options := dialer.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &h2Dialer{
		h2c:     true,
		clients: make(map[string]*http.Client),
		logger:  options.Logger,
		options: options,
	}
}

func NewTLSDialer(opts ...dialer.Option) dialer.Dialer {
	options := dialer.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &h2Dialer{
		clients: make(map[string]*http.Client),
		logger:  options.Logger,
		options: options,
	}
}

func (d *h2Dialer) Init(md md.Metadata) (err error) {
	if err = d.parseMetadata(md); err != nil {
		return
	}

	return nil
}

// Multiplex implements dialer.Multiplexer interface.
func (d *h2Dialer) Multiplex() bool {
	return true
}

func (d *h2Dialer) Dial(ctx context.Context, address string, opts ...dialer.DialOption) (net.Conn, error) {
	d.clientMutex.Lock()
	retired := d.retired
	d.clientMutex.Unlock()
	if retired {
		return nil, net.ErrClosed
	}

	raddr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		d.logger.Error(err)
		return nil, err
	}

	d.clientMutex.Lock()

	client, ok := d.clients[address]
	if !ok {
		options := &dialer.DialOptions{}
		for _, opt := range opts {
			opt(options)
		}

		client = &http.Client{}
		if d.h2c {
			client.Transport = &http2.Transport{
				AllowHTTP: true,
				DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
					return options.Dialer.Dial(ctx, network, addr)
				},
			}
		} else {
			client.Transport = &http.Transport{
				TLSClientConfig: d.options.TLSConfig,
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return options.Dialer.Dial(ctx, network, addr)
				},
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			}
		}

		d.clients[address] = client
	}
	d.clientMutex.Unlock()

	host := d.md.host
	if host == "" {
		host = address
	}

	scheme := "https"
	if d.h2c {
		scheme = "http"
	}
	pr, pw := io.Pipe()
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Scheme: scheme, Host: host},
		Header: d.md.header,
		Body:   pr,
		Host:   host,
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if d.md.path != "" {
		req.Method = http.MethodGet
		req.URL.Path = d.md.path
	}

	if d.logger.IsLevelEnabled(logger.TraceLevel) {
		dump, _ := httputil.DumpRequest(req, false)
		d.logger.Trace(string(dump))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if d.logger.IsLevelEnabled(logger.TraceLevel) {
		dump, _ := httputil.DumpResponse(resp, false)
		d.logger.Trace(string(dump))
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, errors.New(resp.Status)
	}

	conn := &http2Conn{
		r:          resp.Body,
		w:          pw,
		remoteAddr: raddr,
		localAddr:  &net.TCPAddr{IP: net.IPv4zero, Port: 0},
	}
	return conn, nil
}

// Retire closes idle connections of every cached client of a dialer
// whose chain has been replaced, so the clients do not outlive the
// generation that created them (P1-08 family: this table had no
// eviction path at all).
func (d *h2Dialer) Retire() {
	for _, client := range d.detachClients() {
		client.CloseIdleConnections()
	}
}

// Close immediately releases all cached clients.
func (d *h2Dialer) Close() error {
	for _, client := range d.detachClients() {
		client.CloseIdleConnections()
	}
	return nil
}

func (d *h2Dialer) detachClients() []*http.Client {
	if d == nil {
		return nil
	}
	d.clientMutex.Lock()
	d.retired = true
	clients := make([]*http.Client, 0, len(d.clients))
	for _, client := range d.clients {
		if client != nil {
			clients = append(clients, client)
		}
	}
	d.clients = make(map[string]*http.Client)
	d.clientMutex.Unlock()
	return clients
}
