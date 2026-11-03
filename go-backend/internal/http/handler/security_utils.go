package handler

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"go-backend/internal/http/client"
	"go-backend/internal/kernel"
	"go-backend/internal/netguard"
)

// DisableSafeRemoteAddrCheckForTesting allows bypassing the safety check during integration tests.
var DisableSafeRemoteAddrCheckForTesting = false

// D3-01: 联邦出站客户端的拨号期 SSRF 复检与本包的入口校验共用同一
// 测试旁路开关（契约测试以 127.0.0.1 的 httptest 充当对端面板）。
func init() {
	client.SetSafetyBypassFunc(func() bool { return DisableSafeRemoteAddrCheckForTesting })
}

// IsSafeRemoteAddr checks if a given address is safe to connect to (prevents SSRF/Open Proxy).
// It resolves domains to IPs to prevent DNS rebinding attacks pointing to internal networks.
// Supports multiple addresses separated by commas or newlines (one per line).
func IsSafeRemoteAddr(addr string) error {
	// R7-04（G-F3-01）：切分后段集为空集（纯分隔符/纯空白）时显式
	// 拒绝——逐段校验对零个目标真空成立，零目标转发曾可绕过全部
	// 段校验落库。此为格式无效判、与 SSRF 段校验无关，故置于测试
	// 旁路之前，对全部调用方恒生效。
	if err := validateRemoteAddrHasTargets(addr); err != nil {
		return err
	}
	if DisableSafeRemoteAddrCheckForTesting {
		return nil
	}

	for _, part := range splitRemoteParts(addr) {
		if err := checkSingleRemoteAddr(part); err != nil {
			return err
		}
	}
	return nil
}

// validateRemoteAddrHasTargets 是 R7-04 空段集谓词的单一定义点：
// IsSafeRemoteAddr 本体与 forwardCreate/forwardUpdate 的无条件格式
// 判共用同一口径。管理员豁免与 allow_local_remote_addr 开关只针对
// netguard 段校验；零有效目标属格式无效、不在豁免范围内。
func validateRemoteAddrHasTargets(addr string) error {
	if len(splitRemoteParts(addr)) == 0 {
		return fmt.Errorf("目标地址不能为空")
	}
	return nil
}

// remoteAddrTargetsChanged 是 R7-05（G-F3-02）的目标变更判定：以
// splitRemoteParts 的段集（多重集）为准比较提交值与存量值——顺序、
// 空段与首尾空白差不构成变更，段集相同即目标未实际变更。段集为空
// 的提交由 R7-04 的格式判在更前处拒绝（空段集与任一非空段集恒不
// 相等），本谓词不作零目标兜底。
//
// 多重集而非集合口径是 R8-18（H-F3-03）的实测定谳，不得改集合义：
// 重复段在消费侧非惰性——gost 渲染逐段成节点，round/rand/hash 选点
// 按节点条目计权（重复地址得双倍份额），realm 渲染把重复段排成等
// 权 roundrobin 条目，诊断亦逐条目探测；去重/增重即改变负载语义、
// 构成实际变更，须触发复检。仅中转延迟取首段一面对重数惰性，不
// 足以推翻其余三面。段序差不算变更的依据不同：复检对象是地址本
// 身，重排不引入新地址、无漏检面（主备次序属下发语义、落库原文
// 即新序）。
func remoteAddrTargetsChanged(submitted, stored string) bool {
	a := splitRemoteParts(submitted)
	b := splitRemoteParts(stored)
	if len(a) != len(b) {
		return true
	}
	counts := make(map[string]int, len(a))
	for _, p := range a {
		counts[p]++
	}
	for _, p := range b {
		counts[p]--
		if counts[p] < 0 {
			return true
		}
	}
	return false
}

// splitRemoteParts splits a multi-address string by commas and newlines.
func splitRemoteParts(addr string) []string {
	addr = strings.ReplaceAll(addr, "\n", ",")
	addr = strings.ReplaceAll(addr, "\r", ",")
	parts := strings.Split(addr, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// checkSingleRemoteAddr validates a single address.
func checkSingleRemoteAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.Contains(err.Error(), "missing port in address") {
			host = addr
		} else {
			return fmt.Errorf("invalid address format: %v", err)
		}
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("could not resolve address %q: %v", addr, err)
	}

	for _, ip := range ips {
		// R2X-001：改走 netguard 普通转发策略——非公网全拒、硬拒
		// 先行（旧实现仅拦 loopback+RFC1918，链路本地/元数据/CGNAT/
		// 240-4/组播/NAT64·6to4 内嵌形态全部漏过，入口节点 IMDS
		// 可被普通用户直读）。管理员与 allow_local_remote_addr
		// 例外在调用方，本函数口径不变。
		if d := netguard.Judge(ip, netguard.PolicyPublicOnly, false); !d.Allowed {
			return fmt.Errorf("address %q resolves to internal IP: %s", addr, ip.String())
		}
	}

	return nil
}

// checkFederationRemoteAddr 是联邦远端节点 URL 的写面校验（B13-001）：
// 与联邦出站传输层（client/federation_transport.go 的 validateFederationIPs）
// 同一口径——逐个解析地址走 netguard PolicyAllowPrivate：硬拒层
// （元数据精确集 ∪ 169.254/16 ∪ 未指定等）恒拒、先于放行；其余非公网
// 地址仅在与 SecureFetch 共用的私网放行策略（isPrivateAllowed：
// FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true 或 FLMX_FETCH_PRIVATE_ALLOWLIST
// 命中判定口径地址）开启时放行。此前写面走 IsSafeRemoteAddr 的
// PolicyPublicOnly，运维按传输层注释开启内网放行后 import/create 仍被
// 无条件 403，内网/Tailscale 联邦只能经备份恢复旁路写入。本校验只决定
// 写面是否接受该 URL；实际拨号仍由传输层逐跳钉死复检，不因写面放行而
// 放宽拨号面。
func checkFederationRemoteAddr(addr string) error {
	if DisableSafeRemoteAddrCheckForTesting {
		return nil
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.Contains(err.Error(), "missing port in address") {
			host = addr
		} else {
			return fmt.Errorf("invalid address format: %v", err)
		}
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("could not resolve address %q: %v", addr, err)
	}

	for _, ip := range ips {
		// 与 validateFederationRoleTargets 同一判定式：硬拒层由
		// netguard.Judge 前置处理，放行按判定口径（翻译内嵌）地址
		// 查 allowlist，与传输层 federationPrivateAllowed(EffectiveIP)
		// 等价。
		eff := netguard.EffectiveIP(ip)
		if d := netguard.Judge(ip, netguard.PolicyAllowPrivate, eff != nil && isPrivateAllowed(eff)); !d.Allowed {
			return fmt.Errorf("address %q resolves to internal IP: %s", addr, ip.String())
		}
	}

	return nil
}

// validateNoEmptyLabelHost 是空标签主机名的语法层校验（终审 L1-F-06/07/08）：
// 只判语法、不做 DNS 解析。空串不是空标签，由调用方另行处理空值；IP
// 字面量无空标签，可直接传入。
func validateNoEmptyLabelHost(host string) error {
	if kernel.HasEmptyLabel(host) {
		return fmt.Errorf("主机名 %q 包含空标签", host)
	}
	return nil
}

// validateNoEmptyLabelAddr 对 host:port 形态（多目标以逗号/换行分隔）逐个取
// host 做空标签校验；无端口时整段即 host。格式错误由既有校验处理，此处
// 只补空标签判，不新增格式拒绝。
func validateNoEmptyLabelAddr(addr string) error {
	for _, part := range splitRemoteParts(addr) {
		host := part
		if h, _, err := net.SplitHostPort(part); err == nil {
			host = h
		}
		if err := validateNoEmptyLabelHost(host); err != nil {
			return err
		}
	}
	return nil
}

// validateNoEmptyLabelURLHost 解析 URL 取 hostname 做空标签校验；解析
// 失败时不报错，由既有校验处理。
func validateNoEmptyLabelURLHost(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil
	}
	return validateNoEmptyLabelHost(u.Hostname())
}

// IsValidNodeAddress ensures the address is strictly a host or host:port.
// It explicitly denies schemes (http://, https://), paths (/...), and query params (?).
func IsValidNodeAddress(addr string) error {
	addr = strings.TrimSpace(addr)
	if strings.Contains(addr, "://") {
		return fmt.Errorf("address must not contain scheme (e.g. http://)")
	}
	if strings.ContainsAny(addr, "/?") {
		return fmt.Errorf("address must not contain path or query parameters")
	}
	// R3-E-01：结构字符集补齐（@、#、逗号、空白与控制字符）。此前
	// good.com@evil.com、内嵌换行等值全部放行落库，而节点地址是
	// 订阅 auto 模式与中转入口的地址源——拉取期裸拼进订阅权威段
	// 即改写全部订阅者的目标主机。仅加结构字符集：主机名 /
	// IPv4 / IPv6 字面量（含 host:port 与括号 IPv6）及 RFC1918 /
	// 回环的既有可存性一概不变（回环在订阅面的拒绝是拉取期的
	// 口径，不在此处收紧——本地测试节点仍可存）。
	if kernel.HostHasStructuralChars(addr) {
		return fmt.Errorf("address must not contain @, #, commas, whitespace or control characters")
	}
	// R3 B04-001：主机名字符集门（LDH 口径）。节点/面板地址会被无
	// 引号拼入以 root 执行的安装命令（-a %s）与订阅权威段，结构字符
	// 集（上方）只拒 @ # , / ? 与空白控制字符，; $ ` | & ' " ( ) 等
	// shell 元字符仍可穿透。此处把地址整体收紧为地址字符集：字母、
	// 数字、连字符、点、冒号（端口/IPv6）、方括号（括号 IPv6）与 %
	// （IPv6 zone id，fe80::1%eth0 既有可存性保留）。本谓词是全部
	// 地址写面与 validatePanelInstallAddr（安装命令生成面）的共同
	// 祖先，一处落门全集覆盖。
	if !isAddressCharset(addr) {
		return fmt.Errorf("address must contain only letters, digits, hyphens, dots, colons, brackets or %% (IPv6 zone id)")
	}
	// A bare IPv6 literal contains multiple colons, so net.SplitHostPort treats
	// it as a malformed host:port pair. Accept IP literals before attempting
	// host:port parsing; netip also handles scoped IPv6 addresses.
	if _, err := netip.ParseAddr(addr); err == nil {
		return nil
	}

	// L1-F-04: 空标签主机名（a..b）在任何 DNS 下都不可解析，写面即拒。
	// IP 字面量已在上方返回；此处 host 为主机名（host:port 取 host 段，
	// 裸主机名即自身；括号 IPv6 的 host 段不含点，不会误判）。
	// RFC1918/回环的既有可存性一概不变——只加空标签拒绝，不加任何
	// IP 政策（回环在订阅面的拒绝是拉取期的口径，不在此处收紧）。
	host := addr
	port := ""
	hasPort := false
	if h, p, err := net.SplitHostPort(addr); err == nil {
		host, port, hasPort = h, p, true
	} else if !strings.Contains(err.Error(), "missing port in address") {
		return fmt.Errorf("invalid address format")
	}
	// B04-005（数值合法性）：旧实现只验结构不验数值——host:port 的
	// port 从不校验（:99999 原样烤进 root 安装命令），全数字点分
	// host 在 IP 解析失败后只查空标签即放行（999.999.1.1 落库成永
	// 不可达节点）。补两条数值校验，结构口径与既有可存性不变：
	// 域名形态、回环/RFC1918、括号 IPv6 的处理一概不动。
	if hasPort {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("address port must be a number between 1 and 65535")
		}
	}
	if kernel.HasEmptyLabel(host) {
		return fmt.Errorf("address contains an empty DNS label (e.g. \"a..b\")")
	}
	if isAllNumericDottedHost(host) {
		if _, err := netip.ParseAddr(host); err != nil {
			return fmt.Errorf("invalid IPv4 address")
		}
	}
	return nil
}

// isAddressCharset 判断字符串是否全由地址字符组成（R3 B04-001）：
// 字母、数字与 - . : [ ] %。主机名（LDH）、IPv4、host:port、裸/括号
// IPv6 字面量与 zone id 形态都在集内；shell 元字符与非 ASCII 一律
// 在集外。供 IsValidNodeAddress（地址整体）与代理 URL 的 Host 段共用。
func isAddressCharset(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '.' || c == ':' || c == '[' || c == ']' || c == '%':
		default:
			return false
		}
	}
	return true
}

// 1.7.7（KU5）：validateGithubProxyURL 与 shellMetaChars 已随
// 「开启 GitHub 加速」设置整体移除——该谓词的仅有消费者（写面校验
// 与 getGithubProxyConfig 消费兜底）均已删除，安装命令不再拼入任
// 何面板配置的代理值（候选清单为内置常量、逐项加引号）。

// isAllNumericDottedHost 判断 host 是否为「全数字标签的点分形态」
// （如 999.999.1.1、1.2.3.4.5）：这类值只能是 IPv4 字面量的笔误
// 形态——合法主机名的末级标签不可能全数字。含字母的域名与单段
// 数字标签不在此列，沿既有结构口径处理。
func isAllNumericDottedHost(host string) bool {
	if !strings.Contains(host, ".") {
		return false
	}
	for _, r := range host {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}
