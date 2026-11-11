//go:build !linux

package net

import (
	"io"
)

// Non-Linux builds have no TCP_INFO access in this package: the
// probe is unavailable and the edge reaper stays inert (the
// agent's nodes are Linux; the stub exists so the module builds
// everywhere, and unavailability is fail-safe by design).

func tcpInfoOf(c io.ReadWriter) (*tcpInfo, error) {
	return nil, errTCPInfoUnavailable
}

func tcpInfoProbe(c io.ReadWriter) func() (*tcpInfo, error) {
	return nil
}

func setLingerZeroImpl(rw io.ReadWriter) bool {
	return false
}
