package api

import (
	"context"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/handler"
	md "github.com/go-gost/core/metadata"
	"github.com/go-gost/x/api"
	xhttp "github.com/go-gost/x/internal/net/http"
	"github.com/go-gost/x/registry"
)

func init() {
	registry.HandlerRegistry().Register("api", NewHandler)
}

type apiHandler struct {
	handler http.Handler
	md      metadata
	options handler.Options
}

func NewHandler(opts ...handler.Option) handler.Handler {
	options := handler.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &apiHandler{
		options: options,
	}
}

func (h *apiHandler) Init(md md.Metadata) (err error) {
	if err = h.parseMetadata(md); err != nil {
		return
	}

	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	api.Register(r, &api.Options{
		AccessLog:  h.md.accesslog,
		PathPrefix: h.md.pathPrefix,
		Auther:     h.options.Auther,
	})
	h.handler = r

	return
}

func (h *apiHandler) Handle(ctx context.Context, conn net.Conn, opts ...handler.HandleOption) error {
	l := xhttp.NewSingleConnListener(ctx, conn)

	s := http.Server{
		Handler: h.handler,
	}
	// One request per connection: the service hands this handler a single
	// accepted connection and expects Handle to return once it is served.
	// With keep-alives on, a pooled client could hold Handle open
	// indefinitely (IdleTimeout is unset).
	s.SetKeepAlivesEnabled(false)
	s.Serve(l)

	return s.Shutdown(ctx)
}
