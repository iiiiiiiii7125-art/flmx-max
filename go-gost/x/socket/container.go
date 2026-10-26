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
		if strings.Contains(line, "docker") || strings.Contains(line, "lxc") || strings.Contains(line, "kubepods") || strings.Contains(line, "containerd") {
			return true
		}
	}
	return false
}
