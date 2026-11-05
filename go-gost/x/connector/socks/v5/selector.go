package v5

import (
	"crypto/tls"
	"net"
	"net/url"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/gosocks5"
	"github.com/go-gost/x/internal/util/socks"
)

type clientSelector struct {
	methods   []uint8
	User      *url.Userinfo
	TLSConfig *tls.Config
	logger    logger.Logger
}

func (s *clientSelector) Methods() []uint8 {
	s.logger.Debug("methods: ", s.methods)
	return s.methods
}

func (s *clientSelector) AddMethod(methods ...uint8) {
	s.methods = append(s.methods, methods...)
}

func (s *clientSelector) Select(methods ...uint8) (method uint8) {
	return
}

// tlsConfigFor returns the TLS config to handshake conn with. When
// verification is on but the config carries no ServerName, the
// expected name is taken from the address of the SOCKS server being
// connected to (on a clone, so the shared config is never mutated);
// without it crypto/tls would refuse the handshake outright. An
// explicitly configured ServerName or an explicit InsecureSkipVerify
// opt-in is honoured as-is.
func (s *clientSelector) tlsConfigFor(conn net.Conn) *tls.Config {
	cfg := s.TLSConfig
	if cfg == nil || cfg.InsecureSkipVerify || cfg.ServerName != "" {
		return cfg
	}
	cfg = cfg.Clone()
	if host, _, err := net.SplitHostPort(conn.RemoteAddr().String()); err == nil {
		cfg.ServerName = host
	} else {
		cfg.ServerName = conn.RemoteAddr().String()
	}
	return cfg
}

func (s *clientSelector) OnSelected(method uint8, conn net.Conn) (string, net.Conn, error) {
	s.logger.Debug("method selected: ", method)

	switch method {
	case gosocks5.MethodNoAuth:

	case socks.MethodTLS:
		conn = tls.Client(conn, s.tlsConfigFor(conn))

	case gosocks5.MethodUserPass, socks.MethodTLSAuth:
		if method == socks.MethodTLSAuth {
			conn = tls.Client(conn, s.tlsConfigFor(conn))
		}

		var username, password string
		if s.User != nil {
			username = s.User.Username()
			password, _ = s.User.Password()
		}

		req := gosocks5.NewUserPassRequest(gosocks5.UserPassVer, username, password)
		s.logger.Trace(req)
		if err := req.Write(conn); err != nil {
			s.logger.Error(err)
			return "", nil, err
		}

		resp, err := gosocks5.ReadUserPassResponse(conn)
		if err != nil {
			s.logger.Error(err)
			return "", nil, err
		}
		s.logger.Trace(resp)

		if resp.Status != gosocks5.Succeeded {
			return "", nil, gosocks5.ErrAuthFailure
		}

	case gosocks5.MethodNoAcceptable:
		return "", nil, gosocks5.ErrBadMethod
	default:
		return "", nil, gosocks5.ErrBadFormat
	}
	return "", conn, nil
}
