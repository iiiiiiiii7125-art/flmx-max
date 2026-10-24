# Backup & Restore API

Export and import system data for backup, migration, or disaster recovery.

## POST /api/v1/backup/export

Export system data.

**Request:**
```json
{
  "types": ["users", "nodes", "tunnels", "forwards", "speed_limits", "groups"]
}
```

If `types` is empty or omitted, exports all data.

**Available types:**
- `users` - User accounts
- `nodes` - Node configurations
- `tunnels` - Tunnel configurations
- `forwards` - Forward rules
- `speed_limits` - Speed limit rules
- `groups` - User/tunnel groups and permissions
- `configs` - System configurations

**Response:**
```json
{
  "code": 0,
  "data": {
    "version": "2.1.5",
    "exportedAt": 1706659200000,
    "types": ["users", "nodes", "tunnels"],
    "users": [...],
    "nodes": [...],
    "tunnels": [...],
    "forwards": [...],
    "speedLimits": [...],
    "tunnelGroups": [...],
    "userGroups": [...],
    "groupPermissions": [...],
    "configs": {...}
  }
}
```

## POST /api/v1/backup/import

Import system data from a backup.

**Request:**
```json
{
  "version": "2.1.5",
  "exportedAt": 1706659200000,
  "types": ["users", "nodes"],
  "users": [...],
  "nodes": [...]
}
```

**Import Behavior:**
- Existing records are updated if IDs match
- New records are created for non-existent IDs
- Related entities must be included (e.g., forwards require tunnels)

**Response:**
```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "imported": {
      "users": 5,
      "nodes": 3,
      "tunnels": 10
    },
    "skipped": {
      "forwards": 2
    }
  }
}
```

## POST /api/v1/backup/restore

Alias for `/api/v1/backup/import`.

---

## Workflow: Full System Backup

```bash
# Export all data
curl -s -X POST "${FLMX_BASE_URL}/api/v1/backup/export" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{}' > backup-$(date +%Y%m%d).json

echo "Backup saved to backup-$(date +%Y%m%d).json"
```

## Workflow: Partial Export

```bash
# Export only users and tunnels
curl -s -X POST "${FLMX_BASE_URL}/api/v1/backup/export" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"types":["users","tunnels"]}' > partial-backup.json
```

## Workflow: Restore from Backup

```bash
# Import from backup file
curl -s -X POST "${FLMX_BASE_URL}/api/v1/backup/import" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d @backup-20260226.json | jq '.'
```

## Workflow: Migrate to New Panel

```bash
# On source panel
curl -s -X POST "${SOURCE_URL}/api/v1/backup/export" \
  -H "Authorization: ${SOURCE_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{}' > migration.json

# On target panel
curl -s -X POST "${TARGET_URL}/api/v1/backup/import" \
  -H "Authorization: ${TARGET_TOKEN}" \
  -H "Content-Type: application/json" \
  -d @migration.json
```

**Note:** After migration, you may need to:
1. Reinstall node agents with new panel URL
2. Update node secrets if they differ
3. Reassign federation tokens

## Compatibility aliases

The three backup endpoints are also registered under a doubled prefix for
backward compatibility with older clients; behavior is identical:

- `POST /api/v1/api/v1/backup/export`
- `POST /api/v1/api/v1/backup/import`
- `POST /api/v1/api/v1/backup/restore`

New integrations should use the canonical backup paths listed above.

## Gist 备份（1.4.6 新增，仅管理员）

把面板全量备份加密后上传到个人 GitHub 账号的私密 Gist，并可定时自动上传。
默认关闭；加密必填（明文上传不放行）；恢复走与本地文件导入同一尾链，恢复段为 16 段全集。

- `POST /api/v1/backup/gist/status` — Gist 配置与上次上传状态（令牌仅回 hint，不回原文）。
- `POST /api/v1/backup/gist/save` — 保存 Gist 配置（个人 classic token，需 gist 权限；间隔白名单 {0,6,12,24,168}，0 为关闭）。
- `POST /api/v1/backup/gist/upload` — 手动上传一次（未设加密密码时拒绝，零 GitHub 请求）。
- `POST /api/v1/backup/gist/versions` — 最近 10 个 Gist 版本。
- `POST /api/v1/backup/gist/restore` — 从指定 Gist 版本恢复（16 段全集；恢复前自动备份失败按 O7-010 继续导入）。

凭据（token、密码）只存面板配置，不进备份文件。真实 GitHub 端到端未在 1.4.6 门内实测（无真实令牌），见发版说明。
