package nftables

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
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
//
// Legacy derivation, kept verbatim as the fallback when no install
// identity is configured (R18-C3-02): already-deployed installs
// keep their table, so upgrading does not orphan rules. New code
// must prefer TableNameForInstall, which mixes in the per-install
// identity; the legacy name alone is NOT collision-free across
// installs (two default-instance panels share flmx_e3b0c442).
func TableNameForInstance(instance string) string {
	sum := sha256.Sum256([]byte(instance))
	return "flmx_" + hex.EncodeToString(sum[:])[:8]
}

// validInstallID reports whether id is usable as a per-install
// identity (FLMX_INSTALL_ID): 1-64 chars, alphanumerics, hyphen
// and underscore, starting with an alphanumeric. UUIDs as written
// by panel_install.sh pass. Anything else is treated as absent
// (legacy fallback), never hashed in: hashing an unvalidated value
// would silently mint a new table for a typo'd identity.
func validInstallID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// tableNameSuffix is the 8-hex suffix shared by the main and pause
// tables of one install: sha256(instance + "|" + installID) when a
// valid install identity is present, else the legacy sha256(instance).
// The "|" separator keeps ("a", "b|c") distinct from ("a|b", "c").
func tableNameSuffix(instance, installID string) string {
	input := instance
	if validInstallID(installID) {
		input = instance + "|" + installID
	}
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:8]
}

// TableNameForInstall derives this panel install's nft table name
// from its instance key plus its per-install identity (R18-C3-02):
// flmx_<first 8 hex of sha256(instance + "|" + installID)>.
// The identity is generated once at install time and persisted in
// the deployment .env (FLMX_INSTALL_ID); two default-instance
// panels on different hosts therefore get different tables and no
// longer delete each other's rules. With no (or an invalid)
// identity it falls back to TableNameForInstance, so deployed
// installs migrate smoothly without losing rules.
func TableNameForInstall(instance, installID string) string {
	return "flmx_" + tableNameSuffix(instance, installID)
}

// TableName returns the nft table this plan renders into. A plan
// without an instance key (zero value, old callers) renders into
// the default instance's table, never the plain name.
func (p NodePlan) TableName() string {
	return TableNameForInstall(p.InstanceID, p.InstallID)
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
			// R-B01-01：L3 匹配（ip daddr）必须在 L4 匹配（tcp/udp dport）
			// 之前——nft 要求按协议栈顺序写，`tcp ip daddr …` 报
			// `syntax error, unexpected ip`。bindMatch 为空时渲染与
			// 之前完全一致（`tcp dport …`）。
			b.WriteString(fmt.Sprintf("    %s%s dport %d counter dnat %sto %s comment %q\n",
				bindMatch,
				protocol,
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
			// R18-C3-05：纵深——ValidatePlan 已拒主机名目标，此
			// 分支只在绕过 ValidatePlan 直接渲染时可达；跳过计数
			// 时点名转发 ID，绝不静默丢计数。
			log.Printf("nftables render: forward %d target %q is not an IP literal, forward-chain counting rules skipped", rule.ForwardID, rule.TargetHost)
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

// ValidateBindIPTargetFamily rejects a BindIP/target pair whose
// address families differ (R18-C3-03): v6 BindIP + v4 target (or
// the reverse) renders `ip6 daddr … dnat ip to …`, which nft
// refuses with `conflicting protocols: ip6 vs. ip` — and because
// the whole node sync is one nft batch, a single such forward
// froze the entire node's sync. Either side without a parseable
// family (empty BindIP, hostname BindIP/target) cannot be judged
// and is skipped: hostname BindIP is R18-C3-04, out of scope here.
func ValidateBindIPTargetFamily(bindIP, targetHost string) error {
	bindFamily, bindIPNorm, ok := bindIPFamilyIP(bindIP)
	if !ok {
		return nil
	}
	targetFamily := nftAddressFamily(targetHost)
	if targetFamily == "" {
		return nil
	}
	if bindFamily == targetFamily {
		return nil
	}
	bindName := "IPv4"
	targetName := "IPv4"
	if bindFamily == "ip6" {
		bindName = "IPv6"
	}
	if targetFamily == "ip6" {
		targetName = "IPv6"
	}
	return fmt.Errorf("nftables 监听IP %s（%s）与目标地址 %s（%s）地址族不一致：nft 会以 conflicting protocols 拒绝整批规则，请把监听IP与目标地址改为同一地址族", bindIPNorm, bindName, strings.Trim(strings.TrimSpace(targetHost), "[]"), targetName)
}

// ValidateBindIP rejects a BindIP that is not an IP literal
// (R18-C3-04): bindIPFamilyIP silently drops hostname BindIPs and
// the DNAT rule then renders with no daddr restriction, so a
// forward the admin believed bound to one address listens on all
// addresses (fail-open). Empty BindIP is the wildcard form and
// passes; IP literals (plain, bracketed IPv6, host:port with an IP
// host) pass. A hostname must be rejected, never silently widened.
func ValidateBindIP(bindIP string) error {
	trimmed := strings.TrimSpace(bindIP)
	if trimmed == "" {
		return nil
	}
	if _, _, ok := bindIPFamilyIP(trimmed); ok {
		return nil
	}
	return fmt.Errorf("nftables 监听IP %q 不是 IP 地址：主机名形式的监听IP会被渲染层静默丢弃、转发退化为全地址监听，请填写节点 IP 地址或留空（留空即监听全部地址）", trimmed)
}

// ValidatePlan 校验计划内每条规则的 TargetHost（C3-01），错误点名
// 出错的转发 ID。Manager.Reconcile 在渲染与下发之前调用，非法计划
// 整单拒绝、脚本不下发节点。
func ValidatePlan(plan NodePlan) error {
	for _, rule := range plan.Rules {
		if err := ValidateTargetHost(rule.TargetHost); err != nil {
			return fmt.Errorf("转发 %d: %w", rule.ForwardID, err)
		}
		// R18-C3-04：主机名 BindIP 整单拒绝（见上）——渲染层会
		// 静默丢弃它，转发退化为全地址监听，绝不能放行下发。
		if err := ValidateBindIP(rule.BindIP); err != nil {
			return fmt.Errorf("转发 %d: %w", rule.ForwardID, err)
		}
		// R18-C3-05：主机名目标整单拒绝——ValidateTargetHost 放行
		// 主机名、DNAT 照常下发，但 forward 链按族跳过该规则、流
		// 量全程不计量。intake（validateNftablesForwardRequest）
		// 本就只接受 IP 目标，渲染层须同口径，绝不一面放行、一面
		// 静默丢计数。走到这里时 TargetHost 已过 ValidateTarget-
		// Host，故无族即严格主机名。
		if nftAddressFamily(rule.TargetHost) == "" {
			return fmt.Errorf("转发 %d: nftables 目标地址 %q 不是 IP 地址：主机名目标无法生成 forward 链计数规则，流量将不被计量，请填写目标 IP 地址", rule.ForwardID, strings.Trim(strings.TrimSpace(rule.TargetHost), "[]"))
		}
		// R18-C3-03：BindIP 与目标地址族混用整单拒绝。
		if err := ValidateBindIPTargetFamily(rule.BindIP, rule.TargetHost); err != nil {
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
