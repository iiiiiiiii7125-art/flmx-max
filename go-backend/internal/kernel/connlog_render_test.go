package kernel

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// U5（入站连接日志·面板渲染开关）渲染对照：
//   - 未开启任何入站时，两栈生成配置必须与基线 golden 逐字节一致
//     （缺省零变化——存量节点配置 hash 不变、不被重下发）；
//   - 开启任一入站后，生成配置与关闭态的 diff 恰为设计 §3.2/§3.3
//     所列项（Xray: log.access；sing-box: log.level），无溢出改动；
//   - 观察面是进程级的：开哪个入站、开几个，渲染结果相同。

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
// 全部未带 opt-in（零值），渲染必须逐字节命中基线既有 golden——与
// TestRenderXrayGolden / TestRenderSingboxGolden 同一文件、同一口径，
// 本测试是 U5 契约的显式登记点。
func TestConnLogRenderOffMatchesBaselineGolden(t *testing.T) {
	assertGolden(t, "xray.golden.json", renderContent(t, TypeXray, goldenInbounds(t)))
	assertGolden(t, "singbox-server.golden.json", renderContent(t, TypeSingbox, phase3Inbounds(t)))
}

func TestConnLogRenderOnDiffExact(t *testing.T) {
	t.Run("xray", func(t *testing.T) {
		off := goldenInbounds(t)
		offContent := renderContent(t, TypeXray, off)

		on := goldenInbounds(t)
		on[0].ConnLogEnabled = true
		onContent := renderContent(t, TypeXray, on)

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

		// 进程级语义：改开另一个入站、或全开，渲染与只开一个相同。
		onLast := goldenInbounds(t)
		onLast[len(onLast)-1].ConnLogEnabled = true
		if got := renderContent(t, TypeXray, onLast); got != onContent {
			t.Fatal("xray connlog render must not depend on which inbound opted in")
		}
		onAll := goldenInbounds(t)
		for i := range onAll {
			onAll[i].ConnLogEnabled = true
		}
		if got := renderContent(t, TypeXray, onAll); got != onContent {
			t.Fatal("xray connlog render must not depend on how many inbounds opted in")
		}

		offOut, _ := Render(TypeXray, goldenInbounds(t))
		onOut, _ := Render(TypeXray, on)
		if offOut.Hash == onOut.Hash {
			t.Fatal("connlog opt-in must change the render hash so the kernel is re-applied")
		}
	})

	t.Run("singbox", func(t *testing.T) {
		off := phase3Inbounds(t)
		offContent := renderContent(t, TypeSingbox, off)

		on := phase3Inbounds(t)
		on[0].ConnLogEnabled = true
		onContent := renderContent(t, TypeSingbox, on)

		diff := map[string][2]interface{}{}
		jsonDiffPaths(parseConfig(t, offContent), parseConfig(t, onContent), "", diff)
		if got := diffKeys(diff); !reflect.DeepEqual(got, []string{"log.level"}) {
			t.Fatalf("singbox connlog diff paths = %v, want exactly [log.level] (diff=%v)", got, diff)
		}
		if diff["log.level"][0] != "warn" || diff["log.level"][1] != "debug" {
			t.Fatalf("log.level diff = %v, want warn -> debug", diff["log.level"])
		}

		onLast := phase3Inbounds(t)
		onLast[len(onLast)-1].ConnLogEnabled = true
		if got := renderContent(t, TypeSingbox, onLast); got != onContent {
			t.Fatal("singbox connlog render must not depend on which inbound opted in")
		}
		onAll := phase3Inbounds(t)
		for i := range onAll {
			onAll[i].ConnLogEnabled = true
		}
		if got := renderContent(t, TypeSingbox, onAll); got != onContent {
			t.Fatal("singbox connlog render must not depend on how many inbounds opted in")
		}

		offOut, _ := Render(TypeSingbox, phase3Inbounds(t))
		onOut, _ := Render(TypeSingbox, on)
		if offOut.Hash == onOut.Hash {
			t.Fatal("connlog opt-in must change the render hash so the kernel is re-applied")
		}
	})
}
