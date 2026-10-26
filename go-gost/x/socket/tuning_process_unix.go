package socket

import (
	"os/exec"
	"syscall"
	"time"
)

// prepareTuneProcess puts a tuning subprocess in its own process group
// so cancelling the job kills the whole tree (iperf3 included), not
// just the shell (upstream 3.3.43 tuning_process_unix.go).
func prepareTuneProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
}
