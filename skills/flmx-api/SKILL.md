---
name: flmx-api
description: Operate FLMX traffic forwarding management system via REST API. Supports user/node/tunnel/forward management, federation clustering, and traffic monitoring. Use when user wants to manage FLMX panel programmatically or via natural language.
metadata:
  author: FLMX Team
  version: "1.3.4"
  requires_env:
    - FLMX_BASE_URL
    - FLMX_USERNAME
    - FLMX_PASSWORD
---

# FLMX API Operations

Operate FLMX panel through REST API. All endpoints use POST method and return JSON with `{code, msg, data, ts}` envelope.

> **Versioning:** this skill's version follows the FLMX product release line
> (skill `1.1.1` ↔ panel `1.1.1`). Releases are tagged `skill-v<version>`.
> The endpoint list is checked against the backend route table by
> `scripts/check-doc-consistency.sh` — if you add a route, add it here too.

## Supported AI Tools

| Tool | Installation | Notes |
|------|--------------|-------|
| **OpenCode** | `npm i -g @flmx/skill-api` or `ln -s . ~/.agents/skills/flmx-api` | Auto-loads from `~/.agents/skills/` |
| **OpenClaw** | Same as OpenCode | Compatible skill format |
| **Claude Code** | Copy SKILL.md to CLAUDE.md or `~/.claude/CLAUDE.md` | Uses context file instead of skills |

## Prerequisites

Set environment variables before starting:

```bash
export FLMX_BASE_URL="https://your-panel.example.com"
export FLMX_USERNAME="admin"
export FLMX_PASSWORD="your-password"
```

**Security tip:** Add to `~/.flmx/.env` and source on demand:
```bash
mkdir -p ~/.flmx && cat > ~/.flmx/.env << 'EOF'
export FLMX_BASE_URL="https://panel.example.com"
export FLMX_USERNAME="admin"
export FLMX_PASSWORD="your-password"
EOF
chmod 600 ~/.flmx/.env
source ~/.flmx/.env
```

## Authentication Flow

### Session Token Cache

- Token is cached **only for the current conversation**
- New conversation = fresh login required
- Token is NOT written to disk (security)

### Auto-Login Pattern

```
Before ANY API call:
1. Check if TOKEN is cached in current session
   ├─ Yes → Use cached token, proceed
   └─ No → 
       1. Read FLMX_USERNAME and FLMX_PASSWORD from environment
       2. POST /api/v1/user/login with credentials
       3. Cache response.data.token in session memory
       4. Proceed with original request
```

### Login Request

```bash
curl -X POST "${FLMX_BASE_URL}/api/v1/user/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"${FLMX_USERNAME}\",\"password\":\"${FLMX_PASSWORD}\"}"
```

**Response:**
```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "token": "eyJhbGciOiJIUzI1NiIs...",
    "name": "Administrator",
    "role_id": 0,
    "requirePasswordChange": false
  },
  "ts": 1706659200000
}
```

## Authentication Rules

| Header | Value | Critical |
|--------|-------|----------|
| `Authorization` | `<jwt_token>` | ⚠️ NO "Bearer" prefix! |
| `Content-Type` | `application/json` | All requests use JSON |

## Quick Start Workflow

```
User request → Check env vars → Auto-login if needed → Call API → Return result
```

## Intent → API Mapping

| User Intent | API Endpoint | Reference |
|-------------|--------------|-----------|
| "登录" / "查看我的信息" | `/api/v1/user/package` | [auth](references/auth.md) |
| "创建用户" / "添加用户" | `/api/v1/user/create` | [users](references/users.md) |
| "查看用户列表" / "所有用户" | `/api/v1/user/list` | [users](references/users.md) |
| "重置流量" | `/api/v1/user/reset` | [users](references/users.md) |
| "添加节点" / "新建节点" | `/api/v1/node/create` | [nodes](references/nodes.md) |
| "查看节点" / "节点状态" | `/api/v1/node/list` | [nodes](references/nodes.md) |
| "安装命令" / "部署节点" | `/api/v1/node/install` | [nodes](references/nodes.md) |
| "升级节点" | `/api/v1/node/upgrade` | [nodes](references/nodes.md) |
| "创建隧道" / "新建隧道" | `/api/v1/tunnel/create` | [tunnels](references/tunnels.md) |
| "分配隧道给用户" | `/api/v1/tunnel/user/assign` | [tunnels](references/tunnels.md) |
| "创建转发" / "新建转发" / "添加转发" | `/api/v1/forward/create` | [forwards](references/forwards.md) |
| "暂停转发" | `/api/v1/forward/pause` | [forwards](references/forwards.md) |
| "恢复转发" | `/api/v1/forward/resume` | [forwards](references/forwards.md) |
| "删除转发" | `/api/v1/forward/delete` | [forwards](references/forwards.md) |
| "查看我的转发" / "转发列表" | `/api/v1/forward/list` | [forwards](references/forwards.md) |
| "查看流量" / "流量统计" | `/api/v1/forward/list` or `/api/v1/user/package` | [forwards](references/forwards.md) |
| "诊断转发" / "测试连通性" | `/api/v1/forward/diagnose` | [forwards](references/forwards.md) |
| "创建限速规则" | `/api/v1/speed-limit/create` | [speed-limits](references/speed-limits.md) |
| "联邦共享" / "节点共享" | `/api/v1/federation/share/create` | [federation](references/federation.md) |
| "导出备份" | `/api/v1/backup/export` | [backup](references/backup.md) |
| "导入备份" | `/api/v1/backup/import` | [backup](references/backup.md) |
| "安装内核" / "装 Xray / sing-box / Snell" | `/api/v1/kernel/install` | [kernels](references/kernels.md) |
| "内核版本" / "内核能力" | `/api/v1/kernel/versions` / `/api/v1/kernel/capabilities` | [kernels](references/kernels.md) |
| "创建入站" / "新建入站" / "加节点配置" | `/api/v1/inbound/create` | [inbounds](references/inbounds.md) |
| "查看入站" / "入站列表" | `/api/v1/inbound/list` | [inbounds](references/inbounds.md) |
| "导入节点" / "导入分享链接" | `/api/v1/inbound/import` | [inbounds](references/inbounds.md) |
| "入站分享链接" | `/api/v1/inbound/share` | [inbounds](references/inbounds.md) |
| "新建订阅" / "创建订阅" | `/api/v1/subscription/save` | [subscriptions](references/subscriptions.md) |
| "查看订阅" / "订阅列表" | `/api/v1/subscription/list` | [subscriptions](references/subscriptions.md) |
| "分配订阅给用户" | `/api/v1/user-subscription/set` | [subscriptions](references/subscriptions.md) |
| "我的订阅" | `/api/v1/user-subscription/mine` | [subscriptions](references/subscriptions.md) |
| "添加订阅源" / "同步订阅源" | `/api/v1/subscription/source/save` / `/api/v1/subscription/source/refresh` | [subscriptions](references/subscriptions.md) |
| "监控" / "节点指标" / "服务监控" | `/api/v1/monitor/nodes` / `/api/v1/monitor/services` | [monitoring](references/monitoring.md) |
| "监控权限" | `/api/v1/monitor/permission/assign` | [monitoring](references/monitoring.md) |
| "Passkey 登录" / "注册 Passkey" | `/api/v1/user/passkey/login/begin` / `/api/v1/user/passkey/register/begin` | [passkeys](references/passkeys.md) |
| "检查更新" / "面板升级" | `/api/v1/system/check-updates` / `/api/v1/system/upgrade` | [system](references/system.md) |
| "修改公告" | `/api/v1/announcement/update` | [system](references/system.md) |
| "节点调优" | `/api/v1/node/tune` | [nodes](references/nodes.md) |
| "回程路由" | `/api/v1/node/route` | [nodes](references/nodes.md) |
| "待删除转发" / "pending-delete" | `/api/v1/node/nftables/pending-deletes` | [nodes](references/nodes.md) |

## HTTP Request Template

### Bash/curl (with auto-login)

```bash
#!/bin/bash
BASE_URL="${FLMX_BASE_URL}"
USERNAME="${FLMX_USERNAME}"
PASSWORD="${FLMX_PASSWORD}"

# Login and get token
TOKEN=$(curl -s -X POST "${BASE_URL}/api/v1/user/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"${USERNAME}\",\"password\":\"${PASSWORD}\"}" | jq -r '.data.token')

if [ "$TOKEN" == "null" ] || [ -z "$TOKEN" ]; then
  echo "Login failed"
  exit 1
fi

# Use token for API calls - NOTE: NO "Bearer" prefix!
curl -s -X POST "${BASE_URL}/api/v1/node/list" \
  -H "Authorization: ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{}' | jq '.'
```

### Python (requests)

```python
import os
import requests

BASE_URL = os.environ.get("FLMX_BASE_URL")
USERNAME = os.environ.get("FLMX_USERNAME")
PASSWORD = os.environ.get("FLMX_PASSWORD")

# Login
resp = requests.post(f"{BASE_URL}/api/v1/user/login", 
    headers={"Content-Type": "application/json"},
    json={"username": USERNAME, "password": PASSWORD})
result = resp.json()
if result["code"] != 0:
    raise Exception(f"Login failed: {result['msg']}")

TOKEN = result["data"]["token"]

# Authenticated request - NO "Bearer" prefix!
headers = {
    "Content-Type": "application/json",
    "Authorization": TOKEN
}
resp = requests.post(f"{BASE_URL}/api/v1/node/list", headers=headers, json={})
print(resp.json())
```

### Node.js (fetch)

```javascript
const BASE_URL = process.env.FLMX_BASE_URL;
const USERNAME = process.env.FLMX_USERNAME;
const PASSWORD = process.env.FLMX_PASSWORD;

// Login
const loginRes = await fetch(`${BASE_URL}/api/v1/user/login`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: USERNAME, password: PASSWORD })
});
const loginData = await loginRes.json();
if (loginData.code !== 0) throw new Error(loginData.msg);
const TOKEN = loginData.data.token;

// Authenticated request - NO "Bearer" prefix!
const res = await fetch(`${BASE_URL}/api/v1/node/list`, {
  method: 'POST',
  headers: { 
    'Content-Type': 'application/json',
    'Authorization': TOKEN
  },
  body: JSON.stringify({})
});
console.log(await res.json());
```

## Response Handling

**Success:**
```json
{"code": 0, "msg": "success", "data": {...}, "ts": 1706659200000}
```

**Error:**
```json
{"code": -1, "msg": "用户名或密码错误", "ts": 1706659200000}
```

**Pattern:**
```
1. Parse JSON response
2. If code === 0 → return data
3. If code === 401 → token expired, re-login and retry
4. If code === 403 → permission denied, need admin
5. Else → show msg to user as error message
```

## Permission Model

| role_id | Type | Access |
|---------|------|--------|
| 0 | Admin | All endpoints |
| 1 | Regular | Forward CRUD, own profile, assigned tunnels only |

Non-admin users can only see/modify their own resources.

## Module Reference

| Module | Endpoints | Reference |
|--------|-----------|-----------|
| Auth | login, captcha | [auth.md](references/auth.md) |
| Users | CRUD, reset, password | [users.md](references/users.md) |
| Nodes | CRUD, install, upgrade, status | [nodes.md](references/nodes.md) |
| Tunnels | CRUD, user assignment | [tunnels.md](references/tunnels.md) |
| Forwards | CRUD, pause/resume, diagnose | [forwards.md](references/forwards.md) |
| Groups | User/tunnel groups, permissions | [groups.md](references/groups.md) |
| Speed Limits | CRUD | [speed-limits.md](references/speed-limits.md) |
| Federation | Share, remote nodes | [federation.md](references/federation.md) |
| Kernels | Capabilities, install, versions, control, logs | [kernels.md](references/kernels.md) |
| Inbounds | CRUD, import, share, split, relay lines | [inbounds.md](references/inbounds.md) |
| Subscriptions | CRUD, sources, user assignment, public fetch | [subscriptions.md](references/subscriptions.md) |
| Monitoring | Node/tunnel metrics, service monitors, permissions | [monitoring.md](references/monitoring.md) |
| Passkeys | WebAuthn login / register / list / delete | [passkeys.md](references/passkeys.md) |
| System | Version, storage, self-upgrade, announcement, notify | [system.md](references/system.md) |
| Backup | Export/import | [backup.md](references/backup.md) |
| Config | System settings | [config.md](references/config.md) |
| Types | TypeScript interfaces | [types.md](references/types.md) |
| Errors | Error codes | [errors.md](references/errors.md) |
| Examples | Code samples | [examples/](references/examples/) |

## Critical Rules

1. ⚠️ **NO "Bearer" prefix** - `Authorization: <token>`, NOT `Authorization: Bearer <token>`
2. **All endpoints use POST** - Including list/get operations
3. **code === 0 means success** - Any other value is an error
4. **Traffic units**: User.flow is GB, in_flow/out_flow are bytes
5. **Timestamps**: All timestamps are milliseconds since epoch
6. **Token is session-scoped**: Cache in memory only, not on disk

## Common Workflows

### Workflow 1: New User Onboarding (Admin)
```
1. POST /api/v1/user/create → Create user with traffic quota
2. POST /api/v1/tunnel/user/assign → Assign tunnels to user
3. Tell user their username/password
4. User logs in and creates forwards
```

### Workflow 2: Add New Node (Admin)
```
1. POST /api/v1/node/create → Register node in panel
2. POST /api/v1/node/install → Get install command
3. Run install command on target server
4. POST /api/v1/node/check-status → Verify node is online
```

### Workflow 3: Create Forward (Any User)
```
1. POST /api/v1/tunnel/user/tunnel → List available tunnels
2. POST /api/v1/forward/create → Create forward on chosen tunnel
3. POST /api/v1/forward/diagnose → Verify connectivity
```

### Workflow 4: Node Maintenance (Admin)
```
1. POST /api/v1/node/list → Check node statuses
2. POST /api/v1/node/releases → Check available versions
3. POST /api/v1/node/upgrade or /batch-upgrade → Upgrade nodes
4. POST /api/v1/node/rollback → Rollback if needed
```
