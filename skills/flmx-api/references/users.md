# User Management API

All user management endpoints require admin privileges (role_id: 0).

## POST /api/v1/user/list

List non-admin users (admin accounts are not included), with optional
keyword filtering and pagination.

**Request:**
```json
{
  "current": 1,
  "size": 20,
  "keyword": "search-term"
}
```

`keyword` matches against username. Pagination applies only when both
`current` and `size` are positive; omit them to get the full list.

**Response:** `data` is a bare array of user objects (no wrapper):

```json
{
  "code": 0,
  "data": [
    {
      "id": 2,
      "user": "alice",
      "name": "alice",
      "roleId": 1,
      "status": 1,
      "flow": 1000,
      "flowMiB": 0,
      "num": 10,
      "inFlow": 10737418240,
      "outFlow": 21474836480,
      "expTime": 1767225600000,
      "flowResetTime": 1,
      "maxConn": 0,
      "createdTime": 1706659200000,
      "updatedTime": 1706659200000
    }
  ]
}
```

## POST /api/v1/user/create

Create a new user.

**Request:**
```json
{
  "user": "username",
  "pwd": "password",
  "status": 1,
  "flow": 100,
  "num": 10,
  "expTime": 1767225600000,
  "flowResetTime": 1
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| user | string | Yes | Username, 1–100 chars (unique, case-insensitive) |
| pwd | string | Yes | Password, at least 6 characters |
| status | number | No | 1=active, 0=disabled (default: 1) |
| flow | number | No | Traffic quota in GB (default: 100; 0 = unlimited) |
| num | number | No | Max forwards allowed (default: 10) |
| expTime | number | No | Expiry timestamp in ms (default: now + 365 days) |
| flowResetTime | number | No | Monthly reset day: 0 = no reset, 1–31 = day of month (default: 1) |
| maxConn | number | No | Connection limit (0 = unlimited) |
| dailyQuotaGB / monthlyQuotaGB | number | No | Daily / monthly quota in GB (0 = none) |

Created users always get role_id 1 — this endpoint cannot create admins.

**Response:** success envelope with no `data`:
```json
{"code": 0, "msg": "操作成功"}
```

## POST /api/v1/user/update

Update user details.

**Request:**
```json
{
  "id": 2,
  "user": "new-username",
  "pwd": "new-password",
  "status": 1,
  "flow": 200,
  "num": 20,
  "expTime": 1767225600000,
  "flowResetTime": 15
}
```

Note: `pwd` is optional for updates. If omitted, password remains unchanged.

## POST /api/v1/user/delete

Delete a user (cascades to forwards and tunnel assignments).

**Request:**
```json
{"id": 2}
```

**Response:**
```json
{"code": 0, "msg": "操作成功"}
```

## POST /api/v1/user/reset

Reset a user's total traffic counters. `type` is a **number**:
`1` = user level (`id` is the user ID), `2` = assignment level
(`id` is the user-tunnel assignment row ID, not the tunnel ID).

**Request (User level):**
```json
{
  "id": 2,
  "type": 1
}
```

**Request (Assignment level):**
```json
{
  "id": 7,
  "type": 2
}
```

**Response:**
```json
{"code": 0, "msg": "操作成功"}
```

## Traffic Units

| Field | Unit | Conversion |
|-------|------|------------|
| flow | GB | Gigabytes |
| inFlow | Bytes | Divide by 1,073,741,824 for GB |
| outFlow | Bytes | Divide by 1,073,741,824 for GB |

## Example: Create User with 50GB Quota

```bash
curl -X POST "${FLMX_BASE_URL}/api/v1/user/create" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "user": "alice",
    "pwd": "SecurePass123!",
    "status": 1,
    "flow": 50,
    "num": 5,
    "expTime": 1735689600000,
    "flowResetTime": 1
  }'
```

## POST /api/v1/user/updatePassword

Change the current user's own password. A token flagged `PwdChangeRequired`
(forced change after reset / first login) may call only this endpoint.

## POST /api/v1/user/quota/reset

Reset a user's quota usage counters (daily / monthly) without touching the
account's total flow counters — see `/api/v1/user/reset` for the latter.

**Request:** `{"userId": 2, "scope": "daily"}` — `scope` is one of
`"daily"`, `"monthly"`, `"all"` (empty = `"all"`).
