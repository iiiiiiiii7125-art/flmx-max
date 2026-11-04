# Forward Management API

Forwards are port forwarding rules created by users on their assigned tunnels.

## POST /api/v1/forward/list

List forwards. Non-admin users see only their own forwards.

**Request:** `{}` (no pagination or filter parameters are consumed)

**Response:** `data` is a bare array of forward objects:

```json
{
  "code": 0,
  "data": [
    {
      "id": 1,
      "inx": 0,
      "userId": 2,
      "userName": "alice",
      "tunnelId": 1,
      "tunnelName": "HK-US-Tunnel",
      "tunnelTrafficRatio": 1,
      "name": "my-web-server",
      "inIp": "",
      "inPort": 10001,
      "remoteAddr": "192.168.1.100:80",
      "strategy": "fifo",
      "status": 1,
      "inFlow": 1073741824,
      "outFlow": 2147483648,
      "maxConn": 0,
      "ipMaxConn": 0,
      "createdTime": 1706659200000
    }
  ]
}
```

`status`: `0` = paused, `1` = running.

## POST /api/v1/forward/create

Create a new forward.

**Request:**
```json
{
  "name": "my-web-server",
  "tunnelId": 1,
  "remoteAddr": "192.168.1.100:80",
  "strategy": "fifo",
  "inPort": 0,
  "speedId": 0
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| name | string | Yes | Forward name |
| tunnelId | number | Yes | Tunnel to use |
| remoteAddr | string | Yes | Target address(es), comma-separated for load balancing |
| strategy | string | No | One of "fifo", "round", "rand", "hash" (default: "fifo"); anything else is rejected |
| inPort | number | No | Entry port (0 = auto-assign) |
| speedId | number | No | Speed limit rule ID (0 = no limit) |

**Strategy:**
- `fifo` = First target only
- `round` = Round-robin load balancing across targets
- `rand` = Random target per connection
- `hash` = Source-IP hash (sticky)

**Remote Address Format:**
- Single: `192.168.1.100:80`
- Multiple: `192.168.1.100:80,192.168.1.101:80,192.168.1.102:80`

**Response:** success envelope with no `data` — or, when the panel has
something to flag (e.g. a port adjustment), `data` is `{"warnings": [...]}`.
The assigned entry port is **not** in the response; read it back from
`/api/v1/forward/list`.

```json
{"code": 0, "msg": "操作成功"}
```

## POST /api/v1/forward/update

Update forward settings.

**Request:** Same as create, with `id` field required.

```json
{
  "id": 1,
  "name": "my-web-server-updated",
  "remoteAddr": "192.168.1.100:8080",
  "strategy": "round",
  "speedId": 2
}
```

## POST /api/v1/forward/delete

Delete a forward.

**Request:**
```json
{"id": 1}
```

## POST /api/v1/forward/force-delete

Force delete a forward (even if in use).

**Request:**
```json
{"id": 1}
```

## POST /api/v1/forward/pause

Pause a forward (stops traffic but keeps configuration).

**Request:**
```json
{"id": 1}
```

**Response:**
```json
{"code": 0, "msg": "操作成功"}
```

## POST /api/v1/forward/resume

Resume a paused forward.

**Request:**
```json
{"id": 1}
```

## POST /api/v1/forward/diagnose

Diagnose forward connectivity (TCP ping to target).

**Request:**
```json
{"id": 1}
```

**Response:** `data` is a per-hop results payload (same family as
tunnel diagnose): hop reachability/latency entries keyed by hop — there
is no single top-level `reachable` boolean.

## POST /api/v1/forward/update-order

Reorder forwards.

**Request:**
```json
{
  "forwards": [
    {"id": 1, "inx": 0},
    {"id": 2, "inx": 1}
  ]
}
```

## Batch Operations

### POST /api/v1/forward/batch-delete

```json
{"ids": [1, 2, 3]}
```

### POST /api/v1/forward/batch-pause

```json
{"ids": [1, 2, 3]}
```

### POST /api/v1/forward/batch-resume

```json
{"ids": [1, 2, 3]}
```

### POST /api/v1/forward/batch-redeploy

Recreate forwarding services on nodes.

```json
{"ids": [1, 2, 3]}
```

### POST /api/v1/forward/batch-change-tunnel

Move forwards to a different tunnel.

```json
{
  "ids": [1, 2, 3],
  "tunnelId": 5
}
```

## Traffic Units

| Field | Unit | Notes |
|-------|------|-------|
| inFlow | Bytes | Upload traffic |
| outFlow | Bytes | Download traffic |

Convert to GB: `inFlow / 1073741824`

## Example: Create Forward with Load Balancing

```bash
# Create forward with 3 backend servers
curl -s -X POST "${FLMX_BASE_URL}/api/v1/forward/create" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "web-cluster",
    "tunnelId": 1,
    "remoteAddr": "10.0.0.1:80,10.0.0.2:80,10.0.0.3:80",
    "strategy": "round"
  }'
```

## Example: Check Forward Status and Traffic

```bash
curl -s -X POST "${FLMX_BASE_URL}/api/v1/forward/list" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{}' | jq '.data[] | {
    name,
    tunnel: .tunnelName,
    entry_port: .inPort,
    target: .remoteAddr,
    status: (if .status == 1 then "running" else "paused" end),
    upload_gb: (.inFlow / 1073741824 | floor),
    download_gb: (.outFlow / 1073741824 | floor)
  }'
```

## POST /api/v1/forward/reset-flow

Reset a forward's traffic counters (inFlow / outFlow).

## POST /api/v1/forward/diagnose/stream

Streaming variant of forward diagnose: progress and hop results are pushed as a
stream instead of one blocking response.
