package udp

import (
	"context"
	"math"
	"net"
	"sync"

	"github.com/go-gost/core/bypass"
	"github.com/go-gost/core/logger"
)

const (
	MaxMessageSize = math.MaxUint16
)

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

func (r *Relay) Run(ctx context.Context) (err error) {
	errc := make(chan error, 2)

	go func() {
		var b [MaxMessageSize]byte
		for {
			err := func() error {
				n, raddr, err := r.pc1.ReadFrom(b[:])
				if err != nil {
					return err
				}

				if r.bypass != nil && r.bypass.Contains(ctx, "udp", raddr.String()) {
					if r.logger != nil {
						r.logger.Warn("bypass: ", raddr)
					}
					return nil
				}

				target, err := r.pinTarget(ctx, raddr)
				if err != nil {
					if r.logger != nil {
						r.logger.Warn("bypass pin: ", err)
					}
					return nil
				}

				if _, err := r.pc2.WriteTo(b[:n], target); err != nil {
					return err
				}

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
				n, raddr, err := r.pc2.ReadFrom(b[:])
				if err != nil {
					return err
				}

				if r.bypass != nil && r.bypass.Contains(ctx, "udp", raddr.String()) {
					if r.logger != nil {
						r.logger.Warn("bypass: ", raddr)
					}
					return nil
				}

				target, err := r.pinTarget(ctx, raddr)
				if err != nil {
					if r.logger != nil {
						r.logger.Warn("bypass pin: ", err)
					}
					return nil
				}

				if _, err := r.pc1.WriteTo(b[:n], target); err != nil {
					return err
				}

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

	return <-errc
}
