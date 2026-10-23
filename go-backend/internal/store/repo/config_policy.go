package repo

import "strings"

type ConfigAccessPolicy string

const (
	ConfigAccessPublic    ConfigAccessPolicy = "public"
	ConfigAccessSensitive ConfigAccessPolicy = "sensitive"
	// ConfigAccessAuthenticated 是 R10X-014（A-02）接线时补的三级
	// 分类：任意已登录用户可读、匿名不可读。此前 PolicyForConfig
	// 只有 public/sensitive 两级且零生产调用，执行读路径走名单制
	// （未列名即可读），与「未知键 → 敏感」的默认口径矛盾。接线后
	// 读路径统一过 PolicyForConfig：现行在用键逐键补进
	// public / authenticated / sensitive 三表（见下），未分类的新键
	// 落默认 sensitive，fail-closed——新增凭据键只加存储不加名单
	// 时普通用户读不到，而不是静默可读。
	ConfigAccessAuthenticated ConfigAccessPolicy = "authenticated"
)

var publicConfigKeys = map[string]struct{}{
	"app_name":            {},
	"app_logo":            {},
	"app_favicon":         {},
	"app_bg_image":        {},
	"app_bg_image_light":  {},
	"app_bg_image_dark":   {},
	"login_bg_image":      {},
	"cloudflare_site_key": {},
	"is_commercial":       {},
	"hide_footer_brand":   {},
}

var sensitiveConfigKeys = map[string]struct{}{
	"jwt_secret":            {},
	"license_key":           {},
	"license_expiry":        {},
	"license_machine_id":    {},
	"machine_fingerprint":   {},
	"cloudflare_secret_key": {},
	"node_sub_token":        {},
	// Notification credentials: hidden from non-admins and kept out of
	// backups, like the other secrets. The whole notify credential group
	// is listed: chat_id identifies the admin's Telegram chat and
	// api_base can point at a private bot-API proxy, so neither may leak
	// to non-admins via config/list (A3 F-4: chat_id was missing).
	"notify_telegram_bot_token": {},
	"notify_telegram_chat_id":   {},
	"notify_telegram_api_base":  {},
	"notify_webhook_url":        {},
}

// requiredConfigKeys carry a mandatory value domain: submitting an empty
// value is rejected (with the key named in the error) instead of clearing
// the key. These are the domain-validated keys from
// normalizeAndValidateConfigValue (enum/numeric monitors) plus jwt_secret,
// whose absence would silently fall back to a different signing key.
// Monitor key literals mirror internal/monitoring constants
// (ConfigMonitorRetentionDays / ConfigTunnelQualityProbeIntervalSec) and
// the handler's monitorTunnelQualityEnabledConfigKey.
var requiredConfigKeys = map[string]struct{}{
	"jwt_secret":                          {},
	"monitor_tunnel_quality_enabled":      {},
	"monitor_retention_days":              {},
	"monitor_tunnel_quality_interval_sec": {},
}

var systemManagedConfigKeys = map[string]struct{}{
	"license_key":         {},
	"license_expiry":      {},
	"license_machine_id":  {},
	"is_commercial":       {},
	"machine_fingerprint": {},
	"node_sub_token":      {},
}

// authenticatedConfigKeys 是现行在用、但既非公开品牌键也非凭据的
// 配置键全集（R10X-014 接线前置复核：前端 config 页 + forward 页 +
// 节点/监控面 getConfigByName 调用点 + 后端引用逐键枚举）。这些键
// 此前经名单制读路径对任意已登录用户可读，接线后分类不变其可读
// 性，只是不再依赖「未列名即可读」的默认放行。新增在用键必须先
// 补进本表（或 public/sensitive 表）再上线，否则普通用户读不到
// （fail-closed，管理员不受影响）。
var authenticatedConfigKeys = map[string]struct{}{
	// 前端 forward 页普通用户直读（UI 偏好，全局键）。
	"forward_compact_mode":        {},
	"forward_group_order_map":     {},
	"forward_group_collapsed_map": {},
	// 节点延迟目标（延迟测试点清单，非凭据）。
	"latency_targets": {},
	// 监控/隧道质量设置（config 页 + 隧道监控面直读）。
	"monitor_tunnel_quality_enabled":      {},
	"monitor_tunnel_quality_interval_sec": {},
	"monitor_retention_days":              {},
	// 服务监控内部限值（monitoring/limits.go 常量，后端引用全集）。
	"service_monitor_checker_scan_interval_sec": {},
	"service_monitor_worker_limit":              {},
	"service_monitor_min_interval_sec":          {},
	"service_monitor_default_interval_sec":      {},
	"service_monitor_min_timeout_sec":           {},
	"service_monitor_default_timeout_sec":       {},
	"service_monitor_max_timeout_sec":           {},
	// 面板设置（config 页）：开关与地址类，无凭据。
	"captcha_enabled":         {},
	"panel_domain":            {},
	"ip":                      {},
	"github_proxy_enabled":    {},
	"github_proxy_url":        {},
	"allow_local_remote_addr": {},
	// 通知设置中的非凭据键（凭据键 notify_telegram_*/notify_webhook_url
	// 在 sensitiveConfigKeys；日报台账键 notify_daily_last /
	// notify_daily_inbound_total 是运行时状态而非配置，不入本表，
	// 对非管理员按默认 sensitive 收口，管理员与后端直读不受影响）。
	"notify_enabled":          {},
	"notify_events":           {},
	"notify_daily_hour":       {},
	"notify_latency_loss_pct": {},
	"notify_latency_ms":       {},
}

func PolicyForConfig(name string) ConfigAccessPolicy {
	if IsPublicConfigKey(name) {
		return ConfigAccessPublic
	}
	if IsSensitiveConfigKey(name) {
		return ConfigAccessSensitive
	}
	if IsAuthenticatedConfigKey(name) {
		return ConfigAccessAuthenticated
	}
	return ConfigAccessSensitive
}

func IsAuthenticatedConfigKey(name string) bool {
	_, ok := authenticatedConfigKeys[normalizeConfigKey(name)]
	return ok
}

func IsPublicConfigKey(name string) bool {
	_, ok := publicConfigKeys[normalizeConfigKey(name)]
	return ok
}

func IsSensitiveConfigKey(name string) bool {
	_, ok := sensitiveConfigKeys[normalizeConfigKey(name)]
	return ok
}

func IsSystemManagedConfigKey(name string) bool {
	_, ok := systemManagedConfigKeys[normalizeConfigKey(name)]
	return ok
}

func IsRequiredConfigKey(name string) bool {
	_, ok := requiredConfigKeys[normalizeConfigKey(name)]
	return ok
}

// IsClearableConfigKey reports whether submitting an empty value for the
// key clears it: the stored row is deleted and reads fall back to the
// built-in default. Every key that is neither system-managed nor required
// is clearable — credentials, URLs and optional text all treat "absent"
// as their default state. System-managed keys are rejected by callers
// before this is consulted; they are reported non-clearable here as well
// so the predicate is safe to use on its own.
func IsClearableConfigKey(name string) bool {
	if IsSystemManagedConfigKey(name) {
		return false
	}
	return !IsRequiredConfigKey(name)
}

// FilterSensitiveConfigs 是 config/list 非管理员读路径的过滤器
// （R10X-014 接线）：统一过 PolicyForConfig，只保留 public 与
// authenticated 两级；sensitive 与未分类键一律不下发。此前按
// sensitiveConfigKeys 名单制过滤，未列名的新键会原样下发给任意
// 已登录用户，与 PolicyForConfig 的默认敏感口径矛盾。
func FilterSensitiveConfigs(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for name, value := range in {
		if PolicyForConfig(name) == ConfigAccessSensitive {
			continue
		}
		out[name] = value
	}
	return out
}

// backupRuntimeExcludedConfigKeys 是备份 configs 段的运行时台账
// 排除集（R2X-047）：这两键是通知日报的运行时状态（上次发送日期、
// 入站流量快照），不是管理员配置——随备份往返会在恢复旧备份时回滚
// 目标实例的日报台账（当日重复发送、差值失真）。排除只按**全名精确
// 命中**本集合，禁止前缀匹配：
//   - pending_realm_settle:* 前缀族是 X-P3-34 有意落库的待结算标记，
//     必须随备份恢复，显式豁免，不得以 pending_/notify_ 等前缀误杀；
//   - notify_daily_hour 是日报发送时刻的真配置键（与两日报台账键
//     同前缀），显式豁免，照常往返。
var backupRuntimeExcludedConfigKeys = map[string]struct{}{
	"notify_daily_last":          {},
	"notify_daily_inbound_total": {},
}

func isBackupRuntimeExcludedConfigKey(name string) bool {
	_, ok := backupRuntimeExcludedConfigKeys[normalizeConfigKey(name)]
	return ok
}

func FilterBackupConfigs(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for name, value := range in {
		if IsSensitiveConfigKey(name) || IsSystemManagedConfigKey(name) || isBackupRuntimeExcludedConfigKey(name) {
			continue
		}
		out[name] = value
	}
	return out
}

func normalizeConfigKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
