//go:build !windows

package main

import "syscall"

// disableCoreDumps zeroes this process's core size limit. The limit
// is inherited across fork/exec, so the kernels and helpers the agent
// supervises cannot leave core files behind either. A failure is not
// fatal: the startup sweep still removes any stockpile, and the
// systemd unit's LimitCORE=0 backs this up on systemd hosts.
func disableCoreDumps() {
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
}
