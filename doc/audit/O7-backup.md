# O7 备份恢复功能全量审计

**审计时间**：2026-10-20
**审计范围**：备份导出、备份导入、字段完整性、文件安全、定时备份
**结论**：发现 P1×5 / P2×5 / P3×3，无 P0

---

## P1 严重（恢复后数据丢失）

### O7-001：`user_inbound` 表完全未备份 — 订阅分配关系丢失
- **文件**：`go-backend/internal/store/model/model.go:444` (`BackupData` 无 `UserInbounds` 字段)
- **文件**：`go-backend/internal/store/repo/repository.go:1982` (`ExportAll` 未导出 `user_inbound` 表)
- **文件**：`go-backend/internal/store/repo/repository.go:2431` (`Import` 未恢复该表)
- **影响**：`user_inbound` 是订阅分配关系表（哪个用户可访问哪个入站/中转）。全量备份恢复后，所有用户的订阅分配关系**全部丢失**，用户将看不到任何订阅节点。
- **复现**：导出备份 → 清空 `user_inbound` 表 → 导入备份 → 分配关系未恢复。
- **证据**：`BackupData` 结构体包含 users/nodes/tunnels/forwards/userTunnels/speedLimits/tunnelGroups/userGroups/permissions/configs/inbounds，但无 userInbounds。

### O7-002：`ChainTunnelBackup` 缺 `ConnectIP` — 链式隧道连接 IP 丢失
- **文件**：`go-backend/internal/store/model/model.go:646` (`ChainTunnelBackup` 无 `ConnectIP` 字段)
- **文件**：`go-backend/internal/store/repo/repository.go:2252` (`exportChainTunnels` 未导出 `ConnectIP`)
- **文件**：`go-backend/internal/store/repo/repository.go:2699` (`importTunnels` 中 ChainTunnel 恢复未设置 `ConnectIP`，更新列也不含 `connect_ip`)
- **影响**：`ChainTunnel.ConnectIP` 用于链式隧道探测和连接（`tunnel_best_exit.go:163`、`tunnel_quality_prober.go:392,583`）。恢复后链式隧道的多跳连接 IP 丢失，可能导致链式转发失败。
- **对照**：`ChainTunnel` 模型有 9 个字段（ID/TunnelID/ChainType/NodeID/Port/Strategy/Inx/Protocol/ConnectIP），备份只覆盖 8 个。

### O7-003：`UserBackup` 缺 `PasswordChangedAt` 和 `MaxConn`
- **文件**：`go-backend/internal/store/repo/repository.go:2141` (`exportUsers` 未导出 `u.PasswordChangedAt` 和 `u.MaxConn`)
- **影响**：
  - `PasswordChangedAt` 丢失：影响强制改密逻辑判断（M-05 功能）。恢复后用户可能被错误要求改密，或绕过改密要求。
  - `MaxConn` 丢失：用户连接数限制恢复后变为 0（无限制），限流策略失效。
- **对照**：`User` 模型有 16 个字段，`UserBackup` 只覆盖 14 个（缺上述 2 个）。此问题在 I-database-reaudit 中已报告为 P2-2，此处确认仍未修复。

### O7-004：`ForwardBackup` 缺 `MaxConn` — 转发连接数限制丢失
- **文件**：`go-backend/internal/store/repo/repository.go:2279` (`exportForwards` 未导出 `f.MaxConn`)
- **文件**：`go-backend/internal/store/repo/repository.go:2739` (`importForwards` 未恢复 `MaxConn`，更新列也不含 `max_conn`)
- **影响**：恢复后转发的 `MaxConn` 连接数限制丢失（变为 0=无限制），可能导致单转发耗尽服务器连接数。
- **对照**：`Forward` 模型有 20 个字段，`ForwardBackup` 覆盖 19 个（缺 `MaxConn`）。`IPMaxConn` 已覆盖，唯独 `MaxConn` 遗漏。此问题在 I-database-reaudit 中已报告为 P2-1，此处确认仍未修复。

### O7-005：`ForwardPortBackup` 缺 `InIP` — 端口入口 IP 丢失
- **文件**：`go-backend/internal/store/model/model.go:680` (`ForwardPortBackup` 只有 NodeID/Port，无 InIP)
- **文件**：`go-backend/internal/store/repo/repository.go:2315` (`exportForwardPorts` 未导出 `fp.InIP`)
- **文件**：`go-backend/internal/store/repo/repository.go:2772` (`importForwards` 创建 ForwardPort 时未设置 `InIP`：`tx.Create(&model.ForwardPort{ForwardID: f.ID, NodeID: fp.NodeID, Port: fp.Port})`)
- **影响**：`ForwardPort.InIP` 用于 nftables 绑定（`nftables_runtime.go:225`：`BindIP: strings.TrimSpace(fp.InIP)`）和端口冲突检测。恢复后多 IP 服务器的端口绑定 IP 丢失，可能导致转发监听错误的网卡。
- **对照**：`ForwardPort` 模型有 5 个字段（ID/ForwardID/NodeID/Port/InIP），备份只覆盖 2 个（NodeID/Port）。ID 和 ForwardID 可重建，但 InIP 是业务数据不应丢失。

---

## P2 一般

### O7-006：`NodeBackup` 缺 `ExpiryReminderDismissed` — 到期提醒重现
- **文件**：`go-backend/internal/store/model/model.go:597` (`NodeBackup` 无该字段)
- **影响**：恢复后用户已忽略的节点到期提醒会重新弹出。低危害，但属于数据不一致。
- **备注**：在 I-database-reaudit 中已报告为 P2-3，此处确认仍未修复。

### O7-007：`announcement` 表未备份
- **文件**：`go-backend/internal/store/repo/repository.go:1982` (`ExportAll` 未导出 `announcement` 表)
- **影响**：全量备份恢复后，系统公告丢失。需管理员重新发布。

### O7-008：`monitor_permission` 表未备份
- **文件**：`go-backend/internal/store/repo/repository.go:1982` (`ExportAll` 未导出 `monitor_permission` 表)
- **影响**：非管理员用户的监控权限在恢复后丢失，需重新授权。

### O7-009：备份版本无兼容性检查
- **文件**：`go-backend/internal/http/handler/handler.go:1787` (`backupImport` 只检查 `req.BackupData.Version == ""`，不验证版本号)
- **文件**：`go-backend/internal/store/repo/repository.go:1983` (导出时硬编码 `Version: "1.0"`)
- **影响**：未来版本新增字段时，旧版备份导入新版会静默丢失新字段数据；新版备份导入旧版可能因未知字段失败。应实现版本协商或迁移逻辑。

### O7-010：导入前自动备份失败会中断恢复
- **文件**：`go-backend/internal/http/handler/handler.go:1783-1787`
```go
autoBackup, err := h.repo.ExportAll()
if err != nil {
    response.WriteJSON(w, response.Err(-2, fmt.Sprintf("导入前自动备份失败: %v", err)))
    return
}
```
- **影响**：如果当前数据库已损坏（`ExportAll` 失败），用户无法从完好备份恢复——而这正是恢复功能最该工作的场景。建议：自动备份失败时警告但允许继续（用户已明确要覆盖）。

---

## P3 备注

### O7-011：无定时自动备份功能
- **现状**：只有手动导出（`/api/v1/backup/export`），无定时任务。
- **建议**：增加每日自动备份到本地目录，保留 N 天。属功能增强，非缺陷。

### O7-012：`federation_tunnel_binding` 未备份
- **影响**：联邦隧道绑定关系在恢复后丢失。可能是有意设计（联邦配置通常不随备份迁移），记录备查。

### O7-013：`SubscriptionBackup` 缺 `UpdatedTime`
- **影响**：极小。`UpdatedTime` 仅用于显示，恢复后会被重置为导入时间。可忽略。

---

## 已验证通过

1. **备份文件大小限制**：导入用 `decodeJSONWithLimit(r.Body, &req, 100<<20)`，100MB 上限（`handler.go:1770`）。AUDIT-R22 已验证合理。
2. **导出 buffer-first**：先 Encode 到 buffer 再写响应，防止截断损坏（`handler.go:1737-1748`，LAD22-001）。
3. **导入事务性**：`Import` 在 `r.db.Transaction` 中执行，任一表失败则整体回滚（`repository.go:2436`）。
4. **TunnelBackup 字段完整**：14 个字段全覆盖 ✓
5. **UserTunnelBackup 字段完整**：12 个字段全覆盖 ✓
6. **SpeedLimitBackup 字段完整**：8 个字段全覆盖 ✓
7. **TunnelGroup/UserGroup/Permission 字段完整** ✓
8. **SubscriptionBackup 核心字段完整**：Options 存原始 JSON，新字段自动保留 ✓

---

## 字段对照总表

| 模型 | 字段数 | 备份覆盖 | 缺失字段 | 级别 |
|---|---|---|---|---|
| User | 16 | 14 | PasswordChangedAt, MaxConn | P1 |
| Node | 29 | 28 | ExpiryReminderDismissed | P2 |
| Tunnel | 14 | 14 | — | ✓ |
| Forward | 20 | 19 | MaxConn | P1 |
| ForwardPort | 5 | 2 | InIP (ID/ForwardID可重建) | P1 |
| ChainTunnel | 9 | 8 | ConnectIP | P1 |
| UserTunnel | 12 | 12 | — | ✓ |
| SpeedLimit | 8 | 8 | — | ✓ |
| TunnelGroup | 5 | 5 | — | ✓ |
| UserGroup | 5 | 5 | — | ✓ |
| GroupPermission | 5 | 5 | — | ✓ |
| GroupPermissionGrant | 6 | 6 | — | ✓ |
| Subscription | 22 | 20 | AppliedActive(运行时), UpdatedTime | P3 |
| UserInbound | 4 | 0 | **整表未备份** | P1 |
| Announcement | 4 | 0 | 整表未备份 | P2 |
| MonitorPermission | 4 | 0 | 整表未备份 | P2 |

---

## 建议修复顺序

1. **O7-001**（P1）：`BackupData` 加 `UserInbounds` 字段 + 导出/导入（影响最大：订阅分配全丢）
2. **O7-002**（P1）：`ChainTunnelBackup` 加 `ConnectIP`
3. **O7-003**（P1）：`exportUsers` 加 `PasswordChangedAt`/`MaxConn`
4. **O7-004**（P1）：`exportForwards`/`importForwards` 加 `MaxConn`
5. **O7-005**（P1）：`ForwardPortBackup` 加 `InIP`
6. **O7-010**（P2）：自动备份失败时允许继续导入（加警告）
7. 其余 P2/P3 按需处理

**只审计未修复**：源代码未修改。
