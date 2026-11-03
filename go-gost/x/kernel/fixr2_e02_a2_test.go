package kernel

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// R2-F2-E02 钉格：老内核拒绝全部 hop 规则（无 nft_fib）时不得连坐主
// 规则——受白名单守卫的内核必须照常启动，hop 失败只以其实情点名，
// 不得再顶「入站白名单规则同步失败」的错位文案。修复前：syncGuard 的
// hopErr 直接喂给 startBlocked/nftGuardFailure，内核被拦停。

func TestFixR2E02IsolateHopSync(t *testing.T) {
	hopErr := error(&hopRulesError{msg: "全部 2 条 hop 规则被内核拒绝，端口跳跃未生效（计数与白名单规则已照常下发）: hop[0]"})
	gate, note := isolateHopSync(hopErr)
	if gate != nil {
		t.Fatalf("hop-only failure must not gate, got %v", gate)
	}
	if !strings.Contains(note, "hop 规则被内核拒绝") {
		t.Fatalf("hop note must carry the real cause, got %q", note)
	}
	// errors.As 语义：被包装的 hop 错误同样隔离。
	gate, note = isolateHopSync(fmt.Errorf("sync: %w", hopErr))
	if gate != nil || !strings.Contains(note, "hop 规则被内核拒绝") {
		t.Fatalf("wrapped hop error must isolate too, got gate=%v note=%q", gate, note)
	}
	plain := errors.New("nft load failed")
	gate, note = isolateHopSync(plain)
	if gate != plain || note != "" {
		t.Fatalf("plain errors must pass through untouched, got gate=%v note=%q", gate, note)
	}
	gate, note = isolateHopSync(nil)
	if gate != nil || note != "" {
		t.Fatalf("nil must stay nil, got gate=%v note=%q", gate, note)
	}
}

func TestFixR2E02HopFailureDoesNotBlockGuardedStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	rec := stubNft(t)
	stubHopCheck(t, "fib") // old kernel: every hop rule rejected
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	req := whitelistRequest("xray", "9.9.9", "xray", "cfg-hop", []string{"203.0.113.0/24"})
	req.Ports = append(req.Ports, Port{InboundID: 8, Port: 8443, Network: "udp", HopFrom: 20000, HopTo: 30000})
	st, err := m.Apply(req)
	if err != nil {
		t.Fatalf("hop-only failure must not fail the apply: %v", err)
	}
	if !st.Running {
		t.Fatalf("guarded kernel must start despite hop rejection, state=%+v", st)
	}
	// 主规则（白名单＋计数）必须真实下发。
	last := rec.scripts[len(rec.scripts)-1]
	for _, want := range []string{"allow4_7", "in_7_tcp"} {
		if !strings.Contains(last, want) {
			t.Fatalf("main ruleset must be applied despite hop rejection, missing %q:\n%s", want, last)
		}
	}
	logs := strings.Join(m.Status("xray").Kernels[0].Logs, "\n")
	if !strings.Contains(logs, "hop 规则被内核拒绝") {
		t.Fatalf("instance log must name the hop failure as what it is, got:\n%s", logs)
	}
	for _, bad := range []string{"入站白名单规则同步失败", "流量计数规则同步失败"} {
		if strings.Contains(logs, bad) {
			t.Fatalf("hop failure must not be misreported as %q, got:\n%s", bad, logs)
		}
	}
}

// R2-F2-A2（agent 侧）钉格：请求仍带未解析的 "latest" 且二进制已在位
// 时，ensureBinary 会跳过下载——这次跳过必须在实例日志里点名，不得
// 让「安装最新版」静默空转。具体版本在位则照旧不点名。
func TestFixR2A2AgentLatestSkipNamed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubNft(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "latest", "xray", marker)
	placeMarkerKernel(t, m, "singbox", "1.0.0", "sing-box", marker)

	req := whitelistRequest("xray", "latest", "xray", "cfg-latest", nil)
	if st, err := m.Apply(req); err != nil || !st.Running {
		t.Fatalf("apply latest: state=%+v err=%v", st, err)
	}
	logs := strings.Join(m.Status("xray").Kernels[0].Logs, "\n")
	if !strings.Contains(logs, "请求版本为 latest 且二进制已存在，本次未重新下载") {
		t.Fatalf("latest skip must be named in the instance log, got:\n%s", logs)
	}

	req2 := whitelistRequest("singbox", "1.0.0", "sing-box", "cfg-sb", nil)
	if st, err := m.Apply(req2); err != nil || !st.Running {
		t.Fatalf("apply concrete: state=%+v err=%v", st, err)
	}
	logs2 := strings.Join(m.Status("singbox").Kernels[0].Logs, "\n")
	if strings.Contains(logs2, "未重新下载") {
		t.Fatalf("concrete-version applies must not carry the latest-skip note, got:\n%s", logs2)
	}
}
