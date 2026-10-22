package repo

import "strings"

type ConfigAccessPolicy string

const (
	ConfigAccessPublic    ConfigAccessPolicy = "public"
	ConfigAccessSensitive ConfigAccessPolicy = "sensitive"
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

func PolicyForConfig(name string) ConfigAccessPolicy {
	if IsPublicConfigKey(name) {
		return ConfigAccessPublic
	}
	if IsSensitiveConfigKey(name) {
		return ConfigAccessSensitive
	}
	return ConfigAccessSensitive
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

func FilterSensitiveConfigs(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for name, value := range in {
		if IsSensitiveConfigKey(name) {
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
