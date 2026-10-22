package kernel

import (
	"strings"
	"testing"
)

// R2X-024（批 5）：goja 脚本编译产物以脚本哈希为键缓存（sync.Map），
// ValidateSubScript 与 ApplySubScript 共用；Apply 改走 RunProgram，
// VM 仍每请求新建。回归口径：同脚本第二次起不再编译（同一 *Program
// 指针）、输出节点集合/名称与无缓存语义逐一相同、坏脚本不入缓存。

const r2Script = `function operator(nodes) {
  return nodes.filter(n => n.name.indexOf("keep") >= 0).map(n => { n.name = "X " + n.name; return n })
}`

func TestR2B5SubScriptProgramCacheShared(t *testing.T) {
	p1, err := compileSubScript(r2Script)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	p2, err := compileSubScript(r2Script)
	if err != nil {
		t.Fatalf("compile again: %v", err)
	}
	if p1 != p2 {
		t.Fatal("same script must reuse the cached *goja.Program (no recompile)")
	}
	other, err := compileSubScript(r2Script + "\n// different")
	if err != nil {
		t.Fatalf("compile other: %v", err)
	}
	if other == p1 {
		t.Fatal("different script must not share a cached program")
	}
	if _, err := compileSubScript("function operator( {"); err == nil {
		t.Fatal("invalid script must fail compilation")
	}
	if _, err := compileSubScript("function operator( {"); err == nil {
		t.Fatal("invalid script must keep failing (failures are not cached as programs)")
	}
	// Validate 与 Apply 共用缓存：Validate 后 Apply 仍命中同一 Program。
	if err := ValidateSubScript(r2Script); err != nil {
		t.Fatalf("validate: %v", err)
	}
	p3, err := compileSubScript(r2Script)
	if err != nil || p3 != p1 {
		t.Fatalf("validate/apply must share the cache: %v", err)
	}
}

func TestR2B5SubScriptCachedOutputIdentical(t *testing.T) {
	eps := []Endpoint{
		{Name: "keep HK", Host: "203.0.113.1", Port: 443, Direct: true},
		{Name: "drop JP", Host: "203.0.113.2", Port: 443, Direct: true},
		{Name: "keep US", Host: "203.0.113.3", Port: 443, Direct: true},
	}
	var first []string
	for run := 0; run < 3; run++ {
		out, err := ApplySubScript(eps, r2Script)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		names := make([]string, 0, len(out))
		for _, e := range out {
			names = append(names, e.Name)
		}
		if run == 0 {
			first = names
			if got := strings.Join(names, ","); got != "X keep HK,X keep US" {
				t.Fatalf("script output = %q, want filtered+renamed set", got)
			}
			continue
		}
		if strings.Join(names, ",") != strings.Join(first, ",") {
			t.Fatalf("run %d output %v differs from first run %v (cached program changed semantics)", run, names, first)
		}
	}
	// 空脚本与坏脚本行为不变。
	if out, err := ApplySubScript(eps, "  "); err != nil || len(out) != len(eps) {
		t.Fatalf("empty script passthrough: %v %v", out, err)
	}
	if _, err := ApplySubScript(eps, "function operator( {"); err == nil {
		t.Fatal("invalid script must error via ApplySubScript")
	}
}

func BenchmarkR2B5ApplySubScriptCached(b *testing.B) {
	eps := make([]Endpoint, 0, 20)
	for i := 0; i < 20; i++ {
		eps = append(eps, Endpoint{Name: "keep node", Host: "203.0.113.1", Port: 443, Direct: true})
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ApplySubScript(eps, r2Script); err != nil {
			b.Fatal(err)
		}
	}
}
