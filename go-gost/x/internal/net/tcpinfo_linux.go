package net

import (
	"io"
	"syscall"

	"golang.org/x/sys/unix"
)

// tcpInfoOf resolves the syscall.Conn chain of a (possibly
// wrapped) conn and reads TCP_INFO from its socket. Any layer
// refusing, or any syscall failure, yields an error — callers
// must treat that as "no evidence", never as a verdict.
func tcpInfoOf(c io.ReadWriter) (*tcpInfo, error) {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return nil, errTCPInfoUnavailable
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return nil, errTCPInfoUnavailable
	}
	return readTCPInfo(rc)
}

// tcpInfoProbe resolves the conn's RawConn once and returns a
// closure that reads TCP_INFO through it, or nil when the chain
// cannot be resolved (wrapper without SyscallConn delegation,
// non-TCP socket). The registry resolves once at session
// registration because wrapper chains are immutable for the
// session's life.
func tcpInfoProbe(c io.ReadWriter) func() (*tcpInfo, error) {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return nil
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return nil
	}
	return func() (*tcpInfo, error) {
		return readTCPInfo(rc)
	}
}

func readTCPInfo(rc syscall.RawConn) (*tcpInfo, error) {
	var info *tcpInfo
	var gerr error
	cerr := rc.Control(func(fd uintptr) {
		ti, err := unix.GetsockoptTCPInfo(int(fd), unix.SOL_TCP, unix.TCP_INFO)
		if err != nil {
			gerr = err
			return
		}
		info = &tcpInfo{
			State:         ti.State,
			Unacked:       ti.Unacked,
			Probes:        ti.Probes,
			BytesAcked:    ti.Bytes_acked,
			BytesSent:     ti.Bytes_sent,
			BytesReceived: ti.Bytes_received,
			NotsentBytes:  ti.Notsent_bytes,
			SndWnd:        ti.Snd_wnd,
			TotalRetrans:  ti.Total_retrans,
		}
	})
	if cerr != nil {
		return nil, errTCPInfoUnavailable
	}
	if gerr != nil {
		return nil, gerr
	}
	return info, nil
}

func setLingerZeroImpl(rw io.ReadWriter) bool {
	sc, ok := rw.(syscall.Conn)
	if !ok {
		return false
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return false
	}
	var serr error
	cerr := rc.Control(func(fd uintptr) {
		serr = unix.SetsockoptLinger(int(fd), unix.SOL_SOCKET, unix.SO_LINGER,
			&unix.Linger{Onoff: 1, Linger: 0})
	})
	return cerr == nil && serr == nil
}
