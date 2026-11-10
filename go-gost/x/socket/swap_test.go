package socket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// --- 纯函数 --------------------------------------------------------------

func TestParseSwaps(t *testing.T) {
	content := "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n" +
		"/dev/vda2\t\t\t\tpartition\t1048576\t\t10240\t\t-2\n" +
		"/swapfile\t\t\t\tfile\t\t2097152\t\t0\t\t-3\n"
	entries := parseSwaps([]byte(content))
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %+v", entries)
	}
	if entries[0].Name != "/dev/vda2" || entries[0].Type != "partition" || entries[0].SizeMB != 1024 || entries[0].UsedMB != 10 {
		t.Fatalf("partition entry drifted: %+v", entries[0])
	}
	if entries[1].Name != "/swapfile" || entries[1].Type != "file" || entries[1].SizeMB != 2048 || entries[1].UsedMB != 0 {
		t.Fatalf("file entry drifted: %+v", entries[1])
	}
}

func TestParseSwapsMalformed(t *testing.T) {
	if got := parseSwaps(nil); len(got) != 0 {
		t.Fatalf("empty content must yield no entries, got %+v", got)
	}
	if got := parseSwaps([]byte("Filename\tType\tSize\tUsed\tPriority\n")); len(got) != 0 {
		t.Fatalf("header only must yield no entries, got %+v", got)
	}
	content := "Filename Type Size Used Priority\n" +
		"garbage\n" + // 字段不足
		"/x file notanumber 0 -2\n" + // 数值非法
		"/y file 1024 notanumber -2\n" + // 数值非法
		"\n" +
		"/z partition 2048 1024 -2" // 无尾换行也要收
	entries := parseSwaps([]byte(content))
	if len(entries) != 1 || entries[0].Name != "/z" || entries[0].SizeMB != 2 || entries[0].UsedMB != 1 {
		t.Fatalf("malformed lines must be skipped, valid kept: %+v", entries)
	}
}

func TestParseMeminfoKB(t *testing.T) {
	content := []byte("MemTotal:        8192000 kB\nMemFree:         1024000 kB\nMemAvailable:    4096000 kB\n")
	if v, ok := parseMeminfoKB(content, "MemTotal"); !ok || v != 8192000 {
		t.Fatalf("MemTotal = %d, %v", v, ok)
	}
	if v, ok := parseMeminfoKB(content, "MemAvailable"); !ok || v != 4096000 {
		t.Fatalf("MemAvailable = %d, %v", v, ok)
	}
	if _, ok := parseMeminfoKB(content, "SwapTotal"); ok {
		t.Fatalf("missing key must report not-ok")
	}
	if _, ok := parseMeminfoKB([]byte("MemTotal: bogus kB\n"), "MemTotal"); ok {
		t.Fatalf("malformed value must report not-ok")
	}
}

func TestConvergeFstab(t *testing.T) {
	const fp = "/swapfile"
	const canonical = "/swapfile none swap sw 0 0\n"
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{"empty", "", canonical, true},
		{"append preserves others", "UUID=root / ext4 defaults 0 1\n", "UUID=root / ext4 defaults 0 1\n" + canonical, true},
		{"already canonical", canonical, canonical, false},
		{"stale variant replaced in place", "UUID=root / ext4 defaults 0 1\n/swapfile none swap defaults 0 0\nUUID=boot /boot vfat defaults 0 2\n",
			"UUID=root / ext4 defaults 0 1\n" + canonical + "UUID=boot /boot vfat defaults 0 2\n", true},
		{"duplicates collapsed", "/swapfile none swap sw 0 0\n/swapfile none swap sw 0 0\n", canonical, true},
		{"other swap entry untouched", "UUID=partswap none swap sw 0 0\n", "UUID=partswap none swap sw 0 0\n" + canonical, true},
		{"commented managed line kept, canonical appended", "#/swapfile none swap sw 0 0\n", "#/swapfile none swap sw 0 0\n" + canonical, true},
		{"missing trailing newline normalized", "UUID=root / ext4 defaults 0 1", "UUID=root / ext4 defaults 0 1\n" + canonical, true},
	}
	for _, c := range cases {
		got, changed := convergeFstab(c.in, fp)
		if got != c.want || changed != c.changed {
			t.Fatalf("%s: got (%q, %v), want (%q, %v)", c.name, got, changed, c.want, c.changed)
		}
	}
}

func TestValidateSwapSizeGB(t *testing.T) {
	for _, n := range []int{1, 2, 32} {
		if err := validateSwapSizeGB(n); err != nil {
			t.Fatalf("%d GB must be valid: %v", n, err)
		}
	}
	for _, n := range []int{0, -1, 33, 100} {
		if err := validateSwapSizeGB(n); err == nil {
			t.Fatalf("%d GB must be rejected", n)
		}
	}
}

func TestCheckSwapCapacity(t *testing.T) {
	// 恰好达标（可用 == 目标＋预留）放行。
	if err := checkSwapCapacity(2048+256, 0, 2); err != nil {
		t.Fatalf("exact fit must pass: %v", err)
	}
	// 现有 /swapfile 占量计入可用。
	if err := checkSwapCapacity(1000, 1024+256+24, 2); err != nil {
		t.Fatalf("existing file space must count: %v", err)
	}
	// 差 1MB 拒绝，且错误带全部关键数字。
	err := checkSwapCapacity(2048+255, 0, 2)
	if err == nil {
		t.Fatalf("1MB short must be rejected")
	}
	for _, want := range []string{"磁盘空间不足", "2304", "2048", "256", "2303"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("capacity error must carry %q: %v", want, err)
		}
	}
}

func TestSwapoffUnsafe(t *testing.T) {
	if swapoffUnsafe(500, 500) {
		t.Fatalf("used == available must be safe")
	}
	if !swapoffUnsafe(501, 500) {
		t.Fatalf("used > available must be unsafe")
	}
	if swapoffUnsafe(0, 0) {
		t.Fatalf("zero used must be safe")
	}
}

// --- 命令层替身：模拟内核的文件/命令效果 -----------------------------------

const swapTestMeminfo = "MemTotal:        8192000 kB\nMemFree:         1024000 kB\nMemAvailable:    4096000 kB\n"

const swapsHeader = "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"

type swapSim struct {
	swapsPath string
	filePath  string
	freeMB    int64
	container bool
	failAt    map[string]error
	// failOnce 让指定命令仅第一次调用失败（后续恢复正常）：钉
	// 「swapon 失败→restore 的 swapon 须成功」一类首败后愈支。
	failOnce map[string]error
	calls    []string
}

func (s *swapSim) diskFree(string) (int64, error) { return s.freeMB, nil }
func (s *swapSim) inCont() bool                   { return s.container }

func (s *swapSim) run(_ context.Context, name string, args ...string) (string, error) {
	s.calls = append(s.calls, name)
	if err, ok := s.failOnce[name]; ok {
		delete(s.failOnce, name)
		return "", err
	}
	if err, ok := s.failAt[name]; ok {
		return "", err
	}
	switch name {
	case "fallocate": // -l <n>G <path>
		gb, err := strconv.Atoi(strings.TrimSuffix(args[1], "G"))
		if err != nil {
			return "", err
		}
		return "", makeSparse(args[2], int64(gb)*1024*1024*1024)
	case "dd": // of=<path> ... count=<mb>
		var path string
		var mb int64
		for _, a := range args {
			if strings.HasPrefix(a, "of=") {
				path = strings.TrimPrefix(a, "of=")
			}
			if strings.HasPrefix(a, "count=") {
				mb, _ = strconv.ParseInt(strings.TrimPrefix(a, "count="), 10, 64)
			}
		}
		return "", makeSparse(path, mb*1024*1024)
	case "mkswap":
		return "", nil
	case "swapon":
		fi, err := os.Stat(args[0])
		if err != nil {
			return "", err
		}
		f, err := os.OpenFile(s.swapsPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return "", err
		}
		defer func() { _ = f.Close() }()
		_, err = fmt.Fprintf(f, "%s\t\t\tfile\t\t%d\t\t0\t\t-2\n", args[0], fi.Size()/1024)
		return "", err
	case "swapoff":
		raw, err := os.ReadFile(s.swapsPath)
		if err != nil {
			return "", err
		}
		var kept []string
		for _, line := range strings.Split(string(raw), "\n") {
			if f := strings.Fields(line); len(f) > 0 && f[0] == args[0] {
				continue
			}
			kept = append(kept, line)
		}
		return "", os.WriteFile(s.swapsPath, []byte(strings.Join(kept, "\n")), 0o644)
	}
	return "", fmt.Errorf("unexpected command %s", name)
}

func makeSparse(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Truncate(size)
}

func newSwapSimEnv(t *testing.T, swapsContent string) (swapEnv, *swapSim) {
	t.Helper()
	dir := t.TempDir()
	sim := &swapSim{
		swapsPath: filepath.Join(dir, "swaps"),
		filePath:  filepath.Join(dir, "swapfile"),
		freeMB:    8192,
		failAt:    map[string]error{},
		failOnce:  map[string]error{},
	}
	if err := os.WriteFile(sim.swapsPath, []byte(swapsContent), 0o644); err != nil {
		t.Fatal(err)
	}
	meminfoPath := filepath.Join(dir, "meminfo")
	if err := os.WriteFile(meminfoPath, []byte(swapTestMeminfo), 0o644); err != nil {
		t.Fatal(err)
	}
	env := swapEnv{
		procSwapsPath: sim.swapsPath,
		meminfoPath:   meminfoPath,
		fstabPath:     filepath.Join(dir, "fstab"),
		filePath:      sim.filePath,
		backupPath:    sim.filePath + swapBackupSuffix,
		run:           sim.run,
		diskFreeMB:    sim.diskFree,
		inContainer:   sim.inCont,
	}
	return env, sim
}

func TestSwapInfoReply(t *testing.T) {
	content := swapsHeader +
		"/dev/vda2\t\t\t\tpartition\t1048576\t\t10240\t\t-2\n"
	env, sim := newSwapSimEnv(t, content)
	// 托管文件作为第二条在用条目＋磁盘文件在场。
	if err := makeSparse(sim.filePath, 1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	extra := fmt.Sprintf("%s\t\t\tfile\t\t1048576\t\t20480\t\t-3\n", sim.filePath)
	if err := os.WriteFile(sim.swapsPath, []byte(content+extra), 0o644); err != nil {
		t.Fatal(err)
	}
	sim.freeMB = 5000
	out, err := handleSwapCommandEnv(context.Background(), env, "SwapInfo", nil)
	if err != nil {
		t.Fatalf("SwapInfo: %v", err)
	}
	info, ok := out.(swapInfoReply)
	if !ok {
		t.Fatalf("reply type drifted: %T", out)
	}
	if info.TotalMB != 2048 || info.UsedMB != 30 {
		t.Fatalf("totals = %+v, want total 2048 used 30", info)
	}
	if len(info.Entries) != 2 || info.Entries[0].Type != "partition" {
		t.Fatalf("entries drifted: %+v", info.Entries)
	}
	if info.DiskFreeMB != 5000 || info.MemTotalMB != 8000 {
		t.Fatalf("disk/mem = %d/%d, want 5000/8000", info.DiskFreeMB, info.MemTotalMB)
	}
	if !info.ManagedFilePresent {
		t.Fatalf("managed file on disk must be reported present")
	}
	if len(sim.calls) != 0 {
		t.Fatalf("SwapInfo must not execute commands, got %v", sim.calls)
	}
}

func TestSwapSetCreateFromScratch(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	if err := os.WriteFile(env.fstabPath, []byte("UUID=root / ext4 defaults 0 1\nUUID=partswap none swap sw 0 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)})
	if err != nil {
		t.Fatalf("SwapSet: %v", err)
	}
	res, ok := out.(swapSetReply)
	if !ok {
		t.Fatalf("reply type drifted: %T", out)
	}
	if res.Before.TotalMB != 0 || res.After.TotalMB != 2048 || res.SizeGB != 2 {
		t.Fatalf("before/after drifted: %+v", res)
	}
	if strings.Join(sim.calls, ",") != "fallocate,mkswap,swapon" {
		t.Fatalf("call order drifted: %v", sim.calls)
	}
	if fi, err := os.Stat(sim.filePath); err != nil || fi.Size() != 2*1024*1024*1024 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("swapfile state wrong: %v %v", fi, err)
	}
	fstab, _ := os.ReadFile(env.fstabPath)
	want := "UUID=root / ext4 defaults 0 1\nUUID=partswap none swap sw 0 0\n" + sim.filePath + " none swap sw 0 0\n"
	if string(fstab) != want {
		t.Fatalf("fstab drifted:\n%s\nwant:\n%s", fstab, want)
	}
}

func TestSwapSetResizeActive(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	// 托管条目的路径须是 sim 自己的 filePath，覆写 swaps 内容。
	entry := fmt.Sprintf("%s\t\t\tfile\t\t1048576\t\t10240\t\t-2\n", sim.filePath)
	if err := os.WriteFile(sim.swapsPath, []byte(swapsHeader+entry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makeSparse(sim.filePath, 1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	fstabBefore := "UUID=root / ext4 defaults 0 1\n" + sim.filePath + " none swap defaults 0 0\n"
	if err := os.WriteFile(env.fstabPath, []byte(fstabBefore), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)})
	if err != nil {
		t.Fatalf("SwapSet resize: %v", err)
	}
	res := out.(swapSetReply)
	if res.Before.TotalMB != 1024 || res.After.TotalMB != 2048 {
		t.Fatalf("before/after drifted: %+v", res)
	}
	if strings.Join(sim.calls, ",") != "swapoff,fallocate,mkswap,swapon" {
		t.Fatalf("resize must swapoff first: %v", sim.calls)
	}
	if _, err := os.Stat(env.backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup must be removed after success, stat err=%v", err)
	}
	fstab, _ := os.ReadFile(env.fstabPath)
	want := "UUID=root / ext4 defaults 0 1\n" + sim.filePath + " none swap sw 0 0\n"
	if string(fstab) != want {
		t.Fatalf("fstab must hold exactly one canonical line:\n%s", fstab)
	}
}

func TestSwapSetDiskInsufficient(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	sim.freeMB = 1000 // 目标 4G 需要 4096+256
	_, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(4)})
	if err == nil || !strings.Contains(err.Error(), "磁盘空间不足") {
		t.Fatalf("must refuse with capacity error, got %v", err)
	}
	if len(sim.calls) != 0 {
		t.Fatalf("no command may run on capacity refusal, got %v", sim.calls)
	}
	if _, serr := os.Stat(sim.filePath); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("no swapfile may be created on refusal")
	}
}

func TestSwapSetSwapoffRiskRefused(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	// 已用 600MB（614400 KiB）；MemAvailable 改小到 500MB 构造
	// swapoff 压死风险（used > available），重写 meminfo。
	entry := fmt.Sprintf("%s\t\t\tfile\t\t1048576\t\t614400\t\t-2\n", sim.filePath)
	if err := os.WriteFile(sim.swapsPath, []byte(swapsHeader+entry), 0o644); err != nil {
		t.Fatal(err)
	}
	meminfo := "MemTotal:        8192000 kB\nMemAvailable:    512000 kB\n"
	if err := os.WriteFile(env.meminfoPath, []byte(meminfo), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makeSparse(sim.filePath, 1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	_, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)})
	if err == nil || !strings.Contains(err.Error(), "拒绝执行") || !strings.Contains(err.Error(), "600") {
		t.Fatalf("must refuse with risk error carrying numbers, got %v", err)
	}
	if len(sim.calls) != 0 {
		t.Fatalf("no command may run on risk refusal, got %v", sim.calls)
	}
}

func TestSwapSetMkswapFailureRestores(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	entry := fmt.Sprintf("%s\t\t\tfile\t\t1048576\t\t10240\t\t-2\n", sim.filePath)
	if err := os.WriteFile(sim.swapsPath, []byte(swapsHeader+entry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makeSparse(sim.filePath, 1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	sim.failAt["mkswap"] = errors.New("mkswap: 模拟失败")
	_, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)})
	if err == nil {
		t.Fatalf("mkswap failure must error")
	}
	for _, want := range []string{"格式化 Swap", "已恢复执行前状态"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must carry %q: %v", want, err)
		}
	}
	// 恢复后：旧文件回原位且重新在用、备份消失。
	if fi, serr := os.Stat(sim.filePath); serr != nil || fi.Size() != 1024*1024*1024 {
		t.Fatalf("old swapfile must be restored at 1G: %v %v", fi, serr)
	}
	if _, serr := os.Stat(env.backupPath); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("backup must be consumed by restore, stat err=%v", serr)
	}
	raw, _ := os.ReadFile(sim.swapsPath)
	if !strings.Contains(string(raw), sim.filePath) {
		t.Fatalf("old entry must be active again in swaps:\n%s", raw)
	}
	// fstab 一字未动（本例 fstab 文件本就不存在，失败后仍不存在）。
	if _, serr := os.Stat(env.fstabPath); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("fstab must not be written on mid-flow failure")
	}
}

func TestSwapSetContainerAndBadSize(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	sim.container = true
	if _, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)}); err == nil ||
		!strings.Contains(err.Error(), "容器") {
		t.Fatalf("container must be refused explicitly, got %v", err)
	}
	sim.container = false
	for _, bad := range []float64{0, 33} {
		if _, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": bad}); err == nil {
			t.Fatalf("size %v must be rejected", bad)
		}
	}
	if _, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": 2.5}); err == nil ||
		!strings.Contains(err.Error(), "解析 Swap 参数失败") {
		t.Fatalf("fractional size must fail parsing, got %v", err)
	}
	if len(sim.calls) != 0 {
		t.Fatalf("no command may run for refused sets, got %v", sim.calls)
	}
}

func TestSwapCommandDispatchGuards(t *testing.T) {
	env, _ := newSwapSimEnv(t, swapsHeader)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := handleSwapCommandEnv(ctx, env, "SwapInfo", nil); err == nil ||
		!strings.Contains(err.Error(), "预算已耗尽") {
		t.Fatalf("spent budget must refuse, got %v", err)
	}
	if _, err := handleSwapCommandEnv(context.Background(), env, "SwapNope", nil); err == nil ||
		!strings.Contains(err.Error(), "未知命令类型") {
		t.Fatalf("unknown command must be named, got %v", err)
	}
}

// --- fileSizeMB 契约三态（R3-NODE-01） ------------------------------------

// setupActiveSwapFile 在 sim 里布一个在用托管条目＋同尺寸磁盘文件。
func setupActiveSwapFile(t *testing.T, sim *swapSim, sizeMB int64, usedKiB int64) {
	t.Helper()
	entry := fmt.Sprintf("%s\t\t\tfile\t\t%d\t\t%d\t\t-2\n", sim.filePath, sizeMB*1024, usedKiB)
	if err := os.WriteFile(sim.swapsPath, []byte(swapsHeader+entry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makeSparse(sim.filePath, sizeMB*1024*1024); err != nil {
		t.Fatal(err)
	}
}

func TestSwapInfoFileSizeMBStates(t *testing.T) {
	// 无文件：fileSizeMB=0、managedFilePresent=false。
	env, _ := newSwapSimEnv(t, swapsHeader)
	out, err := handleSwapCommandEnv(context.Background(), env, "SwapInfo", nil)
	if err != nil {
		t.Fatalf("SwapInfo: %v", err)
	}
	info := out.(swapInfoReply)
	if info.FileSizeMB != 0 || info.ManagedFilePresent {
		t.Fatalf("no file must report fileSizeMB=0/absent, got %+v", info)
	}

	// 在盘未启用：文件 1536MB 在盘、swaps 只有一条 1024MB 分区
	// 条目 → fileSizeMB 回磁盘文件真实大小（与任何条目大小都不同，
	// 防把条目大小误当文件大小），总量不含该文件。
	env2, sim2 := newSwapSimEnv(t, swapsHeader+"/dev/vda2\t\t\tpartition\t1048576\t\t0\t\t-2\n")
	if err := makeSparse(sim2.filePath, 1536*1024*1024); err != nil {
		t.Fatal(err)
	}
	out, err = handleSwapCommandEnv(context.Background(), env2, "SwapInfo", nil)
	if err != nil {
		t.Fatalf("SwapInfo: %v", err)
	}
	info = out.(swapInfoReply)
	if info.FileSizeMB != 1536 || !info.ManagedFilePresent {
		t.Fatalf("on-disk inactive file must report its real size, got %+v", info)
	}
	if info.TotalMB != 1024 {
		t.Fatalf("inactive file must not enter totals, got %+v", info)
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"fileSizeMB":1536`) {
		t.Fatalf("wire shape must carry fileSizeMB, got %s", raw)
	}

	// 已启用：文件与条目同在 → fileSizeMB=文件大小。
	env3, sim3 := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim3, 1024, 10240)
	out, err = handleSwapCommandEnv(context.Background(), env3, "SwapInfo", nil)
	if err != nil {
		t.Fatalf("SwapInfo: %v", err)
	}
	info = out.(swapInfoReply)
	if info.FileSizeMB != 1024 || !info.ManagedFilePresent || info.TotalMB != 1024 {
		t.Fatalf("active file must report its size, got %+v", info)
	}
}

// TestCheckSwapCapacityBoundaryMirror 与前端预检 CDP 边界格共用
// 一组数字（在盘未启用文件 1024MB、目标 2GB、需求 2304MB）：
// diskFree 1280 → 可用恰达需求放行；1279 → 差 1MB 拒绝。
func TestCheckSwapCapacityBoundaryMirror(t *testing.T) {
	if err := checkSwapCapacity(1280, 1024, 2); err != nil {
		t.Fatalf("exact boundary must pass: %v", err)
	}
	err := checkSwapCapacity(1279, 1024, 2)
	if err == nil {
		t.Fatalf("1MB below boundary must be rejected")
	}
	for _, want := range []string{"2304", "2303"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("boundary error must carry %q: %v", want, err)
		}
	}
}

// --- SwapSet 故障支回归钉（R3-AG-01） --------------------------------------

func TestSwapSetSwapoffFailureKeepsState(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	sim.failAt["swapoff"] = errors.New("swapoff: 模拟失败")
	_, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)})
	if err == nil {
		t.Fatalf("swapoff failure must error")
	}
	for _, want := range []string{"关闭现有 Swap", "机器状态未改变，现有 Swap 仍在启用中"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must carry %q: %v", want, err)
		}
	}
	if strings.Join(sim.calls, ",") != "swapoff" {
		t.Fatalf("only swapoff may be attempted, got %v", sim.calls)
	}
	raw, _ := os.ReadFile(sim.swapsPath)
	if !strings.Contains(string(raw), sim.filePath) {
		t.Fatalf("entry must stay active after swapoff failure:\n%s", raw)
	}
	if fi, serr := os.Stat(sim.filePath); serr != nil || fi.Size() != 1024*1024*1024 {
		t.Fatalf("swapfile must be untouched: %v %v", fi, serr)
	}
	if _, serr := os.Stat(env.backupPath); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("no backup may be created before swapoff succeeds")
	}
}

func TestSwapSetSwaponFailureRestores(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	sim.failOnce["swapon"] = errors.New("swapon: 模拟失败")
	_, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)})
	if err == nil {
		t.Fatalf("swapon failure must error")
	}
	for _, want := range []string{"启用 Swap", "已恢复执行前状态：旧 /swapfile 已还原并重新启用"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must carry %q: %v", want, err)
		}
	}
	if strings.Join(sim.calls, ",") != "swapoff,fallocate,mkswap,swapon,swapon" {
		t.Fatalf("restore must re-run swapon once, got %v", sim.calls)
	}
	if fi, serr := os.Stat(sim.filePath); serr != nil || fi.Size() != 1024*1024*1024 {
		t.Fatalf("old swapfile must be restored at 1G: %v %v", fi, serr)
	}
	raw, _ := os.ReadFile(sim.swapsPath)
	if !strings.Contains(string(raw), sim.filePath) {
		t.Fatalf("old entry must be active again after restore:\n%s", raw)
	}
	if _, serr := os.Stat(env.backupPath); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("backup must be consumed by restore, stat err=%v", serr)
	}
}

func TestSwapSetSwaponFailureRestoreAlsoFails(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	sim.failAt["swapon"] = errors.New("swapon: 模拟持续失败")
	_, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)})
	if err == nil {
		t.Fatalf("swapon failure must error")
	}
	for _, want := range []string{"启用 Swap", "重新启用失败", "当前无 Swap 生效", "请人工执行 swapon"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must carry %q: %v", want, err)
		}
	}
	// 文件已还原但未启用：状态说明必须与机器实况一致。
	if fi, serr := os.Stat(sim.filePath); serr != nil || fi.Size() != 1024*1024*1024 {
		t.Fatalf("old swapfile must be restored at 1G: %v %v", fi, serr)
	}
	raw, _ := os.ReadFile(sim.swapsPath)
	if strings.Contains(string(raw), sim.filePath) {
		t.Fatalf("entry must stay inactive when restore swapon fails:\n%s", raw)
	}
	if _, serr := os.Stat(env.backupPath); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("backup must be consumed by restore, stat err=%v", serr)
	}
}

func TestSwapSetAllocDoubleFailureRestores(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	sim.failAt["fallocate"] = errors.New("fallocate: 模拟失败")
	sim.failAt["dd"] = errors.New("dd: 模拟失败")
	_, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)})
	if err == nil {
		t.Fatalf("double alloc failure must error")
	}
	for _, want := range []string{"创建 Swap 文件", "fallocate 失败", "dd 回退亦失败", "已恢复执行前状态"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must carry %q: %v", want, err)
		}
	}
	if strings.Join(sim.calls, ",") != "swapoff,fallocate,dd,swapon" {
		t.Fatalf("restore must follow the double failure, got %v", sim.calls)
	}
	if fi, serr := os.Stat(sim.filePath); serr != nil || fi.Size() != 1024*1024*1024 {
		t.Fatalf("old swapfile must be restored at 1G: %v %v", fi, serr)
	}
	raw, _ := os.ReadFile(sim.swapsPath)
	if !strings.Contains(string(raw), sim.filePath) {
		t.Fatalf("old entry must be active again after restore:\n%s", raw)
	}
}

func TestSwapSetRenameBackupFailureReactivates(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	// 备份路径被非空目录占住：预清理 Remove 与改名 Rename 都会失败。
	if err := os.MkdirAll(env.backupPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.backupPath, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)})
	if err == nil {
		t.Fatalf("rename failure must error")
	}
	for _, want := range []string{"备份现有 /swapfile", "现有 Swap 已重新启用，机器状态未改变"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must carry %q: %v", want, err)
		}
	}
	if strings.Join(sim.calls, ",") != "swapoff,swapon" {
		t.Fatalf("rename failure must only re-activate, got %v", sim.calls)
	}
	if fi, serr := os.Stat(sim.filePath); serr != nil || fi.Size() != 1024*1024*1024 {
		t.Fatalf("original swapfile must be untouched: %v %v", fi, serr)
	}
	raw, _ := os.ReadFile(sim.swapsPath)
	if !strings.Contains(string(raw), sim.filePath) {
		t.Fatalf("entry must be active again after rename failure:\n%s", raw)
	}
	if _, serr := os.Stat(filepath.Join(env.backupPath, "keep")); serr != nil {
		t.Fatalf("foreign backup directory must be left alone: %v", serr)
	}
}

func TestSwapSetFstabFailureKeepsActiveSwap(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	// fstab 路径指向目录：读回 EISDIR，收敛写必然失败。
	if err := os.Mkdir(env.fstabPath, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)})
	if err == nil {
		t.Fatalf("fstab failure must error, never report clean success")
	}
	for _, want := range []string{
		"已按 2 GB 启用", "写入 /etc/fstab 失败", "新 Swap 正在生效",
		"重启后不会自动挂载", "请人工在 /etc/fstab 中补一行",
		sim.filePath + " none swap sw 0 0",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must carry %q: %v", want, err)
		}
	}
	// 文案与实况一致：新 swap 已按 2G 生效、备份已清、不回滚。
	raw, _ := os.ReadFile(sim.swapsPath)
	if !strings.Contains(string(raw), "2097152") {
		t.Fatalf("new 2G swap must be the active one:\n%s", raw)
	}
	if fi, serr := os.Stat(sim.filePath); serr != nil || fi.Size() != 2*1024*1024*1024 {
		t.Fatalf("new swapfile must be 2G: %v %v", fi, serr)
	}
	if _, serr := os.Stat(env.backupPath); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("backup must already be removed at fstab step, stat err=%v", serr)
	}
}

func TestSwapSetBackupDeleteFailureWarns(t *testing.T) {
	env, sim := newSwapSimEnv(t, swapsHeader)
	setupActiveSwapFile(t, sim, 1024, 10240)
	// 新 swap 启用成功后，把备份文件换成非空目录：代码的
	// os.Remove(backupPath) 必然失败 → 必须走 warnings 而非装无瑕。
	simRun := sim.run
	env.run = func(ctx context.Context, name string, args ...string) (string, error) {
		out, rerr := simRun(ctx, name, args...)
		if rerr == nil && name == "swapon" {
			if err := os.Remove(env.backupPath); err == nil {
				if err := os.MkdirAll(env.backupPath, 0o755); err == nil {
					_ = os.WriteFile(filepath.Join(env.backupPath, "keep"), []byte("x"), 0o644)
				}
			}
		}
		return out, rerr
	}
	out, err := handleSwapCommandEnv(context.Background(), env, "SwapSet", map[string]interface{}{"sizeGB": float64(2)})
	if err != nil {
		t.Fatalf("backup delete failure must stay a warning, got error %v", err)
	}
	res, ok := out.(swapSetReply)
	if !ok {
		t.Fatalf("reply type drifted: %T", out)
	}
	if res.Before.TotalMB != 1024 || res.After.TotalMB != 2048 {
		t.Fatalf("before/after drifted: %+v", res)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("exactly one warning expected, got %+v", res.Warnings)
	}
	for _, want := range []string{"旧 Swap 备份文件", "删除失败", "可人工删除"} {
		if !strings.Contains(res.Warnings[0], want) {
			t.Fatalf("warning must carry %q: %q", want, res.Warnings[0])
		}
	}
	// fstab 收敛不受警告影响、恰一条标准行。
	fstab, _ := os.ReadFile(env.fstabPath)
	if string(fstab) != sim.filePath+" none swap sw 0 0\n" {
		t.Fatalf("fstab must still converge:\n%s", fstab)
	}
}
