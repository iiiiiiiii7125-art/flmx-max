# 审查域 B 报告：数据库与迁移

## 概要

- 审查范围：`go-backend/internal/store/repo/repository.go`（迁移）、`repository_subscription.go`、`repository_inbound_backup.go`、`internal/store/model/` 全量 struct
- 发现问题数：P0 0 个 / P1 1 个 / P2 1 个 / P3 2 个
- 实际执行的测试：
  1. 构造无 `latency_targets` 列的旧 SQLite 库，调用 `repo.Open()`，验证列自动加上、旧数据完好、默认值为 `""` —— **通过**
  2. `go test ./internal/store/repo/ -count=1` 全量 —— **通过**（8.9s）
  3. 全量 struct（44 张表）vs `autoMigrateAll` models 列表逐项对照 —— **无遗漏**

---

## P1 严重

### B-001 备份导出丢失 `latency_targets`，恢复后节点测试点配置静默丢失

- 文件：
  - `go-backend/internal/store/repo/repository.go:2192`（`exportNodes`）
  - `go-backend/internal/store/model/model.go:597`（`NodeBackup` struct）
- 现象：`exportNodes` 逐字段拷贝 `model.Node` → `model.NodeBackup`，但未拷贝 `LatencyTargets`；`NodeBackup` struct 也没有对应 JSON 字段。
- 复现步骤：
  1. 给某节点设置自定义延迟测试点（`node.latency_targets` 非空）
  2. 导出备份 → 检查 JSON，`nodes[]` 条目无测试点字段
  3. 恢复备份 → 该节点的自定义测试点丢失，静默回退为全局默认
- 期望：备份包含 `latency_targets`，恢复后保留
- 实际：备份中无此字段，恢复后丢失且无任何提示
- 影响范围：所有使用节点自定义测试点的用户；备份/恢复流程
- 修复方向：`NodeBackup` 加 `LatencyTargets string \`json:"latencyTargets,omitempty"\``，`exportNodes` 拷贝 `b.LatencyTargets = n.LatencyTargets`，`importInbounds` 恢复时写回（注意 `node` 表恢复路径）

---

## P2 一般

### B-002 SQLite 下 Node/Tunnel/Forward 整表跳过 AutoMigrate 是系统性风险，未来新字段会静默丢失

- 文件：`go-backend/internal/store/repo/repository.go:360-383`
- 现象：`autoMigrateAll` 中，若 `node`/`tunnel`/`forward` 表已存在，则整表 `continue` 跳过 `AutoMigrate`。该逻辑自 v1.0.0 初始提交即存在（`git log -L` 确认），无注释说明原因。
- 证据：本次移植新增 `Node.LatencyTargets`（`model.go:99`）即因此未被迁移，导致生产环境 `no such column: latency_targets`（用户现场已复现）。
- 当前状态：父代理已用 point-fix 覆盖（`repository.go:394-398`，`HasColumn` 检查 + `AddColumn`），实测有效（见 B2）。
- 为什么是 P2 而非 P0：当前已知字段已修复；风险在于**未来**任何加到这三张表的字段都会重蹈覆辙，且失败是静默的（启动不报错，运行时 SQL 才炸）。
- 修复方向（二选一）：
  1. 把 `prepareSQLiteLegacyColumns`（`:447`）升级为通用机制：对 Node/Tunnel/Forward 逐字段 `HasColumn` 检查并 `AddColumn`，而不是维护硬编码字段名列表；
  2. 或在每次新增字段时同步更新该列表（当前做法，依赖人工不遗漏）。

---

## P3 体验/可维护性

### B-003 `latency_targets` 迁移修复未放入既有 `prepareSQLiteLegacyColumns` 模式

- 文件：`go-backend/internal/store/repo/repository.go:394-398`
- 现象：仓库已存在成熟的逐字段补列模式 `prepareSQLiteLegacyColumns`（`:447-487`，在 `autoMigrateAll` 之前运行，覆盖 Node/Tunnel/Forward 的 20+ 个历史字段），但本次修复另起一段 one-off 代码放在 `migrateV1PortUserUnique` 之后。
- 影响：功能正确（已实测），但后续维护者需要在两个地方找补列逻辑，容易遗漏。
- 修复方向：将 `"LatencyTargets"` 并入 `prepareSQLiteLegacyColumns` 的 Node 字段列表，删除 `:394-398` 的重复代码。注意字段名风格统一（该列表用 Go 字段名如 `"ServerIPV4"`，GORM `HasColumn`/`AddColumn` 均支持）。

### B-004 `SaveSubscription` 无事务（备注，非缺陷）

- 文件：`go-backend/internal/store/repo/repository_subscription.go:196-205`
- 现象：`SaveSubscription` 是单条 `Create`/`Save`，无事务包裹；handler 层 `SaveSubscription` + `applyKernelsForInbounds` 分两步，无原子性。
- 结论：**不判为缺陷**。单行写由 DB 保证原子；保存与下发分离是 F7X-001 的有意设计（下发失败靠 sync 循环重试，不回滚已保存的配置）。记录备查。

---

## 已验证通过的项（无问题）

| 检查点 | 结论 | 证据 |
|---|---|---|
| B1.1 models 列表覆盖度 | 44 张表全覆盖 + `NodeSeq`，无遗漏 | `comm` 对照 `TableName()` 列表 vs models 列表，差集为空 |
| B1.2 `latency_targets` 迁移有效性 | 有效 | B2 实测：旧库 Open 后列存在、数据完好、默认值 `""` |
| B1.3 `currentSchemaVersion` | 无需递增 | 新列走 AutoMigrate，不走版本化数据迁移；`=6` 正确 |
| B1.4 `migrateSchema` 早退 | 不影响列迁移 | 早退仅跳过版本化数据迁移（`:3145`），AutoMigrate 独立运行 |
| B2 旧库升级 | 通过 | 实测见上；`SchemaVersion`/`NodeSeq` 正常初始化 |
| B2 PostgreSQL | 未验证（环境无 PG） | — |
| B3 Token 唯一冲突 | 风险可忽略 | `randomHex(16)` 128bit，`uniqueIndex` 为安全网 |
| B3 `DeleteSubscription` | 有事务 | `repository_subscription.go:207-216` |
| B4 订阅 Options 备份 | 正常 | `SubscriptionBackup.Options` 存原始 JSON（`repository_inbound_backup.go:80-86`），新字段随 JSON 不透明备份 |
| B4 旧备份恢复默认值 | 正常 | 缺字段时由 DB `DEFAULT ''` 填充，语义为"跟随全局"，正确 |

---

## 未能验证的项

- **PostgreSQL 升级路径**：本机无 PG 环境，`OpenPostgres` 路径的列迁移未实测。SQLite 的跳过逻辑是 `db.Dialector.Name() != "sqlite"` 分支之外，PG 走正常 `AutoMigrate`，理论上无此问题，但未实际验证。
- **并发 Token 冲突**：128bit 随机冲突概率可忽略，未做并发压测。
- **SQLite 长事务阻塞**：`MaxOpenConns(1)` 为设计基线（见判定基线），属已知权衡，未压测。

---

## 给审查员 A 的联动信息

- P0-1（`subPipeline` 丢字段）修复后，注意 `subscription.options` 列存的是完整 JSON，备份恢复不受影响；但**恢复旧备份到新版本时**，旧 JSON 缺新字段，`parsePipeline` 需补默认值（建议 A 在 A1 中覆盖）。
