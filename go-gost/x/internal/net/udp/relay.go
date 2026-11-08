package udp

import (
	"context"
	"errors"
	"math"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gost/core/bypass"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/internal/util/eventlog"
)

const (
	MaxMessageSize = math.MaxUint16

	// defaultIdleTimeout bounds a UDP association that makes no
	// progress in either direction. The value matches the FIX-1
	// transport no-progress window (internal/net, 300s) so the
	// whole forwarding stack applies one silence budget: the
	// criterion is absence of progress, not connection age, so a
	// slow trickle of datagrams keeps an association alive
	// indefinitely while a peer whose kernel stays up but whose
	// application died is reaped within one window.
	defaultIdleTimeout = 300 * time.Second
)

// errIdleTimeout is returned by Relay.Run when the association is
// torn down for lack of progress in either direction.
var errIdleTimeout = errors.New("udp relay: idle timeout, no progress in either direction")

// PinMemo memoizes successful dial-target pins (see dialTargetPinner)
// for the lifetime of ONE UDP association (R2-A02-001).
//
// Without a memo, every packet of an association re-runs PinDialTarget
// and its DNS lookup: with multiple A records in rotation, adjacent
// packets of the same association could pin to different backend IPs
// (protocol sessions tied to one backend see the peer address drift),
// and every packet pays a resolver round-trip that a busy resolver may
// rate-limit into drops. The first successful pin of a target is fully
// validated by the bypass; subsequent packets of the same association
// reuse it, so the association stays pinned to one backend.
//
// Scope rule (do not widen): a PinMemo must never be shared across
// associations — a cross-association cache would pin later flows to a
// stale backend and weaken the rebinding protection the pin exists
// for. Errors are never memoized: a failed pin stays fail-closed and
// is retried on the next packet.
type PinMemo struct {
	mu   sync.Mutex
	pins map[string]net.Addr
}

// Lookup returns the memoized pin for key (an address string), if any.
func (m *PinMemo) Lookup(key string) (net.Addr, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	addr, ok := m.pins[key]
	return addr, ok
}

// Store memoizes a successful pin for key.
func (m *PinMemo) Store(key string, addr net.Addr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pins == nil {
		m.pins = make(map[string]net.Addr)
	}
	m.pins[key] = addr
}

type Relay struct {
	pc1 net.PacketConn
	pc2 net.PacketConn

	bypass bypass.Bypass
	logger logger.Logger

	// pins memoizes this relay's per-target pins. A Relay is created
	// per UDP association (socks/http/relay handlers), so the memo's
	// lifetime is exactly the association's — see PinMemo's scope rule.
	pins PinMemo

	// events aggregates the per-packet bypass-drop and pin-failure
	// logging (P1-03 family): both fire at packet rate. Built in
	// Run from the relay logger and flushed when Run returns.
	events     *eventlog.Aggregator
	bypassHits *eventlog.Counter
	pinErrs    *eventlog.Counter

	// idleTimeout is the no-progress window enforced by Run
	// (F07). Zero means defaultIdleTimeout.
	idleTimeout time.Duration
}

func NewRelay(pc1, pc2 net.PacketConn) *Relay {
	return &Relay{
		pc1: pc1,
		pc2: pc2,
	}
}

func (r *Relay) WithBypass(bp bypass.Bypass) *Relay {
	r.bypass = bp
	return r
}

// dialTargetPinner is implemented by bypasses that can pin a
// hostname target to a validated IP literal at the dial point
// (see bypass.DialTargetPinner). A UDP packet target carried in a
// protocol header (e.g. a SOCKS5 domain address) would otherwise be
// resolved twice — once by Bypass.Contains and again inside the
// packet conn's WriteTo — leaving the same rebinding gap as the TCP
// dial path (A02-001). When the bypass implements this interface,
// the relay pins the target after the bypass check and writes to
// the pinned literal only.
type dialTargetPinner interface {
	PinDialTarget(ctx context.Context, network, addr string) (string, error)
}

// pinTarget returns the address a packet must actually be written
// to. Without a pinning bypass it is addr unchanged; with one, a
// hostname target is replaced by its validated IP literal (an error
// drops the packet, fail-closed), while IP-literal targets pass
// through untouched. Successful pins are memoized per target for the
// relay's (= the association's) lifetime (R2-A02-001, see PinMemo);
// failures are not memoized and stay fail-closed per packet.
func (r *Relay) pinTarget(ctx context.Context, addr net.Addr) (net.Addr, error) {
	pinner, ok := r.bypass.(dialTargetPinner)
	if !ok || addr == nil {
		return addr, nil
	}
	key := addr.String()
	if pinned, hit := r.pins.Lookup(key); hit {
		return pinned, nil
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
	r.pins.Store(key, out)
	return out, nil
}

func (r *Relay) WithLogger(logger logger.Logger) *Relay {
	r.logger = logger
	return r
}

// WithIdleTimeout overrides the no-progress window after which Run
// tears the association down. A window <= 0 restores the default
// (defaultIdleTimeout). Production handlers use the default; the
// override exists for tests and for callers with a stricter local
// policy.
func (r *Relay) WithIdleTimeout(d time.Duration) *Relay {
	r.idleTimeout = d
	return r
}

// readDeadliner is the deadline surface an endpoint must offer for
// its direction of the relay to be idle-bounded. Every endpoint
// shape used by the relay's call sites offers it natively (raw UDP
// sockets, the udp listener's session conns, stream conns adapted
// to PacketConn) or by promotion through the stats/traffic/metrics
// wrappers, all of which embed the underlying PacketConn.
type readDeadliner interface {
	SetReadDeadline(time.Time) error
}

// progressTracker is the shared bidirectional activity clock of one
// association: a successful datagram move in EITHER direction is
// progress (the FIX-1 transport criterion). It is two atomics of
// state and no goroutine; the per-datagram cost is one deadline arm
// plus one atomic store, the same arming discipline the transport
// applies per read segment.
type progressTracker struct {
	last atomic.Int64 // unix nano of the last successful datagram move
}

func (t *progressTracker) touch() {
	t.last.Store(time.Now().UnixNano())
}

// deadline is the instant at which the association becomes
// reapable if nothing else moves: last progress + window.
func (t *progressTracker) deadline(window time.Duration) time.Time {
	return time.Unix(0, t.last.Load()).Add(window)
}

func (t *progressTracker) stalled(window time.Duration) bool {
	return time.Since(time.Unix(0, t.last.Load())) >= window
}

func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// readDatagram reads one datagram from src under the shared
// no-progress window. src's read deadline is armed at
// lastProgress+window before each blocking read; a wake on timeout
// ends the read only when the whole association has in fact made
// no progress for the full window — if the sibling direction moved
// a datagram recently, the deadline is simply re-armed. An endpoint
// that refuses deadline arming keeps the historical unbounded
// behaviour for its own direction only; its sibling stays bounded.
func (r *Relay) readDatagram(src net.PacketConn, b []byte, prog *progressTracker, window time.Duration) (int, net.Addr, error) {
	dl, canArm := src.(readDeadliner)
	for {
		if canArm {
			_ = dl.SetReadDeadline(prog.deadline(window))
		}
		n, addr, err := src.ReadFrom(b)
		if err == nil {
			prog.touch()
			return n, addr, nil
		}
		if isTimeout(err) && !prog.stalled(window) {
			continue
		}
		return 0, nil, err
	}
}

func (r *Relay) Run(ctx context.Context) (err error) {
	r.events = eventlog.New(r.logger, eventlog.DefaultInterval)
	r.bypassHits = r.events.DetailCounter(
		"udp relay: %d packet(s) dropped by bypass, last peer %s, %d total")
	r.pinErrs = r.events.DetailCounter(
		"udp relay: %d bypass pin failure(s), last: %s, %d total")
	defer r.events.Flush()

	window := r.idleTimeout
	if window <= 0 {
		window = defaultIdleTimeout
	}

	// F07: the relay itself bounds the association's lifetime.
	// Previously Run returned only when an endpoint errored or was
	// closed, and not every endpoint pairing has a TTL pool behind
	// it (a bare ListenPacket socket paired with a stream conn had
	// no reaper at all), so an association whose peer went silent
	// without closing — kernel alive, application dead — pinned
	// both endpoints and both goroutines forever. Now a direction
	// that finds the whole association stalled closes both
	// endpoints, which unblocks the sibling direction, and Run
	// returns errIdleTimeout.
	prog := &progressTracker{}
	prog.touch()
	var timedOut atomic.Bool

	// Unarm whatever deadlines this Run armed, so an endpoint that
	// outlives the relay (a caller-managed socket) is not left with
	// a stale deadline in the past.
	defer func() {
		for _, pc := range []net.PacketConn{r.pc1, r.pc2} {
			if dl, ok := pc.(readDeadliner); ok {
				_ = dl.SetReadDeadline(time.Time{})
			}
		}
	}()

	errc := make(chan error, 2)

	go func() {
		var b [MaxMessageSize]byte
		for {
			err := func() error {
				n, raddr, err := r.readDatagram(r.pc1, b[:], prog, window)
				if err != nil {
					if isTimeout(err) {
						timedOut.Store(true)
						r.pc1.Close()
						r.pc2.Close()
						return errIdleTimeout
					}
					return err
				}

				if r.bypass != nil && r.bypass.Contains(ctx, "udp", raddr.String()) {
					if r.logger != nil {
						r.bypassHits.NoteDetail(raddr.String())
					}
					return nil
				}

				target, err := r.pinTarget(ctx, raddr)
				if err != nil {
					if r.logger != nil {
						r.pinErrs.NoteErr(err)
					}
					return nil
				}

				if _, err := r.pc2.WriteTo(b[:n], target); err != nil {
					return err
				}
				prog.touch()

				if r.logger != nil {
					r.logger.Tracef("%s >>> %s data: %d",
						r.pc2.LocalAddr(), raddr, n)

				}

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
				n, raddr, err := r.readDatagram(r.pc2, b[:], prog, window)
				if err != nil {
					if isTimeout(err) {
						timedOut.Store(true)
						r.pc1.Close()
						r.pc2.Close()
						return errIdleTimeout
					}
					return err
				}

				if r.bypass != nil && r.bypass.Contains(ctx, "udp", raddr.String()) {
					if r.logger != nil {
						r.bypassHits.NoteDetail(raddr.String())
					}
					return nil
				}

				target, err := r.pinTarget(ctx, raddr)
				if err != nil {
					if r.logger != nil {
						r.pinErrs.NoteErr(err)
					}
					return nil
				}

				if _, err := r.pc1.WriteTo(b[:n], target); err != nil {
					return err
				}
				prog.touch()

				if r.logger != nil {
					r.logger.Tracef("%s <<< %s data: %d",
						r.pc2.LocalAddr(), raddr, n)

				}

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
		err = errIdleTimeout
	}
	return err
}
