//go:build linux

package kernel

import "syscall"

// Kernels run in their own process group and die with the agent, so a
// crashed agent never leaves an orphan holding the inbound ports.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

// sysProcAttrFor additionally drops the child to the given user when
// uid > 0. Only snell-server processes get a credential (the Snell
// egress guard matches their packets by skuid); supplementary groups
// are dropped so the child keeps no other identity.
func sysProcAttrFor(uid, gid uint32) *syscall.SysProcAttr {
	attr := sysProcAttr()
	if uid > 0 {
		attr.Credential = &syscall.Credential{Uid: uid, Gid: gid, NoSetGroups: true}
	}
	return attr
}

func signalGroup(pid int, sig syscall.Signal) {
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = syscall.Kill(pid, sig)
	}
}
