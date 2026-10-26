package socket

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 批次一（上游 3.3.43 自动调优）回归测试。
//
// 注意：上游 tuning_auto_test.go（582 行、20 个 Test）已随批次二取回
// 原文（~/workspace/port-3343/upstream/tuning_auto_test.go），深扫
// 与自动任务的对照移植见 tuning_deep_test.go；本文件保留批次一
// 按简报主题重写的自动任务测试（样本解析、双轮握手、内存/页大小、
// 组表新增键、任务互斥取消、恢复/transaction、容器检测），其中
// 样本解析已按上游原文补齐接收端与非法输入用例，deep 拒绝用例
// 已随批次二实现删除（见 tuning_deep_test.go 的
// TestTuneDeepRequiresRecentMeasurementAndTrafficBudget）。

// resetTuneTasks clears the in-memory job slot so a previous test's
// finished job cannot leak into the next test's readTuneJob.
func resetTuneTasks(t *testing.T) {
	t.Helper()
	tuningTasks.Lock()
	tuningTasks.job, tuningTasks.cancel, tuningTasks.path = nil, nil, ""
	tuningTasks.Unlock()
}

func TestParseTuneSample(t *testing.T) {
	s, err := parseTuneSample("iperf3 noise\nFLYX_SAMPLE=2803.6 4 2790.1\ntrailer\n")
	if err != nil {
		t.Fatal(err)
	}
	if s.Mbps != 2790.1 || s.Retransmits != 4 || s.LossPercent <= 0 || s.LossPercent > 1 {
		t.Fatalf("sample: %+v", s)
	}
	// Two fields: received defaults to sent.
	s, err = parseTuneSample("FLYX_SAMPLE=100.0 0\n")
	if err != nil || s.Mbps != 100.0 || s.LossPercent != 0 {
		t.Fatalf("two-field sample: %+v %v", s, err)
	}
	// Receiver side wins over the sender side (upstream
	// TestTuneSampleParsingUsesReceiverAndRejectsInvalidData).
	s, err = parseTuneSample("log\nFLYX_SAMPLE=1000 12 940\n")
	if err != nil || s.Mbps != 940 || s.Retransmits != 12 || s.LossPercent <= 0 {
		t.Fatalf("receiver sample: %+v %v", s, err)
	}
	for _, out := range []string{
		"",
		"no sample here\n",
		"FLYX_SAMPLE=0 0\n",       // sent <= 0
		"FLYX_SAMPLE=-5 0\n",      // negative
		"FLYX_SAMPLE=NaN 0\n",     // NaN
		"FLYX_SAMPLE=Inf 0\n",     // Inf
		"FLYX_SAMPLE=100 -1\n",    // negative retransmits
		"FLYX_SAMPLE=abc 0\n",     // unparsable
		"FLYX_SAMPLE=100\n",       // too few fields
		"FLYX_SAMPLE=100 0 bad\n", // bad receiver value
		"FLYX_SAMPLE=NaN 0 20\n",
		"FLYX_SAMPLE=100 0 NaN\n",
		"FLYX_SAMPLE=100 0 nope\n",
		"FLYX_SAMPLE=10 0 0\n", // receiver <= 0
		"FLYX_SAMPLE=+Inf 1\n",
		"FLYX_SAMPLE=oops\n",
	} {
		if _, err := parseTuneSample(out); err == nil {
			t.Errorf("input %q must fail", out)
		}
	}
}

func TestMergeTunePeerPassesTakesMinLatency(t *testing.T) {
	// 探针证据（IPERF3-PROBE-REPORT.md §2）：冷启动握手虚高 80–310 ms，
	// Run 1 sea11 160 ms、ams-clouvider 142 ms；稳态 sea11 仅 8.1 ms。
	// 单轮排序会把首选定在 139 Mbps 的 ams 上，与真实 4140 Mbps 差
	// 约 30 倍。双轮取最小值恢复稳态排序：sea 必须排第一。
	a := []tunePeer{
		{host: "speedtest.sea11.us.leaseweb.net", port: 5201, family: "4", latency: 160 * time.Millisecond},
		{host: "ams.speedtest.clouvider.net", port: 5201, family: "4", latency: 142 * time.Millisecond},
		{host: "speedtest.lax12.us.leaseweb.net", port: 5201, family: "6", latency: 181 * time.Millisecond},
	}
	b := []tunePeer{
		{host: "speedtest.sea11.us.leaseweb.net", port: 5201, family: "4", latency: 8 * time.Millisecond},
		{host: "ams.speedtest.clouvider.net", port: 5201, family: "4", latency: 140 * time.Millisecond},
		{host: "speedtest.sfo12.us.leaseweb.net", port: 5201, family: "6", latency: 25 * time.Millisecond},
	}
	merged := mergeTunePeerPasses(a, b)
	if len(merged) != 4 {
		t.Fatalf("merged: %+v", merged)
	}
	if merged[0].host != "speedtest.sea11.us.leaseweb.net" || merged[0].latency != 8*time.Millisecond {
		t.Fatalf("first must be warmed sea11: %+v", merged[0])
	}
	if merged[1].host != "speedtest.sfo12.us.leaseweb.net" || merged[2].host != "ams.speedtest.clouvider.net" || merged[3].host != "speedtest.lax12.us.leaseweb.net" {
		t.Fatalf("order: %+v", merged)
	}
	// Same host, different family are distinct entries.
	c := mergeTunePeerPasses(
		[]tunePeer{{host: "h", port: 5201, family: "4", latency: 10 * time.Millisecond}},
		[]tunePeer{{host: "h", port: 5201, family: "6", latency: 9 * time.Millisecond}},
	)
	if len(c) != 2 || c[0].family != "6" {
		t.Fatalf("family entries: %+v", c)
	}
	// Empty passes stay empty; a peer seen in one pass is kept.
	if got := mergeTunePeerPasses(nil, nil); len(got) != 0 {
		t.Fatalf("empty: %+v", got)
	}
}

func TestTuneSizesMissingMemoryCapped(t *testing.T) {
	// D43-T5：内存读不到时预算兜底 4 MiB，永不去掉上限（上游
	// TestTuneMissingMemoryCannotCreateUnboundedBuffer）。旧行为
	// （3.3.42）是无上限，tuning_bdp_test.go 的 "unknown memory"
	// 用例随此改断言。
	s := tuneSizesFor(tuneParams{BandwidthMbps: 1000, RTTMs: 150}, 0)
	if s.BufMax != 4*mib || s.Reason != "受内存限制" {
		t.Fatalf("missing memory: %+v", s)
	}
	if s.TCPMem != "4096 8192 16384" {
		t.Fatalf("tcp_mem floors: %q", s.TCPMem)
	}
}

func TestTuneSizesPageSize(t *testing.T) {
	// D43-T5：页大小走 os.Getpagesize()，tcp_mem 下限改为 16/32/64 MiB。
	old := tunePageSize
	tunePageSize = func() int { return 16384 }
	t.Cleanup(func() { tunePageSize = old })
	s := tuneSizesFor(tuneParams{BandwidthMbps: 100, RTTMs: 100}, 128)
	if s.TCPMem != "1024 2048 4096" {
		t.Fatalf("16K pages tcp_mem: %q", s.TCPMem)
	}
	// 4096 页时同一输入的下限是 4096/8192/16384（见 BDP 用例 "tiny"）。
	tunePageSize = func() int { return 4096 }
	if s := tuneSizesFor(tuneParams{BandwidthMbps: 100, RTTMs: 100}, 128); s.TCPMem != "4096 8192 16384" {
		t.Fatalf("4K pages tcp_mem: %q", s.TCPMem)
	}
}

func TestTuneGroupsNewKeys(t *testing.T) {
	// D43-T5 新增的 11 个设置项（简报称 9 键/10 项，逐字枚举实为
	// 11 个名字，全部搬运；见批次一报告 §D43-T5）。
	groups := tuneGroupsFor(tuneSizesFor(defaultTuneParams, 2048))
	got := map[string]tuneSetting{}
	for _, g := range groups {
		for _, s := range g.Settings {
			got[s.Key] = s
		}
	}
	bufDef := "1048576"
	for _, c := range []struct {
		key     string
		value   string
		atLeast bool
	}{
		{"net.core.rmem_default", bufDef, false},
		{"net.core.wmem_default", bufDef, false},
		{"net.ipv4.tcp_window_scaling", "1", false},
		{"net.ipv4.tcp_moderate_rcvbuf", "1", false},
		{"net.ipv4.tcp_adv_win_scale", "1", false},
		{"net.ipv4.tcp_no_metrics_save", "0", false},
		{"net.ipv4.tcp_sack", "1", false},
		{"net.ipv4.tcp_dsack", "1", false},
		{"net.ipv4.tcp_timestamps", "1", false},
		{"net.core.netdev_budget", "600", true},
		{"net.core.optmem_max", "65536", true},
	} {
		s, ok := got[c.key]
		if !ok {
			t.Errorf("missing key %s", c.key)
			continue
		}
		if s.Value != c.value || s.AtLeast != c.atLeast {
			t.Errorf("%s: %+v want value=%s atLeast=%v", c.key, s, c.value, c.atLeast)
		}
	}
}

func TestSaveLoadTuneDetection(t *testing.T) {
	fakeProcSys(t, "cubic bbr")
	d := &tuneDetection{BandwidthMbps: 4140, BandwidthSource: "iperf3 四并发实测", Interface: "ens5", Peer: "speedtest.sea11.us.leaseweb.net", Port: 5201, Family: "6", Applied: true}
	path := tuneStatePath("tuning-result.json")
	if err := saveTuneJSON(path, d); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("result mode: %v %v", fi, err)
	}
	back := loadTuneDetection()
	if back == nil || back.BandwidthMbps != 4140 || back.Peer != d.Peer || !back.Applied {
		t.Fatalf("roundtrip: %+v", back)
	}
	// Missing file loads as nil (separate dir, no result written).
	dir := t.TempDir()
	old := tuneBackupPath
	tuneBackupPath = filepath.Join(dir, "tuning-backup.json")
	t.Cleanup(func() { tuneBackupPath = old })
	if loadTuneDetection() != nil {
		t.Fatal("missing result must be nil")
	}
}

func TestReadTuneJobRestartMarksFailed(t *testing.T) {
	resetTuneTasks(t)
	fakeProcSys(t, "cubic bbr")
	path := tuneStatePath("tuning-job.json")
	j := &tuneJob{ID: "abc", Mode: "auto", State: "running", Stage: "measuring", Percent: 25, Message: "实测出口带宽", StartedAt: time.Now().UnixMilli()}
	if err := saveTuneJSON(path, j); err != nil {
		t.Fatal(err)
	}
	back := readTuneJob()
	if back == nil || back.State != "failed" || !strings.Contains(back.Message, "Agent 已重启") || back.FinishedAt == 0 {
		t.Fatalf("restart rewrite: %+v", back)
	}
	// The rewrite is persisted: a second read sees the failed state.
	if again := readTuneJob(); again == nil || again.State != "failed" {
		t.Fatalf("second read: %+v", again)
	}
}

func TestTuneLogWriterCaps(t *testing.T) {
	resetTuneTasks(t)
	tuningTasks.Lock()
	tuningTasks.job = &tuneJob{ID: "x", State: "running"}
	tuningTasks.Unlock()
	t.Cleanup(func() { resetTuneTasks(t) })
	w := &tuneLogWriter{}
	line := strings.Repeat("y", 100) + "\n"
	for i := 0; i < 400; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	tuningTasks.Lock()
	logs := tuningTasks.job.Logs
	tuningTasks.Unlock()
	if len(logs) > 32768 || len(logs) < 16384 {
		t.Fatalf("job logs not capped to a 16K tail: %d bytes", len(logs))
	}
	if tail := w.tail(); len([]rune(tail)) > 301 {
		t.Fatalf("tail too long: %d runes", len([]rune(tail)))
	}
}

func TestStartTuneJobRejectsNonLinuxAndUnknownMode(t *testing.T) {
	resetTuneTasks(t)
	fakeProcSys(t, "cubic bbr")
	old := tunePlatform
	tunePlatform = "windows"
	t.Cleanup(func() { tunePlatform = old })
	if _, err := startTuneJob("auto", 0); err == nil || !strings.Contains(err.Error(), "Linux") {
		t.Fatalf("non-linux: %v", err)
	}
	tunePlatform = "linux"
	for _, mode := range []string{"", "bogus", "AUTO"} {
		if _, err := startTuneJob(mode, 0); err == nil || !strings.Contains(err.Error(), "未知调优模式") {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
}

func TestStartTuneJobCancel(t *testing.T) {
	resetTuneTasks(t)
	fakeProcSys(t, "cubic bbr")
	oldPlatform, oldExec := tunePlatform, tuneAutoExecute
	tunePlatform = "linux"
	started := make(chan struct{})
	var once sync.Once
	tuneAutoExecute = func(ctx context.Context, mode string, maxGB float64) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { tunePlatform, tuneAutoExecute = oldPlatform, oldExec })

	st, err := startTuneJob("auto", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Automatic || st.Job == nil || st.Job.State != "running" {
		t.Fatalf("start status: %+v", st)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("job goroutine did not start")
	}
	// A second job while one runs is refused (single-flight).
	if _, err := startTuneJob("auto", 0); err == nil || !strings.Contains(err.Error(), "已有调优任务正在运行") {
		t.Fatalf("second start: %v", err)
	}
	cancelTuneJob()
	// Wait for the goroutine to record the cancelled state.
	deadline := time.Now().Add(5 * time.Second)
	for {
		j := readTuneJob()
		if j != nil && j.State == "cancelled" {
			if !strings.Contains(j.Message, "已应用的基础参数保留") {
				t.Fatalf("cancel message: %q", j.Message)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never cancelled: %+v", j)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestHandleTuneCommandStartCancel(t *testing.T) {
	resetTuneTasks(t)
	fakeProcSys(t, "cubic bbr")
	oldPlatform, oldExec := tunePlatform, tuneAutoExecute
	tunePlatform = "linux"
	tuneAutoExecute = func(ctx context.Context, mode string, maxGB float64) error { return nil }
	t.Cleanup(func() { tunePlatform, tuneAutoExecute = oldPlatform, oldExec })

	if _, err := handleTuneCommand("TuneStart", map[string]interface{}{"mode": "bogus"}); err == nil {
		t.Fatal("bogus mode must fail")
	}
	if _, err := handleTuneCommand("TuneStart", map[string]interface{}{"mode": "deep", "maxTrafficGB": 100.0}); err == nil || !strings.Contains(err.Error(), "请先完成自动调优") {
		t.Fatalf("deep via command without a recent measurement: %v", err)
	}
	out, err := handleTuneCommand("TuneStart", map[string]interface{}{"mode": "auto"})
	if err != nil {
		t.Fatal(err)
	}
	st, ok := out.(tuneStatus)
	if !ok || !st.Automatic || st.Job == nil {
		t.Fatalf("start status: %+v", out)
	}
	// The fast stub finishes at once; the persisted job ends succeeded.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if j := readTuneJob(); j != nil && j.State == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never succeeded: %+v", readTuneJob())
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Cancel on an idle agent is a no-op that still returns status.
	if _, err := handleTuneCommand("TuneCancel", nil); err != nil {
		t.Fatalf("idle cancel: %v", err)
	}
	// Unknown commands keep the old wording (panel translates it).
	if _, err := handleTuneCommand("TuneNope", nil); err == nil || !strings.Contains(err.Error(), "未知命令类型") {
		t.Fatalf("unknown: %v", err)
	}
}

func TestCallTuneEngineBridge(t *testing.T) {
	fakeProcSys(t, "cubic bbr")
	// No bridge file on disk: the call must fail, never silently pass.
	d := &tuneDetection{Interface: "ens5", Peer: "speedtest.sea11.us.leaseweb.net", Port: 5201, Family: "6"}
	if _, err := callTuneEngine(context.Background(), "sample", d); err == nil {
		t.Fatal("missing bridge must fail")
	}
	// With a bridge file present, the command runs through tuneCommand.
	oldCmd := tuneCommand
	tuneCommand = func(_ context.Context, name string, args ...string) (string, error) {
		if name == "bash" && len(args) > 2 && args[2] == "sample" {
			return "FLYX_SAMPLE=100.0 0\n", nil
		}
		return "", nil
	}
	t.Cleanup(func() { tuneCommand = oldCmd })
	if err := os.MkdirAll(tuneEngineDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tuneEngineDir(), "bridge.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := callTuneEngine(context.Background(), "sample", d, "4")
	if err != nil || !strings.Contains(out, "FLYX_SAMPLE=") {
		t.Fatalf("bridge call: %q %v", out, err)
	}
}

func TestExecuteTuneJobRestore(t *testing.T) {
	resetTuneTasks(t)
	fakeProcSys(t, "cubic bbr")
	oldUnit := tuneShapeUnitPath
	dir := t.TempDir()
	tuneShapeUnitPath = func() string { return filepath.Join(dir, "flmx-tuning-qdisc.service") }
	t.Cleanup(func() { tuneShapeUnitPath = oldUnit })
	// Tune something first, then restore via the job entry point.
	if _, err := applyTuning([]string{"buffer", "forward"}, defaultTuneParams); err != nil {
		t.Fatal(err)
	}
	if v, _ := readSysctl("net.ipv4.ip_forward"); v != "1" {
		t.Fatalf("forward not applied: %q", v)
	}
	if err := executeTuneJob(context.Background(), "restore", 0); err != nil {
		t.Fatalf("restore job: %v", err)
	}
	if v, _ := readSysctl("net.ipv4.ip_forward"); v != "0" {
		t.Fatalf("forward not restored: %q", v)
	}
	if _, err := os.Stat(tuneStatePath("tuning-result.json")); !os.IsNotExist(err) {
		t.Fatal("result file must be removed by restore")
	}
}

func TestRecoverTuneTransaction(t *testing.T) {
	resetTuneTasks(t)
	fakeProcSys(t, "cubic bbr")
	// No transaction file: nil.
	if err := recoverTuneTransaction(); err != nil {
		t.Fatalf("no txn: %v", err)
	}
	// Corrupt transaction: error, never a silent pass.
	path := tuneStatePath("tuning-transaction.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverTuneTransaction(); err == nil {
		t.Fatal("corrupt txn must fail")
	}
	_ = os.Remove(path)

	// Roundtrip: captured files are restored and the record removed.
	oldUnit := tuneShapeUnitPath
	oldCmd := tuneCommand
	dir := t.TempDir()
	tuneShapeUnitPath = func() string { return filepath.Join(dir, "flmx-tuning-qdisc.service") }
	calls := 0
	tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		calls++
		return "", nil
	}
	t.Cleanup(func() { tuneShapeUnitPath, tuneCommand = oldUnit, oldCmd })
	engineDir := tuneEngineDir()
	shapePath := filepath.Join(engineDir, "shape.sh")
	if err := os.MkdirAll(engineDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// restoreTuneQueues replays current-qdisc.sh through tuneCommand
	// (stubbed above), so the file only needs to exist for the txn.
	if err := os.WriteFile(filepath.Join(engineDir, "current-qdisc.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	files, err := captureTuneShapeFiles()
	if err != nil {
		t.Fatal(err)
	}
	// The engine dir has no shape.sh yet in batch 1: capture records
	// Exists=false for it; add a real one to prove restore writes it.
	if err := os.WriteFile(shapePath, []byte("#!/bin/sh\n# committed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	files, err = captureTuneShapeFiles()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveTuneJSON(path, files); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(shapePath); err != nil {
		t.Fatal(err)
	}
	if err := recoverTuneTransaction(); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if b, err := os.ReadFile(shapePath); err != nil || string(b) != "#!/bin/sh\n# committed\n" {
		t.Fatalf("shape not restored: %q %v", b, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("txn file must be removed after recovery")
	}
	if calls == 0 {
		t.Fatal("queue replay was never invoked")
	}
}

func TestRestoreAutomaticTuningSysctl(t *testing.T) {
	resetTuneTasks(t)
	fakeProcSys(t, "cubic bbr")
	oldUnit := tuneShapeUnitPath
	dir := t.TempDir()
	tuneShapeUnitPath = func() string { return filepath.Join(dir, "flmx-tuning-qdisc.service") }
	t.Cleanup(func() { tuneShapeUnitPath = oldUnit })
	if _, err := applyTuning([]string{"buffer", "forward"}, defaultTuneParams); err != nil {
		t.Fatal(err)
	}
	// A detection result exists (as an auto run would leave it).
	if err := saveTuneJSON(tuneStatePath("tuning-result.json"), &tuneDetection{BandwidthMbps: 1000, Applied: true}); err != nil {
		t.Fatal(err)
	}
	if err := restoreAutomaticTuning(context.Background()); err != nil {
		t.Fatalf("restoreAuto: %v", err)
	}
	if v, _ := readSysctl("net.ipv4.ip_forward"); v != "0" {
		t.Fatalf("forward not restored: %q", v)
	}
	if _, err := os.Stat(tuneBackupPath); !os.IsNotExist(err) {
		t.Fatal("backup must be gone after full restore")
	}
	if _, err := os.Stat(tuneStatePath("tuning-result.json")); !os.IsNotExist(err) {
		t.Fatal("result must be gone after restore")
	}
}

func TestInContainer(t *testing.T) {
	oldMarker, oldCgroup := containerMarkerPath, containerCgroupPath
	t.Cleanup(func() { containerMarkerPath, containerCgroupPath = oldMarker, oldCgroup })
	dir := t.TempDir()
	marker := filepath.Join(dir, ".dockerenv")
	cgroup := filepath.Join(dir, "cgroup")
	containerMarkerPath, containerCgroupPath = marker, cgroup
	if InContainer() {
		t.Fatal("no markers must be false")
	}
	if err := os.WriteFile(cgroup, []byte("12:devices:/docker/abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !InContainer() {
		t.Fatal("docker cgroup must be true")
	}
	if err := os.Remove(cgroup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if !InContainer() {
		t.Fatal("marker file must be true")
	}
}
