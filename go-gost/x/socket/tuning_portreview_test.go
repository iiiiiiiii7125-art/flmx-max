package socket

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// PORT-REVIEW 复核修复批 A lane 回归测试（F-01…F-04，基线 bb66240）。
//
//   - F-01 (PORT-REVIEW-L1-D2S, P2): D2 只修了生成器，已落盘的旧格式
//     回放脚本（mqmajor 条件只有 `-z`，无 `= 0` 分支）仍楔死三路恢复。
//     修复在 restoreTuneQueues 执行前校验脚本格式，旧格式先就地迁移
//     再执行；TestPortReviewStalePreFixReplayScriptMigratesOnRecovery
//     用 L1 §2 的红探针复现（当前生成器输出逐字节降级为旧格式脚本 +
//     transaction + recoverTuneTransaction）。
//   - F-02 (PORT-REVIEW-L1-D3D, P2): D3 只修了 auto，deep 成功消息仍为
//     光秃秃“调优完成”。修复把 tuning_auto.go:220 的条件扩到 deep。
//   - F-03 (PORT-REVIEW-L1-D4F, P3): D4 的 `del` 对外来根具破坏性。
//     修复在 shape.sh 的 del 前加根种类护栏；外来根保留并报错。
//   - F-04 (PORT-REVIEW-L1-D4U2, P3): 旧 restore（sysctl-only）与
//     restoreAuto 并存可误调留整形。修复下线旧动作；
//     TestPortReviewTuneApplyRestoreRejected 覆盖 agent 侧，面板侧见
//     go-backend/internal/http/handler/node_tune_portreview_test.go。

func TestPortReviewStalePreFixReplayScriptMigratesOnRecovery(t *testing.T) {
	// L1 §2 红探针：以当前生成器输出逐字节降级为旧格式脚本（删
	// `|| [ "$mqmajor" = 0 ]`）写入 current-qdisc.sh + transaction，
	// 调 recoverTuneTransaction()。bb66240 上 FAIL（bash 执行失败:
	// exit status 2；Error: Failed to find specified qdisc.，与批次
	// 二真机楔死日志逐字同款）；修复后须 PASS 且 transaction 清掉。
	fakeProcSys(t, "reno cubic bbr")
	oldUnit := tuneShapeUnitPath
	unitDir := t.TempDir()
	tuneShapeUnitPath = func() string { return filepath.Join(unitDir, "flmx-tuning-qdisc.service") }
	t.Cleanup(func() { tuneShapeUnitPath = oldUnit })
	if err := os.MkdirAll(tuneEngineDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	queue := "qdisc mq 0: root\n" +
		"qdisc fq_codel 0: parent :1 limit 10240p flows 1024 quantum 1514 target 5ms interval 100ms memory_limit 32Mb ecn\n" +
		"qdisc fq_codel 0: parent :2 limit 10240p flows 1024 quantum 1514 target 5ms interval 100ms memory_limit 32Mb ecn"
	script, err := tuneQueueReplay("eth0", queue)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `|| [ "$mqmajor" = 0 ]`) {
		t.Fatalf("generator did not emit the D2 format: %s", script)
	}
	// Downgrade byte-for-byte to the pre-D2 format, as L1 did.
	stale := strings.Replace(script, `|| [ "$mqmajor" = 0 ]`, "", 1)
	scriptPath := filepath.Join(tuneEngineDir(), "current-qdisc.sh")
	if err := os.WriteFile(scriptPath, []byte(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	files, err := captureTuneShapeFiles()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveTuneJSON(tuneStatePath("tuning-transaction.json"), files); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tc"), []byte(fakeKernelMQTC), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_TC_STATE", filepath.Join(dir, "tc-state"))
	t.Setenv("FAKE_TC_LOG", filepath.Join(dir, "tc-log"))
	base := tuneCommand
	tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "systemctl" {
			return "", nil
		}
		return base(ctx, name, args...)
	}
	t.Cleanup(func() { tuneCommand = base })
	if err := recoverTuneTransaction(); err != nil {
		t.Fatalf("D2S: recovery with a stale pre-D2 replay script still fails: %v", err)
	}
	if _, err := os.Stat(tuneStatePath("tuning-transaction.json")); !os.IsNotExist(err) {
		t.Fatal("D2S: transaction file not cleaned after recovery")
	}
	// The on-disk script must have been migrated to the D2 format, so
	// the next recovery does not depend on the migration running again.
	migrated, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(migrated), `|| [ "$mqmajor" = 0 ]`) {
		t.Fatalf("D2S: stale script was executed but not migrated on disk:\n%s", migrated)
	}
}

func TestPortReviewDeepJobMessageCountsFailedSettings(t *testing.T) {
	// L1 §3 红探针：预置 Applied=true、24 小时内、带 1 条 warnings
	// 的 detection，桩掉 tuneAutoExecute 为空操作，跑
	// startTuneJob("deep", 10)。bb66240 上 FAIL（消息 "调优完成"）；
	// 修复后消息须含 “1 项参数未生效”。applied:true 语义不动。
	resetTuneTasks(t)
	fakeProcSys(t, "reno cubic bbr")
	d := &tuneDetection{
		Warnings:      []string{"net.core.somaxconn 设置失败: open /proc/sys/net/core/somaxconn: read-only file system"},
		BandwidthMbps: 4000,
		Interface:     "eth0",
		Peer:          "127.0.0.1",
		Port:          5201,
		Family:        "4",
		MeasuredAt:    time.Now().UnixMilli(),
		Applied:       true,
		EstimatedGB:   10,
	}
	if err := saveTuneJSON(tuneStatePath("tuning-result.json"), d); err != nil {
		t.Fatal(err)
	}
	oldPlatform, oldExec := tunePlatform, tuneAutoExecute
	tunePlatform = "linux"
	tuneAutoExecute = func(ctx context.Context, mode string, maxGB float64) error { return nil }
	t.Cleanup(func() { tunePlatform, tuneAutoExecute = oldPlatform, oldExec })
	if _, err := startTuneJob("deep", 10, 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if j := readTuneJob(); j != nil && j.State == "succeeded" {
			if !strings.Contains(j.Message, "1 项参数未生效") {
				t.Fatalf("D3D: deep succeeded message hides the failed setting: %q", j.Message)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("deep job never succeeded: %+v", readTuneJob())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// portReviewOldD4ShapeScript is the exact pre-guard shape.sh the D4
// re-verify run seeded on the test machine (verify-logs-d4/
// shape-4835.sh, md5 88ed07d4deb58241e53f0f0a0bd67890, 333 bytes):
// unconditional `tc qdisc del ... root || true` + add. It is frozen
// here only to pin L1 §4's red evidence — the test below asserts the
// OLD bytes delete a foreign root, so it keeps passing after the fix
// (it runs the frozen artifact, not tuneShapeScript).
const portReviewOldD4ShapeScript = "#!/bin/sh\nset -eu\ntc qdisc del dev 'ens5' root || true\ntc qdisc add dev 'ens5' root handle 1: htb default 10\ntc class replace dev 'ens5' parent 1: classid 1:10 htb rate 4835mbit ceil 4835mbit burst 2417500 cburst 2417500 quantum 1514\ntc qdisc replace dev 'ens5' parent 1:10 handle 10: fq limit 40960 flow_limit 8192 maxrate 4835mbit\n"

func TestPortReviewOldShapeScriptDeletesForeignRoot(t *testing.T) {
	// L1 §4 红对照：外来 1000 Mbit HTB 根（fake 内核建模）——旧脚本
	// RC=0，外来根被删、改为 FLMX 4835。此测试跑冻结的旧字节，修
	// 复前后都应 PASS；它只钉住“旧行为确实具破坏性”这一事实。
	dir := t.TempDir()
	bin, state := writeFakeKernelHTBTC(t, dir)
	writeTCState(t, state, "cake", "8001", "", "0")
	scriptPath := filepath.Join(dir, "shape.sh")
	if err := os.WriteFile(scriptPath, []byte(portReviewOldD4ShapeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runShapeScript(t, scriptPath, bin, state)
	if err != nil {
		t.Fatalf("old shape.sh on a foreign root should have deleted it (red evidence): %v\n%s", err, out)
	}
	got := readTCState(t, state)
	if !strings.Contains(got, "root=htb") || strings.Contains(got, "root=cake") {
		t.Fatalf("old shape.sh did not replace the foreign root: %q", got)
	}
}

func TestPortReviewShapeScriptRefusesForeignRoot(t *testing.T) {
	// D4F：外来根（非本调优装上的 HTB/cake/tbf 等）须保留并报错，
	// 不得删除。bb66240 的 tuneShapeScript 无护栏，在此外来根上
	// RC=0 并删根（见上一个测试的旧字节对照），故本测试在 bb66240
	// 上 FAIL；修复后须 PASS（非零退出、外来根原样保留）。
	dir := t.TempDir()
	bin, state := writeFakeKernelHTBTC(t, dir)
	writeTCState(t, state, "cake", "8001", "", "0")
	script := tuneShapeScript("eth0", 4835)
	scriptPath := filepath.Join(dir, "shape.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("shape.sh fails bash -n: %v\n%s\n%s", err, out, script)
	}
	out, err := runShapeScript(t, scriptPath, bin, state)
	if err == nil {
		t.Fatalf("D4F: shape.sh must refuse a foreign root, but it exited 0 and replaced it:\n%s\n%s", out, script)
	}
	if !strings.Contains(out, "保留") {
		t.Fatalf("D4F: refusal must say the foreign config was kept: %q\n%s", out, script)
	}
	got := readTCState(t, state)
	if !strings.Contains(got, "root=cake") || !strings.Contains(got, "handle=8001") {
		t.Fatalf("D4F: foreign root was not preserved: %q\n%s", got, script)
	}
}

func TestPortReviewTuneApplyRestoreRejected(t *testing.T) {
	// D4U2 (agent 侧)：旧 TuneApply restore=true 只恢复 sysctl、会
	// 保留整形配置，已下线；直调须被拒并提示走恢复原配置
	// （TuneStart mode=restore / 面板 restoreAuto）。
	fakeProcSys(t, "reno cubic bbr")
	if _, err := handleTuneCommand("TuneApply", map[string]interface{}{"restore": true}); err == nil || !strings.Contains(err.Error(), "restoreAuto") {
		t.Fatalf("D4U2: TuneApply restore must be rejected and point at restoreAuto: %v", err)
	}
}
