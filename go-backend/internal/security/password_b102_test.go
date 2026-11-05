package security

import (
	"errors"
	"strings"
	"testing"
)

// B1-02: bcrypt 静默截断 72 字节之外的输入——此前设密面无最大
// 长度，两条仅在 72 字节后不同的口令完全等价。本组用例钉住：
// 设密面（HashPassword 单点）拒超长、校验面不靠截断命中、恰
// 72 字节仍可用。legacy MD5 长口令原有「不被 72 字节门误伤」
// 的豁免已随 R-MD5 sunset 落空：legacy 存储整体不再验行。

func TestHashPasswordRejectsOver72Bytes(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", 72)); err != nil {
		t.Fatalf("72-byte password must be accepted, got %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", 73)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("73-byte password must be rejected with ErrPasswordTooLong, got %v", err)
	}
	// 字节口径而非 rune：36 个双字节字符 = 72 字节可过，37 个不可。
	if _, err := HashPassword(strings.Repeat("é", 36)); err != nil {
		t.Fatalf("72-byte multibyte password must be accepted, got %v", err)
	}
	if _, err := HashPassword(strings.Repeat("é", 37)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("74-byte multibyte password must be rejected, got %v", err)
	}
}

func TestVerifyPasswordNoTruncationEquivalence(t *testing.T) {
	prefix := strings.Repeat("a", 72)
	hash, err := HashPassword(prefix + "")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := VerifyPassword(hash, prefix); !ok {
		t.Fatal("the exact 72-byte password must verify")
	}
	// 基线形态：bcrypt 截断使 prefix+"X" 与 prefix+"Y" 互相等价。
	if ok, _ := VerifyPassword(hash, prefix+"X"); ok {
		t.Fatal("a >72-byte candidate must not verify via bcrypt truncation")
	}
	if ok, _ := VerifyPassword(hash, prefix+"Y"); ok {
		t.Fatal("a >72-byte candidate must not verify via bcrypt truncation")
	}
}

// R-MD5 sunset 取代 B1-02 的 legacy 豁免：超长 legacy 口令与其余
// legacy 口令同命——存储只报 legacy 形态、永不命中。
func TestVerifyPasswordLegacyMD5LongPasswordRejectedBySunset(t *testing.T) {
	long := strings.Repeat("z", 100)
	stored := legacyMD5Hex(long)
	if ok, legacy := VerifyPassword(stored, long); ok || !legacy {
		t.Fatalf("legacy MD5 must never verify after sunset (ok=%v legacy=%v)", ok, legacy)
	}
}
