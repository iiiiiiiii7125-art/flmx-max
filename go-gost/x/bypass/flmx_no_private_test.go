package bypass

import (
	"context"
	"errors"
	"net"
	"testing"
)

// FIX2-C #8（FIX-I 未竟项）：内置 bypass flmx-no-private 供联邦 exit
// 角色服务在连接时拒绝私网/特殊目标。Contains 返回 true = 阻断。
func TestNoPrivateBypassBlocksLiteralTargets(t *testing.T) {
	bp := NewNoPrivateBypass()
	blocked := []string{
		"10.0.0.5:443", "172.16.3.4:80", "172.31.255.255:80", "192.168.1.1:22",
		"127.0.0.1:6365", "169.254.169.254:80", "100.64.1.2:80", "0.0.0.0:80",
		"[::1]:443", "[fe80::1]:443", "[fd00::5]:443", "[ff02::1]:443",
		"198.18.0.7:80", "224.0.0.251:5353",
	}
	for _, addr := range blocked {
		if !bp.Contains(context.Background(), "tcp", addr) {
			t.Fatalf("expected %s to be blocked", addr)
		}
	}
	allowed := []string{
		"8.8.8.8:53", "1.1.1.1:443", "203.0.113.9:443", "[2606:4700:4700::1111]:443",
		"172.32.0.1:80", "11.0.0.1:80", "100.128.0.1:80",
	}
	for _, addr := range allowed {
		if bp.Contains(context.Background(), "tcp", addr) {
			t.Fatalf("expected %s to be allowed", addr)
		}
	}
	if bp.IsWhitelist() {
		t.Fatal("no-private bypass must be blacklist semantics (Contains=true blocks)")
	}
}

func TestNoPrivateBypassResolvesHostnames(t *testing.T) {
	orig := noPrivateLookupIP
	defer func() { noPrivateLookupIP = orig }()
	noPrivateLookupIP = func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "internal.example.com":
			return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("10.1.2.3")}, nil
		case "public.example.com":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		default:
			return nil, errors.New("no such host")
		}
	}

	bp := NewNoPrivateBypass()
	if !bp.Contains(context.Background(), "tcp", "internal.example.com:443") {
		t.Fatal("hostname resolving (partly) to a private address must be blocked")
	}
	if bp.Contains(context.Background(), "tcp", "public.example.com:443") {
		t.Fatal("hostname resolving only to public addresses must be allowed")
	}
	// 解析失败 fail-closed：无法证明目标非私网时阻断。
	if !bp.Contains(context.Background(), "tcp", "unresolvable.example.com:443") {
		t.Fatal("unresolvable hostname must be blocked (fail-closed)")
	}
}
