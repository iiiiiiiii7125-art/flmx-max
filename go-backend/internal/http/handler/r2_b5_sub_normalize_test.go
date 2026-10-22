package handler

import (
	"strings"
	"testing"

	"go-backend/internal/kernel"
)

// R2X-025（批 5）：subPipeline.normalize 残余归一——
//   - Regions 按 RegionNames()+「其他」已知集修剪、去重、过滤未知值；
//   - Protocols 按已知协议集（自建常量 + importTypes 值域）过滤；
//   - RelayIDs 上限 1000 条，超限报错。
// 红态要点：带空格地区此前原样落库、拉取过滤恒空（0 节点）。

func TestR2B5NormalizeRegionsTrimmedAndFiltered(t *testing.T) {
	p := subPipeline{Regions: []string{" 🇭🇰 香港 ", "🇭🇰 香港", " 不存在的地区 ", kernel.RegionOther, "其他"}}
	if err := p.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(p.Regions) != 2 || p.Regions[0] != "🇭🇰 香港" || p.Regions[1] != kernel.RegionOther {
		t.Fatalf("regions not trimmed/deduped/filtered: %q", p.Regions)
	}
	// 拉取侧：规范值必须命中香港节点（修复前带空格值落库 → 0 节点）。
	eps := []kernel.Endpoint{
		{Name: "香港 01", Host: "203.0.113.1", Port: 443, Direct: true},
		{Name: "日本 01", Host: "203.0.113.2", Port: 443, Direct: true},
	}
	out := kernel.FilterEndpoints(eps, kernel.SubOptions{Regions: p.Regions[:1]})
	if len(out) != 1 || out[0].Name != "香港 01" {
		t.Fatalf("normalized region filter must keep the HK node, got %v", out)
	}
	// 全部地区均未知 → 过滤后为空集（空集 = 不过滤是既有语义，未知值
	// 不得原样落库再造成静默空订阅）。
	p2 := subPipeline{Regions: []string{"火星"}}
	if err := p2.normalize(); err != nil {
		t.Fatalf("normalize unknown-only regions: %v", err)
	}
	if len(p2.Regions) != 0 {
		t.Fatalf("unknown regions must be filtered out, got %q", p2.Regions)
	}
}

func TestR2B5NormalizeProtocolsFiltered(t *testing.T) {
	p := subPipeline{Protocols: []string{" vless ", "snell", "socks5", "http", "不存在的协议", "vless"}}
	if err := p.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []string{"vless", "snell", "socks5", "http"}
	if len(p.Protocols) != len(want) {
		t.Fatalf("protocols = %q, want %q", p.Protocols, want)
	}
	for i := range want {
		if p.Protocols[i] != want[i] {
			t.Fatalf("protocols = %q, want %q", p.Protocols, want)
		}
	}
	// 自建协议全集与导入专有协议（socks5/http）都在已知集内。
	for _, proto := range kernel.SubscriptionProtocols() {
		pp := subPipeline{Protocols: []string{proto}}
		if err := pp.normalize(); err != nil {
			t.Fatalf("normalize %s: %v", proto, err)
		}
		if len(pp.Protocols) != 1 || pp.Protocols[0] != proto {
			t.Fatalf("known protocol %q must survive normalize, got %q", proto, pp.Protocols)
		}
	}
}

func TestR2B5NormalizeRelayIDsLimit(t *testing.T) {
	ids := make([]int64, 1001)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	p := subPipeline{RelayIDs: ids}
	if err := p.normalize(); err == nil {
		t.Fatal("1001 relayIds must be rejected")
	} else if !strings.Contains(err.Error(), "中转") {
		t.Fatalf("relayIds limit error should name the field, got %v", err)
	}
	// 恰好 1000 条（含 0=直连）合法，去重/剔负既有行为不变。
	ok := make([]int64, 0, 1000)
	ok = append(ok, 0)
	for i := 1; i < 1000; i++ {
		ok = append(ok, int64(i))
	}
	p2 := subPipeline{RelayIDs: ok}
	if err := p2.normalize(); err != nil {
		t.Fatalf("1000 relayIds must pass: %v", err)
	}
	if len(p2.RelayIDs) != 1000 {
		t.Fatalf("relayIds after normalize = %d, want 1000", len(p2.RelayIDs))
	}
}
