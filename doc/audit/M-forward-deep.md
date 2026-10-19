# 审查域 M 报告：转发/隧道/计费深度审计

## 概要
- 审查范围：端口转发、隧道转发、流量计费、限速、配额耗尽、中转端点生成
- 发现问题数：**P0 1 个** / P1 2 个 / P2 3 个 / P3 2 个
- 审查方式：代码审查 + 逻辑推演（无真机环境，部分项标记未验证）

---

## P0 阻断

### M-001 单向/双向计费逻辑完全错误
- **文件**：
  - `go-backend/internal/http/handler/nftables_traffic.go:446-454`
  - `go-backend/internal/http/handler/flow_upload_batch.go:83-84`
- **现象**：
  隧道 `Flow` 字段语义（前端 `tunnel.tsx:132`）：
  - `1`：单向计算（仅上传）
  - `2`：双向计算（上传+下载）
  
  但后端把 `tunnelFlow` 当乘数用：
  ```go
  // nftables_traffic.go:446-454
  scaledIn, ok := scaleNftTrafficBytes(delta.BytesIn, meta.TrafficRatio, meta.TunnelFlow)
  scaledOut, ok := scaleNftTrafficBytes(delta.BytesOut, meta.TrafficRatio, meta.TunnelFlow)
  // ...
  quotaDelta := scaledIn + scaledOut
  ```
  ```go
  // scaleNftTrafficBytes:503-519
  return scaled * tunnelFlow, true  // 直接乘以 tunnelFlow
  ```
  ```go
  // flow_upload_batch.go:83-84（gost 路径同样）
  scaledIn := int64(float64(item.D)*meta.TrafficRatio) * meta.TunnelFlow
  scaledOut := int64(float64(item.U)*meta.TrafficRatio) * meta.TunnelFlow
  ```
- **实际行为**：
  - Flow=1（单向）：quota = (in+out)*1 = in+out（应为仅 in，**多扣了下载流量**）
  - Flow=2（双向）：quota = (in+out)*2（**双倍扣费**）
- **期望**：
  - Flow=1：quotaDelta = scaledIn（仅上传）
  - Flow=2：quotaDelta = scaledIn + scaledOut
- **影响范围**：所有 nftables 和 gost 转发的流量计费。单向隧道用户被多扣费，双向隧道用户被双倍扣费。
- **修复方向**：
  ```go
  // scaleNftTrafficBytes 不应乘以 tunnelFlow，只做 ratio 缩放
  // 在调用处根据 mode 选择：
  var quotaDelta int64
  if meta.TunnelFlow == 1 {
      quotaDelta = scaledIn  // 单向：仅上传
  } else {
      quotaDelta = scaledIn + scaledOut  // 双向或默认
  }
  ```
  注意：`flowDeltas` 的 InFlow/OutFlow 明细仍应分别记录（用于展示），只有 `quotaDelta`（扣配额用）需要按模式选择。

---

## P1 严重

### M-002 端口冲突检测跳过损坏的入站
- **文件**：`go-backend/internal/http/handler/kernel.go:876-880`
- **代码**：
  ```go
  other, err := inboundFromModel(row)
  if err != nil {
      continue  // 静默跳过，无日志
  }
  ```
- **现象**：如果某个已存在的入站 JSON 配置损坏（settings/transport/security 字段非法），`inboundFromModel` 返回错误，冲突检测直接跳过该入站。新建入站可能与损坏入站的端口冲突而不被发现。
- **影响**：端口冲突漏检，可能导致服务启动失败或流量劫持。
- **修复方向**：记录错误日志，或返回错误中断创建流程（fail-closed）。

### M-003 `inboundEndpoints` 多处静默失败（订阅端点生成）
- **文件**：`go-backend/internal/http/handler/kernel_relay.go:412-466`
- **问题点**：
  1. **:414-416** `relayViews(0)` 失败仅 `log.Printf`，继续用空 map → **所有中转端点静默丢失**（与 F-002 同类）
  2. **:429-431** `inboundFromModel(row)` 失败时 `continue`，**零日志** → 单个入站损坏则从所有订阅消失（与 F-001 同类）
  3. **:434** `h.repo.GetNodeByID` 错误被忽略（`node, _`）→ 节点查询失败时 host 回退失效
  4. **:439-443** `host` 为空时静默跳过，无日志 → ShareHost 和 node.ServerIP 都为空时端点丢失
  5. **:450** `!rel.ForwardExists || rel.ForwardStatus != 1` 时 `continue`，无日志 → 中转未就绪时端点丢失
  6. **:452** `rel.Entries` 为空时循环不执行，无日志 → 隧道无入口时端点丢失
- **影响**：用户本次遇到的"订阅返回空"正是此类静默失败的集中爆发，排查极其困难。
- **修复方向**：每个 `continue` 前加 `log.Printf` 记录原因；或统一收集跳过原因，在 debug 模式下返回。

---

## P2 一般

### M-004 计数器重置时流量丢失
- **文件**：`go-backend/internal/http/handler/nftables_traffic.go:373-376`
- **代码**：
  ```go
  if sample.Bytes < oldBytes {
      stateInputs = append(stateInputs, stateInput)
      continue  // 直接丢弃本次 delta，不计数
  }
  ```
- **现象**：nftables 计数器重置（节点重启、规则重建）时，上次采样到重置之间的流量被丢弃，不计入账单。
- **影响**：少计费（对运营者不利），但属于计数器方案的已知局限。
- **备注**：难以完美解决，建议在文档中说明。

### M-005 配额暂停的 nft 路径存在 gap
- **文件**：`go-backend/internal/http/handler/flow_policy.go:437-448`
- **代码**：
  ```go
  if !h.reconcileNodeWithTimeout(nodeID, exclude, nil, 30*time.Second) {
      log.Printf("quota pause gap node_id=%d forward_ids=%v reason=reconcile_timeout_or_error", nodeID, ids)
      continue  // DB 状态未更新为 Paused！
  }
  ```
- **现象**：nftables reconcile 超时或失败时，DB 中的 forward 状态仍为 Active，但实际规则可能已部分移除。DB 与实际状态不一致。
- **影响**：下次计费周期会重试（自愈），但 gap 期间流量可能漏计或多计。
- **修复方向**：已记录 gap 日志，可接受；建议增加告警。

### M-006 整数溢出检查导致静默丢弃流量
- **文件**：`go-backend/internal/http/handler/nftables_traffic.go:393-401, 442-444`
- **现象**：多处 `if delta > math.MaxInt64 - x { continue }`，溢出时直接丢弃该转发的本次流量，无日志。
- **影响**：极端情况下流量丢失。实际中单次采样间隔的流量不可能接近 MaxInt64，风险极低。
- **备注**：防御性代码，可接受；建议加日志。

---

## P3 体验/备注

### M-007 限速是否生效未验证
- **文件**：`go-backend/internal/http/handler/control_plane.go:325-346`
- **说明**：限速通过 `SpeedID` 关联 `speed_limit` 表，control plane 下发到节点。代码路径存在，但无真机环境验证实际限速效果。
- **状态**：未验证，需真机测试。

### M-008 隧道状态同步延迟
- **说明**：隧道诊断显示成功，但 `ForwardStatus` 的更新可能有延迟。订阅端点生成依赖 `ForwardStatus == 1`，状态不同步时会导致端点缺失。
- **状态**：未验证，需观察生产环境。

---

## 已验证通过的项

1. **端口冲突检测主路径**：`inboundPortConflict` 的三层检查（端口段占用、节点转发段重叠、其他入站重叠）逻辑正确。
2. **隧道端口冲突**：`validateTunnelEntryPortConflictsForNewEntriesTx` 存在且被调用。
3. **配额暂停的 gost 路径**：`controlForwardServices` + `UpdateForwardStatus` 顺序正确。
4. **流量比率缩放**：`TrafficRatio` 的缩放逻辑正确（除 M-001 的 Flow 乘数 bug 外）。
5. **计数器方向过滤**：只处理 `to_target` / `from_target`，其他方向正确忽略。

---

## 未能验证的项

- nftables 真实节点上的计数器读取和规则下发（无真机）
- 限速实际效果（无真机）
- 大流量下的计数器溢出行为（理论分析）
- 隧道端到端建立流程（无真机）
- PostgreSQL 下的计费逻辑（代码与 SQLite 共用，应一致）

---

## 建议修复顺序

1. **M-001**（计费 bug）— 直接影响用户扣费，必须立即修复
2. **M-003**（静默失败）— 可观测性，影响排查效率
3. **M-002**（冲突检测跳过）— 边界情况，低频但高危
4. M-004/M-005/M-006 — 已知局限，文档说明即可
