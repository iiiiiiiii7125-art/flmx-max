package socket

import (
	"os"
	"path/filepath"
	"testing"
)

// R2-020: the tuning state root (backup / params / tcpfit engine
// state) must anchor to the agent data directory in the container
// form, like every other piece of agent state, instead of the
// script form's /etc/flux_agent — inside the image that path is the
// ephemeral container layer, so a rebuild silently dropped the
// tuning backup and the committed shaper state. The script form's
// paths stay byte-identical.

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
	if got := tuningStateDir(); got != "/etc/flux_agent" {
		t.Fatalf("script form tuning state root changed: got %q, want /etc/flux_agent", got)
	}
}

func TestTuningStatePathsScriptFormGold(t *testing.T) {
	// The package-level paths are resolved once at startup from the
	// real environment; whenever the test process itself is not in a
	// container they must be the script form's historical paths.
	if InContainer() {
		t.Skip("test process runs in a container; script-form gold not applicable")
	}
	if tuneBackupPath != "/etc/flux_agent/tuning-backup.json" {
		t.Fatalf("script form backup path drifted: %q", tuneBackupPath)
	}
	if tuneParamsPath != "/etc/flux_agent/tuning-params.json" {
		t.Fatalf("script form params path drifted: %q", tuneParamsPath)
	}
	if got := tuneStatePath("tcpfit"); got != "/etc/flux_agent/tcpfit" {
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
