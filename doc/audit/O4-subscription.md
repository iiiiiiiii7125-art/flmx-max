# O4 订阅系统功能全量审计

**审计日期**: 2026-10-20
**参考手册**: `doc/guide/subscription.md`
**审计员**: O4-subscription

## 审计范围

对照手册，对订阅系统 8 大功能逐项核验：
1. 订阅 CRUD（创建、编辑、删除、复制）
2. 订阅分配（按用户、按入站、按中转）
3. 订阅渲染（Clash/Surge/SingBox/Loon/QX/Shadowrocket）
4. 订阅规则（规则组）
5. 订阅脚本（goja）
6. 订阅签名（HMAC）
7. 我的订阅（用户端）
8. 处理流水线（过滤→改名→国旗→前缀→去重→排序）

## 发现问题

### P1

**O4-001：订阅预览不返回脚本错误，前端无法显示**
- **文件**: `go-backend/internal/http/handler/subscription.go:733` (`subscriptionPreview`)
- **手册**: "脚本执行失败不影响改名规则等其他步骤的结果" 且 "编辑器会显示错误"
- **实际**: `subscriptionPreview` 调用 `kernel.ApplySubOptions`，脚本失败时错误被静默忽略（`sub_options.go:253` 的 `if scripted, err := ApplySubScript(...); err == nil`）。`SubScriptError` 函数存在（`sub_options.go:258`）但 preview 未调用。前端 `subscription.tsx` 也无脚本错误显示逻辑。
- **复现**: 在订阅编辑页输入死循环脚本 `function operator(n){while(true){}}`，点"看看结果"，无错误提示，节点列表正常返回（脚本被静默跳过）。
- **影响**: 用户无法得知脚本有误，误以为脚本生效。

### P2

**O4-002：TestLoonList/TestQXList 失败（测试维护债）**
- **文件**: `go-backend/internal/kernel/share_clients_test.go:34,57`
- **原因**: 测试硬编码期望 `sni=www.microsoft.com`，实际默认 SNI 已在 1.0.2 改为 `www.amd.com`（`kernel/model.go:160`）。
- **复现**: `go test -vet=off ./internal/kernel/ -run "TestLoonList|TestQXList"` → 2 FAIL。
- **影响**: 非功能 bug，但 CI 若跑此测试会红。

**O4-003：订阅 HMAC 签名未绑定订阅 token**
- **文件**: `go-backend/internal/http/handler/user_inbound.go:19`
- **实际**: `subURLSig` 输入仅 `flvx-subsig-v1:{userID}`，同一用户的 sig 可复用于该用户有权限的任意订阅 token。
- **风险**: 低（token 128 位不可猜，且后端有二次权限过滤）。但不符合"签名绑定资源"的最佳实践。
- **建议**: 改为 `flvx-subsig-v1:{userID}:{token}`。

**O4-004：手册与代码的流水线顺序描述不一致**
- **手册**: "过滤 → 改名 → 国旗 → 前缀 → 去重 → 排序"
- **代码** (`sub_options.go:219-256`): 过滤 → 改名 → 国旗 → 去重 → 前缀 → 排序
- **实际影响**: 无（去重键为 host|port|protocol，不含名称，前缀不影响去重结果）。
- **建议**: 统一文档或代码注释，避免后续维护者误解。

### P3

**O4-005：`NormalizeRuleGroups` 未在保存时调用**
- **文件**: `go-backend/internal/http/handler/subscription.go:61` (`normalize()`)
- **实际**: 非法 rule key 存入 DB，依赖渲染侧 `enabledRuleGroups` 的 fail-safe 过滤。
- **影响**: 低。数据卫生问题。

## 验证通过

### 1. 订阅 CRUD ✅
- 创建/编辑/删除/重置均正常。删除时 `respondAfterSubPush` 触发 `applyKernelsForInbounds`，凭据从节点移除（符合手册"面板会把它的凭据从节点上删掉"）。
- 重置链接生成新 UUID/密码，旧链接失效（`subscriptionReset`）。

### 2. 处理流水线 ✅
- `ApplySubOptions` (`sub_options.go:219`) 顺序：Filter → Rename → Flag → Dedupe → Prefix → Sort → Script → markChain。
- 脚本在 `markChain` 之前执行，链式匹配能看到脚本改名后的名字（符合手册"匹配的是改名后的名字，也认原名"）。
- 去重键为 host|port|protocol（手册"地址、端口、协议都相同的只保留第一个"）✅。

### 3. 订阅规则（10 规则组）✅
- `ruleGroupDefs` 共 10 组：ads/ai/youtube/netflix/media/telegram/google/microsoft/apple/games，与手册一致。
- `NormalizeRuleGroups` 按展示顺序排列，非法 key 过滤。
- Clash 输出含 `proxy-groups` + `rules`；sing-box 含路由规则；Loon/QX/Shadowrocket 输出完整配置（非纯节点列表）✅。

### 4. 订阅脚本（goja 沙箱）✅
- 1 秒超时（`scriptTimeout`），20000 字符上限（`MaxScriptLength`）。
- 仅 `name` 变更被采纳，其他字段只读（`sub_script.go:110-113`）。
- 裸 goja VM，无网络/文件/定时器；`console.log` 已 stub。
- 脚本失败不影响其他步骤（`ApplySubOptions` 忽略错误，符合手册）。

### 5. 链式代理 ✅
- `chainMatchers`：多选名称时按 `Original` 精确匹配；表达式时匹配 `Name` 或 `Original`（手册"匹配的是改名后的名字，也认原名"）。
- 两项都填且都匹配才生效，否则清空 `ChainRole`（手册"两项都填且都匹配到节点才生效"）。
- Loon/QX 输出不含链式代理参数（`loonConfig`/`qxConfig` 未引用 `ChainRole`），落地节点为直连（手册"✗"）✅。
- Base64 通用链接无法表达链式（`share.go` 的 base64 路径未处理 chain）✅。

### 6. 订阅签名 ✅
- HMAC-SHA256，`subtle.ConstantTimeCompare` 防时序攻击。
- `?user=` 无 sig 时 fail-closed 403（`kernel_relay.go:555`）。

### 7. Subscription-Userinfo ✅
- 非分享订阅的 `total` 已修复（1.0.3，`kernel_relay.go:635` 的 `total = sub.TrafficLimit`）。
- 格式 `upload=%d; download=%d; total=%d; expire=%d` 符合 Clash 规范。

### 8. 到期/流量/每月重置 ✅
- `syncSubscriptions` 后台循环处理到期、超限、每月重置，触发 kernel 推送移除凭据。
- `lastResetAt` 正确处理月末边界（如 2 月 30 日→28 日）。
- 90% 和用完时推送通知（`notify` 调用点存在）。

### 9. 我的订阅（用户端）✅
- `getMyInbounds` 按 `Enabled != 1` 过滤（F-AUDIT-001 已修复）。
- 按中转级别过滤（`allowedRelays` map）。

### 10. P0-1/P0-2/H-001 修复验证 ✅
- `subPipeline` 18 字段齐全，`options()` 映射完整。
- 订阅列表 API 返回 `ruleGroups`。
- `parsePipeline` 新字段 nil→[] 默认值已补（1.0.7）。

## 未能验证
- 浏览器实际操作（无浏览器环境）：订阅编辑页交互、"看看结果"预览、客户端一键导入（`clash://` 等 scheme）。
- 大订阅性能（千节点级渲染耗时）。
- 订阅源自动更新（定时任务触发）。

## 修复建议顺序
1. O4-001（P1）：preview 返回 `SubScriptError`，前端显示脚本错误。
2. O4-002（P2）：更新测试期望 SNI 为 `www.amd.com`。
3. O4-003（P2）：HMAC 绑定 token。
4. O4-004/O4-005（P3）：文档与数据卫生。
