package kernel

import (
	"errors"
	"path/filepath"
	"testing"
)

// R2-003: the kernel state root must land on the container install
// form's data volume (the image pins FLMX_KERNEL_DIR there, and
// ResolveBaseDir falls back to the working directory — the data
// directory — when the variable is missing), while the script form's
// root and the explicit env override stay exactly as before. Before
// the fix the container form silently used the script form's root
// inside the ephemeral container layer and lost every kernel binary
// and state.json on each container rebuild.

func stubKernelResolution(t *testing.T, inContainer bool, wd string, wdErr error) {
	t.Helper()
	oldDetect, oldWD := detectKernelContainer, kernelWorkDir
	detectKernelContainer = func() bool { return inContainer }
	kernelWorkDir = func() (string, error) { return wd, wdErr }
	t.Cleanup(func() { detectKernelContainer, kernelWorkDir = oldDetect, oldWD })
}

func TestResolveBaseDirEnvOverrideWins(t *testing.T) {
	stubKernelResolution(t, true, "/data", nil)
	t.Setenv("FLMX_KERNEL_DIR", "/pinned/kernels")
	t.Setenv("FLVX_KERNEL_DIR", "/legacy/kernels")
	if got := ResolveBaseDir(); got != "/pinned/kernels" {
		t.Fatalf("FLMX_KERNEL_DIR must win over the container fallback, got %q", got)
	}
	t.Setenv("FLMX_KERNEL_DIR", "")
	if got := ResolveBaseDir(); got != "/legacy/kernels" {
		t.Fatalf("legacy FLVX_KERNEL_DIR must still be honored, got %q", got)
	}
}

func TestResolveBaseDirContainerFallsBackToWorkDir(t *testing.T) {
	t.Setenv("FLMX_KERNEL_DIR", "")
	t.Setenv("FLVX_KERNEL_DIR", "")
	stubKernelResolution(t, true, "/etc/flmx_agent", nil)
	want := filepath.Join("/etc/flmx_agent", "kernels")
	if got := ResolveBaseDir(); got != want {
		t.Fatalf("container form must root kernels on the data directory, got %q want %q", got, want)
	}
}

func TestResolveBaseDirScriptFormDefaultUnchanged(t *testing.T) {
	t.Setenv("FLMX_KERNEL_DIR", "")
	t.Setenv("FLVX_KERNEL_DIR", "")
	stubKernelResolution(t, false, "/anywhere", nil)
	if got := ResolveBaseDir(); got != defaultBaseDir {
		t.Fatalf("script form root changed: got %q want %q", got, defaultBaseDir)
	}
	if defaultBaseDir != "/etc/flmx_agent/kernels" {
		t.Fatalf("script form gold value drifted: %q", defaultBaseDir)
	}
}

// 改名双认：脚本形态的工作目录就是安装目录（unit 的
// WorkingDirectory），现行与存量两种布局名都锚到自身目录下的
// kernels——存量节点经面板原地升级时内核状态不换根、不丢失，
// install.sh 迁移整目录后新根随目录一并到达。
func TestResolveBaseDirScriptFormLayoutWorkDir(t *testing.T) {
	t.Setenv("FLMX_KERNEL_DIR", "")
	t.Setenv("FLVX_KERNEL_DIR", "")
	for _, wd := range []string{"/etc/flmx_agent", "/etc/flux_agent"} {
		stubKernelResolution(t, false, wd, nil)
		want := filepath.Join(wd, "kernels")
		if got := ResolveBaseDir(); got != want {
			t.Fatalf("script form in layout %q must root at %q, got %q", wd, want, got)
		}
	}
}

func TestResolveBaseDirContainerBadWorkDirFallsBackToDefault(t *testing.T) {
	t.Setenv("FLMX_KERNEL_DIR", "")
	t.Setenv("FLVX_KERNEL_DIR", "")
	stubKernelResolution(t, true, "", errors.New("no cwd"))
	if got := ResolveBaseDir(); got != defaultBaseDir {
		t.Fatalf("unusable working directory must fall back to the default root, got %q", got)
	}
	stubKernelResolution(t, true, "relative/dir", nil)
	if got := ResolveBaseDir(); got != defaultBaseDir {
		t.Fatalf("relative working directory must fall back to the default root, got %q", got)
	}
}
