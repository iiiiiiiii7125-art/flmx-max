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

func stubKernelResolution(t *testing.T, inContainer bool, wd string, wdErr error, exe string, exeErr error) {
	t.Helper()
	oldDetect, oldWD, oldExe := detectKernelContainer, kernelWorkDir, kernelExePath
	detectKernelContainer = func() bool { return inContainer }
	kernelWorkDir = func() (string, error) { return wd, wdErr }
	kernelExePath = func() (string, error) { return exe, exeErr }
	t.Cleanup(func() { detectKernelContainer, kernelWorkDir, kernelExePath = oldDetect, oldWD, oldExe })
}

func TestResolveBaseDirEnvOverrideWins(t *testing.T) {
	stubKernelResolution(t, true, "/data", nil, "/etc/flmx_agent/flmx_agent", nil)
	t.Setenv("FLMX_KERNEL_DIR", "/pinned/kernels")
	t.Setenv("FLVX_KERNEL_DIR", "/legacy/kernels")
	if got := ResolveBaseDir(); got != "/pinned/kernels" {
		t.Fatalf("FLMX_KERNEL_DIR must win over the container fallback, got %q", got)
	}
	// 2.0.0 起旧名 FLVX_KERNEL_DIR 双读退场：只设旧名必须被忽略，
	// 回落容器形态的工作目录根。
	t.Setenv("FLMX_KERNEL_DIR", "")
	if got := ResolveBaseDir(); got != filepath.Join("/data", "kernels") {
		t.Fatalf("retired FLVX_KERNEL_DIR must be ignored, got %q", got)
	}
}

func TestResolveBaseDirContainerFallsBackToWorkDir(t *testing.T) {
	t.Setenv("FLMX_KERNEL_DIR", "")
	stubKernelResolution(t, true, "/etc/flmx_agent", nil, "", errors.New("no exe"))
	want := filepath.Join("/etc/flmx_agent", "kernels")
	if got := ResolveBaseDir(); got != want {
		t.Fatalf("container form must root kernels on the data directory, got %q want %q", got, want)
	}
}

func TestResolveBaseDirScriptFormDefaultUnchanged(t *testing.T) {
	t.Setenv("FLMX_KERNEL_DIR", "")
	// Neither the executable nor the working directory names an
	// install: the fallback is still the gold default root.
	stubKernelResolution(t, false, "/anywhere", nil, "", errors.New("no exe"))
	if got := ResolveBaseDir(); got != defaultBaseDir {
		t.Fatalf("script form root changed: got %q want %q", got, defaultBaseDir)
	}
	stubKernelResolution(t, false, "/anywhere", nil, "relative/flmx_agent", nil)
	if got := ResolveBaseDir(); got != defaultBaseDir {
		t.Fatalf("relative executable path must fall back to the default root, got %q", got)
	}
	if defaultBaseDir != "/etc/flmx_agent/kernels" {
		t.Fatalf("script form gold value drifted: %q", defaultBaseDir)
	}
}

// F2-NFT-01: the script form's install identity is the agent binary's
// own directory, not the working directory. The unit runs the binary
// out of its install directory, so both layout generations anchor to
// their own kernels — and a second agent on the same host (scratch,
// relocated) roots under ITS OWN directory instead of falling through
// to the live install's root.
func TestResolveBaseDirScriptFormExeDir(t *testing.T) {
	t.Setenv("FLMX_KERNEL_DIR", "")
	for _, exe := range []string{"/etc/flmx_agent/flmx_agent", "/etc/flux_agent/flux_agent"} {
		// The working directory deliberately names nothing: the
		// executable alone must decide.
		stubKernelResolution(t, false, "/anywhere", nil, exe, nil)
		want := filepath.Join(filepath.Dir(exe), "kernels")
		if got := ResolveBaseDir(); got != want {
			t.Fatalf("script form with exe %q must root at %q, got %q", exe, want, got)
		}
	}
}

func TestResolveBaseDirScriptFormWorkDirFallback(t *testing.T) {
	t.Setenv("FLMX_KERNEL_DIR", "")
	// The executable path is unresolvable: the working directory is
	// a recognized install layout after all — root under it.
	for _, wd := range []string{"/etc/flmx_agent", "/etc/flux_agent"} {
		stubKernelResolution(t, false, wd, nil, "", errors.New("no exe"))
		want := filepath.Join(wd, "kernels")
		if got := ResolveBaseDir(); got != want {
			t.Fatalf("script form in layout %q must root at %q, got %q", wd, want, got)
		}
	}
}

// F2-NFT-01: the observed collision layout — a scratch agent whose
// binary sits directly in its scratch directory, launched from that
// directory (working directory names no layout). Before the fix it
// resolved to /etc/flmx_agent/kernels and overwrote the live
// agent's binaries and state.json.
func TestResolveBaseDirScratchExeDirIsolated(t *testing.T) {
	t.Setenv("FLMX_KERNEL_DIR", "")
	for _, dir := range []string{"/root/scratch-nodeconnlog", "/root/scratch-r2b2-f2"} {
		stubKernelResolution(t, false, dir, nil, filepath.Join(dir, "flmx_agent"), nil)
		want := filepath.Join(dir, "kernels")
		if got := ResolveBaseDir(); got != want {
			t.Fatalf("scratch agent in %q must root at %q, got %q", dir, want, got)
		}
	}
}

func TestResolveBaseDirContainerBadWorkDirFallsBackToDefault(t *testing.T) {
	t.Setenv("FLMX_KERNEL_DIR", "")
	// The executable names a real install, but the container branch
	// must not consult it: a container with an unusable working
	// directory falls back to the default root.
	stubKernelResolution(t, true, "", errors.New("no cwd"), "/etc/flmx_agent/flmx_agent", nil)
	if got := ResolveBaseDir(); got != defaultBaseDir {
		t.Fatalf("unusable working directory must fall back to the default root, got %q", got)
	}
	stubKernelResolution(t, true, "relative/dir", nil, "/etc/flmx_agent/flmx_agent", nil)
	if got := ResolveBaseDir(); got != defaultBaseDir {
		t.Fatalf("relative working directory must fall back to the default root, got %q", got)
	}
}
