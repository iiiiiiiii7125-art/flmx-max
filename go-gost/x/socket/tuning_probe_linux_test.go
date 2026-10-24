//go:build linux

package socket

import (
	"encoding/binary"
	"testing"
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

// The offset is pinned here, not by a live netlink dump: the old
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
