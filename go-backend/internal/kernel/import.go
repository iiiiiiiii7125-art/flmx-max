package kernel

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"

	"go-backend/internal/netguard"
)

// CanonicalShareHostValue is the canonical form a share host is
// validated and rendered with (B2-01): whitespace-trimmed, brackets
// stripped, and a single trailing dot (the FQDN root dot) removed.
// The dot strip runs on both sides of the bracket strip so a root dot
// in either placement canonicalizes — after the brackets
// ([2001:db8::9].) or inside them ([2001:db8::9.]); the pre-R3 order
// (trim brackets, then strip the dot) left a residual bracket on the
// first form, so the value validated differed from the value shipped
// (R3-A-01). A second consecutive dot is never consumed:
// "example.com.." keeps a trailing dot in canonical form and stays
// rejected by ValidateImportHost, exactly as before.
func CanonicalShareHostValue(host string) string {
	v := strings.TrimSpace(host)
	bracketed := strings.HasPrefix(v, "[")
	v = stripOneRootDot(v)
	v = strings.Trim(v, "[]")
	if bracketed {
		v = stripOneRootDot(v)
	}
	return v
}

// stripOneRootDot removes a single trailing dot, but never the second
// dot of a ".." pair (that form is malformed, not an FQDN root dot).
func stripOneRootDot(v string) string {
	if strings.HasSuffix(v, ".") && !strings.HasSuffix(v, "..") {
		return v[:len(v)-1]
	}
	return v
}

// HostHasStructuralChars reports whether s carries characters that can
// break out of a host slot: URI authority or line-structure characters
// (@ # , / ?), whitespace, or control characters. A value carrying
// them in a node address rewrites the authority segment of every
// subscription rendered from it (R3-E-01); the save surface
// (IsValidNodeAddress) and the import/backup sanitize surfaces share
// this one predicate. It deliberately says nothing about which hosts
// are legitimate — hostnames, IP literals, RFC1918 and loopback all
// pass; class judgements live in ValidateImportHost / netguard.
func HostHasStructuralChars(s string) bool {
	for _, r := range s {
		switch {
		case r == '@' || r == '#' || r == ',' || r == '/' || r == '?':
			return true
		case unicode.IsSpace(r) || unicode.IsControl(r):
			return true
		}
	}
	return false
}

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
		// R3X-010：该政策已在 netguard 一处补拒 Reserved/Multicast
		// 与 fe80::/10，本消费方自动同效，无需在此另加判定。
		// N-01（1.4.0 调和）：fe80 的拒绝即由此严格政策的
		// ClassLinkLocal 补拒达成，不入 netguard 硬拒层。
		if d := netguard.Judge(ip, netguard.PolicyHardDenyOrLoopback, false); !d.Allowed {
			// R1-F-01：翻译内嵌特殊地址（NAT64/6to4 内嵌 127/8 回环、
			// 内嵌 0.0.0.0 未指定）时 netguard 只给 loopback 判定或
			// unspecified 硬拒；前端同口径报“本机”文案，后端原先一律
			// 报云元数据桶文案。按更准确的一方对齐：这两类报本机文案
			// （裁决本身仍是拒绝，不变）。直接回环/未指定形态已在上
			// 方先行返回，走到这里的两类只能是翻译内嵌。
			if d.Reason == "loopback" || d.Result.HardReason == "unspecified" {
				return errors.New("地址不能是本机或 0.0.0.0")
			}
			return errors.New("地址不能是云元数据、链路本地或保留/组播地址")
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
	if host == "" || len(host) > 253 || !hostPattern.MatchString(host) || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || hasEmptyLabel(host) {
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
