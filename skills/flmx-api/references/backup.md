# Backup & Restore API

Export and import system data for backup, migration, or disaster recovery.
All backup endpoints are admin-only.

Response-shape note: `import` and `restore` return the
standard `{code, msg, data, ts}` envelope. `export` and the
`import-auto-backup` download return the **raw backup JSON as the response
body** (with a `Content-Disposition: attachment` header), not an envelope.

## Segment types

Backups are organized into 14 segment types (exact names, camelCase):

- `users` - User accounts
- `nodes` - Node configurations (also carries the `nodeSSHConfigs`
  top-level segment, see below)
- `tunnels` - Tunnel configurations
- `forwards` - Forward rules
- `userTunnels` - User-tunnel assignments
- `speedLimits` - Speed limit rules
- `configs` - System configurations (filtered: sensitive, system-managed
  and retired keys never enter a backup file; retired keys — config keys
  whose feature was removed, e.g. the `gist_*` keys — are also dropped
  on restore from old backups and named in `importWarnings`)
- `inbounds` - Node kernels, inbounds, relay links and traffic counter
  baselines (restored as one unit)
- `userInbounds` - Legacy per-inbound user assignments
- `announcements` - Announcements
- `monitorPermissions` - Monitoring permission grants
- `userSubscriptions` - Subscription assignments
- `serviceMonitors` - Service monitor definitions
- `passkeys` - WebAuthn passkey credentials (public-key material only:
  credential ID, public key, sign counter, attestation ceremony data —
  WebAuthn private keys never leave the authenticator). Rows whose
  user no longer exists are skipped on restore and counted in
  `skippedRows.passkeys`.

Unknown type names are rejected by name.

A backup file has 15 top-level segment keys: the 14 types above plus
`nodeSSHConfigs` (per-node SSH connection configs), which travels with
the `nodes` type.
The removed node-group segments (`tunnelGroups`, `userGroups`,
`permissions`) are never exported; in old backup files they are accepted
on import only as a decode shell — counted in the receipt's `*Ignored`
fields and named in `importWarnings`, never stored.

Treat backup files as credential-grade: they contain node secrets,
subscription tokens and SSH credentials.

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
  "nodeSSHConfigs": [...]
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
- Only one import may run at a time; a concurrent one is
  rejected.
- A backup whose `users` segment carries more than 1000 non-bcrypt
  password rows is rejected up front.
- After the write, the panel reconciles metering, invalidates config
  caches, re-pushes kernels and forwards, and cleans up revoked
  authorizations.

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

Before every import the panel stores one server-side
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
# Import all 14 segment types from a backup file
jq '. + {"types": ["users","nodes","tunnels","forwards","userTunnels","speedLimits","configs","inbounds","userInbounds","announcements","monitorPermissions","userSubscriptions","serviceMonitors","passkeys"]}' \
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
jq '. + {"types": ["users","nodes","tunnels","forwards","userTunnels","speedLimits","configs","inbounds","userInbounds","announcements","monitorPermissions","userSubscriptions","serviceMonitors","passkeys"]}' \
  migration.json > migration-import.json

curl -s -X POST "${TARGET_URL}/api/v1/backup/import" \
  -H "Authorization: ${TARGET_TOKEN}" \
  -H "Content-Type: application/json" \
  -d @migration-import.json
```

**Note:** Node secrets and subscription tokens
travel inside the backup, so restored nodes reconnect with
their existing secrets — do not treat the file as safe to share.

## Compatibility aliases

All backup endpoints are also registered under a doubled prefix for
backward compatibility with older clients; behavior is identical:

- `POST /api/v1/api/v1/backup/export`
- `POST /api/v1/api/v1/backup/import`
- `POST /api/v1/api/v1/backup/restore`
- `GET /api/v1/api/v1/backup/import-auto-backup`
- `GET /api/v1/api/v1/backup/import-auto-backup/status`

New integrations should use the canonical backup paths listed above.
