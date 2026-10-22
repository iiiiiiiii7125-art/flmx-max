package kernel

import (
	"testing"
)

// X-P3-12（L-02-2）：sing-box 来源的 Snell+ShadowTLS 解析此前不生成
// client-fingerprint——同文件 ss detour 分支置 "chrome"、自建入站
// clashSnell 恒输出，唯独此路缺失，两路渲染的 ShadowTLS 伪装字段不一致。
func TestR1B7ImportedSnellShadowTLSFingerprint(t *testing.T) {
	nodes, errs := ParseImport(`{"outbounds":[
		{"type":"snell","tag":"stls4","server_port":0,"version":4,"psk":"psk-aaaa","detour":"stls4-d"},
		{"type":"shadowtls","tag":"stls4-d","server":"1.2.3.4","server_port":6160,"version":3,"password":"stls-pw","tls":{"enabled":true,"server_name":"www.bing.com"}}
	]}`)
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("parse: nodes=%v errs=%v", nodes, errs)
	}
	if got := nodes[0].Proxy.str("client-fingerprint"); got != "chrome" {
		t.Fatalf("parsed snell+shadowtls client-fingerprint = %q, want chrome (same as ss detour branch)", got)
	}
	node, ok := ClashProxy(Endpoint{Name: nodes[0].Name, Host: nodes[0].Host, Port: nodes[0].Port, Proxy: nodes[0].Proxy})
	if !ok {
		t.Fatal("imported snell+shadowtls must render for clash")
	}
	if got := yamlToMap(node)["client-fingerprint"]; got != "chrome" {
		t.Fatalf("clash render client-fingerprint = %v, want chrome: %v", got, yamlToMap(node))
	}
}
