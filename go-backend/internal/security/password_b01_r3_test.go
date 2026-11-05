package security

import (
	"testing"
	"time"
)

// R3 B01-003 回归：legacy MD5 校验原为快速路径，错密微秒级返回，
// 与「用户不存在（dummy bcrypt）/bcrypt 错密（~100ms）」形成三档
// 可区分时序，可枚举仍用 MD5 存量口令的账号。修复把 dummy bcrypt
// 补齐下沉进 VerifyPassword 的 legacy 失败分支：三类失败同量级。

func TestVerifyPasswordLegacyMismatchBurnsBcryptCost(t *testing.T) {
	bcryptHash, err := HashPassword("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	legacyHash := legacyMD5Hex("correct-horse")

	measure := func(hash string) time.Duration {
		start := time.Now()
		ok, _ := VerifyPassword(hash, "wrong-password")
		if ok {
			t.Fatal("wrong password must not verify")
		}
		return time.Since(start)
	}
	// 预热（bcrypt 首次调用的固定开销不计入判别）。
	_ = measure(bcryptHash)

	bcryptWrong := measure(bcryptHash)
	legacyWrong := measure(legacyHash)
	// 同量级判别：补齐后 legacy 错密应与 bcrypt 错密耗时接近
	// （同为一次 cost-10 bcrypt）。阈值取 1/4 留足慢机抖动余量；
	// 修复前 legacy 错密为微秒级，必然远低于该阈值。
	if legacyWrong < bcryptWrong/4 {
		t.Fatalf("legacy mismatch too fast: %v vs bcrypt mismatch %v (timing side channel)", legacyWrong, bcryptWrong)
	}
	if legacyWrong < 10*time.Millisecond {
		t.Fatalf("legacy mismatch returned in %v, want bcrypt-order cost", legacyWrong)
	}
}

// legacy 语义（R-MD5 sunset 后）：存储形态恒如实报告 legacy，
// 但正确口令也不再命中——原「命中 (true,true)」断言已被 sunset
// 取代；错密同为 (false,true)；bcrypt 两态不变。
func TestVerifyPasswordLegacyFlags(t *testing.T) {
	legacyHash := legacyMD5Hex("s3cret")
	if ok, legacy := VerifyPassword(legacyHash, "s3cret"); ok || !legacy {
		t.Fatalf("legacy correct password = (%v,%v), want (false,true)", ok, legacy)
	}
	if ok, legacy := VerifyPassword(legacyHash, "nope"); ok || !legacy {
		t.Fatalf("legacy miss = (%v,%v), want (false,true)", ok, legacy)
	}
	bcryptHash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if ok, legacy := VerifyPassword(bcryptHash, "s3cret"); !ok || legacy {
		t.Fatalf("bcrypt hit = (%v,%v), want (true,false)", ok, legacy)
	}
	if ok, legacy := VerifyPassword(bcryptHash, "nope"); ok || legacy {
		t.Fatalf("bcrypt miss = (%v,%v), want (false,false)", ok, legacy)
	}
}
