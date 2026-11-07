package grpc

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/go-gost/core/dialer"
	md "github.com/go-gost/core/metadata"
	pb "github.com/go-gost/x/internal/util/grpc/proto"
	"github.com/go-gost/x/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

func init() {
	registry.DialerRegistry().Register("grpc", NewDialer)
}

// grpcClient pairs the generated stub with its ClientConn so the
// connection can actually be closed when the client is evicted or the
// dialer is retired (P1-08: previously only the stub was cached, so
// the ClientConn — its TCP connection and goroutines — leaked across
// chain generations).
type grpcClient struct {
	client pb.GostTunelClientX
	conn   *grpc.ClientConn
}

type grpcDialer struct {
	clients     map[string]*grpcClient
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

	return &grpcDialer{
		clients: make(map[string]*grpcClient),
		options: options,
	}
}

func (d *grpcDialer) Init(md md.Metadata) (err error) {
	return d.parseMetadata(md)
}

// Multiplex implements dialer.Multiplexer interface.
func (d *grpcDialer) Multiplex() bool {
	return true
}

func (d *grpcDialer) Dial(ctx context.Context, addr string, opts ...dialer.DialOption) (net.Conn, error) {
	d.clientMutex.Lock()
	defer d.clientMutex.Unlock()

	if d.retired {
		return nil, net.ErrClosed
	}

	entry, ok := d.clients[addr]
	if ok && entry.conn.GetState() == connectivity.Shutdown {
		// delete-on-dead: the cached connection is gone for good,
		// drop it and fall through to create a fresh one.
		entry.conn.Close()
		delete(d.clients, addr)
		entry, ok = nil, false
	}
	if !ok {
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
		// d.options.Logger.Infof("grpc dialer, addr %s, host %s/%s", addr, d.md.host, options.Host)

		grpcOpts := []grpc.DialOption{
			// grpc.WithBlock(),
			grpc.WithContextDialer(func(c context.Context, s string) (net.Conn, error) {
				return options.Dialer.Dial(c, "tcp", s)
			}),
			grpc.WithAuthority(host),
			grpc.WithConnectParams(grpc.ConnectParams{
				Backoff:           backoff.DefaultConfig,
				MinConnectTimeout: d.md.minConnectTimeout,
			}),
		}
		if !d.md.insecure {
			grpcOpts = append(grpcOpts, grpc.WithTransportCredentials(credentials.NewTLS(d.options.TLSConfig)))
		} else {
			grpcOpts = append(grpcOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		}

		if d.md.keepalive {
			grpcOpts = append(grpcOpts, grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:                d.md.keepaliveTime,
				Timeout:             d.md.keepaliveTimeout,
				PermitWithoutStream: d.md.keepalivePermitWithoutStream,
			}))
		}

		cc, err := grpc.NewClient(addr, grpcOpts...)
		if err != nil {
			d.options.Logger.Error(err)
			return nil, err
		}
		entry = &grpcClient{
			client: pb.NewGostTunelClientX(cc),
			conn:   cc,
		}
		d.clients[addr] = entry
	}

	ctx2, cancel := context.WithCancel(context.Background())
	cli, err := entry.client.TunnelX(ctx2, d.md.path)
	if err != nil {
		cancel()
		return nil, err
	}

	return &conn{
		c:          cli,
		localAddr:  &net.TCPAddr{},
		remoteAddr: &net.TCPAddr{},
		cancelFunc: cancel,
	}, nil
}

// Retire releases every cached client of a dialer whose chain has
// been replaced. grpc offers no graceful drain for a ClientConn, so
// retiring closes the connections outright; Chain.Retire can now
// reach them instead of leaking them across generations (P1-08).
func (d *grpcDialer) Retire() {
	for _, entry := range d.detachClients() {
		entry.conn.Close()
	}
}

// Close immediately releases all cached ClientConns.
func (d *grpcDialer) Close() error {
	var errs []error
	for _, entry := range d.detachClients() {
		errs = append(errs, entry.conn.Close())
	}
	return errors.Join(errs...)
}

func (d *grpcDialer) detachClients() []*grpcClient {
	if d == nil {
		return nil
	}
	d.clientMutex.Lock()
	d.retired = true
	clients := make([]*grpcClient, 0, len(d.clients))
	for _, entry := range d.clients {
		if entry != nil {
			clients = append(clients, entry)
		}
	}
	d.clients = make(map[string]*grpcClient)
	d.clientMutex.Unlock()
	return clients
}
