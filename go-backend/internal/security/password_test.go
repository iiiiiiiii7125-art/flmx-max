package security

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
	"testing"
)

// legacyMD5Hex 仅供测试构造 legacy MD5 存储夹具（生产侧
// security.MD5 已随 R-MD5 sunset 整删，验密接受面不存在）。
func legacyMD5Hex(plain string) string {
	sum := md5.Sum([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func TestHashPasswordProducesBcrypt(t *testing.T) {
	hash, err := HashPassword("admin_user")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if len(hash) < 50 || !strings.HasPrefix(hash, "$2") {
		t.Fatalf("expected bcrypt hash, got %q", hash)
	}
	if ok, legacy := VerifyPassword(hash, "admin_user"); !ok || legacy {
		t.Fatalf("VerifyPassword() = (%v,%v), want (true,false)", ok, legacy)
	}
}

// R-MD5 sunset 取代原「legacy 命中 (true,true)」语义：正确口令也
// 不再验行，只如实报告存储形态 (false,true)，由登录面收口账户。
func TestVerifyPasswordRejectsLegacyMD5(t *testing.T) {
	if ok, legacy := VerifyPassword("3c85cdebade1c51cf64ca9f3c09d182d", "admin_user"); ok || !legacy {
		t.Fatalf("VerifyPassword() = (%v,%v), want (false,true)", ok, legacy)
	}
}

func TestIsLegacyPasswordHash(t *testing.T) {
	if !IsLegacyPasswordHash("3c85cdebade1c51cf64ca9f3c09d182d") {
		t.Fatal("expected 32-char hex MD5 hash to be legacy")
	}
	hash, err := HashPassword("admin_user")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if IsLegacyPasswordHash(hash) {
		t.Fatalf("expected bcrypt hash not to be legacy: %q", hash)
	}
}
