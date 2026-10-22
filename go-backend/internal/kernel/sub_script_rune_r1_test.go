package kernel

import (
	"strings"
	"testing"
)

// X-P3-3（B-F3，BC-10）：订阅脚本 20000 上限文档承诺「字符」，执行侧
// ValidateSubScript 此前按字节计数，含中文注释的脚本提前被拒。
// 7000 个汉字（21000 字节）+ 代码：字符 <20000、字节 >20000，须通过；
// 字符 >20000 仍拒（防过修）。
func TestR1B7SubScriptLimitCountsRunes(t *testing.T) {
	script := "/*" + strings.Repeat("汉", 7000) + "*/\nfunction operator(nodes) { return nodes; }"
	if len(script) <= MaxScriptLength {
		t.Fatalf("fixture must exceed the limit in bytes: %d", len(script))
	}
	if n := len([]rune(script)); n >= MaxScriptLength {
		t.Fatalf("fixture must stay under the limit in runes: %d", n)
	}
	if err := ValidateSubScript(script); err != nil {
		t.Fatalf("multi-byte script under 20000 runes must validate: %v", err)
	}
	eps := []Endpoint{{Name: "n1", Host: "203.0.113.10", Port: 443}}
	out, err := ApplySubScript(eps, script)
	if err != nil {
		t.Fatalf("multi-byte script must execute: %v", err)
	}
	if len(out) != 1 || out[0].Name != "n1" {
		t.Fatalf("script must return nodes unchanged: %v", out)
	}

	tooLong := "/*" + strings.Repeat("汉", 20001) + "*/\nfunction operator(nodes) { return nodes; }"
	if err := ValidateSubScript(tooLong); err == nil || !strings.Contains(err.Error(), "20000") {
		t.Fatalf("script over 20000 runes must still be rejected, got %v", err)
	}
	if _, err := ApplySubScript(eps, tooLong); err == nil {
		t.Fatal("ApplySubScript must reject a script over 20000 runes")
	}
}
