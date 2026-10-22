package handler

import (
	"errors"
	"net"
	"testing"
)

// R3 批1（R3X-011）探测/延迟目标面回归。红态（修复前）：normalize
// 对元数据/回环/保留字面量与 localhost 全放行（仅格式校验）。

func TestR3B1NormalizeRejectsDeniedTargets(t *testing.T) {
	deny := []string{
		"169.254.169.254", // 云元数据（硬拒精确集）
		"169.254.0.23",    // 169.254/16 整段硬拒
		"127.0.0.1",       // 回环
		"::1",             // 回环 v6
		"0.1.2.3",         // 保留段（R3X-010 同批收紧）
		"224.0.0.1",       // 组播
		"fe80::1",         // 链路本地
		"localhost",       // 回环别名
		"ip6-localhost",   // 回环别名
	}
	for _, host := range deny {
		if _, _, err := normalizeTunnelProbeTarget(host, 80); err == nil {
			t.Errorf("normalizeTunnelProbeTarget(%q, 80) must be rejected", host)
		}
	}
}

func TestR3B1NormalizeStillAllowsLegitTargets(t *testing.T) {
	allow := []string{
		"www.bing.com", // 默认探测域名
		"8.8.8.8",      // 公网字面量
		"10.0.0.5",     // 私网字面量（管理员测内网网关为合法用法，不采 PublicOnly）
		"100.64.0.1",   // CGNAT 字面量
	}
	for _, host := range allow {
		if _, _, err := normalizeTunnelProbeTarget(host, 443); err != nil {
			t.Errorf("normalizeTunnelProbeTarget(%q, 443) must stay allowed: %v", host, err)
		}
	}
	if target, configured, err := normalizeTunnelProbeTarget("www.bing.com", 443); err != nil || !configured || target.Host != "www.bing.com" || target.Port != 443 {
		t.Errorf("normalizeTunnelProbeTarget(www.bing.com, 443) = %+v configured=%v err=%v", target, configured, err)
	}
}

// 主机名保存时解析同判：解析到回环/组播的名字拒存；解析失败不拒存；
// 解析到公网照常可存；fake-IP 合成段（198.18/15）不作拒存依据。
func TestR3B1NormalizeHostnameResolutionPolicy(t *testing.T) {
	orig := tunnelProbeTargetLookupIP
	defer func() { tunnelProbeTargetLookupIP = orig }()

	stub := func(ips []net.IP, err error) {
		tunnelProbeTargetLookupIP = func(string) ([]net.IP, error) { return ips, err }
	}

	stub([]net.IP{net.ParseIP("127.0.0.1")}, nil)
	if _, _, err := normalizeTunnelProbeTarget("evil.example.com", 80); err == nil {
		t.Error("hostname resolving to loopback must be rejected")
	}
	stub([]net.IP{net.ParseIP("ff02::1")}, nil)
	if _, _, err := normalizeTunnelProbeTarget("ip6-allhosts", 80); err == nil {
		t.Error("hostname resolving to multicast (C-S2 alias) must be rejected")
	}
	stub([]net.IP{net.ParseIP("169.254.169.254")}, nil)
	if _, _, err := normalizeTunnelProbeTarget("meta.example.com", 80); err == nil {
		t.Error("hostname resolving to metadata must be rejected")
	}
	stub(nil, errors.New("no such host"))
	if _, _, err := normalizeTunnelProbeTarget("unresolvable.example.com", 443); err != nil {
		t.Errorf("resolution failure must not reject saving: %v", err)
	}
	stub([]net.IP{net.ParseIP("8.8.8.8")}, nil)
	if _, _, err := normalizeTunnelProbeTarget("dns.example.com", 443); err != nil {
		t.Errorf("hostname resolving to public must stay allowed: %v", err)
	}
	stub([]net.IP{net.ParseIP("198.18.208.97")}, nil)
	if _, _, err := normalizeTunnelProbeTarget("www.bing.com", 443); err != nil {
		t.Errorf("fake-IP synthesized answer (198.18/15) must not reject saving: %v", err)
	}
}

// 下发面存量复检：已存坏目标必须 ok=false（跳过+告警），未配置沿用
// 默认目标，已存合法目标原样放行。
func TestR3B1CheckedStoredTargetSkipsDenied(t *testing.T) {
	for _, host := range []string{"169.254.169.254", "localhost", "0.1.2.3"} {
		if target, ok := checkedTunnelProbeTargetValues(host, 80); ok {
			t.Errorf("checkedTunnelProbeTargetValues(%q, 80) must skip stored bad target, got %+v ok=true", host, target)
		}
	}
	if target, ok := checkedTunnelProbeTargetValues("", 0); !ok || target.Host != defaultTunnelProbeTargetHost {
		t.Errorf("unconfigured target must fall back to default with ok=true, got %+v ok=%v", target, ok)
	}
	if target, ok := checkedTunnelProbeTargetValues("www.bing.com", 443); !ok || target.Host != "www.bing.com" || target.Port != 443 {
		t.Errorf("stored legit target must pass through, got %+v ok=%v", target, ok)
	}
}

// 延迟测试点与隧道探测目标共用 normalize（node_latency.go:84），
// 同批同函数生效——此处以 parseLatencyTargets 为延迟面证据。
func TestR3B1ParseLatencyTargetsRejectsDenied(t *testing.T) {
	for _, host := range []string{"169.254.169.254", "localhost"} {
		raw := `[{"name":"x","host":"` + host + `","port":8080,"enabled":true}]`
		if _, err := parseLatencyTargets(raw); err == nil {
			t.Errorf("parseLatencyTargets(host=%q) must be rejected", host)
		}
	}
	raw := `[{"name":"x","host":"www.bing.com","port":443,"enabled":true}]`
	if _, err := parseLatencyTargets(raw); err != nil {
		t.Errorf("parseLatencyTargets(www.bing.com) must stay allowed: %v", err)
	}
}
