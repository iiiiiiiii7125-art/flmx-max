//go:build unix

package socket

import "syscall"

// currentNoFileLimit reports this process's RLIMIT_NOFILE soft and
// hard values (0, 0 when unreadable). The limits.d file written by
// face ③ only takes effect for processes started through a PAM login
// session; a systemd service keeps whatever its unit sets (the agent
// unit sets no LimitNOFILE), so the status reports the live values
// rather than the file's.
func currentNoFileLimit() (soft, hard uint64) {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return 0, 0
	}
	return rl.Cur, rl.Max
}
