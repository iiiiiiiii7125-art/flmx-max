package socket

import (
	"os"
	"strings"
)

// InContainer reports whether the agent runs inside a container.
// Upstream 3.3.43 reads this from x/socket/container.go (part of the
// D34-3 container support this fork did not port); only this one
// function is pulled in for the tuning restore path, which needs it
// to decide between a systemd unit and re-applying a committed
// shaper at agent start.
//
// F2-11: a bare "lxc" cgroup hit is NOT a container-form signal.
// LXC system containers run the script-form install (systemd unit),
// and their cgroup v1 paths routinely contain "/lxc/<name>"; counting
// that as container form makes panel upgrade/rollback refuse forever
// with a receipt that misdirects to the Docker reinstall command.
// Container form here means the Docker image install only, signalled
// by /.dockerenv or a docker/containerd/kubepods cgroup hit.
var (
	containerMarkerPath = "/.dockerenv"
	containerCgroupPath = "/proc/self/cgroup"
)

func InContainer() bool {
	if _, err := os.Stat(containerMarkerPath); err == nil {
		return true
	}
	b, err := os.ReadFile(containerCgroupPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, "docker") || strings.Contains(line, "kubepods") || strings.Contains(line, "containerd") {
			return true
		}
	}
	return false
}
