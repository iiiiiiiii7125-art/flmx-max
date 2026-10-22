package kernel

import "strings"

// 订阅中的路由规则（SubOptions.Rules）：配置按服务生成策略组、远程规则集；
// Mihomo 和 sing-box 还会生成 DNS 配置，让国内域名走国内解析。
// Mihomo 和 sing-box 通过节点选择组拉取规则集，国内客户端也能下载。
// Mihomo 读 MetaCubeX .mrs，Stash 读同一规则集的 YAML 格式，sing-box（1.12+）读 .srs；
// Surge / Loon / Quantumult X / Shadowrocket 读 blackmatrix7 列表。
// 开启规则后，Loon / QX / Shadowrocket 输出完整配置而非节点列表（见 share_clients.go）。
// 移植自 kure29/flyx。

const (
	ruleDirectGroup = "全球直连"
	ruleFinalGroup  = "漏网之鱼"

	metaRulesURL = "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/"
	listRulesURL = "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/"
)

// 规则组优先选什么。
const (
	pickProxy  = "proxy"
	pickUS     = "us" // 美国地区组（如果有）：AI 服务拒绝香港节点
	pickDirect = "direct"
	pickReject = "reject"
)

// ruleSet 给每个客户端命名规则集：MetaCubeX geosite / geoip 名，
// 以及 blackmatrix7 列表名（各客户端目录下同名）。
type ruleSet struct {
	sites []string
	ips   []string
	lists []string
	// ipLists 是 blackmatrix7 的 IP 列表，给 Loon 和 Quantumult X 用：
	// 它们的本地 GEOIP 规则会跑在远程列表之前。
	ipLists []string
}

type ruleGroupDef struct {
	key, name string
	pick      string
	set       ruleSet
	// before 把这些规则集排到本组规则之前，例如国内 Steam 域名直连。
	before       ruleSet
	beforeTarget string
}

// ruleGroupDefs 是可选的服务组，按规则顺序排列（更具体的集合在前：
// YouTube 在 Google 之前）。
var ruleGroupDefs = []ruleGroupDef{
	{key: "ads", name: "广告拦截", pick: pickReject,
		set: ruleSet{sites: []string{"category-ads-all"}, lists: []string{"Advertising"}}},
	{key: "ai", name: "AI 服务", pick: pickUS,
		set: ruleSet{sites: []string{"openai", "anthropic", "google-gemini", "category-ai-!cn"}, lists: []string{"OpenAI", "Claude", "Gemini"}}},
	{key: "youtube", name: "YouTube", pick: pickProxy,
		set: ruleSet{sites: []string{"youtube"}, lists: []string{"YouTube"}}},
	{key: "netflix", name: "Netflix", pick: pickProxy,
		set: ruleSet{sites: []string{"netflix"}, ips: []string{"netflix"}, lists: []string{"Netflix"}}},
	{key: "media", name: "国外媒体", pick: pickProxy,
		set: ruleSet{sites: []string{"disney", "spotify", "tiktok"}, lists: []string{"Disney", "Spotify", "TikTok"}}},
	{key: "telegram", name: "Telegram", pick: pickProxy,
		set: ruleSet{sites: []string{"telegram"}, ips: []string{"telegram"}, lists: []string{"Telegram"}}},
	{key: "google", name: "Google", pick: pickProxy,
		set: ruleSet{sites: []string{"google"}, ips: []string{"google"}, lists: []string{"Google"}}},
	// GitHub 属于微软，但直连慢或被墙。
	{key: "microsoft", name: "微软服务", pick: pickDirect,
		set:    ruleSet{sites: []string{"onedrive", "microsoft"}, lists: []string{"OneDrive", "Microsoft"}},
		before: ruleSet{sites: []string{"github"}, lists: []string{"GitHub"}}, beforeTarget: "节点选择"},
	{key: "apple", name: "苹果服务", pick: pickDirect,
		set: ruleSet{sites: []string{"apple"}, lists: []string{"Apple"}}},
	{key: "games", name: "游戏平台", pick: pickDirect,
		set:    ruleSet{sites: []string{"steam", "category-games"}, lists: []string{"Steam", "Game"}},
		before: ruleSet{sites: []string{"steam@cn", "category-games@cn"}, lists: []string{"SteamCN"}}, beforeTarget: "DIRECT"},
}

// RuleGroup 是可选的服务组，供编辑器用。
type RuleGroup struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// RuleGroups 按展示顺序列出可选的服务组。
func RuleGroups() []RuleGroup {
	out := make([]RuleGroup, len(ruleGroupDefs))
	for i, d := range ruleGroupDefs {
		out[i] = RuleGroup{Key: d.key, Name: d.name}
	}
	return out
}

// NormalizeRuleGroups 保留已知的 key，按展示顺序排列。
func NormalizeRuleGroups(keys []string) []string {
	want := toSet(keys)
	out := []string{}
	for _, d := range ruleGroupDefs {
		if want[d.key] {
			out = append(out, d.key)
		}
	}
	return out
}

func enabledRuleGroups(opts SubOptions) []ruleGroupDef {
	want := toSet(opts.RuleGroups)
	var out []ruleGroupDef
	for _, d := range ruleGroupDefs {
		if want[d.key] {
			out = append(out, d)
		}
	}
	return out
}

// ruleStep 把一个规则集路由到一个组（"DIRECT" 是客户端的直连策略）。
type ruleStep struct {
	set    ruleSet
	target string
	// reject 标记广告拦截步骤：sing-box 直接拒绝，没有 reject 出站可放进组。
	reject bool
}

// ruleSteps 按顺序列出规则；最后一条（ruleFinalGroup）由各格式自行处理。
func ruleSteps(opts SubOptions) []ruleStep {
	steps := []ruleStep{{set: ruleSet{sites: []string{"private"}, ips: []string{"private"}, lists: []string{"Lan"}}, target: ruleDirectGroup}}
	for _, d := range enabledRuleGroups(opts) {
		if d.beforeTarget != "" {
			steps = append(steps, ruleStep{set: d.before, target: d.beforeTarget})
		}
		steps = append(steps, ruleStep{set: d.set, target: d.name, reject: d.pick == pickReject})
	}
	return append(steps,
		ruleStep{set: ruleSet{sites: []string{"geolocation-!cn"}, lists: []string{"Global"}}, target: "节点选择"},
		ruleStep{set: ruleSet{sites: []string{"cn"}, ips: []string{"cn"}, lists: []string{"China"}, ipLists: []string{"ChinaIPs"}}, target: ruleDirectGroup})
}

// policyGroup 是客户端无关的 select 组；"DIRECT" 和 "REJECT" 是客户端内置策略。
type policyGroup struct {
	name    string
	members []string
}

// rulePolicyGroups 是服务组加上直连组和收尾组。regions 是配置里的地区组名。
func rulePolicyGroups(opts SubOptions, regions []string) []policyGroup {
	proxies := append([]string{"节点选择", "自动选择"}, regions...)
	var out []policyGroup
	for _, d := range enabledRuleGroups(opts) {
		var members []string
		switch d.pick {
		case pickReject:
			members = []string{"REJECT", "DIRECT"}
		case pickDirect:
			members = append([]string{"DIRECT"}, proxies...)
		case pickUS:
			members = append(usFirst(proxies), "DIRECT")
		default:
			members = append(append([]string{}, proxies...), "DIRECT")
		}
		out = append(out, policyGroup{d.name, members})
	}
	return append(out,
		policyGroup{ruleDirectGroup, []string{"DIRECT", "节点选择"}},
		policyGroup{ruleFinalGroup, append(append([]string{}, proxies...), "DIRECT")})
}

// usGroup 是美国地区组名（见 regions）。
const usGroup = "🇺🇸 美国"

// usFirst 把美国地区组移到最前。
func usFirst(names []string) []string {
	out := []string{}
	for _, n := range names {
		if n == usGroup {
			out = append(out, n)
		}
	}
	for _, n := range names {
		if n != usGroup {
			out = append(out, n)
		}
	}
	return out
}

// ruleGroupNames 是规则可能新增的所有组名，节点名不得占用。
func ruleGroupNames() []string {
	out := []string{ruleDirectGroup, ruleFinalGroup}
	for _, d := range ruleGroupDefs {
		out = append(out, d.name)
	}
	return out
}

// ruleSetTag 给配置中的规则集命名："site-geolocation-not-cn"。
func ruleSetTag(kind, name string) string {
	return kind + "-" + strings.NewReplacer("-!", "-not-", "@", "-").Replace(name)
}

func metaRuleURL(branch, kind, name, ext string) string {
	return metaRulesURL + branch + "/geo/" + kind + "/" + name + ext
}

// listRuleURL 是某客户端目录（"Surge" / "Loon" / "QuantumultX" / "Clash"）下的 blackmatrix7 列表。
func listRuleURL(dir, name, ext string) string {
	return listRulesURL + dir + "/" + name + "/" + name + ext
}

// ruleTarget 把步骤目标映射到客户端的直连策略名。
func ruleTarget(target, direct string) string {
	if target == "DIRECT" {
		return direct
	}
	return target
}

// ---- Mihomo ----

var (
	domesticDoH = []string{"https://dns.alidns.com/dns-query", "https://doh.pub/dns-query"}
	foreignDoH  = []string{"https://dns.google/dns-query", "https://cloudflare-dns.com/dns-query"}
)

// clashRules 返回 Mihomo 配置的规则 providers 和 rules；
// yamlSets 为 Stash 写 YAML 格式的规则集（Stash 读不了 .mrs）。
func clashRules(opts SubOptions, yamlSets bool) (yamlNode, []interface{}) {
	format, ext := "mrs", ".mrs"
	if yamlSets {
		format, ext = "yaml", ".yaml"
	}
	providers := yamlNode{}
	seen := map[string]bool{}
	provider := func(kind, name string) string {
		tag := ruleSetTag(kind, name)
		if !seen[tag] {
			seen[tag] = true
			behavior, dir := "domain", "geosite"
			if kind == "ip" {
				behavior, dir = "ipcidr", "geoip"
			}
			providers = append(providers, yamlField{tag, yamlNode{
				{"type", "http"}, {"behavior", behavior}, {"format", format},
				{"url", metaRuleURL("meta", dir, name, ext)},
				{"path", "./ruleset/" + tag + ext}, {"interval", 86400}, {"proxy", "节点选择"},
			}})
		}
		return tag
	}
	var rules []interface{}
	for _, s := range ruleSteps(opts) {
		for _, n := range s.set.sites {
			rules = append(rules, "RULE-SET,"+provider("site", n)+","+s.target)
		}
		for _, n := range s.set.ips {
			rules = append(rules, "RULE-SET,"+provider("ip", n)+","+s.target+",no-resolve")
		}
	}
	return providers, append(rules, "MATCH,"+ruleFinalGroup)
}

// clashRuleSettings 是搭配规则的 Mihomo 顶层设置：fake-ip DNS，
// 国内域名走国内解析，其余经代理走 DoH，外加嗅探。
func clashRuleSettings() yamlNode {
	private, cn := ruleSetTag("site", "private"), ruleSetTag("site", "cn")
	return yamlNode{
		{"unified-delay", true},
		{"tcp-concurrent", true},
		{"dns", yamlNode{
			{"enable", true},
			{"ipv6", true},
			{"enhanced-mode", "fake-ip"},
			{"fake-ip-range", "198.18.0.1/16"},
			{"fake-ip-filter", []string{"rule-set:" + private + "," + cn, "+.lan", "+.local",
				"+.msftconnecttest.com", "+.msftncsi.com"}},
			// DNS 查询走规则，所以国外 DoH 经代理访问。
			{"respect-rules", true},
			{"default-nameserver", []string{"223.5.5.5", "119.29.29.29"}},
			{"proxy-server-nameserver", domesticDoH},
			{"nameserver", foreignDoH},
			{"nameserver-policy", yamlNode{{"rule-set:" + private + "," + cn, domesticDoH}}},
		}},
		{"sniffer", yamlNode{
			{"enable", true},
			{"parse-pure-ip", true},
			{"sniff", yamlNode{
				{"HTTP", yamlNode{{"ports", []interface{}{80, "8080-8880"}}, {"override-destination", true}}},
				{"TLS", yamlNode{{"ports", []interface{}{443, 8443}}}},
				{"QUIC", yamlNode{{"ports", []interface{}{443, 8443}}}},
			}},
		}},
	}
}

// ---- sing-box ----

// singboxRules 返回 sing-box 1.12+ 配置的路由规则（sniff / hijack-dns 之后）、
// 远程规则集和 DNS 段。
func singboxRules(opts SubOptions) ([]interface{}, []interface{}, map[string]interface{}) {
	var sets []interface{}
	seen := map[string]bool{}
	ruleSetTags := func(s ruleSet) []string {
		var tags []string
		add := func(kind, dir, name string) {
			tag := ruleSetTag(kind, name)
			tags = append(tags, tag)
			if !seen[tag] {
				seen[tag] = true
				sets = append(sets, map[string]interface{}{"type": "remote", "tag": tag, "format": "binary",
					"url": metaRuleURL("sing", dir, name, ".srs"), "download_detour": "节点选择", "update_interval": "1d"})
			}
		}
		for _, n := range s.sites {
			add("site", "geosite", n)
		}
		for _, n := range s.ips {
			add("ip", "geoip", n)
		}
		return tags
	}
	rules := []interface{}{map[string]interface{}{"ip_is_private": true, "outbound": ruleDirectGroup}}
	for _, s := range ruleSteps(opts) {
		rule := map[string]interface{}{"rule_set": ruleSetTags(s.set)}
		if s.reject {
			rule["action"] = "reject"
		} else {
			rule["outbound"] = ruleTarget(s.target, "direct")
		}
		rules = append(rules, rule)
	}
	dns := map[string]interface{}{
		"servers": []interface{}{
			map[string]interface{}{"type": "https", "tag": "dns-remote", "server": "1.1.1.1", "detour": "节点选择"},
			map[string]interface{}{"type": "https", "tag": "dns-local", "server": "223.5.5.5"},
		},
		"rules": []interface{}{
			map[string]interface{}{"rule_set": []string{ruleSetTag("site", "private"), ruleSetTag("site", "cn")}, "server": "dns-local"},
		},
		"final": "dns-remote",
	}
	return rules, sets, dns
}

// ---- Surge ----

func surgeRules(opts SubOptions) []string {
	var lines []string
	for _, s := range ruleSteps(opts) {
		for _, n := range s.set.lists {
			lines = append(lines, "RULE-SET,"+listRuleURL("Surge", n, ".list")+","+s.target)
		}
	}
	return append(lines, "GEOIP,CN,"+ruleDirectGroup, "FINAL,"+ruleFinalGroup+",dns-failed")
}
