# TypeScript Type Definitions

All field names below are the API's actual camelCase wire names.

## API Response Envelope

```typescript
interface APIResponse<T = unknown> {
  code: number;      // 0 = success
  msg: string;       // Message (usually Chinese; "操作成功" on success)
  ts: number;        // Unix timestamp in milliseconds
  data?: T;          // Response payload (absent on many success responses)
}
```

## Lists & Pagination

List endpoints return a **bare array** in `data` — there is no
`{list, total}` wrapper anywhere in the API. Only `user/list` consumes
pagination parameters (`current`, `size`, `keyword`); other list
endpoints ignore the body.

```typescript
interface UserListRequest {
  current?: number;  // 1-based page
  size?: number;     // page size
  keyword?: string;  // username substring filter
}
```

## User

```typescript
// user/list item (admin accounts are excluded from the list)
interface User {
  id: number;
  user: string;           // Username
  name: string;           // Same value as `user`
  roleId: number;         // 0 = admin, 1 = regular
  status: number;         // 1 = active, 0 = disabled
  flow: number;           // Traffic quota in GB, 0 = unlimited
  flowMiB: number;        // Quota in MiB when set (0 = GB value rules)
  num: number;            // Max forwards allowed
  inFlow: number;         // Used upload in bytes
  outFlow: number;        // Used download in bytes
  expTime: number;        // Expiry timestamp (ms)
  flowResetTime: number;  // Monthly reset day (1-31), 0 = no reset
  maxConn: number;        // Connection limit, 0 = unlimited
  createdTime?: number;
  updatedTime?: number;
}

interface UserCreateRequest {
  user: string;
  pwd: string;
  status?: number;
  flow?: number;
  num?: number;
  expTime?: number;
  flowResetTime?: number;
  maxConn?: number;
  dailyQuotaGB?: number;
  monthlyQuotaGB?: number;
}

// user/package payload
interface UserPackage {
  userInfo: User;
  tunnelPermissions: UserTunnel[];
  forwards: Forward[];
  statisticsFlows: unknown[];
}
```

## Node

```typescript
// node/list item — the secret is NOT exposed here
interface Node {
  id: number;
  inx: number;
  name: string;
  remark?: string;
  expiryTime?: number | null;
  renewalCycle?: string;
  ip: string;
  serverIp: string;
  serverIpV4?: string;
  serverIpV6?: string;
  extraIPs?: string;
  port: string;           // "1000-65535"
  interfaceName?: string;
  http: number;           // 1 = enabled
  tls: number;
  socks: number;
  tcpListenAddr: string;  // "[::]"
  udpListenAddr: string;
  status: number;         // 1 = online, 0 = offline
  forwardMode?: string;   // "agent" | "nftables"
  version?: string;
  expiryReminderDismissed?: number;
  sshConfig?: unknown;
}

interface NodeCreateRequest {
  name: string;
  serverIp: string;
  serverIpV4?: string;
  serverIpV6?: string;
  extraIPs?: string;
  port?: string;
  interfaceName?: string;
  remark?: string;
  expiryTime?: number;
  renewalCycle?: string;
  http?: number;
  tls?: number;
  socks?: number;
  tcpListenAddr?: string;
  udpListenAddr?: string;
  forwardMode?: string;
  isRemote?: number;
  remoteToken?: string;
}
```

## Tunnel

```typescript
interface NodeRef {
  id: number;
  name: string;
  // ... further node fields as returned by the list DTO
}

// tunnel/list item — node fields are arrays of node reference objects
interface Tunnel {
  id: number;
  inx: number;
  name: string;
  type: number;           // 1 = port forward, 2 = tunnel forward
  flow: number;           // Traffic multiplier
  trafficRatio: number;
  status: number;         // 1 = active, 0 = disabled
  ipPreference?: string;  // "ipv4", "ipv6", ""
  engine?: string;        // "" / "gost" | "realm"
  inIp?: string;
  probeTargetHost?: string;
  probeTargetPort?: number;
  inNodeId: NodeRef[];
  chainNodes: NodeRef[][];
  outNodeId: NodeRef[];
  createdTime?: number;
}

interface TunnelCreateRequest {
  name: string;
  type: number;
  flow?: number;
  trafficRatio?: number;
  status?: number;
  ipPreference?: string;
  inIp?: string;
  inNodeId: number[];
  chainNodeId?: number[];
  outNodeId: number[];
}
```

## Forward

```typescript
interface Forward {
  id: number;
  inx: number;
  userId: number;
  userName?: string;
  tunnelId: number;
  tunnelName?: string;
  tunnelTrafficRatio?: number;
  name: string;
  inIp?: string;
  inPort: number;
  remoteAddr: string;
  strategy: string;       // "fifo" | "round" | "rand" | "hash"
  status: number;         // 1 = running, 0 = paused
  inFlow: number;         // Upload bytes
  outFlow: number;        // Download bytes
  maxConn: number;
  ipMaxConn: number;
  createdTime?: number;
}

interface ForwardCreateRequest {
  name: string;
  tunnelId: number;
  remoteAddr: string;
  strategy?: string;
  inPort?: number;
  speedId?: number;
}
```

## Speed Limit

```typescript
interface SpeedLimit {
  id: number;
  name: string;
  speed: number;          // Mbps
  status: number;
  createdTime?: number;
  updatedTime?: number;
}
```

## User-Tunnel Assignment

```typescript
interface UserTunnel {
  id: number;             // Assignment row ID
  userId: number;
  tunnelId: number;
  tunnelName?: string;
  status: number;
  flow: number;           // Quota for this tunnel in GB, 0 = unlimited
  flowMiB: number;
  num: number;
  inFlow: number;
  outFlow: number;
  expTime: number;
  flowResetTime: number;
  speedId?: number | null;
  speedLimitName?: string | null;
}
```

## Backup

```typescript
// The export response body itself (no envelope, no `types` field).
// The import request is this shape PLUS a required `types: string[]`.
interface BackupExport {
  version: string;        // "1.0"
  exportedAt: number;
  users?: User[];
  nodes?: Node[];
  tunnels?: Tunnel[];
  forwards?: Forward[];
  speedLimits?: SpeedLimit[];
  configs?: Record<string, string>;
  // ... plus the remaining segment keys (see backup.md)
}
```

## Client Helper Class

```typescript
class FlmxClient {
  private baseUrl: string;
  private username: string;
  private password: string;
  private token?: string;

  constructor(baseUrl?: string, username?: string, password?: string) {
    this.baseUrl = baseUrl ?? process.env.FLMX_BASE_URL ?? "";
    this.username = username ?? process.env.FLMX_USERNAME ?? "";
    this.password = password ?? process.env.FLMX_PASSWORD ?? "";
  }

  private async ensureToken(): Promise<void> {
    if (this.token) return;
    
    const res = await fetch(`${this.baseUrl}/api/v1/user/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ 
        username: this.username, 
        password: this.password 
      }),
    });
    
    const result: APIResponse<{ token: string }> = await res.json();
    if (result.code !== 0) throw new Error(result.msg);
    this.token = result.data!.token;
  }

  async request<T>(endpoint: string, data?: object): Promise<T> {
    await this.ensureToken();
    
    const res = await fetch(`${this.baseUrl}${endpoint}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Authorization": this.token!,  // NO "Bearer " prefix!
      },
      body: JSON.stringify(data ?? {}),
    });
    
    const result: APIResponse<T> = await res.json();
    if (result.code === 401) {
      this.token = undefined;
      return this.request(endpoint, data);
    }
    if (result.code !== 0) throw new Error(result.msg);
    return result.data!;
  }

  // Convenience methods — list endpoints return bare arrays
  async listNodes(): Promise<Node[]> {
    return this.request<Node[]>("/api/v1/node/list", {});
  }

  async listForwards(): Promise<Forward[]> {
    return this.request<Forward[]>("/api/v1/forward/list", {});
  }

  async createForward(req: ForwardCreateRequest): Promise<void> {
    await this.request("/api/v1/forward/create", req);
  }

  async getUserPackage(): Promise<UserPackage> {
    return this.request("/api/v1/user/package", {});
  }
}
```
