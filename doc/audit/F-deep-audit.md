# 深度审计报告 F：数据库迁移与订阅数据流

## 概要
- 审计范围：数据库迁移系统性风险、订阅数据流完整性、订阅渲染边界条件、并发与数据一致性
- 审计方式：代码审查 + 实际运行测试（P0-1 端到端链路测试通过）
- 发现问题：**P1 2 个** / P2 3 个 / P3 2 个
- 前提：P0-1（subPipeline 缺字段）、P0-2（ruleGroups 未下发）、P1-1（备份缺 latency_targets）已修复并验证

## P1 严重

### F-001：`inboundFromModel` 失败时静默丢弃入站，零日志
- **文件**：`go-backend/internal/http/handler/kernel_relay.go:429-431`
- **代码**：
  ```go
  in, err := inboundFromModel(row)
  if err != nil {
      continue
  }
  ```
- **现象**：单个入站的 `settings`/`transport`/`security` JSON 损坏时，该入站从**所有**订阅中静默消失，没有任何日志。管理员无法察觉，只能看到订阅节点数变少。
- **复现**：`UPDATE node_inbound SET settings='{' WHERE id=X` 后访问该入站所在订阅，节点消失，`docker logs` 无任何输出。
- **期望**：至少 `log.Printf("inbound %d skipped: %v", row.ID, err)`，让运维能定位。
- **实际**：零日志，与用户本次遇到的"订阅返回空"属同一类静默失败。
- **影响范围**：所有订阅渲染路径（`openSubscription`、`subscriptionPreview`、`subscriptionView` 的 NodeCount）都走 `subEndpoints` → `inboundEndpoints`。
- **修复方向**：加日志；或在管理端入站列表标注"配置损坏"。

### F-002：`relayViews(0)` 失败时仅 log，全部中转端点静默丢失
- **文件**：`go-backend/internal/http/handler/kernel_relay.go:414-416`
- **代码**：
  ```go
  relays, err := h.relayViews(0)
  if err != nil {
      log.Printf("relay lookup failed: %v", err)
  }
  ```
- **现象**：`ListInboundRelays` 出错（DB 异常）时，`relays` 为 nil，`byInbound` 为空 map，**所有**中转端点被跳过，但直连端点正常返回。订阅表现"中转节点全部消失"，极难排查。
- **期望**：DB 错误应向上传播返回 500，而非降级为"部分数据"。
- **实际**：降级继续，仅一条 `log.Printf`（且用户 grep `error|fail` 时因大小写/措辞可能漏掉——实际关键词是 "failed"）。
- **影响范围**：同 F-001 的三条渲染路径。
- **修复方向**：`inboundEndpoints` 返回 error，调用方返回 500；或至少在响应头/日志中标记降级。

## P2 一般

### F-003：`parsePipeline` 未给新切片字段设默认值，读写路径不一致
- **文件**：`go-backend/internal/http/handler/subscription.go:102-111`
- **代码**：
  ```go
  func parsePipeline(raw string) subPipeline {
      var p subPipeline
      _ = json.Unmarshal([]byte(raw), &p)
      if p.Regions == nil { p.Regions = []string{} }
      if p.Protocols == nil { p.Protocols = []string{} }
      return p   // RuleGroups/ChainLandingNames/ChainRelayNames 保持 nil
  }
  ```
- **现象**：读旧数据（P0-1 修复前保存的 JSON，缺新字段）时，新字段为 nil；而 `normalize()`（保存路径）将其置为 `[]`。同一订阅在保存前后，`options()` 产出的 `SubOptions` 在 nil vs empty 上不一致；若某处将 pipeline 回显为 JSON，nil 序列化为 `null` 而非 `[]`，前端 `ruleGroups.length` 可能抛错（取决于前端判空写法）。
- **修复方向**：`parsePipeline` 中补齐三个字段的 `[]string{}` 默认值，与 `normalize()` 一致。

### F-004：`normalize()` 未用 `NormalizeRuleGroups` 过滤非法 key
- **文件**：`go-backend/internal/http/handler/subscription.go:59-99`；`go-backend/internal/kernel/sub_rules.go:93`
- **现象**：`kernel` 已有 `NormalizeRuleGroups`（保留已知 key、按展示顺序排列），但保存路径未调用。恶意/误传的未知 key 会存入 DB。
- **实际影响**：渲染时 `enabledRuleGroups` 用 `toSet` 匹配已知 def，未知 key 被静默忽略——fail-safe，无功能故障，属数据卫生问题。
- **修复方向**：`normalize()` 中加 `p.RuleGroups = kernel.NormalizeRuleGroups(p.RuleGroups)`。

### F-005：`SaveSubscription` 无事务的 read-modify-write 竞态
- **文件**：`go-backend/internal/store/repo/repository_subscription.go:196-206`
- **代码**：`r.db.Create(s)` / `r.db.Omit("upload_bytes","download_bytes","traffic_reset_time").Save(s)`，单语句原子，但 handler 层是"读→改→存"全行覆盖。
- **现象**：两名管理员同时编辑同一订阅的不同字段，后保存者覆盖前者的修改（last-writer-wins 整行）。
- **缓解**：流量计数器三列被 `Omit` 保护，不会被 stale read 清零；`Token` 有唯一索引，128bit 随机碰撞可忽略。
- **评级 P2**：概率低（需两人同时编辑同一订阅），后果为配置丢失而非数据损坏。

## P3 体验/备注

### F-006：`normalize()` 脚本长度检查与 `ValidateSubScript` 重复
- **文件**：`subscription.go:94-96` vs `sub_options.go:126` → `sub_script.go:50`
- 无害的纵深防御，保留亦可。

### F-007：`GetNodeByID` 错误被忽略，直连端点可能静默跳过
- **文件**：`kernel_relay.go:435`：`node, _ = h.repo.GetNodeByID(row.NodeID)`
- 若节点行缺失且 `ShareHost` 为空，直连端点不生成且无日志。与 F-001 同类，建议合并处理。

## 已验证通过的项（实测证据）

1. **P0-1 端到端链路**：自写测试 `TestPipelineNewFieldsRoundTrip` 通过 —— 含 5 新字段的 JSON → `subPipeline` → `options()` → `json.Marshal`（DB）→ `json.Unmarshal` → 字段完整；`parsePipeline` 解析旧 JSON（无新字段）不 panic。✅
2. **迁移 skip 移除无副作用**：`model.Node`/`Tunnel`/`Forward` 三个 struct 均无 `uniqueIndex` tag，`AutoMigrate` 不会因重复数据建索引失败；GORM AutoMigrate 不删列，只加列。✅
3. **订阅 Options 备份**：`SubscriptionBackup.Options` 存原始 JSON 字符串（`repository_inbound_backup.go:84`），新字段自动保留，无需 struct 变更。✅
4. **SubOptions 构造点唯一**：全仓仅 `subscription.go:52` 一处从用户输入构造 `kernel.SubOptions`，P0-1 修复无遗漏。✅
5. **订阅测试套件**：`go test ./internal/kernel/ -run "TestSub|TestShare|TestClash|TestApply"` 通过；`go build ./...` 通过。✅
6. **B 域联动确认**：`autoMigrateAll` 的显式 `latency_targets` 兜底列（`repository.go:390-396`）经 B 域实测有效；44 张表 vs models 列表无遗漏。✅

## 未能验证的项
- PostgreSQL 下新列迁移（无 PG 环境；代码路径为整包 `AutoMigrate`，理论 covered）
- 双管理员并发编辑同一订阅的竞态复现（概率低，未构造）
- 前端对 503 的展示（`openSubscription` 面向 Clash 等外部客户端；面板内预览走 JSON code 路径，不受影响）

## 建议修复顺序
1. F-001 + F-002 + F-007（同一函数内的三处静默失败，合并为一次"订阅渲染可观测性"修复）
2. F-003（读写一致性，一行级修复）
3. F-004（数据卫生）
4. F-005（低概率竞态，可延后）
