# Backup & Restore API

Export and import system data for backup, migration, or disaster recovery.
All backup endpoints are admin-only.

Response-shape note: `import`, `restore` and the Gist endpoints return the
standard `{code, msg, data, ts}` envelope. `export` and the
`import-auto-backup` download return the **raw backup JSON as the response
body** (with a `Content-Disposition: attachment` header), not an envelope.

## Segment types

Backups are organized into 13 segment types (exact names, camelCase):

- `users` - User accounts
- `nodes` - Node configurations (also carries the `nodeSSHConfigs` and
  `peerShares` top-level segments, see below)
- `tunnels` - Tunnel configurations
- `forwards` - Forward rules
- `userTunnels` - User-tunnel assignments
- `speedLimits` - Speed limit rules
- `configs` - System configurations (filtered: sensitive, system-managed
  and Gist keys never enter a backup file)
- `inbounds` - Node kernels, inbounds, relay links and traffic counter
  baselines (restored as one unit)
- `userInbounds` - Legacy per-inbound user assignments
- `announcements` - Announcements
- `monitorPermissions` - Monitoring permission grants
- `userSubscriptions` - Subscription assignments
- `serviceMonitors` - Service monitor definitions

Unknown type names are rejected by name.

A backup file has 15 top-level segment keys: the 13 types above plus
`nodeSSHConfigs` (per-node SSH connection configs) and `peerShares`
(federation share definitions), which travel with the `nodes` type.
The removed node-group segments (`tunnelGroups`, `userGroups`,
`permissions`) are never exported; in old backup files they are accepted
on import only as a decode shell — counted in the receipt's `*Ignored`
fields and named in `importWarnings`, never stored.

Treat backup files as credential-grade: they contain node secrets,
subscription tokens, SSH credentials and federation share definitions.

## POST /api/v1/backup/export

Export system data. The response body is the backup JSON itself.

**Request:**
```json
{
  "types": ["users", "nodes", "tunnels", "forwards", "speedLimits"]
}
```

If `types` is empty or omitted, exports all data.

**Response (raw backup JSON, no envelope):**
```json
{
  "version": "1.0",
  "exportedAt": 1706659200000,
  "users": [...],
  "nodes": [...],
  "tunnels": [...],
  "forwards": [...],
  "speedLimits": [...],
  "configs": {...},
  "inbounds": {...},
  "nodeSSHConfigs": [...],
  "peerShares": [...]
}
```

## POST /api/v1/backup/import

Import system data from a backup.

**Request:** the backup JSON plus a `types` field listing which segments
to import. `types` is **required** — an import without it is rejected.
`version` must be `"1.0"`.

```json
{
  "version": "1.0",
  "exportedAt": 1706659200000,
  "types": ["users", "nodes"],
  "users": [...],
  "nodes": [...]
}
```

**Import behavior:**
- Runs as a single transaction; records are upserted by ID.
- Before any write, the panel saves a pre-import snapshot on the server
  (latest one only, kept 24 hours; the snapshot bytes never appear in the
  receipt). If snapshot generation fails, the import still proceeds and
  the receipt carries a warning.
- Only one import / Gist restore may run at a time; a concurrent one is
  rejected.
- A backup whose `users` segment carries more than 1000 non-bcrypt
  password rows is rejected up front.
- After the write, the panel reconciles metering, invalidates config
  caches, re-pushes kernels and forwards, and cleans up revoked
  authorizations (the same tail chain as a Gist restore).

**Response:** envelope whose `data` is a flat `ImportResult` — one
`*Imported` counter per segment (plus sub-counters for the `inbounds`
segment), the four `*Ignored` counters for removed segments, and
disclosure fields:

```json
{
  "code": 0,
  "msg": "操作成功",
  "data": {
    "usersImported": 5,
    "nodesImported": 3,
    "tunnelsImported": 10,
    "forwardsImported": 8,
    "tunnelGroupsIgnored": 0,
    "userGroupsIgnored": 0,
    "permissionsIgnored": 0,
    "inboundNodeGroupsIgnored": 0,
    "userSubscriptionsImported": 4,
    "userSubscriptionsSkipped": 1,
    "skippedRows": {"forwards": 2},
    "importWarnings": ["..."],
    "repushNodeIds": [1, 2],
    "repushFailedNodeIds": []
  }
}
```

Rows skipped because a referenced entity does not exist are counted in
`skippedRows` (keyed by segment name) and named in `importWarnings` —
never silently dropped.

## POST /api/v1/backup/restore

Alias for `/api/v1/backup/import` (same handler, same contract).

## Pre-import snapshot (import-auto-backup)

Before every import / Gist restore the panel stores one server-side
snapshot of the current database (single file, 24-hour validity).

### GET /api/v1/backup/import-auto-backup

Download the snapshot (raw backup JSON body). Admin only; returns a 404
envelope when no snapshot exists.

### GET /api/v1/backup/import-auto-backup/status

```json
{
  "code": 0,
  "data": {"exists": true, "expiresAt": 1706745600}
}
```

`expiresAt` is a Unix timestamp in **seconds** (0 when no snapshot).

---

## Workflow: Full System Backup

```bash
# Export all data (response body is the backup file itself)
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

The import body is the backup file **plus** the `types` field (the
exported file does not carry one):

```bash
# Import all 13 segment types from a backup file
jq '. + {"types": ["users","nodes","tunnels","forwards","userTunnels","speedLimits","configs","inbounds","userInbounds","announcements","monitorPermissions","userSubscriptions","serviceMonitors"]}' \
  backup-20260226.json > import-body.json

curl -s -X POST "${FLMX_BASE_URL}/api/v1/backup/import" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d @import-body.json | jq '.'
```

## Workflow: Migrate to New Panel

```bash
# On source panel
curl -s -X POST "${SOURCE_URL}/api/v1/backup/export" \
  -H "Authorization: ${SOURCE_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{}' > migration.json

# On target panel (add the types field as in the restore workflow)
jq '. + {"types": ["users","nodes","tunnels","forwards","userTunnels","speedLimits","configs","inbounds","userInbounds","announcements","monitorPermissions","userSubscriptions","serviceMonitors"]}' \
  migration.json > migration-import.json

curl -s -X POST "${TARGET_URL}/api/v1/backup/import" \
  -H "Authorization: ${TARGET_TOKEN}" \
  -H "Content-Type: application/json" \
  -d @migration-import.json
```

**Note:** Node secrets, subscription tokens and federation share
definitions travel inside the backup, so restored nodes reconnect with
their existing secrets — do not treat the file as safe to share.

## Compatibility aliases

All backup endpoints are also registered under a doubled prefix for
backward compatibility with older clients; behavior is identical:

- `POST /api/v1/api/v1/backup/export`
- `POST /api/v1/api/v1/backup/import`
- `POST /api/v1/api/v1/backup/restore`
- `GET /api/v1/api/v1/backup/import-auto-backup`
- `GET /api/v1/api/v1/backup/import-auto-backup/status`
- `POST /api/v1/api/v1/backup/gist/status`
- `POST /api/v1/api/v1/backup/gist/save`
- `POST /api/v1/api/v1/backup/gist/upload`
- `POST /api/v1/api/v1/backup/gist/versions`
- `POST /api/v1/api/v1/backup/gist/restore`

New integrations should use the canonical backup paths listed above.

## Gist 备份（仅管理员，全部 POST）

把面板全量备份加密后上传到个人 GitHub 账号的私密 Gist，并可定时自动上传。
默认关闭；加密必填（未设加密密码时上传被拒绝、零 GitHub 请求）；恢复走与
本地文件导入同一条尾链，恢复段集与本地导入的全量段集相同（13 个段类型，
`nodeSSHConfigs`、`peerShares` 随 `nodes` 段往返）。

加密方式：scrypt（N=32768, r=8, p=1）派生密钥 + AES-256-GCM；信封标记为
`flmx`（读取时兼容旧标记 `flyx`）。单份备份明文超过 20 MB 拒绝上传；
内容未变化时跳过上传、不产生新版本。凭据（token、密码）只存面板配置，
不进备份文件——导出时 `gist_*` 等配置键会被过滤掉。

- `POST /api/v1/backup/gist/status` — Gist 配置与上次上传状态。响应
  `data`：`{configured, tokenHint, encrypted, intervalHours, gistUrl,
  lastAt, lastUpload, lastError}`（令牌仅回 hint，不回原文）。
- `POST /api/v1/backup/gist/save` — 保存 Gist 配置。请求体：
  `{"token": "...", "clearToken": false, "password": "...",
  "clearPassword": false, "intervalHours": 24}`。token/password 留空 =
  保留已存值；`clearToken` / `clearPassword` = 清除（清除令牌同时把间隔
  归零）。个人 classic token，需 gist 权限，保存前会先向 GitHub 校验；
  间隔白名单 {0,6,12,24,168}，0 为关闭；令牌、密码各限 255 字。
- `POST /api/v1/backup/gist/upload` — 手动上传一次。响应 `data`：
  `{"result": {"uploaded": true, "message": "..."}, "status": {...}}`。
- `POST /api/v1/backup/gist/versions` — 最近 10 个 Gist 版本。响应
  `data`：`{"versions": [{"version": "...", "committedAt": 1706659200000}]}`。
- `POST /api/v1/backup/gist/restore` — 从指定 Gist 版本恢复。请求体：
  `{"version": "...", "password": "..."}`（password 为该版本上传时的加密
  密码）。响应与本地导入相同（`ImportResult`）；恢复前同样会先生成服务
  端快照，快照失败只警告续行、不中断恢复。
