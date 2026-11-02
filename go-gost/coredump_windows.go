//go:build windows

package main

// disableCoreDumps is a no-op on Windows: there is no RLIMIT_CORE and
// no core_pattern file convention to defend against.
func disableCoreDumps() {}
