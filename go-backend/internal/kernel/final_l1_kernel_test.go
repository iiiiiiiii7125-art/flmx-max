package kernel

import (
	"encoding/base64"
	"strings"
	"testing"
)

// 终审修复轮 Lane 1（kernel 面）：L1-F-01 / L1-F-02 / L1-F-03 回归。
// 先红后绿：三项均为 T-01 空标签同族残留，复用 HasEmptyLabel 按机制
// 收口；文案各复用所在面的既有文案（SNI / Host / Reality 目标地址）。

// L1-F-01：自建入站 RealityDest `a..b:443` 被接受并落库。
// normalizeSecurity Reality 分支只做 SplitHostPort + 端口检查，host
// 段不走 hostPattern / HasEmptyLabel；SNI 同一函数内已在 :648 校验。
func TestFinalL1RealityDestEmptyLabel(t *testing.T) {
	bad := []string{"a..b:443", "a...b:443"}
	for _, dest := range bad {
		in := &Inbound{Security: Security{Type: SecurityReality, SNI: "example.com", RealityDest: dest}}
		if err := normalizeSecurity(in); err == nil || err.Error() != "Reality 目标地址不正确" {
			t.Errorf("normalizeSecurity(RealityDest=%q) = %v, want %q", dest, err, "Reality 目标地址不正确")
		}
	}
	// Normalize 面同判（lane-1 探针原样：Normalize 返回 nil、原值落库）。
	for _, dest := range bad {
		in := &Inbound{Protocol: ProtocolVLESS, Port: 443,
			Security: Security{Type: SecurityReality, SNI: "example.com", RealityDest: dest}}
		if err := Normalize(TypeXray, in); err == nil || err.Error() != "Reality 目标地址不正确" {
			t.Errorf("Normalize(RealityDest=%q) = %v, want %q", dest, err, "Reality 目标地址不正确")
		}
	}
	// 合法对照：域名与 IP 字面量放行。
	for _, dest := range []string{"example.com:443", "203.0.113.9:443"} {
		in := &Inbound{Security: Security{Type: SecurityReality, SNI: "example.com", RealityDest: dest}}
		if err := normalizeSecurity(in); err != nil {
			t.Errorf("normalizeSecurity(RealityDest=%q) = %v, want nil", dest, err)
		}
	}
	// 空 RealityDest 仍回填 SNI + ":443"，不动该逻辑。
	in := &Inbound{Security: Security{Type: SecurityReality, SNI: "example.com", RealityDest: ""}}
	if err := normalizeSecurity(in); err != nil {
		t.Fatalf("normalizeSecurity(RealityDest empty) = %v, want nil", err)
	}
	if in.Security.RealityDest != "example.com:443" {
		t.Errorf("empty RealityDest backfill = %q, want %q", in.Security.RealityDest, "example.com:443")
	}
}

// L1-F-02：导入路径 SS/Snell 插件类 Host 字段全部漏校验。
// validateImportHostnames 只查 serverName + ws/http/h2/xhttp 四载体，
// 未枚举 plugin-opts.host / obfs-opts.host / shadow-tls.host。
// 以下 8 形态为 lane-1 报告原样（URI obfs-host、v2ray-plugin host、
// shadow-tls host、Surge obfs-host ×2、Clash plugin-opts host、
// sing-box SS/Snell detour），坏值须拒、合法对照须放行。
func TestFinalL1PluginObfsHostEmptyLabel(t *testing.T) {
	ssUser := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:pw"))
	bad := map[string]string{
		// URI：obfs-host（applySSPlugin → plugin-opts.host）。
		"uri-obfs-host": "ss://" + ssUser + "@203.0.113.10:8388/?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Da..b#x",
		// URI：v2ray-plugin host（→ plugin-opts.host）。
		"uri-v2ray-plugin-host": "ss://" + ssUser + "@203.0.113.10:8388/?plugin=v2ray-plugin%3Bmode%3Dwebsocket%3Bhost%3Da..b#x",
		// URI：shadow-tls host（→ plugin-opts.host）。
		"uri-shadow-tls-host": "ss://" + ssUser + "@203.0.113.10:8388/?plugin=shadow-tls%3Bhost%3Da..b%3Bpassword%3Dstls-password-1#x",
		// Surge SS obfs-host（→ plugin-opts.host）。
		"surge-ss-obfs-host": "SS1 = ss, 203.0.113.10, 8388, encrypt-method=aes-128-gcm, password=pw, obfs=http, obfs-host=a..b",
		// Surge Snell obfs-host（→ obfs-opts.host）。
		"surge-snell-obfs-host": "SN1 = snell, 203.0.113.10, 6160, psk=psk-aaaaaaaa, obfs=http, obfs-host=a..b",
		// Clash plugin-opts host 透传。
		"clash-plugin-opts-host": `
proxies:
  - name: CLASH-SS
    type: ss
    server: 203.0.113.10
    port: 8388
    cipher: aes-128-gcm
    password: pw
    plugin: obfs
    plugin-opts:
      mode: http
      host: a..b
`,
		// sing-box SS detour shadowtls server_name（→ plugin-opts.host）。
		"singbox-ss-detour": `{"outbounds":[
  {"type":"shadowsocks","tag":"sb-ss","server":"1.2.3.4","server_port":8388,"method":"aes-128-gcm","password":"pw","detour":"sb-stls"},
  {"type":"shadowtls","tag":"sb-stls","server":"9.9.9.9","server_port":443,"version":3,"password":"stls-pw","tls":{"enabled":true,"server_name":"a..b"}}
]}`,
		// sing-box Snell detour shadowtls server_name（→ shadow-tls.host）。
		"singbox-snell-detour": `{"outbounds":[
  {"type":"snell","tag":"sb-snell","server_port":0,"version":6,"psk":"psk-cccccccc","detour":"sb-s6-stls"},
  {"type":"shadowtls","tag":"sb-s6-stls","server":"9.9.9.9","server_port":443,"version":3,"password":"stls-pw","tls":{"enabled":true,"server_name":"a..b"}}
]}`,
	}
	for kind, text := range bad {
		nodes, errs := ParseImport(text)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 0 nodes with error", kind, len(nodes), errs)
			continue
		}
		if !strings.Contains(errs[0], "Host 格式错误") {
			t.Errorf("%s: err = %q, want %q", kind, errs[0], "Host 格式错误")
		}
	}
	// 合法对照：同形 host=example.com / www.bing.com 须放行。
	good := map[string]string{
		"uri-obfs-host": "ss://" + ssUser + "@203.0.113.10:8388/?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dexample.com#x",
		"surge-snell-obfs-host": "SN1 = snell, 203.0.113.10, 6160, psk=psk-aaaaaaaa, obfs=http, obfs-host=example.com",
		"clash-plugin-opts-host": `
proxies:
  - name: CLASH-SS
    type: ss
    server: 203.0.113.10
    port: 8388
    cipher: aes-128-gcm
    password: pw
    plugin: obfs
    plugin-opts:
      mode: http
      host: example.com
`,
		"singbox-snell-detour": `{"outbounds":[
  {"type":"snell","tag":"sb-snell","server_port":0,"version":6,"psk":"psk-cccccccc","detour":"sb-s6-stls"},
  {"type":"shadowtls","tag":"sb-s6-stls","server":"9.9.9.9","server_port":443,"version":3,"password":"stls-pw","tls":{"enabled":true,"server_name":"www.bing.com"}}
]}`,
	}
	for kind, text := range good {
		nodes, errs := ParseImport(text)
		if len(errs) != 0 || len(nodes) != 1 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 1 node", kind, len(nodes), errs)
		}
	}
}

// L1-F-03：备份恢复面 ValidateImportedProxy 漏掉 sweep 的主机名列。
// 该谓词只调 validateImportedProxyAddress + validateProxyCredentials，
// 不调 validateImportHostnames，与 ParseImport（finishProxy）漂移；
// 唯一调用方为入站恢复（repository_inbound_backup.go:327）。
func TestFinalL1ValidateImportedProxyDrift(t *testing.T) {
	bad := map[string]Proxy{
		// SNI 空标签：ParseImport 拒，恢复谓词此前放行。
		"sni": {"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID, "servername": "a..b"},
		// WS Host 空标签：同上。
		"ws-host": {"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID, "servername": "example.com",
			"ws-opts": map[string]interface{}{"headers": map[string]interface{}{"Host": "a..b"}}},
		// 插件 Host 空标签（L1-F-02 的载体）：恢复面同样须拒。
		"plugin-host": {"type": "ss", "server": "1.2.3.4", "port": 8388, "cipher": "aes-128-gcm", "password": "pw",
			"plugin": "obfs", "plugin-opts": map[string]interface{}{"mode": "http", "host": "a..b"}},
	}
	wantErr := map[string]string{
		"sni":         "SNI 格式错误",
		"ws-host":     "Host 格式错误",
		"plugin-host": "Host 格式错误",
	}
	for kind, p := range bad {
		if err := ValidateImportedProxy(p); err == nil || err.Error() != wantErr[kind] {
			t.Errorf("%s: ValidateImportedProxy = %v, want %q", kind, err, wantErr[kind])
		}
	}
	good := map[string]Proxy{
		"sni": {"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID, "servername": "example.com"},
		"ws-host": {"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID, "servername": "example.com",
			"ws-opts": map[string]interface{}{"headers": map[string]interface{}{"Host": "example.com"}}},
		"plugin-host": {"type": "ss", "server": "1.2.3.4", "port": 8388, "cipher": "aes-128-gcm", "password": "pw",
			"plugin": "obfs", "plugin-opts": map[string]interface{}{"mode": "http", "host": "example.com"}},
	}
	for kind, p := range good {
		if err := ValidateImportedProxy(p); err != nil {
			t.Errorf("%s: ValidateImportedProxy = %v, want nil", kind, err)
		}
	}
}
