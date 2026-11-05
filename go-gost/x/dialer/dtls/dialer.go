package dtls

import (
	"context"
	"crypto/tls"
	"net"

	"github.com/go-gost/core/dialer"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	xdtls "github.com/go-gost/x/internal/util/dtls"
	"github.com/go-gost/x/registry"
	"github.com/pion/dtls/v3"
	dtlsnet "github.com/pion/dtls/v3/pkg/net"
)

func init() {
	registry.DialerRegistry().Register("dtls", NewDialer)
}

type dtlsDialer struct {
	md      metadata
	logger  logger.Logger
	options dialer.Options
}

func NewDialer(opts ...dialer.Option) dialer.Dialer {
	options := dialer.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &dtlsDialer{
		logger:  options.Logger,
		options: options,
	}
}

func (d *dtlsDialer) Init(md md.Metadata) (err error) {
	return d.parseMetadata(md)
}

// effectiveTLSConfig resolves the TLS settings for a dial to addr. An
// explicitly configured TLS config is used as-is; with no config the
// default verifies the peer (F1-01) — non-verification remains
// available only through an explicit InsecureSkipVerify opt-in. The
// expected server name falls back to the dialled host when the config
// does not carry one.
func effectiveTLSConfig(cfg *tls.Config, addr string) (tlsCfg *tls.Config, serverName string) {
	if cfg == nil {
		cfg = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}
	serverName = cfg.ServerName
	if serverName == "" {
		if host, _, err := net.SplitHostPort(addr); err == nil {
			serverName = host
		} else {
			serverName = addr
		}
	}
	return cfg, serverName
}

func (d *dtlsDialer) Dial(ctx context.Context, addr string, opts ...dialer.DialOption) (net.Conn, error) {
	var options dialer.DialOptions
	for _, opt := range opts {
		opt(&options)
	}

	conn, err := options.Dialer.Dial(ctx, "udp", addr)
	if err != nil {
		return nil, err
	}

	tlsCfg, serverName := effectiveTLSConfig(d.options.TLSConfig, addr)
	config := dtls.Config{
		Certificates:         tlsCfg.Certificates,
		InsecureSkipVerify:   tlsCfg.InsecureSkipVerify,
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		ServerName:           serverName,
		RootCAs:              tlsCfg.RootCAs,
		FlightInterval:       d.md.flightInterval,
		MTU:                  d.md.mtu,
	}

	c, err := dtls.Client(dtlsnet.PacketConnFromConn(conn), conn.RemoteAddr(), &config)
	if err != nil {
		return nil, err
	}
	if err := c.HandshakeContext(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return xdtls.Conn(c, d.md.bufferSize), nil
}
