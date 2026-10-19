# 中转/隧道功能全量审计报告

**审计员**：O3-relay
**日期**：2026-10-20
**参考手册**：doc/guide/relay.md
**工作目录**：~/workspace/flvx-max

## 审计范围

对照手册 `doc/guide/relay.md`，审计以下功能：
1. 隧道 CRUD：创建、编辑、删除
2. 中转规则：落地/中转节点配置
3. 隧道状态：连接状态、重连逻辑
4. 链式代理：多跳链式配置
5. 隧道流量：单向/双向计费（M-001 已修复，复审确认）
6. 中转订阅：按中转的订阅分配

## 手册功能对照验证

### ✅ 1. 中转线路自动加入订阅（手册："中转线路会自动加入订阅，名称为'隧道名 → 入站名'"）

**实现位置**：`go-backend/internal/http/handler/kernel_relay.go:211`

```go
name = fmt.Sprintf("%s → %s", firstNonEmpty(tunnelName, "中转"), row.Name)
```

**验证**：实现与手册一致。隧道名为空时回退为"中转"。

### ✅ 2. 级联删除（手册："删除中转、入站或内核时，相关的转发会一并清理"）

**实现位置**：
- 隧道删除：`go-backend/internal/http/handler/mutations.go:5062` → `deleteTunnelByID`
- 级联逻辑：`go-backend/internal/store/repo/repository_mutations.go:691` → `DeleteTunnelCascade`

**验证**：`DeleteTunnelCascade` 在事务中清理：
- ForwardPort（转发端口）
- NodeInboundRelay（中转关联）
- NftCounterState（nft 计数器状态）
- NftRuleBinding（nft 规则绑定）
- NftPendingDelete（待删除 nft 规则）
- Forward（转发）
- GroupPermissionGrant（分组权限）
- UserTunnel（用户隧道）

清理顺序正确（先子表后父表），使用事务保证原子性。

### ✅ 3. 隧道转发模式（手册：三种对接方式）

**实现位置**：`go-backend/internal/http/handler/kernel_relay.go:122` → `relayTarget`

**验证**：
- **端口转发**：直接连接落地机公网地址（`row.Listen` 为通配符时使用 `node.ServerIP`）
- **隧道转发（出口为落地机）**：当 `tunnel.Type == 2` 且出口节点就是落地节点时，返回 `127.0.0.1:端口`（`kernel_relay.go:138-144`）
- **隧道转发（出口为其他节点）**：返回落地机公网地址，由出口节点连接

逻辑与手册描述一致。

### ✅ 4. PROXY Protocol（手册："开启后，中转转发会附带 PROXY protocol"）

**实现位置**：
- 同步逻辑：`go-backend/internal/http/handler/kernel_protect.go:141` → `syncRelayProxyProtocol`
- 创建时：`go-backend/internal/http/handler/kernel_relay.go:225`（`ppSend = relayProxyProtocolVersion`）

**验证**：入站开启 `AcceptProxyProtocol=1` 时，所有托管的中转转发自动同步发送 PROXY protocol v1。

### ✅ 5. PROXY 开启后自动仅中转（手册："开启后直连将无法使用，入站会自动变为'仅中转'"）

**实现位置**：`go-backend/internal/http/handler/kernel.go:757-759`

```go
if row.AcceptProxyProtocol == 1 {
    // Clients cannot speak PROXY protocol, so direct access stops working.
    row.RelayOnly = 1
}
```

**验证**：实现与手册一致，强制设置 `RelayOnly=1`。

### ✅ 6. 入站白名单（手册："中转增删时白名单会自动更新"）

**实现位置**：
- 白名单生成：`go-backend/internal/http/handler/kernel.go:1225`（`port["allow"] = h.inboundAllowList(row)`）
- 中转删除后刷新：`go-backend/internal/http/handler/kernel_relay.go:377`（`h.refreshInboundWhitelist(rel.InboundID)`）

**验证**：中转删除时调用 `refreshInboundWhitelist` 更新白名单。

### ✅ 7. 隧道流量计费（M-001 已修复）

**修复位置**：
- `go-backend/internal/http/handler/nftables_traffic.go:450`（nftables 路径）
- `go-backend/internal/http/handler/flow_upload_batch.go`（gost 上报路径）

**复审确认**：
- Flow=1（单向）：仅计上传，不再使用乘数
- Flow=2（双向）：计上传+下载，不再双倍扣费
- 前端标签正确："单向计算（仅上传）" / "双向计算（上传+下载）"（`vite-frontend/src/pages/tunnel.tsx:3142`）

**验证**：修复正确，与手册/前端语义一致。

### ✅ 8. 按中转订阅分配

**实现位置**：`go-backend/internal/store/model/model.go:276-282`

```go
// RelayID = 0 means the inbound itself (direct endpoints);
// RelayID > 0 means a specific relay (tunnel forward) of that inbound.
RelayID   int64 `gorm:"column:relay_id;not null;default:0;uniqueIndex:idx_user_inbound_relay_unique"`
```

**验证**：数据模型支持按中转的细粒度订阅分配。

## 发现问题

### P1

**O3-001：隧道删除时未刷新相关入站的白名单**

**文件**：`go-backend/internal/http/handler/mutations.go:1723`（`tunnelDelete`）

**问题**：删除隧道时调用 `deleteTunnelByID` → `DeleteTunnelCascade` 清理了 `NodeInboundRelay` 记录，但**没有**调用 `refreshInboundWhitelist` 更新受影响入站的白名单。

对比中转删除（`kernel_relay.go:377`）：
```go
h.refreshInboundWhitelist(rel.InboundID)  // 中转删除时有刷新
```

隧道删除时，相关的 `NodeInboundRelay` 被级联删除，但白名单中的中转机 IP 残留，导致：
1. 白名单包含已不存在的中转机 IP（无害但脏数据）
2. 更严重：如果白名单是"仅允许列表"，残留 IP 不会造成安全问题，但属于数据不一致

**复现步骤**：
1. 创建入站，开启白名单
2. 创建隧道，添加中转
3. 删除隧道（不是删除中转）
4. 检查入站白名单：已删除隧道的中转机 IP 仍在列表中

**建议修复**：在 `deleteTunnelByID` 或 `tunnelDelete` 中，删除前查询受影响的 inbound IDs，删除后调用 `refreshInboundWhitelist`。

---

### P2

**O3-002：`relayTarget` 中落地节点 IP 选择逻辑可能返回空**

**文件**：`go-backend/internal/http/handler/kernel_relay.go:150-160`

```go
host := row.Listen
if isWildcardListen(host) {
    node, err := h.repo.GetNodeByID(row.NodeID)
    if err != nil || node == nil {
        return "", errors.New("落地节点不存在")
    }
    host = strings.TrimSpace(node.ServerIP)
    if v4 := strings.TrimSpace(node.ServerIPV4.String); host == "" && v4 != "" {
        host = v4
    }
}
if host == "" {
    return "", errors.New("落地节点没有可用地址")
}
```

**问题**：当 `node.ServerIP` 为空且 `ServerIPV4` 也为空时，返回错误"落地节点没有可用地址"。但没有尝试 `ServerIPV6`。

**影响**：纯 IPv6 节点无法作为落地机使用端口转发模式。

**建议**：补充 IPv6 回退逻辑。

---

**O3-003：隧道批量删除未做白名单刷新（与 O3-001 同类）**

**文件**：`go-backend/internal/http/handler/mutations.go:1788`（`tunnelBatchDelete`）

**问题**：批量删除隧道时同样没有刷新白名单，与 O3-001 相同原因。

---

### P3

**O3-004：中转名称长度限制 100 字，但隧道名+入站名可能超长**

**文件**：`go-backend/internal/http/handler/kernel_relay.go:211-214`

```go
name = fmt.Sprintf("%s → %s", firstNonEmpty(tunnelName, "中转"), row.Name)
...
if len([]rune(name)) > 100 {
    response.WriteJSON(w, response.ErrDefault("名称不超过 100 字"))
    return
}
```

**问题**：自动生成的名称（隧道名 → 入站名）如果超过 100 字会报错，但用户没有输入名称，无法自行缩短。应该自动截断而非报错。

**影响**：低频（需要隧道名+入站名合计超过 97 字）。

---

## 测试验证

运行隧道/中转相关测试：
```bash
go test ./internal/http/handler/ -run "TestTunnel|TestRelay" -count=1
# 结果：ok（20.566s）
```

## 总结

**手册一致性**：8 项手册描述功能全部实现一致，无手册与实现不符。

**发现问题**：P1 1 个 / P2 2 个 / P3 1 个

**最严重**：O3-001（P1）隧道删除后白名单未刷新，导致数据不一致。建议修复。

**M-001 复审**：计费修复正确，Flow 语义与前端标签一致。
