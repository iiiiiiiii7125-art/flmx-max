# Node Management API

All node endpoints require admin privileges (role_id: 0).

## POST /api/v1/node/list

List all nodes with status information.

**Request:**
```json
{
  "page": 1,
  "pageSize": 20,
  "keyword": ""
}
```

**Response:**
```json
{
  "code": 0,
  "data": {
    "list": [
      {
        "id": 1,
        "name": "HK-Node-1",
        "secret": "abc123...",
        "server_ip": "1.2.3.4",
        "server_ip_v4": "1.2.3.4",
        "server_ip_v6": "2001:db8::1",
        "port": "1000-65535",
        "interface_name": "eth0",
        "http": 1,
        "tls": 1,
        "socks": 1,
        "tcp_listen_addr": "[::]",
        "udp_listen_addr": "[::]",
        "status": 1,
        "is_remote": 0,
        "version": "2.1.5",
        "created_at": 1706659200000,
        "updated_at": 1706659200000
      }
    ],
    "total": 1
  }
}
```

**Status Values:**
- `0` = Offline
- `1` = Online

**is_remote Values:**
- `0` = Local node (managed by this panel)
- `1` = Remote node (federation from another panel)

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
  "isRemote": 0,
  "remoteUrl": "",
  "remoteToken": ""
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| name | string | Yes | Node name |
| serverIp | string | Yes | Primary server IP (display) |
| serverIpV4 | string | No | IPv4 address |
| serverIpV6 | string | No | IPv6 address |
| port | string | No | Allowed port range (default: "1000-65535") |
| interfaceName | string | No | Network interface for traffic |
| http | number | No | Enable HTTP protocol (1/0) |
| tls | number | No | Enable TLS protocol (1/0) |
| socks | number | No | Enable SOCKS protocol (1/0) |
| tcpListenAddr | string | No | TCP listen address (default: "[::]") |
| udpListenAddr | string | No | UDP listen address (default: "[::]") |
| isRemote | number | No | Federation node (1/0) |
| remoteUrl | string | If isRemote=1 | Remote panel URL |
| remoteToken | string | If isRemote=1 | Federation token |

**Response:**
```json
{"code": 0, "msg": "success", "data": {"id": 2, "secret": "xyz789..."}}
```

## POST /api/v1/node/install

Generate installation command for a node.

**Request:**
```json
{"id": 2}
```

**Response:**
```json
{
  "code": 0,
  "data": {
    "command": "curl -fsSL https://panel.example.com/install.sh | bash -s -- --secret xyz789... --server https://panel.example.com"
  }
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

**Response:**
```json
{
  "code": 0,
  "data": {
    "updated": 5,
    "online": 4,
    "offline": 1
  }
}
```

## POST /api/v1/node/update-order

Reorder nodes (for display purposes).

**Request:**
```json
{
  "orders": [
    {"id": 1, "order": 0},
    {"id": 2, "order": 1}
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
    {"version": "2.1.5", "published_at": 1706659200000},
    {"version": "2.1.4", "published_at": 1706572800000}
  ]
}
```

## POST /api/v1/node/upgrade

Upgrade a single node agent.

**Request:**
```json
{
  "id": 2,
  "version": "2.1.5"
}
```

## POST /api/v1/node/batch-upgrade

Upgrade multiple node agents.

**Request:**
```json
{
  "ids": [1, 2, 3],
  "version": "2.1.5"
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
# 1. Create node
RESPONSE=$(curl -s -X POST "${FLMX_BASE_URL}/api/v1/node/create" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"name":"SG-Node-1","serverIp":"203.0.113.10"}')

NODE_ID=$(echo "$RESPONSE" | jq -r '.data.id')

# 2. Get install command
curl -s -X POST "${FLMX_BASE_URL}/api/v1/node/install" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{\"id\":${NODE_ID}}"

# 3. Run install command on target server (manual step)

# 4. Verify node is online
curl -s -X POST "${FLMX_BASE_URL}/api/v1/node/list" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{}' | jq ".data.list[] | select(.id == $NODE_ID) | {name, status}"
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
