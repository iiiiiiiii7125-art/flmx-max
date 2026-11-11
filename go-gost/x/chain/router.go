package chain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/recorder"
	"github.com/go-gost/core/selector"
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

// minAttemptSlice is the D1 merge floor: when the even share of the
// remaining envelope drops below it, the current attempt takes the
// whole remainder and is the last one.
const minAttemptSlice = 300 * time.Millisecond

// retryBackoff returns the sleep between attempts: 200ms after the
// first failure, 400ms after the second and every later one, with
// ±20% jitter so concurrent dials do not retry in lockstep.
func retryBackoff(interval int) time.Duration {
	base := 400 * time.Millisecond
	if interval <= 0 {
		base = 200 * time.Millisecond
	}
	return time.Duration(float64(base) * (0.8 + 0.4*rand.Float64()))
}

// sleepRetryBackoff sleeps for the interval's backoff, truncated to
// the envelope time left before deadline (the backoff is charged to
// the envelope, D1), and aborts early with the parent context's error
// when the caller has given up (D6).
func sleepRetryBackoff(parent context.Context, interval int, deadline time.Time) error {
	d := retryBackoff(interval)
	if !deadline.IsZero() {
		if rem := time.Until(deadline); rem < d {
			d = rem
		}
	}
	if d < 0 {
		d = 0
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-parent.Done():
		return parent.Err()
	case <-t.C:
		return nil
	}
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

// attemptContext is a promotable attempt deadline context (B-01).
// While the attempt is dialling, its deadline is the attempt slice,
// so a blackhole attempt is still cut at the slice. When the attempt
// succeeds, promote moves the deadline out to the envelope end: a
// context-retaining transport built over the returned conn then
// observes a context that lives until min(conn Close, envelope end)
// instead of dying at the slice boundary (with N=3 the slice was a
// third of the baseline's full-timeout context). Cancellation by the
// owner (cancelOnCloseConn via the returned CancelFunc) and parent
// cancellation behave exactly as with context.WithTimeout.
type attemptContext struct {
	parent context.Context
	done   chan struct{}

	mu         sync.Mutex
	deadline   time.Time
	err        error
	timer      *time.Timer
	stopParent func() bool
}

func newAttemptContext(parent context.Context, timeout time.Duration) (context.Context, *attemptContext, context.CancelFunc) {
	c := &attemptContext{
		parent:   parent,
		done:     make(chan struct{}),
		deadline: time.Now().Add(timeout),
	}
	c.timer = time.AfterFunc(timeout, func() { c.finish(context.DeadlineExceeded) })
	c.stopParent = context.AfterFunc(parent, func() { c.finish(parent.Err()) })
	return c, c, c.cancel
}

func (c *attemptContext) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
	if c.timer != nil {
		c.timer.Stop()
	}
	if c.stopParent != nil {
		c.stopParent()
	}
}

func (c *attemptContext) cancel() { c.finish(context.Canceled) }

// promote extends the deadline to the given later time. It is a
// no-op once the context has ended or when the new deadline is not
// later than the current one.
func (c *attemptContext) promote(deadline time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || !deadline.After(c.deadline) {
		return
	}
	c.deadline = deadline
	c.timer.Stop()
	c.timer = time.AfterFunc(time.Until(deadline), func() { c.finish(context.DeadlineExceeded) })
}

func (c *attemptContext) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deadline, true
}

func (c *attemptContext) Done() <-chan struct{} { return c.done }

func (c *attemptContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *attemptContext) Value(key any) any { return c.parent.Value(key) }

// markerJournal tracks the fail markers of every node this call may
// dial, so the D2 fallback reselection can undo exactly the marks
// this call itself added (A-01). The hop layer filters candidates
// through FailFilter on those markers: when every candidate failed
// earlier in this same call, peeling the advisory exclude list alone
// still left every candidate filtered out, and the fallback died as
// an empty route instead of redialling a failed candidate.
//
// A node's snapshot is taken at first sight — before the first
// attempt that could mark it — which is equivalent to a snapshot at
// the start of the call for every mark this call can make: marks are
// only written by route dial/bind attempts, and every attempt's path
// is observed before it runs. rollback restores the snapshots right
// before a fallback reselection; settle, deferred to the end of the
// call, re-applies this call's failure marks so the cross-connection
// FailFilter state matches the pre-retry-engine semantics (a node
// that failed attempts in this call ends marked once per failed
// attempt; a node on the successful attempt's path is left to the
// reset its own success already performed).
type markerJournal struct {
	snaps      map[selector.Marker]int64
	failed     map[selector.Marker]int
	succeeded  map[selector.Marker]bool
	rolledBack bool
}

func newMarkerJournal() *markerJournal {
	return &markerJournal{
		snaps:     make(map[selector.Marker]int64),
		failed:    make(map[selector.Marker]int),
		succeeded: make(map[selector.Marker]bool),
	}
}

func (j *markerJournal) observe(route chain.Route) {
	for _, node := range routePath(route) {
		if node == nil {
			continue
		}
		if m := node.Marker(); m != nil {
			if _, ok := j.snaps[m]; !ok {
				j.snaps[m] = m.Count()
			}
		}
	}
}

func (j *markerJournal) noteFailure(route chain.Route) {
	seen := make(map[selector.Marker]bool)
	for _, node := range routePath(route) {
		if node == nil {
			continue
		}
		if m := node.Marker(); m != nil && !seen[m] {
			seen[m] = true
			j.failed[m]++
		}
	}
}

func (j *markerJournal) noteSuccess(route chain.Route) {
	for _, node := range routePath(route) {
		if node == nil {
			continue
		}
		if m := node.Marker(); m != nil {
			j.succeeded[m] = true
		}
	}
}

func (j *markerJournal) rollback() {
	for m, snap := range j.snaps {
		if m.Count() != snap {
			m.Reset()
			for i := int64(0); i < snap; i++ {
				m.Mark()
			}
		}
	}
	j.rolledBack = true
}

func (j *markerJournal) settle() {
	if !j.rolledBack {
		return
	}
	for m, fails := range j.failed {
		if j.succeeded[m] {
			continue
		}
		for target := j.snaps[m] + int64(fails); m.Count() < target; {
			m.Mark()
		}
	}
}

func (r *Router) dial(ctx context.Context, network, address string, log logger.Logger) (conn net.Conn, err error) {
	count := r.options.Retries + 1
	if count <= 0 {
		count = 1
	}

	log.Debugf("dial %s/%s", address, network)

	parent := ctx

	// D1 total envelope: all attempts and backoffs of this call share
	// one deadline, t0+options.Timeout. Each attempt's cap is the
	// remaining envelope divided by the remaining attempt count, so a
	// fast failure carries its unused share forward.
	var deadline time.Time
	if r.options.Timeout > 0 {
		deadline = time.Now().Add(r.options.Timeout)
	}

	// Nodes that fail an attempt join the exclude list for the next
	// one (D3-04): without this, every retry re-selected the node
	// that had just failed. The hop layer honours the list via
	// ctxvalue.ExcludeNodesFromContext; any list the caller already
	// supplied is kept and extended.
	exclude := ctxvalue.ExcludeNodesFromContext(ctx)
	// D2: the caller-supplied part of exclude is hard and is never
	// stripped; entries this call appends itself (ownExclude, the
	// tail beyond hardExclude) are advisory and may be peeled off by
	// the fallback reselection in selectRoute.
	hardExclude := append([]string(nil), exclude...)

	// A-01: journal the fail markers of every node this call dials;
	// the D2 fallback rolls this call's own marks back before
	// reselecting, and settle re-applies them when the call ends.
	journal := newMarkerJournal()
	defer journal.settle()

	for i := 0; i < count; i++ {
		// D1/D6: a cancelled parent ends the call with the parent's
		// error; an attempt's own timeout does not (parent still nil).
		if perr := parent.Err(); perr != nil {
			err = perr
			return nil, err
		}

		last := i == count-1

		ctx := parent
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
		// B-01: the attempt context is promotable — its deadline is
		// the attempt slice while dialling, and is promoted to the
		// envelope deadline on success (see attemptContext).
		var cancel context.CancelFunc
		var actx *attemptContext
		if r.options.Timeout > 0 {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				if err == nil {
					err = context.DeadlineExceeded
				}
				return nil, err
			}
			slice := remaining / time.Duration(count-i)
			if slice < minAttemptSlice {
				// Merge floor: take the whole remainder, last attempt.
				slice = remaining
				last = true
			}
			ctx, actx, cancel = newAttemptContext(ctx, slice)
		}

		buf := ctxvalue.BufferFromContext(ctx)
		if buf != nil {
			buf.Reset()
		}

		var ipAddr string
		ipAddr, err = xnet.Resolve(ctx, "ip", address, r.options.Resolver, r.options.HostMapper, log)
		if err != nil {
			// D3: a router-level resolve failure joins the retry
			// loop with the same shape as a dial failure instead of
			// breaking out of it.
			log.Error(err)
			if cancel != nil {
				cancel()
			}
			if perr := parent.Err(); perr != nil {
				err = perr
				return nil, err
			}
			if last {
				break
			}
			if serr := sleepRetryBackoff(parent, i, deadline); serr != nil {
				err = serr
				return nil, err
			}
			continue
		}

		// Dial-point re-validation (A02-001): whatever resolution
		// produced ipAddr (service resolver, hosts mapper, or none —
		// in which case ipAddr is still a hostname the route's dialer
		// would resolve a second time), the guard validates the final
		// target and pins hostnames to a validated IP literal, so the
		// address that is connected to is the address that was judged.
		// D6: a guard verdict is deterministic for this call, so it
		// short-circuits ahead of all retry semantics.
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

		// D2 selectRoute: select with the full exclude list; if that
		// yields no route and this call has advisory ownExclude
		// entries, peel them off and reselect once with only the hard
		// caller exclude. Multi-candidate attempts still see the full
		// list first, so unfailed candidates stay preferred (D3-04).
		fallbackUsed := false
		selectRoute := func(ex []string) (chain.Route, context.Context) {
			sctx := ctxvalue.ContextWithExcludeNodes(ctx, ex)
			rt := r.options.Chain.Route(sctx, network, ipAddr, chain.WithHostRouteOption(address))
			if (rt == nil || len(rt.Nodes()) == 0) && !fallbackUsed && len(exclude) > len(hardExclude) {
				fallbackUsed = true
				// A-01: peeling the exclude list is not enough on
				// its own — the hop's FailFilter would still drop
				// every candidate this call itself marked. Roll
				// this call's marks back so the reselection sees
				// the pre-call filter state; settle re-applies
				// them when the call ends.
				journal.rollback()
				sctx = ctxvalue.ContextWithExcludeNodes(ctx, hardExclude)
				rt = r.options.Chain.Route(sctx, network, ipAddr, chain.WithHostRouteOption(address))
			}
			return rt, sctx
		}

		var route chain.Route
		routeCtx := ctx
		if r.options.Chain != nil {
			route, routeCtx = selectRoute(exclude)
			if route == nil || len(route.Nodes()) == 0 {
				// Fail closed (D1-H4): a configured chain that yields no
				// route must not fall back to a direct dial. Mirrors Bind.
				// D6: still empty after the D2 fallback — no further
				// attempts, the candidate set is genuinely empty.
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
		journal.observe(route)
		conn, err = route.Dial(routeCtx, network, ipAddr,
			chain.InterfaceDialOption(r.options.IfceName),
			chain.NetnsDialOption(r.options.Netns),
			chain.SockOptsDialOption(r.options.SockOpts),
			chain.LoggerDialOption(log),
		)
		if err == nil {
			journal.noteSuccess(route)
			if actx != nil {
				// B-01: the successful attempt's context now lives
				// to the envelope end (or conn Close, via cancel).
				actx.promote(deadline)
			}
			if cancel != nil {
				conn = cancelOnCloseConn(conn, cancel)
			}
			break
		}
		if cancel != nil {
			cancel()
		}
		journal.noteFailure(route)
		for _, node := range routePath(route) {
			exclude = append(exclude, node.Name, node.Addr)
		}
		log.Errorf("route(retry=%d) %s", i, err)
		if perr := parent.Err(); perr != nil {
			err = perr
			return nil, err
		}
		if last {
			break
		}
		if serr := sleepRetryBackoff(parent, i, deadline); serr != nil {
			err = serr
			return nil, err
		}
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

	parent := ctx

	// D1 total envelope, same skeleton as dial: all attempts and
	// backoffs share one deadline, t0+options.Timeout, and each
	// attempt's cap is the remaining envelope over the remaining
	// attempt count. (Listener routers do not read retries, so with
	// the usual single attempt this is exactly today's behaviour.)
	var deadline time.Time
	if r.options.Timeout > 0 {
		deadline = time.Now().Add(r.options.Timeout)
	}

	// Same retry-exclusion contract as dial (D3-04).
	exclude := ctxvalue.ExcludeNodesFromContext(ctx)
	// D2: caller-supplied exclude is hard; entries appended by this
	// call itself are advisory (see dial).
	hardExclude := append([]string(nil), exclude...)

	// A-01 marker journal, same contract as dial.
	journal := newMarkerJournal()
	defer journal.settle()

	for i := 0; i < count; i++ {
		if perr := parent.Err(); perr != nil {
			err = perr
			return nil, err
		}

		last := i == count-1

		ctx := parent
		if len(exclude) > 0 {
			ctx = ctxvalue.ContextWithExcludeNodes(ctx, exclude)
		}
		// Explicit per-attempt context management, same contract as
		// dial (D3-07): failed attempts release their context at the
		// end of the iteration; a successful attempt's context lives
		// exactly as long as the returned listener.
		// B-01: promotable attempt context, same as dial.
		var cancel context.CancelFunc
		var actx *attemptContext
		if r.options.Timeout > 0 {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				if err == nil {
					err = context.DeadlineExceeded
				}
				return nil, err
			}
			slice := remaining / time.Duration(count-i)
			if slice < minAttemptSlice {
				slice = remaining
				last = true
			}
			ctx, actx, cancel = newAttemptContext(ctx, slice)
		}

		// D2 selectRoute, same contract as dial: full exclude first,
		// one reselection with only the hard caller exclude when the
		// first yields no route and ownExclude is non-empty.
		fallbackUsed := false
		selectRoute := func(ex []string) (chain.Route, context.Context) {
			sctx := ctxvalue.ContextWithExcludeNodes(ctx, ex)
			rt := r.options.Chain.Route(sctx, network, address)
			if (rt == nil || len(rt.Nodes()) == 0) && !fallbackUsed && len(exclude) > len(hardExclude) {
				fallbackUsed = true
				// A-01: roll this call's own fail marks back so
				// FailFilter does not veto the reselection (see dial).
				journal.rollback()
				sctx = ctxvalue.ContextWithExcludeNodes(ctx, hardExclude)
				rt = r.options.Chain.Route(sctx, network, address)
			}
			return rt, sctx
		}

		var route chain.Route
		routeCtx := ctx
		if r.options.Chain != nil {
			route, routeCtx = selectRoute(exclude)
			if route == nil || len(route.Nodes()) == 0 {
				// D6: still empty after the D2 fallback — fail
				// closed immediately, no further attempts.
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
		journal.observe(route)
		ln, err = route.Bind(routeCtx, network, address, opts...)
		if err == nil {
			journal.noteSuccess(route)
			if actx != nil {
				actx.promote(deadline)
			}
			if cancel != nil {
				ln = &cancelOnCloseListener{Listener: ln, cancel: cancel}
			}
			break
		}
		if cancel != nil {
			cancel()
		}
		journal.noteFailure(route)
		for _, node := range routePath(route) {
			exclude = append(exclude, node.Name, node.Addr)
		}
		log.Errorf("route(retry=%d) %s", i, err)
		if perr := parent.Err(); perr != nil {
			err = perr
			return nil, err
		}
		if last {
			break
		}
		if serr := sleepRetryBackoff(parent, i, deadline); serr != nil {
			err = serr
			return nil, err
		}
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
