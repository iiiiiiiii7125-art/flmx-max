package kernel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// F-032 回归：旧内核处于崩溃退避间隙（进程瞬时未运行）时下发新坏配置，
// 回滚判定不得依赖下发瞬间的进程快照——旧配置曾确认可用就必须回滚，
// 且未确认可用的坏配置不得被 persist 固化进 state.json。
func TestApplyRollsBackWhenPrevMomentarilyDown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubNft(t)
	srv, _ := serveKernel(t, zipWith(t, map[string]string{"xray": fakeKernel}))
	base := t.TempDir()
	m := NewManager(base)
	defer m.Shutdown()

	st, err := m.Apply(applyRequest(srv, `{"v":1}`, true))
	if err != nil || !st.Running {
		t.Fatalf("apply v1: %+v %v", st, err)
	}

	// 模拟退避间隙：v1 进程此刻不在运行（崩溃后 supervisor 退避中），
	// 但 v1 本身是已确认可用的配置。
	m.mu.Lock()
	inst := m.instances["xray"]
	m.mu.Unlock()
	m.stopProcesses(inst)

	st, err = m.Apply(applyRequest(srv, `{"crash":true}`, true))
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("坏配置必须回滚到 v1 并说明已恢复, got err=%v state=%+v", err, st)
	}
	if !st.Running {
		t.Fatalf("回滚后内核必须在运行: %+v", st)
	}
	cfg, _ := os.ReadFile(filepath.Join(base, "xray", "run", "config.json"))
	if string(cfg) != `{"v":1}` {
		t.Fatalf("回滚后配置文件 = %q, want v1", cfg)
	}

	// state.json 只能含 v1，不得固化坏配置。
	var reqs []ApplyRequest
	if b, rerr := os.ReadFile(filepath.Join(base, "state.json")); rerr == nil {
		if jerr := json.Unmarshal(b, &reqs); jerr != nil {
			t.Fatalf("state.json 解析失败: %v", jerr)
		}
	}
	for _, r := range reqs {
		if r.Hash == hashString(`{"crash":true}`) {
			t.Fatalf("坏配置被 persist 固化进 state.json: %+v", reqs)
		}
	}
}

// F-032 回归（另一半）：从未成功运行过的坏配置在 Apply 失败后不得写进
// state.json，否则 agent 重启 Restore 会反复拉起坏配置。
func TestApplyDoesNotPersistNeverRanConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubNft(t)
	srv, _ := serveKernel(t, zipWith(t, map[string]string{"xray": fakeKernel}))
	base := t.TempDir()
	m := NewManager(base)
	defer m.Shutdown()

	if _, err := m.Apply(applyRequest(srv, `{"crash":true}`, true)); err == nil {
		t.Fatal("坏配置首次 Apply 必须报错")
	}
	b, err := os.ReadFile(filepath.Join(base, "state.json"))
	if os.IsNotExist(err) {
		return // 从未落盘同样合格：坏配置没有被固化。
	}
	if err != nil {
		t.Fatal(err)
	}
	var reqs []ApplyRequest
	if jerr := json.Unmarshal(b, &reqs); jerr != nil {
		t.Fatalf("state.json 解析失败: %v", jerr)
	}
	for _, r := range reqs {
		if r.Hash == hashString(`{"crash":true}`) {
			t.Fatalf("从未运行成功的坏配置被 persist 固化: %+v", reqs)
		}
	}
}
