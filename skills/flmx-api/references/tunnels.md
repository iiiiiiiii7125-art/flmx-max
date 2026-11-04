# Tunnel Management API

Tunnels define the forwarding path: entry node(s) → (chain nodes) → exit node(s).

## POST /api/v1/tunnel/list

List all tunnels.

**Request:** `{}`

**Response:** `data` is a bare array of tunnel objects. The node fields
are arrays of **node reference objects** (`{id, name, ...}`), not bare
IDs:

```json
{
  "code": 0,
  "data": [
    {
      "id": 1,
      "inx": 0,
      "name": "HK-US-Tunnel",
      "type": 1,
      "flow": 1,
      "trafficRatio": 1,
      "status": 1,
      "ipPreference": "ipv4",
      "engine": "",
      "inIp": "",
      "probeTargetHost": "",
      "probeTargetPort": 0,
      "inNodeId": [{"id": 1, "name": "HK-Node-1"}],
      "chainNodes": [],
      "outNodeId": [{"id": 2, "name": "US-Node-1"}],
      "createdTime": 1706659200000
    }
  ]
}
```

## POST /api/v1/tunnel/get

Get a single tunnel by ID.

**Request:**
```json
{"id": 1}
```

## POST /api/v1/tunnel/create

Create a new tunnel.

**Request:**
```json
{
  "name": "JP-SG-Tunnel",
  "type": 1,
  "flow": 1,
  "trafficRatio": 1,
  "status": 1,
  "ipPreference": "ipv4",
  "inIp": "",
  "inNodeId": [3],
  "chainNodeId": [],
  "outNodeId": [4]
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| name | string | Yes | Tunnel name |
| type | number | Yes | 1=port forward, 2=tunnel forward |
| flow | number | No | Traffic multiplier (default: 1) |
| trafficRatio | number | No | Traffic ratio (default: 1) |
| status | number | No | 1=active, 0=disabled (default: 1) |
| ipPreference | string | No | "ipv4", "ipv6", or "" (both) |
| engine | string | No | Forwarding engine: "" / "gost" (panel forwarder, default) or "realm" (see below) |
| inIp | string | No | Custom entry IP |
| inNodeId | number[] | Yes | Entry node IDs |
| chainNodeId | number[] | No | Chain/relay node IDs |
| outNodeId | number[] | Yes | Exit node IDs |

**Tunnel Types:**
- `1` = Port Forward: Simple port-to-port forwarding
- `2` = Tunnel Forward: Multi-hop tunnel forwarding

**Forwarding engine (`engine`):** `""` / `"gost"` runs the tunnel's forwards
with the node agent's forwarder (full feature set, per-forward rate / connection
limits). `"realm"` runs them with realm on the entry nodes instead — port
forward tunnels (type 1) only, entry nodes must not be remote or
nftables-mode, no per-forward rate / connection limits, balancing is round
robin or IP hash only. Billing differs: realm forward traffic is counted
toward user quota; panel-forwarder (gost) port-forward traffic is not.

**Response:** success envelope with no `data` (read the new tunnel back
from `/api/v1/tunnel/list` if you need its ID):
```json
{"code": 0, "msg": "操作成功"}
```

## POST /api/v1/tunnel/update

Update tunnel configuration.

**Request:** Same as create, with `id` field required.

## POST /api/v1/tunnel/delete

Delete a tunnel.

**Request:**
```json
{"id": 2}
```

## POST /api/v1/tunnel/batch-delete

Delete multiple tunnels.

**Request:**
```json
{"ids": [2, 3]}
```

## POST /api/v1/tunnel/diagnose

Diagnose tunnel connectivity.

**Request:**
```json
{"id": 1}
```

**Response:** `data` carries the diagnosis context and a per-hop
`results` array (each hop reports its own reachability/latency fields):

```json
{
  "code": 0,
  "data": {
    "tunnelName": "HK-US-Tunnel",
    "tunnelType": 1,
    "timestamp": 1706659200000,
    "results": []
  }
}
```

## POST /api/v1/tunnel/update-order

Reorder tunnels.

**Request:**
```json
{
  "tunnels": [
    {"id": 1, "inx": 0},
    {"id": 2, "inx": 1}
  ]
}
```

## POST /api/v1/tunnel/batch-redeploy

Redeploy multiple tunnels (recreate forwarding services).

**Request:**
```json
{"ids": [1, 2, 3]}
```

---

## User-Tunnel Assignment

These endpoints manage which users can use which tunnels.

### POST /api/v1/tunnel/user/tunnel

List tunnels visible to the current user (or all tunnels for admin).

**Request:** `{}`

**Response:** a bare array of lightweight tunnel descriptors
(`portRangeMin` / `portRangeMax` are present only when the tunnel
restricts its port range):

```json
{
  "code": 0,
  "data": [
    {
      "id": 1,
      "name": "HK-US-Tunnel"
    }
  ]
}
```

### POST /api/v1/tunnel/user/list

List user-tunnel assignments for one user (admin only).

**Request:**
```json
{"userId": 2}
```

**Response:** `data` is a bare array of assignment objects:

```json
{
  "code": 0,
  "data": [
    {
      "id": 1,
      "userId": 2,
      "tunnelId": 1,
      "tunnelName": "HK-US-Tunnel",
      "status": 1,
      "flow": 50,
      "flowMiB": 0,
      "num": 0,
      "inFlow": 1073741824,
      "outFlow": 2147483648,
      "expTime": 0,
      "flowResetTime": 1,
      "speedId": 0,
      "speedLimitName": null
    }
  ]
}
```

### POST /api/v1/tunnel/user/assign

Assign a tunnel to a user.

**Request:**
```json
{
  "userId": 2,
  "tunnelId": 1,
  "flow": 50,
  "expTime": 0,
  "speedId": 0
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| userId | number | Yes | User ID |
| tunnelId | number | Yes | Tunnel ID |
| flow | number | No | Traffic quota for this tunnel in GB (0 = unlimited) |
| flowMiB | number | No | Quota in MiB (takes precedence when > 0) |
| expTime | number | No | Expiry for this assignment (ms, 0=never) |
| flowResetTime | number | No | Monthly reset day for this assignment |
| num | number | No | Max forwards on this tunnel for the user |
| speedId | number | No | Speed limit rule ID |
| status | number | No | 1=enabled, 0=disabled for this user |

### POST /api/v1/tunnel/user/batch-assign

Batch assign tunnels to a user.

**Request:**
```json
{
  "userId": 2,
  "tunnels": [
    {"tunnelId": 1, "speedId": 0},
    {"tunnelId": 2, "speedId": 0}
  ]
}
```

### POST /api/v1/tunnel/user/remove

Remove a tunnel assignment from a user. `id` is the **assignment row
ID** (from `tunnel/user/list`), not a user/tunnel pair.

**Request:**
```json
{"id": 1}
```

### POST /api/v1/tunnel/user/update

Update user-tunnel assignment settings.

**Request:**
```json
{
  "id": 1,
  "flow": 100,
  "expTime": 1767225600000,
  "speedId": 2
}
```

## Example: Assign Tunnel to User

```bash
# 1. Create tunnel (response carries no data; read the ID back from the list)
curl -s -X POST "${FLMX_BASE_URL}/api/v1/tunnel/create" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"name":"Test-Tunnel","type":1,"inNodeId":[1],"outNodeId":[2]}' > /dev/null

TUNNEL_ID=$(curl -s -X POST "${FLMX_BASE_URL}/api/v1/tunnel/list" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{}' | jq -r '.data[] | select(.name == "Test-Tunnel") | .id')

# 2. Assign to user with 30GB quota
curl -s -X POST "${FLMX_BASE_URL}/api/v1/tunnel/user/assign" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{\"userId\":2,\"tunnelId\":${TUNNEL_ID},\"flow\":30}"
```

## POST /api/v1/tunnel/delete-preview

Preview what deleting a tunnel would affect (its forwards and assignments)
before actually deleting.

## POST /api/v1/tunnel/delete-with-forwards

Delete a tunnel together with the forwards running on it, in one operation.

## POST /api/v1/tunnel/batch-delete-preview

Preview the impact of deleting multiple tunnels.

## POST /api/v1/tunnel/batch-delete-with-forwards

Delete multiple tunnels together with their forwards.

## POST /api/v1/tunnel/diagnose/stream

Streaming variant of tunnel diagnose (per-hop results pushed incrementally).
