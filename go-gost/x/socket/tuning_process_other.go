//go:build !unix

package socket

import "os/exec"

// prepareTuneProcess is a no-op off unix (fork addition; upstream ships
// the unix file only): automatic tuning rejects non-Linux platforms
// before any subprocess starts, so no process-group handling is needed.
func prepareTuneProcess(cmd *exec.Cmd) {}
