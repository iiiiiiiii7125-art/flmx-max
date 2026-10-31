package registry

import (
	"context"

	"github.com/go-gost/core/bypass"
)

type bypassRegistry struct {
	registry[bypass.Bypass]
}

func (r *bypassRegistry) Register(name string, v bypass.Bypass) error {
	return r.registry.Register(name, v)
}

func (r *bypassRegistry) Get(name string) bypass.Bypass {
	if name != "" {
		return &bypassWrapper{name: name, r: r}
	}
	return nil
}

func (r *bypassRegistry) get(name string) bypass.Bypass {
	return r.registry.Get(name)
}

type bypassWrapper struct {
	name string
	r    *bypassRegistry
}

func (w *bypassWrapper) Contains(ctx context.Context, network, addr string, opts ...bypass.Option) bool {
	bp := w.r.get(w.name)
	if bp == nil {
		return false
	}
	return bp.Contains(ctx, network, addr, opts...)
}

func (p *bypassWrapper) IsWhitelist() bool {
	bp := p.r.get(p.name)
	if bp == nil {
		return false
	}
	return bp.IsWhitelist()
}

// dialTargetPinner mirrors bypass.DialTargetPinner (kept local so
// this package does not import the bypass implementation package):
// bypasses that can pin a hostname dial target to a validated IP
// literal at the dial point.
type dialTargetPinner interface {
	PinDialTarget(ctx context.Context, network, addr string) (string, error)
}

// PinDialTarget forwards the dial-point pin to the registered
// bypass when it implements DialTargetPinner (R3-K1 / A02-001).
// This name wrapper is an indirection layer of the same kind as
// BypassGroup and must not hide the pinning capability from
// consumers asserting the interface. When the name does not
// resolve or the underlying bypass cannot pin, the target passes
// through unchanged — the same "no pinning member" semantics as a
// group without a pinner.
func (w *bypassWrapper) PinDialTarget(ctx context.Context, network, addr string) (string, error) {
	bp := w.r.get(w.name)
	if bp == nil {
		return addr, nil
	}
	pinner, ok := bp.(dialTargetPinner)
	if !ok {
		return addr, nil
	}
	return pinner.PinDialTarget(ctx, network, addr)
}
