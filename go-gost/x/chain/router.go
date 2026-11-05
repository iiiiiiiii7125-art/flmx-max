package chain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/recorder"
	ctxvalue "github.com/go-gost/x/ctx"
	xnet "github.com/go-gost/x/internal/net"
)

// DialTargetGuard validates the resolved dial target immediately
// before any connection is attempted, and may rewrite it (pin a
// hostname to a validated IP literal). A non-nil error aborts the
// dial (fail-closed, no retries: the verdict is deterministic for a
// given resolution). Installed by service parsing for services whose
// bypass must hold at the dial point, not only at the handler's
// pre-check (A02-001: check and dial used to resolve independently,
// so a rebinding DNS could pass the check and land on a private
// address at dial time).
type DialTargetGuard func(ctx context.Context, network, address string) (string, error)

type Router struct {
	options chain.RouterOptions

	dialTargetGuard DialTargetGuard
}

// SetDialTargetGuard installs the dial-point target guard. It is a
// setter rather than a RouterOption because core RouterOptions has no
// field for it; service parsing wires it right after NewRouter.
func (r *Router) SetDialTargetGuard(g DialTargetGuard) {
	if r != nil {
		r.dialTargetGuard = g
	}
}

func NewRouter(opts ...chain.RouterOption) *Router {
	r := &Router{}
	for _, opt := range opts {
		if opt != nil {
			opt(&r.options)
		}
	}
	if r.options.Timeout == 0 {
		r.options.Timeout = 15 * time.Second
	}

	if r.options.Logger == nil {
		r.options.Logger = logger.Default().WithFields(map[string]any{"kind": "router"})
	}
	return r
}

func (r *Router) Options() *chain.RouterOptions {
	if r == nil {
		return nil
	}
	return &r.options
}

func (r *Router) Dial(ctx context.Context, network, address string) (conn net.Conn, err error) {
	host := address
	if h, _, _ := net.SplitHostPort(address); h != "" {
		host = h
	}
	r.record(ctx, recorder.RecorderServiceRouterDialAddress, []byte(host))

	log := r.options.Logger.WithFields(map[string]any{
		"sid": ctxvalue.SidFromContext(ctx),
	})

	conn, err = r.dial(ctx, network, address, log)
	if err != nil {
		r.record(ctx, recorder.RecorderServiceRouterDialAddressError, []byte(host))
		return
	}

	if network == "udp" || network == "udp4" || network == "udp6" {
		if _, ok := conn.(net.PacketConn); !ok && conn.RemoteAddr().Network() != "udp" {
			return &packetConn{conn}, nil
		}
	}
	return
}

func (r *Router) record(ctx context.Context, name string, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	for _, rec := range r.options.Recorders {
		if rec.Record == name {
			return rec.Recorder.Record(ctx, data)
		}
	}
	return nil
}

func (r *Router) dial(ctx context.Context, network, address string, log logger.Logger) (conn net.Conn, err error) {
	count := r.options.Retries + 1
	if count <= 0 {
		count = 1
	}

	log.Debugf("dial %s/%s", address, network)

	// Nodes that fail an attempt join the exclude list for the next
	// one (D3-04): without this, every retry re-selected the node
	// that had just failed. The hop layer honours the list via
	// ctxvalue.ExcludeNodesFromContext; any list the caller already
	// supplied is kept and extended.
	exclude := ctxvalue.ExcludeNodesFromContext(ctx)

	for i := 0; i < count; i++ {
		ctx := ctx
		if len(exclude) > 0 {
			ctx = ctxvalue.ContextWithExcludeNodes(ctx, exclude)
		}
		// The per-attempt timeout context is managed explicitly, not
		// with defer (D3-07): a failed attempt's context is released
		// at the end of the iteration, while a successful attempt's
		// context must outlive this function for exactly as long as
		// the returned conn — context-retaining transports built
		// over the conn observe its cancellation. Ownership of the
		// cancel func therefore transfers to the conn (see
		// cancelOnCloseConn).
		var cancel context.CancelFunc
		if r.options.Timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, r.options.Timeout)
		}

		buf := ctxvalue.BufferFromContext(ctx)
		if buf != nil {
			buf.Reset()
		}

		var ipAddr string
		ipAddr, err = xnet.Resolve(ctx, "ip", address, r.options.Resolver, r.options.HostMapper, log)
		if err != nil {
			log.Error(err)
			if cancel != nil {
				cancel()
			}
			break
		}

		// Dial-point re-validation (A02-001): whatever resolution
		// produced ipAddr (service resolver, hosts mapper, or none —
		// in which case ipAddr is still a hostname the route's dialer
		// would resolve a second time), the guard validates the final
		// target and pins hostnames to a validated IP literal, so the
		// address that is connected to is the address that was judged.
		if r.dialTargetGuard != nil {
			ipAddr, err = r.dialTargetGuard(ctx, network, ipAddr)
			if err != nil {
				log.Error(err)
				if cancel != nil {
					cancel()
				}
				return nil, err
			}
		}

		if buf != nil {
			buf.Reset()
		}

		var route chain.Route
		if r.options.Chain != nil {
			route = r.options.Chain.Route(ctx, network, ipAddr, chain.WithHostRouteOption(address))
			if route == nil || len(route.Nodes()) == 0 {
				// Fail closed (D1-H4): a configured chain that yields no
				// route must not fall back to a direct dial. Mirrors Bind.
				err = emptyRouteError(r.options.Chain)
				if cancel != nil {
					cancel()
				}
				return
			}
		}

		if buf == nil {
			buf = &bytes.Buffer{}
		}
		for _, node := range routePath(route) {
			fmt.Fprintf(buf, "%s@%s > ", node.Name, node.Addr)
		}
		fmt.Fprintf(buf, "%s", ipAddr)
		log.Debugf("route(retry=%d) %s", i, buf.String())

		if route == nil {
			route = DefaultRoute
		}
		conn, err = route.Dial(ctx, network, ipAddr,
			chain.InterfaceDialOption(r.options.IfceName),
			chain.NetnsDialOption(r.options.Netns),
			chain.SockOptsDialOption(r.options.SockOpts),
			chain.LoggerDialOption(log),
		)
		if err == nil {
			if cancel != nil {
				conn = cancelOnCloseConn(conn, cancel)
			}
			break
		}
		if cancel != nil {
			cancel()
		}
		for _, node := range routePath(route) {
			exclude = append(exclude, node.Name, node.Addr)
		}
		log.Errorf("route(retry=%d) %s", i, err)
	}

	return
}

func (r *Router) Bind(ctx context.Context, network, address string, opts ...chain.BindOption) (ln net.Listener, err error) {
	count := r.options.Retries + 1
	if count <= 0 {
		count = 1
	}

	log := r.options.Logger.WithFields(map[string]any{
		"sid": ctxvalue.SidFromContext(ctx),
	})

	log.Debugf("bind on %s/%s", address, network)

	// Same retry-exclusion contract as dial (D3-04).
	exclude := ctxvalue.ExcludeNodesFromContext(ctx)

	for i := 0; i < count; i++ {
		ctx := ctx
		if len(exclude) > 0 {
			ctx = ctxvalue.ContextWithExcludeNodes(ctx, exclude)
		}
		// Explicit per-attempt context management, same contract as
		// dial (D3-07): failed attempts release their context at the
		// end of the iteration; a successful attempt's context lives
		// exactly as long as the returned listener.
		var cancel context.CancelFunc
		if r.options.Timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, r.options.Timeout)
		}

		var route chain.Route
		if r.options.Chain != nil {
			route = r.options.Chain.Route(ctx, network, address)
			if route == nil || len(route.Nodes()) == 0 {
				err = emptyRouteError(r.options.Chain)
				if cancel != nil {
					cancel()
				}
				return
			}
		}

		if log.IsLevelEnabled(logger.DebugLevel) {
			buf := bytes.Buffer{}
			for _, node := range routePath(route) {
				fmt.Fprintf(&buf, "%s@%s > ", node.Name, node.Addr)
			}
			fmt.Fprintf(&buf, "%s", address)
			log.Debugf("route(retry=%d) %s", i, buf.String())
		}

		if route == nil {
			route = DefaultRoute
		}
		ln, err = route.Bind(ctx, network, address, opts...)
		if err == nil {
			if cancel != nil {
				ln = &cancelOnCloseListener{Listener: ln, cancel: cancel}
			}
			break
		}
		if cancel != nil {
			cancel()
		}
		for _, node := range routePath(route) {
			exclude = append(exclude, node.Name, node.Addr)
		}
		log.Errorf("route(retry=%d) %s", i, err)
	}

	return
}

func routePath(route chain.Route) (path []*chain.Node) {
	if route == nil {
		return
	}
	for _, node := range route.Nodes() {
		if tr := node.Options().Transport; tr != nil {
			path = append(path, routePath(tr.Options().Route)...)
		}
		path = append(path, node)
	}
	return
}

var errHalfCloseNotSupported = errors.New("half-close not supported by the underlying connection")

// cancelOnCloseConn transfers ownership of a successful dial
// attempt's timeout-context cancel func to the returned conn: the
// context lives exactly as long as the conn instead of being
// cancelled when Dial returns (D3-07). Packet conns keep their
// net.PacketConn face.
func cancelOnCloseConn(conn net.Conn, cancel context.CancelFunc) net.Conn {
	if pc, ok := conn.(net.PacketConn); ok {
		return &cancelPacketConn{Conn: conn, pc: pc, cancel: cancel}
	}
	return &cancelConn{Conn: conn, cancel: cancel}
}

type cancelConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c *cancelConn) Close() error {
	err := c.Conn.Close()
	c.cancel()
	return err
}

// CloseWrite forwards the half-close to the underlying conn when it
// supports it, so wrapping does not hide the capability from
// relay code (e.g. xnet.Transport).
func (c *cancelConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errHalfCloseNotSupported
}

func (c *cancelConn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return errHalfCloseNotSupported
}

type cancelPacketConn struct {
	net.Conn
	pc     net.PacketConn
	cancel context.CancelFunc
}

func (c *cancelPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	return c.pc.ReadFrom(b)
}

func (c *cancelPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	return c.pc.WriteTo(b, addr)
}

func (c *cancelPacketConn) Close() error {
	err := c.Conn.Close()
	c.cancel()
	return err
}

// cancelOnCloseListener is the Bind counterpart of cancelOnCloseConn:
// the successful attempt's context is released when the listener is
// closed (D3-07).
type cancelOnCloseListener struct {
	net.Listener
	cancel context.CancelFunc
}

func (l *cancelOnCloseListener) Close() error {
	err := l.Listener.Close()
	l.cancel()
	return err
}

type packetConn struct {
	net.Conn
}

func (c *packetConn) ReadFrom(b []byte) (n int, addr net.Addr, err error) {
	n, err = c.Read(b)
	addr = c.Conn.RemoteAddr()
	return
}

func (c *packetConn) WriteTo(b []byte, addr net.Addr) (n int, err error) {
	return c.Write(b)
}
