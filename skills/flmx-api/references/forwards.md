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
      "remoteAddr": "203.0.113.100:80",
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
  "remoteAddr": "203.0.113.100:80",
  "strategy": "fifo",
  "inPort": 0
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
| speedId | number | No | Speed limit rule ID (0 = no limit). Admin-only: ordinary users are rejected whenever the key is present, even with value 0 — omit it unless you are an admin. |

**Strategy:**
- `fifo` = First target only
- `round` = Round-robin load balancing across targets
- `rand` = Random target per connection
- `hash` = Source-IP hash (sticky)

**Remote Address Format:**
- Single: `203.0.113.100:80`
- Multiple: `203.0.113.100:80,203.0.113.101:80,203.0.113.102:80`

> Intranet targets are NOT copy-pasteable for ordinary users: the
> panel's SSRF guard (`IsSafeRemoteAddr`) rejects private/loopback/
> link-local targets with **403** for non-admin callers unless an
> admin has enabled `allow_local_remote_addr`. Admins are exempt.
> The examples above use TEST-NET-3 documentation addresses
> (`203.0.113.0/24`, RFC 5737) so they pass the guard's format
> checks — substitute your real public target before sending.

**Response:** success envelope with no `data` — or, when the panel has
something to flag (e.g. a port adjustment), `data` is `{"warnings": [...]}`.
The assigned entry port is **not** in the response; read it back from
`/api/v1/forward/list`.

```json
{"code": 0, "msg": "操作成功"}
```

## POST /api/v1/forward/update

Update forward settings.

**Request:** Same as create, with `id` field required. (`speedId` is admin-only — see create.)

```json
{
  "id": 1,
  "name": "my-web-server-updated",
  "remoteAddr": "203.0.113.100:8080",
  "strategy": "round"
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
{"forwardId": 1}
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
    "remoteAddr": "203.0.113.100:80,203.0.113.101:80,203.0.113.102:80",
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
