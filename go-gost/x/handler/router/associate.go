package router

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gost/core/common/bufpool"
	"github.com/go-gost/core/limiter"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/observer/stats"
	"github.com/go-gost/core/router"
	"github.com/go-gost/core/sd"
	"github.com/go-gost/relay"
	xip "github.com/go-gost/x/internal/net/ip"
	"github.com/go-gost/x/internal/util/cache"
	traffic_wrapper "github.com/go-gost/x/limiter/traffic/wrapper"
	stats_wrapper "github.com/go-gost/x/observer/stats/wrapper"
	"github.com/go-gost/x/registry"
	"github.com/google/uuid"
	"github.com/songgao/water/waterutil"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func (h *routerHandler) handleAssociate(ctx context.Context, conn net.Conn, network, host string, routerID relay.TunnelID, log logger.Logger) (err error) {
	log = log.WithFields(map[string]any{
		"dst":    fmt.Sprintf("%s/%s", host, network),
		"cmd":    "associate",
		"router": routerID.String(),
		"host":   host,
	})

	resp := relay.Response{
		Version: relay.Version1,
		Status:  relay.StatusOK,
	}

	if ingress := h.md.ingress; ingress != nil && host != "" {
		var rid relay.TunnelID
		if rule := ingress.GetRule(ctx, host); rule != nil {
			rid = parseRouterID(rule.Endpoint)
		}

		if !rid.Equal(routerID) {
			resp.Status = relay.StatusHostUnreachable
			resp.WriteTo(conn)
			err := fmt.Errorf("no route to host %s", host)
			log.Error(err)
			return err
		}
	}

	uuid, err := uuid.NewRandom()
	if err != nil {
		resp.Status = relay.StatusInternalServerError
		resp.WriteTo(conn)
		return
	}
	connectorID := relay.NewConnectorID(uuid[:])

	resp.Features = append(resp.Features,
		&relay.TunnelFeature{
			ID: connectorID,
		},
	)
	resp.WriteTo(conn)

	conn = &packetConn{Conn: conn}

	// F08: track bidirectional progress of this association. The
	// read loop below and the ConnectorPool writer both run over
	// this conn, so wrapping it here makes reads (packets from the
	// connector) and writes (packets routed to the connector)
	// count as progress alike.
	act := &associateActivity{}
	act.touch()
	conn = &activityConn{Conn: conn, act: act}

	clientID := fmt.Sprintf("%s@%s", host, routerID)
	var stats stats.Stats
	if h.stats != nil {
		stats = h.stats.Stats(clientID)
	}
	conn = stats_wrapper.WrapConn(conn, stats)
	conn = traffic_wrapper.WrapConn(
		conn,
		h.limiter,
		clientID,
		limiter.ScopeOption(limiter.ScopeClient),
		limiter.ServiceOption(h.options.Service),
		limiter.ClientOption(clientID),
		limiter.NetworkOption(network),
		limiter.SrcOption(conn.RemoteAddr().String()),
	)

	h.pool.Add(routerID, NewConnector(routerID, connectorID, host, LockWriter(conn), &ConnectorOptions{}))
	defer h.pool.Del(routerID, host, connectorID)

	if h.md.sd != nil {
		err := h.md.sd.Register(ctx, &sd.Service{
			ID:      connectorID.String(),
			Name:    clientID,
			Node:    h.id,
			Network: "udp",
			Address: h.md.entryPoint,
		})
		if err != nil {
			h.log.Error(err)
		}

		defer h.md.sd.Deregister(ctx, &sd.Service{
			ID:   connectorID.String(),
			Name: clientID,
			Node: h.id,
		})

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		go h.sdRenew(ctx, clientID, connectorID.String())
	}

	log.Debugf("%s/%s: router=%s, connector=%s, weight=%d established", host, network, routerID, connectorID, connectorID.Weight())

	// F08: this read loop is the association's only lifecycle
	// owner, and it previously blocked in Read with no deadline
	// at all (the handler cleared the handshake deadline before
	// dispatch). A connector whose application died while its
	// kernel kept the tunnel up therefore never returned: its
	// ConnectorPool entry stayed routable forever and sdRenew
	// kept renewing its SD registration without bound. The loop
	// now arms the read deadline at lastProgress+window before
	// every read (the FIX-1 transport discipline; packetConn may
	// serve a buffered remainder without blocking, in which case
	// the armed deadline simply applies to the next real read),
	// and a timeout wake ends the association only when neither
	// direction has moved a packet for the full window. Returning
	// is what reaps the rest: the deferred pool.Del drops the
	// pool entry and the deferred cancel stops sdRenew.
	defer conn.SetReadDeadline(time.Time{})

	var b [MaxMessageSize]byte
	for {
		_ = conn.SetReadDeadline(act.deadline(associateIdleTimeout))
		n, err := conn.Read(b[:])
		if err != nil {
			if isTimeoutErr(err) {
				if act.stalled(associateIdleTimeout) {
					return errAssociateIdleTimeout
				}
				continue
			}
			if err == io.EOF {
				return nil
			}
			return err
		}

		h.handlePacket(ctx, b[:n], routerID, log)
	}
}

// associateIdleTimeout is the no-progress window for one router
// association. It matches the FIX-1 transport window (300s): the
// criterion is absence of progress in either direction, not age,
// so a connector exchanging even a trickle of packets stays
// associated indefinitely. A variable only so tests can shrink
// the window.
var associateIdleTimeout = 300 * time.Second

var errAssociateIdleTimeout = errors.New("router: association idle timeout, no progress in either direction")

// associateActivity is the shared bidirectional progress clock of
// one association: an atomic timestamp, no goroutine. The
// per-packet cost is one deadline arm plus one atomic store, the
// arming discipline the FIX-1 transport applies per read segment.
type associateActivity struct {
	last atomic.Int64 // unix nano of the last successful read or write
}

func (a *associateActivity) touch() {
	a.last.Store(time.Now().UnixNano())
}

func (a *associateActivity) deadline(window time.Duration) time.Time {
	return time.Unix(0, a.last.Load()).Add(window)
}

func (a *associateActivity) stalled(window time.Duration) bool {
	return time.Since(time.Unix(0, a.last.Load())) >= window
}

// activityConn records successful reads and writes on the
// association connection as progress.
type activityConn struct {
	net.Conn
	act *associateActivity
}

func (c *activityConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.act.touch()
	}
	return n, err
}

func (c *activityConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.act.touch()
	}
	return n, err
}

func isTimeoutErr(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (h *routerHandler) sdRenew(ctx context.Context, clientID string, connectorID string) {
	tc := time.NewTicker(h.md.sdRenewInterval)
	defer tc.Stop()

	for {
		select {
		case <-tc.C:
			h.md.sd.Renew(ctx, &sd.Service{
				ID:   connectorID,
				Name: clientID,
				Node: h.id,
			})
		case <-ctx.Done():
			return
		}
	}
}

func (h *routerHandler) handlePacket(ctx context.Context, data []byte, routerID relay.TunnelID, log logger.Logger) error {
	var dstIP net.IP
	if waterutil.IsIPv4(data) {
		header, err := ipv4.ParseHeader(data)
		if err != nil {
			return err
		}

		dstIP = header.Dst

		if log.IsLevelEnabled(logger.TraceLevel) {
			log.Tracef("%s >> %s %-4s %d/%-4d %-4x %d",
				header.Src, header.Dst, xip.Protocol(waterutil.IPv4Protocol(data)),
				header.Len, header.TotalLen, header.ID, header.Flags)
		}
	} else if waterutil.IsIPv6(data) {
		header, err := ipv6.ParseHeader(data)
		if err != nil {
			return err
		}

		dstIP = header.Dst

		if log.IsLevelEnabled(logger.TraceLevel) {
			log.Tracef("%s >> %s %s %d %d",
				header.Src, header.Dst,
				xip.Protocol(waterutil.IPProtocol(header.NextHeader)),
				header.PayloadLen, header.TrafficClass)
		}
	} else {
		return fmt.Errorf("unknown packet, discarded(%d)", len(data))
	}

	rid := routerID.String()

	route := h.getRoute(ctx, rid, dstIP.String())
	if route == nil || route.Gateway == "" {
		// no route to host, discard
		return fmt.Errorf("no route to host %s", dstIP)
	}

	if log.IsLevelEnabled(logger.TraceLevel) {
		log.Tracef("route for %s: %s -> %s", dstIP, route.Dst, route.Gateway)
	}

	if c := h.pool.Get(routerID, route.Gateway); c != nil {
		if w := c.Writer(); w != nil {
			w.Write(data)
		}
		return nil
	}

	raddr := h.getAddrforRoute(ctx, rid, route.Gateway)
	if raddr == nil {
		return nil
	}

	req := &relay.Request{
		Version: relay.Version1,
		Cmd:     relay.CmdAssociate,
		Features: []relay.Feature{
			&relay.TunnelFeature{
				ID: routerID,
			},
			&relay.AddrFeature{
				AType: relay.AddrDomain,
				Host:  route.Gateway,
			},
		},
	}

	buf := bytes.Buffer{}
	req.WriteTo(&buf)
	buf.Write(data)

	h.epConn.WriteTo(buf.Bytes(), raddr)

	return nil
}

func (h *routerHandler) getRoute(ctx context.Context, rid string, dst string) *router.Route {
	if h.md.routerCacheEnabled {
		if item := h.routeCache.Get(dst); item != nil && !item.Expired() {
			v, _ := item.Value().(*router.Route)
			return v
		}
	}

	var route *router.Route
	if r := registry.RouterRegistry().Get(rid); r != nil {
		route = r.GetRoute(ctx, dst, router.IDOption(rid))
	}
	if route == nil && h.md.router != nil {
		route = h.md.router.GetRoute(ctx, dst, router.IDOption(rid))
	}

	if h.md.routerCacheEnabled {
		h.routeCache.Set(dst, cache.NewItem(route, h.md.routerCacheExpiration))
	}
	return route
}

func (h *routerHandler) getAddrforRoute(ctx context.Context, routerID, gateway string) net.Addr {
	if h.md.sd == nil {
		return nil
	}
	clientID := fmt.Sprintf("%s@%s", gateway, routerID)

	if item := h.sdCache.Get(clientID); item != nil && !item.Expired() {
		addr, _ := item.Value().(net.Addr)
		return addr
	}

	ss, _ := h.md.sd.Get(ctx, clientID)

	service := &sd.Service{}
	for _, s := range ss {
		if s.Node != h.id {
			service = s
			break
		}
	}
	raddr, _ := net.ResolveUDPAddr("udp", service.Address)
	h.sdCache.Set(clientID, cache.NewItem(raddr, h.md.sdCacheExpiration))

	return raddr
}

type packetConn struct {
	net.Conn
	rbuf []byte // unread remainder of the current frame
}

func (c *packetConn) Read(b []byte) (n int, err error) {
	if len(c.rbuf) > 0 {
		n = copy(b, c.rbuf)
		c.rbuf = c.rbuf[n:]
		return n, nil
	}

	var bb [2]byte
	_, err = io.ReadFull(c.Conn, bb[:])
	if err != nil {
		return
	}

	dlen := int(binary.BigEndian.Uint16(bb[:]))
	if len(b) >= dlen {
		return io.ReadFull(c.Conn, b[:dlen])
	}

	buf := bufpool.Get(dlen)
	defer bufpool.Put(buf)

	_, err = io.ReadFull(c.Conn, buf)
	if err != nil {
		return
	}
	n = copy(b, buf[:dlen])
	c.rbuf = append(c.rbuf, buf[n:dlen]...)

	return
}

func (c *packetConn) Write(b []byte) (n int, err error) {
	if len(b) > math.MaxUint16 {
		err = errors.New("write: data maximum exceeded")
		return
	}

	buf := bufpool.Get(len(b) + 2)
	defer bufpool.Put(buf)

	binary.BigEndian.PutUint16(buf[:2], uint16(len(b)))
	n = copy(buf[2:], b)

	return c.Conn.Write(buf)
}

type lockWriter struct {
	w  io.Writer
	mu sync.Mutex
}

func LockWriter(w io.Writer) io.Writer {
	return &lockWriter{w: w}
}

func (w *lockWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.w.Write(p)
}

func (w *lockWriter) Close() error {
	if closer, ok := w.w.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
