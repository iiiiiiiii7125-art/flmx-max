//go:build windows

package kernel

import (
	"os"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr { return nil }

// sysProcAttrFor ignores the credential on Windows; see sys_other.go.
func sysProcAttrFor(uint32, uint32) *syscall.SysProcAttr { return nil }

func signalGroup(pid int, _ syscall.Signal) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
