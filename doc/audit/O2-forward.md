# O2 — 端口转发功能全量审计

**审计员**：端口转发专项
**基线**：main（1.0.7 之后，含 M-001 计费修复）
**参考手册**：`doc/guide/forward.md`
**日期**：2026-10-20
**方法**：代码审查 + 边界推演 + 现有测试基线（`go test -run TestForward` 通过）

## 手册对照

| 手册描述 | 实现 | 结论 |
|---|---|---|
| 协议支持 TCP/UDP | 每转发同端口同时建 `_tcp`/`_udp` 监听（设计基线，不区分 protocol） | ✅ 一致 |
| 入口：节点+监听端口 | `forwardCreate` 选隧道入口节点 + `inPort` | ✅ 一致 |
| 出口：目标 IP+端口 | `remoteAddr`，非管理员走 `IsSafeRemoteAddr` | ✅ 一致 |
| 隧道转发 | Tunnel Type=2，独立链路 | ✅ 一致 |
| 限速 | forward 级 `SpeedID` + 隧道级回退，fail-closed | ✅ 一致 |
| 计费模式单向/双向 | M-001 已修复（本轮复核无回归） | ✅ 一致 |

## 发现问题

### P1

**O2-001 — 转发入口端口无上限校验（port > 65535 可写入 DB）**
- **文件**：`go-backend/internal/http/handler/mutations.go`
  - `forwardCreate`（约 2265 行）：`port := asInt(req["inPort"], 0)` → 仅处理 `port <= 0`（自动分配），无 `> 65535` 拒绝
  - `forwardUpdate`（约 2432 行）：同样逻辑
- **复现**：`POST /api/v1/forward/create`，body `{"tunnelId":1,"name":"x","remoteAddr":"1.2.3.4:80","inPort":99999}` → 预检 `validateForwardPortAvailability`（`port <= 0` 直接返回 nil，但 99999 > 0 会继续查库，无冲突则通过）→ `CreateForwardTx` 写入 `forward_port(port=99999)` → 节点 gost 监听 99999 失败，DB 留坏数据
- **影响**：坏数据需手动清理；前端有端口范围校验（`forward.tsx:2146`），但 API 可绕过
- **建议**：在 `forwardCreate`/`forwardUpdate` 的 `port <= 0` 分支后加 `if port > 65535 → 报错`

### P2

**O2-002 — `scaleFlowByTunnel` 仍含 M-001 同类 bug（死代码）**
- **文件**：`go-backend/internal/http/handler/flow_policy.go:230-244`
```go
scaledIn := int64(float64(inFlow)*tunnel.TrafficRatio) * tunnel.Flow
scaledOut := int64(float64(outFlow)*tunnel.TrafficRatio) * tunnel.Flow
```
- 把 `tunnel.Flow` 当乘数，与 M-001 同一错误模式
- **现状**：全仓无调用方（`grep -rn scaleFlowByTunnel` 仅定义），死代码，当前无实际危害
- **建议**：按 M-001 语义修复或直接删除

**O2-003 — 端口冲突预检遗漏 `chain_tunnel` 表**
- **文件**：
  - `go-backend/internal/store/repo/repository_nft_pending_delete.go:226` `FindOtherForwardOnNodePortTx`：只查 `forward_port`
  - `go-backend/internal/store/repo/repository_control.go:158` `HasOtherForwardOnNodePortTx`：查 `forward_port` + `node_inbound`，不查 `chain_tunnel`
- **证据**：`migration_r5b4_v1.go:105` 建有 `idx_chain_tunnel_node_port(node_id, port)` 唯一索引，证明 chain_tunnel 确实在节点上占用监听端口
- **复现**：chain_tunnel 在 node1 占用 8080 → 创建转发 `inPort=8080` → 预检通过（两表都无记录）→ DB 唯一索引也拦不住（不同表）→ 节点上两个服务抢 8080，后启动的失败
- **建议**：两个预检函数都加上 `chain_tunnel` 的 `node_id+port` 查询

**O2-004 — `ReplaceForwardPorts` 未取 per-node 端口分配锁**
- **文件**：`go-backend/internal/store/repo/repository_mutations.go:941`
- `CreateForwardTx`（1464 行）有 `AcquirePortAllocLock`（R5-B4），但 `ReplaceForwardPorts`（转发更新端口时调用）无锁
- **后果**：并发更新两个转发到同一端口时，预检都通过，靠 `idx_forward_port_node_port` 唯一索引兜底 → 第二个报晦涩的 DB 错误而非"端口已被占用"，且 `forwardUpdate` 的回滚路径要处理
- **建议**：与 `CreateForwardTx` 一致，在 `ReplaceForwardPorts` 内按 nodeID 排序取锁

**O2-005 — 已禁用的限速规则仍被引用生效**
- **文件**：
  - `go-backend/internal/store/repo/repository_flow.go:375` `SpeedLimitExists`：`Where("id = ?", id)`，不查 `status`
  - `go-backend/internal/store/repo/repository_flow.go:405` `GetSpeedLimitSpeed`：同样不查 `status`
  - 调用方 `control_plane.go:328`：`GetSpeedLimitSpeed` 成功且 `> 0` 即下发限速
- **复现**：管理员在"限制与策略"页禁用一条限速规则（status=0）→ 引用它的转发在下次同步时仍被限速（预期：禁用=解除限速）
- **对比**：删除规则时 `GetSpeedLimitSpeed` 返回错误 → `limiterID` 为 nil → 不限速（fail-open，合理）；禁用却继续限速，前后不一致
- **建议**：两处查询加 `AND status = 1`，或 `normalizeSpeedLimitReference` 校验 status

**O2-006 — 配额耗尽边界不一致（用户级 vs 隧道级）**
- **文件**：`go-backend/internal/http/handler/flow_policy.go`
  - 用户级（约 285 行）：`if flowLimit < current` → 恰好用完（`==`）时**放行**
  - 隧道级（约 324 行）：`if utCurrent >= utFlowLimit` → 恰好用完时**拦截**
- **影响**：用户总额度恰好用尽时，创建转发：用户检查通过、隧道检查拦截，报错信息是"该隧道流量已超额"而非"流量已超额"，排查时误导
- **建议**：统一为 `>=`（超额即拦）

### P3

**O2-007 — `maxConn` 负值未归零（`ipMaxConn` 有）**
- **文件**：`mutations.go:2315`（create）、`2507`（update）
```go
maxConn := asInt(req["maxConn"], 0)       // 负值直接入库
ipMaxConn := asInt(req["ipMaxConn"], 0)
if ipMaxConn < 0 { ipMaxConn = 0 }        // 只有这个被归零
```
- **实际影响**：无（`buildConnLimiterConfigs` 只处理 `> 0`，负值等价于 0=不限）
- **建议**：给 `maxConn` 加同样的归零，保持一致

**O2-008 — `IsSafeRemoteAddr` 的 DNS 重绑定局限**
- **文件**：`go-backend/internal/http/handler/security_utils.go:16`
- 仅在创建/更新时解析 DNS 并拒绝内网 IP；若域名事后重绑定到内网 IP，检查被绕过
- 已知局限，单实例面板场景风险低，记录备查

**O2-009 — 多实例部署下端口预检是 best-effort**
- `validateForwardPortAvailability`（非 Tx 版）在 `forwardCreate`/`forwardUpdate` 中是事务外预检；`CreateForwardTx` 内靠 per-node 进程锁 + 唯一索引保证
- 单实例部署下安全；多实例（多面板共一库）时预检有竞态，靠唯一索引兜底
- 建议在文档注明"面板为单实例设计"

## 验证通过

1. **端口冲突 DB 约束**：`idx_forward_port_node_port(node_id, port)` 唯一索引存在（`migration_r5b4_v1.go:105`）
2. **创建并发安全**：`CreateForwardTx` 有 per-node 排序锁（R5-B4）+ 唯一冲突映射为"端口已被占用"（非 500）
3. **暂停/恢复/删除**：数据面优先（先改节点/规则再写 DB），nft 与 gost 双路径，失败有日志和后台重试（pending-delete 队列）
4. **配额自动暂停**：`enforceFlowPolicies` → `pauseUserForwards`/`pauseUserTunnelForwards`，nft 走 reconcile 排除、gost 走 `PauseService`，30s 超时记 gap 不阻塞采集循环
5. **限速 fail-closed**：节点离线时限速下发直接失败（R5R7-001），不静默跳过
6. **批量操作**：`forwardBatchDelete/Pause/Resume/Redeploy/ChangeTunnel` 按节点聚合 reconcile，一节点一次；部分失败逐项报告（success/fail/pending）
7. **remoteAddr SSRF 防护**：非管理员走 `IsSafeRemoteAddr`（DNS 解析 + 拒绝 loopback/private）
8. **普通用户权限**：不能设置/修改限速规则（`forwardCreate:2243`、`forwardUpdate` 内 `roleID != 0` 检查）；`resolveForwardAccess` 做越权校验
9. **M-001 无回归**：`nftables_traffic.go` / `flow_upload_batch.go` 的 Flow 语义修复仍在位；`scaleNftTrafficBytes` 已去乘数
10. **现有测试**：`go test ./internal/http/handler/ -run TestForward -count=1` 通过

## 未能验证

- 真实节点上的端口监听冲突（无节点环境）
- nftables 规则实际下发/残留（无 nft 环境；代码路径已审）
- 并发创建冲突端口的竞态（单机逻辑已审，锁+唯一索引双保险）

## 修复建议顺序

1. O2-001（P1，端口越界，API 可绕过前端）
2. O2-003（P2，chain_tunnel 端口冲突漏检）
3. O2-005（P2，禁用限速仍生效）
4. O2-002/O2-004/O2-006/O2-007（P2/P3，顺手修）
