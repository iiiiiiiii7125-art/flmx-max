package net

import (
	"errors"
	"io"
)

// tcpInfo is the platform-neutral snapshot of the TCP_INFO
// fields the reaper's edge criterion reads. It exists so the
// sampler (reap.go) compiles on platforms without TCP_INFO;
// there tcpInfoOf simply reports unavailability, which the
// criterion treats as "no evidence" and never reaps on.
type tcpInfo struct {
	State         uint8  // TCP state; tcpStateEstablished when live
	Unacked       uint32 // segments offered to the peer, never ACKed
	Probes        uint8  // zero-window probes already sent
	BytesAcked    uint64 // bytes the peer has ACKed — the consumption signal
	BytesSent     uint64 // bytes offered toward the peer
	BytesReceived uint64 // bytes arrived from the peer
	NotsentBytes  uint32 // bytes stuck in the local send buffer
	SndWnd        uint32 // peer's advertised receive window; 0 = not consuming
	TotalRetrans  uint32 // lifetime retransmissions (corroboration)
}

const tcpStateEstablished = 1

var errTCPInfoUnavailable = errors.New("net: TCP_INFO unavailable for this connection")

// setLingerZero marks the socket behind rw (resolved through
// its SyscallConn chain) SO_LINGER with a zero timeout, so the
// subsequent Close aborts the connection (RST) instead of a
// graceful FIN. Best effort: it reports false when the chain
// cannot be resolved or the platform cannot set it, and callers
// fall back to a plain close.
func setLingerZero(rw io.ReadWriter) bool {
	return setLingerZeroImpl(rw)
}
