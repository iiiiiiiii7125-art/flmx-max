# 审查域 A 报告：订阅全链路

## 概要
- 审查范围：订阅配置读写闭环、渲染正确性、脚本安全性、流量配额、订阅分配、对外端点
- 发现问题数：**P0 1 个** / P1 0 个 / P2 2 个 / P3 1 个
- 实际执行的测试：
  - Go 单元测试：`TestScriptTimeoutActual`（死循环脚本 1.004s 超时）、`TestScriptFieldEscape`（字段无逃逸）、`TestScriptInvalidIDSkipped`（非法 ID 跳过）——全部通过
  - 内核测试套件：`go test ./internal/kernel/` 通过
  - 代码审查：`subPipeline` vs `SubOptions` 字段逐项对照、`ApplySubOptions` 流水线顺序、`subURLSig` HMAC、`subOverQuota`/`lastResetAt` 逻辑

## P0 阻断

### A-001 订阅规则与脚本在 HTTP 层被完全丢弃
- 文件：`go-backend/internal/http/handler/subscription.go:30-52`
- 现象：前端发送的 `rules`、`ruleGroups`、`script`、`chainLandingNames`、`chainRelayNames` 五个字段，在后端被静默丢弃，订阅规则引擎和脚本功能在 Web 层完全不可用。
- 复现步骤：
  1. 前端 `vite-frontend/src/api/index.ts:661-683` 的 `SubPipeline` 接口包含全部 18 个字段（含上述 5 个）。
  2. 前端 `vite-frontend/src/pages/subscription.tsx:76-80` 设置默认值并通过 `toPayload` 的 `...rest` 展开发送。
  3. 后端 `subscription.go:30-44` 的 `subPipeline` 结构体仅有 13 个字段，缺失上述 5 个。
  4. `encoding/json` 反序列化时未知字段被静默丢弃。
  5. `subscription.go:46-51` 的 `options()` 方法未映射这 5 个字段。
  6. `subscription.go:558-559` 的 `SaveSubscription` 执行 `json.Marshal(req.Options)`，数据库中永远存不下这 5 个字段。
  7. `subscription.go:727` 的预览路径同样调用 `req.Options.options()`，同样丢失。
- 期望：前端配置的规则/脚本能保存到数据库并在渲染时生效。
- 实际：三处（保存、预览、渲染）全部丢失，只有直接调用 `kernel.SubOptions` 的单元测试能通过。
- 影响范围：订阅规则引擎（10 个规则组）、订阅 JS 脚本、链式代理多选（`ChainLandingNames`/`ChainRelayNames`）三个移植功能在生产环境完全不可用。
- 修复方向：在 `subPipeline` 补 5 个字段（带正确的 `json` tag）、在 `options()` 补映射、在 `normalize()` 补长度校验（`Script` 20000 字符、`RuleGroups` 数量上限）。

### 字段对照表（A1）
| `kernel.SubOptions` 字段 | `subPipeline` 有无 | `options()` 映射 | 状态 |
|---|---|---|---|
| Lines | ✅ | ✅ | 正常 |
| Regions | ✅ | ✅ | 正常 |
| Protocols | ✅ | ✅ | 正常 |
| Include | ✅ | ✅ | 正常 |
| Exclude | ✅ | ✅ | 正常 |
| Rename | ✅ | ✅ | 正常 |
| AddFlag | ✅ | ✅ | 正常 |
| Prefix | ✅ | ✅ | 正常 |
| Dedupe | ✅ | ✅ | 正常 |
| Sort | ✅ | ✅ | 正常 |
| RegionGroups | ✅ | ✅ | 正常 |
| ChainLandingNames | ❌ | ❌ | **P0-1 断点** |
| ChainRelayNames | ❌ | ❌ | **P0-1 断点** |
| ChainLanding | ✅ | ✅ | 正常（旧字段兼容） |
| ChainRelay | ✅ | ✅ | 正常（旧字段兼容） |
| Script | ❌ | ❌ | **P0-1 断点** |
| Rules | ❌ | ❌ | **P0-1 断点** |
| RuleGroups | ❌ | ❌ | **P0-1 断点** |

## P1 严重
（无）

## P2 一般

### A-002 `ApplySubOptions` 中脚本错误被静默吞掉，调用方无法区分"无脚本"和"脚本失败"
- 文件：`go-backend/internal/kernel/sub_options.go:258-260`
- 现象：`if scripted, err := ApplySubScript(out, opts.Script); err == nil { out = scripted }` —— 脚本执行失败时 `err` 被丢弃，直接使用脚本执行前的 `out`。虽然注释说明"编辑器会显示错误（SubScriptError）"，但 `SubScriptError` 需要重新执行一次脚本（`sub_options.go:273`），存在重复执行开销，且正常渲染路径无法感知脚本失败。
- 影响：用户看到的订阅是"脚本失败前的结果"，而编辑器显示脚本有错，两处状态不一致时难以排查。
- 修复方向：在 `SubOptions` 或返回值中携带脚本错误，或在日志中记录。

### A-003 `normalize()` 未校验新增字段的长度/合法性
- 文件：`go-backend/internal/http/handler/subscription.go:54-80`
- 现象：`normalize()` 校验了 `Rename`、`Include`、`Exclude`、`ChainLanding`、`ChainRelay`、`Prefix`、`Sort`、`Lines`，但未校验 `Script`（应 ≤20000 字符，与 `ValidateSubScript` 一致）、`RuleGroups`（应为已知规则组白名单）、`ChainLandingNames`/`ChainRelayNames`（数量上限）。
- 影响：恶意或误操作的超长脚本可存入数据库（`ValidateSubScript` 仅在 `ApplySubScript` 时检查，保存时不检查）。
- 修复方向：在 `normalize()` 中补 `len(p.Script) > kernel.MaxScriptLength` 检查和规则组白名单校验。

## P3 体验

### A-004 订阅脚本错误信息未包含行号
- 文件：`go-backend/internal/kernel/sub_script.go:136-144`
- 现象：`scriptError` 将 goja 的异常转为 `fmt.Errorf("脚本出错：%s", ...)`，但未提取 JS 堆栈中的行号。用户写错脚本时难以定位。
- 修复方向：从 `*goja.Exception` 的 `Stack()` 中解析行号并包含在错误信息中。

## 已验证通过的项

### A2 订阅渲染正确性
- `clashProfile(endpoints, groups, opts)` 三参数调用点（`share.go:548`）正确。
- `singboxProfile(endpoints, groups, opts)` 调用点（`share.go:565`）正确。
- P2 回归测试（`share_rules_regression_test.go`）在移植时已验证 `Rules=false` 四格式逐字节一致。

### A3 订阅脚本安全性（实测通过）
- **超时**：死循环脚本在 1.004s 被中断（`scriptTimeout = time.Second`），误差在预期内。
- **字段隔离**：脚本仅能看到 `id/name/original/server/port/protocol/region/line` 8 个字段；实测修改 `server` 被忽略，仅 `name` 变更被采纳。
- **ID 映射**：返回数组中 `id` 越界（如 99）时被 `continue` 跳过，不会 panic。
- **沙箱**：`goja.New()` 裸虚拟机，无网络/文件/定时器；`console.log` 被无害化。

### A4 订阅流量与配额
- `subOverQuota`：`TrafficLimit > 0 && used >= limit`，逻辑正确。
- `lastResetAt`：跨月天数钳制（`day > last → day = last`），`ResetDay=29/30/31` 在 2 月份会钳制到 28/29，行为合理。
- `subDueReset`：仅对分享订阅生效，非分享订阅永不重置，符合设计。
- 时区：使用服务器本地时区（`now.Location()`），与 `syncSubscriptions` 的调度时区一致。

### A5 订阅分配
- `subURLSig` 使用 `HMAC-SHA256(jwtSecret, "flvx-subsig-v1:{userID}")`，`verifySubURLSig` 使用 `subtle.ConstantTimeCompare`，防时序攻击。
- 签名仅绑定 `userID`，`relay`/`inbound`/`format` 等参数不签名——这是正确的，因为后端在验证签名后会重新查询该用户的 `user_inbound` 分配并过滤，参数篡改无法越权（无分配则返回空）。
- `user_inbound` 唯一索引 `(user_id, inbound_id, relay_id)` 已在 R5 迁移中处理。

### A6 流水线顺序
- `ApplySubOptions` 顺序：过滤 → 改名 → 去重 → 排序 → **脚本** → `markChain`。
- 脚本在 `markChain` 之前执行是正确的：脚本仅被允许修改 `name`，`markChain` 按 `Original`（脚本执行前已固化）匹配 `ChainLandingNames`/`ChainRelayNames`，不受脚本改名影响。

## 未能验证的项
- **A2 大订阅性能**：1000+ 节点时 `clashProfile` 的 YAML 生成耗时，未实测（需构造大批量数据）。
- **A4 并发配额**：`ApplySubscriptionCounterSamples` 的并发写入死锁风险，未实测（需并发压测环境）。
- **A5 端到端 IDOR**：未实际启动服务测试 `?user=` 篡改，结论基于代码审查（HMAC 恒定时间比较 + 后端二次鉴权，逻辑上无漏洞）。
- **PostgreSQL 迁移**：`subPipeline` 的 JSON 字段在 PG 下的读写，未实测（沙箱无 PG）。
