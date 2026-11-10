package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

// F2-11 (kernel half): probeKernelContainer must stay in sync with
// socket.InContainer — a bare "lxc" cgroup hit is not container
// form (LXC system containers run the script-form install); only
// docker/containerd/kubepods hits and the /.dockerenv marker count.
func TestProbeKernelContainerLxcCgroupIsScriptForm(t *testing.T) {
	dir := t.TempDir()
	oldMarker, oldCgroup := kernelContainerMarkerPath, kernelContainerCgroupPath
	kernelContainerMarkerPath = filepath.Join(dir, "no-dockerenv")
	cgroup := filepath.Join(dir, "cgroup")
	kernelContainerCgroupPath = cgroup
	t.Cleanup(func() { kernelContainerMarkerPath, kernelContainerCgroupPath = oldMarker, oldCgroup })

	cases := []struct {
		name  string
		lines string
		want  bool
	}{
		{"lxc system container", "12:devices:/lxc/test-node\n", false},
		{"docker", "12:devices:/docker/abc123\n", true},
		{"containerd", "12:devices:/system.slice/containerd.service\n", true},
		{"kubepods", "12:devices:/kubepods.slice/kubepods-burstable.slice\n", true},
		{"plain host", "12:devices:/\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(cgroup, []byte(tc.lines), 0644); err != nil {
				t.Fatal(err)
			}
			if got := probeKernelContainer(); got != tc.want {
				t.Fatalf("probeKernelContainer() = %v, want %v (cgroup %q)", got, tc.want, tc.lines)
			}
		})
	}
}
