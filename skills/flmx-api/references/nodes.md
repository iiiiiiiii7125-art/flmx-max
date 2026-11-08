# Node Management API

All node endpoints require admin privileges (role_id: 0).

## POST /api/v1/node/list

List all nodes with status information.

**Request:** `{}`

**Response:** `data` is a bare array of node objects (no wrapper). The
list DTO deliberately omits the node `secret`:

```json
{
  "code": 0,
  "data": [
    {
      "id": 1,
      "inx": 0,
      "name": "HK-Node-1",
      "remark": "",
      "expiryTime": null,
      "renewalCycle": "",
      "ip": "1.2.3.4",
      "serverIp": "1.2.3.4",
      "serverIpV4": "1.2.3.4",
      "serverIpV6": "2001:db8::1",
      "extraIPs": "",
      "port": "1000-65535",
      "interfaceName": "eth0",
      "http": 1,
      "tls": 1,
      "socks": 1,
      "tcpListenAddr": "[::]",
      "udpListenAddr": "[::]",
      "status": 1,
      "forwardMode": "agent",
      "version": "1.9.9",
      "expiryReminderDismissed": 0,
      "sshConfig": null
    }
  ]
}
```

**Status Values:**
- `0` = Offline
- `1` = Online

## POST /api/v1/node/create

Create a new node.

**Request:**
```json
{
  "name": "US-Node-1",
  "serverIp": "5.6.7.8",
  "serverIpV4": "5.6.7.8",
  "serverIpV6": "2001:db8::2",
  "port": "1000-65535",
  "interfaceName": "eth0",
  "http": 1,
  "tls": 1,
  "socks": 1,
  "tcpListenAddr": "[::]",
  "udpListenAddr": "[::]",
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| name | string | Yes | Node name |
| serverIp | string | Yes | Primary server IP (display) |
| serverIpV4 | string | No | IPv4 address |
| serverIpV6 | string | No | IPv6 address |
| extraIPs | string | No | Additional addresses |
| port | string | No | Allowed port range (default: "1000-65535") |
| interfaceName | string | No | Network interface for traffic |
| remark | string | No | Remark |
| expiryTime | number | No | Node expiry timestamp (ms) |
| renewalCycle | string | No | Renewal cycle label |
| http | number | No | Enable HTTP protocol (1/0) |
| tls | number | No | Enable TLS protocol (1/0) |
| socks | number | No | Enable SOCKS protocol (1/0) |
| tcpListenAddr | string | No | TCP listen address (default: "[::]") |
| udpListenAddr | string | No | UDP listen address (default: "[::]") |
| forwardMode | string | No | `"agent"` (default) or `"nftables"` |

The panel generates the node secret itself at creation.

**Response:** success envelope with no `data`:
```json
{"code": 0, "msg": "操作成功"}
```

To get the new node's ID, read it back from `/api/v1/node/list`.

## POST /api/v1/node/install

Generate installation command for a node.

**Request:**
```json
{"id": 2}
```

Optional fields: `channel` (release channel), `mode` (`"script"`
default, `"docker"` or `"docker-compose"`), `privileged` (container
modes only).

**Response:** `data` is the install command **string itself**:

```json
{
  "code": 0,
  "data": "curl -fsSL https://panel.example.com/install.sh | bash -s -- ..."
}
```

## POST /api/v1/node/update

Update node configuration.

**Request:** Same fields as create, with `id` field required.

```json
{
  "id": 2,
  "name": "US-Node-1-Updated",
  "serverIp": "5.6.7.8",
  "http": 1,
  "tls": 1,
  "socks": 0
}
```

## POST /api/v1/node/delete

Delete a node.

**Request:**
```json
{"id": 2}
```

## POST /api/v1/node/batch-delete

Delete multiple nodes.

**Request:**
```json
{"ids": [2, 3, 4]}
```

## POST /api/v1/node/check-status

Refresh and check status of all nodes.

**Request:** `{}`

**Response:** `data` is the full node list array (same shape as
`/api/v1/node/list`).

## POST /api/v1/node/update-order

Reorder nodes (for display purposes).

**Request:**
```json
{
  "nodes": [
    {"id": 1, "inx": 0},
    {"id": 2, "inx": 1}
  ]
}
```

## POST /api/v1/node/releases

List available FLMX agent releases.

**Request:** `{}`

**Response:**
```json
{
  "code": 0,
  "data": [
    {"version": "1.9.9", "name": "1.9.9", "publishedAt": "2026-11-05T01:25:00+08:00", "prerelease": false, "channel": "stable"}
  ]
}
```

## POST /api/v1/node/upgrade

Upgrade a single node agent.

**Request:**
```json
{
  "id": 2,
  "version": "1.9.9"
}
```

## POST /api/v1/node/batch-upgrade

Upgrade multiple node agents.

**Request:**
```json
{
  "ids": [1, 2, 3],
  "version": "1.9.9"
}
```

## POST /api/v1/node/rollback

Rollback node agent to previous version.

**Request:**
```json
{"id": 2}
```

## Example: Full Node Setup Workflow

```bash
# 1. Create node (response carries no data; read the ID back from the list)
curl -s -X POST "${FLMX_BASE_URL}/api/v1/node/create" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"name":"SG-Node-1","serverIp":"203.0.113.10"}'

NODE_ID=$(curl -s -X POST "${FLMX_BASE_URL}/api/v1/node/list" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{}' | jq -r '.data[] | select(.name == "SG-Node-1") | .id')

# 2. Get install command (data is the command string)
curl -s -X POST "${FLMX_BASE_URL}/api/v1/node/install" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{\"id\":${NODE_ID}}" | jq -r '.data'

# 3. Run install command on target server (manual step)

# 4. Verify node is online
curl -s -X POST "${FLMX_BASE_URL}/api/v1/node/list" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{}' | jq ".data[] | select(.id == $NODE_ID) | {name, status}"
```

## POST /api/v1/node/dismiss-expiry-reminder

Dismiss the expiry reminder for a node (the dismissed state survives backup /
restore).

## POST /api/v1/node/latency/targets

Get / set latency test targets. A node may pick its own targets; empty means the
global default. Limits: at most 10 targets, names up to 20 chars, one call may
cover at most 200 nodes.

## POST /api/v1/node/latency/probe

Trigger an immediate latency probe for a node.

## POST /api/v1/node/latency/history

A node's latency trend to each reference point over a time window: bucketed
samples with per-point average / max / loss. Samples are retained for at most
7 days. Request: `{"id": 1, "hours": 24}` (`hours` optional, default 24, max
168). Response `data`: `{since, until, points: [{name, samples: [{t, ms,
lossPct}], avgMs, maxMs, lossPct}]}`; a bucket's `ms` is `null` when every
probe in that bucket failed.

## POST /api/v1/node/tune

Apply or revert the 6 sysctl tuning groups (BBR, buffers, TCP, …) on a node.
Applied values persist across reboots.

## POST /api/v1/node/restart-schedule

Set one node's scheduled agent restart (a fuse, off by default). Request:
`{"id": 1, "enabled": 1, "time": "04:00", "jitterMinutes": 30}` — `time` is
the node's local time in `HH:MM` (00:00–23:59); `jitterMinutes` is a random
delay window of 0–720 minutes after that time. The panel stores the setting
(authoritative) and pushes it to the agent, which persists it as a systemd
timer on the node: only the agent service (`flmx_agent`) restarts, never the
host, and the timer fires even while the panel is unreachable. Response
`data`: `{enabled, time, jitterMinutes, pushed}` — `pushed=false` means the
node was offline (the setting is stored and re-pushed automatically on
reconnect) or `pushError` explains the failure. nftables nodes have no
agent service and are rejected.

## POST /api/v1/node/route

Get the node's cached return-route classification (CN2 / 163 / 4837 / 9929 /
CMI / CMIN2 …). Results refresh every 24h; failures retry after 6h.

## POST /api/v1/node/route/probe

Trigger an immediate return-route probe (requires raw-socket permission on the
node: root or CAP_NET_RAW).

## POST /api/v1/node/nftables/test

Dry-run / test nftables rule application for a node's forwards.

## POST /api/v1/node/nftables/reconcile

Force reconciliation between desired forward rules and the node's actual
nftables state.

## POST /api/v1/node/nftables/clear

Clear the node's panel-managed nftables rules.

## POST /api/v1/node/nftables/pending-deletes

List forwards stuck in pending-delete (status=2) awaiting data-plane cleanup,
with retry count, next retry time and last error. Admin only. Deleting a forward
while its node is offline marks it pending-delete; a background job retries with
1m/5m/30m/2h backoff, capped at 24h.

## POST /api/v1/node/traffic/month

Each node's whole-machine NIC traffic since the 1st of this month, in the
viewer's time zone (the node list's “本月网卡” figure).

**Request:**
```json
{"tzOffset": 480}
```
`tzOffset` is the viewer's UTC offset in minutes (the frontend sends
`-new Date().getTimezoneOffset()`); values outside ±14h are treated as 0.

**Response:** `data` is a map of node ID to `[inBytes, outBytes]`:
```json
{
  "code": 0,
  "data": {"1": [1073741824, 2147483648], "2": [536870912, 1073741824]}
}
```

These are whole-machine NIC counters, a different metric from tunnel
billing / subscription quota counters — the two never reconcile and must
not be mixed.
