package kernel

import "strings"

// 规则集词汇：订阅模板 builder 生成配置时用的服务分类与规则集地址。
// 原订阅规则引擎（SubOptions.Rules / RuleGroups 驱动的内置渲染注入）
// 已于 2.1.2 随「规则与脚本」删除；本文件只保留模板 builder 仍共用的
// 规则集定义（分类、规则源 URL、标签命名、国内 DoH），不再有任何
// 订阅选项消费面。Mihomo 读 MetaCubeX .mrs，Stash 读同一规则集的
// YAML 格式，sing-box（1.12+）读 .srs；Surge / Loon / Quantumult X /
// Shadowrocket 读 blackmatrix7 列表。

const (
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

// ruleTarget 把规则目标映射到客户端的直连策略名。
func ruleTarget(target, direct string) string {
	if target == "DIRECT" {
		return direct
	}
	return target
}

// domesticDoH 是模板生成配置里国内解析的 DoH 集。
var domesticDoH = []string{"https://dns.alidns.com/dns-query", "https://doh.pub/dns-query"}
