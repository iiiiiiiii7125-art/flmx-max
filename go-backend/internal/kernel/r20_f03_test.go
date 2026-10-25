package kernel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// R20-F03: VLESS 空闲超时语义为 2×connIdle（P3）。
//
// 2× 来源定位（xray-core v1.260327.0，面板下发值逐字原样、未翻倍）：
//   - 面板渲染：policy.levels[L] = {connIdle: N}（render_xray.go），
//     flmx-idle-<id> 出站 settings.userLevel = L，均与用户设定值一致。
//   - VLESS 入站正常路径不装空闲计时器（proxy/vless/inbound/inbound.go
//     只取 ForLevel(0) 作握手 deadline）；会话唯一的计时器是 freedom
//     出站的 signal.CancelAfterInactivity（proxy/freedom/freedom.go），
//     以 ForLevel(userLevel).Timeouts.ConnectionIdle = N 计时。
//   - 该计时器是窗口化检查（common/signal/timer.go）：SetTimeout(N)
//     建 Interval=N 的 Periodic 任务，Start 时立即执行一次 check（消耗
//     初始 Update 记号），此后每 N 秒检查一次；check 仅在自上次检查起
//     无任何 Update（buf.Copy 每份数据调 UpdateActivity）时才关闭。
//     故：全程无数据 → 首次检查（约 N）即断；窗口内有数据 → 该次检查
//     消耗记号存活、下一次检查（约 2N）才断。一般地，自末次数据起的
//     实际关断在 (N, 2N] 区间内、取决于数据落在检查格内的相位，
//     不是恒等于 2N，更不是“计时自会话建立起算 2N”。
//   - 因此修法 ①（把计时语义改成“末次数据起算恰好 N 秒断”）在面板侧
//     做不到：Xray 只暴露 connIdle 秒数这一档配置，量化粒度在上游
//     ActivityTimer 实现里；把下发值减半会让静默会话提前到 N/2 断开
//     （更坏的方向）。修法定为 ②：文案改称实测语义，行为不变、无 BC。
//     本测试钉死两点：渲染值逐字原样（防日后有人“减半修复”），
//     以及 UI 文案与文档的承诺与实测语义一致（防承诺漂移回 N 秒即断）。

func r20f03RepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go-backend", "go.mod")); err != nil {
		t.Fatalf("repo root not found from %s: %v", file, err)
	}
	return root
}

func r20f03ReadRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r20f03RepoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestR20F03ConnIdleRenderedVerbatim(t *testing.T) {
	// N=5 必须原样下发：policy connIdle == 5（不是 2.5 也不是 10），
	// flmx-idle-7 出站 userLevel == 其 level。2N 是上游计时粒度，
	// 渲染侧不得以改值“补偿”。
	in := Inbound{ID: 7, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}, ConnIdleSeconds: 5}
	parsed := renderConnIdle(t, TypeXray, []Inbound{in})
	levels := policyLevels(t, parsed)
	if levels["1"].(map[string]interface{})["connIdle"] != float64(5) {
		t.Fatalf("level 1 connIdle = %v, want 5 (verbatim, no halving/doubling)", levels["1"])
	}
	if got := connIdleUserLevel(t, connIdleOutbound(t, parsed, "flmx-idle-7")); got != 1 {
		t.Fatalf("flmx-idle-7 userLevel = %v, want 1", got)
	}
	// 渲染产物里除 policy 外不得再有第二处 connIdle 值（防双写改值）。
	raw, _ := json.Marshal(parsed)
	if strings.Count(string(raw), `"connIdle":5`) != 1 {
		t.Fatalf("rendered config must carry exactly one connIdle:5, got: %s", raw)
	}
}

func TestR20F03IdleCopyStatesWindowedSemantics(t *testing.T) {
	// UI 表单文案必须承诺窗口化语义，不得再承诺“N 秒空闲即断”。
	modal := r20f03ReadRepoFile(t, filepath.Join("vite-frontend", "src", "pages", "inbound", "inbound-form-modal.tsx"))
	if strings.Contains(modal, "连接无数据超过该秒数即断开") {
		t.Fatalf("inbound form still promises N-second idle cutoff: %q", "连接无数据超过该秒数即断开")
	}
	if !strings.Contains(modal, "自末次数据起 1–2 倍该秒数后断开") {
		t.Fatalf("inbound form must state the windowed semantics (自末次数据起 1–2 倍该秒数后断开)")
	}
	// 文档必须同步：不得再写“空闲超过设定秒数后断开”，也不得再把
	// 2× 写成恒定倍数；须写明 N–2N 区间与相位决定因素。
	doc := r20f03ReadRepoFile(t, filepath.Join("doc", "guide", "nodes.md"))
	if strings.Contains(doc, "空闲超过设定秒数后由节点主动断开") {
		t.Fatalf("doc still promises N-second idle cutoff")
	}
	if strings.Contains(doc, "约为设定值的 2 倍") {
		t.Fatalf("doc still frames 2x as a constant factor")
	}
	if !strings.Contains(doc, "N–2N 秒") {
		t.Fatalf("doc must state the N–2N windowed semantics")
	}
}
