// Package netguard 是 FLMX 全部 IP 判定面的唯一收口（R2 批1，
// verdicts §2.1）：规范化 → 翻译地址提取 → 硬拒层 → 分类 → 按调用方
// 策略判定。此前判定集在后端物理复制 5 套、agent 侧 1 套，逐面补丁
// 已被两轮审计证伪，本包为后端正本（仅 stdlib，无内部依赖）。
//
// 同步义务（镜像）：agent 侧 go-gost/x/bypass/flmx_no_private.go 是
// 本分类器的镜像实现（独立 module 无法 import 本包）。修改本包的
// 段集、硬拒集或翻译规则时，必须同批同步该文件，反之亦然；两侧
// 文件头互相注明。
package netguard

import (
	"net"
	"net/netip"
)

// Class 是地址的公网性分类（对翻译地址按内嵌 IPv4 分类）。
type Class int

const (
	ClassPublic Class = iota
	ClassPrivate
	ClassLoopback
	ClassLinkLocal
	ClassCGNAT
	ClassMulticast
	ClassUnspecified
	ClassReserved
)

func (c Class) String() string {
	switch c {
	case ClassPublic:
		return "public"
	case ClassPrivate:
		return "private"
	case ClassLoopback:
		return "loopback"
	case ClassLinkLocal:
		return "link_local"
	case ClassCGNAT:
		return "cgnat"
	case ClassMulticast:
		return "multicast"
	case ClassUnspecified:
		return "unspecified"
	case ClassReserved:
		return "reserved"
	}
	return "unknown"
}

// Result 是一次分类的完整结论。
type Result struct {
	Input      netip.Addr // 原始输入
	Normalized netip.Addr // Unmap 后的输入（翻译前）
	Effective  netip.Addr // 判定口径地址：翻译地址为其内嵌 IPv4，其余同 Normalized
	Translated bool       // 是否经 NAT64 /96 或 6to4 提取
	Translation string    // "nat64" / "6to4" / ""（本地 NAT64 不提取，见 LocalNAT64）
	LocalNAT64 bool       // 命中 64:ff9b:1::/48 本地 NAT64 前缀（整段 NonPublic）
	HardDeny   bool       // 硬拒层命中（永在任何放行判定之前）
	HardReason string     // "metadata" / "link_local_segment" / "unspecified" / "nat64_local" / "invalid"
	Class      Class      // Effective 的分类（LocalNAT64 时为 ClassReserved）
}

// IsPublic 报告该地址是否为（内嵌）公网地址。
func (r Result) IsPublic() bool { return r.Class == ClassPublic && !r.HardDeny }

// NonPublic 报告该地址是否非公网（硬拒地址亦属非公网）。
func (r Result) NonPublic() bool { return r.Class != ClassPublic || r.HardDeny }

var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local     = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour      = netip.MustParsePrefix("2002::/16")
	linkLocalV4    = netip.MustParsePrefix("169.254.0.0/16")
	linkLocalV6    = netip.MustParsePrefix("fe80::/10")
)

// metadataExact 是云元数据精确集（硬拒）。链路本地整段另行硬拒
// （169.254.0.0/16 与 fe80::/10，verdicts §3.5；N-01 起 v6 段
// 同入——无 zone 的 fe80 地址在任何消费方都不可路由）；CGNAT 段
// 不整段硬拒（Tailscale 等合法用途），100.100.100.200 只作精确条目。
var metadataExact = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"),
	netip.MustParseAddr("100.100.100.200"),
	netip.MustParseAddr("fd00:ec2::254"),
}

// nonPublicPrefixes 是分类段集基底，取 agent bypass 集合（含
// 240/4、192.0.0/24、198.18/15、100::/64 等），不取旧 isPrivateIP
// 集合（其不含 240/4，255.255.255.255 会被放行）。
var nonPublicPrefixes = []struct {
	prefix netip.Prefix
	class  Class
}{
	{netip.MustParsePrefix("0.0.0.0/8"), ClassReserved},
	{netip.MustParsePrefix("10.0.0.0/8"), ClassPrivate},
	{netip.MustParsePrefix("172.16.0.0/12"), ClassPrivate},
	{netip.MustParsePrefix("192.168.0.0/16"), ClassPrivate},
	{netip.MustParsePrefix("169.254.0.0/16"), ClassLinkLocal},
	{netip.MustParsePrefix("100.64.0.0/10"), ClassCGNAT},
	{netip.MustParsePrefix("192.0.0.0/24"), ClassReserved},
	{netip.MustParsePrefix("198.18.0.0/15"), ClassReserved},
	{netip.MustParsePrefix("240.0.0.0/4"), ClassReserved},
	{netip.MustParsePrefix("fc00::/7"), ClassPrivate},
	{netip.MustParsePrefix("fe80::/10"), ClassLinkLocal},
	{netip.MustParsePrefix("100::/64"), ClassReserved},
}

func classifyPlain(a netip.Addr) Class {
	switch {
	case a.IsUnspecified():
		return ClassUnspecified
	case a.IsLoopback():
		return ClassLoopback
	case a.IsMulticast(), a.IsLinkLocalMulticast():
		return ClassMulticast
	case a.IsLinkLocalUnicast():
		return ClassLinkLocal
	}
	for _, e := range nonPublicPrefixes {
		if e.prefix.Contains(a) {
			return e.class
		}
	}
	if a.IsPrivate() {
		return ClassPrivate
	}
	if !a.IsGlobalUnicast() {
		return ClassReserved
	}
	return ClassPublic
}

func isMetadataExact(a netip.Addr) bool {
	for _, m := range metadataExact {
		if a == m {
			return true
		}
	}
	return false
}

// ClassifyAddr 执行完整判定链：Unmap 规范化 → 翻译提取 → 硬拒 → 分类。
func ClassifyAddr(a netip.Addr) Result {
	r := Result{Input: a}
	if !a.IsValid() {
		r.HardDeny = true
		r.HardReason = "invalid"
		r.Class = ClassUnspecified
		return r
	}
	norm := a.Unmap().WithZone("")
	r.Normalized = norm
	r.Effective = norm

	// 翻译地址提取。本地 NAT64 前缀的内嵌位布局含 u 字节穿插，
	// 照 /96 式提取会提错，故整段 NonPublic 直接拒、不提取。
	if nat64Local.Contains(norm) {
		r.LocalNAT64 = true
		r.Class = ClassReserved
		r.HardDeny = true
		r.HardReason = "nat64_local"
		return r
	}
	if nat64WellKnown.Contains(norm) {
		b := norm.As16()
		r.Effective = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		r.Translated = true
		r.Translation = "nat64"
	} else if sixToFour.Contains(norm) {
		b := norm.As16()
		r.Effective = netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})
		r.Translated = true
		r.Translation = "6to4"
	}
	// Teredo 2001::/32 内嵌经 XOR 混淆无法可靠提取，本轮不处理
	// （残余观察，见 verdicts §2.1）。

	r.Class = classifyPlain(r.Effective)

	// 硬拒层（对判定口径地址判定，翻译内嵌同判）。
	switch {
	case isMetadataExact(r.Effective):
		r.HardDeny, r.HardReason = true, "metadata"
	case linkLocalV4.Contains(r.Effective), linkLocalV6.Contains(r.Effective):
		r.HardDeny, r.HardReason = true, "link_local_segment"
	case r.Effective.IsUnspecified():
		r.HardDeny, r.HardReason = true, "unspecified"
	}
	return r
}

// Policy 是调用方策略（硬拒层之后）。
type Policy int

const (
	// PolicyPublicOnly：全部非公网拒（普通转发、联邦诊断）。
	PolicyPublicOnly Policy = iota
	// PolicyAllowPrivate：非公网仅在 allowPrivate 为真时放行
	// （SecureFetch、联邦传输/角色目标、监控 allow_private 通道）。
	PolicyAllowPrivate
	// PolicyHardDenyOnly：仅硬拒层（notify——内网 webhook 合法）。
	PolicyHardDenyOnly
	// PolicyHardDenyOrLoopback：硬拒层 + loopback 层（导入主机、probe）。
	PolicyHardDenyOrLoopback
)

// Decision 是一次策略判定的结果。
type Decision struct {
	Allowed bool
	Reason  string // "public" / "private_allowed" / "hard_deny" / "non_public" / "loopback"
	Result  Result
}

// JudgeAddr 按策略判定一个地址。
func JudgeAddr(a netip.Addr, policy Policy, allowPrivate bool) Decision {
	r := ClassifyAddr(a)
	d := Decision{Result: r}
	if r.HardDeny {
		d.Reason = "hard_deny"
		return d
	}
	switch policy {
	case PolicyPublicOnly:
		if r.Class == ClassPublic {
			d.Allowed, d.Reason = true, "public"
		} else {
			d.Reason = "non_public"
		}
	case PolicyAllowPrivate:
		if r.Class == ClassPublic {
			d.Allowed, d.Reason = true, "public"
		} else if allowPrivate {
			d.Allowed, d.Reason = true, "private_allowed"
		} else {
			d.Reason = "non_public"
		}
	case PolicyHardDenyOnly:
		d.Allowed, d.Reason = true, "not_hard_denied"
	case PolicyHardDenyOrLoopback:
		if r.Class == ClassLoopback {
			d.Reason = "loopback"
		} else {
			d.Allowed, d.Reason = true, "not_hard_denied"
		}
	}
	return d
}

// ---- net.IP 便捷封装（存量调用方以 net.IP 为主） ----

func addrFromIP(ip net.IP) (netip.Addr, bool) {
	if ip == nil {
		return netip.Addr{}, false
	}
	a, ok := netipxFromIP(ip)
	return a, ok
}

func netipxFromIP(ip net.IP) (netip.Addr, bool) {
	a, err := netip.ParseAddr(ip.String())
	if err != nil {
		return netip.Addr{}, false
	}
	return a, true
}

// Classify 是 ClassifyAddr 的 net.IP 版；nil/非法地址按硬拒处理。
func Classify(ip net.IP) Result {
	a, ok := addrFromIP(ip)
	if !ok {
		return Result{HardDeny: true, HardReason: "invalid", Class: ClassUnspecified}
	}
	return ClassifyAddr(a)
}

// Judge 是 JudgeAddr 的 net.IP 版。
func Judge(ip net.IP, policy Policy, allowPrivate bool) Decision {
	a, ok := addrFromIP(ip)
	if !ok {
		return Decision{Reason: "hard_deny", Result: Result{HardDeny: true, HardReason: "invalid", Class: ClassUnspecified}}
	}
	return JudgeAddr(a, policy, allowPrivate)
}

// HardDenyIP 报告 ip 是否命中硬拒层。
func HardDenyIP(ip net.IP) bool { return Classify(ip).HardDeny }

// IsNonPublicIP 报告 ip 是否非公网（按判定口径，翻译内嵌同判）。
func IsNonPublicIP(ip net.IP) bool { return Classify(ip).NonPublic() }

// EffectiveIP 返回判定口径地址的 net.IP 形态（翻译地址为内嵌 IPv4，
// mapped 已展开；其余原样）。判定无效时返回 nil。
func EffectiveIP(ip net.IP) net.IP {
	r := Classify(ip)
	if !r.Effective.IsValid() {
		return nil
	}
	return net.IP(r.Effective.AsSlice())
}
