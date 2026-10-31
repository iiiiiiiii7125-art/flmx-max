package auth

import (
	"strings"
	"testing"
	"time"
)

// R3 B01-002 回归：splitToken 旧实现逐字节拼接呈 O(n²)，未认证超长
// Authorization 头（100KB 量级）单请求烧数秒 CPU；且 ParseClaims 无
// 长度上限。修复：strings.Split 线性切分 + 入口 8KB 上限直接拒绝。
// 两个消费方（HTTP middleware、WS ValidateToken）共用 ParseClaims。

func TestSplitTokenSemanticsPreserved(t *testing.T) {
	for in, want := range map[string][]string{
		"":        {""},
		"a":       {"a"},
		"a.b":     {"a", "b"},
		"a.b.c":   {"a", "b", "c"},
		".a.":     {"", "a", ""},
		"a..b":    {"a", "", "b"},
		"..":      {"", "", ""},
		"abc.def": {"abc", "def"},
	} {
		got := splitToken(in)
		if len(got) != len(want) {
			t.Fatalf("splitToken(%q) = %v, want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("splitToken(%q) = %v, want %v", in, got, want)
			}
		}
	}
}

// 超限 token 必须在解析前被拒（合法签发 token 约 300B，远低于上限）。
func TestParseClaimsRejectsOverlongToken(t *testing.T) {
	long := strings.Repeat("a", maxTokenBytes+1)
	if _, err := ParseClaims(long, "secret"); err == nil {
		t.Fatal("overlong token must be rejected at ParseClaims entry")
	}
	if _, ok := ValidateToken(long, "secret"); ok {
		t.Fatal("overlong token must not validate (WS consumer shares ParseClaims)")
	}
	// 恰在上限内仍走正常解析路径（垃圾内容以签名/格式错误被拒，
	// 不得以长度为由误拒——上限只拦量级异常）。
	atCap := strings.Repeat("a", maxTokenBytes)
	if _, err := ParseClaims(atCap, "secret"); err != nil && err.Error() == "token too long" {
		t.Fatal("token exactly at the cap must not be rejected for length")
	}
}

// 线性化判别：50KB 无点 token 在旧 O(n²) 实现下约需 3.6s（副本探针
// 实测标度 10k→0.18s、50k→3.6s、100k→17.9s）；线性实现为毫秒级。
// 经 splitToken 直测（超限 token 在 ParseClaims 入口即被拒，走不到
// 切分），阈值取 1.5s 留足慢机余量。
func TestSplitTokenIsLinear(t *testing.T) {
	big := strings.Repeat("a", 50_000)
	start := time.Now()
	parts := splitToken(big)
	elapsed := time.Since(start)
	if len(parts) != 1 || len(parts[0]) != 50_000 {
		t.Fatalf("splitToken(50k) shape wrong: %d parts", len(parts))
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("splitToken(50k) took %v, want linear time (old O(n^2) ~3.6s)", elapsed)
	}
}

// 修复不得误伤正常签发/校验链。
func TestParseClaimsStillAcceptsIssuedTokenAfterHardening(t *testing.T) {
	token, err := GenerateToken(42, "alice", 1, "secret")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseClaims(token, "secret")
	if err != nil {
		t.Fatalf("issued token must still parse: %v", err)
	}
	if claims.Sub != "42" || claims.User != "alice" {
		t.Fatalf("claims mismatch: %+v", claims)
	}
}
