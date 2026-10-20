// 敏感配置键清单：这些键不得持久化到 localStorage；万一已缓存，
// 登出（clearSession）时必须一并清除。
//
// 单一真源在后端：go-backend/internal/store/repo/config_policy.go 的
// sensitiveConfigKeys。后端新增敏感键时必须同步补进本清单，否则该键
// 会被前端缓存落盘、且登出后残留（F-047 教训：前端曾长期停留在 5 项，
// 落后后端 9 项，缺 license_expiry / node_sub_token /
// notify_telegram_bot_token / notify_webhook_url）。
// 本文件保持零依赖（叶子模块），避免 config/site 与 utils/session
// 之间出现循环导入。
export const SENSITIVE_CONFIG_KEYS = new Set([
  "jwt_secret",
  "license_key",
  "license_expiry",
  "license_machine_id",
  "machine_fingerprint",
  "cloudflare_secret_key",
  "node_sub_token",
  "notify_telegram_bot_token",
  "notify_webhook_url",
]);

// 与 config/site.ts 的 CACHE_PREFIX 保持一致（配置缓存键前缀）。
const CONFIG_CACHE_PREFIX = "vite_config_";

export const purgeSensitiveConfigCache = () => {
  SENSITIVE_CONFIG_KEYS.forEach((key) => {
    localStorage.removeItem(CONFIG_CACHE_PREFIX + key);
  });
};
