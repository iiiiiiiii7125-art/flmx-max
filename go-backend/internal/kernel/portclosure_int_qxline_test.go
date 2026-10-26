package kernel

import (
	"strings"
	"testing"
)

// PORT-CLOSURE 集成补漏：PC-03(d) qxLine 渲染（FIX-PLAN PC-03 实例 (d)）。
//
// qxLine（share_clients.go:375）是全仓唯一不用 hostPort() 的渲染器：
// fmt.Sprintf("%s:%d", server, port) 把裸 IPv6 直拼成
// "shadowsocks=2001:db8::1:443, …"——端口被折进地址、无端口分量。
// 上游：ClashProxy（share.go:246）与 forEndpoint（proxy.go:284）
// 均把 server 存为去括号裸值，qxLine 是唯一不补括号的出口。
// Lane A 只修了 processServerAddress（handler 侧实例 (a)/(b)/(c)），
// 未触及 kernel 侧的 (d)。修法：qxLine 改用 hostPort()（与全部
// URL 渲染器同口径）；IPv4/主机名渲染不变。

// pcIntQXLine renders a QX line for a bare SS proxy at host:port via
// the production path (endpointProxy -> qxLine).
func pcIntQXLine(t *testing.T, host string, port int) (string, bool) {
	t.Helper()
	e := Endpoint{
		Name:   "pc-int-qx",
		Host:   host,
		Port:   port,
		Direct: true,
		Proxy: Proxy{
			"type": "ss", "server": host, "port": port,
			"cipher": "aes-128-gcm", "password": "pw",
		},
	}
	p, ok := endpointProxy(e)
	if !ok {
		return "", false
	}
	return qxLine(p)
}

// TestPortClosureIntQXLineBracketsBareIPv6 — PC-03(d) (P3)：裸 IPv6
// 节点的 QX 行须渲染为 shadowsocks=[2001:db8::1]:443, …。
func TestPortClosureIntQXLineBracketsBareIPv6(t *testing.T) {
	line, ok := pcIntQXLine(t, "2001:db8::1", 443)
	if !ok {
		t.Fatalf("qxLine skipped bare-IPv6 SS proxy; want a rendered line")
	}
	if !strings.Contains(line, "[2001:db8::1]:443") {
		t.Errorf("qxLine = %q; want it to contain %q (bracketed IPv6 host:port)", line, "[2001:db8::1]:443")
	}
	if strings.Contains(line, "2001:db8::1:443") {
		t.Errorf("qxLine = %q; contains malformed unbracketed %q", line, "2001:db8::1:443")
	}
	// 对照：IPv4 与主机名渲染不变。
	if line4, ok := pcIntQXLine(t, "203.0.113.10", 443); !ok || !strings.Contains(line4, "203.0.113.10:443") {
		t.Errorf("control IPv4 qxLine = %q (ok=%v); want it to contain %q", line4, ok, "203.0.113.10:443")
	}
	if lineH, ok := pcIntQXLine(t, "example.com", 443); !ok || !strings.Contains(lineH, "example.com:443") {
		t.Errorf("control hostname qxLine = %q (ok=%v); want it to contain %q", lineH, ok, "example.com:443")
	}
	// 对照：已带括号的输入同样归一化为括号形态。
	if lineB, ok := pcIntQXLine(t, "[2001:db8::1]", 443); !ok || !strings.Contains(lineB, "[2001:db8::1]:443") {
		t.Errorf("control bracketed-input qxLine = %q (ok=%v); want it to contain %q", lineB, ok, "[2001:db8::1]:443")
	}
}
