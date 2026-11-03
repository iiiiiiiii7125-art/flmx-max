package socket

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R2F2-D01 钉格：面板连带自卸载的宿主调优文件清理、归属守卫、
// 回执预检。全部指向临时树，绝不触碰真实 /etc。

type hostCleanupTree struct {
	conf       string
	legacyConf string
	bbrModule  string
	unit       string
	engineDir  string
	backupPath string
}

func stubHostCleanupTree(t *testing.T) *hostCleanupTree {
	t.Helper()
	root := t.TempDir()
	tree := &hostCleanupTree{
		conf:       filepath.Join(root, "sysctl.d", "99-flmx-tuning.conf"),
		legacyConf: filepath.Join(root, "sysctl.d", "99-flvx-tuning.conf"),
		bbrModule:  filepath.Join(root, "modules-load.d", "flmx-bbr.conf"),
		unit:       filepath.Join(root, "systemd", "flmx-tuning-qdisc.service"),
		engineDir:  filepath.Join(root, "agent", "tcpfit"),
		backupPath: filepath.Join(root, "agent", "tuning-backup.json"),
	}
	oldConf, oldLegacy, oldBBR := tuneConfPath, legacyTuneConfPath, tuneBBRModule
	oldUnit, oldBackup := tuneShapeUnitPath, tuneBackupPath
	oldContainer := uninstallInContainer
	tuneConfPath = tree.conf
	legacyTuneConfPath = tree.legacyConf
	tuneBBRModule = tree.bbrModule
	tuneShapeUnitPath = func() string { return tree.unit }
	tuneBackupPath = tree.backupPath
	// 容器形态桩：清理里的 systemctl 步被短路，单测不依赖宿主
	// 是否装有 systemctl（disable/daemon-reload 的脚本形态口径
	// 由 test-install-scripts-proxy.sh 的 R6-02 用例覆盖）。
	uninstallInContainer = func() bool { return true }
	t.Cleanup(func() {
		tuneConfPath, legacyTuneConfPath, tuneBBRModule = oldConf, oldLegacy, oldBBR
		tuneShapeUnitPath, tuneBackupPath = oldUnit, oldBackup
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

// 核心场景：调优恢复失败（队列备份丢失）后自卸载继续执行，宿主
// 调优文件集必须被删净——否则 sysctl conf 开机重放、qdisc unit
// 在安装目录删除后悬空。
func TestCleanupHostTuningFilesRemovesWholeSet(t *testing.T) {
	tree := stubHostCleanupTree(t)
	stubLegacyFootprint(t, false)
	for _, path := range []string{tree.conf, tree.legacyConf, tree.bbrModule, tree.unit} {
		writeHostFile(t, path)
	}

	rep := cleanupHostTuningFiles()
	if len(rep.Failed) != 0 || len(rep.Skipped) != 0 {
		t.Fatalf("report = %+v, want clean removal", rep)
	}
	if len(rep.Removed) != 4 {
		t.Fatalf("removed = %v, want the whole 4-file set", rep.Removed)
	}
	for _, path := range []string{tree.conf, tree.legacyConf, tree.bbrModule, tree.unit} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("host tuning file %s must be removed", path)
		}
	}
}

// 归属守卫：检测到旧式 gost 足迹时，legacy conf 可能由同机另一
// 安装持有——跳过并点名，其余三件照删（与 install.sh 3b 步同口
// 径）。
func TestCleanupHostTuningFilesSkipsLegacyWithFootprint(t *testing.T) {
	tree := stubHostCleanupTree(t)
	stubLegacyFootprint(t, true)
	for _, path := range []string{tree.conf, tree.legacyConf, tree.bbrModule, tree.unit} {
		writeHostFile(t, path)
	}

	rep := cleanupHostTuningFiles()
	if _, err := os.Stat(tree.legacyConf); err != nil {
		t.Fatal("legacy conf must survive when a legacy gost footprint is present")
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0], tree.legacyConf) {
		t.Fatalf("skipped = %v, want the legacy conf named", rep.Skipped)
	}
	if len(rep.Removed) != 3 || len(rep.Failed) != 0 {
		t.Fatalf("report = %+v, want the other three removed", rep)
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

// 预检：队列备份丢失＋整形仍在 → 恢复必然 fail-closed 早退，回
// 执 warnings 必须点名失败与待删宿主文件；备份在位时不预告。
func TestAssessUninstallTuningRestoreBlocked(t *testing.T) {
	tree := stubHostCleanupTree(t)
	writeHostFile(t, filepath.Join(tree.engineDir, "shape.sh"))
	writeHostFile(t, tree.conf)

	a := assessUninstallTuning()
	if !a.RestoreBlocked {
		t.Fatal("missing queue backup with a persisted shape must be assessed as blocked")
	}
	warnings := a.warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "调优恢复将失败") ||
		!strings.Contains(warnings[0], tree.conf) {
		t.Fatalf("warnings = %v, want the failure and the host file named", warnings)
	}

	// 队列备份补回后同盘面不再阻断（恢复可正常走完）。
	writeHostFile(t, filepath.Join(tree.engineDir, "original-qdisc.sh"))
	a = assessUninstallTuning()
	if a.RestoreBlocked || len(a.warnings()) != 0 {
		t.Fatalf("assessment = %+v, want unblocked with no warnings", a)
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
		return errors.New("找不到整形前的队列备份")
	}
	t.Cleanup(func() { uninstallRestoreTuning = origRestore })

	runAgentUninstall()
	if strings.Join(s.order, ",") != "restore,hostcleanup,kernels,launch" {
		t.Fatalf("unexpected step order after restore failure: %v", s.order)
	}
}
