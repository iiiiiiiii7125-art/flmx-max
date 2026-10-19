# 从 kure29/flyx 移植功能记录

> 移植日期：2026-10-20
> 来源：https://github.com/kure29/flyx (main 分支)
> 目标：flvx-max

## 移植批次总览

| 批次 | 功能 | 状态 |
|------|------|------|
| 1 | JWT 密钥持久化 | ✅ 完成 |
| 2 | 节点延迟显示 | ✅ 完成（node.tsx 挂载位待精修） |
| 3 | 节点调优 | ✅ 完成 |
| 4 | 回程路由追踪 | ✅ 完成 |
| 5 | GitHub 镜像代理 | ✅ 完成 |
| 6 | 订阅规则引擎 + 订阅脚本 | ✅ 完成（渲染层规则模式精修待后续） |
| 7 | 前端组件补齐 | ✅ 完成（4 组件零引用，未复制） |

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

- `go build ./...`（go-backend、go-gost）：✅ 通过
- `go vet ./internal/kernel/`：✅ 通过
- `go test ./internal/kernel/`：✅ 通过（无回归）
- `npx tsc --noEmit`：✅ 通过

## 待精修项

1. `node.tsx` 中 `LatencyCell` 在卡片/列表的具体挂载位置。
2. `tune-modal.tsx` 在 `node.tsx` 操作菜单的入口。
3. 订阅规则的完整配置渲染（`share_client_configs.go` 依赖补齐）。
4. `clashProfile` 的规则模式集成。
