package v5

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/go-gost/core/limiter"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/observer/stats"
	"github.com/go-gost/gosocks5"
	ctxvalue "github.com/go-gost/x/ctx"
	xnet "github.com/go-gost/x/internal/net"
	"github.com/go-gost/x/internal/net/udp"
	"github.com/go-gost/x/internal/util/socks"
	traffic_wrapper "github.com/go-gost/x/limiter/traffic/wrapper"
	xstats "github.com/go-gost/x/observer/stats"
	stats_wrapper "github.com/go-gost/x/observer/stats/wrapper"
	xrecorder "github.com/go-gost/x/recorder"
)

// udpAssociationIdleTimeout is the no-progress window the UDP
// relay enforces for one association (F07); the control
// connection's lifetime is bounded through it (F06). A variable
// only so tests can shrink the window — production keeps the
// FIX-1-aligned 300s.
var udpAssociationIdleTimeout = 300 * time.Second

func (h *socks5Handler) handleUDP(ctx context.Context, conn net.Conn, ro *xrecorder.HandlerRecorderObject, log logger.Logger) error {
	log = log.WithFields(map[string]any{
		"cmd": "udp",
	})

	if !h.md.enableUDP {
		reply := gosocks5.NewReply(gosocks5.NotAllowed, nil)
		log.Trace(reply)
		log.Error("socks5: UDP relay is disabled")
		return reply.Write(conn)
	}

	lc := xnet.ListenConfig{
		Netns: h.options.Netns,
	}
	// The local address is normally TCP, but a wrapped/UDP/TUN
	// connection can present another type; the unchecked assertion
	// here used to panic and kill the whole agent (D3-02).
	tcpAddr, ok := conn.LocalAddr().(*net.TCPAddr)
	if !ok {
		err := fmt.Errorf("socks5: unexpected local address type %T", conn.LocalAddr())
		log.Error(err)
		reply := gosocks5.NewReply(gosocks5.Failure, nil)
		log.Trace(reply)
		reply.Write(conn)
		return err
	}
	laddr := &net.UDPAddr{IP: tcpAddr.IP, Port: 0} // use out-going interface's IP
	cc, err := lc.ListenPacket(ctx, "udp", laddr.String())
	if err != nil {
		log.Error(err)
		reply := gosocks5.NewReply(gosocks5.Failure, nil)
		log.Trace(reply)
		reply.Write(conn)
		return err
	}
	defer cc.Close()

	saddr := gosocks5.Addr{}
	saddr.ParseFrom(cc.LocalAddr().String())
	reply := gosocks5.NewReply(gosocks5.Succeeded, &saddr)
	log.Trace(reply)
	if err := reply.Write(conn); err != nil {
		log.Error(err)
		return err
	}

	log = log.WithFields(map[string]any{
		"bind": fmt.Sprintf("%s/%s", cc.LocalAddr(), cc.LocalAddr().Network()),
	})
	log.Debugf("bind on %s OK", cc.LocalAddr())

	// obtain a udp connection
	var buf bytes.Buffer
	c, err := h.options.Router.Dial(ctxvalue.ContextWithBuffer(ctx, &buf), "udp", "") // UDP association
	ro.Route = buf.String()
	if err != nil {
		log.Error(err)
		return err
	}
	defer c.Close()

	pc, ok := c.(net.PacketConn)
	if !ok {
		err := errors.New("socks5: wrong connection type")
		log.Error(err)
		return err
	}

	pStats := xstats.Stats{}
	cc = stats_wrapper.WrapPacketConn(cc, &pStats)

	defer func() {
		ro.InputBytes = pStats.Get(stats.KindInputBytes)
		ro.OutputBytes = pStats.Get(stats.KindOutputBytes)
	}()

	{
		clientID := ctxvalue.ClientIDFromContext(ctx)
		cc = traffic_wrapper.WrapPacketConn(
			cc,
			h.limiter,
			string(clientID),
			limiter.ServiceOption(h.options.Service),
			limiter.ScopeOption(limiter.ScopeClient),
			limiter.NetworkOption("udp"),
			limiter.ClientOption(string(clientID)),
			limiter.SrcOption(conn.RemoteAddr().String()),
		)
		if h.options.Observer != nil {
			pstats := h.stats.Stats(string(clientID))
			pstats.Add(stats.KindTotalConns, 1)
			pstats.Add(stats.KindCurrentConns, 1)
			defer pstats.Add(stats.KindCurrentConns, -1)
			cc = stats_wrapper.WrapPacketConn(cc, pstats)
		}
	}

	r := udp.NewRelay(socks.UDPConn(cc), pc).
		WithBypass(h.options.Bypass).
		WithLogger(log).
		WithIdleTimeout(udpAssociationIdleTimeout)

	// F06: RFC 1928 ties the association's lifetime to the TCP
	// control connection, and the drain below (io.Copy) returns
	// when the peer closes or resets it. That alone was not
	// enough: a client whose application died while its kernel
	// kept the connection up (no FIN, no RST, no data) pinned the
	// handler, the control connection, the UDP socket and the
	// relay goroutines forever, because nothing else ever ended
	// the wait. The relay now ends it: Run returns when the
	// association is over — an endpoint error, or the F07
	// no-progress timeout after a full window of silence on the
	// UDP leg — and closing the control connection unblocks the
	// drain immediately, so the deferred closes reap the whole
	// association. Progress on either leg keeps the association
	// alive; the criterion is bidirectional progress, not
	// connection age. No new goroutine: the relay goroutine is
	// the one that has always run here.
	go func() {
		r.Run(ctx)
		conn.Close()
	}()

	t := time.Now()
	log.Debugf("%s <-> %s", conn.RemoteAddr(), cc.LocalAddr())
	io.Copy(io.Discard, conn)
	log.WithFields(map[string]any{"duration": time.Since(t)}).
		Debugf("%s >-< %s", conn.RemoteAddr(), cc.LocalAddr())

	return nil
}
