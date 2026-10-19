# 通知系统全量审计（O8）

- 审计员：notify-auditor（子代理）
- 时间：2026-10-20
- 基线：main @ 1.0.7（含 M-001 等修复后）
- 参考手册：`doc/guide/notify.md`
- 范围：`go-backend/internal/http/handler/notify.go`（448 行）、`notify_reports.go`（276 行）、`kernel_probe.go`（导入节点探测）、`kernel.go:1561`（内核错误）、`vite-frontend/src/pages/config/notify-card.tsx`、合同测试 `tests/contract/notify_contract_test.go`
- 结论：**P0 0 / P1 0 / P2 2 / P3 4**。核心链路（SSRF 防护、去重、鉴权、敏感配置）扎实；问题集中在**状态全放内存**导致的重启语义缺口。

---

## P2

### O8-001 面板重启时，已掉线节点永不告警（漏报）

- **文件**：`go-backend/internal/http/handler/notify.go:322-344`（`notifyNodeOffline`）、`go-backend/internal/http/handler/handler.go:151-152`
- **复现步骤**：
  1. 节点 A 在线，通知已启用并勾选"节点掉线 / 恢复"。
  2. 节点 A 断开连接（`SetNodeOfflineHook` 触发 `notifyNodeOffline`，90s 定时器 pending）。
  3. 在 90s 内重启面板（或 90s 后、告警已发出后重启——两种情况都丢）。
  4. 重启后节点 A 一直不恢复。
  5. **结果**：永远收不到"节点掉线"告警。`offlineTimers`/`offlineAlerted` 是 `notifier` 的内存 map，重启即清空；重启后该节点在本进程内从未"断开"过，没有事件能再次触发 `notifyNodeOffline`。
- **影响**：面板升级/重启窗口内掉线的节点会被静默漏报，管理员只能靠每日汇总里的"节点在线：X / Y"发现。
- **手册对照**：手册称"节点断开超过 90 秒才提醒"，未说明重启语义；但从运维角度，重启不应吞掉正在发生的故障告警。
- **建议**：启动时对超过 90s 未见心跳的节点补一次 `notifyNodeOffline`（或把 `offlineAlerted` 持久化）。

### O8-002 通知发送无重试，失败即丢失

- **文件**：`go-backend/internal/http/handler/notify.go:150-162`（`notify`）、`notify_reports.go:213-224`（`maybeDailyReport`）
- **复现步骤**：
  1. 启用通知，Webhook 指向一个会超时/5xx 的地址（或面板出网瞬断）。
  2. 触发任一事件（节点掉线/测试通知）。
  3. **结果**：`notify()` 起 goroutine 发送，`send` 返回 err 后仅 `log.Printf("notify %s failed: %v")`，无重试、无持久化队列，告警丢失。
  4. 每日汇总更甚：`maybeDailyReport` 先 `UpsertConfig(notify_daily_last, today)` **再** `h.notify(...)`——发送失败当天不会再试。
- **手册对照**：手册未承诺重试；但"边界条件：通知失败重试"在任务书中被明确要求审查，故列为 P2。
- **建议**：至少对 `maybeDailyReport` 改为"发送成功后再标记"（`notify` 是异步的，可改为同步 `send` 后再落盘）；或引入有限重试（3 次、指数退避）。

---

## P3

### O8-003 删除节点时未清理 notifier 状态（幽灵告警 + 内存泄漏）

- **文件**：`go-backend/internal/http/handler/notify.go:322-344`、`go-backend/internal/http/handler/mutations.go:5058`（`deleteNodeByID`）
- **复现步骤**：
  1. 节点 A 断开，90s 定时器 pending（`offlineTimers[A]` 存在）。
  2. 在面板删除节点 A（`deleteNodeByID` 全程未触碰 `h.notifier`——已用 grep 确认零引用）。
  3. 定时器触发：`IsNodeOnline(A)` 为 false → `offlineAlerted[A]=true` → 发送"节点掉线"，`nodeLabel(A)` 查库失败退化为 `"节点 {id}"`。
  4. **结果**：给已删除的节点发了一条无意义的掉线告警；`offlineTimers`/`offlineAlerted` 的条目永久残留（map 无上限，长期运行缓慢泄漏）。
- **建议**：`deleteNodeByID` 内调用 `h.notifier` 清理该节点定时器与标记（需给 `notifier` 加一个 `forgetNode(id)` 方法）。

### O8-004 已过期的节点/用户/订阅每次重启后重新告警

- **文件**：`notify.go:356-390`（`checkNodeExpiry`）、`notify_reports.go:60-124`（`checkUserReminders`）
- **复现步骤**：
  1. 某节点 `expiry_time` 为一个月前（`ExpiryReminderDismissed=0`）。
  2. `checkNodeExpiry`：`left := expiry.Sub(now)` 为负，`left > notifyExpiryWindow`（3 天）为 false → **不跳过** → `expiryAlerted` 内存去重（进程内只发一次）→ 发送"节点已到期"。
  3. 重启面板 → `expiryAlerted` 清空 → 再次发送"节点已到期"。
  4. 用户侧同理：`checkUserReminders` 的 `exp:{id}:{expTime}` key、`sub:{id}:{expTime}` key 均为内存去重；`time.UnixMilli(u.ExpTime).Sub(now) <= window` 对早已过期的用户同样成立。
- **手册对照**：手册只说"到期前 3 天内提醒一次"，未定义"已过期很久"的行为；当前实现把"已过期"当作需要反复提醒的事项，但去重又只在内存里，导致语义 = "每次重启 spam 一次"。
- **建议**：二选一并写进手册——(a) 已过期超过 N 天的不再提醒；(b) 去重持久化（config 表存 `notify_expiry_sent:{kind}:{id}:{expTime}`）。

### O8-005 内核错误通知无"同一问题只提醒一次"去重

- **文件**：`go-backend/internal/http/handler/kernel.go:1556-1562`
- **代码**：
  ```go
  if newStatus == kernelStatusError && k.Status != kernelStatusError {
      ...
      h.notify(notifyEventKernelError, "内核运行出错", msg)
  }
  ```
- **问题**：仅靠"状态翻转"去重。内核在 error↔ok 间抖动（agent 上报不稳定、配置反复应用失败）时，每次翻回 error 都发一条，没有类似 `alertOnce` 的收敛。节点掉线有 `offlineAlerted`、流量有 `alerted`，唯独内核错误没有。
- **手册对照**：手册"同一个问题只提醒一次，恢复后才会再次提醒"——抖动场景下"恢复"可能只是 agent 的一次误报，会导致刷屏。
- **建议**：给 kernel_error 加 `alertOnce("kerr:{nodeID}:{kernelType}")`，在状态恢复 ok 时 `clearAlerts`。

### O8-006 `notify_events="none"` 靠命名巧合实现"全不选"

- **文件**：`vite-frontend/src/pages/config/notify-card.tsx:108`、`go-backend/internal/http/handler/notify.go:133-147`
- **问题**：前端取消全选时存字面量 `"none"`；后端 `notifyConfig` 把它 split 后逐个比对事件 key，"none" 恰好不等于任何事件 → 禁用。功能正确，但属于脆弱的隐式约定：未来若新增 key 为 `none` 的事件会静默启用；且 `"none,node_offline"` 这种中间态（先全不选再勾一个）会把无意义的 `"none"` 永久留在配置里。
- **建议**：后端在 `notifyConfig` 里显式识别 `"none"`（或前端全不选时存空字符串 + 单独的 `notify_enabled` 已可表达，实际当前 `notify_enabled=false` 就够了）。

---

## 已验证通过（手册逐项对照）

| 手册条目 | 实现 | 结论 |
|---|---|---|
| 节点掉线超 90s 才提醒，恢复再报 | `nodeOfflineGrace=90s` 定时器；`notifyNodeOnline` 仅当 `offlineAlerted` 时发恢复 | ✅ |
| 导入节点连续 2 次失败（约 6 分钟） | `notifyInboundDownStreak=2`，`externalProbeInterval=3min`；`track()` 状态机；恢复通知 | ✅（合同测试 `TestNotifyImportedNodeDownContract` 覆盖：一次失败不报、持续只报一次、恢复报、未勾选事件不报） |
| 内核出错附带错误信息 | `kernel.go:1561`，错误截断 300 字 | ✅（去重见 O8-005） |
| 节点到期前 3 天提醒一次，可在节点页关闭 | `notifyExpiryWindow=3*24h`；`expiryAlerted[id]==expTime` 去重；`ExpiryReminderDismissed==1` 跳过 | ✅（重启语义见 O8-004） |
| 用户流量 90%/用完、3 天内到期；分享订阅同理 | `checkUserReminders` + `checkSubQuota`；`alertOnce("flow:{id}:warn/full")`；低于 90% 时 `clearAlerts` 恢复 | ✅ |
| 每日汇总定时发送 | `maybeDailyReport`：整点匹配 + `notify_daily_last` 持久化防重发；`notify_daily_hour` 前后端双重 0–23 校验 | ✅（失败语义见 O8-002） |
| 同一问题只提醒一次 | `offlineAlerted` / `alerted` / `expiryAlerted` 三套内存去重 | ✅（持久化缺口见 O8-001/O8-004） |
| Webhook JSON 字段 | `event/title/message/text/time/app`，`text=[App] Title\nMessage\n时间：...` | ✅ 与手册示例一致；`event` 枚举含手册列出的 6 种 + `test` |
| 返回 2xx 视为成功 | `sendWebhook`：`status<200 \|\| >=300` 即失败 | ✅ |
| Telegram 反代地址 | `notify_telegram_api_base` 为空回退 `https://api.telegram.org`；`sendTelegram` 拼 `<base>/bot<Token>/sendMessage` | ✅ |
| 发送测试用未保存的表单值 | `notifyTest` 以请求体优先、缺省回退已保存配置 | ✅（合同测试覆盖） |
| Bot Token/Webhook 仅管理员可见、不进备份 | `IsSensitiveConfigKey` 含两 key；`handler.go:537/1129/1172` 非 admin 过滤；备份过滤 | ✅（合同测试覆盖 token 不泄漏） |
| Webhook SSRF 防护（LAD26-001 复审） | `validateNotifyURL`：拒非 http/https、云元数据地址（含 IP 字面量与 DNS 解析后逐 IP 检查）；`CheckRedirect` 同样校验、最多 5 跳（R5R26-001） | ✅ 复审通过 |
| URL 脱敏（LAD26-017 复审） | `post()` 错误路径返回固定文案（"通知地址无效/请求超时/无法连接"），不含 URL/token；校验错误（"通知地址…"开头）可透出 | ✅ 复审通过；合同测试断言失败信息不含 token |
| 事件开关：空 = 全开 | `notifyConfig`：`notify_events` 为空跳过过滤；前端全选时存 `""` | ✅ |
| 非 admin 调 `/api/v1/notify/test` 被拒绝 | 中间件鉴权 | ✅（合同测试覆盖） |

**说明**：
- 任务书提到"Bark/邮件"渠道：实际代码与手册中**没有** Bark/邮件原生渠道，只有 Telegram + Webhook 两种；Bark/邮件是通过 Webhook 的 `text` 字段转发对接的（手册原文如此）。实现与手册一致，无缺失。
- 合同测试 `TestNotifyTestEndpointContract`、`TestNotifyImportedNodeDownContract` 均通过（实测）。
- 未发现 P0/P1：通知是"尽力而为"的辅助通道，无认证绕过、无 SSRF 绕过、无数据损坏类问题。

---

## 未能验证

- 真实 Telegram Bot 发送（无外网凭据；`sendTelegram` 的请求构造与合同测试的 mock server 断言一致）。
- 面板重启场景（O8-001/O8-004）为代码路径推导，未做真机重启实测。
- 高频抖动下的内核错误刷屏量级（O8-005）为逻辑推导。
