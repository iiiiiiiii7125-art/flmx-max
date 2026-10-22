package handler

import (
	"strings"
	"testing"
)

// X-P3-3（B-F3，BC-10）保存侧：subPipeline.normalize 的脚本上限此前与
// 执行侧一样按字节计数。7000 汉字脚本（字符 <20000、字节 >20000）
// 保存须通过；字符 >20000 仍拒（防过修）。与 kernel 侧
// TestR1B7SubScriptLimitCountsRunes 配对，两处同改 RuneCountInString。
func TestR1B7NormalizeScriptLimitCountsRunes(t *testing.T) {
	script := "/*" + strings.Repeat("汉", 7000) + "*/\nfunction operator(nodes) { return nodes; }"
	p := subPipeline{Script: script}
	if err := p.normalize(); err != nil {
		t.Fatalf("save side must accept multi-byte script under 20000 runes: %v", err)
	}
	p = subPipeline{Script: "/*" + strings.Repeat("汉", 20001) + "*/\nfunction operator(nodes) { return nodes; }"}
	if err := p.normalize(); err == nil || !strings.Contains(err.Error(), "20000") {
		t.Fatalf("save side must still reject script over 20000 runes, got %v", err)
	}
}
