package tun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/hop"
	md "github.com/go-gost/core/metadata"
	ctxvalue "github.com/go-gost/x/ctx"
	tun_util "github.com/go-gost/x/internal/util/tun"
	"github.com/go-gost/x/registry"
)

var (
	ErrTun        = errors.New("tun device error")
	ErrInvalidNet = errors.New("invalid net IP")
)

func init() {
	registry.HandlerRegistry().Register("tun", NewHandler)
}

type tunHandler struct {
	hop hop.Hop
	// routes is the learned forwarding table (source IP -> tunnel
	// address; a single collapsed key in p2p mode). It intentionally
	// has NO entry aging (R2 wave-3 disposition of the
	// FIX-LEAK-FAMILY §7 residue note): entries hold only two small
	// values — no connection, goroutine or handle — the table lives
	// and dies with this handler instance, and its growth is bounded
	// by the address space of the attached network (keys are source
	// IPs of packets arriving on an established association, not
	// values an unauthenticated peer can mint at will). The panel
	// never emits tun services today; the handler is registered only
	// for upstream gost completeness. If tun services ever become a
	// product surface, add bounded entry aging here first: a departed
	// peer's stale entry can otherwise keep attracting traffic to
	// its old address.
	routes  sync.Map
	md      metadata
	options handler.Options
}

func NewHandler(opts ...handler.Option) handler.Handler {
	options := handler.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &tunHandler{
		options: options,
	}
}

func (h *tunHandler) Init(md md.Metadata) (err error) {
	if err = h.parseMetadata(md); err != nil {
		return
	}

	return
}

// Forward implements handler.Forwarder.
func (h *tunHandler) Forward(hop hop.Hop) {
	h.hop = hop
}

func (h *tunHandler) Handle(ctx context.Context, conn net.Conn, opts ...handler.HandleOption) error {
	defer conn.Close()

	log := h.options.Logger

	v, _ := conn.(md.Metadatable)
	if v == nil {
		err := errors.New("tun: wrong connection type")
		log.Error(err)
		return err
	}
	config := v.Metadata().Get("config").(*tun_util.Config)

	start := time.Now()
	log = log.WithFields(map[string]any{
		"remote": conn.RemoteAddr().String(),
		"local":  conn.LocalAddr().String(),
		"sid":    ctxvalue.SidFromContext(ctx),
	})

	log.Infof("%s <> %s", conn.RemoteAddr(), conn.LocalAddr())
	defer func() {
		log.WithFields(map[string]any{
			"duration": time.Since(start),
		}).Infof("%s >< %s", conn.RemoteAddr(), conn.LocalAddr())
	}()

	var target *chain.Node
	if h.hop != nil {
		target = h.hop.Select(ctx)
	}
	if target != nil {
		network := "udp"
		if _, _, err := net.SplitHostPort(target.Addr); err != nil {
			network = "ip"
		}

		log = log.WithFields(map[string]any{
			"dst": fmt.Sprintf("%s/%s", target.Addr, network),
		})
		log.Debugf("%s >> %s", conn.RemoteAddr(), target.Addr)

		if err := h.handleClient(ctx, conn, network, target.Addr, config, log); err != nil {
			log.Error(err)
		}
		return nil
	}

	return h.handleServer(ctx, conn, config, log)
}

type tunRouteKey [16]byte

func ipToTunRouteKey(ip net.IP) (key tunRouteKey) {
	copy(key[:], ip.To16())
	return
}
