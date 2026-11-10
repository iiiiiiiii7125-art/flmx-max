package service

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/auth"
	"github.com/go-gost/core/service"
	"github.com/go-gost/x/api"
	"github.com/go-gost/x/internal/keepalive"
)

type options struct {
	accessLog  bool
	pathPrefix string
	auther     auth.Authenticator
}

type Option func(*options)

func PathPrefixOption(pathPrefix string) Option {
	return func(o *options) {
		o.pathPrefix = pathPrefix
	}
}

func AccessLogOption(enable bool) Option {
	return func(o *options) {
		o.accessLog = enable
	}
}

func AutherOption(auther auth.Authenticator) Option {
	return func(o *options) {
		o.auther = auther
	}
}

// Listen creates a TCP listener stamped with the agent-wide kernel
// keepalive policy (internal/keepalive). It is exported for the
// main module (go-gost/program.go), which cannot import
// x/internal/keepalive directly (Go internal-package rule: the
// main module is not rooted under github.com/go-gost/x), so the
// management-plane listeners created there go through this helper
// instead of hand-writing keepalive parameters.
func Listen(network, addr string) (net.Listener, error) {
	lc := net.ListenConfig{}
	keepalive.ApplyListenConfig(&lc)
	return lc.Listen(context.Background(), network, addr)
}

type server struct {
	s      *http.Server
	ln     net.Listener
	cclose chan struct{}
}

func NewService(network, addr string, opts ...Option) (service.Service, error) {
	if network == "" {
		network = "tcp"
	}
	ln, err := Listen(network, addr)
	if err != nil {
		return nil, err
	}

	var options options
	for _, opt := range opts {
		opt(&options)
	}

	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	api.Register(r, &api.Options{
		AccessLog:  options.accessLog,
		PathPrefix: options.pathPrefix,
		Auther:     options.auther,
	})

	return &server{
		s: &http.Server{
			Handler:           r,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		ln:     ln,
		cclose: make(chan struct{}),
	}, nil
}

func (s *server) Serve() error {
	return s.s.Serve(s.ln)
}

func (s *server) Addr() net.Addr {
	return s.ln.Addr()
}

func (s *server) Close() error {
	// Close can race the goroutine entering Serve during a failed startup.
	// http.Server.Close alone does not own the listener until Serve starts.
	err := s.s.Close()
	s.ln.Close()
	return err
}

func (s *server) IsClosed() bool {
	select {
	case <-s.cclose:
		return true
	default:
		return false
	}
}
