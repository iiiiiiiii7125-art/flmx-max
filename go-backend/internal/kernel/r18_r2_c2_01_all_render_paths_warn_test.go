package kernel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// R18-R2-C2-01: D-2 警告只覆盖了两条 sing-box JSON 渲染路径；
// 分享链接、Clash YAML、Surge 行（及 Loon/QX 行、导入 Clash 面
// 的通用透传）仍静默渲染 skip-verify，nodes.md:127 的“每次渲染
// 都会打一条警告”已为假。本测试钉住全部渲染路径：每条路径仍
// 原样渲染跳过标志（渲染一字不改），同时发出点名目标地址与
// 渲染形式的警告；受信证书对照组全程静默。
func r18r2c201SelfSigned(t *testing.T, protocol, name, host string, port int, settings Settings) Endpoint {
	t.Helper()
	return Endpoint{Name: name, Host: host, Port: port, Inbound: Inbound{
		Protocol:  protocol,
		Settings:  settings,
		Transport: Transport{Type: TransportTCP},
		Security:  Security{Type: SecurityTLS, CertMode: CertModeSelf, SNI: "s.example.com"},
	}}
}

func TestR18R2C201AllRenderPathsWarn(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	self := []struct {
		name     string
		endpoint Endpoint
		render   func(e Endpoint) (string, bool)
		flag     string // must still be rendered verbatim
		target   string
	}{
		{"share-vmess", r18r2c201SelfSigned(t, ProtocolVMess, "VM", "203.0.113.11", 8443, Settings{UUID: uuid}),
			func(e Endpoint) (string, bool) {
				uri, ok := ShareURI(e)
				if !ok {
					return "", false
				}
				raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "vmess://"))
				if err != nil {
					return "", false
				}
				var v map[string]interface{}
				if err := json.Unmarshal(raw, &v); err != nil {
					return "", false
				}
				if fmt.Sprint(v["allowInsecure"]) != "1" {
					return "", false
				}
				return uri, true
			}, "allowInsecure=1", "203.0.113.11:8443"},
		{"share-hysteria2", r18r2c201SelfSigned(t, ProtocolHysteria2, "HY2", "203.0.113.12", 8443, Settings{Password: "hy2pass"}),
			func(e Endpoint) (string, bool) { uri, ok := ShareURI(e); return uri, ok && strings.Contains(uri, "insecure=1") }, "insecure=1", "203.0.113.12:8443"},
		{"share-tuic", r18r2c201SelfSigned(t, ProtocolTUIC, "TUIC", "203.0.113.13", 8443, Settings{UUID: uuid, Password: "tuicpass"}),
			func(e Endpoint) (string, bool) { uri, ok := ShareURI(e); return uri, ok && strings.Contains(uri, "allow_insecure=1") }, "allow_insecure=1", "203.0.113.13:8443"},
		{"share-anytls", r18r2c201SelfSigned(t, ProtocolAnyTLS, "ATLS", "203.0.113.14", 8443, Settings{Password: "atlspass"}),
			func(e Endpoint) (string, bool) { uri, ok := ShareURI(e); return uri, ok && strings.Contains(uri, "insecure=1") }, "insecure=1", "203.0.113.14:8443"},
		{"share-vless", r18r2c201SelfSigned(t, ProtocolVLESS, "VL", "203.0.113.15", 8443, Settings{UUID: uuid}),
			func(e Endpoint) (string, bool) { uri, ok := ShareURI(e); return uri, ok && strings.Contains(uri, "allowInsecure=1") }, "allowInsecure=1", "203.0.113.15:8443"},
		{"share-trojan", r18r2c201SelfSigned(t, ProtocolTrojan, "TJ", "203.0.113.16", 8443, Settings{Password: "tjpass"}),
			func(e Endpoint) (string, bool) { uri, ok := ShareURI(e); return uri, ok && strings.Contains(uri, "allowInsecure=1") }, "allowInsecure=1", "203.0.113.16:8443"},
		{"clash-vmess", r18r2c201SelfSigned(t, ProtocolVMess, "VM", "203.0.113.11", 8443, Settings{UUID: uuid}),
			func(e Endpoint) (string, bool) { n, ok := ClashProxy(e); return fmt.Sprint(n), ok && strings.Contains(fmt.Sprint(n), "skip-cert-verify") }, "skip-cert-verify", "203.0.113.11:8443"},
		{"clash-hysteria2", r18r2c201SelfSigned(t, ProtocolHysteria2, "HY2", "203.0.113.12", 8443, Settings{Password: "hy2pass"}),
			func(e Endpoint) (string, bool) { n, ok := ClashProxy(e); return fmt.Sprint(n), ok && strings.Contains(fmt.Sprint(n), "skip-cert-verify") }, "skip-cert-verify", "203.0.113.12:8443"},
		{"surge-trojan", r18r2c201SelfSigned(t, ProtocolTrojan, "TJ", "203.0.113.16", 8443, Settings{Password: "tjpass"}),
			func(e Endpoint) (string, bool) { line, ok := SurgeLine(e); return line, ok && strings.Contains(line, "skip-cert-verify=true") }, "skip-cert-verify=true", "203.0.113.16:8443"},
		{"surge-hysteria2", r18r2c201SelfSigned(t, ProtocolHysteria2, "HY2", "203.0.113.12", 8443, Settings{Password: "hy2pass"}),
			func(e Endpoint) (string, bool) { line, ok := SurgeLine(e); return line, ok && strings.Contains(line, "skip-cert-verify=true") }, "skip-cert-verify=true", "203.0.113.12:8443"},
	}

	imported := []struct {
		name   string
		link   string
		render func(e Endpoint) (string, bool)
		flag   string
		target string
	}{
		{"import-share-vmess-tls", `vmess://` + base64.StdEncoding.EncodeToString(mustJSON(t, map[string]interface{}{
			"v": "2", "ps": "VMImp", "add": "v.example.com", "port": "443", "id": uuid,
			"aid": "0", "scy": "auto", "net": "tcp", "type": "none", "tls": "tls",
			"sni": "v.example.com", "skip-cert-verify": true})),
			func(e Endpoint) (string, bool) { return r18r2c201VMessAllowsInsecure(e, "allowInsecure") }, "allowInsecure=1", "v.example.com:443"},
		{"import-share-vmess-reality", `vmess://` + base64.StdEncoding.EncodeToString(mustJSON(t, map[string]interface{}{
			"v": "2", "ps": "VMReality", "add": "r.example.com", "port": "443", "id": uuid,
			"aid": "0", "scy": "auto", "net": "tcp", "type": "none", "tls": "reality",
			"sni": "r.example.com", "pbk": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"skip-cert-verify": true})),
			func(e Endpoint) (string, bool) { return r18r2c201VMessAllowsInsecure(e, "allowInsecure") }, "allowInsecure=1", "r.example.com:443"},
		{"import-share-trojan", "trojan://pass%40word@t.example.com:443?security=tls&sni=t.example.com&type=ws&path=%2Ftr&host=t.example.com&allowInsecure=1#Trojan",
			func(e Endpoint) (string, bool) { uri, ok := ShareURI(e); return uri, ok && strings.Contains(uri, "allowInsecure=1") }, "allowInsecure=1", "t.example.com:443"},
		{"import-share-hysteria2", "hysteria2://hy2pass@h.example.com:443/?sni=h.example.com&insecure=1#HY2",
			func(e Endpoint) (string, bool) { uri, ok := ShareURI(e); return uri, ok && strings.Contains(uri, "insecure=1") }, "insecure=1", "h.example.com:443"},
		{"import-share-tuic", "tuic://" + uuid + ":tuicpass@u.example.com:443?allow_insecure=1&sni=u.example.com#TUIC",
			func(e Endpoint) (string, bool) { uri, ok := ShareURI(e); return uri, ok && strings.Contains(uri, "allow_insecure=1") }, "allow_insecure=1", "u.example.com:443"},
		{"import-share-anytls", "anytls://atlspass@a.example.com:443/?sni=a.example.com&insecure=1#ATLS",
			func(e Endpoint) (string, bool) { uri, ok := ShareURI(e); return uri, ok && strings.Contains(uri, "insecure=1") }, "insecure=1", "a.example.com:443"},
		{"import-surge-trojan", "trojan://pass%40word@t.example.com:443?security=tls&sni=t.example.com&type=ws&path=%2Ftr&host=t.example.com&allowInsecure=1#Trojan",
			func(e Endpoint) (string, bool) { line, ok := proxySurge(e); return line, ok && strings.Contains(line, "skip-cert-verify=true") }, "skip-cert-verify=true", "t.example.com:443"},
		{"import-clash-trojan", "trojan://pass%40word@t.example.com:443?security=tls&sni=t.example.com&type=ws&path=%2Ftr&host=t.example.com&allowInsecure=1#Trojan",
			func(e Endpoint) (string, bool) { n, ok := proxyClash(e); return fmt.Sprint(n), ok && strings.Contains(fmt.Sprint(n), "skip-cert-verify") }, "skip-cert-verify", "t.example.com:443"},
		{"import-loon-trojan", "trojan://pass%40word@t.example.com:443?security=tls&sni=t.example.com&type=ws&path=%2Ftr&host=t.example.com&allowInsecure=1#Trojan",
			func(e Endpoint) (string, bool) {
				p, ok := endpointProxy(e)
				if !ok {
					return "", false
				}
				line, ok := loonLine(p)
				return line, ok && strings.Contains(line, "skip-cert-verify=true")
			}, "skip-cert-verify=true", "t.example.com:443"},
		{"import-qx-trojan", "trojan://pass%40word@t.example.com:443?security=tls&sni=t.example.com&type=ws&path=%2Ftr&host=t.example.com&allowInsecure=1#Trojan",
			func(e Endpoint) (string, bool) {
				p, ok := endpointProxy(e)
				if !ok {
					return "", false
				}
				line, ok := qxLine(p)
				return line, ok && strings.Contains(line, "tls-verification=false")
			}, "tls-verification=false", "t.example.com:443"},
	}

	buf, restore := r18c201CaptureLog(t)
	defer restore()
	for _, c := range self {
		if _, ok := c.render(c.endpoint); !ok {
			t.Fatalf("%s: render must still emit %s", c.name, c.flag)
		}
	}
	for _, c := range imported {
		n := mustImportOne(t, c.link)
		if !n.Proxy.flag("skip-cert-verify") {
			t.Fatalf("%s: fixture must carry skip-cert-verify", c.name)
		}
		e := Endpoint{Name: n.Name, Host: n.Host, Port: n.Port, Proxy: n.Proxy, Direct: true}
		if _, ok := c.render(e); !ok {
			t.Fatalf("%s: render must still emit %s", c.name, c.flag)
		}
	}
	logged := buf.String()
	for _, c := range self {
		if !strings.Contains(logged, c.target) || !strings.Contains(logged, "跳过") {
			t.Fatalf("%s: warning must name target %s and skipped verification, got log: %q", c.name, c.target, logged)
		}
	}
	for _, c := range imported {
		if !strings.Contains(logged, c.target) || !strings.Contains(logged, "跳过") {
			t.Fatalf("%s: warning must name target %s and skipped verification, got log: %q", c.name, c.target, logged)
		}
	}
}

// 受信证书对照组：同一批渲染路径在非自签目标上全程静默。
func TestR18R2C201TrustedCertAllPathsSilent(t *testing.T) {
	eps := shareFixtures(t)
	e := eps[2] // VMess, WS, TLS, file certificate (golden fixture id 3)
	if e.selfSigned() {
		t.Fatal("fixture must not be self-signed")
	}
	buf, restore := r18c201CaptureLog(t)
	defer restore()
	if _, ok := ShareURI(e); !ok {
		t.Fatal("trusted-cert endpoint must render a share link")
	}
	if _, ok := ClashProxy(e); !ok {
		t.Fatal("trusted-cert endpoint must render a Clash entry")
	}
	if _, ok := SurgeLine(e); !ok {
		t.Fatal("trusted-cert endpoint must render a Surge line")
	}
	if got := buf.String(); strings.Contains(got, "跳过") {
		t.Fatalf("trusted-cert renders must not warn, got log: %q", got)
	}
}

func mustJSON(t *testing.T, v map[string]interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// r18r2c201VMessAllowsInsecure decodes a rendered VMess share link
// and checks the allowInsecure flag inside the base64 JSON (the
// literal flag never appears in the encoded link text).
func r18r2c201VMessAllowsInsecure(e Endpoint, key string) (string, bool) {
	uri, ok := ShareURI(e)
	if !ok {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "vmess://"))
	if err != nil {
		return "", false
	}
	var v map[string]interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	if fmt.Sprint(v[key]) != "1" {
		return "", false
	}
	return uri, true
}
