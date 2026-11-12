package kernel

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// U5（入站连接日志·面板渲染开关）渲染对照：
//   - 节点开关关时，两栈生成配置必须与基线 golden 逐字节一致
//     （缺省零变化——存量节点配置 hash 不变、不被重下发）；
//   - 节点开关开时，生成配置与关闭态的 diff 恰为设计 §3.2/§3.3
//     所列项（Xray: log.access；sing-box: log.level），无溢出改动；
//   - 观察面是进程级的：只由节点开关（RenderOptions.ConnLogEnabled）
//     决定，与入站集合无关——逐入站开关已撤，不再有“开哪个入站”
//     的维度。

// jsonDiffPaths 递归比较两个已解析 JSON 值，返回变化的叶子路径
// （old/new 对）。仅供本文件对照断言用。
func jsonDiffPaths(a, b interface{}, path string, out map[string][2]interface{}) {
	switch av := a.(type) {
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok {
			out[path] = [2]interface{}{a, b}
			return
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		for k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			avv, aok := av[k]
			bvv, bok := bv[k]
			if !aok || !bok {
				out[child] = [2]interface{}{avv, bvv}
				continue
			}
			jsonDiffPaths(avv, bvv, child, out)
		}
	case []interface{}:
		bv, ok := b.([]interface{})
		if !ok || len(av) != len(bv) {
			out[path] = [2]interface{}{a, b}
			return
		}
		for i := range av {
			jsonDiffPaths(av[i], bv[i], fmt.Sprintf("%s[%d]", path, i), out)
		}
	default:
		if !reflect.DeepEqual(a, b) {
			out[path] = [2]interface{}{a, b}
		}
	}
}

func renderContent(t *testing.T, kernelType string, inbounds []Inbound) string {
	t.Helper()
	out, err := Render(kernelType, inbounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != 1 {
		t.Fatalf("files: %+v", out.Files)
	}
	return out.Files[0].Content
}

func renderContentWith(t *testing.T, kernelType string, inbounds []Inbound, opts RenderOptions) string {
	t.Helper()
	out, err := RenderWith(kernelType, inbounds, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != 1 {
		t.Fatalf("files: %+v", out.Files)
	}
	return out.Files[0].Content
}

func parseConfig(t *testing.T, content string) map[string]interface{} {
	t.Helper()
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	return parsed
}

func diffKeys(diff map[string][2]interface{}) []string {
	keys := make([]string, 0, len(diff))
	for k := range diff {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestConnLogRenderOffMatchesBaselineGolden 钉死缺省零变化：夹具入站
// 是普通入站（无逐入站开关——已撤），渲染必须逐字节命中基线既有
// golden——与 TestRenderXrayGolden / TestRenderSingboxGolden 同一
// 文件、同一口径，本测试是 U5 契约的显式登记点。
// D-04：节点开关关时渲染须回落——sing-box 不升 debug、Xray 不开
// access 日志；节点开关开时才升。缺省 RenderOptions（开关关）即
// 回落态。
func TestConnLogRenderNodeOffFallsBack(t *testing.T) {
	t.Run("singbox", func(t *testing.T) {
		inbounds := phase3Inbounds(t)
		offContent := renderContentWith(t, TypeSingbox, inbounds, RenderOptions{})
		if lvl := parseConfig(t, offContent)["log"].(map[string]interface{})["level"]; lvl != "warn" {
			t.Fatalf("node off: log.level = %v, want warn (must not raise to debug)", lvl)
		}
		onContent := renderContentWith(t, TypeSingbox, inbounds, RenderOptions{ConnLogEnabled: true})
		if lvl := parseConfig(t, onContent)["log"].(map[string]interface{})["level"]; lvl != "debug" {
			t.Fatalf("node on: log.level = %v, want debug", lvl)
		}
	})

	t.Run("xray", func(t *testing.T) {
		inbounds := goldenInbounds(t)
		offContent := renderContentWith(t, TypeXray, inbounds, RenderOptions{})
		if _, present := parseConfig(t, offContent)["log"].(map[string]interface{})["access"]; present {
			t.Fatal("node off: log.access must be absent (must not open the access log)")
		}
		onContent := renderContentWith(t, TypeXray, inbounds, RenderOptions{ConnLogEnabled: true})
		if got := parseConfig(t, onContent)["log"].(map[string]interface{})["access"]; got != connLogAccessPath {
			t.Fatalf("node on: log.access = %v, want %q", got, connLogAccessPath)
		}
	})
}

func TestConnLogRenderOffMatchesBaselineGolden(t *testing.T) {
	assertGolden(t, "xray.golden.json", renderContent(t, TypeXray, goldenInbounds(t)))
	assertGolden(t, "singbox-server.golden.json", renderContent(t, TypeSingbox, phase3Inbounds(t)))
}

func TestConnLogRenderOnDiffExact(t *testing.T) {
	// D-04：本测试的「开」态须显式带节点开关开（RenderOptions
	// {ConnLogEnabled: true}）；缺省 opts（开关关）下不升日志级，
	// 见 TestConnLogRenderNodeOffFallsBack。开与关的唯一差别是
	// opts——同一入站集合，逐入站开关已撤。
	nodeOn := RenderOptions{ConnLogEnabled: true}
	t.Run("xray", func(t *testing.T) {
		off := goldenInbounds(t)
		offContent := renderContent(t, TypeXray, off)

		on := goldenInbounds(t)
		onContent := renderContentWith(t, TypeXray, on, nodeOn)

		diff := map[string][2]interface{}{}
		jsonDiffPaths(parseConfig(t, offContent), parseConfig(t, onContent), "", diff)
		if got := diffKeys(diff); !reflect.DeepEqual(got, []string{"log.access"}) {
			t.Fatalf("xray connlog diff paths = %v, want exactly [log.access] (diff=%v)", got, diff)
		}
		if diff["log.access"][1] != connLogAccessPath {
			t.Fatalf("log.access = %v, want %q", diff["log.access"][1], connLogAccessPath)
		}
		if lvl := parseConfig(t, onContent)["log"].(map[string]interface{})["loglevel"]; lvl != "warning" {
			t.Fatalf("log.loglevel = %v, want warning (access 开启不得抬错误日志级别)", lvl)
		}

		// 进程级语义：同一入站集合、同一开关，渲染幂等。
		if got := renderContentWith(t, TypeXray, goldenInbounds(t), nodeOn); got != onContent {
			t.Fatal("xray connlog render must be deterministic for the same inbounds and node switch")
		}

		offOut, _ := Render(TypeXray, goldenInbounds(t))
		onOut, _ := RenderWith(TypeXray, on, nodeOn)
		if offOut.Hash == onOut.Hash {
			t.Fatal("connlog node switch must change the render hash so the kernel is re-applied")
		}
	})

	t.Run("singbox", func(t *testing.T) {
		off := phase3Inbounds(t)
		offContent := renderContent(t, TypeSingbox, off)

		on := phase3Inbounds(t)
		onContent := renderContentWith(t, TypeSingbox, on, nodeOn)

		diff := map[string][2]interface{}{}
		jsonDiffPaths(parseConfig(t, offContent), parseConfig(t, onContent), "", diff)
		if got := diffKeys(diff); !reflect.DeepEqual(got, []string{"log.level"}) {
			t.Fatalf("singbox connlog diff paths = %v, want exactly [log.level] (diff=%v)", got, diff)
		}
		if diff["log.level"][0] != "warn" || diff["log.level"][1] != "debug" {
			t.Fatalf("log.level diff = %v, want warn -> debug", diff["log.level"])
		}

		if got := renderContentWith(t, TypeSingbox, phase3Inbounds(t), nodeOn); got != onContent {
			t.Fatal("singbox connlog render must be deterministic for the same inbounds and node switch")
		}

		offOut, _ := Render(TypeSingbox, phase3Inbounds(t))
		onOut, _ := RenderWith(TypeSingbox, on, nodeOn)
		if offOut.Hash == onOut.Hash {
			t.Fatal("connlog node switch must change the render hash so the kernel is re-applied")
		}
	})
}
