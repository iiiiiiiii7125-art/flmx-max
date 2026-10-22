package kernel

import (
	"errors"
	"fmt"
	"strings"
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
// 移植自 kure29/flyx。

const (
	scriptTimeout   = time.Second
	MaxScriptLength = 20000
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
	if _, err := goja.Compile("script.js", script, false); err != nil {
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
	vm := goja.New()
	timer := time.AfterFunc(scriptTimeout, func() { vm.Interrupt("timeout") })
	defer timer.Stop()
	// 粘贴的脚本里常见 console.log；保留但无害化。
	console := vm.NewObject()
	_ = console.Set("log", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = vm.Set("console", console)

	if _, err := vm.RunString(script); err != nil {
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
	list, ok := result.Export().([]interface{})
	if !ok {
		return nil, errors.New("operator 需要返回节点数组")
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
			e.Name = strings.TrimSpace(name)
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
