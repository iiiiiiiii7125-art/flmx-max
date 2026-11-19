package nftables

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"
)

// 改名过渡：表名由 flvx 改为 flmx，旧 plain 名只在升级迁移里删
// （见 buildApplyCommand 的首建门控）。当前表名按面板实例隔离
// （P1-1）：同机多面板、或面板与同源项目共管同一节点时，plain
// 名 flmx 会让后下发的一方删建前一方的整张转发/计数表。
const (
	// plainTableName 是隔离前的 plain 表名；新表不再用它，只在
	// 迁移时清理。
	plainTableName = "flmx"
	// legacyTableName 是改名前的表名；同样只在迁移时清理。
	legacyTableName = "flvx"
)

// TableNameForInstance derives this panel install's nft table name
// from its instance key (FLMX_INSTANCE; "" is the default
// instance): flmx_<first 8 hex of sha256(instance)>. Stable across
// restarts (same instance, same table), distinct per instance, and
// a valid nft identifier. The default instance hashes to the fixed
// name flmx_e3b0c442 (sha256("") = e3b0c442...).
func TableNameForInstance(instance string) string {
	sum := sha256.Sum256([]byte(instance))
	return "flmx_" + hex.EncodeToString(sum[:])[:8]
}

// TableName returns the nft table this plan renders into. A plan
// without an instance key (zero value, old callers) renders into
// the default instance's table, never the plain name.
func (p NodePlan) TableName() string {
	return TableNameForInstance(p.InstanceID)
}

func RenderTable(plan NodePlan) string {
	table := plan.TableName()
	var b strings.Builder
	b.WriteString("table inet " + table + " {\n")
	b.WriteString("  chain prerouting {\n")
	b.WriteString("    type nat hook prerouting priority dstnat; policy accept;\n")
	for _, rule := range sortedRules(plan.Rules) {
		family := nftAddressFamily(rule.TargetHost)
		dnatFamily := ""
		if family != "" {
			dnatFamily = family + " "
		}
		// A4-005（BC-03）：设了 BindIP 的转发只 DNAT 发往该地址的流量，
		// 不再在节点全部地址上生效（与 gost/realm 的 bind 口径对齐）。
		bindMatch := ""
		if fam, ip, ok := bindIPFamilyIP(rule.BindIP); ok {
			bindMatch = fmt.Sprintf("%s daddr %s ", fam, ip)
		}
		for _, protocol := range normalizedProtocols(rule.Protocols) {
			b.WriteString(fmt.Sprintf("    %s %sdport %d counter dnat %sto %s comment %q\n",
				protocol,
				bindMatch,
				rule.InPort,
				dnatFamily,
				formatDNATTarget(rule.TargetHost, rule.TargetPort),
				counterComment(table, rule.ForwardID, CounterDirectionDNAT, protocol),
			))
		}
	}
	b.WriteString("  }\n\n")
	b.WriteString("  chain postrouting {\n")
	b.WriteString("    type nat hook postrouting priority srcnat; policy accept;\n")
	if len(plan.Rules) > 0 {
		b.WriteString("    masquerade comment \"" + table + " masquerade\"\n")
	}
	b.WriteString("  }\n\n")
	b.WriteString("  chain forward {\n")
	b.WriteString("    type filter hook forward priority filter; policy accept;\n")
	for _, rule := range sortedRules(plan.Rules) {
		family := nftAddressFamily(rule.TargetHost)
		if family == "" {
			continue
		}
		targetHost := strings.Trim(strings.TrimSpace(rule.TargetHost), "[]")
		// A4-005：计数同样按原始目的地址（= BindIP）收紧，避免发往其他
		// 地址的同端口流量被算进本转发。
		bindMatch := ""
		if fam, ip, ok := bindIPFamilyIP(rule.BindIP); ok {
			bindMatch = fmt.Sprintf("ct original %s daddr %s ", fam, ip)
		}
		for _, protocol := range normalizedProtocols(rule.Protocols) {
			b.WriteString(fmt.Sprintf("    meta l4proto %s ct original proto-dst %d %s%s daddr %s %s dport %d counter comment %q\n",
				protocol,
				rule.InPort,
				bindMatch,
				family,
				targetHost,
				protocol,
				rule.TargetPort,
				counterComment(table, rule.ForwardID, CounterDirectionToTarget, protocol),
			))
			b.WriteString(fmt.Sprintf("    meta l4proto %s ct original proto-dst %d %s%s saddr %s %s sport %d counter comment %q\n",
				protocol,
				rule.InPort,
				bindMatch,
				family,
				targetHost,
				protocol,
				rule.TargetPort,
				counterComment(table, rule.ForwardID, CounterDirectionFromTarget, protocol),
			))
		}
	}
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

// ValidateTargetHost 是 TargetHost 落入 nft 规则文本前的渲染层校验
// （C3-01）：TargetHost 是 RenderTable 唯一原样插值的自由文本字段，
// 含空白/换行/nft 标点的值一旦进入脚本即是 nft 语法注入。生产构造点
// 今天有 IP 门把关，但校验必须落在渲染/应用单点上，任何构造路径
// （含未来与恢复面）都绕不过。接受集合与渲染归一口径一致：先按
// formatDNATTarget 同法 Trim 空白与方括号，归一后须为 IP 字面量或
// 严格主机名（标签 1–63 字符、字母数字与连字符、首尾为字母数字、
// 无空标签、总长 ≤253），其余一律拒绝。
func ValidateTargetHost(host string) error {
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	if trimmed == "" {
		return fmt.Errorf("nftables 目标地址为空")
	}
	if net.ParseIP(trimmed) != nil {
		return nil
	}
	if len(trimmed) > 253 {
		return fmt.Errorf("nftables 目标主机名过长: %q", host)
	}
	for _, label := range strings.Split(trimmed, ".") {
		if !isStrictHostnameLabel(label) {
			return fmt.Errorf("nftables 目标地址不是合法 IP 或主机名: %q", host)
		}
	}
	return nil
}

func isStrictHostnameLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if isAlnum {
			continue
		}
		if c == '-' && i > 0 && i < len(label)-1 {
			continue
		}
		return false
	}
	return true
}

// ValidatePlan 校验计划内每条规则的 TargetHost（C3-01），错误点名
// 出错的转发 ID。Manager.Reconcile 在渲染与下发之前调用，非法计划
// 整单拒绝、脚本不下发节点。
func ValidatePlan(plan NodePlan) error {
	for _, rule := range plan.Rules {
		if err := ValidateTargetHost(rule.TargetHost); err != nil {
			return fmt.Errorf("转发 %d: %w", rule.ForwardID, err)
		}
	}
	return nil
}

func counterComment(table string, forwardID int64, direction, protocol string) string {
	return fmt.Sprintf("%s forward:%d %s %s", table, forwardID, direction, protocol)
}

func RuleHash(rule Rule) string {
	protocols := normalizedProtocols(rule.Protocols)
	// A4-005：BindIP 已参与渲染，必须进 hash——否则改 inIp 不触发规则
	// 代际更新（旧实现有专测固化忽略行为，随渲染修复一并废止）。
	_, bindIP, _ := bindIPFamilyIP(rule.BindIP)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%d|%s|%d|%s|%s",
		rule.ForwardID,
		rule.InPort,
		strings.TrimSpace(rule.TargetHost),
		rule.TargetPort,
		strings.Join(protocols, ","),
		bindIP,
	)))
	return hex.EncodeToString(sum[:])
}

// bindIPFamilyIP extracts the matchable address from a forward's BindIP
// (plain IP, bracketed IPv6, or host:port as tolerated by the gost path).
// ok=false when BindIP is empty or not an IP literal (no restriction then,
// preserving the historical wildcard behavior for unparseable values).
func bindIPFamilyIP(bindIP string) (family, ip string, ok bool) {
	trimmed := strings.TrimSpace(bindIP)
	if trimmed == "" {
		return "", "", false
	}
	host := strings.Trim(trimmed, "[]")
	if h, _, err := net.SplitHostPort(trimmed); err == nil {
		host = h
	}
	parsed := net.ParseIP(host)
	if parsed == nil {
		return "", "", false
	}
	if parsed.To4() == nil {
		return "ip6", host, true
	}
	return "ip", host, true
}

func PlanHashes(plan NodePlan) map[int64]string {
	hashes := make(map[int64]string, len(plan.Rules))
	for _, rule := range plan.Rules {
		hashes[rule.ForwardID] = RuleHash(rule)
	}
	return hashes
}

func sortedRules(rules []Rule) []Rule {
	out := append([]Rule(nil), rules...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].InPort == out[j].InPort {
			return out[i].ForwardID < out[j].ForwardID
		}
		return out[i].InPort < out[j].InPort
	})
	return out
}

func normalizedProtocols(protocols []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 2)
	for _, protocol := range protocols {
		p := strings.ToLower(strings.TrimSpace(protocol))
		if p != "tcp" && p != "udp" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return []string{"tcp", "udp"}
	}
	sort.Strings(out)
	return out
}

func formatDNATTarget(host string, port int) string {
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	if ip := net.ParseIP(trimmed); ip != nil && ip.To4() == nil {
		return fmt.Sprintf("[%s]:%d", trimmed, port)
	}
	return fmt.Sprintf("%s:%d", trimmed, port)
}

func nftAddressFamily(host string) string {
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return ""
	}
	if ip.To4() == nil {
		return "ip6"
	}
	return "ip"
}
