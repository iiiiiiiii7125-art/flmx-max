package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
)

// 订阅脚本（SubOptions.Script）：管理员编写的 JavaScript，用于过滤、改名、
// 排序节点，在内置步骤之后运行：
//
//	function operator(nodes) {
//	  return nodes.filter(n => n.region === "🇭🇰 香港").map(n => {
//	    n.name = "HK " + n.name
//	    return n
//	  })
//	}
//
// 每个节点是一个普通对象；只有变更的 name 会被采纳，其他字段只读。
// 脚本运行在裸 goja 虚拟机中（无网络、文件、定时器），scriptTimeout 后强制停止。

const (
	scriptTimeout   = time.Second
	MaxScriptLength = 20000
	// maxScriptNameRunes caps a script-adopted node name, mirroring the
	// 200-rune bound the panel enforces on node names elsewhere
	// (normalizeNames in the subscription handler). A longer name is a
	// script error, never a truncation: silently shortening it could
	// collide two distinct nodes into one rendered name.
	maxScriptNameRunes = 200
)

// scriptNode 是脚本看到的节点对象；id 用于把返回的节点映射回 endpoint。
func scriptNode(i int, e Endpoint) map[string]interface{} {
	line := "relay"
	if e.Direct {
		line = "direct"
	}
	return map[string]interface{}{
		"id":       i,
		"name":     e.Name,
		"original": e.Original,
		"server":   strings.Trim(e.Host, "[]"),
		"port":     e.Port,
		"protocol": EndpointProtocol(e),
		"region":   RegionOf(e.Name),
		"line":     line,
	}
}

// subScriptPrograms caches compiled scripts by content hash (R2X-024):
// a goja.Program is safe to run on any VM, so one compilation serves
// every request; the VM itself is still created per request (a goja
// Runtime is not thread-safe and must not be shared). Compile failures
// are not cached.
//
// B5-04：缓存必须有界——每个脚本版本（含已删订阅的、编辑中的中间版）
// 都按内容哈希占一条，无淘汰时驻留到进程重启、随日常编辑单调涨。
// 上限 128 条，满了整表清空重来：重编译便宜且罕见，不值得上 LRU；
// 驻留条目的指针复用语义（R2X-024 测试）不变。
const maxCachedSubScriptPrograms = 128

type subScriptProgramCache struct {
	mu sync.Mutex
	m  map[string]*goja.Program
}

func (c *subScriptProgramCache) load(key string) (*goja.Program, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prog, ok := c.m[key]
	return prog, ok
}

func (c *subScriptProgramCache) store(key string, prog *goja.Program) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; !ok && len(c.m) >= maxCachedSubScriptPrograms {
		c.m = make(map[string]*goja.Program, maxCachedSubScriptPrograms)
	}
	c.m[key] = prog
}

func (c *subScriptProgramCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

var subScriptPrograms = &subScriptProgramCache{m: make(map[string]*goja.Program)}

// subScriptProgramCount reports the live cache size (regression tests).
func subScriptProgramCount() int { return subScriptPrograms.size() }

// compileSubScript returns the cached program for script, compiling and
// caching it on first use. ValidateSubScript and ApplySubScript share it.
func compileSubScript(script string) (*goja.Program, error) {
	sum := sha256.Sum256([]byte(script))
	key := hex.EncodeToString(sum[:])
	if prog, ok := subScriptPrograms.load(key); ok {
		return prog, nil
	}
	prog, err := goja.Compile("script.js", script, false)
	if err != nil {
		return nil, err
	}
	subScriptPrograms.store(key, prog)
	return prog, nil
}

// ValidateSubScript 检查脚本能否编译。
func ValidateSubScript(script string) error {
	if strings.TrimSpace(script) == "" {
		return nil
	}
	// X-P3-3（B-F3/BC-10）：上限按字符（rune）计，与文档「20000 字符」
	// 承诺一致；保存侧 normalize 同口径，两处必须同改防再分叉。
	if utf8.RuneCountInString(script) > MaxScriptLength {
		return fmt.Errorf("脚本最多 %d 个字符", MaxScriptLength)
	}
	if _, err := compileSubScript(script); err != nil {
		return fmt.Errorf("脚本语法错误：%v", err)
	}
	return nil
}

// ApplySubScript 在 endpoints 上运行脚本的 operator(nodes)，按脚本返回的
// 顺序和名称返回。
func ApplySubScript(endpoints []Endpoint, script string) ([]Endpoint, error) {
	if strings.TrimSpace(script) == "" {
		return endpoints, nil
	}
	if err := ValidateSubScript(script); err != nil {
		return nil, err
	}
	// R2X-024：执行走缓存的编译产物（RunProgram），不再 RunString
	// 二次编译；Validate 已把该脚本的 Program 放进缓存，此处必命中。
	prog, err := compileSubScript(script)
	if err != nil {
		return nil, fmt.Errorf("脚本语法错误：%v", err)
	}
	vm := goja.New()
	timer := time.AfterFunc(scriptTimeout, func() { vm.Interrupt("timeout") })
	defer timer.Stop()
	// 粘贴的脚本里常见 console.log；保留但无害化。
	console := vm.NewObject()
	_ = console.Set("log", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = vm.Set("console", console)

	if _, err := vm.RunProgram(prog); err != nil {
		return nil, scriptError(err)
	}
	operator, ok := goja.AssertFunction(vm.Get("operator"))
	if !ok {
		return nil, errors.New("脚本里需要定义 function operator(nodes)")
	}
	nodes := make([]interface{}, len(endpoints))
	for i, e := range endpoints {
		nodes[i] = scriptNode(i, e)
	}
	result, err := operator(goja.Undefined(), vm.ToValue(nodes))
	if err != nil {
		return nil, scriptError(err)
	}
	// B5-03：vm.Interrupt 只停 JavaScript；下面 Export() 的 Go 侧物化
	// 不可中断，成本由脚本返回的数组决定。脚本合法上只能返回输入的
	// 子集/重排（重复项随后会被 seen 去重丢弃），所以先在 JS 侧读
	// length 封顶、再 Export，Export 后复核一次兜底——超限结果一概
	// 不物化，按脚本错误走既有 fail-soft（保留脚本前节点集）。
	if !goja.IsUndefined(result) && !goja.IsNull(result) {
		if lv := result.ToObject(vm).Get("length"); lv != nil && !goja.IsUndefined(lv) && !goja.IsNull(lv) {
			if n := lv.ToInteger(); n < 0 || n > int64(len(endpoints)) {
				return nil, errors.New("operator 返回的节点数不能超过输入节点数")
			}
		}
	}
	list, ok := result.Export().([]interface{})
	if !ok {
		return nil, errors.New("operator 需要返回节点数组")
	}
	if len(list) > len(endpoints) {
		return nil, errors.New("operator 返回的节点数不能超过输入节点数")
	}
	out := make([]Endpoint, 0, len(list))
	seen := map[int]bool{}
	for _, item := range list {
		node, ok := item.(map[string]interface{})
		if !ok {
			return nil, errors.New("operator 返回的数组里只能是节点对象")
		}
		i, ok := scriptIndex(node["id"])
		if !ok || i < 0 || i >= len(endpoints) || seen[i] {
			continue
		}
		seen[i] = true
		e := endpoints[i]
		if name, ok := node["name"].(string); ok && strings.TrimSpace(name) != "" {
			name = strings.TrimSpace(name)
			// B5-03：采纳名无长度帽时，一个脚本能给每个节点挂上百 MB
			// 的名字，后续每个渲染器都会反复拷贝。超限按脚本错误
			// 处理（fail-soft 保留原名），不截断、不采纳。
			if utf8.RuneCountInString(name) > maxScriptNameRunes {
				return nil, fmt.Errorf("脚本给节点起的名字超过 %d 个字符", maxScriptNameRunes)
			}
			e.Name = name
		}
		out = append(out, e)
	}
	return out, nil
}

func scriptIndex(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int64:
		return int(n), true
	case int:
		return n, true
	case float64:
		return int(n), n == float64(int(n))
	}
	return 0, false
}

func scriptError(err error) error {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		return fmt.Errorf("脚本运行超过 %v，已停止", scriptTimeout)
	}
	var exc *goja.Exception
	if errors.As(err, &exc) {
		return fmt.Errorf("脚本出错：%s", exc.Value().String())
	}
	return fmt.Errorf("脚本出错：%v", err)
}
