package metrics

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/go-gost/core/handler"
	md "github.com/go-gost/core/metadata"
	xhttp "github.com/go-gost/x/internal/net/http"
	xmetrics "github.com/go-gost/x/metrics"
	"github.com/go-gost/x/registry"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func init() {
	registry.HandlerRegistry().Register("metrics", NewHandler)
}

type metricsHandler struct {
	handler http.Handler
	mux     *http.ServeMux
	md      metadata
	options handler.Options
}

func NewHandler(opts ...handler.Option) handler.Handler {
	options := handler.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &metricsHandler{
		options: options,
	}
}

func (h *metricsHandler) Init(md md.Metadata) (err error) {
	if err = h.parseMetadata(md); err != nil {
		return
	}

	xmetrics.Enable(true)

	h.handler = promhttp.Handler()

	mux := http.NewServeMux()
	mux.Handle(h.md.path, http.HandlerFunc(h.handleFunc))
	h.mux = mux

	return
}

func (h *metricsHandler) Handle(ctx context.Context, conn net.Conn, opts ...handler.HandleOption) error {
	l := xhttp.NewSingleConnListener(ctx, conn)

	s := http.Server{
		Handler: h.mux,
	}
	// One request per connection: the service hands this handler a single
	// accepted connection and expects Handle to return once it is served.
	// With keep-alives on, a pooled client could hold Handle open
	// indefinitely (IdleTimeout is unset).
	s.SetKeepAlivesEnabled(false)
	s.Serve(l)

	return s.Shutdown(ctx)
}

func (h *metricsHandler) Close() error {
	return nil
}

func (h *metricsHandler) handleFunc(w http.ResponseWriter, r *http.Request) {
	if auther := h.options.Auther; auther != nil {
		u, p, _ := r.BasicAuth()
		if _, ok := auther.Authenticate(r.Context(), u, p); !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	log := h.options.Logger
	start := time.Now()

	h.handler.ServeHTTP(w, r)

	log = log.WithFields(map[string]any{
		"remote":   r.RemoteAddr,
		"duration": time.Since(start),
	})
	log.Infof("%s %s", r.Method, r.RequestURI)
}


