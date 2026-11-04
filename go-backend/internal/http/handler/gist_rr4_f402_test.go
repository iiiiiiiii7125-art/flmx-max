package handler

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// RR4-F4-02: scrypt 参数帽只判数值区间，N 在帽内但非 2 的幂时
// gistKey 的英文库错误（scrypt: N must be > 1 and a power of 2）
// 原样透传给管理员，与文档承诺的四分法不符。该子类与帽外参数
// 同归「不认识的加密格式」；解密面其余出口（需密码/数据损坏/
// 密码不对）口径不变。
func TestGistOpenNonPowerOfTwoScryptNIsUnknownFormat(t *testing.T) {
	for _, n := range []int{3, 1000, 32769} {
		env := gistEnvelope{
			Format: gistEnvelopeFormat, V: 1, KDF: "scrypt", N: n, R: 8, P: 1,
			Salt:  base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")),
			Nonce: base64.StdEncoding.EncodeToString([]byte("0123456789ab")),
			Data:  base64.StdEncoding.EncodeToString([]byte("ciphertext")),
		}
		content, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		_, encrypted, err := gistOpen(content, "pw")
		if !encrypted {
			t.Fatalf("N=%d: envelope must be recognized as encrypted", n)
		}
		if err == nil || err.Error() != "不认识的加密格式" {
			t.Fatalf("N=%d: want exactly 「不认识的加密格式」, got %v", n, err)
		}
		if strings.Contains(err.Error(), "scrypt") {
			t.Fatalf("N=%d: library error text must not leak, got %v", n, err)
		}
	}
}
