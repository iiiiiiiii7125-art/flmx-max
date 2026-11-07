package socket

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 自卸载的宿主调优文件清理、归属守卫、回执预检（第二代调优模型
// 足迹集）。全部指向临时树，绝不触碰真实 /etc。

type hostCleanupTree struct {
	kernelConf string
	limitsConf string
	bbrConf    string
	gai        string
	gaiBackup  string
	rpsUnit    string
	rpsScript  string
	oldConf    string
	bbrModule  string
	qdiscUnit  string
	legacyConf string
}

func (tree *hostCleanupTree) files() []string {
	return []string{
		tree.kernelConf, tree.limitsConf, tree.bbrConf, tree.gaiBackup,
		tree.rpsUnit, tree.rpsScript, tree.oldConf, tree.bbrModule,
		tree.qdiscUnit, tree.legacyConf,
	}
}

func stubHostCleanupTree(t *testing.T) *hostCleanupTree {
	t.Helper()
	root := t.TempDir()
	tree := &hostCleanupTree{
		kernelConf: filepath.Join(root, "sysctl.d", "99-network-performance.conf"),
		limitsConf: filepath.Join(root, "limits.d", "99-network-performance.conf"),
		bbrConf:    filepath.Join(root, "sysctl.d", "10-bbr.conf"),
		gai:        filepath.Join(root, "gai.conf"),
		gaiBackup:  filepath.Join(root, "gai.conf.bak"),
		rpsUnit:    filepath.Join(root, "systemd", "flmx-tuning-rps.service"),
		rpsScript:  filepath.Join(root, "bin", "flmx-tuning-rps.sh"),
		oldConf:    filepath.Join(root, "sysctl.d", "99-flmx-tuning.conf"),
		bbrModule:  filepath.Join(root, "modules-load.d", "flmx-bbr.conf"),
		qdiscUnit:  filepath.Join(root, "systemd", "flmx-tuning-qdisc.service"),
		legacyConf: filepath.Join(root, "sysctl.d", "99-flvx-tuning.conf"),
	}
	old := struct {
		kernel, limits, bbr, gai, gaiBak, rpsUnit, rpsScript string
		oldConf, legacy, bbrMod, shapeUnit                   string
	}{
		tuneKernelConfPath, tuneLimitsConfPath, tuneBBRConfPath, tuneGaiConfPath, tuneGaiBackupPath,
		tuneRPSUnitPath, tuneRPSScriptPath, oldTuneConfPath, legacyTuneConfPath, oldTuneBBRModulePath, oldTuneShapeUnitPath,
	}
	tuneKernelConfPath, tuneLimitsConfPath, tuneBBRConfPath = tree.kernelConf, tree.limitsConf, tree.bbrConf
	tuneGaiConfPath, tuneGaiBackupPath = tree.gai, tree.gaiBackup
	tuneRPSUnitPath, tuneRPSScriptPath = tree.rpsUnit, tree.rpsScript
	oldTuneConfPath, legacyTuneConfPath = tree.oldConf, tree.legacyConf
	oldTuneBBRModulePath, oldTuneShapeUnitPath = tree.bbrModule, tree.qdiscUnit
	oldContainer := uninstallInContainer
	// 容器形态桩：清理里的 systemctl 步被短路，单测不依赖宿主
	// 是否装有 systemctl。
	uninstallInContainer = func() bool { return true }
	t.Cleanup(func() {
		tuneKernelConfPath, tuneLimitsConfPath, tuneBBRConfPath = old.kernel, old.limits, old.bbr
		tuneGaiConfPath, tuneGaiBackupPath = old.gai, old.gaiBak
		tuneRPSUnitPath, tuneRPSScriptPath = old.rpsUnit, old.rpsScript
		oldTuneConfPath, legacyTuneConfPath = old.oldConf, old.legacy
		oldTuneBBRModulePath, oldTuneShapeUnitPath = old.bbrMod, old.shapeUnit
		uninstallInContainer = oldContainer
	})
	return tree
}

func writeHostFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stubLegacyFootprint(t *testing.T, present bool) {
	t.Helper()
	orig := uninstallLegacyFootprint
	uninstallLegacyFootprint = func() bool { return present }
	t.Cleanup(func() { uninstallLegacyFootprint = orig })
}

// 核心场景：自卸载时宿主调优文件集必须删净——否则 sysctl conf
// 开机重放、RPS unit 在脚本删除后悬空。
func TestCleanupHostTuningFilesRemovesWholeSet(t *testing.T) {
	tree := stubHostCleanupTree(t)
	stubLegacyFootprint(t, false)
	for _, path := range tree.files() {
		writeHostFile(t, path)
	}

	rep := cleanupHostTuningFiles()
	if len(rep.Failed) != 0 || len(rep.Skipped) != 0 {
		t.Fatalf("report = %+v, want clean removal", rep)
	}
	if len(rep.Removed) != len(tree.files()) {
		t.Fatalf("removed = %v, want the whole %d-file set", rep.Removed, len(tree.files()))
	}
	for _, path := range tree.files() {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("host tuning file %s must be removed", path)
		}
	}
}

// 归属守卫：检测到旧式 gost 足迹时，legacy conf 可能由同机另一
// 安装持有——跳过并点名，其余照删。
func TestCleanupHostTuningFilesSkipsLegacyWithFootprint(t *testing.T) {
	tree := stubHostCleanupTree(t)
	stubLegacyFootprint(t, true)
	for _, path := range tree.files() {
		writeHostFile(t, path)
	}

	rep := cleanupHostTuningFiles()
	if _, err := os.Stat(tree.legacyConf); err != nil {
		t.Fatal("legacy conf must survive when a legacy gost footprint is present")
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0], tree.legacyConf) {
		t.Fatalf("skipped = %v, want the legacy conf named", rep.Skipped)
	}
	if len(rep.Removed) != len(tree.files())-1 || len(rep.Failed) != 0 {
		t.Fatalf("report = %+v, want the rest removed", rep)
	}
}

// 幂等：已清状态重复执行，无删除、无失败、无跳过。
func TestCleanupHostTuningFilesIdempotentOnCleanState(t *testing.T) {
	stubHostCleanupTree(t)
	stubLegacyFootprint(t, false)

	rep := cleanupHostTuningFiles()
	if len(rep.Removed) != 0 || len(rep.Skipped) != 0 || len(rep.Failed) != 0 {
		t.Fatalf("report = %+v, want empty on a clean host", rep)
	}
}

// gai 兜底还原：恢复步未消费的备份＋调优行仍在位时，清理先按备
// 份还原 gai.conf 再删备份（本体不删）。
func TestCleanupHostTuningFilesRestoresGaiFromBackup(t *testing.T) {
	tree := stubHostCleanupTree(t)
	stubLegacyFootprint(t, false)
	orig := "label ::/0          1\n"
	writeHostFile(t, tree.gaiBackup)
	if err := os.WriteFile(tree.gaiBackup, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	writeHostFile(t, tree.gai)
	if err := os.WriteFile(tree.gai, []byte(orig+gaiPrecedenceLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanupHostTuningFiles()
	content, err := os.ReadFile(tree.gai)
	if err != nil || string(content) != orig {
		t.Fatalf("gai.conf = %q, %v; want restored original", content, err)
	}
	if _, err := os.Stat(tree.gaiBackup); !os.IsNotExist(err) {
		t.Fatal("gai backup must be removed after restore")
	}
}

// 足迹判定与 install.sh legacy_gost_footprint_present 同口径：
// 旧配置目录里的二进制/连接配置、或命中指纹的 gost.service。
func TestLegacyGostFootprintPresent(t *testing.T) {
	root := t.TempDir()
	gostDir := filepath.Join(root, "gost")
	unitDir := filepath.Join(root, "systemd")
	unitPath := filepath.Join(unitDir, "gost.service")
	oldFiles, oldDir := legacyGostServiceFilePaths, legacyGostDirPath
	legacyGostServiceFilePaths = []string{unitPath}
	legacyGostDirPath = gostDir
	t.Cleanup(func() {
		legacyGostServiceFilePaths, legacyGostDirPath = oldFiles, oldDir
	})

	if legacyGostFootprintPresent() {
		t.Fatal("empty tree must report no footprint")
	}
	// 仅有 WorkingDirectory、无匹配 ExecStart：不算足迹。
	writeHostFile(t, unitPath)
	if err := os.WriteFile(unitPath, []byte("[Service]\nWorkingDirectory="+gostDir+"\nExecStart=/usr/bin/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if legacyGostFootprintPresent() {
		t.Fatal("unit without the legacy ExecStart shapes must not count as a footprint")
	}
	// ExecStart 指向旧目录二进制：足迹成立。
	if err := os.WriteFile(unitPath, []byte("[Service]\nWorkingDirectory="+gostDir+"\nExecStart="+filepath.Join(gostDir, "gost")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !legacyGostFootprintPresent() {
		t.Fatal("unit with legacy ExecStart must count as a footprint")
	}
	if err := os.Remove(unitPath); err != nil {
		t.Fatal(err)
	}
	// 旧配置目录里有连接配置：足迹成立（与服务文件无关）。
	writeHostFile(t, filepath.Join(gostDir, "config.json"))
	if !legacyGostFootprintPresent() {
		t.Fatal("legacy config.json must count as a footprint")
	}
}

// 预检：有调优足迹在位时回执 warnings 预告回退与待删文件；干净
// 盘面不预告。
func TestAssessUninstallTuningAnnouncesFootprint(t *testing.T) {
	tree := stubHostCleanupTree(t)
	if a := assessUninstallTuning(); len(a.warnings()) != 0 {
		t.Fatalf("clean tree must produce no warnings: %+v", a)
	}

	writeHostFile(t, tree.kernelConf)
	writeHostFile(t, tree.rpsUnit)
	a := assessUninstallTuning()
	warnings := a.warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "回退系统调优") ||
		!strings.Contains(warnings[0], tree.kernelConf) || !strings.Contains(warnings[0], tree.rpsUnit) {
		t.Fatalf("warnings = %v, want the rollback notice naming the files", warnings)
	}

	// 只有 gai 调优行在位（无文件足迹）时同样预告。
	tree2 := stubHostCleanupTree(t)
	writeHostFile(t, tree2.gai)
	if err := os.WriteFile(tree2.gai, []byte(gaiPrecedenceLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a = assessUninstallTuning()
	if !a.IPv4Tuned || len(a.warnings()) != 1 {
		t.Fatalf("assessment = %+v, want IPv4Tuned with one warning", a)
	}
}

// 全流程：恢复接缝报错时，runAgentUninstall 仍走完宿主清理与
// 内核清理并启动卸载脚本（卸载不因恢复失败中断），顺序为
// restore → hostcleanup → kernels → launch。
func TestRunAgentUninstallContinuesPastRestoreFailure(t *testing.T) {
	s := stubUninstallHooks(t, "/etc/flux_agent/flux_agent", false)
	origRestore := uninstallRestoreTuning
	uninstallRestoreTuning = func() error {
		s.order = append(s.order, "restore")
		return errors.New("部分调优回退失败")
	}
	t.Cleanup(func() { uninstallRestoreTuning = origRestore })

	runAgentUninstall()
	if strings.Join(s.order, ",") != "restore,hostcleanup,kernels,launch" {
		t.Fatalf("unexpected step order after restore failure: %v", s.order)
	}
}
