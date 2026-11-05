package kernel

import (
	"fmt"
	"testing"
)

// FIX-B6 / B5-04 回归：编译缓存有界。基线 sync.Map 无淘汰——探针实测
// 编译 178 个不同脚本后缓存 178 条全驻留（红证见本路 notes）。修复后
// 条数永不超过 maxCachedSubScriptPrograms，且被淘汰的脚本重新请求
// 仍能正确编译执行（淘汰只丢编译产物、不丢语义）。
func TestFixB6SubScriptCacheBounded(t *testing.T) {
	total := maxCachedSubScriptPrograms + 50
	for i := 0; i < total; i++ {
		script := fmt.Sprintf("function operator(nodes) { return nodes } // variant %d", i)
		if _, err := compileSubScript(script); err != nil {
			t.Fatalf("compile variant %d: %v", i, err)
		}
		if n := subScriptProgramCount(); n > maxCachedSubScriptPrograms {
			t.Fatalf("after %d distinct scripts the cache holds %d entries, cap is %d", i+1, n, maxCachedSubScriptPrograms)
		}
	}
	// The very first variant was evicted by the overflow clears; asking
	// for it again must recompile and still execute correctly.
	evicted := "function operator(nodes) { return nodes } // variant 0"
	out, err := ApplySubScript(fixB6ScriptEps(), evicted)
	if err != nil {
		t.Fatalf("evicted script must recompile and run: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("evicted script output = %v, want both nodes", out)
	}
	if n := subScriptProgramCount(); n > maxCachedSubScriptPrograms {
		t.Fatalf("cache holds %d entries after recompile, cap is %d", n, maxCachedSubScriptPrograms)
	}
}
