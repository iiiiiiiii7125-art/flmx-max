package socket

// R7-C-02: agent-side outbound policy for the one-click probe.
//
// probeSpeedtestDownload used http.DefaultClient (arbitrary redirect
// following) and probeReferenceConnectRTT dialed any host:port with a
// bare net.DialTimeout, so a panel admin could make every managed
// node fetch from, or port-scan, its own LAN / cloud metadata —
// panel admin rights are not node-LAN rights. Both faces now judge
// their target before any dial, with the mirrored netguard
// classifier (x/bypass.IsNonPublicIP, the agent-side mirror of
// go-backend/internal/netguard):
//
//   - hard-deny layer always (metadata exact set, 169.254/16,
//     unspecified, local NAT64 segment, NAT64/6to4 embedded forms);
//   - every non-public range rejected by default (loopback, RFC1918,
//     ULA, CGNAT, link-local, reserved, multicast);
//   - speedtest redirects re-judged per hop (max 5) and the dial
//     itself is pinned to IPs judged at dial time, so DNS rebinding
//     cannot swap the target between check and connect.
//
// Allowlist design (explicit, no silent hole): there is deliberately
// NO private-target exception in this change. A deployment that
// legitimately speed-tests against a private target needs a new
// explicit channel — a panel-side CIDR allowlist (e.g.
// FLMX_SPEEDTEST_PRIVATE_ALLOWLIST) judged on the panel before
// dispatch and forwarded to the agent as part of the probe payload,
// with the agent re-judging the target against exactly that list.
// Until such a channel exists, private probe targets are rejected on
// both sides. Side effect, accepted: the optional panelRttMs leg
// auto-fills ReferenceHost from the agent's own panel connection —
// a panel reachable only at a private address now yields
// panelRttMs unavailable instead of a dialed reference; the leg is
// reference-only and never changes the suggested line.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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

// validateProbeSpeedtestURL judges a speedtest URL before dispatch:
// http/https only, real host (url.Parse Hostname, so userinfo
// masking is judged on the host actually dialed), all resolved IPs
// public.
func validateProbeSpeedtestURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("测速地址无效，已拒绝")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("测速地址须为 http(s) 链接")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("测速地址主机为空，已拒绝")
	}
	_, err = judgeProbeHost(ctx, host)
	return err
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

// probeDialContext pins the speedtest dial: re-resolve at dial time,
// re-judge every IP, dial a validated IP (TOCTOU / rebinding safe).
func probeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := judgeProbeHost(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: probeReferenceTimeout + 5*time.Second}
	var lastErr error
	for _, ip := range ips {
		dialIP := ip
		if ip4 := ip.To4(); ip4 != nil {
			dialIP = ip4
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(dialIP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func probeSpeedtestClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // pinned dialing is meaningless via a proxy
	tr.DialContext = probeDialContext
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("测速地址重定向次数过多，已拒绝")
			}
			return validateProbeSpeedtestURL(req.Context(), req.URL.String())
		},
	}
}
