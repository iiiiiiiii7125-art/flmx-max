package socket

import (
	"os"
	"path/filepath"
	"testing"
)

// F2-11: InContainer must not count a bare "lxc" cgroup hit as
// container form. LXC system containers run the script-form install
// (systemd unit) and their cgroup v1 paths routinely contain
// "/lxc/<name>"; counting that as container form makes panel
// upgrade/rollback refuse forever with a receipt that misdirects to
// the Docker reinstall command. Docker/containerd/kubepods hits and
// the /.dockerenv marker stay container-form signals.
func TestInContainerLxcCgroupIsScriptForm(t *testing.T) {
	dir := t.TempDir()
	oldMarker, oldCgroup := containerMarkerPath, containerCgroupPath
	containerMarkerPath = filepath.Join(dir, "no-dockerenv")
	cgroup := filepath.Join(dir, "cgroup")
	containerCgroupPath = cgroup
	t.Cleanup(func() { containerMarkerPath, containerCgroupPath = oldMarker, oldCgroup })

	cases := []struct {
		name  string
		lines string
		want  bool
	}{
		{"lxc system container", "12:devices:/lxc/test-node\n11:memory:/lxc/test-node\n", false},
		{"docker", "12:devices:/docker/abc123\n", true},
		{"containerd", "12:devices:/system.slice/containerd.service\n", true},
		{"kubepods", "12:devices:/kubepods.slice/kubepods-burstable.slice\n", true},
		{"plain host", "12:devices:/\n11:memory:/\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(cgroup, []byte(tc.lines), 0644); err != nil {
				t.Fatal(err)
			}
			if got := InContainer(); got != tc.want {
				t.Fatalf("InContainer() = %v, want %v (cgroup %q)", got, tc.want, tc.lines)
			}
		})
	}
}

func TestInContainerDockerenvMarker(t *testing.T) {
	dir := t.TempDir()
	oldMarker, oldCgroup := containerMarkerPath, containerCgroupPath
	marker := filepath.Join(dir, ".dockerenv")
	containerMarkerPath = marker
	containerCgroupPath = filepath.Join(dir, "no-cgroup")
	t.Cleanup(func() { containerMarkerPath, containerCgroupPath = oldMarker, oldCgroup })
	if InContainer() {
		t.Fatal("missing marker + missing cgroup must not be container form")
	}
	if err := os.WriteFile(marker, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	if !InContainer() {
		t.Fatal("/.dockerenv present must be container form")
	}
}
