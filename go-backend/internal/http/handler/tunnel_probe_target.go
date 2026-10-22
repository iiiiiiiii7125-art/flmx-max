package handler

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"go-backend/internal/netguard"
	"go-backend/internal/store/model"
)

// tunnelProbeTargetLookupIP 是保存面主机名解析入口（var 以便测试注入）。
var tunnelProbeTargetLookupIP = net.LookupIP

const (
	defaultTunnelProbeTargetHost = "www.bing.com"
	defaultTunnelProbeTargetPort = 443
)

type tunnelProbeTarget struct {
	Host string
	Port int
}

func defaultTunnelProbeTarget() tunnelProbeTarget {
	return tunnelProbeTarget{Host: defaultTunnelProbeTargetHost, Port: defaultTunnelProbeTargetPort}
}

func normalizeTunnelProbeTarget(host string, port int) (tunnelProbeTarget, bool, error) {
	host = strings.TrimSpace(host)
	if host == "" && port == 0 {
		return defaultTunnelProbeTarget(), false, nil
	}
	if host == "" {
		return tunnelProbeTarget{}, false, errors.New("测试目标 Host 不能为空")
	}
	if port <= 0 || port > 65535 {
		return tunnelProbeTarget{}, false, errors.New("测试目标端口必须是 1-65535")
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#") || strings.ContainsAny(host, " \t\r\n") || isTunnelProbeTargetSchemeLikeHost(host) {
		return tunnelProbeTarget{}, false, errors.New("测试目标 Host 不能包含协议或路径")
	}
	if normalized, ok := normalizeTunnelProbeTargetHost(host); ok {
		host = normalized
	} else {
		return tunnelProbeTarget{}, false, errors.New("测试目标 Host 格式无效")
	}
	if err := validateTunnelProbeTargetHostPolicy(host); err != nil {
		return tunnelProbeTarget{}, false, err
	}

	return tunnelProbeTarget{Host: host, Port: port}, true, nil
}

// fakeIPBenchmarkPrefix 是 fake-IP DNS 的合成地址段（198.18.0.0/15，
// RFC 2544 基准测试段，Clash 等 fake-IP 实现的默认地址池）。
var fakeIPBenchmarkPrefix = netip.MustParsePrefix("198.18.0.0/15")

// validateTunnelProbeTargetHostPolicy 是 R3X-011 的保存面政策校验
// （格式校验之后）：字面 IP 走 netguard PolicyHardDenyOrLoopback；
// 主机名先过回环别名名单（与导入面同名单，确定性拒绝），再保存时
// 解析一次同判——任一解析结果命中同政策即拒存，解析失败/无结果
// 不拒存（执行面另有下发跳过与节点侧复检兜底）。不采 PublicOnly：
// 私网/CGNAT 解析结果与字面量同口径放行（管理员测内网网关合法）。
func validateTunnelProbeTargetHostPolicy(host string) error {
	if addr, err := netip.ParseAddr(host); err == nil {
		if d := netguard.JudgeAddr(addr, netguard.PolicyHardDenyOrLoopback, false); !d.Allowed {
			return errors.New("测试目标地址不允许")
		}
		return nil
	}
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return errors.New("测试目标地址不允许")
	}
	switch lower {
	case "ip6-localhost", "ip6-loopback", "localhost.localdomain":
		return errors.New("测试目标地址不允许")
	}
	ips, err := tunnelProbeTargetLookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil
	}
	for _, ip := range ips {
		d := netguard.Judge(ip, netguard.PolicyHardDenyOrLoopback, false)
		if d.Allowed {
			continue
		}
		// fake-IP 例外：面板自身处于 fake-IP 解析环境时，任意公网
		// 域名都会解析到 198.18/15 合成段（本机实测 www.bing.com
		// -> 198.18.x.x）。若据此拒存，全部公网域名目标将不可保
		// 存，属误伤；故该段仅在「解析结果」上豁免——用户直接填
		// 的字面量 198.18.x.x 仍按 ClassReserved 在上方拒存。
		if d.Result.Class == netguard.ClassReserved && fakeIPBenchmarkPrefix.Contains(d.Result.Effective) {
			continue
		}
		return errors.New("测试目标地址不允许")
	}
	return nil
}

func normalizeTunnelProbeTargetHost(host string) (string, bool) {
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		if !strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]") {
			return "", false
		}
		inner := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
		addr, err := netip.ParseAddr(inner)
		if err != nil || !addr.Is6() {
			return "", false
		}
		return inner, true
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.String(), true
	}
	if strings.Contains(host, ":") || isTunnelProbeTargetIPv4Like(host) {
		return "", false
	}
	if !isValidTunnelProbeTargetHost(host) {
		return "", false
	}
	return host, true
}

func isValidTunnelProbeTargetHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !isASCIILetter(r) && !isASCIIDigit(r) && r != '-' {
				return false
			}
		}
	}
	return true
}

func isTunnelProbeTargetIPv4Like(host string) bool {
	if host == "" {
		return false
	}
	for _, r := range host {
		if !isASCIIDigit(r) && r != '.' {
			return false
		}
	}
	return strings.Contains(host, ".")
}

func isTunnelProbeTargetSchemeLikeHost(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}

	colon := strings.IndexByte(host, ':')
	if colon <= 0 {
		return false
	}
	for i, r := range host[:colon] {
		if i == 0 {
			if !isASCIILetter(r) {
				return false
			}
			continue
		}
		if !isASCIILetter(r) && !isASCIIDigit(r) && r != '+' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isASCIIDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

func parseTunnelProbeTargetFromRequest(req map[string]interface{}) (tunnelProbeTarget, bool, error) {
	if req == nil {
		return defaultTunnelProbeTarget(), false, nil
	}
	rawHost, hasHost := req["probeTargetHost"]
	rawPort, hasPort := req["probeTargetPort"]
	if !hasHost && !hasPort {
		return defaultTunnelProbeTarget(), false, nil
	}
	host, err := parseTunnelProbeTargetHostValue(rawHost)
	if err != nil {
		return tunnelProbeTarget{}, false, err
	}
	port, err := parseTunnelProbeTargetPortValue(rawPort)
	if err != nil {
		return tunnelProbeTarget{}, false, err
	}
	return normalizeTunnelProbeTarget(host, port)
}

func parseTunnelProbeTargetHostValue(raw interface{}) (string, error) {
	if raw == nil {
		return "", nil
	}
	host, ok := raw.(string)
	if !ok {
		return "", errors.New("测试目标 Host 格式无效")
	}
	if host != strings.TrimSpace(host) {
		return "", errors.New("测试目标 Host 不能包含协议或路径")
	}
	return host, nil
}

func parseTunnelProbeTargetPortValue(raw interface{}) (int, error) {
	if raw == nil {
		return 0, nil
	}
	switch v := raw.(type) {
	case float64:
		if v != float64(int64(v)) {
			return 0, errors.New("测试目标端口必须是整数")
		}
		return int(v), nil
	case string:
		if v == "" {
			return 0, nil
		}
		if v != strings.TrimSpace(v) {
			return 0, errors.New("测试目标端口必须是整数")
		}
		port, err := strconv.Atoi(v)
		if err != nil {
			return 0, errors.New("测试目标端口必须是整数")
		}
		return port, nil
	case int:
		return v, nil
	case int32:
		return int(v), nil
	case int64:
		return int(v), nil
	default:
		return 0, errors.New("测试目标端口必须是整数")
	}
}

func effectiveTunnelProbeTarget(tunnel *model.Tunnel) tunnelProbeTarget {
	if tunnel == nil {
		return defaultTunnelProbeTarget()
	}
	return effectiveTunnelProbeTargetValues(tunnel.ProbeTargetHost, tunnel.ProbeTargetPort)
}

func effectiveTunnelProbeTargetValues(host string, port int) tunnelProbeTarget {
	target, configured, err := normalizeTunnelProbeTarget(host, port)
	if err != nil || !configured {
		return defaultTunnelProbeTarget()
	}
	return target
}

// checkedTunnelProbeTargetValues 是下发面的存量目标复检（R3X-011）：
// 已保存的目标在下发给节点执行前再过一遍 normalize 政策。未配置
// （空）沿用默认目标；已配置但被政策拒绝的存量坏目标返回 ok=false，
// 调用方必须跳过该目标的下发并已在此打告警日志——不得回退默认
// 目标冒充探测成功，也不得把坏目标原样下发。
func checkedTunnelProbeTargetValues(host string, port int) (tunnelProbeTarget, bool) {
	target, configured, err := normalizeTunnelProbeTarget(host, port)
	if err != nil {
		log.Printf("tunnel probe target skipped: stored target %q port %d rejected: %v", host, port, err)
		return tunnelProbeTarget{}, false
	}
	if !configured {
		return defaultTunnelProbeTarget(), true
	}
	return target, true
}

func formatTunnelProbeTarget(target tunnelProbeTarget) string {
	if addr, err := netip.ParseAddr(target.Host); err == nil && addr.Is6() {
		return fmt.Sprintf("[%s]:%d", target.Host, target.Port)
	}
	return fmt.Sprintf("%s:%d", target.Host, target.Port)
}
