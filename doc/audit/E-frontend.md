# 审查域 E 报告：前端功能可用性

## 概要

- 审查范围：7 批移植功能的前端挂载完整性、前后端字段契约一致性、订阅编辑页状态管理
- 发现问题数：**P0 2 个** / P1 0 个 / P2 1 个 / P3 0 个
- 实际执行的检查：静态代码审查（字段对照、挂载点 grep、API 路径对照）；**无浏览器环境，页面实际渲染未验证**

---

## P0 阻断

### E-001 订阅规则/脚本/链式多选字段在后端 HTTP 层被完全丢弃

- 文件：`go-backend/internal/http/handler/subscription.go:30-52`
- 现象：前端订阅编辑页「规则与脚本」区可正常填写（开关、规则分组多选、脚本编辑器均已挂载），保存后后端静默丢弃；重新打开编辑页时已填内容丢失；预览接口同样无效果。
- 复现步骤：
  1. 前端 `SubPipeline` 类型已含 5 个新字段（`vite-frontend/src/api/index.ts:661-682`：`rules`、`ruleGroups`、`chainLandingNames`、`chainRelayNames`、`script`），`EMPTY_PIPELINE` 已初始化（`vite-frontend/src/pages/subscription.tsx:61-80`），`toPayload` 经 `...rest` 透传全部字段（`subscription.tsx:326-337`）。
  2. 后端 `subPipeline` 结构体（`subscription.go:30-43`）只有 13 个旧字段，缺失上述 5 个 → `json.Unmarshal` 时直接丢弃。
  3. `options()`（`:45-51`）未映射 5 个字段 → `kernel.SubOptions` 永远收不到。
  4. `parsePipeline`（`:82-92`）反序列化用同一结构体 → 数据库读回时同样丢失，编辑页回显为空。
  5. `subscriptionPreview`（`:702`）走同一 `subscriptionRequest` → 预览也不生效。
- 期望：保存的规则/脚本/链式多选能持久化并作用于订阅渲染。
- 实际：Web 层该功能完全不可用（仅单元测试能通过）。
- 影响范围：订阅规则引擎、订阅脚本、链式代理多选三个移植功能在 Web 端全部失效。
- 修复方向：给 `subPipeline` 补 5 个字段 + json tag；在 `options()` 中映射到 `kernel.SubOptions`；在 `normalize()` 中补校验（`RuleGroups` 建议调 `kernel.NormalizeRuleGroups` 过滤未知 key，`Script` 限长）。

### E-002 订阅列表接口未下发规则分组清单，前端分组复选框永不渲染

- 文件：`go-backend/internal/http/handler/subscription.go:447-450`
- 现象：即使 E-001 修复，前端「规则分组」复选框依然不会出现。
- 证据：
  - 前端从列表接口读取分组清单：`setRuleGroups(subRes.data.ruleGroups ?? [])`（`vite-frontend/src/pages/subscription.tsx:902`）。
  - 后端 `subscriptionList` 响应只含 `items` 和 `regions`（`subscription.go:447-450`），无 `ruleGroups` 键 → 前端恒得 `[]`。
  - 分组复选框渲染条件为 `{o.rules && ruleGroups.length > 0 && (...)}`（`subscription.tsx:759`）→ 永假。
  - 后端已有现成的数据源 `kernel.RuleGroups()`（`go-backend/internal/kernel/sub_rules.go:84`，返回 `[]RuleGroup{Key, Name}` 且 json tag 与前端 `SubRuleGroup`（`api/index.ts:684-687`）一致），只是未在接口中暴露。
- 期望：打开规则开关后可勾选 10 个规则分组。
- 实际：只能开总开关，无法选择分组（`o.ruleGroups` 恒空）。
- 影响范围：订阅规则引擎的分组粒度分流不可用。
- 修复方向：在 `subscriptionList` 响应中增加 `"ruleGroups": kernel.RuleGroups()`。

---

## P2 一般

### E-003 `subPipeline.normalize()` 未处理新字段

- 文件：`go-backend/internal/http/handler/subscription.go:54-78`
- 现象：`normalize()` 只 trim 旧字段（`Rename`/`Include`/`Exclude`/`ChainLanding`/`ChainRelay`），`Script` 未做去空白/限长，`RuleGroups` 未做 key 白名单过滤。
- 影响：脏数据可入库；未知 group key 会在渲染层被静默忽略（`enabledRuleGroups` 按 key 匹配），用户勾选了不存在的分组也无提示。
- 修复方向：`Script` 限长（如 20000 字符，与 `sub_script.go` 上限一致）；`RuleGroups = kernel.NormalizeRuleGroups(p.RuleGroups)`。

---

## 已验证通过的项（有证据）

### E1 挂载完整性

| 功能 | 挂载点 | 证据 |
|---|---|---|
| 延迟显示（列表） | `node.tsx:1997` `renderLatency(node, "list")` | TableCell 渲染，无条件 |
| 延迟显示（卡片） | `node.tsx:2266-2268` | `node.latency &&` 条件已移除，始终渲染（此前 P1 遗留 bug 已修） |
| 延迟检测/路由/测试点入口 | `components/latency-cell.tsx:229,237,355-372` | 「检测」按钮、「回程路由」、「测试点」下拉项均存在 |
| 调优入口 | `node.tsx:2056-2061`（列表）、`2566-2568`（卡片） | nftables 模式隐藏逻辑存在 |
| 调优弹窗 | `node.tsx:3562` `<NodeTuneModal>` | 已挂载 |
| 路由弹窗 | `node.tsx:3563-3567` `<RouteModal nodeId nodeName>` | props 与 `route-modal.tsx:105-106` 一致 |
| 测试点弹窗 | `node.tsx:3568-3572` | `onSaved` 后 `loadNodes({silent:true})` 刷新 |
| 测试点配置 | `config.tsx:42,1709` `<LatencySettings>` | 已挂载 |
| 订阅规则/脚本 UI | `subscription.tsx:739-799` | 「规则与脚本」Section、开关、分组复选框、脚本编辑器均存在 |

### E2 前后端契约（除 E-001/E-002 外）

- 5 个延迟/调优/路由 API 路径前后端一致：`/node/latency/probe`、`/node/latency/targets`、`/node/tune`、`/node/route`、`/node/route/probe`（前端 `api/index.ts:867-925`，后端 `handler.go:231-237`）。
- `RouteModal` 的 `nodeId: number | null` 与调用方 `routeNode?.id ?? null` 一致。
- 订阅保存后刷新列表：`save()` 内 `await load()`（`subscription.tsx:938`）。
- `draftFrom` 用 `{ ...EMPTY_PIPELINE, ...s.options }` 合并（`subscription.tsx:315`），后端一旦返回新字段即能正确回显，无需前端改动。
- 链式多选 UI 未移植属已知缺口（类型已就位），本次不计为缺陷。

---

## 未能验证的项

- 延迟单元格的实际渲染效果、颜色分档、探测按钮交互：未验证，需人工在浏览器确认（无浏览器环境）。
- 调优弹窗、路由弹窗、测试点弹窗的实际打开与交互：未验证，需人工在浏览器确认。
- 订阅规则开关/分组/脚本的端到端保存→预览：未验证（被 E-001/E-002 阻断，修复后需重验）。
- 旧浏览器/移动端布局：未验证。
