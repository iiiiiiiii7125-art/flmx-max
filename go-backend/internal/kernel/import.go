package kernel

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"go-backend/internal/netguard"
)

// ParseImportHostPort validates the address of an imported node: a public
// IP or a hostname, plus a port.
func ParseImportHostPort(hostport string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(hostport))
	if err != nil {
		return "", 0, errors.New("地址格式应为 主机:端口")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("端口不正确")
	}
	if err := ValidateImportHost(host); err != nil {
		return "", 0, err
	}
	return host, port, nil
}

// ValidateImportHost accepts a routable IP or a hostname.
func ValidateImportHost(host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() || ip.IsLoopback() {
			return errors.New("地址不能是本机或 0.0.0.0")
		}
		// R2X-043：字面 IP 分支只加 netguard 硬拒层（元数据精确集 ∪
		// 169.254/16 整段 ∪ 未指定，翻译内嵌同判）+ loopback 层
		// （含 NAT64/6to4 内嵌回环）。RFC1918/CGNAT 字面量是合法
		// 导入对象，不得顺手禁（防过修）。
		if d := netguard.Judge(ip, netguard.PolicyHardDenyOrLoopback, false); !d.Allowed {
			return errors.New("地址不能是云元数据或链路本地地址")
		}
		return nil
	}
	// R1 X-P2-1（C-01，BC-04）：主机名分支前的规范化拦截。此前只拦
	// 字面 IP，localhost 与非规范 IP 编码（整数 2130706433、简写
	// 127.1、八进制 0177.0.0.1、十六进制 0x7f.1）全部经主机名分支
	// 放行，拨号时被解析为回环地址。抓取侧 SecureFetch 对同批形态
	// 是 fail-closed 的，导入侧在此对齐。三个共用入口（导入、订阅
	// 源同步、导入节点编辑）都经本函数，一处收口。
	if err := rejectNonCanonicalImportHost(host); err != nil {
		return err
	}
	if host == "" || len(host) > 253 || !hostPattern.MatchString(host) || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return fmt.Errorf("地址 %q 不正确", host)
	}
	return nil
}

// rejectNonCanonicalImportHost 拒绝 localhost 与非规范 IP 编码。
// 本函数只在 net.ParseIP 已失败（非规范 IP 字面量）后调用。
func rejectNonCanonicalImportHost(host string) error {
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return errors.New("地址不能是 localhost")
	}
	// R2X-005：补回环别名名单（仅名单早期拒绝，不做导入时解析校验——
	// 解析防线在 probe 拨号前，见 handler/kernel_probe.go）。
	switch lower {
	case "ip6-localhost", "ip6-loopback", "localhost.localdomain":
		return errors.New("地址不能是 localhost")
	}
	// 任一段以 0x 开头即十六进制编码（如 0x7f.1、0x7f000001）。
	for _, seg := range strings.Split(lower, ".") {
		if strings.HasPrefix(seg, "0x") {
			return fmt.Errorf("地址 %q 不正确", host)
		}
	}
	// 主机名只由数字与点组成时，它声称的是一个 IP 字面量：既然
	// ParseIP 已拒绝，它只能是整数/简写/八进制等非规范编码
	// （2130706433、127.1、0177.0.0.1），一律拒绝。规范 IP 在
	// 上方 IP 分支已放行，不会到达这里。
	if host != "" && isDigitsAndDots(host) {
		return fmt.Errorf("地址 %q 不正确", host)
	}
	return nil
}

func isDigitsAndDots(host string) bool {
	for _, r := range host {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

func decodeBase64Loose(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
