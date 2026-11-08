# FIX-M0051-M0001 — 节点回退回执失实（中）＋内核别名口径不一致（低）

分支：`fix-m0051-m0001`（自冻结尖端 6f37cb68，int-wave-20261107）。只改产品代码，未合 main、未推送。
证据源：`live-all/DRV-FIX-DOC-AG-report.md` §2 专段；本文所有行号均为本分支现状。

## M-0051 节点回退未落地（中）

### 根因（已坐实）

链路三段全部读过：

- 面板 `nodeRollback`（`go-backend/internal/http/handler/upgrade.go:888`）：`SendCommand("RollbackAgent", 30s)` 成功即回 `response.OK(rollbackReceiptData(result))`。
- Agent `handleRollbackAgent`（`go-gost/x/socket/websocket_reporter.go:3400`）：`planAgentRollback` 预检通过 → 建回退脚本 → `launchAgentControlScript`（`:2118`）——即 `exec.Command(name, args...).Start()`，** detached 启动、不等待、不收输出**；脚本一 spawn 即回 success 并带 `fromVersion/toVersion/backupGeneration`。
- 回退脚本（`buildAgentRollbackScriptFor`，`:2283`）：sleep 1 → 停服务 → mktemp 暂存＋cp＋cmp＋mv 原子换 binary → 重启服务 → 两次健康采样，日志只写节点本地 `agent-upgrade.log`，面板永不读取。

即：回执断言的“落点”在回执时刻无人能知——handler 进程本身会在脚本停服务时被杀死，同步确认在架构上不可能。原回执把目标写成 `toVersion` 并配成功 toast，操作员只能读成“已回退到 X”。LIVE-ALL 实证：回执 04:53:06 UTC 称成功，`/opt/flmx-liveall/bin/flmx_agent` mtime 仍为 03:35、大小未变、无 `.old`、应答进程未重启。

### 决策：回执改为下发语义（不选 agent 二次确认＋面板轮询）

两个候选方向中选前者，理由三条：

1. **同步确认不存在**：agent 侧的“二次确认”只能发生在重启后的重连——即面板拿重连后上报的版本比对目标。这需要新增待核验状态机（存目标版本、重连时比对、失配告警通道），而面板内没有现成的操作员可见告警面（go-backend 无 eventlog 包；节点列表只被动显示版本号）。为一纸回执新建一套核验子系统，超出本案比例。
2. **升级面同形**：`nodeUpgrade`（`upgrade.go:534`）同样是“下发成功即回执成功＋`markNodePendingUpgradeRedeploy`”，面板接受该口径。回退与升级对齐到同一诚实度，不另立更高标准。
3. **回执本身就是缺陷面**：M-0051 立案的是“回执成功、回退未落地”——修回执的语义即修掉了误导；落点核验路径已存在且无需新建：agent 重连时经 WS `version` 参数上报版本（`go-backend/internal/ws/server.go:645` → `UpdateNodeOnline`），操作员在节点列表即可看到版本是否变为目标。

### 改动

`go-backend/internal/http/handler/upgrade.go`：

- `rollbackReceiptData` 改为下发回执：新增 `status: "dispatched"`；目标版本改称 `expectedVersion`；**删 `toVersion` 字段**；`message` 改为“回退命令已下发，节点正在重启；目标版本 X，落点待节点重连后在节点列表核验”（`legacy-unknown` 代次仍追加“未经紧邻上一代校验”披露；无数据老 agent 仍走同披露，文案“已发送”→“已下发”）。
- `nodeRollback` 下发成功后补 `h.markNodePendingUpgradeRedeploy(req.ID)`：与升级面同口径，重连后立即重建节点运行时（原先回退重启后要等 redeploy 冷却）。

### BC（单独点名）

- **回执契约变化**：`data.toVersion` 删除，改为 `data.expectedVersion`＋`data.status="dispatched"`。面板前端不受影响——`vite-frontend/src/pages/node.tsx:1404-1410` 只读 `data.message`，前端无改动。仓库内无其他 `toVersion` 读者（grep 核过）。外部脚本若解析 `toVersion` 须改读 `expectedVersion`，且不得再把回执当作落点证明。**进发版说明。**
- `fromVersion`／`backupGeneration` 保留：二者描述的是下发计划（下发前版本／所用备份代次），不是落点事实。

### `.old` 来源不明（未结案，写清边界）

Rig 节点“从未面板升级却报出 previous 代次备份”一事，代码面查不下去：`writeAgentBackupMeta` 唯一调用点是 `handleUpgradeAgent`（`websocket_reporter.go:3361`），即 meta 存在即意味着某次面板升级写过它；只读核验现状：`/opt/flmx-liveall/bin/` 内无 `.old`、无 `.old.meta`、无 `agent-upgrade.log`（回退脚本首行即 `exec >>agent-upgrade.log`，其缺失佐证脚本未曾落盘执行），`flmx_agent` mtime 仍为 03:35。结论：来源无法从代码＋现状重建，不编造；防线是 `planAgentRollback` 的配对校验（备份 hash 与安装 hash 不配即拒）＋本修复（回执不再断言落点），幽灵 "previous" 从此造不出虚假成功回执。验活重跑时以新回退实测为准。

### 测试

- `r6_rollback_receipt_test.go` 按新契约重写（三测全过）：目标版本称 `expectedVersion`、无 `toVersion` 键、`status` 为 `dispatched`、文案含“核验”与目标版本；legacy 代次仍披露未校验；老 agent 无版本捏造、不转发 bare message。
- Handler 整包：见下节总结果。

### 验活重跑方案（等主控通知再上机）

前置：新 agent 须先上测试机（回退逻辑在 agent 侧无改动，但面板回执在新版才变；回退本身的落盘行为由脚本执行，须新面板＋现 agent 即可测回执面；落点核验走版本列表）。
1. 在 Rig 节点（或 scratch 节点）做一次面板升级，确认 `.old`＋`.old.meta` 生成（`planAgentRollback` 配对前置）。
2. 调回退：断言回执 `code=0`、`status=dispatched`、`expectedVersion`＝升级前版本、无 `toVersion` 字段、文案含“核验”。
3. 落点核验：节点重连后列表版本＝`expectedVersion`；`flmx_agent` mtime/大小变为 `.old` 的值；进程重启。
4. 反向腿：删 `.old` 后调回退，断言 fail-closed 拒回（“没有可用的备份文件”），不发下发回执。

## M-0001 内核别名口径不一致（低）

### 根因（已坐实）

- 目录 `Name` 是 `"sing-box"`（`capabilities.go:204`），`Type` 是 `"singbox"`（`TypeSingbox`，`:14`）。操作员按显示名调 API 是自然行为。
- `Lookup`（`capabilities.go:271`）只 lower＋trim、不去连字符 → `"sing-box"` 查无。
- 三面三种文案：`/kernel/versions`（`kernel.go:2487`）→“未知内核”；`inboundSave`（`kernel.go:1057`）经 `GetNodeKernelByType(nodeID, lower(trim))` 查不到已装的 `"singbox"` 行 → `k == nil` →“请先在该节点安装内核”（误导：内核明明已装）；`Normalize`（`model.go:293`）→“暂不支持内核 sing-box”。
- 全集枚举（机械 grep）：`Lookup` 调用点 7 处（`kernel.go:376/2112/2487/2887`、`ComboSupported:285`、`chain.go:68`、`model.go:293`），全部经 `Lookup` 收敛；`GetNodeKernelByType` 调用点 3 处（`realm.go:263` 传常量、`kernel.go:399` 传 `info.Type`——已规范、`kernel.go:1057` 传原始输入——唯一故障点）。`chain.go:51`／`render.go:81` 的 switch 吃的是已入库的规范值，不在射程内。

### 改动

- `go-backend/internal/kernel/capabilities.go`：新增 `normalizeKernelType`（lower＋trim＋去连字符），`Lookup` 改用它；新增导出 `CanonicalKernelType`（命中则返 `info.Type`，未命中返归一化后的输入，DB 面对未知名保持今日 not-found 行为）。
- `go-backend/internal/http/handler/kernel.go:1057`（`inboundSave`）：`GetNodeKernelByType` 的入参改为 `kernel.CanonicalKernelType(req.KernelType)`。
- 去连字符无碰撞：目录仅 4 型（xray/singbox/snell/realm），`"sing-box"`→`"singbox"` 是唯一带连字符的别名。
- 前端不动：前端只发规范名 `singbox`（3 处）。

### 测试

新增 `go-backend/internal/kernel/capabilities_alias_test.go`（4 测全过）：`Lookup("sing-box"/"Sing-Box"/"SING-BOX"/" sing-box "/"singbox"/"SingBox")` 全命中 `TypeSingbox`；其余三核不受影响，未知名仍查无；`CanonicalKernelType("sing-box")=="singbox"`，未知名原样归一化透传；`Normalize("sing-box", vless:12345)` 全程通过，`Normalize("no-such-kernel")` 仍报“暂不支持内核 no-such-kernel”。

## 测试总结果

- `go test./internal/kernel/ -count=1`：**ok（2.405s）**，含 4 个新增别名测试。
- `go test ./internal/http/handler/ -timeout 1800s -count=1`: ok (639.380s), full package passes.
- `gofmt`：5 个改动文件全净；`go vet./internal/kernel/` 净。

## 改动清单（git）

分支 `fix-m0051-m0001`，只提交以下 5 个文件（`go-backend/go.mod` 的 2 行改动是入场前已有的他路状态，不动、不提交）：

- `go-backend/internal/http/handler/upgrade.go`：回执下发语义＋`markNodePendingUpgradeRedeploy`。
- `go-backend/internal/http/handler/r6_rollback_receipt_test.go`：新契约三测。
- `go-backend/internal/http/handler/kernel.go:1057`：`inboundSave` 查库用规范名。
- `go-backend/internal/kernel/capabilities.go`：`normalizeKernelType`＋`CanonicalKernelType`＋`Lookup` 改用。
- `go-backend/internal/kernel/capabilities_alias_test.go`（新增）：4 测。

## M-0001 验活重跑方案（等主控通知再上机）

前置：新面板上测试机。已装 `singbox` 内核的节点上传 `"sing-box"` 调三面，预期一致通过：
1. `/kernel/versions`（`kernelType: "sing-box"`）→ code 0，版本列表与 `"singbox"` 同值；
2. 入站创建（`kernelType: "sing-box"`）→ 不再报“请先在该节点安装内核”，成功落盘；
3. `Normalize` 面（经入站创建复核）→ 不再报“暂不支持内核 sing-box”。
反向腿：`"no-such-kernel"` 三面仍 fail-closed（未知内核／请先安装内核／暂不支持），文案与改前一致。驱动侧 M-0001b 腿（规范名 PASS、别名 SKIP 记账）由 DRV-FIX-DOC-AG 批改为 PASS 预期，重跑时一并核对。
