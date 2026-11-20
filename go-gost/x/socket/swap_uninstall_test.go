package socket

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// R18-D2-02：卸载须处理面板托管的 /swapfile 与 fstab 行。托管
// 认定取严格口径——只认规范行「<file> none swap sw 0 0」；
// 无规范行但在用/在盘的 /swapfile 保留并披露（不删）。
// swapoff 不安全（已用 > 可用内存，或可用内存读不到）时同样保
// 留并披露，步骤判成功（Skipped）；只有真正删不掉才判失败。

func TestIsCanonicalManagedFstabLine(t *testing.T) {
	const fp = "/swapfile"
	cases := []struct {
		line string
		want bool
	}{
		{"/swapfile none swap sw 0 0", true},
		{"/swapfile  none   swap  sw  0  0", true},  // 空白形态不敏感
		{"  /swapfile none swap sw 0 0  ", true},    // 首尾空白不敏感
		{"/swapfile none swap defaults 0 0", false}, // 非规范参数
		{"/swapfile swap swap sw 0 0", false},       // 挂载点不是 none
		{"/swapfile none swap sw 0 1", false},       // pass 字段不同
		{"#/swapfile none swap sw 0 0", false},      // 注释行
		{"/swapfile none swap sw 0 0 # 备注", false},  // 带行尾注释
		{"/dev/vda2 none swap sw 0 0", false},       // 他方条目
		{"", false},
	}
	for _, c := range cases {
		if got := isCanonicalManagedFstabLine(c.line, fp); got != c.want {
			t.Fatalf("line %q: got %v, want %v", c.line, got, c.want)
		}
	}
}

func TestRemoveManagedFstabLines(t *testing.T) {
	const fp = "/swapfile"
	const canonical = "/swapfile none swap sw 0 0\n"
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{"no line is a no-op", "UUID=root / ext4 defaults 0 1\n", "UUID=root / ext4 defaults 0 1\n", false},
		{"canonical removed, others verbatim", "UUID=root / ext4 defaults 0 1\n" + canonical + "UUID=boot /boot vfat defaults 0 2\n",
			"UUID=root / ext4 defaults 0 1\nUUID=boot /boot vfat defaults 0 2\n", true},
		{"non-canonical variant kept", "/swapfile none swap defaults 0 0\n", "/swapfile none swap defaults 0 0\n", false},
		{"commented canonical kept", "#/swapfile none swap sw 0 0\n", "#/swapfile none swap sw 0 0\n", false},
		{"other swap entry kept", "UUID=partswap none swap sw 0 0\n" + canonical, "UUID=partswap none swap sw 0 0\n", true},
		{"duplicates all removed", canonical + canonical, "", true},
		{"only line removed", canonical, "", true},
		{"missing trailing newline normalized on change", "UUID=root / ext4 defaults 0 1\n/swapfile none swap sw 0 0",
			"UUID=root / ext4 defaults 0 1\n", true},
	}
	for _, c := range cases {
		got, changed := removeManagedFstabLines(c.in, fp)
		if got != c.want || changed != c.changed {
			t.Fatalf("%s: got (%q, %v), want (%q, %v)", c.name, got, changed, c.want, c.changed)
		}
	}
}

// writeSwapUninstallFstab 给清理测试布 fstab：canonical 行用 sim
// 自己的 filePath 拼，避免与真实 /swapfile 混淆。
func writeSwapUninstallFstab(t *testing.T, env swapEnv, lines ...string) {
	t.Helper()
	if err := os.WriteFile(env.fstabPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func canonicalSwapLine(sim *swapSim) string {
	return sim.filePath + " none swap sw 0 0"
}

func TestCleanupManagedSwapDeletesActiveSafe(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240) // 已用 10MB，可用 4000MB，安全
	writeSwapUninstallFstab(t, env,
		"UUID=root / ext4 defaults 0 1",
		canonicalSwapLine(sim),
		"UUID=boot /boot vfat defaults 0 2",
	)

	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 0 || len(rep.Skipped) != 0 {
		t.Fatalf("report = %+v, want clean removal", rep)
	}
	if len(rep.Removed) != 2 {
		t.Fatalf("removed = %v, want the fstab line and the file", rep.Removed)
	}
	if strings.Join(sim.calls, ",") != "swapoff" {
		t.Fatalf("only swapoff may run, got %v", sim.calls)
	}
	if _, err := os.Stat(sim.filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("swapfile must be gone")
	}
	fstab, _ := os.ReadFile(env.fstabPath)
	want := "UUID=root / ext4 defaults 0 1\nUUID=boot /boot vfat defaults 0 2\n"
	if string(fstab) != want {
		t.Fatalf("fstab must keep only unrelated lines:\n%s\nwant:\n%s", fstab, want)
	}
	raw, _ := os.ReadFile(sim.swapsPath)
	if strings.Contains(string(raw), sim.filePath) {
		t.Fatalf("entry must be gone from swaps:\n%s", raw)
	}
}

func TestCleanupManagedSwapSkipsUnsafe(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	// 已用 600MB（614400 KiB），MemAvailable 改小到 500MB。
	setupActiveSwapFile(t, sim, 1024, 614400)
	meminfo := "MemTotal:        8192000 kB\nMemAvailable:    512000 kB\n"
	if err := os.WriteFile(env.meminfoPath, []byte(meminfo), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSwapUninstallFstab(t, env, canonicalSwapLine(sim))

	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 0 {
		t.Fatalf("unsafe retain must not fail the step: %+v", rep)
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0], "600") || !strings.Contains(rep.Skipped[0], "500") {
		t.Fatalf("skipped reason must carry both numbers: %+v", rep)
	}
	if len(sim.calls) != 0 {
		t.Fatalf("no command may run on unsafe retain, got %v", sim.calls)
	}
	if _, err := os.Stat(sim.filePath); err != nil {
		t.Fatalf("swapfile must be retained: %v", err)
	}
	fstab, _ := os.ReadFile(env.fstabPath)
	if !strings.Contains(string(fstab), canonicalSwapLine(sim)) {
		t.Fatalf("fstab line must be retained:\n%s", fstab)
	}
}

func TestCleanupManagedSwapSkipsUnknownMemAvail(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240) // 已用 10MB
	// meminfo 无 MemAvailable：无法确认 swapoff 安全。
	if err := os.WriteFile(env.meminfoPath, []byte("MemTotal:        8192000 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSwapUninstallFstab(t, env, canonicalSwapLine(sim))

	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 0 || len(rep.Skipped) != 1 {
		t.Fatalf("unknown MemAvailable must skip without failing: %+v", rep)
	}
	if !strings.Contains(rep.Skipped[0], "MemAvailable") {
		t.Fatalf("skipped reason must name MemAvailable: %+v", rep)
	}
	if len(sim.calls) != 0 {
		t.Fatalf("no command may run, got %v", sim.calls)
	}
	if _, err := os.Stat(sim.filePath); err != nil {
		t.Fatalf("swapfile must be retained: %v", err)
	}
}

func TestCleanupManagedSwapSkipsNonCanonical(t *testing.T) {
	// 在用文件＋非规范 fstab 行：疑似人工/他方创建，保留并披露。
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	writeSwapUninstallFstab(t, env, sim.filePath+" none swap defaults 0 0")

	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 0 || len(rep.Skipped) != 1 {
		t.Fatalf("non-canonical active file must skip without failing: %+v", rep)
	}
	for _, want := range []string{"fstab", "保留", "人工"} {
		if !strings.Contains(rep.Skipped[0], want) {
			t.Fatalf("skipped reason must carry %q: %+v", want, rep)
		}
	}
	if len(sim.calls) != 0 {
		t.Fatalf("no command may run, got %v", sim.calls)
	}
	if _, err := os.Stat(sim.filePath); err != nil {
		t.Fatalf("swapfile must be retained: %v", err)
	}

	// 在用文件＋fstab 完全无行：同样保留。
	env2, sim2 := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim2, 1024, 10240)
	rep = env2.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 0 || len(rep.Skipped) != 1 {
		t.Fatalf("active file without any fstab line must skip: %+v", rep)
	}
	if _, err := os.Stat(sim2.filePath); err != nil {
		t.Fatalf("swapfile must be retained: %v", err)
	}
}

func TestCleanupManagedSwapDeletesInactiveFile(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	// 在盘未启用：不须 swapoff，直接删文件＋行。
	if err := makeSparse(sim.filePath, 1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	writeSwapUninstallFstab(t, env, canonicalSwapLine(sim))

	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 0 || len(rep.Skipped) != 0 {
		t.Fatalf("report = %+v, want clean removal", rep)
	}
	if len(sim.calls) != 0 {
		t.Fatalf("inactive file needs no swapoff, got %v", sim.calls)
	}
	if _, err := os.Stat(sim.filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("swapfile must be gone")
	}
	fstab, _ := os.ReadFile(env.fstabPath)
	if strings.Contains(string(fstab), sim.filePath) {
		t.Fatalf("fstab line must be gone:\n%s", fstab)
	}
}

func TestCleanupManagedSwapRemovesStaleFstabLine(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	// 文件已不在盘、只有规范行残留：只删行。
	writeSwapUninstallFstab(t, env,
		"UUID=root / ext4 defaults 0 1",
		canonicalSwapLine(sim),
	)

	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 0 || len(rep.Skipped) != 0 {
		t.Fatalf("report = %+v, want clean removal", rep)
	}
	if len(rep.Removed) != 1 || !strings.Contains(rep.Removed[0], "fstab") {
		t.Fatalf("removed = %v, want only the fstab line", rep.Removed)
	}
	if len(sim.calls) != 0 {
		t.Fatalf("no command may run, got %v", sim.calls)
	}
	fstab, _ := os.ReadFile(env.fstabPath)
	if strings.Contains(string(fstab), sim.filePath) {
		t.Fatalf("fstab line must be gone:\n%s", fstab)
	}
}

func TestCleanupManagedSwapNoopWhenAbsent(t *testing.T) {
	env, _ := newSwapSimEnv(t, swapsHeader)
	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Removed) != 0 || len(rep.Skipped) != 0 || len(rep.Failed) != 0 {
		t.Fatalf("absent swap must be a silent no-op: %+v", rep)
	}
}

func TestCleanupManagedSwapSwapoffFailure(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	writeSwapUninstallFstab(t, env, canonicalSwapLine(sim))
	sim.failAt["swapoff"] = errors.New("swapoff: 模拟失败")

	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 1 {
		t.Fatalf("swapoff failure must fail the step: %+v", rep)
	}
	for _, want := range []string{"关闭 Swap", "仍在启用", "人工"} {
		if !strings.Contains(rep.Failed[0], want) {
			t.Fatalf("failure must carry %q: %+v", want, rep)
		}
	}
	// 失败序是先断持久化、再 swapoff：行已删、文件与在用条目
	// 都在，失败文案须与该实况一致。
	fstab, _ := os.ReadFile(env.fstabPath)
	if strings.Contains(string(fstab), sim.filePath) {
		t.Fatalf("fstab line is removed before swapoff:\n%s", fstab)
	}
	if _, err := os.Stat(sim.filePath); err != nil {
		t.Fatalf("swapfile must be retained after swapoff failure: %v", err)
	}
	raw, _ := os.ReadFile(sim.swapsPath)
	if !strings.Contains(string(raw), sim.filePath) {
		t.Fatalf("entry must stay active after swapoff failure:\n%s", raw)
	}
}

func TestCleanupManagedSwapFstabUnreadableSkips(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	// fstab 路径指向目录：读不到就无法确认托管归属——按保留
	// 处理（Skipped），绝不在归属不明时删文件；破坏性动作零执行。
	if err := os.Mkdir(env.fstabPath, 0o755); err != nil {
		t.Fatal(err)
	}

	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 0 || len(rep.Skipped) != 1 {
		t.Fatalf("unreadable fstab must skip without failing: %+v", rep)
	}
	if !strings.Contains(rep.Skipped[0], "fstab") {
		t.Fatalf("skipped reason must name fstab: %+v", rep)
	}
	if len(sim.calls) != 0 {
		t.Fatalf("no destructive command may run when ownership is unconfirmed, got %v", sim.calls)
	}
	if _, err := os.Stat(sim.filePath); err != nil {
		t.Fatalf("swapfile must be untouched: %v", err)
	}
	raw, _ := os.ReadFile(sim.swapsPath)
	if !strings.Contains(string(raw), sim.filePath) {
		t.Fatalf("entry must stay active:\n%s", raw)
	}
}

// removeManagedFstabLine 的错误必须原样上抛（调用方据此判失败，
// 不得把读/写错误吞成“无行可删”）。
func TestRemoveManagedFstabLineErrors(t *testing.T) {
	env, _ := newSwapSimEnv(t, swapsHeader)
	if err := os.Mkdir(env.fstabPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := env.removeManagedFstabLine(); err == nil {
		t.Fatalf("unreadable fstab must error")
	}
	if ok, err := env.managedFstabLinePresent(); err == nil || ok {
		t.Fatalf("unreadable fstab must error, got present=%v err=%v", ok, err)
	}
}

func TestCleanupManagedSwapRemovesStaleBackup(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	writeSwapUninstallFstab(t, env, canonicalSwapLine(sim))
	// SwapSet 崩溃残留的旧备份（从不启用，本产品自有后缀）。
	if err := makeSparse(env.backupPath, 512*1024*1024); err != nil {
		t.Fatal(err)
	}

	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 0 || len(rep.Skipped) != 0 {
		t.Fatalf("report = %+v, want clean removal", rep)
	}
	if _, err := os.Stat(env.backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale backup must be gone")
	}
	if _, err := os.Stat(sim.filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("swapfile must be gone")
	}
}

func TestCleanupManagedSwapContainerSkips(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	sim.container = true
	setupActiveSwapFile(t, sim, 1024, 10240)
	writeSwapUninstallFstab(t, env, canonicalSwapLine(sim))

	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 0 || len(rep.Removed) != 0 || len(rep.Skipped) != 1 {
		t.Fatalf("container must skip without touching host swap: %+v", rep)
	}
	if !strings.Contains(rep.Skipped[0], "容器") {
		t.Fatalf("skipped reason must name the container form: %+v", rep)
	}
	if len(sim.calls) != 0 {
		t.Fatalf("no command may run in container, got %v", sim.calls)
	}
	if _, err := os.Stat(sim.filePath); err != nil {
		t.Fatalf("swapfile must be untouched: %v", err)
	}
}

func TestCleanupManagedSwapReadStateFailureSkips(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	writeSwapUninstallFstab(t, env, canonicalSwapLine(sim))
	// /proc/swaps 读不到：无法确认安全，一律保留、不删。
	if err := os.Remove(sim.swapsPath); err != nil {
		t.Fatal(err)
	}

	rep := env.cleanupManagedSwap(context.Background())
	if len(rep.Failed) != 0 || len(rep.Skipped) != 1 {
		t.Fatalf("unreadable swaps must skip without failing: %+v", rep)
	}
	if len(sim.calls) != 0 {
		t.Fatalf("no command may run, got %v", sim.calls)
	}
	if _, err := os.Stat(sim.filePath); err != nil {
		t.Fatalf("swapfile must be retained: %v", err)
	}
}

// --- ack 预检：回执 warnings 须在执行前点名处置范围 -------------

func TestAssessManagedSwapWarnings(t *testing.T) {
	// 托管＋安全：预告删除。
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 2048, 10240)
	writeSwapUninstallFstab(t, env, canonicalSwapLine(sim))
	warnings := env.assessManagedSwap().warnings()
	if len(warnings) != 1 {
		t.Fatalf("managed safe must warn once, got %v", warnings)
	}
	for _, want := range []string{"删除", "/swapfile", "fstab", "2048"} {
		if !strings.Contains(warnings[0], want) {
			t.Fatalf("delete notice must carry %q: %q", want, warnings[0])
		}
	}

	// 托管＋不安全：预告保留并带两个数值。
	env2, sim2 := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim2, 1024, 614400)
	if err := os.WriteFile(env2.meminfoPath, []byte("MemTotal:        8192000 kB\nMemAvailable:    512000 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSwapUninstallFstab(t, env2, canonicalSwapLine(sim2))
	warnings = env2.assessManagedSwap().warnings()
	if len(warnings) != 1 {
		t.Fatalf("managed unsafe must warn once, got %v", warnings)
	}
	for _, want := range []string{"保留", "600", "500"} {
		if !strings.Contains(warnings[0], want) {
			t.Fatalf("retain notice must carry %q: %q", want, warnings[0])
		}
	}

	// 在用但无规范行：预告保留。
	env3, sim3 := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim3, 1024, 10240)
	writeSwapUninstallFstab(t, env3, sim3.filePath+" none swap defaults 0 0")
	warnings = env3.assessManagedSwap().warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "保留") {
		t.Fatalf("non-canonical active file must warn retention, got %v", warnings)
	}

	// 无 swap：静默。
	env4, _ := newSwapSimEnv(t, swapsHeader)
	if warnings := env4.assessManagedSwap().warnings(); len(warnings) != 0 {
		t.Fatalf("absent swap must warn nothing, got %v", warnings)
	}

	// 容器形态：静默（宿主机 Swap 不归容器 agent 管）。
	env5, sim5 := newSwapSimEnv(t, swapsHeader)
	sim5.container = true
	setupActiveSwapFile(t, sim5, 1024, 10240)
	writeSwapUninstallFstab(t, env5, canonicalSwapLine(sim5))
	if warnings := env5.assessManagedSwap().warnings(); len(warnings) != 0 {
		t.Fatalf("container must warn nothing, got %v", warnings)
	}
}

// --- 接线：卸载步骤里的 Skipped 判成功、Failed 判失败 ---------

func TestRunAgentUninstallSwapStepSkippedKeepsSuccess(t *testing.T) {
	s := stubUninstallHooks(t, "/etc/flux_agent/flux_agent", false)
	orig := uninstallManagedSwapCleanup
	uninstallManagedSwapCleanup = func() swapCleanupReport {
		s.order = append(s.order, "swap")
		return swapCleanupReport{Skipped: []string{"/swapfile 在用但 fstab 无托管行，保留"}}
	}
	t.Cleanup(func() { uninstallManagedSwapCleanup = orig })

	outcome := runAgentUninstallSteps()
	if outcome.failed() {
		t.Fatalf("swap Skipped must not fail the uninstall: %+v", outcome.steps)
	}
	found := false
	for _, step := range outcome.steps {
		if step.Step == "cleanup-managed-swap" {
			found = true
			if !step.OK {
				t.Fatalf("swap step must be OK on Skipped: %+v", step)
			}
		}
	}
	if !found {
		t.Fatalf("swap step missing from outcome: %+v", outcome.steps)
	}
	if strings.Join(s.order, ",") != "restore,hostcleanup,swap,kernels,launch" {
		t.Fatalf("unexpected step order: %v", s.order)
	}
}

func TestRunAgentUninstallSwapStepFailedFailsUninstall(t *testing.T) {
	stubUninstallHooks(t, "/etc/flux_agent/flux_agent", false)
	orig := uninstallManagedSwapCleanup
	uninstallManagedSwapCleanup = func() swapCleanupReport {
		return swapCleanupReport{Failed: []string{"/swapfile（关闭 Swap 失败）"}}
	}
	t.Cleanup(func() { uninstallManagedSwapCleanup = orig })

	outcome := runAgentUninstallSteps()
	if !outcome.failed() || outcome.failedStep != "cleanup-managed-swap" {
		t.Fatalf("swap Failed must fail the uninstall at cleanup-managed-swap: %+v", outcome)
	}
}
