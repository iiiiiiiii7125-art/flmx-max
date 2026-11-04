package socket

import (
	"os"
	"path/filepath"
	"testing"
)

// R2-020: the tuning state root (backup / params / tcpfit engine
// state) must anchor to the agent data directory in the container
// form, like every other piece of agent state, instead of the
// script form's install path — inside the image that path is the
// ephemeral container layer, so a rebuild silently dropped the
// tuning backup and the committed shaper state.
//
// 改名双认（RN-2）：脚本形态的默认根是现行布局 /etc/flmx_agent；
// 工作目录命中任一世代布局名时锚到工作目录本身——存量
// /etc/flux_agent 节点经面板原地升级后状态根不换、调优备份不丢，
// 与 ResolveBaseDir 的脚本形态分支同口径。

func stubContainerDetection(t *testing.T, inContainer bool) {
	t.Helper()
	oldMarker, oldCgroup := containerMarkerPath, containerCgroupPath
	t.Cleanup(func() { containerMarkerPath, containerCgroupPath = oldMarker, oldCgroup })
	dir := t.TempDir()
	cgroup := filepath.Join(dir, "cgroup")
	if err := os.WriteFile(cgroup, []byte("0::/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	containerCgroupPath = cgroup
	if inContainer {
		marker := filepath.Join(dir, ".dockerenv")
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		containerMarkerPath = marker
	} else {
		containerMarkerPath = filepath.Join(dir, "absent-marker")
	}
	if got := InContainer(); got != inContainer {
		t.Fatalf("stubbed InContainer() = %v, want %v", got, inContainer)
	}
}

func TestTuningStateDirScriptForm(t *testing.T) {
	stubContainerDetection(t, false)
	// 测试进程的工作目录（包目录）不是布局目录 → 现行默认根。
	if got := tuningStateDir(); got != "/etc/flmx_agent" {
		t.Fatalf("script form tuning state root changed: got %q, want /etc/flmx_agent", got)
	}
}

// 脚本形态在布局目录内运行（unit 的 WorkingDirectory）时锚到该
// 目录本身：现行与存量两种布局名同等对待。
func TestTuningStateDirScriptFormLayoutWorkDir(t *testing.T) {
	stubContainerDetection(t, false)
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	for _, name := range []string{"flmx_agent", "flux_agent"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		if got := tuningStateDir(); got != dir {
			t.Fatalf("script form in layout %q must anchor there, got %q", dir, got)
		}
	}
}

func TestTuningStatePathsScriptFormGold(t *testing.T) {
	// The package-level paths are resolved once at startup from the
	// real environment; whenever the test process itself is not in a
	// container they must be the script form's current paths.
	if InContainer() {
		t.Skip("test process runs in a container; script-form gold not applicable")
	}
	if tuneBackupPath != "/etc/flmx_agent/tuning-backup.json" {
		t.Fatalf("script form backup path drifted: %q", tuneBackupPath)
	}
	if tuneParamsPath != "/etc/flmx_agent/tuning-params.json" {
		t.Fatalf("script form params path drifted: %q", tuneParamsPath)
	}
	if got := tuneStatePath("tcpfit"); got != "/etc/flmx_agent/tcpfit" {
		t.Fatalf("script form engine dir drifted: %q", got)
	}
}

func TestTuningStateDirContainerFormUsesDataDir(t *testing.T) {
	stubContainerDetection(t, true)
	dataDir := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	if err := os.Chdir(dataDir); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := tuningStateDir(); got != wd {
		t.Fatalf("container form tuning state root = %q, want the data directory %q", got, wd)
	}
}
