# 审查域 I 报告：数据库迁移复审（re-audit）

## 概要
- 审查范围：P1-1（备份 `latency_targets`）与 P2-1（移除三表跳过逻辑）修复的完整性
- 发现问题数：**P1 1 个 / P2 3 个 / P3 1 个**（均为新发现，P1-1/P2-1 本体修复验证通过）
- 实测：构造缺列旧库 → `repo.Open()` 迁移验证通过

## 修复验证（逐项确认）

### ✅ 1. `autoMigrateAll` 不再跳过三张表
- 文件：`go-backend/internal/store/repo/repository.go:360-366`
- 确认：`hasNode`/`hasTunnel`/`hasForward` 的 `continue` 跳过逻辑已删除，现为无条件 `db.AutoMigrate(item)`。
- 风险评估：三 model 均无 `uniqueIndex` tag（B 审查员已确认），移除跳过无副作用。

### ✅ 2. 显式补列逻辑正确
- 文件：`repository.go:368-375`
- 代码：
  ```go
  if m.HasTable(&model.Node{}) {
      if !m.HasColumn(&model.Node{}, "latency_targets") {
          if err := m.AddColumn(&model.Node{}, "latency_targets"); err != nil {
              return err
          }
      }
  }
  ```
- 确认：`HasTable` + `!HasColumn` + `AddColumn` 三段式正确，失败时返回错误中断启动（fail-fast，符合预期）。

### ✅ 3. `NodeBackup` 有 `LatencyTargets` 字段
- 文件：`go-backend/internal/store/model/model.go:624`
- `LatencyTargets string \`json:"latencyTargets,omitempty"\`` ✅

### ✅ 4. `exportNodes` 拷贝了该字段
- 文件：`repository.go:2218`
- `b.LatencyTargets = n.LatencyTargets` ✅

### ✅ 5. `importNodes` 写入和更新列包含 `latency_targets`
- 文件：`repository.go:2658`（写入）、`repository.go:2666`（`DoUpdates` 列表含 `"latency_targets"`）✅

### ✅ 6. 实测：缺列旧库迁移
测试方法：新建库 → 插入 1 条 node 数据 → `ALTER TABLE node DROP COLUMN latency_targets` 模拟旧库 → `repo.Open()` → 验证。

实测输出（`go test -run TestAuditReaudit_LatencyTargetsMigration`）：
```
旧库已构造：latency_targets 列不存在
迁移后 latency_targets 列存在: true
数据完好: name=oldnode server_ip=1.2.3.4 latency_targets=""
--- PASS (0.29s)
```
- 列被加上 ✅
- 旧数据完好（name/server_ip 未损坏）✅
- 旧行默认值 `""`（符合 `not null default ''`）✅
- 测试文件已删除，未污染仓库。

### ✅ 7. 其他新增字段遗漏检查
- `SubscriptionBackup` 存 `Options` 原始 JSON，P0-1 修复后新字段自动保留 ✅
- 但发现以下**真缺失**（见新发现问题）。

## 新发现问题

### P1-1：`NodeBackup` 缺 `ForwardMode`，备份恢复后节点转发模式丢失
- 文件：`model.go:597`（`NodeBackup`）、`model.go:93`（`Node.ForwardMode`）
- 现象：`Node` 有 `forward_mode` 列（`agent`/`nftables`，默认 `agent`），`NodeBackup` 无对应字段，`exportNodes`/`importNodes` 均未处理。
- 影响：备份恢复后，nftables 模式节点被静默重置为 `agent` 模式，转发行为改变。
- 证据：`repository.go:480-481` 有该列的显式迁移（说明是后加字段，同样走过"加列漏备份"的老路）；`repository.go:913` 消费 `n.ForwardMode`。
- 修复方向：`NodeBackup` 加 `ForwardMode` 字段 + export/import 补齐。

### P2-1：`ForwardBackup` 缺 `MaxConn`
- 文件：`model.go:656`（`ForwardBackup` 有 `IPMaxConn` 但无 `MaxConn`）、`model.go:49`（`Forward.MaxConn`）
- 影响：恢复后转发的最大连接数限制丢失（重置为 0=不限），限流策略失效。
- 修复方向：同上。

### P2-2：`UserBackup` 缺 `MaxConn` 和 `PasswordChangedAt`
- 文件：`model.go:576`（`UserBackup`）、`model.go:27-28`（`User.PasswordChangedAt`/`MaxConn`）
- 影响：恢复后用户的最大连接数限制丢失；`PasswordChangedAt` 丢失可能影响"强制改密"类安全逻辑的判断（M-05 相关）。
- 修复方向：同上。

### P2-3：`NodeBackup` 缺 `ExpiryReminderDismissed`
- 文件：`model.go:597` vs `model.go:97`
- 影响：恢复后已忽略的到期提醒会再次弹出（体验问题）。
- 修复方向：同上。

### P3-1：系统性建议——新增 model 字段时缺乏"备份对照"检查项
- 现象：`latency_targets`、`forward_mode`、`ForwardMode`（备份）三次重复同类遗漏。
- 建议：在开发规范中增加检查项——"新增 model 字段时，必须同步检查对应 `*Backup` struct、export/import 函数"；或在 CI 加一个反射测试，自动对比 model 字段与备份字段的差集。

## 未能验证的项
- PostgreSQL 下的迁移路径（本机无 PG 环境）。
- 生产库真实升级（仅用构造的 SQLite 旧库实测）。

## 结论
P1-1 与 P2-1 的本体修复**验证通过**，实测有效。但复审发现备份体系存在**系统性字段遗漏**（P1-1/P2-1/P2-2/P2-3），建议与 P0-1 一并在下个版本修复，并采纳 P3-1 的防复发机制。
