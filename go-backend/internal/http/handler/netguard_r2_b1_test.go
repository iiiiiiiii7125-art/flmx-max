package handler

import (
	"net"
	"testing"

	"go-backend/internal/store/model"
)

// R2 批1 handler 面回归（修复前红、修复后绿）：
// R2X-001 普通转发、R2X-006 诊断/角色目标、R2X-026 SecureFetch+notify
// 硬拒整段、R2X-005/R2X-043 probe 拨号前判定。

func clearFetchEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "")
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "")
}

func allowFetchEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "")
}

// R2X-001：普通转发策略——非公网全拒、硬拒先行。
func TestR2B1CheckSingleRemoteAddrNonPublicDenied(t *testing.T) {
	clearFetchEnv(t)
	deny := []string{
		"169.254.169.254:80", "169.254.0.23:80", "100.100.100.200:80",
		"240.0.0.1:80", "255.255.255.255:80", "100.64.1.2:80",
		"0.0.0.0:80", "10.0.0.1:80", "127.0.0.1:80", "[::1]:80",
		"[fe80::1]:80", "[fd00:ec2::254]:80", "224.0.0.1:80",
		"[::ffff:169.254.169.254]:80",
		"[64:ff9b::a9fe:a9fe]:80", // NAT64 内嵌元数据
		"[64:ff9b::a00:1]:80",     // NAT64 内嵌私网
		"[64:ff9b:1::808:808]:80", // 本地 NAT64
		"[2002:a9fe:a9fe::1]:80",  // 6to4 内嵌元数据
		"[2002:0a00:0001::1]:80",  // 6to4 内嵌私网
	}
	for _, addr := range deny {
		if err := checkSingleRemoteAddr(addr); err == nil {
			t.Fatalf("checkSingleRemoteAddr(%q) must be rejected", addr)
		}
	}
	allow := []string{
		"8.8.8.8:80",
		"[64:ff9b::808:808]:80",  // NAT64 内嵌公网
		"[2002:0808:0808::1]:80", // 6to4 内嵌公网
	}
	for _, addr := range allow {
		if err := checkSingleRemoteAddr(addr); err != nil {
			t.Fatalf("checkSingleRemoteAddr(%q) must stay allowed: %v", addr, err)
		}
	}
}

// R2X-026：硬拒层 = 名单 ∪ 169.254/16，SecureFetch 与 notify 共用；
// allow_private 下整段仍拒，普通私网抓取维持放行。
func TestR2B1HardSegmentDeniedUnderAllowPrivate(t *testing.T) {
	allowFetchEnv(t)
	for _, s := range []string{"169.254.0.23", "169.254.1.1", "169.254.169.254", "100.100.100.200"} {
		if err := validateFetchIP(net.ParseIP(s), "https://example.com", "https"); err == nil {
			t.Fatalf("validateFetchIP(%s) must be hard-denied even with allow_private", s)
		}
		if err := validateNotifyURL("http://" + s + "/hook"); err == nil {
			t.Fatalf("validateNotifyURL(%s) must be hard-denied", s)
		}
	}
	if err := validateFetchIP(net.ParseIP("10.0.0.1"), "http://10.0.0.1/x", "http"); err != nil {
		t.Fatalf("private fetch with allow_private must stay allowed: %v", err)
	}
	if err := validateNotifyURL("http://10.0.0.1/hook"); err != nil {
		t.Fatalf("private notify target must stay allowed (only hard deny applies): %v", err)
	}
}

// R2X-005/R2X-043 probe 面：存量行指向 loopback 时拨号前拦截——
// 即使该端口确有服务在听也不得拨（红态：修复前会拨通返回 OK）。
func TestR2B1ProbeExternalInboundBlocksLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	row := model.NodeInbound{Listen: "127.0.0.1", Port: port, Protocol: "ss"}
	p := probeExternalInbound(t.Context(), row)
	if p.OK {
		t.Fatal("probe must not dial loopback targets")
	}
	row2 := model.NodeInbound{Listen: "169.254.169.254", Port: port, Protocol: "ss"}
	p2 := probeExternalInbound(t.Context(), row2)
	if p2.OK {
		t.Fatal("probe must not dial metadata targets")
	}
}
