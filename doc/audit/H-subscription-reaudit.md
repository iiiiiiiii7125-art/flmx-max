# 审查域 H 报告：订阅功能复审（re-audit）

## 概要
- 审查对象：P0-1（subPipeline 缺 5 字段）、P0-2（ruleGroups 未下发）的修复
- 发现问题数：**P1 1 个** / P2 1 个 / P3 1 个
- 实际执行的测试：`go test ./internal/kernel/ -run TestSub`（通过）、`go test ./internal/kernel/`（2 失败，原因已查明）、Go 程序实测 nil 切片 JSON 行为、前后端字段逐项对照（18/18）

## 修复验证：逐项确认

### ✅ H-V01：subPipeline 已包含 18 个字段
- 文件：`go-backend/internal/http/handler/subscription.go:30-49`
- 确认：13 个旧字段 + 5 个新字段（`Script`、`Rules`、`RuleGroups`、`ChainLandingNames`、`ChainRelayNames`），JSON tag 与前端 `SubPipeline` 接口 18/18 一一对应（见下表）。

| # | 后端 json tag | 前端字段 | 一致 |
|---|---|---|---|
| 1-13 | lines/regions/protocols/include/exclude/rename/addFlag/prefix/dedupe/sort/regionGroups/chainLanding/chainRelay | 同名 | ✅ |
| 14 | script | script | ✅ |
| 15 | rules | rules | ✅ |
| 16 | ruleGroups | ruleGroups | ✅ |
| 17 | chainLandingNames | chainLandingNames | ✅ |
| 18 | chainRelayNames | chainRelayNames | ✅ |

### ✅ H-V02：options() 正确映射全部字段
- 文件：`subscription.go:51-58`
- 确认：5 个新字段全部映射到 `kernel.SubOptions`，无遗漏。

### ✅ H-V03：normalize() 校验新字段
- 文件：`subscription.go:86-96`
- 确认：`RuleGroups`/`ChainLandingNames`/`ChainRelayNames` nil→`[]` 默认值；`Script` 长度上限 20000（与执行时一致，保存时提前拦截）。

### ✅ H-V04：订阅列表 API 下发 ruleGroups
- 文件：`subscription.go:473`
- 确认：`"ruleGroups": kernel.RuleGroups()` 已加入响应。
- 契约验证：后端 `RuleGroup{Key string `json:"key"`, Name string `json:"name"`}` 与前端 `SubRuleGroup{key, name}` 完全一致；前端 `subscription.tsx:902` 从 `subRes.data.ruleGroups ?? []` 读取，`759` 行渲染条件 `ruleGroups.length > 0` 可满足。

### ✅ H-V05：保存路径包含新字段
- 文件：`subscription.go:579`，`json.Marshal(req.Options)`，`req.Options` 类型为 `subPipeline` → 18 字段全部持久化。

### ✅ H-V06：预览路径覆盖
- 文件：`subscription.go:749`，`subscriptionPreview` 使用 `req.Options.options()` → 同一修复覆盖，无需额外改动。

### ✅ H-V07：单元测试
- `go test ./internal/kernel/ -run TestSub -count=1` → **通过**。

## 新发现问题

### P1：H-001 parsePipeline 未给新切片字段设默认值，旧订阅编辑时前端崩溃
- 文件：`go-backend/internal/http/handler/subscription.go:102-111`
- 现象：`parsePipeline` 只对 `Regions`/`Protocols` 设 `[]` 默认值，`RuleGroups`/`ChainLandingNames`/`ChainRelayNames` 保持 nil。
- 复现步骤：
  1. 数据库中有一条 P0-1 修复前创建的订阅（Options JSON 无 `ruleGroups` 键）
  2. 后端 `subscriptionView` 经 `parsePipeline` 得到 `RuleGroups=nil`，`json.Marshal` 输出 `"ruleGroups":null`（已用 Go 程序实测确认：`RuleGroups == nil: true`，marshal 得 `"ruleGroups":null`）
  3. 前端 `draftFrom`（`subscription.tsx:314`）执行 `options: { ...EMPTY_PIPELINE, ...s.options }`，`null` 覆盖 `[]` 默认值
  4. 用户打开该订阅编辑页，拨动「开启分流规则」开关 → `onChange`（`753` 行）求值 `o.ruleGroups.length === 0` → **TypeError: Cannot read properties of null**
  5. 即使跳过此处，`769` 行 `o.ruleGroups.includes(g.key)` 同样崩溃
- 期望：旧订阅开启规则时，`ruleGroups` 应为 `[]`，开关可正常拨动并默认全选（`754` 行逻辑）。
- 实际：React 组件崩溃。
- 影响范围：所有在 P0-1 修复前创建的订阅（凡升级用户）。
- 修复方向：在 `parsePipeline` 中补 `if p.RuleGroups == nil { p.RuleGroups = []string{} }`（另两个字段同理），与既有 `Regions`/`Protocols` 模式一致。此即深审 F-003，复审确认其为 P1（有明确崩溃路径）。

### P2：H-002 两个单测因 SNI 默认值变更而失败，期望值未同步
- 文件：`go-backend/internal/kernel/share_clients_test.go:43,62`
- 现象：`go test ./internal/kernel/ -count=1` 中 `TestLoonList`、`TestQXList` 失败。期望值硬编码 `sni=www.microsoft.com` / `obfs-host=www.microsoft.com`，实际输出为 `www.amd.com`。
- 原因：1.0.2 有意将 `DefaultRealitySNI` 改为 `www.amd.com`（用户要求），测试期望未同步更新。**非功能 bug**，属测试维护债。
- 影响：CI 若跑全量 kernel 测试会红；会掩盖真正的回归。
- 修复方向：将两处期望值改为 `www.amd.com`（另有 3 个文件含该字符串但测试仍通过：`kernel_test.go`、`share_test.go`、`import_test.go`、`tests/contract/kernel_share_contract_test.go`，建议一并检查）。

### P3：H-003 Validate() 未对 RuleGroups 做白名单过滤
- 文件：`go-backend/internal/kernel/sub_options.go:126-143`
- 现象：`Validate()` 校验 Rename/Include/Exclude/ChainLanding/ChainRelay/Script，但不对 `RuleGroups` 调用既有的 `NormalizeRuleGroups` 做 key 白名单过滤。
- 影响：低。渲染侧 `sub_rules.go` 对未知 key 有 fail-safe（未知组被忽略），属数据卫生问题。深审 F-004 已记录，此处确认仍未修。
- 修复方向：在 `Validate()` 或 `normalize()` 中调用 `kernel.NormalizeRuleGroups`。

## 回归检查
- `go test ./internal/kernel/ -run TestSub` ✅ 通过
- `go test ./internal/kernel/`：仅上述 2 个失败，均为 SNI 期望值过期，无其他回归
- `go build ./...` ✅ 通过
- 订阅保存→读取→渲染链路：18 字段端到端一致（新数据）；旧数据缺字段时除 H-001 外无 panic（`json.Unmarshal` 容忍缺失键，已验证）
- P0-1/P0-2 的三处数据丢失点（保存 `:579`、预览 `:749`、读回 `:430`）均已覆盖，无遗漏

## 未能验证的项
- 浏览器实际编辑旧订阅的崩溃复现（无浏览器环境；崩溃路径已通过代码+Go 实测双重确认，置信度高）
- PostgreSQL 下的 JSON 读写（本机无 PG）
