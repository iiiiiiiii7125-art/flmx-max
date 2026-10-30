package bypass

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

// A02-001 回归：拨号落点的 Pin 必须与 Contains 共用同一次解析并把
// 主机名钉死为已验证的 IP 字面量——判定放行而拨号解析到私网的
// 重绑定分叉不得存在。

func stubLookup(t *testing.T, fn func(host string) ([]net.IP, error)) {
	t.Helper()
	orig := noPrivateLookupIP
	noPrivateLookupIP = func(_ context.Context, host string) ([]net.IP, error) {
		return fn(host)
	}
	t.Cleanup(func() { noPrivateLookupIP = orig })
}

func TestPinNoPrivateDialTargetLiteralAddresses(t *testing.T) {
	ctx := context.Background()

	// 公网字面量原样通过。
	got, err := PinNoPrivateDialTarget(ctx, "tcp", "203.0.113.9:443")
	if err != nil || got != "203.0.113.9:443" {
		t.Fatalf("public literal: got %q err %v", got, err)
	}

	// 私网/回环/元数据字面量在落点被拒（即使判定面被绕过）。
	for _, addr := range []string{
		"127.0.0.1:80", "10.0.0.5:22", "192.168.1.1:80",
		"169.254.169.254:80", "100.64.0.1:80", "[::1]:80",
	} {
		if _, err := PinNoPrivateDialTarget(ctx, "tcp", addr); err == nil {
			t.Fatalf("non-public literal %s must be rejected at the dial point", addr)
		}
	}
}

func TestPinNoPrivateDialTargetHostnamePinnedToValidatedIP(t *testing.T) {
	// 解析器只答公网：主机名被钉死为该公网字面量，后续任何环节
	// （路由解析器/hosts/直连拨号器）都不再有二次解析的机会。
	stubLookup(t, func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.9"), net.ParseIP("203.0.113.10")}, nil
	})
	got, err := PinNoPrivateDialTarget(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatalf("pin public host: %v", err)
	}
	if got != "203.0.113.9:443" {
		t.Fatalf("expected pinned literal 203.0.113.9:443, got %q", got)
	}
}

func TestPinNoPrivateDialTargetRebindingSetRejected(t *testing.T) {
	// 重绑定集合：任一答案为私网即整组拒绝——判定看到的是整组，
	// 拨号不可能从组里挑到私网地址。
	stubLookup(t, func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.9"), net.ParseIP("127.0.0.1")}, nil
	})
	if _, err := PinNoPrivateDialTarget(context.Background(), "tcp", "rebind.test:80"); err == nil {
		t.Fatal("answer set containing a private IP must be rejected")
	}
}

func TestPinNoPrivateDialTargetResolutionFailureClosed(t *testing.T) {
	stubLookup(t, func(host string) ([]net.IP, error) {
		return nil, errors.New("dns boom")
	})
	if _, err := PinNoPrivateDialTarget(context.Background(), "tcp", "gone.test:80"); err == nil {
		t.Fatal("resolution error must fail closed")
	}

	stubLookup(t, func(host string) ([]net.IP, error) {
		return nil, nil
	})
	if _, err := PinNoPrivateDialTarget(context.Background(), "tcp", "empty.test:80"); err == nil {
		t.Fatal("empty answer set must fail closed")
	}
}

func TestPinNoPrivateDialTargetNetworkFamilySelection(t *testing.T) {
	stubLookup(t, func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("203.0.113.9")}, nil
	})
	got4, err := PinNoPrivateDialTarget(context.Background(), "tcp4", "example.com:443")
	if err != nil || got4 != "203.0.113.9:443" {
		t.Fatalf("tcp4 pin: got %q err %v", got4, err)
	}
	got6, err := PinNoPrivateDialTarget(context.Background(), "tcp6", "example.com:443")
	if err != nil || !strings.HasPrefix(got6, "[2001:db8::1]") {
		t.Fatalf("tcp6 pin: got %q err %v", got6, err)
	}
}

func TestPinNoPrivateDialTargetEmptyHostPassesThrough(t *testing.T) {
	// UDP association 的空目标在 Router 层无可验证的主机，逐包目标
	// 由 UDP 中继钉死，此处必须原样放行。
	got, err := PinNoPrivateDialTarget(context.Background(), "udp", "")
	if err != nil || got != "" {
		t.Fatalf("empty target: got %q err %v", got, err)
	}
}

func TestNoPrivateBypassImplementsDialTargetPinner(t *testing.T) {
	var bp any = NewNoPrivateBypass()
	pinner, ok := bp.(DialTargetPinner)
	if !ok {
		t.Fatal("noPrivateBypass must implement DialTargetPinner")
	}
	stubLookup(t, func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.9")}, nil
	})
	got, err := pinner.PinDialTarget(context.Background(), "tcp", "example.com:443")
	if err != nil || got != "203.0.113.9:443" {
		t.Fatalf("pinner: got %q err %v", got, err)
	}
}
