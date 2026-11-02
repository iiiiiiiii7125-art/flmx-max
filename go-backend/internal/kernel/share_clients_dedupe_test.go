package kernel

import (
	"strconv"
	"strings"
	"testing"
)

// R2-014（B-sub-01）：Shadowrocket 链式展开名在 uniqueNames 之后生成、
// 原先从不再检——既有节点的字面名恰为「落地名（经 中转名）」时，下发
// 配置出现两个同名 proxies（探针构造：条目 7、互异 6、面板计数 7）。
// 修复口径：展开后收尾去重，只改展开生成的副本名（dialer-proxy 的指
// 向目标是中转名，绝不能改），既有节点保名；计数面（条目口径）不变。

func srChainEndpoints() []Endpoint {
	ss := func(name, host string) Endpoint {
		return Endpoint{Name: name, Host: host, Port: 8388, Direct: true,
			Proxy: Proxy{"type": "ss", "server": host, "port": 8388,
				"cipher": "aes-128-gcm", "password": "pw", "udp": true}}
	}
	return []Endpoint{
		ss("A", "192.0.2.1"),
		ss("B", "192.0.2.2"),
		ss("R1", "192.0.2.3"),
		ss("R2", "192.0.2.4"),
		ss("A（经 R1）", "192.0.2.5"), // 字面名与 A 经 R1 的展开名碰撞
	}
}

// 锚定正则使字面名节点不命中落地/中转（与审计探针同构造）。
var srChainOpts = SubOptions{ChainLanding: "/^(A|B)$/", ChainRelay: "/^(R1|R2)$/"}

func nodeNameField(n yamlNode) string {
	for _, f := range n {
		if f.Key == "name" {
			if s, ok := f.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

func profileNames(body string) []string {
	var names []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- name:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "- name:"))
		if strings.HasPrefix(v, "\"") {
			if unq, err := strconv.Unquote(v); err == nil {
				v = unq
			}
		}
		names = append(names, v)
	}
	return names
}

func TestShadowrocketChainExpansionNamesRechecked(t *testing.T) {
	eps := srChainEndpoints()
	body, _, err := Subscription(FormatShadowrocket, eps, srChainOpts)
	if err != nil {
		t.Fatal(err)
	}
	names := profileNames(body)
	if len(names) != 7 {
		t.Fatalf("profile must carry 7 entries, got %d: %v\n%s", len(names), names, body)
	}
	seen := map[string]int{}
	for _, n := range names {
		seen[n]++
	}
	if len(seen) != 7 {
		t.Fatalf("profile names must all be distinct after the fix, got %v", names)
	}
	// 字面名节点保名，展开副本加后缀（dialer-proxy 目标名不可动）。
	if seen["A（经 R1）"] != 1 || seen["A（经 R1） #2"] != 1 {
		t.Fatalf("collision must resolve to one literal name + one suffixed copy, got %v", names)
	}
	if got := SubscriptionNodeCount(FormatShadowrocket, eps, srChainOpts); got != 7 {
		t.Fatalf("panel count = %d, want 7 (entry caliber unchanged)", got)
	}
}

func TestShadowrocketProxiesExpansionNamesRechecked(t *testing.T) {
	eps := uniqueNames(ApplySubOptions(srChainEndpoints(), srChainOpts), false)
	proxies, names := shadowrocketProxies(eps, srChainOpts)
	if len(proxies) != 7 {
		t.Fatalf("proxies = %d, want 7", len(proxies))
	}
	emitted := map[string]bool{}
	for _, p := range proxies {
		n, ok := p.(yamlNode)
		if !ok {
			t.Fatalf("proxy is %T, want yamlNode", p)
		}
		name := nodeNameField(n)
		if emitted[name] {
			t.Fatalf("duplicate emitted proxy name %q", name)
		}
		emitted[name] = true
	}
	// 组名单与下发名集合同步：全部互异且都是实得名。
	if len(names) != 7 {
		t.Fatalf("group names = %v, want 7 entries", names)
	}
	for _, n := range names {
		if !emitted[n] {
			t.Fatalf("group name %q is not an emitted proxy name", n)
		}
	}
	// 字面名节点（192.0.2.5）保名；经 R1 的 A 副本（192.0.2.1）带后缀，
	// 其 dialer-proxy 仍指向 R1。
	for _, p := range proxies {
		n := p.(yamlNode)
		server, dialer := "", ""
		for _, f := range n {
			if f.Key == "server" {
				server, _ = f.Value.(string)
			}
			if f.Key == "dialer-proxy" {
				dialer, _ = f.Value.(string)
			}
		}
		switch server {
		case "192.0.2.5":
			if got := nodeNameField(n); got != "A（经 R1）" {
				t.Fatalf("literal node renamed to %q, want its own name kept", got)
			}
		case "192.0.2.1":
			if dialer == "R1" && nodeNameField(n) != "A（经 R1） #2" {
				t.Fatalf("expanded copy via R1 = %q, want suffixed name", nodeNameField(n))
			}
			if dialer == "R2" && nodeNameField(n) != "A（经 R2）" {
				t.Fatalf("expanded copy via R2 = %q, want unchanged name", nodeNameField(n))
			}
		}
	}
}
