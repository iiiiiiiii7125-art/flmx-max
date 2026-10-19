# O6 流量计费功能审计报告

**审计日期**：2026-10-20
**审计员**：流量计费专项审计
**审计范围**：nftables 流量采集、gost 流量上报、单向/双向计费、流量倍率、配额扣减、配额耗尽、流量重置、流量查询

## 审计结论

**发现问题：P0 0 / P1 1 / P2 3 / P3 2**

M-001（P0 计费 bug）已修复并验证通过，本报告确认修复完整，无回归。

## P1 严重

### O6-001：配额执法 DB 不一致（`user_quota.go:60-64`）

**问题描述**：
`enforceUserQuotaIfNeeded` 中，如果 `controlForwardServices(forward, "PauseService")` 成功（实际服务已暂停），但后续 `h.repo.UpdateForwardStatus(forward.ID, 0, now)` 失败（DB 未更新），则：
1. 服务实际已暂停，但 DB 中 `forward.status` 仍为 1（活跃）
2. 该 forward 不会被加入 `pausedIDs` 列表
3. 当用户配额重置/恢复时，`applyUserQuotaRelease` 只会恢复 `pausedIDs` 中的转发，该转发永远不会被恢复

**复现步骤**：
1. 模拟 `UpdateForwardStatus` 返回错误（如 DB 锁、连接断开）
2. 触发用户配额超限
3. 观察：服务被暂停，但 DB 状态仍为 1，且配额恢复后服务不会自动恢复

**影响**：转发服务永久暂停，需人工介入恢复。低频但高危。

**证据**：
```go
// user_quota.go:57-64
if err := h.controlForwardServices(forward, "PauseService", false); err != nil {
    continue  // 服务暂停失败，跳过（合理）
}
if err := h.repo.UpdateForwardStatus(forward.ID, 0, now); err != nil {
    continue  // DB 更新失败，跳过 —— 但服务已经暂停了！
}
pausedIDs = append(pausedIDs, forward.ID)  // 只有 DB 成功才加入
```

## P2 一般

### O6-002：InFlow/OutFlow 统计口径不一致（`nftables_traffic.go:478-486` vs `flow_upload_batch.go:100-104`）

**问题描述**：
nftables 路径和 gost 路径的 InFlow/OutFlow 语义不一致：

| 路径 | Flow=1（单向/仅上传） | Upload 去向 |
|------|---------------------|------------|
| nftables | 只计 BytesIn（上传） | InFlow |
| gost | 只计 item.U（上传） | OutFlow |

**影响**：计费（quotaUsage）两条路径都正确（都只计上传），但 `forward_traffic` 表的 InFlow/OutFlow 统计在两条路径下含义相反，前端展示"上传/下载"流量时会产生混淆。

**证据**：
```go
// nftables_traffic.go:478-486 (Flow=1 时)
batch.flowDeltas[flowIdx].InFlow += scaledIn   // scaledIn = 上传
batch.flowDeltas[flowIdx].OutFlow += scaledOut // scaledOut = 0

// flow_upload_batch.go:100-104 (Flow=1 时)
InFlow:  scaledIn,  // scaledIn = 0
OutFlow: scaledOut, // scaledOut = 上传
```

**建议**：统一口径，或在文档中明确说明。非计费错误，优先级 P2。

### O6-003：nftables 计数器重置导致流量丢失（`nftables_traffic.go:372-375`）

**问题描述**：
当 nftables 计数器重置（节点重启、规则重建）时，`sample.Bytes < oldBytes`，代码直接更新基线而不计算增量：
```go
if sample.Bytes < oldBytes {
    stateInputs = append(stateInputs, stateInput)
    continue  // 上次采集到重置之间的流量丢失
}
```

**影响**：两次采集之间如果发生计数器重置，期间的流量不会被计费。属于已知局限（注释中有说明），但用户无感知。

**建议**：在文档中明确说明此局限，或在重置时记录警告日志。目前仅静默丢弃。

### O6-004：配额执法静默跳过失败的转发（`user_quota.go:57-59`）

**问题描述**：
```go
if err := h.controlForwardServices(forward, "PauseService", false); err != nil {
    continue  // 静默跳过，无日志
}
```
如果暂停服务失败，该转发保持活跃，但后续 `MarkUserQuotaDisabled` 会将用户标记为"已因配额禁用"，造成"用户被禁用但转发仍在跑"的不一致状态。

**建议**：添加错误日志，便于排查。

## P3 备注

### O6-005：TrafficRatio=0 允许零计费（`nftables_traffic.go:514`）

```go
func scaleNftTrafficBytes(bytes int64, ratio float64) (int64, bool) {
    if bytes < 0 || ratio < 0 {
        return 0, false
    }
    // ratio=0 通过检查，返回 scaled=0
```

**说明**：`ratio=0` 会导致所有流量按 0 计算（免费）。这可能是有意设计（免费隧道），也可能是误配置。建议在隧道创建/编辑时验证，或在文档中明确说明。

### O6-006：float64 精度损失（`nftables_traffic.go:518-522`）

```go
scaledFloat := float64(bytes) * ratio
if math.IsNaN(scaledFloat) || math.IsInf(scaledFloat, 0) || scaledFloat < 0 || scaledFloat >= math.Pow(2, 63) {
    return 0, false
}
scaled = int64(scaledFloat)
```

**说明**：`float64` 在值超过 2^53 时会损失整数精度。检查 `>= 2^63` 防止了溢出，但 2^53 到 2^63 之间的值可能有 1-2 字节的精度误差。对于流量计费场景（GB 级别），误差可忽略。低风险，记录备查。

## M-001 修复验证 ✅

**修复内容**（1.0.7）：
1. `nftables_traffic.go`：`scaleNftTrafficBytes` 移除 `tunnelFlow` 乘数参数；调用方按 Flow=1/2 语义选择计费方向
2. `flow_upload_batch.go`：移除 `* meta.TunnelFlow` 乘数；Flow=1 时只计上传（item.U），Flow=2 时计上传+下载

**验证**：
- ✅ 全仓搜索确认无残留的 TunnelFlow 乘数用法（仅剩 metadata 传递）
- ✅ `go test ./internal/store/repo/ -run TestApplyNftTrafficAccounting` 通过
- ✅ `go build ./...` 通过
- ✅ 语义确认：前端 `tunnel.tsx:3142` 明确 "1: 单向计算（仅上传）"，修复符合产品定义

**计费语义（修复后）**：
| Flow | 含义 | nftables 计费 | gost 计费 |
|------|------|--------------|----------|
| 1 | 单向（仅上传） | BytesIn × ratio | item.U × ratio |
| 2 | 双向（上传+下载） | (BytesIn+BytesOut) × ratio | (item.U+item.D) × ratio |

## 已验证通过

1. **负值防护**（H-17）：`flow_upload_batch.go:44-47` 丢弃 `item.U<0 || item.D<0` ✅
2. **溢出防护**：多处 `math.MaxInt64` 检查，`scaleNftTrafficBytes` 拒绝 NaN/Inf/负值 ✅
3. **原子扣减**：`addUserQuotaUsageBatchTx` 使用 `gorm.Expr("daily_used_bytes + ?")` 原子增量 ✅
4. **事务一致性**：`ApplyNftTrafficAccounting` 在事务中执行 delta+quota+state 三步 ✅
5. **窗口滚动**：`applyUserQuotaWindowRoll` 正确处理日/月窗口切换 ✅
6. **重置事务**：`ResetUserQuotaUsage` 在事务中执行，失败回滚 ✅

## 未能验证

- 实际 nftables 计数器采集（需真机 + nft 环境）
- 大并发下的配额扣减竞态（需压力测试）
- 计数器重置时的实际流量丢失量（需模拟节点重启）
