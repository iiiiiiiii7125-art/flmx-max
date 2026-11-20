//go:build linux

package socket

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

// buildDiagMsg builds a synthetic inet_diag_msg payload: the 72-byte
// message plus one INET_DIAG_INFO attribute carrying a tcp_info blob.
func buildDiagMsg(t *testing.T, state byte, info []byte) []byte {
	t.Helper()
	msg := make([]byte, inetDiagMsgLen)
	msg[1] = state
	alen := 4 + len(info)
	attr := make([]byte, (alen+3)&^3)
	binary.LittleEndian.PutUint16(attr[0:2], uint16(alen))
	binary.LittleEndian.PutUint16(attr[2:4], inetDiagInfoAttr)
	copy(attr[4:], info)
	return append(msg, attr...)
}

// tcpInfoBlob lays out a tcp_info blob with the given rtt/rttvar at
// the kernel offsets (rtt 68, rttvar 72 per include/uapi/linux/tcp.h).
func tcpInfoBlob(rtt, rttvar uint32) []byte {
	blob := make([]byte, 104)
	binary.LittleEndian.PutUint32(blob[68:72], rtt)
	binary.LittleEndian.PutUint32(blob[72:76], rttvar)
	return blob
}

// The dump must be issued per family: AF_UNSPEC is rejected with
// NLMSG_ERROR -2 by some kernels (Debian 6.1 cloud), so the family
// byte of the request is pinned here.
func TestBuildDiagRequestFamily(t *testing.T) {
	for _, family := range []byte{2, 10} { // AF_INET, AF_INET6
		req := buildDiagRequest(family)
		if len(req) != 72 {
			t.Fatalf("family %d: len %d, want 72", family, len(req))
		}
		if req[16] != family {
			t.Fatalf("family %d: req[16]=%d, want %d", family, req[16], family)
		}
		if binary.LittleEndian.Uint16(req[4:6]) != 20 {
			t.Fatalf("family %d: type %d, want 20", family, binary.LittleEndian.Uint16(req[4:6]))
		}
		if req[17] != 6 { // IPPROTO_TCP
			t.Fatalf("family %d: protocol %d, want 6", family, req[17])
		}
		if req[18] != 2 { // INET_DIAG_INFO extension bit
			t.Fatalf("family %d: ext %d, want 2", family, req[18])
		}
		if binary.LittleEndian.Uint32(req[20:24]) != 0xfff {
			t.Fatalf("family %d: states %#x, want 0xfff", family, binary.LittleEndian.Uint32(req[20:24]))
		}
	}
}

// buildDiagDatagram packs diag-message payloads plus NLMSG_DONE
// into one netlink datagram, the unit sockDiagDump reads at a time.
func buildDiagDatagram(t *testing.T, payloads ...[]byte) []byte {
	t.Helper()
	var dg []byte
	for _, p := range payloads {
		hdr := make([]byte, 16)
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(16+len(p)))
		binary.LittleEndian.PutUint16(hdr[4:6], sockDiagByFamily)
		dg = append(dg, hdr...)
		dg = append(dg, p...)
		for len(dg)%4 != 0 {
			dg = append(dg, 0)
		}
	}
	done := make([]byte, 16)
	binary.LittleEndian.PutUint32(done[0:4], 16)
	binary.LittleEndian.PutUint16(done[4:6], unix.NLMSG_DONE)
	return append(dg, done...)
}

// R18-D2-14: the old fixed 64KB buffer silently truncated a busy
// node's dump datagram and the parse failed the whole RTT leg.
// The dump must now detect the truncation via MSG_TRUNC and retry
// with a bigger buffer instead of failing. The recv path is staged
// through the sockDiagRecvfrom seam: a live kernel cannot be relied
// on here (this sandbox's kernel answers every inet_diag dump with
// NLMSG_ERROR -2, as does ss — it falls back to /proc), so the
// truncation/retry ladder is pinned by stub, and the live leg
// still needs a real host to exercise.
func TestR18D214SockDiagDumpRetriesAfterTruncation(t *testing.T) {
	old := sockDiagRecvfrom
	defer func() { sockDiagRecvfrom = old }()
	var bufSizes []int
	calls := 0
	sockDiagRecvfrom = func(fd int, p []byte, flags int) (int, unix.Sockaddr, error) {
		bufSizes = append(bufSizes, len(p))
		if flags&unix.MSG_TRUNC == 0 {
			t.Errorf("recv call %d: MSG_TRUNC flag missing (flags=%d)", calls+1, flags)
		}
		calls++
		if calls == 1 {
			// First attempt (256KB buffer): the kernel's datagram
			// does not fit — MSG_TRUNC reports its true length.
			return len(p) + 1, nil, nil
		}
		// Second attempt (1MB buffer): one established socket
		// plus DONE, delivered whole.
		dg := buildDiagDatagram(t, buildDiagMsg(t, tcpEstablished, tcpInfoBlob(12345, 0)))
		copy(p, dg)
		return len(dg), nil, nil
	}
	rtts, err := sockDiagDump(unix.AF_INET)
	if err != nil {
		t.Fatalf("dump after truncation retry: %v", err)
	}
	if len(rtts) != 1 || rtts[0] != 12345 {
		t.Fatalf("rtts = %v, want [12345]", rtts)
	}
	if len(bufSizes) != 2 || bufSizes[0] != 256<<10 || bufSizes[1] != 1<<20 {
		t.Fatalf("retry ladder = %v, want [262144 1048576]", bufSizes)
	}
}

func TestR18D214SockDiagDumpGivesUpAfterLadderExhausted(t *testing.T) {
	old := sockDiagRecvfrom
	defer func() { sockDiagRecvfrom = old }()
	var bufSizes []int
	sockDiagRecvfrom = func(fd int, p []byte, flags int) (int, unix.Sockaddr, error) {
		bufSizes = append(bufSizes, len(p))
		return len(p) + 1, nil, nil
	}
	_, err := sockDiagDump(unix.AF_INET)
	if err == nil {
		t.Fatal("dump must fail once every buffer size is exhausted")
	}
	if len(bufSizes) != 3 || bufSizes[0] != 256<<10 || bufSizes[1] != 1<<20 || bufSizes[2] != 4<<20 {
		t.Fatalf("retry ladder = %v, want [262144 1048576 4194304]", bufSizes)
	}
}
// code read offset 72 (tcpi_rttvar), which is 0 on stable connections
// and made the probe see zero samples on every real machine.
func TestExtractTCPRTT(t *testing.T) {
	for _, c := range []struct {
		name    string
		payload []byte
		want    uint32
		wantOK  bool
	}{
		{
			"rtt at 68 with rttvar 0",
			buildDiagMsg(t, tcpEstablished, tcpInfoBlob(12345, 0)),
			12345, true,
		},
		{
			"rtt and rttvar both set, rtt wins",
			buildDiagMsg(t, tcpEstablished, tcpInfoBlob(500, 700)),
			500, true,
		},
		{
			"rtt 0 with rttvar set must be excluded",
			buildDiagMsg(t, tcpEstablished, tcpInfoBlob(0, 999)),
			0, false,
		},
		{
			"non-established socket ignored",
			buildDiagMsg(t, 6, tcpInfoBlob(12345, 0)),
			0, false,
		},
		{
			"no info attribute",
			buildDiagMsg(t, tcpEstablished, nil)[:inetDiagMsgLen],
			0, false,
		},
		{
			"truncated tcp_info blob",
			buildDiagMsg(t, tcpEstablished, make([]byte, 40)),
			0, false,
		},
	} {
		if got, ok := extractTCPRTT(c.payload); got != c.want || ok != c.wantOK {
			t.Errorf("%s: got (%d,%v) want (%d,%v)", c.name, got, ok, c.want, c.wantOK)
		}
	}
}
