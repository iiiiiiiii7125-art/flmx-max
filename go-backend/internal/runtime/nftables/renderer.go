package nftables

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"
)

// 改名过渡：表名由 flvx 改为 flmx。runner 的应用脚本会同时删除 legacy
// 表，避免旧表残留在内核中继续生效（转发/计数双份）。
const (
	tableName       = "flmx"
	legacyTableName = "flvx"
)

func RenderTable(plan NodePlan) string {
	var b strings.Builder
	b.WriteString("table inet " + tableName + " {\n")
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
				counterComment(rule.ForwardID, CounterDirectionDNAT, protocol),
			))
		}
	}
	b.WriteString("  }\n\n")
	b.WriteString("  chain postrouting {\n")
	b.WriteString("    type nat hook postrouting priority srcnat; policy accept;\n")
	if len(plan.Rules) > 0 {
		b.WriteString("    masquerade comment \"" + tableName + " masquerade\"\n")
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
				counterComment(rule.ForwardID, CounterDirectionToTarget, protocol),
			))
			b.WriteString(fmt.Sprintf("    meta l4proto %s ct original proto-dst %d %s%s saddr %s %s sport %d counter comment %q\n",
				protocol,
				rule.InPort,
				bindMatch,
				family,
				targetHost,
				protocol,
				rule.TargetPort,
				counterComment(rule.ForwardID, CounterDirectionFromTarget, protocol),
			))
		}
	}
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

func counterComment(forwardID int64, direction, protocol string) string {
	return fmt.Sprintf("%s forward:%d %s %s", tableName, forwardID, direction, protocol)
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
