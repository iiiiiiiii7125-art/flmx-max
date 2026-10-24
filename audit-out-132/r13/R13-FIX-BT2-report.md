# R13 修复批 T2 报告（Tunnel 字段写入/编辑/恢复三面）

- 批：T2（R13X-002 / R13X-003 / R13X-007，均为 P3）
- 依据：`audit-out-132/r13/R13-X-ledger.md`、`R13-X-verdicts.md §1`、`R13-FIX-PLAN.md §2`
- 基线：`integrate-r13` 树，产品内容与 `60a4392`（最近发布 tag 1.4.4）一致；
  本分支切自 `integrate-r13 @ 9571433`，该提交相对 60a4392 仅为文档批
  （`R13-BC-draft.md` / `R13-R12X002-redetermination-draft.md` /
  `R13-FIX-BDOC-report.md` 三文件），产品代码零差异。
- 分支：`fix-r13/bT2`（施工分支，不合入 `integrate-r13`，由协调者统一集成）
- 提交：产品与测试提交 `cad99e4` + 本报告提交（本文件随第二提交入分支）
- 纪律：禁 `git add -A`；本批只 add 明确文件，另以补丁过滤方式从
  `repository.go` 工作区差异中只暂存我方 hunk——同文件内批 R 并行
  施工的 inbounds 计数 / `validateBackupTypes` 等 hunk 未暂存、未提交，
  仍留在工作区归批 R；`model.go`、`repository_inbound_backup.go`、
  `backup_r13_br_test.go`（两包）均未动、未提交。

## 1. 逐条关闭结论

| 断言 | 条目 | 结论 |
|---|---|---|
| T2-a | R13X-002（P3） | ✅ 关闭 |
| T2-b | R13X-003（P3） | ✅ 关闭（后端探针 + 前端构建产物双证据，见 §3） |
| T2-c | R13X-007（P3） | ✅ 关闭 |
| 合计 | 3 断言 | **关闭 3 / 未关闭 0** |

任一断言未过则对应条目不关闭——本批三断言逐条全过，无部分关闭。

## 2. T2-a（R13X-002）：tunnelCreate 守卫键表补齐

- 改动：`go-backend/internal/http/handler/mutations.go` tunnelCreate
  守卫段（原 `flow` + `trafficRatio` 两键）改为与 tunnelUpdate 同形
  的四键循环：`type`、`flow`、`status` + `trafficRatio`，文案沿用
  update 同格（`%s 参数无效`）。status 数值域外枚举门（L4 O-3）
  不在本断言内，未补——只拦不可解析输入。
- 先红后绿：新增 `go-backend/internal/http/handler/r13_t2a_tunnel_create_guard_test.go`。
  修复前红：`type:"abc"`、`status:"abc"`、`type:1.5` 三例均建库成功
  （测试 FAIL）；修复后绿。
- 断言逐条结果：
  - `type:"abc"` → 拒，文案含 `type 参数无效`，不落库 ✅
  - `status:"abc"` → 拒，文案含 `status 参数无效`，不落库 ✅
  - `type:1.5` → 拒，文案含 `type 参数无效`，不落库 ✅
  - 对照组 `flow:"abc"` → 仍被拒（R12X-003 断言在位，修复前后均拒）✅
  - 合法整数 `type:1/status:1/flow:2/trafficRatio:2.5` 建库逐字 ✅
- 回归锚点：既有 `TestR11B4*` 全过（见 §5）。

## 3. T2-b（R13X-003）：面板编辑表单按 presence 口径省略

- 定版方向：① 省略（verdicts §1.2）；备选方向 ②（诚实阻断提示）不采。
- 改动：`vite-frontend/src/pages/tunnel.tsx`（仅此一文件）：
  - `handleEdit` 打开编辑时以 ref 记录存量原值
    `editOriginalFlowRatioRef = {flow, trafficRatio}`；`handleAdd`
    新建时清零该 ref；
  - `handleSubmit` 提交前算 `omitFlow` / `omitRatio`：仅当编辑模式、
    表单值与原值相等（用户未改选）、且原值域外（flow ∉ {1,2} 或
    ratio ∉ [0,100]）时为真；用户显式改选后对应 omit 恒为假，
    新值全量提交，不得被省略逻辑吞掉；
  - 省略以**删键**实现（`delete payload.flow / trafficRatio`），
    后端缺席字段按 presence 口径回填存量、原样保留；新建路径
    仍用原 `data` 全量提交，不受影响；
  - 校验侧配套豁免：将被省略的 ratio 在校验时以合法占位值 1
    代入 `validateForm`（`validateTunnelForm` 的 ratio 范围校验
    否则先于省略拦住无关编辑）；占位值只进校验器、不进负载。
    flow 无客户端范围校验，无需占位。
- 断言逐条结果：
  - 表单形态探针（全量发存量 `flow=0`）：后端 update 侧在场校验
    仍拒（既有 `TestR11B4TunnelUpdateRejectsOutOfRangeRatioAndFlow`
    的 `flow-zero` 例，修复前后均拒）——即面板旧行为确为被挡，
    本修复改的是面板请求形态而非后端断言 ✅
  - 省略形态探针（请求缺席 flow/trafficRatio）：既有
    `TestR11B4TunnelUpdatePartialPreservesStockOutOfRange` 全过——
    存量 `flow=0 / traffic_ratio=-2` 行无关编辑（改名）成功、
    存量值原样保留 ✅
  - 合法行编辑行为不变：既有 `TestR11B4TunnelUpdateAcceptsLegalRatioAndFlow`
    全过；前端合法行提交路径（omit 恒假）与改前一致 ✅
  - 显式改选不被吞：代码级保证（omit 以 `form 值 === 原值` 为
    前提）+ 构建产物证据（下）✅
- 前端验证（无 Vitest/Jest 基建，不新增前端测试，按 AGENTS 惯例）：
  - `tsc` 通过；`vite build` 通过（`✓ built in 8.46s`）；
    注：本机无 `pnpm` 二进制，故直接跑与 `pnpm run build`
    （`tsc && vite build`）等价的 `node_modules/.bin/tsc` +
    `node_modules/.bin/vite build`；
  - 构建产物核对：`dist/assets/tunnel-CMHjx6kp.js` 含
    `T&&t&&delete re.flow,T&&n&&delete re.trafficRatio`（T=isEdit、
    t=omitFlow、n=omitRatio），新建路径仍用原负载——省略逻辑
    确已进包且仅作用于编辑路径。
- 边界（不改动 R11X-009 断言本身）：R11X-009 的 API 级保证以
  请求形态为前提；本修复是让面板请求形态与该前提对齐，两面
  收敛，无矛盾（verdicts §1.3 原句口径）。

## 4. T2-c（R13X-007）：importTunnels 恢复面复用保存面两门

- 改动：`go-backend/internal/store/repo/repository.go`：
  - 新增 `validateImportedTunnelFlow`（∈ {1,2}）与
    `validateImportedTunnelTrafficRatio`（∈ [0,100]，含 NaN/Inf
    拒），合法集与文案口径与 handler 包 `validateTunnelFlow` /
    `validateTunnelTrafficRatio` 逐字同集；
  - `importTunnels` 开头先过门、再预删：域外行 `rep.skip("tunnels")`
    + 逐行 `warnf` 点名（id、名、域外值，与 forwards status 门
    同形）；钳制 + warning 备选不采。
- 字面复用说明（须向协调者交代）：handler 包依赖 repo 包，repo
  直接引用 handler 的校验函数将成包循环，故字面函数复用不可行；
  本批以同合法集本地门镜像实现，并在函数注释中写明“两门任一
  漂移即视为本断言失效，须同步改动”。若终局审计坚持字面复用，
  须另立重构断言（把校验函数下移至共享包），不在 T2-c 名下扩面。
- 先红后绿：新增 `go-backend/internal/store/repo/backup_r13_t2c_test.go`。
  修复前红：`TunnelsImported = 6`（域外行全部落库）；修复后绿。
- 断言逐条结果（`Import(backup, ["tunnels"])`）：
  - `Flow=99999` 行被跳过且回执点名（名 `t2c-flow-huge`）✅
  - `TrafficRatio=-5` 行被跳过且回执点名（名 `t2c-ratio-neg`）✅
  - `TrafficRatio=200` 行被跳过且回执点名（名 `t2c-ratio-over`）✅
  - `Flow=0` 行被跳过且回执点名（名 `t2c-flow-zero`）✅
  - `SkippedRows["tunnels"] = 5`（含同 id 存量行的第 5 行，见下）✅
  - 合法隧道行（flow=1/ratio=1）逐字落库，`TunnelsImported = 1` ✅
  - 跳过不殃及存量：与被跳过行同 id（21）的存量行名/flow 原样
    保留——门在预删之前，跳过行的存量链路不被预删 ✅
- 回执点名形态：逐行 warning，如
  `备份恢复：隧道 #12（"t2c-flow-huge"）的 flow=99999 不在合法枚举 {1,2} 内，已跳过该行未恢复`。

## 5. 回归结论

- `go build ./...`：通过（隔离副本，见下注）。
- `go vet`：handler 包与 repo 包均通过（隔离副本）。
- 受影响包全量测试（隔离副本，见下注）：
  - `go test ./internal/http/handler/ -count=1`：**ok（361.8s）**
  - `go test ./internal/store/repo/ -count=1`：**ok（82.8s）**
  - 受影响包产品失败 **0**。
- 靶向测试：`TestR13T2a*` + `TestR11B4*` 全过；
  `TestR13T2c*` + `TestR5B5ImportStatusEnumGate` 全过。
- 前端：`tsc` + `vite build` 通过（见 §3）。
- **隔离副本注**：本机共享工作树内批 R 正并行施工，施工中
  `repository_inbound_backup.go` / `backup_r13_br_test.go`（两包）
  一度进入不可构建的中间态（`inboundImportCounts` 类型切换未
  闭合、`Scan(&a, &b)` 多参），致共享树内 `go test ./internal/store/repo/`
  与 `go vet ./internal/http/handler/`（经 repo 包）当时无法构建
  ——该破损属批 R 在途改动、非本批文件所致，本批未触碰上述文件。
  故全量回归改在 `/tmp/t2c-check/go-backend` 隔离副本执行：该副本
  取自破损前一致态，仅剔除批 R 的在途测试文件，产品代码与本批
  提交内容一致。协调者集成时须以集成树重跑为准。

## 5b. 修 A 漏 B：T2-c 门触发的既有测试夹具更新（属本批回归范畴）

首轮全量跑暴露 9 项既有失败（handler 1 项 + repo 8 项），根因同一：
既有备份夹具的隧道行 flow 值在 R11 前是随意占位（种子直写
`1000` / `99999`，或构造 `TunnelBackup` 时缺省 `0`），flow 值与
用例目的无关；T2-c 门落地后这些行按计划应被跳过，用例的原断言
（链路恢复、往返零警告等）随之失效——这是门生效的必然结果，
不是门本身的误伤（T2-c 断言明文要求 `Flow=99999` 与 `Flow=0`
行被跳过）。处置：只改夹具、不改产品门与用例断言——

- `backup_g2_test.go`：种子隧道 flow `1000` → `1`（3 处：链路
  NULL 语义夹具、漂移对照行、限速绑定种子行）；
- `backup_r2_b8_test.go`：隧道夹具补 `Flow: 1`（3 行：全量恢复
  t1、ratio-zero 的 free/half 行；ratio=0 本身合法，不受影响）；
- `backup_r3_b6_test.go`：往返零警告夹具隧道补 `Flow: 1`；
- `backup_r4_b4_test.go`：全量恢复夹具隧道 t1/t2 补 `Flow: 1`；
- `backup_r5_b5_test.go`：嵌套预删夹具隧道补 `Flow: 1`（2 处：
  跳过子例与干净对照子例，后者被前者先失败所掩盖、一并修复）；
- `backup_r4_b5_test.go`（handler）：tunnels-only 用例在导入前
  把导出行的 flow 归一为 `1`（附 R13X-007 注记）；共享种子
  helper `seedRelayTargetTunnel` 本人未动（多用例共用，flow 值
  与其余用例目的无关，局部归一 blast 最小）。
- 修复后 9 项靶向重跑全绿，再全量两包 ok（见 §5）。

## 6. 未验证清单

- 真机验证（靶向验证门 #2 / #3 的面板与 DB 核对面）：本批未做
  Lightsail 真机验证——本地 SQLite 探针 + 构建产物核对已覆盖断言
  的代码面，门 #2 / #3 的真机结论归靶向验证门，不由本报告冒充。
- 前端交互真机点击（存量 flow=0 行在面板上改名成功）：未在
  浏览器内实点；以后端省略形态探针 + 构建产物删键证据为
  准，面板实点归门 #3。
- 批 R 文件（`repository_inbound_backup.go` 等）的最终集成态：
  不属本批，集成时重验。
- L4 O-2（userCreate 等其他 create 入口同类形态）与 O-3
  （status 数值域外枚举门）：按禁扩面要求不占号、不并入本批。

## 7. 禁扩面自查

- 未补 status 枚举门（O-3）：T2-a 只拦不可解析输入，`status:5`
  等数值域外值仍按旧口径落库（与断言边界一致）。
- 未碰 userCreate 等其他 create 入口（O-2）。
- 未改 R11X-009 / R12X-003 断言本身；未改动 agent 协议与 DB schema。
- 未改动有意设计护栏（presence 口径、fail-closed 守卫语义均维持）。

— R13-FIX-BT2-report END · 批 T2 关闭 3/3 · END
