package kernel

import (
	"strings"
	"testing"
	"time"
)

// FIX-B6 / B5-03 + B5-05 回归：脚本结果物化必须有界（返回条数、采纳名
// 长度），且 guide 承诺的两条沙箱保证（1s 超时真终止、无宿主对象泄漏）
// 必须有测试钉住。条数/名字两顶帽在基线代码上为红（无帽、照单全收），
// 超时/confinement 两条是保证钉（基线即绿、防未来重构悄悄拆掉）。

func fixB6ScriptEps() []Endpoint {
	return []Endpoint{
		{Name: "node-a", Host: "203.0.113.1", Port: 443, Direct: true},
		{Name: "node-b", Host: "203.0.113.2", Port: 443, Direct: true},
	}
}

func TestFixB6ScriptResultCountCapped(t *testing.T) {
	// 基线：Export() 先把 100k 条全物化再去重成 1 条且不报错。
	script := `function operator(nodes) { return Array(100000).fill(nodes[0]) }`
	start := time.Now()
	_, err := ApplySubScript(fixB6ScriptEps(), script)
	if err == nil {
		t.Fatal("oversized script result must be rejected, not materialised and deduped")
	}
	if !strings.Contains(err.Error(), "不能超过输入节点数") {
		t.Fatalf("error = %v, want result-count cap error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cap check took %v; the length must be rejected before Export()", elapsed)
	}
}

func TestFixB6ScriptResultReorderStillWorks(t *testing.T) {
	script := `function operator(nodes) { return nodes.slice().reverse() }`
	out, err := ApplySubScript(fixB6ScriptEps(), script)
	if err != nil {
		t.Fatalf("reorder within the input count must keep working: %v", err)
	}
	if len(out) != 2 || out[0].Name != "node-b" || out[1].Name != "node-a" {
		t.Fatalf("reordered output = %v", out)
	}
}

func TestFixB6ScriptNameLengthCapped(t *testing.T) {
	long := strings.Repeat("x", 201)
	script := `function operator(nodes) { nodes[0].name = "` + long + `"; return nodes }`
	if _, err := ApplySubScript(fixB6ScriptEps(), script); err == nil {
		t.Fatal("adopted name over 200 runes must be a script error (no truncation, no adoption)")
	} else if !strings.Contains(err.Error(), "200") {
		t.Fatalf("error = %v, want name-cap error naming the 200 bound", err)
	}
	// Exactly 200 runes (multi-byte, counted as runes not bytes) is adopted.
	exact := strings.Repeat("界", 200)
	script = `function operator(nodes) { nodes[0].name = "` + exact + `"; return nodes }`
	out, err := ApplySubScript(fixB6ScriptEps(), script)
	if err != nil {
		t.Fatalf("200-rune name must be adopted: %v", err)
	}
	if out[0].Name != exact {
		t.Fatalf("adopted name = %q, want the 200-rune name verbatim", out[0].Name)
	}
}

// B5-05 保证钉：operator 内死循环必须被 1s 中断并给出超时错误。
func TestFixB6ScriptTimeoutTerminates(t *testing.T) {
	script := `function operator(nodes) { while (true) {} }`
	start := time.Now()
	_, err := ApplySubScript(fixB6ScriptEps(), script)
	if err == nil || !strings.Contains(err.Error(), "脚本运行超过") {
		t.Fatalf("infinite loop must end with the timeout error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout termination took %v, want ~1s (generous bound 5s)", elapsed)
	}
}

// B5-05 保证钉：顶层（operator 之外）死循环走 RunProgram 阶段，同样被中断。
func TestFixB6ScriptTopLevelTimeoutTerminates(t *testing.T) {
	script := "while (true) {}\nfunction operator(nodes) { return nodes }"
	start := time.Now()
	_, err := ApplySubScript(fixB6ScriptEps(), script)
	if err == nil || !strings.Contains(err.Error(), "脚本运行超过") {
		t.Fatalf("top-level infinite loop must end with the timeout error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("top-level timeout termination took %v", elapsed)
	}
}

// B5-05 保证钉：沙箱封闭——脚本看不到任何宿主能力（require/process/
// Buffer/定时器），直接引用 process 必须是脚本错误而不是拿到 Go 对象。
func TestFixB6ScriptSandboxConfinement(t *testing.T) {
	guard := `function operator(nodes) {
  if (typeof require !== "undefined" || typeof process !== "undefined" ||
      typeof Buffer !== "undefined" || typeof setTimeout !== "undefined") {
    throw new Error("host object leaked")
  }
  return nodes
}`
	out, err := ApplySubScript(fixB6ScriptEps(), guard)
	if err != nil {
		t.Fatalf("confinement guard script must run clean: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("guard script output = %v, want both nodes", out)
	}
	if _, err := ApplySubScript(fixB6ScriptEps(),
		`function operator(nodes) { return process.version }`); err == nil {
		t.Fatal("referencing process must be a script error, never a host value")
	}
}
