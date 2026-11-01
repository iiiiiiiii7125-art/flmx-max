package handler

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// subscriptionFrontURLsConfigKey 是「订阅前置地址」设置键：JSON 字符串
// 数组，每个元素是一个前置 base（scheme+host[:port]，无路径）。订阅
// 链接的展示/复制可按前置地址组装——面板域名被部分运营商阻断/烂路由
// 时，用户经前置（反代/CDN）拉取同一条订阅。服务端按 token 出订阅
// 与 Host 无关，前置只是链接组装层的别名，openSubscription 不感知
// 本键。归一化/校验在写面（normalizeAndValidateConfigValue）单点
// 收口，读面拿到的恒是规范形态。
const subscriptionFrontURLsConfigKey = "subscription_front_urls"

// maxSubscriptionFrontURLs 是前置地址个数上限：防误填/滥用撑爆设置
// 与链接选择面；正常场景（按运营商/线路备几条）远用不到。
const maxSubscriptionFrontURLs = 20

// normalizeSubscriptionFrontURLs 校验并归一订阅前置地址配置值。
// 空串返回 ""（走调用方的可清空语义：删除该键、回落未配置）；整体
// 非法（非 JSON 数组、任一元素非法、超上限）整批拒绝并点名原因；
// 重复值（含归一后同值，如仅末尾斜杠之差）去重保序。
func normalizeSubscriptionFrontURLs(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	var entries []string
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return "", fmt.Errorf("订阅前置地址格式无效：须为 JSON 字符串数组")
	}
	out := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		normalized, err := normalizeSubscriptionFrontURL(entry)
		if err != nil {
			return "", err
		}
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) > maxSubscriptionFrontURLs {
		return "", fmt.Errorf("订阅前置地址最多填写 %d 个", maxSubscriptionFrontURLs)
	}
	if len(out) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// normalizeSubscriptionFrontURL 归一单个前置 base：仅允许 http/https
// 的 scheme+host(+端口) 形态，scheme 与主机名小写化、剥除末尾斜杠；
// 路径/查询/片段/用户信息一律拒绝——前置只换域名，路径恒由订阅
// Path 决定，混入这些成分必然是误填，保存期即拦下。
func normalizeSubscriptionFrontURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("订阅前置地址不能为空")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("订阅前置地址格式无效：%q", raw)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("订阅前置地址必须以 http:// 或 https:// 开头：%q", raw)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("订阅前置地址不能包含用户信息：%q", raw)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("订阅前置地址不能包含路径：%q", raw)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("订阅前置地址不能包含查询或片段：%q", raw)
	}
	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("订阅前置地址缺少主机名：%q", raw)
	}
	// 与 panel_domain 同口径的主机名空标签判（主机名字段写面同族门）。
	if err := validateNoEmptyLabelHost(host); err != nil {
		return "", fmt.Errorf("订阅前置地址主机名格式无效：%w", err)
	}
	host = strings.ToLower(host)
	if strings.Contains(host, ":") {
		// IPv6 字面量：Hostname() 已剥方括号，组装时补回。
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" {
		n, convErr := strconv.Atoi(port)
		if convErr != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("订阅前置地址端口无效：%q", raw)
		}
		host += ":" + port
	}
	return scheme + "://" + host, nil
}
