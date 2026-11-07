package socket

// R7-C-02: agent-side outbound policy for the one-click probe.
//
// probeReferenceConnectRTT used to dial any host:port with a bare
// net.DialTimeout, so a panel admin could make every managed node
// port-scan its own LAN / cloud metadata — panel admin rights are
// not node-LAN rights. The reference target is now judged before
// any dial, with the mirrored netguard classifier
// (x/bypass.IsNonPublicIP, the agent-side mirror of
// go-backend/internal/netguard):
//
//   - hard-deny layer always (metadata exact set, 169.254/16,
//     unspecified, local NAT64 segment, NAT64/6to4 embedded forms);
//   - every non-public range rejected by default (loopback, RFC1918,
//     ULA, CGNAT, link-local, reserved, multicast);
//   - the target is re-judged on the resolved IPs, so DNS answers
//     pointing at non-public space are rejected wholesale.
//
// Allowlist design (explicit, no silent hole): there is deliberately
// NO private-target exception in this change. A deployment that
// legitimately probes against a private target needs a new explicit
// channel — a panel-side CIDR allowlist judged on the panel before
// dispatch and forwarded to the agent as part of the probe payload,
// with the agent re-judging the target against exactly that list.
// Until such a channel exists, private probe targets are rejected.
// Side effect, accepted: the optional panelRttMs leg auto-fills
// ReferenceHost from the agent's own panel connection — a panel
// reachable only at a private address now yields panelRttMs
// unavailable instead of a dialed reference; the leg is
// reference-only and never changes the suggested line.

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/go-gost/x/bypass"
)

var probeLookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// judgeProbeHost resolves host (literal or name) and returns the
// validated IPs; any non-public answer rejects the whole target
// (fail-closed, resolution failure included).
func judgeProbeHost(ctx context.Context, host string) ([]net.IP, error) {
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		var err error
		ips, err = probeLookupIP(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("目标地址无法解析，已拒绝: %s", host)
		}
	}
	for _, ip := range ips {
		if bypass.IsNonPublicIP(ip) {
			return nil, fmt.Errorf("目标地址指向非公网地址，已拒绝: %s", host)
		}
	}
	return ips, nil
}

// validateProbeReferenceHost judges a host:port reference target:
// port must be in 1–65535 and every resolved IP public.
func validateProbeReferenceHost(ctx context.Context, hostport string) (host, port string, ips []net.IP, err error) {
	host, port, err = net.SplitHostPort(strings.TrimSpace(hostport))
	if err != nil {
		return "", "", nil, fmt.Errorf("参考地址须为 host:port 格式")
	}
	p, perr := strconv.Atoi(port)
	if perr != nil || p < 1 || p > 65535 {
		return "", "", nil, fmt.Errorf("参考地址端口超出范围，已拒绝: %s", port)
	}
	ips, err = judgeProbeHost(ctx, host)
	if err != nil {
		return "", "", nil, err
	}
	return host, port, ips, nil
}
