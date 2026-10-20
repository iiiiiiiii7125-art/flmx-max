package kernel

import (
	"crypto/tls"
	"strings"
	"testing"
)

// F-078 (audit FINAL §5-B8 / A7-008，疑似→实证): 自签模式下用户粘贴的
// CertPEM/KeyPEM 只校验了证书域名/有效期（certMatchesName），不校验
// 证书与私钥是否配对。实证（修复前）：certA+keyB（同 SNI、不同对）
// 直调 Normalize 返回 nil 且原样保留错配对，落库后下发到节点才在内核
// 启动时炸，面板层毫无提示。修复：配对校验失败在保存期显式报错。
func TestSelfCertPairingF078(t *testing.T) {
	certA, keyA, err := SelfSignedCert("a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, keyB, err := SelfSignedCert("a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(cert, key string) Inbound {
		return Inbound{Protocol: ProtocolTrojan, Port: 443,
			Security: Security{Type: SecurityTLS, CertMode: CertModeSelf,
				SNI: "a.example.com", CertPEM: cert, KeyPEM: key}}
	}

	bad := mk(certA, keyB)
	err = Normalize(TypeXray, &bad)
	if err == nil {
		t.Fatalf("mismatched cert/key pair must be rejected at save time (kept keyB=%v)",
			bad.Security.KeyPEM == keyB)
	}
	if !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("error must say the pair mismatches, got %v", err)
	}

	good := mk(certA, keyA)
	if err := Normalize(TypeXray, &good); err != nil {
		t.Fatalf("matching pair must be kept: %v", err)
	}
	if good.Security.CertPEM != certA || good.Security.KeyPEM != keyA {
		t.Fatal("matching user-supplied pair must be preserved verbatim")
	}

	// 既有语义不变：未提供材料时仍自动生成配对的自签证书。
	gen := mk("", "")
	if err := Normalize(TypeXray, &gen); err != nil {
		t.Fatal(err)
	}
	if gen.Security.CertPEM == "" || gen.Security.KeyPEM == "" {
		t.Fatal("empty materials must still trigger self-signed generation")
	}
	if _, err := tls.X509KeyPair([]byte(gen.Security.CertPEM), []byte(gen.Security.KeyPEM)); err != nil {
		t.Fatalf("generated pair must be valid: %v", err)
	}
}
