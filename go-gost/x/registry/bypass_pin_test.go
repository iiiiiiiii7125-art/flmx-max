package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/go-gost/core/bypass"
)

// R3-K1 / A02-001 回归（包装层）：注册表按名返回的 bypassWrapper
// 是与 BypassGroup 同型的间接层——它曾同样只转发 Contains/
// IsWhitelist、不转发 PinDialTarget，包住 pinner 时钉死能力对
// 断言方不可见。本测试把「包装/组合类型必须转发钉死」钉在
// wrapper 上：底层是 pinner 则委托之（含拒绝的 fail-closed
// 传播），底层缺失或非 pinner 则原样放行（无 pinner 的现状语义）。

type stubPinner struct{ deny bool }

func (s stubPinner) Contains(context.Context, string, string, ...bypass.Option) bool {
	return false
}
func (s stubPinner) IsWhitelist() bool { return false }
func (s stubPinner) PinDialTarget(_ context.Context, _, addr string) (string, error) {
	if s.deny {
		return "", errors.New("stub pinner: rejected")
	}
	if addr == "pin.test:53" {
		return "203.0.113.7:53", nil
	}
	return addr, nil
}

type stubNonPinner struct{}

func (stubNonPinner) Contains(context.Context, string, string, ...bypass.Option) bool {
	return false
}
func (stubNonPinner) IsWhitelist() bool { return false }

type pinCapable interface {
	PinDialTarget(ctx context.Context, network, addr string) (string, error)
}

func TestBypassWrapperForwardsPinDialTarget(t *testing.T) {
	ctx := context.Background()
	reg := BypassRegistry()

	const pinnerName = "k1-r3-wrapper-pinner"
	if err := reg.Register(pinnerName, stubPinner{}); err != nil {
		t.Fatalf("register stub pinner: %v", err)
	}
	t.Cleanup(func() { reg.Unregister(pinnerName) })

	w := reg.Get(pinnerName)
	pinner, ok := w.(pinCapable)
	if !ok {
		t.Fatal("registry wrapper around a pinner must itself implement PinDialTarget")
	}
	got, err := pinner.PinDialTarget(ctx, "udp", "pin.test:53")
	if err != nil || got != "203.0.113.7:53" {
		t.Fatalf("wrapper must delegate the pin to the underlying pinner: got %q err %v", got, err)
	}

	const denyName = "k1-r3-wrapper-deny"
	if err := reg.Register(denyName, stubPinner{deny: true}); err != nil {
		t.Fatalf("register denying pinner: %v", err)
	}
	t.Cleanup(func() { reg.Unregister(denyName) })
	if _, err := reg.Get(denyName).(pinCapable).PinDialTarget(ctx, "udp", "pin.test:53"); err == nil {
		t.Fatal("wrapper must propagate the underlying pinner's rejection (fail-closed)")
	}
}

func TestBypassWrapperPinPassesThroughWithoutPinner(t *testing.T) {
	ctx := context.Background()
	reg := BypassRegistry()

	const plainName = "k1-r3-wrapper-plain"
	if err := reg.Register(plainName, stubNonPinner{}); err != nil {
		t.Fatalf("register non-pinner: %v", err)
	}
	t.Cleanup(func() { reg.Unregister(plainName) })

	for name, w := range map[string]bypass.Bypass{
		"non-pinner underlying": reg.Get(plainName),
		"unregistered name":     reg.Get("k1-r3-wrapper-absent"),
	} {
		pinner, ok := w.(pinCapable)
		if !ok {
			t.Fatalf("%s: wrapper type must always implement PinDialTarget", name)
		}
		got, err := pinner.PinDialTarget(ctx, "udp", "target.test:53")
		if err != nil || got != "target.test:53" {
			t.Fatalf("%s: without an underlying pinner the target must pass through unchanged, got %q err %v", name, got, err)
		}
	}
}
