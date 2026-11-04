# Federation / Clustering API

Federation allows sharing nodes between FLMX panels. One panel can share nodes, and another panel can use them as remote nodes.

## Share Management (Admin)

### POST /api/v1/federation/share/list

List all peer shares.

**Request:** `{}`

**Response:** `data` is a bare array. Each item is the share record
(with its `token` — this list is the only place the token is returned)
merged with runtime usage fields:

```json
{
  "code": 0,
  "data": [
    {
      "id": 1,
      "name": "Share-to-Partner",
      "nodeId": 1,
      "token": "share-token-abc123",
      "maxBandwidth": 107374182400,
      "expiryTime": 1767225600000,
      "portRangeStart": 10000,
      "portRangeEnd": 20000,
      "allowedDomains": "example.com,api.example.com",
      "allowedIps": "10.0.0.0/8,192.168.0.0/16",
      "currentFlow": 0,
      "isActive": true,
      "createdTime": 1706659200000,
      "updatedTime": 1706659200000,
      "usedPorts": [10001],
      "usedPortDetails": [],
      "activeRuntimeNum": 1
    }
  ]
}
```

(`relayUsername` / `relayPassword` are present in the shape but always
returned blank on this admin face.)

### POST /api/v1/federation/share/create

Create a peer share (share a node with another panel).

**Request:**
```json
{
  "name": "Share-to-Partner",
  "nodeId": 1,
  "maxBandwidth": 107374182400,
  "expiryTime": 1767225600000,
  "portRangeStart": 10000,
  "portRangeEnd": 20000,
  "allowedDomains": "example.com,api.example.com",
  "allowedIps": "10.0.0.0/8,192.168.0.0/16"
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| name | string | Yes | Share name |
| nodeId | number | Yes | Node to share |
| maxBandwidth | number | No | Max traffic in bytes (0 = unlimited) |
| expiryTime | number | No | Expiry timestamp in ms (0 = never) |
| portRangeStart | number | No | Allowed port range start |
| portRangeEnd | number | No | Allowed port range end |
| allowedDomains | string | No | Comma-separated domains |
| allowedIps | string | No | Comma-separated IPs/CIDRs |

**Response:** success envelope with **no `data`** — the generated
`token` is NOT returned here. Read it back from
`/api/v1/federation/share/list` (match by name), then give it to the
remote panel.

```json
{"code": 0, "msg": "操作成功"}
```

### POST /api/v1/federation/share/update

Update a peer share.

**Request:** Same as create, with `id` field required.

### POST /api/v1/federation/share/delete

Delete a peer share.

**Request:**
```json
{"id": 1}
```

### POST /api/v1/federation/share/reset-flow

Reset traffic counter for a share.

**Request:**
```json
{"id": 1}
```

### POST /api/v1/federation/share/remote-usage/list

List remote node usage statistics.

**Request:** `{}`

---

## Federation Runtime (Peer-to-Peer)

These endpoints use **Bearer token authentication** (different from JWT).

### POST /api/v1/federation/connect

Connect to a remote panel and get share info.

**Headers:**
```
Authorization: Bearer <share-token>
```

**Request:** no body.

**Response:** `data` is the remote node descriptor:

```json
{
  "code": 0,
  "data": {
    "shareId": 1,
    "shareName": "Share-to-Partner",
    "nodeId": 1,
    "nodeName": "HK-Node-1",
    "serverIp": "1.2.3.4",
    "status": 1,
    "maxBandwidth": 107374182400,
    "currentFlow": 0,
    "expiryTime": 1767225600000,
    "portRangeStart": 10000,
    "portRangeEnd": 20000,
    "relayUsername": "",
    "relayPassword": ""
  }
}
```

### POST /api/v1/federation/tunnel/create

Create a federation tunnel on the remote node.

**Headers:**
```
Authorization: Bearer <share-token>
```

**Request:**
```json
{
  "protocol": "tcp",
  "remotePort": 15000,
  "target": "10.0.0.1:80"
}
```

**Response:** `data` = `{"tunnelId": 42}`.

### POST /api/v1/federation/runtime/reserve-port

Reserve a port on the remote node.

**Request:**
```json
{
  "resourceKey": "tunnel:1:entry",
  "protocol": "tcp",
  "requestedPort": 15000
}
```

**Response:** `data` = `{"reservationId": "...", "bindingId": 0, "allocatedPort": 15000}`.

### POST /api/v1/federation/runtime/apply-role

Apply for a role (entry/chain/exit) on the remote node.

**Request:**
```json
{
  "reservationId": "...",
  "resourceKey": "tunnel:1:entry",
  "role": "exit",
  "protocol": "tcp",
  "strategy": "round",
  "targets": [{"host": "10.0.0.1", "port": 80, "protocol": "tcp"}]
}
```

**Response:** `data` = `{"bindingId": 7, "reservationId": "...", "allocatedPort": 15000}`.

### POST /api/v1/federation/runtime/release-role

Release a role on the remote node.

**Request:**
```json
{
  "bindingId": 7,
  "reservationId": "...",
  "resourceKey": "tunnel:1:entry"
}
```

### POST /api/v1/federation/runtime/diagnose

TCP ping diagnostics from remote node to target.

**Request:**
```json
{
  "ip": "10.0.0.1",
  "port": 80,
  "count": 4,
  "timeout": 5,
  "protocol": "tcp"
}
```

### POST /api/v1/federation/runtime/command

Execute a command on the remote node.

**Request:**
```json
{
  "commandType": "status",
  "data": {}
}
```

**Response:** `data` = `{"type": "...", "success": true, "message": "...", "data": {...}}`.

---

## Node Import (Admin)

### POST /api/v1/federation/node/import

Import a remote node from another panel.

**Request:**
```json
{
  "remoteUrl": "https://other-panel.example.com",
  "token": "share-token-abc123"
}
```

Only `remoteUrl` and `token` are consumed (the node name comes from the
remote panel). This creates a node with `isRemote: 1`.

---

## Workflow: Share Node with Another Panel

**On the sharing panel (Panel A):**

```bash
# 1. Create a share (response carries no data)
curl -s -X POST "${FLMX_BASE_URL}/api/v1/federation/share/create" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Share-HK-Node",
    "nodeId": 1,
    "portRangeStart": 10000,
    "portRangeEnd": 20000,
    "allowedIps": "0.0.0.0/0"
  }' > /dev/null

# 2. Read the generated token back from the share list
SHARE_TOKEN=$(curl -s -X POST "${FLMX_BASE_URL}/api/v1/federation/share/list" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{}' | jq -r '.data[] | select(.name == "Share-HK-Node") | .token')
echo "Share Token: $SHARE_TOKEN"
echo "Panel URL: ${FLMX_BASE_URL}"
```

**On the receiving panel (Panel B):**

```bash
# 3. Import the remote node
curl -s -X POST "${FLMX_BASE_URL}/api/v1/federation/node/import" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "remoteUrl": "https://panel-a.example.com",
    "token": "share-token-abc123"
  }'

# 4. Use the remote node in tunnels like a local node
curl -s -X POST "${FLMX_BASE_URL}/api/v1/tunnel/create" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Federated-Tunnel",
    "type": 1,
    "inNodeId": [1],
    "outNodeId": [2]
  }'
```
