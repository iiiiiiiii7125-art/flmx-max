# 审计汇总报告

## 概要
- 审查域：A 订阅 / B 数据库 / C 节点 / D 转发 / E 前端
- 发现问题：**P0 3 个** / P1 1 个 / P2 5 个 / P3 6 个
- 审查方式：五域并行，实际运行测试验证

## P0 阻断（必须修复）

### P0-1：订阅规则/脚本在 HTTP 层被完全丢弃
- **文件**：`go-backend/internal/http/handler/subscription.go:30-52`
- **现象**：`subPipeline` 结构体只有 13 个字段，缺失 `Script`、`Rules`、`RuleGroups`、`ChainLandingNames`、`ChainRelayNames`。前端发送这些字段时，JSON 反序列化静默丢弃。
- **影响**：订阅规则引擎、订阅脚本、链式多选三个移植功能在 Web 层完全不可用。
- **报告**：A-subscription.md、E-frontend.md（E-001）
- **修复方向**：补结构体字段 + 补 `options()` 映射 + 补 `normalize()` 校验 + 补 `parsePipeline` 默认值

### P0-2：订阅列表接口未下发规则分组清单
- **文件**：`go-backend/internal/http/handler/subscription.go:447-450`
- **现象**：前端从 `subRes.data.ruleGroups` 读取分组清单，但后端响应只有 `items`/`regions`，导致复选框永不渲染。
- **报告**：E-frontend.md（E-002）
- **修复方向**：在订阅列表响应中加入 `ruleGroups: kernel.RuleGroups()`

### P0-3：（预留，如有补充）

## P1 严重

### P1-1：备份导出丢失 `latency_targets`
- **文件**：`go-backend/internal/store/repo/repository.go:2192`
- **现象**：`exportNodes` 未拷贝 `LatencyTargets` 字段，`NodeBackup` struct 无对应字段。备份恢复后节点自定义测试点静默丢失。
- **报告**：B-database.md（B-001）
- **修复方向**：`NodeBackup` 加字段 + `exportNodes` 拷贝 + 恢复时写入

## P2 一般

| 编号 | 问题 | 文件 |
|---|---|---|
| P2-1 | SQLite 下 Node/Tunnel/Forward 整表跳过 AutoMigrate 是系统性风险 | repository.go:360-383 |
| P2-2 | `normalize()` 未校验新增字段长度 | subscription.go |
| P2-3 | 脚本失败时静默使用旧结果，无法区分"无脚本"和"脚本失败" | sub_script.go |
| P2-4 | 二次调优时内核拒绝写入会删除历史备份 | tuning.go:240 |
| P2-5 | 空订阅状态码变更（200+空 → 503）需在发版说明标注 | kernel_relay.go:609 |

## P3 体验/备注

| 编号 | 问题 |
|---|---|
| P3-1 | 脚本错误信息无行号 |
| P3-2 | 非分享订阅流量头 `total` 恒为 0（已在 1.0.3 修复） |
| P3-3 | `inboundProbeView` 强制 OK 掩盖总体状态 |
| P3-4 | 调优写文件前未 MkdirAll |
| P3-5 | 配置文件注释 "FLYX" 拼写 |
| P3-6 | DB_PATH 未设置时镜像缓存走临时目录 |

## 建议修复顺序

1. **P0-1**（订阅字段丢弃）— 阻断所有新功能，必须首先修复
2. **P0-2**（规则分组清单）— 与 P0-1 同属订阅功能，可一并修复
3. **P1-1**（备份丢失）— 数据完整性问题
4. P2-1（迁移系统性风险）— 长期隐患，建议重构迁移逻辑
5. 其余 P2/P3 按需修复

## 未能验证的项

- PostgreSQL 升级路径（无 PG 环境）
- nftables 真实节点、隧道端到端、大订阅压力测试（环境限制）
- 前端页面实际渲染（无浏览器环境，需人工确认）
- 真实节点调优、路由追踪、镜像下载（沙箱 DNS 拦截）
