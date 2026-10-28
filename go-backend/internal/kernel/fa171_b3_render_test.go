package kernel

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// FLMX 1.7.1 发版后完全审计第 3 批（B-02 / B-05 / B-06，P2）：
// 订阅渲染面——TCP+HTTP 头伪装、VMess+XHTTP、NaïveProxy 的 base64 缺失/降级。
// 每项先红后绿：以下断言在修复前失败、修复后通过。

const fa171B3UUID = "11111111-2222-3333-4444-555555555555"

// fa171B3ParseURI 经面板自有解析器读入一条分享链接（与真实导入同一入口）。
func fa171B3ParseURI(t *testing.T, link string) Proxy {
	t.Helper()
	p, err := parseURI(link)
	if err != nil {
		t.Fatalf("parseURI(%q) 须成功：%v", link, err)
	}
	return p
}

func fa171B3Endpoint(name, host string, port int, p Proxy) Endpoint {
	return Endpoint{Name: name, Host: host, Port: port, Direct: true, Proxy: p}
}

// ---------- B-02：network="http"（TCP+HTTP 头伪装）的 base64 面 ----------

func TestFA171B3HTTPNetworkVLESSBase64(t *testing.T) {
	p := fa171B3ParseURI(t, "vless://"+fa171B3UUID+"@203.0.113.10:443?security=tls&sni=example.com&type=tcp&headerType=http&host=a.example.com&path=%2F#VLESSHttp")
	if p.network() != "http" {
		t.Fatalf("读入后 network 须为 http，got %q", p.network())
	}
	uri, ok := proxyURI(fa171B3Endpoint("VLESSHttp", "203.0.113.10", 443, p))
	if !ok {
		t.Fatal("VLESS+http 节点在 base64 面须下发，不再整节点丢弃")
	}
	for _, want := range []string{"type=tcp", "headerType=http", "host=a.example.com", "path=%2F", "security=tls", "sni=example.com"} {
		if !strings.Contains(uri, want) {
			t.Fatalf("base64 链接须含 %q，got %q", want, uri)
		}
	}
	// 往返：自产链接须能原样回导为 network=http 且 host/path 保留。
	back := fa171B3ParseURI(t, uri)
	if back.network() != "http" {
		t.Fatalf("回导后 network 须为 http，got %q", back.network())
	}
	host, path, _, _ := transportFields(back)
	if host != "a.example.com" || path != "/" {
		t.Fatalf("回导后 host/path 须保留，got host=%q path=%q", host, path)
	}
}

func TestFA171B3HTTPNetworkTrojanBase64(t *testing.T) {
	p := fa171B3ParseURI(t, "trojan://pw123@203.0.113.10:443?security=tls&sni=example.com&type=tcp&headerType=http&host=a.example.com&path=%2F#TrojanHttp")
	if p.network() != "http" {
		t.Fatalf("读入后 network 须为 http，got %q", p.network())
	}
	uri, ok := proxyURI(fa171B3Endpoint("TrojanHttp", "203.0.113.10", 443, p))
	if !ok {
		t.Fatal("Trojan+http 节点在 base64 面须下发，不再整节点丢弃")
	}
	for _, want := range []string{"type=tcp", "headerType=http", "host=a.example.com", "path=%2F"} {
		if !strings.Contains(uri, want) {
			t.Fatalf("base64 链接须含 %q，got %q", want, uri)
		}
	}
	back := fa171B3ParseURI(t, uri)
	if back.network() != "http" {
		t.Fatalf("回导后 network 须为 http，got %q", back.network())
	}
}

// B-02：VMess+http 在 base64 面本就可渲染（type=http），修后不得回退。
func TestFA171B3HTTPNetworkVMessBase64Control(t *testing.T) {
	p := Proxy{"type": "vmess", "server": "203.0.113.10", "port": 443,
		"uuid": fa171B3UUID, "alterId": 0, "network": "http", "tls": true, "servername": "example.com",
		"http-opts": map[string]interface{}{"path": []interface{}{"/"}, "headers": map[string]interface{}{"Host": []interface{}{"a.example.com"}}}}
	uri, ok := proxyURI(fa171B3Endpoint("VMessHttp", "203.0.113.10", 443, p))
	if !ok {
		t.Fatal("VMess+http 节点在 base64 面须下发")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "vmess://"))
	if err != nil {
		t.Fatalf("vmess 链接须为合法 base64 JSON：%v", err)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("vmess 链接须为合法 JSON：%v", err)
	}
	if v["type"] != "http" || v["host"] != "a.example.com" || v["path"] != "/" {
		t.Fatalf("VMess+http 回放须为 type=http/host/path，got %v", v)
	}
}

// B-02：Surge / Loon / QX 三面无 TCP+HTTP 头伪装载体，继续略过——
// 该处置由 doc/guide/nodes.md 排除表列明（与 base64 面的下发并列有据）。
func TestFA171B3HTTPNetworkOtherFacesExcluded(t *testing.T) {
	httpProxy := func(typ string) Proxy {
		p := Proxy{"type": typ, "server": "203.0.113.10", "port": 443,
			"network": "http", "tls": true, "servername": "example.com",
			"http-opts": map[string]interface{}{"path": []interface{}{"/"}, "headers": map[string]interface{}{"Host": []interface{}{"a.example.com"}}}}
		if typ == "vmess" {
			p["uuid"] = fa171B3UUID
			p["alterId"] = 0
		} else {
			p["password"] = "pw123"
		}
		return p
	}
	tcpProxy := func(typ string) Proxy {
		p := httpProxy(typ)
		delete(p, "network")
		delete(p, "http-opts")
		return p
	}
	for _, typ := range []string{"vmess", "trojan"} {
		e := fa171B3Endpoint("H", "203.0.113.10", 443, httpProxy(typ))
		if _, ok := proxySurge(e); ok {
			t.Fatalf("Surge 面无 http 传输载体，%s+http 须略过", typ)
		}
		if _, ok := loonLine(e.Proxy); ok {
			t.Fatalf("Loon 面无 http 传输载体，%s+http 须略过", typ)
		}
		if _, ok := qxLine(e.Proxy); ok {
			t.Fatalf("QX 面无 http 传输载体，%s+http 须略过", typ)
		}
		// 对照：同一节点改回 plain TCP 时三面均须下发（丢的是传输形态，不是节点）。
		ec := fa171B3Endpoint("H", "203.0.113.10", 443, tcpProxy(typ))
		if _, ok := proxySurge(ec); !ok {
			t.Fatalf("Surge 面须下发 %s+tcp 对照节点", typ)
		}
		if _, ok := loonLine(ec.Proxy); !ok {
			t.Fatalf("Loon 面须下发 %s+tcp 对照节点", typ)
		}
		if _, ok := qxLine(ec.Proxy); !ok {
			t.Fatalf("QX 面须下发 %s+tcp 对照节点", typ)
		}
	}
}

// ---------- B-05：VMess+XHTTP 在 base64 面不得降级为明文 TCP ----------

func TestFA171B3VMessXHTTPBase64(t *testing.T) {
	j, _ := json.Marshal(map[string]interface{}{
		"v": "2", "ps": "VMessXHTTP", "add": "203.0.113.10", "port": "443",
		"id": fa171B3UUID, "aid": "0", "scy": "auto", "net": "xhttp",
		"type": "none", "host": "a.example.com", "path": "/x",
		"tls": "tls", "sni": "example.com",
	})
	p := fa171B3ParseURI(t, "vmess://"+base64.StdEncoding.EncodeToString(j))
	if p.network() != "xhttp" {
		t.Fatalf("读入后 network 须为 xhttp，got %q", p.network())
	}
	uri, ok := proxyURI(fa171B3Endpoint("VMessXHTTP", "203.0.113.10", 443, p))
	if !ok {
		t.Fatal("VMess+XHTTP 节点在 base64 面须下发")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "vmess://"))
	if err != nil {
		t.Fatalf("vmess 链接须为合法 base64 JSON：%v", err)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("vmess 链接须为合法 JSON：%v", err)
	}
	if v["net"] != "xhttp" || v["host"] != "a.example.com" || v["path"] != "/x" {
		t.Fatalf("VMess+XHTTP 回放须为 net=xhttp/host/path，got net=%v host=%v path=%v", v["net"], v["host"], v["path"])
	}
	// 往返：自产链接须能原样回导为 network=xhttp 且 host/path 保留。
	back := fa171B3ParseURI(t, uri)
	if back.network() != "xhttp" {
		t.Fatalf("回导后 network 须为 xhttp，got %q", back.network())
	}
	host, path, _, _ := transportFields(back)
	if host != "a.example.com" || path != "/x" {
		t.Fatalf("回导后 host/path 须保留，got host=%q path=%q", host, path)
	}
}

// B-05 对照：VLESS+XHTTP 在 base64 面本就可完整往返，修后不得回退。
func TestFA171B3VLESSXHTTPBase64Control(t *testing.T) {
	p := fa171B3ParseURI(t, "vless://"+fa171B3UUID+"@203.0.113.10:443?security=tls&sni=example.com&type=xhttp&path=%2Fx&host=a.example.com&mode=auto#VLESSXHTTP")
	if p.network() != "xhttp" {
		t.Fatalf("读入后 network 须为 xhttp，got %q", p.network())
	}
	uri, ok := proxyURI(fa171B3Endpoint("VLESSXHTTP", "203.0.113.10", 443, p))
	if !ok {
		t.Fatal("VLESS+XHTTP 节点在 base64 面须下发")
	}
	for _, want := range []string{"type=xhttp", "path=%2Fx", "host=a.example.com"} {
		if !strings.Contains(uri, want) {
			t.Fatalf("VLESS+XHTTP 链接须含 %q，got %q", want, uri)
		}
	}
}

// ---------- B-06：NaïveProxy 在 base64 面 ----------

func fa171B3NaiveProxy() Proxy {
	// 与 sing-box JSON 导入的落库形态一致（见 import_fix132_test.go）。
	return Proxy{"type": "naive", "server": "5.6.7.8", "port": 443,
		"username": "u1", "password": "p1", "tls": true, "servername": "nv.example.com"}
}

func TestFA171B3NaiveBase64(t *testing.T) {
	uri, ok := proxyURI(fa171B3Endpoint("nv", "5.6.7.8", 443, fa171B3NaiveProxy()))
	if !ok {
		t.Fatal("NaïveProxy 节点在 base64 面须下发，不再静默缺失")
	}
	if uri != "naive://u1:p1@5.6.7.8:443#nv" {
		t.Fatalf("NaïveProxy 链接须为 naive://user:pass@host:port 形态，got %q", uri)
	}
}

func TestFA171B3NaiveBase64EscapesCredentials(t *testing.T) {
	p := fa171B3NaiveProxy()
	p["username"] = "user@x"
	p["password"] = "p@ss:word/x"
	uri, ok := proxyURI(fa171B3Endpoint("nv", "5.6.7.8", 443, p))
	if !ok {
		t.Fatal("NaïveProxy 节点在 base64 面须下发")
	}
	// 与同文件 socks5 / trojan 支同一口径：userinfo 经 url.PathEscape
	//（/ 转义为 %2F；@ 与 : 为 path 合法字符、保留原样，解析时按
	// 最后一个 @ 切分 userinfo，与 splitURI 同口径）。
	if !strings.HasPrefix(uri, "naive://user@x:p@ss:word%2Fx@5.6.7.8:443#nv") {
		t.Fatalf("用户名/密码须按 PathEscape 转义，got %q", uri)
	}
	if _, err := url.Parse(uri); err != nil {
		t.Fatalf("NaïveProxy 链接须为合法 URL：%v", err)
	}
}

// B-06 对照：Clash 面仍须略过 NaïveProxy（Mihomo 无客户端）；
// sing-box 面仍须渲染（UA 门在订阅层，不在本函数）。
func TestFA171B3NaiveOtherFacesControl(t *testing.T) {
	e := fa171B3Endpoint("nv", "5.6.7.8", 443, fa171B3NaiveProxy())
	if _, ok := proxyClash(e); ok {
		t.Fatal("Clash 面须继续略过 NaïveProxy（Mihomo 无客户端）")
	}
	outs, ok := proxySingbox(e)
	if !ok || len(outs) == 0 || outs[0]["type"] != "naive" {
		t.Fatalf("sing-box 面须渲染 NaïveProxy，got ok=%v outs=%v", ok, outs)
	}
}
