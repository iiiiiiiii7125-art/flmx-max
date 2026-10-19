# 从 kure29/flyx 移植功能记录

> 移植日期：2026-10-20
> 来源：https://github.com/kure29/flyx (main 分支)
> 目标：flvx-max

## 移植批次总览

> 状态定义：`后端完成` = API/逻辑/路由/仓储就绪且可编译；`前端已挂载` = 组件在页面中有引用且用户可操作。
> 挂载位缺失意味着功能对用户不可见，属于**未完成**，不记为"完成"。

| 批次 | 功能 | 后端 | 前端 | 状态说明 |
|------|------|------|------|----------|
| 1 | JWT 密钥持久化 | ✅ | 不适用 | ✅ 完成（无界面，启动时自动生效） |
| 2 | 节点延迟显示 | ✅ | ❌ 未挂载 | 后端 API/路由/探测全就绪；`LatencyCell`、`latency-card.tsx`、`latency-targets-modal.tsx` 零引用 → P1 修复 |
| 3 | 节点调优 | ✅ | ❌ 未挂载 | agent 命令 + 面板 API 就绪；`tune-modal.tsx` 零引用，node.tsx 无入口 → P1 修复 |
| 4 | 回程路由追踪 | ✅ | ❌ 未挂载 | agent 命令 + 面板 API 就绪；`RouteModal` 零引用，node.tsx 无入口 → P1 修复 |
| 5 | GitHub 镜像代理 | ✅ | 不适用 | ✅ 完成（无界面，`/api/v1/open_api/gh/` 已注册） |
| 6 | 订阅规则引擎 + 订阅脚本 | ⚠️ 部分 | ⚠️ 部分 | 规则引擎/脚本核心已移植；规则模式完整配置渲染缺失 → P2 修复；链式多选 UI 缺失 |
| 7 | 前端组件补齐 | 不适用 | ✅ | ✅ 完成（4 组件零引用，未复制，见 P4 复核） |

## 适配点及原因

### 1. JWT 密钥持久化
- **适配**：对方 `EnsureJWTSecret` 检查 `JWTSecret` 是否为空；我方 `FromEnv` 总会生成临时密钥。
- **方案**：通过 `JWTEphemeral` 标志判断是否为临时密钥，临时密钥才走文件持久化。
- **原因**：保持 F-AUDIT-009 测试通过（FromEnv 必须生成临时密钥）。

### 2. 节点延迟显示
- **适配**：对方 `reachProbe` → 我方 `externalProbe` + `Points []latencyPoint` 字段。
- **方案**：扩展现有类型，不并行维护两套语义相同的结构。
- **原因**：文档要求"优先扩展现有类型"。
- **占位**：第二批时 `withRoutes`/`traceNodeRoutes` 为桩，`route` 字段留空；第四批替换为真实实现。
- **前端**：`route-modal.tsx` 第二批为占位，第四批替换；`latency-targets-modal.tsx` 用精简本地 `Node` 接口（我方 Node 内联定义在 node.tsx）。

### 3. 节点调优
- **适配**：配置文件名 `99-flyx-tuning.conf` → `99-flvx-tuning.conf`。
- **原因**：品牌区分，避免与上游冲突。
- **备份路径**：`/etc/flux_agent/tuning-backup.json` 保持不变（文档要求）。

### 4. 回程路由追踪
- **适配**：`node_route.go` 中的 `reachProbe` → `externalProbe`。
- **方案**：sed 全局替换，签名一致，无语义冲突。

### 5. GitHub 镜像代理
- **适配**：无。`githubRepo` 变量名与我方 `upgrade.go` 完全一致。
- **验证**：白名单自动包含 `iiiiiiiii7125-art/flvx-max`。

### 6. 订阅规则引擎 + 订阅脚本
- **适配 1**：`sub_options.go` 新增 5 字段，`ChainLanding`/`ChainRelay` 保留兼容旧单选。
- **方案**：`sub_chain.go` 新增 `chainMatchers`，有多选名称时按 `Original` 精确匹配，否则回退旧表达式。
- **适配 2**：`share.go` 的 `uniqueNames` 加 `rules bool` 参数，预留规则组名。
- **未完成**：`share_client_configs.go`（规则模式的 Loon/QX/Shadowrocket 完整配置渲染）因依赖缺失暂未移植；`clashProfile` 的规则模式集成待精修。
- **依赖**：`github.com/dop251/goja` 为唯一新增依赖（文档允许）。

### 7. 前端组件补齐
- **结论**：`view-mode.tsx`、`page-kit.tsx`、`info-card.tsx`、`action-sheet.tsx` 在已移植的 5 个前端文件中零引用，未复制。
- **原因**：文档要求"被引用才移植，没被引用就跳过"。

## 与对方实现有意不同的地方

1. **JWT**：用 `JWTEphemeral` 标志而非空值判断（见上）。
2. **延迟类型**：`externalProbe.Points` 而非独立的 `reachProbe`（类型合并）。
3. **调优配置文件名**：`99-flvx-tuning.conf` 而非 `99-flyx-tuning.conf`。
4. **订阅规则渲染**：核心引擎已移植，完整配置渲染（Loon/QX/Shadowrocket 规则模式）待后续。

## 实测结果

### 编译与静态检查（通过项）

- `go build ./...`（go-backend、go-gost）：✅ 通过
- `go vet ./internal/kernel/`：✅ 通过
- `go test ./internal/kernel/`：✅ 通过（无回归）
- `npx tsc --noEmit`：✅ 通过

### 全量测试（失败项，未隐瞒）

`go test ./internal/http/handler/ ./internal/store/repo/` 共 **66 个失败用例**。

**失败原因分类**（均为 Windows 环境限制，非移植引入）：

| 类别 | 表现 | 说明 |
|------|------|------|
| nftables 相关 | `nft` 命令不存在 | Windows 无 nftables |
| 文件权限相关 | `0600`/POSIX mode 断言失败 | Windows 不认 POSIX 权限位 |
| SQLite 临时文件清理 | 文件删除失败 | Windows 文件锁语义 |

**基线对照结论**：移植前提交 `5e1edce8`（审计收敛合入 main 的提交）同样 **66 个失败**；
逐个比对用例名后，**新增失败 0 个、消失 0 个**。这些用例需在 Linux 环境验证，
Windows 上不作为回归判据。

### 基线对照方法（可复现）

```
基线提交：5e1edce8adfb7c46124b17a7b3b2b3caa46302c4
命令：go test ./internal/http/handler/ ./internal/store/repo/ 2>&1 | findstr "--- FAIL"
比对：去掉耗时后缀后按用例名做集合差
判定：差集为空 ⇒ 无回归
```

## P1–P4 遗留问题修复状态

| 问题 | 内容 | 状态 |
|------|------|------|
| P1 | 前端挂载缺失（LatencyCell/tune-modal/RouteModal/latency-card/latency-targets-modal） | 待修复 |
| P2 | 订阅规则完整配置渲染缺失（share_client_configs.go、clashProfile 规则模式） | 待修复 |
| P3 | 文档实测记录失真 | ✅ 本轮已修复 |
| P4 | 组件引用复核（P1 完成后重新扫描 4 组件引用） | 待 P1 完成后复核 |

## 待精修项（明细）

1. `node.tsx` 中 `LatencyCell` 在卡片/列表的挂载 → P1-1/P1-2。
2. `tune-modal.tsx` 在 `node.tsx` 操作菜单的入口 → P1-3。
3. `RouteModal` 入口 → P1-4；`latency-targets-modal.tsx` 入口 → P1-5。
4. `latency-card.tsx` 挂载到 `config.tsx` → P1-6。
5. 订阅规则的完整配置渲染（`share_client_configs.go` 依赖补齐）→ P2-1。
6. `clashProfile` 的规则模式集成 → P2-2。
7. 链式代理多选 UI（`chainLandingNames`/`chainRelayNames` 节点选择器）→ 后续按我方单页 `Editor` 结构单独设计。
