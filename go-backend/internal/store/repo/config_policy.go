package repo

import (
	"sort"
	"strings"
)

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
	// FIX-KCPMEM (P1-04/P1-05): KCP generation-face settings, admin-only.
	// kcp_fec_mode is the FEC档位 (default/off); kcp_node_mem_mb is the
	// per-node memory declaration (nodeID→MiB JSON) that caps KCP
	// windows/buffers. Neither is read by any non-admin surface (no
	// frontend getConfigByName call); both are written only via the
	// admin config page with normalizeAndValidateConfigValue, and an
	// empty submission clears back to default. Sensitive, as
	// kcp_tuning.go states for kcp_fec_mode; the node-memory map is
	// infrastructure detail with the same admin-only posture.
	"kcp_fec_mode":    {},
	"kcp_node_mem_mb": {},
}

// requiredConfigKeys carry a mandatory value domain: submitting an empty
// value is rejected (with the key named in the error) instead of clearing
// the key. These are the domain-validated keys from
// normalizeAndValidateConfigValue (enum/numeric monitors) plus jwt_secret.
// jwt_secret is listed as defense-in-depth only: its DB row has no
// consumer — the signing key comes solely from the JWT_SECRET env var or
// the DB-sibling jwt_secret file (internal/config EnsureJWTSecret) — and
// the handler write face rejects every value with a named error
// (R1-11-02); this entry keeps an empty submission named-rejected even
// if that gate were bypassed. (An earlier comment here described a
// fallback to a different signing key on absence; that path no longer
// exists.)
// Monitor key literals mirror internal/monitoring constants
// (ConfigMonitorRetentionDays / ConfigTunnelQualityProbeIntervalSec) and
// the handler's monitorTunnelQualityEnabledConfigKey. The service_monitor_*
// limits family (RR-F5-01) is domain-validated at save time too but is
// deliberately NOT listed here: those keys are clearable — an empty
// submission clears back to the built-in defaults, which are always in
// domain (same posture as notify_daily_hour).
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
	// 节点页展示分组的段序名单（JSON 标签数组，纯展示偏好）：登记
	// 为 authenticated 使其随备份 configs 段导出/恢复（导出面只剔
	// sensitive，见 FilterBackupConfigs），与 forward 同族口径。
	"node_display_group_order": {},
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
	"captcha_enabled": {},
	"panel_domain":    {},
	"ip":              {},
	// 订阅前置地址（KU6）：链接组装层设置（前置 base 列表），无凭据；
	// 普通用户的「我的订阅」页也要读它组装前置链接，故同
	// panel_domain 一样归 authenticated（已登录可读）。
	"subscription_front_urls": {},
	// 1.7.7（KU5）：github_proxy_enabled / github_proxy_url 已随
	// 「开启 GitHub 加速」设置整体移除，不再列入 authenticated
	// 可读面——两键落默认 sensitive（非管理员不可读），已存值不
	// 做破坏性迁移、仅不再读写。
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
	// I-01：待发配额恢复通知是运行时台账（解禁时发送失败的补发
	// 依据），不是管理员配置——随备份往返会在恢复旧备份时把已
	// 送达过的恢复通知在目标实例上重发一遍。
	"notify_quota_recovery_pending": {},
	// R6-I-01：待发标记的世代水位与标记本身同为运行时台账——
	// 随备份往返会在恢复旧备份时把水位回滚，使旧回调在目标实例上
	// 重新满足“同代”条件而复活过时消息。
	"notify_quota_recovery_pending_gen": {},
	// R3-MON-01：升级未落地告警的待发台账与世代水位（发送失败
	// 待补发的依据），与配额两键同理——随备份往返会在目标实例
	// 上重发已送达的告警／回滚水位复活过时文案。
	"notify_upgrade_failed_pending":     {},
	"notify_upgrade_failed_pending_gen": {},
}

func isBackupRuntimeExcludedConfigKey(name string) bool {
	_, ok := backupRuntimeExcludedConfigKeys[normalizeConfigKey(name)]
	return ok
}

// retiredConfigKeysList 是已随功能下线而注销的配置键全集（单一
// 事实源）：这些键的功能代码已删除、键已退出全部策略登记表，存量
// 行由对应迁移一次性删净。但迁移是版本门控的一次性步进，实例到
// 位后永不重跑；备份导入面又按 R3 K5 口径对未分类键原样恢复——
// 旧备份一恢复，注销键即全数落库复活、无读者、无清理路径，与
// 「删除不留残留」裁定正相反（R2-E3-F03）。故备份过滤骨架恒排除
// 本集（导出/导入两面），与 schema v8 迁移
// migrateRemoveGistConfigKeys 同源消费本清单——增删注销键只改
// 这一处，两面自动同步。
//   - gist_* 8 键＋ Gist 失败通知台账 2 键（DEV-RM-GIST，2.1.0；
//     其中 gist_token 是 GitHub 凭据本体）。
var retiredConfigKeysList = []string{
	"gist_token",
	"gist_password",
	"gist_id",
	"gist_interval_hours",
	"gist_last_at",
	"gist_last_upload",
	"gist_last_error",
	"gist_last_hash",
	"notify_gist_failure_pending",
	"notify_gist_failure_pending_gen",
}

var retiredConfigKeys = func() map[string]struct{} {
	out := make(map[string]struct{}, len(retiredConfigKeysList))
	for _, k := range retiredConfigKeysList {
		out[k] = struct{}{}
	}
	return out
}()

// IsRetiredConfigKey 报告键是否已随功能下线注销（备份恒排除集，
// 与迁移删行清单 retiredConfigKeysList 同源）。
func IsRetiredConfigKey(name string) bool {
	_, ok := retiredConfigKeys[normalizeConfigKey(name)]
	return ok
}

// backupFilterExemptConfigKeyPrefixes 是备份**导出**过滤的设计
// 豁免前缀族（RB4-01）：这些键按 PolicyForConfig 落默认 sensitive，
// 但其值是必须随备份往返的运行时语义数据而非凭据。豁免只按本
// 登记逐前缀匹配（键名先归一），新增豁免必须在此显式登记并注明
// 理由——此前 pending_realm_settle 族靠「不在过滤明单内」隐式
// 保留，导出过滤改用默认敏感口径后若无此显式豁免会被误杀
// （导入面不叠加默认敏感口径、本就保留，不经此豁免）。
//   - pending_realm_settle:（X-P3-34）：realm 引擎切换的待结算标记
//     （handler/realm.go pendingRealmSettleKey 生成），恢复旧备份
//     时丢失即丢结算；值为标记 JSON，非凭据。
var backupFilterExemptConfigKeyPrefixes = []string{
	"pending_realm_settle:",
}

func isBackupFilterExemptConfigKey(name string) bool {
	normalized := normalizeConfigKey(name)
	for _, prefix := range backupFilterExemptConfigKeyPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// filterBackupConfigSet 是备份 configs 段过滤的共用骨架：凭据
// 明单、systemManaged（凭据与系统托管运行态）、运行时台账与已
// 注销键（功能下线，R2-E3-F03）四组恒排除；excludeDefaultSensitive
// 决定是否再叠加 PolicyForConfig 的默认敏感口径（导出面叠加、
// 导入面不叠加，见两包装函数注释）。
func filterBackupConfigSet(in map[string]string, excludeDefaultSensitive bool) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for name, value := range in {
		if IsSensitiveConfigKey(name) || IsSystemManagedConfigKey(name) || isBackupRuntimeExcludedConfigKey(name) || IsRetiredConfigKey(name) {
			continue
		}
		if excludeDefaultSensitive {
			if isBackupFilterExemptConfigKey(name) {
				out[name] = value
				continue
			}
			// RB4-01：导出面判据与读路径同源——PolicyForConfig
			// 判 sensitive（含未分类键的默认敏感）一律不进备份
			// 明文。此前仅靠默认落入 sensitive 的键（1.7.7 移除
			// 后存量残留的 github_proxy_*、管理员可写的任意未知
			// 键）会明文进备份，与读路径的默认敏感口径矛盾。
			if PolicyForConfig(name) == ConfigAccessSensitive {
				continue
			}
		}
		out[name] = value
	}
	return out
}

// FilterBackupConfigs 是备份**导出**面（本地备份导出）的
// configs 段过滤：在共用骨架上叠加
// PolicyForConfig 的默认敏感口径——未知/legacy 键不进备份明文；
// 设计豁免前缀族（pending_realm_settle:*）显式保留。
func FilterBackupConfigs(in map[string]string) map[string]string {
	return filterBackupConfigSet(in, true)
}

// filterRestoreConfigs 是备份**导入**面（importConfigs）的 configs
// 段过滤：只走共用骨架，不叠加默认敏感口径。备份文件已在管理员
// 手中，导入面的职责是阻止凭据/系统态/运行时台账被备份复活
// 覆盖，而不是分类未知键——跨版本备份里的未分类键必须原样恢复
// （R3 K5 钉死：custom_probe_key / ok_key / plain_key 均须落库），
// 其落库后的读出仍由读路径 PolicyForConfig 收口，不因恢复而扩大
// 暴露。若与导出面共用默认敏感口径，旧备份迁移会静默丢设置。
func filterRestoreConfigs(in map[string]string) map[string]string {
	return filterBackupConfigSet(in, false)
}

// restoreDroppedConfigKeys 返回导入面过滤（filterRestoreConfigs）
// 将剔除的键名清单（字典序）：判据与 filterBackupConfigSet(in,
// false) 的骨架恒同集——IsSensitiveConfigKey ||
// IsSystemManagedConfigKey || isBackupRuntimeExcludedConfigKey，
// 两处必须同步增删。B8-12：importConfigs 在过滤前取此清单进回
// 执点名——此前被剔键静默消失，管理员无从知道备份里的哪些设
// 置没回来（只有键名，绝不带值）。
func restoreDroppedConfigKeys(in map[string]string) []string {
	dropped := make([]string, 0)
	for name := range in {
		if IsSensitiveConfigKey(name) || IsSystemManagedConfigKey(name) || isBackupRuntimeExcludedConfigKey(name) {
			dropped = append(dropped, name)
		}
	}
	sort.Strings(dropped)
	return dropped
}

// restoreRetiredConfigKeys 返回导入载荷中命中的已注销键名清单
// （字典序）：判据与 filterBackupConfigSet 的注销集恒同源
// （IsRetiredConfigKey）。importConfigs 在过滤前取此清单单独点名
// ——注销键的处置理由是功能已下线，与 B8-12 的凭据/系统托管/台账
// 点名分开（R2-E3-F03），两份名单不重叠（注销键已退出全部策略
// 登记表，不会同时命中 restoreDroppedConfigKeys）。
func restoreRetiredConfigKeys(in map[string]string) []string {
	dropped := make([]string, 0)
	for name := range in {
		if IsRetiredConfigKey(name) {
			dropped = append(dropped, name)
		}
	}
	sort.Strings(dropped)
	return dropped
}

func normalizeConfigKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// NormalizeConfigKey 是 normalizeConfigKey 的导出形态，供写入口
// （R2 B14-001：config update / update-single）在落库前把键名归一到
// 与读路径/策略判定同一口径——此前写面只 TrimSpace，管理员写大写
// 变体返回成功却生成读不到的影子行、变体更新静默不生效。
func NormalizeConfigKey(name string) string {
	return normalizeConfigKey(name)
}
