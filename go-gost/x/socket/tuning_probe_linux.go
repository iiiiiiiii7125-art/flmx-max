//go:build linux

package socket

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

// sock_diag source for the probe's RTT leg (Linux only).
//
// Request: NETLINK_INET_DIAG (4) / SOCK_DIAG_BY_FAMILY (20), dump of
// all TCP sockets, with the INET_DIAG_INFO extension bit
// (1 << (INET_DIAG_INFO-1)) so the kernel appends struct tcp_info.
// Response: per socket an inet_diag_msg (72 bytes: family/state/timer/
// retrans + 48-byte sockid + expires/rqueue/wqueue/uid/inode) followed
// by rtattr attributes; the INET_DIAG_INFO attribute (type 2) carries
// struct tcp_info, whose tcpi_rtt field (µs, smoothed RTT) sits at
// offset 72 in that blob (12-byte u8 header incl. padding, then u32
// fields in kernel order: rto, ato, snd_mss, rcv_mss, unacked, sacked,
// lost, retrans, fackets, last_data_sent, last_ack_sent, last_data_recv,
// last_ack_recv, pmtu, rcv_ssthresh, rtt, ...).
//
// Only TCP_ESTABLISHED (state 1) sockets with rtt > 0 are returned:
// other states have no meaningful RTT, and rtt == 0 means the kernel
// has not measured this connection yet. Netlink payloads are host
// endian (little-endian on the agent's Linux targets). Any failure
// (non-Linux is handled by the fallback file, permission, short read)
// surfaces as an error and the probe reports RTT unavailable — it is
// never papered over with a guessed value. Unprivileged agents still
// see their own process's sockets, which is exactly the forwarded
// connections this tuning sizes buffers for.

const (
	sockDiagByFamily = 20
	inetDiagInfoAttr = 2
	tcpEstablished   = 1
	inetDiagMsgLen   = 72
	tcpInfoRttOff    = 72
	tcpInfoMinLen    = 76
)

func sockDiagTCPRTTs() ([]uint32, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_INET_DIAG)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	// Bound the dump read: a kernel that never sends NLMSG_DONE must
	// not hang the probe (and therefore the panel's 30s command
	// timeout) forever.
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 5})

	// nlmsghdr (16) + inet_diag_req_v2 (56).
	req := make([]byte, 72)
	binary.LittleEndian.PutUint32(req[0:4], 72)
	binary.LittleEndian.PutUint16(req[4:6], sockDiagByFamily)
	binary.LittleEndian.PutUint16(req[6:8], unix.NLM_F_REQUEST|unix.NLM_F_DUMP)
	binary.LittleEndian.PutUint32(req[8:12], 1)
	// pid 0 = kernel.
	req[16] = 0 // AF_UNSPEC: both families
	req[17] = unix.IPPROTO_TCP
	req[18] = 1 << (2 - 1)                           // INET_DIAG_INFO extension bit
	binary.LittleEndian.PutUint32(req[20:24], 0xfff) // all TCP states
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}

	var rtts []uint32
	buf := make([]byte, 64<<10)
	done := false
	for !done {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return nil, err
		}
		off := 0
		for off+16 <= n {
			length := int(binary.LittleEndian.Uint32(buf[off : off+4]))
			msgType := binary.LittleEndian.Uint16(buf[off+4 : off+6])
			if length < 16 || off+length > n {
				return nil, fmt.Errorf("sock_diag 响应截断")
			}
			payload := buf[off+16 : off+length]
			switch msgType {
			case unix.NLMSG_DONE:
				done = true
			case unix.NLMSG_ERROR:
				if len(payload) >= 4 {
					if code := int32(binary.LittleEndian.Uint32(payload[0:4])); code != 0 {
						return nil, fmt.Errorf("sock_diag 内核错误 %d", -code)
					}
				}
			case sockDiagByFamily:
				if len(payload) < inetDiagMsgLen {
					continue
				}
				if payload[1] != tcpEstablished {
					continue
				}
				// Attributes after the 72-byte message.
				ao := inetDiagMsgLen
				for ao+4 <= len(payload) {
					alen := int(binary.LittleEndian.Uint16(payload[ao : ao+2]))
					atype := binary.LittleEndian.Uint16(payload[ao+2 : ao+4])
					if alen < 4 || ao+alen > len(payload) {
						break
					}
					if atype == inetDiagInfoAttr {
						info := payload[ao+4 : ao+alen]
						if len(info) >= tcpInfoMinLen {
							if rtt := binary.LittleEndian.Uint32(info[tcpInfoRttOff : tcpInfoRttOff+4]); rtt > 0 {
								rtts = append(rtts, rtt)
							}
						}
					}
					// rtattr lengths are 4-byte aligned.
					ao += (alen + 3) &^ 3
				}
			}
			off += (length + 3) &^ 3
		}
	}
	return rtts, nil
}
