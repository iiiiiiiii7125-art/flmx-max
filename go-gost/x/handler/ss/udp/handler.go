package ss

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/logger"
	md "github.com/go-gost/core/metadata"
	"github.com/go-gost/core/recorder"
	ctxvalue "github.com/go-gost/x/ctx"
	"github.com/go-gost/x/internal/idle"
	netudp "github.com/go-gost/x/internal/net/udp"
	"github.com/go-gost/x/internal/util/eventlog"
	"github.com/go-gost/x/internal/util/relay"
	"github.com/go-gost/x/internal/util/ss"
	rate_limiter "github.com/go-gost/x/limiter/rate"
	xrecorder "github.com/go-gost/x/recorder"
	"github.com/go-gost/x/registry"
	"github.com/shadowsocks/go-shadowsocks2/core"
)

func init() {
	registry.HandlerRegistry().Register("ssu", NewHandler)
}

type ssuHandler struct {
	cipher   core.Cipher
	md       metadata
	options  handler.Options
	recorder recorder.RecorderObject
}

func NewHandler(opts ...handler.Option) handler.Handler {
	options := handler.Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &ssuHandler{
		options: options,
	}
}

func (h *ssuHandler) Init(md md.Metadata) (err error) {
	if err = h.parseMetadata(md); err != nil {
		return
	}

	// Fail closed (D1-H1): without auth there is no cipher, and accepting
	// traffic without a cipher means plaintext. Refuse to initialize.
	if h.options.Auth == nil {
		return errors.New("shadowsocks: auth (method/password) is required, refusing to run unencrypted")
	}
	{
		method := h.options.Auth.Username()
		password, _ := h.options.Auth.Password()
		h.cipher, err = ss.ShadowCipher(method, password, h.md.key)
		if err != nil {
			return
		}
		if h.cipher == nil {
			return errors.New("shadowsocks: failed to create cipher for method " + method)
		}
	}

	for _, ro := range h.options.Recorders {
		if ro.Record == xrecorder.RecorderServiceHandler {
			h.recorder = ro
			break
		}
	}

	return
}

func (h *ssuHandler) Handle(ctx context.Context, conn net.Conn, opts ...handler.HandleOption) (err error) {
	defer conn.Close()

	start := time.Now()

	ro := &xrecorder.HandlerRecorderObject{
		Service:    h.options.Service,
		Network:    "udp",
		RemoteAddr: conn.RemoteAddr().String(),
		LocalAddr:  conn.LocalAddr().String(),
		Time:       start,
		SID:        string(ctxvalue.SidFromContext(ctx)),
	}
	ro.ClientIP, _, _ = net.SplitHostPort(conn.RemoteAddr().String())

	log := h.options.Logger.WithFields(map[string]any{
		"remote": conn.RemoteAddr().String(),
		"local":  conn.LocalAddr().String(),
		"sid":    ctxvalue.SidFromContext(ctx),
	})

	log.Infof("%s <> %s", conn.RemoteAddr(), conn.LocalAddr())
	defer func() {
		if err != nil {
			ro.Err = err.Error()
		}
		ro.Duration = time.Since(start)
		ro.Record(ctx, h.recorder.Recorder)

		log.WithFields(map[string]any{
			"duration": time.Since(start),
		}).Infof("%s >< %s", conn.RemoteAddr(), conn.LocalAddr())
	}()

	if !h.checkRateLimit(conn.RemoteAddr()) {
		return rate_limiter.ErrRateLimit
	}

	pc, ok := conn.(net.PacketConn)
	if ok {
		if h.cipher != nil {
			pc = h.cipher.PacketConn(pc)
		}
		// standard UDP relay.
		pc = ss.UDPServerConn(pc, conn.RemoteAddr())
	} else {
		if h.cipher != nil {
			conn = ss.ShadowConn(h.cipher.StreamConn(conn), nil)
		}
		// UDP over TCP
		pc = relay.UDPTunServerConn(conn)
	}

	// obtain a udp connection
	var buf bytes.Buffer
	c, err := h.options.Router.Dial(ctxvalue.ContextWithBuffer(ctx, &buf), "udp", "") // UDP association
	ro.Route = buf.String()
	if err != nil {
		log.Error(err)
		return err
	}
	defer c.Close()

	cc, ok := c.(net.PacketConn)
	if !ok {
		err := errors.New("ss: wrong connection type")
		log.Error(err)
		return err
	}

	t := time.Now()
	log.Infof("%s <-> %s", conn.LocalAddr(), cc.LocalAddr())
	h.relayPacket(ctx, pc, cc, ro, log)
	log.WithFields(map[string]any{"duration": time.Since(t)}).
		Infof("%s >-< %s", conn.LocalAddr(), cc.LocalAddr())

	return nil
}

// dialTargetPinner mirrors bypass.DialTargetPinner: bypasses that
// can pin a hostname target to a validated IP literal at the dial
// point (A02-001). A shadowsocks UDP target may be a domain name;
// Bypass.Contains and the packet conn's WriteTo would resolve it
// independently, leaving a rebinding gap between check and dial.
type dialTargetPinner interface {
	PinDialTarget(ctx context.Context, network, addr string) (string, error)
}

// pinUDPTarget returns the address a packet must actually be
// written to: with a pinning bypass, a hostname target is replaced
// by its validated IP literal (an error drops the packet); anything
// else passes through unchanged. Successful pins are memoized in
// pins — one memo per relayPacket call, i.e. per UDP association
// (R2-A02-001, see netudp.PinMemo's scope rule: never shared across
// associations, failures never memoized).
func (h *ssuHandler) pinUDPTarget(ctx context.Context, pins *netudp.PinMemo, addr net.Addr) (net.Addr, error) {
	pinner, ok := h.options.Bypass.(dialTargetPinner)
	if !ok || addr == nil {
		return addr, nil
	}
	key := addr.String()
	if pins != nil {
		if pinned, hit := pins.Lookup(key); hit {
			return pinned, nil
		}
	}
	pinned, err := pinner.PinDialTarget(ctx, "udp", key)
	if err != nil {
		return nil, err
	}
	var out net.Addr
	if pinned == key {
		out = addr
	} else {
		ua, err := net.ResolveUDPAddr("udp", pinned)
		if err != nil {
			return nil, err
		}
		out = ua
	}
	if pins != nil {
		pins.Store(key, out)
	}
	return out, nil
}

func (h *ssuHandler) relayPacket(ctx context.Context, pc1, pc2 net.PacketConn, ro *xrecorder.HandlerRecorderObject, log logger.Logger) (err error) {
	// R2-A02-001: one pin memo per association (this relayPacket
	// call), shared by both directions of the same association only.
	pins := &netudp.PinMemo{}

	// F11: the bypass-drop and pin-failure warnings fire at packet
	// rate; aggregate them exactly like udp.Relay does (one
	// summary line per kind per window, flushed when the
	// association ends). The drop semantics do not change — only
	// the log shape does.
	events := eventlog.New(log, eventlog.DefaultInterval)
	bypassHits := events.DetailCounter(
		"ss udp relay: %d packet(s) dropped by bypass, last peer %s, %d total")
	pinErrs := events.DetailCounter(
		"ss udp relay: %d bypass pin failure(s), last: %s, %d total")
	defer events.Flush()

	// Rescan member of the F07 family: these two hand-rolled copy
	// loops had no no-progress bound at all. In UDP-over-TCP mode
	// pc1 is a stream conn, so a client whose application died
	// while its kernel kept the connection up blocked both
	// goroutines — and held the upstream association — forever.
	// Both directions now read under the shared bidirectional
	// progress window, exactly as udp.Relay enforces it (F07): a
	// direction that finds the whole association stalled closes
	// both endpoints and ends the relay.
	act := &ssRelayActivity{}
	act.touch()
	window := idle.Resolve(ssRelayIdleTimeout)
	var timedOut atomic.Bool

	errc := make(chan error, 2)

	go func() {
		var b [MaxMessageSize]byte
		for {
			err := func() error {
				n, addr, err := readSSPacket(pc1, b[:], act, window)
				if err != nil {
					if isSSTimeout(err) {
						// F2-19 attribution: unwind (close both
						// ends) either way, but only an armed AND
						// fully stalled window may be labelled idle
						// timeout (router/associate caliber). With
						// the window off, readSSPacket has already
						// passed a foreign timeout through verbatim;
						// return it as-is.
						pc1.Close()
						pc2.Close()
						if window > 0 && act.stalled(window) {
							timedOut.Store(true)
							return errSSRelayIdleTimeout
						}
						return err
					}
					return err
				}

				if ro.Host == "" {
					ro.Host = addr.String()
				}

				if h.options.Bypass != nil && h.options.Bypass.Contains(ctx, addr.Network(), addr.String()) {
					bypassHits.NoteDetail(addr.String())
					return nil
				}

				target, perr := h.pinUDPTarget(ctx, pins, addr)
				if perr != nil {
					pinErrs.NoteErr(perr)
					return nil
				}

				if _, err = pc2.WriteTo(b[:n], target); err != nil {
					return err
				}
				act.touch()

				log.Tracef("%s >>> %s data: %d",
					pc2.LocalAddr(), addr, n)
				return nil
			}()

			if err != nil {
				errc <- err
				return
			}
		}
	}()

	go func() {
		var b [MaxMessageSize]byte
		for {
			err := func() error {
				n, raddr, err := readSSPacket(pc2, b[:], act, window)
				if err != nil {
					if isSSTimeout(err) {
						// F2-19 attribution: unwind (close both
						// ends) either way, but only an armed AND
						// fully stalled window may be labelled idle
						// timeout (router/associate caliber). With
						// the window off, readSSPacket has already
						// passed a foreign timeout through verbatim;
						// return it as-is.
						pc1.Close()
						pc2.Close()
						if window > 0 && act.stalled(window) {
							timedOut.Store(true)
							return errSSRelayIdleTimeout
						}
						return err
					}
					return err
				}

				if h.options.Bypass != nil && h.options.Bypass.Contains(ctx, raddr.Network(), raddr.String()) {
					bypassHits.NoteDetail(raddr.String())
					return nil
				}

				target, perr := h.pinUDPTarget(ctx, pins, raddr)
				if perr != nil {
					pinErrs.NoteErr(perr)
					return nil
				}

				if _, err = pc1.WriteTo(b[:n], target); err != nil {
					return err
				}
				act.touch()

				log.Tracef("%s <<< %s data: %d",
					pc2.LocalAddr(), raddr, n)
				return nil
			}()

			if err != nil {
				errc <- err
				return
			}
		}
	}()

	err = <-errc
	if timedOut.Load() {
		err = errSSRelayIdleTimeout
	}
	return err
}

// ssRelayIdleTimeout optionally overrides the no-progress window
// for one SS-UDP association. Zero — the production value —
// defers to the node-wide window (internal/idle, disabled unless
// the panel configured one); the criterion is absence of progress
// in either direction, not age. A variable only so tests can
// shrink the window.
var ssRelayIdleTimeout time.Duration

var errSSRelayIdleTimeout = errors.New("ss udp relay: idle timeout, no progress in either direction")

// ssRelayActivity is the shared bidirectional progress clock of
// one SS-UDP association: an atomic timestamp, no goroutine (the
// same construction udp.Relay uses for F07).
type ssRelayActivity struct {
	last atomic.Int64 // unix nano of the last successful packet move
}

func (a *ssRelayActivity) touch() {
	a.last.Store(time.Now().UnixNano())
}

func (a *ssRelayActivity) deadline(window time.Duration) time.Time {
	return time.Unix(0, a.last.Load()).Add(window)
}

func (a *ssRelayActivity) stalled(window time.Duration) bool {
	return time.Since(time.Unix(0, a.last.Load())) >= window
}

func isSSTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// readSSPacket reads one packet from pc under the shared
// no-progress window: pc's read deadline is armed at
// lastProgress+window before each blocking read, and a timeout
// wake is returned to the caller only when the whole association
// has in fact made no progress for the full window — if the
// sibling direction moved a packet recently, the deadline is
// re-armed instead. The resumable-wake treatment applies only
// while the window is armed (window > 0): with the window off no
// deadline is armed at all, so a timeout-class error surfacing
// then cannot be one of these wakes — it is a real failure of
// that leg (F-212-C: a kernel keepalive verdict on a silently
// dead peer returns ETIMEDOUT) and is returned unswallowed, so
// the caller unwinds the whole association and closes both
// endpoints. An endpoint that refuses deadline arming
// keeps the historical unbounded behaviour for its own direction
// only.
func readSSPacket(pc net.PacketConn, b []byte, act *ssRelayActivity, window time.Duration) (int, net.Addr, error) {
	dl, canArm := pc.(interface{ SetReadDeadline(time.Time) error })
	for {
		if canArm && window > 0 {
			_ = dl.SetReadDeadline(act.deadline(window))
		}
		n, addr, err := pc.ReadFrom(b)
		if err == nil {
			act.touch()
			return n, addr, nil
		}
		if isSSTimeout(err) && window > 0 && !act.stalled(window) {
			continue
		}
		return 0, nil, err
	}
}

func (h *ssuHandler) checkRateLimit(addr net.Addr) bool {
	if h.options.RateLimiter == nil {
		return true
	}
	host, _, _ := net.SplitHostPort(addr.String())
	if limiter := h.options.RateLimiter.Limiter(host); limiter != nil {
		return limiter.Allow(1)
	}

	return true
}
