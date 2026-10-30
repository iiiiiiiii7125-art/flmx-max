package udp

import (
	"context"
	"math"
	"net"

	"github.com/go-gost/core/bypass"
	"github.com/go-gost/core/logger"
)

const (
	MaxMessageSize = math.MaxUint16
)

type Relay struct {
	pc1 net.PacketConn
	pc2 net.PacketConn

	bypass bypass.Bypass
	logger logger.Logger
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
// through untouched.
func (r *Relay) pinTarget(ctx context.Context, addr net.Addr) (net.Addr, error) {
	pinner, ok := r.bypass.(dialTargetPinner)
	if !ok || addr == nil {
		return addr, nil
	}
	pinned, err := pinner.PinDialTarget(ctx, "udp", addr.String())
	if err != nil {
		return nil, err
	}
	if pinned == addr.String() {
		return addr, nil
	}
	ua, err := net.ResolveUDPAddr("udp", pinned)
	if err != nil {
		return nil, err
	}
	return ua, nil
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
