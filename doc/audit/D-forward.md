# 审查域 D 报告：转发/隧道/流量计费

## 概要

- **审查范围**：对比移植前基线 `5e1edce8`，检查 7 批 flyx 移植是否破坏既有转发/隧道/计费功能
- **发现问题数**：P0 0 个 / P1 0 个 / P2 1 个 / P3 2 个
- **实际执行的测试**：
  - `git diff 5e1edce8 HEAD` 逐文件对比（control_plane.go、handler.go、kernel_probe.go、kernel_relay.go、jobs.go、nftables 相关、mutations.go、flow/quota 文件）
  - 路由重复性检查：187 个 `mux.HandleFunc` 注册，`sort | uniq -d` 无重复
  - `go test ./internal/kernel/ -run "TestSub|TestShare|TestClash"` ✅ 通过
  - `go test ./internal/kernel/ -run "TestSubScript|TestChain|TestApplySub"` ✅ 通过
  - `go test ./internal/http/handler/ -run "TestQuota|TestFlow|TestSubOver|TestUserQuota"` ✅ 通过（10.1s）
  - `go vet ./internal/kernel/` ✅ 通过
  - 自编写实测：`TestScriptBeforeMarkChainOrder` 验证脚本改名后链式分组是否保留 ✅ 通过（脚本将"香港 1"改名为"HK 1"，`markChain` 仍通过 `Original` 正确设置 `ChainRole=landing`）

## P0 阻断

无。

## P1 严重

无。

## P2 一般

### D-001 空订阅状态码变更（200+空 → 503）

- **文件**：`go-backend/internal/http/handler/kernel_relay.go:609-612`
- **现象**：`openSubscription` 在 `endpoints` 为空时，现在返回 `503 Service Unavailable` + 中文错误「订阅暂无可用节点（入站未启用或中转转发未建立）」，而移植前（`5e1edce8`）返回 `200` + 空 body。
- **复现步骤**：
  1. 创建订阅，关联一个 `RelayOnly=1` 且中转转发未建立的入站
  2. `GET /api/v1/open_api/sub/<token>?format=clash`
  3. 移植前：HTTP 200，空 body；现在：HTTP 503 + 错误文本
- **期望**：行为变更应有明确记录。返回 503 更合理（避免客户端静默失败），但属于既有 API 的行为变更。
- **实际**：静默改为空 503。部分老旧客户端可能只处理 200，需要确认兼容性。
- **影响范围**：所有订阅客户端（Clash/sing-box/Loon/QX/Shadowrocket/Surge）在订阅无可用节点时的错误处理路径。
- **修复方向**：保留 503（更合理），但需在发版说明中明确标注此行为变更。如需最大兼容，可改为仍返回 200 但在 body 中嵌入错误注释（YAML `#` 注释）。

## P3 体验/备注

### D-002 非分享订阅 `Subscription-Userinfo` 头 `total` 恒为 0（已在 1.0.3 修复）

- **文件**：`go-backend/internal/http/handler/kernel_relay.go:623-626`（修复前）
- **现象**：非分享订阅（`isShare(sub)==false`）的 `Subscription-Userinfo` 响应头中 `total=0`，导致 Clash 客户端不显示流量信息，只显示「远程」。
- **根因**：`total` 变量仅在 `isShare` 分支赋值，else 分支只累加 `up`/`down`，`total` 保持初始值 0。
- **状态**：已在本次审查前由主会话修复（`total = sub.TrafficLimit`），1.0.3 包含此修复。本报告记录备查。
- **证据**：用户实测截图显示「FLVX - 我的订阅」在修复前显示「远程」，修复后应显示流量。

### D-003 `externalProbe.Points` 字段新增（兼容性确认）

- **文件**：`go-backend/internal/http/handler/kernel_probe.go:34-35`
- **变更**：`externalProbe` 结构体新增 `Points []latencyPoint \`json:"points,omitempty"\`` 字段。
- **影响分析**：
  - JSON 序列化：`omitempty` 保证无数据时不输出，旧客户端解析不受影响 ✅
  - Agent 侧（go-gost）：`grep -rn "externalProbe" go-gost/` 无命中，agent 不直接使用此结构体 ✅
  - 既有 `inboundProbe` 行为：字段仅追加，无既有字段改名/删除/改类型 ✅
- **结论**：无回归风险，记录备查。

## 确认无问题的项（D1 回归检查）

| 检查项 | 结果 | 证据 |
|---|---|---|
| `control_plane.go` 变更 | 零变更 | `git diff 5e1edce8 HEAD -- "**/control_plane.go"` 为空 |
| `handler.go` 既有路由 | 仅增量，无修改/删除 | diff 显示仅新增 7 个移植路由 + 2 个结构体字段 + 2 处配置钩子 |
| 路由重复注册 | 无 | 187 个 `mux.HandleFunc`，`sort \| uniq -d` 为空 |
| nftables 逻辑 | 零变更 | `git diff` 按 `*nft*` 文件名过滤为空 |
| 转发/隧道 CRUD (`mutations.go`, `kernel.go`) | 零变更 | diff 为空 |
| 流量/配额 (`flow_*.go`, `user_quota.go`, `subscription.go` 计费部分) | 零变更 | diff 为空 |
| `jobs.go` | 仅增 `h.jobsCtx = ctx` 一行 | diff 确认 |
| 订阅流水线顺序（脚本 vs markChain） | 安全 | 实测：脚本改名后 `markChain` 仍通过 `Original` 正确匹配（`chainMatchers` 同时匹配 `Name` 和 `Original`，见 `sub_chain.go:47-54`） |
| `uniqueNames(..., rules)` | 正确 | `rules=true` 时预留规则组名避免冲突（`share.go:832-838`） |

## D2 计费正确性（代码审查 + 测试）

- **流量统计加减**：`flow_upload_batch.go`、`nftables_traffic.go` 零变更；相关测试通过。
- **配额耗尽暂停/恢复**：`user_quota.go`（`enforceUserQuotaIfNeeded`、`applyUserQuotaRelease`）零变更；测试通过。
- **单向/双向计费**：无相关代码变更。
- **限速生效**：`flow_policy*.go` 零变更。
- **订阅流量头**：见 D-002（已修复）。

## D3 转发/隧道 CRUD（代码审查）

- **端口冲突检测**：`kernel.go:849 inboundPortConflict`、`mutations.go:1545 validateTunnelEntryPortConflictsForNewEntriesTx` 零变更。
- **离线节点操作**：无相关代码变更。
- **批量操作回滚**：无相关代码变更。

## 未能验证的项

- **nftables 实际生效**：本机为 Linux VM 但无真实节点环境，`nft` 命令存在但无法模拟完整转发链路。代码零变更，风险低。需在真实节点验证（如有）。
- **隧道转发端到端**：需真实节点 + 隧道服务，未验证。代码零变更。
- **大订阅性能（1000+ 节点）**：未做压力测试。`ApplySubOptions` 为 O(n) 线性逻辑，`ApplySubScript` 有 1 秒超时保护，理论风险低。
- **PostgreSQL 迁移**：本机仅 SQLite。`latency_targets` 列的迁移修复（`repository.go` 显式 `AddColumn`）使用了 GORM Migrator API，PG 兼容性理论上无问题，但未实测。
