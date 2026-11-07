//go:build !unix

package socket

// currentNoFileLimit is a no-op off unix: tuning targets Linux
// nodes, and the status face simply reports zeros elsewhere.
func currentNoFileLimit() (soft, hard uint64) { return 0, 0 }
