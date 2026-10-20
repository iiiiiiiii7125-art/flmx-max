package kernel

import (
	"strings"
	"testing"
)

// VLESS 加密的串文法与派生关系：服务端串含票据段与秘密本身，客户端串含
// 0rtt 段与派生公钥（x25519 为 43 字符、mlkem768 为 1579 字符）。
func TestEncryptionStringsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		auth       string
		secretLen  int
		pubTailLen int
	}{
		{EncryptionAuthX25519, 43, 43},
		{EncryptionAuthMLKEM, 86, 1579},
	} {
		secret, err := GenerateEncryptionSecret(tc.auth)
		if err != nil {
			t.Fatalf("%s generate: %v", tc.auth, err)
		}
		if len(secret) != tc.secretLen {
			t.Fatalf("%s secret len = %d, want %d", tc.auth, len(secret), tc.secretLen)
		}
		s := Settings{EncryptionAuth: tc.auth, EncryptionSecret: secret}
		dec := s.DecryptionString()
		if !strings.HasPrefix(dec, "mlkem768x25519plus.native.600s.100-111-1111.75-0-111.50-0-3333.") {
			t.Fatalf("%s decryption shape: %.60s...", tc.auth, dec)
		}
		if !strings.HasSuffix(dec, "."+secret) {
			t.Fatalf("%s decryption must end with the server secret", tc.auth)
		}
		enc, err := s.EncryptionString()
		if err != nil {
			t.Fatalf("%s derive: %v", tc.auth, err)
		}
		if !strings.HasPrefix(enc, "mlkem768x25519plus.native.0rtt.100-111-1111.75-0-111.50-0-3333.") {
			t.Fatalf("%s encryption shape: %.60s...", tc.auth, enc)
		}
		tail := enc[strings.LastIndex(enc, ".")+1:]
		if len(tail) != tc.pubTailLen {
			t.Fatalf("%s client tail len = %d, want %d", tc.auth, len(tail), tc.pubTailLen)
		}
		if tail == secret {
			t.Fatalf("%s client tail must be the derived public key, not the secret", tc.auth)
		}
	}
}

// Normalize：Xray+VLESS 启用加密时自动生成秘密并保留 vision flow（security
// 为 none 时靠加密也可保留）；sing-box 内核启用必须报错。
func TestNormalizeEncryption(t *testing.T) {
	in := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443,
		Settings: Settings{Flow: FlowVision, EncryptionAuth: EncryptionAuthX25519},
		Security: Security{Type: SecurityNone}}
	if err := Normalize(TypeXray, &in); err != nil {
		t.Fatalf("normalize xray vless enc: %v", err)
	}
	if in.Settings.EncryptionSecret == "" || in.Settings.EncryptionMode != EncryptionModeNative {
		t.Fatalf("secret/mode not filled: %+v", in.Settings)
	}
	if in.Settings.Flow != FlowVision {
		t.Fatalf("vision flow must survive with encryption at security none, got %q", in.Settings.Flow)
	}

	bad := Inbound{ID: 2, Protocol: ProtocolVLESS, Port: 443,
		Settings: Settings{EncryptionAuth: EncryptionAuthX25519},
		Security: Security{Type: SecurityTLS, SNI: "example.com"}}
	if err := Normalize(TypeSingbox, &bad); err == nil || !strings.Contains(err.Error(), "仅支持 Xray") {
		t.Fatalf("sing-box enc must be rejected, got %v", err)
	}
}

// 渲染与订阅：Xray 入站产出 decryption 串；URI/Clash 携带 encryption；
// sing-box 与 Loon/QX 跳过（无已证实支持）。
func TestEncryptionRenderAndShare(t *testing.T) {
	secret, _ := GenerateEncryptionSecret(EncryptionAuthX25519)
	in := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443,
		Settings: Settings{UUID: testUUID, EncryptionAuth: EncryptionAuthX25519, EncryptionMode: EncryptionModeNative, EncryptionSecret: secret},
		Security: Security{Type: SecurityNone}}
	in.Transport.Type = TransportTCP

	rendered, err := Render(TypeXray, []Inbound{in})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	found := false
	for _, f := range rendered.Files {
		if strings.Contains(f.Content, `"decryption": "mlkem768x25519plus.native.600s.`) {
			found = true
		}
	}
	if !found {
		t.Fatal("xray config lacks the decryption string")
	}

	e := Endpoint{Name: "enc", Host: "203.0.113.20", Port: 443, Direct: true, Inbound: in}
	uri, ok := ShareURI(e)
	if !ok || !strings.Contains(uri, "encryption=mlkem768x25519plus.native.0rtt.") {
		t.Fatalf("share URI lacks encryption: %q ok=%v", uri, ok)
	}
	node, ok := ClashProxy(e)
	if !ok {
		t.Fatal("clash must render enc vless")
	}
	clashHasEnc := false
	for _, f := range node {
		if f.Key == "encryption" {
			clashHasEnc = true
		}
	}
	if !clashHasEnc {
		t.Fatal("clash node lacks the encryption key")
	}
	if _, ok := SingboxOutbounds(e); ok {
		t.Fatal("sing-box must skip enc vless")
	}
	p := Proxy{"type": "vless", "server": "203.0.113.20", "port": 443, "uuid": testUUID, "encryption": "mlkem768x25519plus.native.0rtt.x.y"}
	if _, ok := loonLine(p); ok {
		t.Fatal("loon must skip enc vless")
	}
	if _, ok := qxLine(p); ok {
		t.Fatal("qx must skip enc vless")
	}
	if _, ok := proxySingbox(Endpoint{Name: "enc imp", Host: "203.0.113.20", Port: 443, Proxy: p}); ok {
		t.Fatal("proxySingbox must skip enc vless")
	}
}

// 导入的 ENC 节点：URI 与 Clash 必须保真透传 encryption（不得被写回 none
// 或丢弃）；非法认证方式在 Normalize 阶段拒绝。
func TestImportedEncryptionPreserved(t *testing.T) {
	encStr := "mlkem768x25519plus.native.0rtt.100-111-1111.75-0-111.50-0-3333.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	e := Endpoint{Name: "imp enc", Host: "203.0.113.21", Port: 443, Direct: true, Proxy: Proxy{
		"type": "vless", "server": "203.0.113.21", "port": 443, "uuid": testUUID,
		"network": "tcp", "tls": true, "encryption": encStr,
	}}
	uri, ok := proxyURI(e)
	if !ok || !strings.Contains(uri, "encryption="+encStr) {
		t.Fatalf("imported URI must preserve encryption: %q ok=%v", uri, ok)
	}
	node, ok := proxyClash(e)
	if !ok {
		t.Fatal("imported clash must render enc vless")
	}
	found := false
	for _, f := range node {
		if f.Key == "encryption" && f.Value == encStr {
			found = true
		}
	}
	if !found {
		t.Fatal("imported clash node lost the encryption key")
	}

	bad := Inbound{ID: 3, Protocol: ProtocolVLESS, Port: 443,
		Settings: Settings{EncryptionAuth: "bogus"},
		Security: Security{Type: SecurityNone}}
	if err := Normalize(TypeXray, &bad); err == nil || !strings.Contains(err.Error(), "认证方式") {
		t.Fatalf("bogus auth must be rejected, got %v", err)
	}
}
