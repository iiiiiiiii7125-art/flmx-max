//go:build !linux && !windows

package kernel

import "syscall"

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// sysProcAttrFor ignores the credential on this platform: the Snell
// egress guard is Linux/nftables-bound, and a Snell apply on a
// non-Linux agent already fails loudly at the nftables check before
// any process starts.
func sysProcAttrFor(uint32, uint32) *syscall.SysProcAttr {
	return sysProcAttr()
}

func signalGroup(pid int, sig syscall.Signal) {
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = syscall.Kill(pid, sig)
	}
}
