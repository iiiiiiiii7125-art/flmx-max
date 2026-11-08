# System Configuration API

Manage system-wide settings. All configuration values are strings.

Access to each key is classified in the backend (`config_policy.go`) into
three tiers, enforced identically on every read/write path:

- **public** — readable anonymously.
- **authenticated** — readable by any logged-in user.
- **sensitive** — readable/writable by admins only. **Unclassified keys
  default to sensitive.**

Some keys are never returned by the config API at all, even to admins:
`license_key`, `license_machine_id`, `machine_fingerprint`,
`jwt_secret`, `cloudflare_secret_key`,
`notify_telegram_bot_token`, `notify_telegram_api_base`,
`notify_webhook_url`. Reading them yields 403 and `config/list` omits
them; they are written only through their dedicated surfaces
(deployment files) or write-only form fields.

> Only use keys that exist in the product (see the table below). The
> write endpoints accept unclassified keys and store them, but an
> unconsumed key changes nothing — the operation silently has no effect.

## POST /api/v1/config/get

Get a single configuration by name. **Not public**: this endpoint is
optional-auth — public-tier keys may be read anonymously,
authenticated-tier keys require a login, sensitive-tier keys require an
admin. (The only anonymous config endpoint is
`POST /api/v1/public/config/get`, which serves whitelisted public keys.)

**Request:**
```json
{"name": "app_name"}
```

**Response:**
```json
{
  "code": 0,
  "data": {
    "id": 1,
    "name": "app_name",
    "value": "My FLMX Panel",
    "time": 1706659200000
  }
}
```

## POST /api/v1/config/list

List all configurations. Login required; for non-admin callers the
sensitive-tier keys are filtered out, and the never-returned keys above
are omitted for everyone.

**Request:** `{}`

**Response:** `data` is a flat name → value map:
```json
{
  "code": 0,
  "data": {
    "app_name": "My FLMX Panel",
    "app_logo": "data:image/png;base64,...",
    "captcha_enabled": "true",
    "cloudflare_site_key": "0x4...",
    "monitor_retention_days": "7"
  }
}
```

## POST /api/v1/config/update

Batch update multiple configurations (admin only). The request body is
the flat key → value map itself:

**Request:**
```json
{
  "app_name": "New Panel Name",
  "captcha_enabled": "true"
}
```

Only include the keys you want to update.

Rules enforced on write:

- System-managed keys are rejected (`license_*`,
  `is_commercial`, `node_sub_token`, `machine_fingerprint`, …) — they
  have dedicated writers.
- Required keys (`jwt_secret`, `monitor_tunnel_quality_enabled`,
  `monitor_tunnel_quality_interval_sec`, `monitor_retention_days`)
  cannot be cleared.
- For clearable keys, an empty string deletes the key (reads then fall
  back to the default).

**Response:**
```json
{"code": 0, "msg": "操作成功"}
```

## POST /api/v1/config/update-single

Update a single configuration (same write rules as `config/update`;
sensitive keys require admin).

**Request:**
```json
{
  "name": "app_name",
  "value": "My Awesome Panel"
}
```

## GET /api/v1/announcement/get

Get the site announcement (login required).

**Method:** GET

**Response:**
```json
{
  "code": 0,
  "data": {
    "content": "System maintenance scheduled for tonight",
    "enabled": 1,
    "update_time": 1706659200000
  }
}
```

## POST /api/v1/announcement/update

Update the site announcement (admin only).

**Request:**
```json
{"content": "New announcement message", "enabled": 1}
```

---

## Common Configuration Keys

| Key | Tier | Description |
|-----|------|-------------|
| `app_name` | public | Panel display name (default `FLMX`) |
| `app_logo` | public | Logo (PNG data URL) |
| `app_favicon` | public | Favicon (PNG data URL) |
| `app_bg_image` | public | Default wallpaper (data URL) |
| `app_bg_image_light` | public | Light-mode wallpaper override |
| `login_bg_image` | public | Login page wallpaper (empty = follow main) |
| `hide_footer_brand` | public | `"true"` hides the footer brand |
| `cloudflare_site_key` | public | Cloudflare Turnstile site key |
| `cloudflare_secret_key` | sensitive, write-only | Turnstile secret key (never readable via this API) |
| `captcha_enabled` | authenticated | `"true"` enables login captcha |
| `panel_domain` | authenticated | Panel domain used when composing links |
| `subscription_front_urls` | authenticated | JSON array of subscription front origins |
| `monitor_tunnel_quality_enabled` | authenticated | `"true"`/`"false"` tunnel quality probing |
| `monitor_tunnel_quality_interval_sec` | authenticated | Probe interval, 1–3600 |
| `monitor_retention_days` | authenticated | Monitoring data retention, 1–400 (default 7) |
| `notify_enabled` | authenticated | `"true"` enables notifications |

There are **no global default-user keys**: per-user quota / expiry /
forward-count values are set per account on `user/create` (see
[users](users.md)).

---

## Example: Update Panel Name

```bash
curl -s -X POST "${FLMX_BASE_URL}/api/v1/config/update" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "app_name": "Awesome Traffic Panel"
  }'
```

## Example: Enable Cloudflare Turnstile Captcha

```bash
curl -s -X POST "${FLMX_BASE_URL}/api/v1/config/update" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "captcha_enabled": "true",
    "cloudflare_site_key": "0x4AAAAAAAAjq0JN9YQg",
    "cloudflare_secret_key": "0x4AAAAAAAAjq0JN9YQg_secret"
  }'
```
