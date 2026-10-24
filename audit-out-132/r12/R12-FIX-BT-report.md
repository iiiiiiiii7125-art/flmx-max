# R12 修复报告 — 批 T（Tunnel.Flow 域外值族）

- **基线**：`main` @ `cefb566`（= `integrate-r11` 尖端），工作分支 `fix-r12/bT`（自 `cefb566` 切出；未动 `main`、未动 `integrate-r12`，集成由协调者做）。
- **分支修复注记（据实登记）**：提交初次落点时共享仓库内并行批工作已切走当前分支，提交误落在 `fix-r12/bX` 尖端（`51de1b7`，父级为批 X 报告提交 `2c5f4d5`）；已用 cherry-pick 在 `fix-r12/bT` 上重建（父级 `cefb566`），并将 `fix-r12/bX` 指回其原尖端 `2c5f4d5`（仅移除误落的本批提交，批 X 提交本身未动）。最终以 `fix-r12/bT` 尖端为准。
- **依据**：`audit-out-132/r12/R12-FIX-PLAN.md` §1 批 T、 `R12-X-verdicts.md` §0.1 / §0.3 / §1 / §2.1。任务书口径未改、条目未增删；断言更新仅 FIX-PLAN 点名的 T-d 一处。
- **不做项遵守**：存量 `Flow:0` 行未迁移（dashboard 三态下显「未知」即诚实态）；`validateTunnelFlow` 调用点未扩展（仍为 `mutations.go:1268` / `:1750` 两处，联邦路径改值即收口）。

## 施工（4 文件，17 增 / 4 删）

| 文件 | 改动 |
|---|---|
| `vite-frontend/src/pages/dashboard.tsx`（T-a） | UT 卡片标签由二态改三态（见下） |
| `go-backend/internal/store/repo/repository_federation.go:142`（T-b） | `CreateFederationTunnel` 内 `Flow: 0` → `Flow: 1` |
| `go-backend/internal/http/handler/mutations.go`（T-c） | `tunnelCreate` 内、 `asInt64`/`asFloat` 默认化之前补 FIX2-A 守卫（新增 11 行，纯插入） |
| `go-backend/tests/contract/tunnel_ip_preference_contract_test.go:48`（T-d） | create 负载 `"flow":99999` → `"flow":1`（FIX-PLAN 点名，测试夹具改动） |

`git diff` 核对：除上述 4 文件外无改动；T-d 的 `:105` update 负载未动（`cefb566` 已处置为 `"flow":1`，本批 diff 不含该行）；update 侧守卫（`mutations.go:1736` 起）未动。

## T-a 展示面 — 关闭

- 改动：卡片标签三态 — `tunnelFlow===1 → 「单向计费」`（沿用蓝色）/ `===2 → 「双向计费」`（沿用橙色）/ 其他 → 「未知」（新增中性灰态 `bg-gray-100 dark:bg-gray-500/20 text-gray-700 dark:text-gray-300`，沿用现有两色之外的中性态，未新增计费语义）。
- 语义对照：与列表面 `getTunnelFlowDisplay`（`vite-frontend/src/pages/tunnel/form.ts:255-264`）分区一致（1 / 2 / 其他）；名词沿用卡片原有「计费」措辞（列表面为「单向计算/双向计算」），任务书逐字口径为卡片标签「单向计费/双向计费/未知」，故未把列表面措辞搬入卡片。
- 三态逐值复核（取自改后三元表达式的等价逻辑演算）：`0 → 未知`、`1 → 单向计费`、`2 → 双向计费`、`3 → 未知`。存量 `Flow:0` 行显「未知」：代码证据如上（`0` 落入其他分支）；真机（浏览器实测）未做——本侧为通用子代理、无 live browser 操作权限，门 #1 的真机逐值确认留待协调者/门验证环节补做，不冒充已真机验证。
- 前端回归：`tsc --noEmit` 零输出、exit 0。

## T-b 联邦产出 — 关闭

- 完备性 grep（施工前，联邦路径内第二处 `Flow:` 字面硬编码排查）：
  - `repository_federation.go` 内 `Flow` 字面仅 `:142` 一处（`Flow: 0`，已改 `1`）；`CreateFederationTunnel` 全仓唯一生产调用点为 `handler/federation.go:1439`，联邦 handler 内无另建 `model.Tunnel{...}` 字面。
  - 全仓 `Flow: 0` 非测试命中仅此一处；其余 `Flow:` 命中均为变量回填（`t.Flow` / `u.Flow` / `ut.Flow` 等）或 `InFlow/OutFlow: 0`，非联邦产出硬编码——**登记：无第二处，同批无并入项**。
- 计费归一同值：与 `repository_flow.go:95-97` 的 `TunnelFlow <= 0 ⇒ 1`（按单向入账）同值。
- 先红后绿（临时探针测试，验证后已删除、不入提交）：修复前 `TestZZR12BT_FederationFlow` FAIL（`expected federation tunnel flow=1, got 0`）；修复后 PASS（`CreateFederationTunnel` 新建隧道落库 `flow=1` 实测）。

## T-c create 守卫 — 关闭

- 改动位置：`tunnelCreate`（`mutations.go:1219` 起）内、名称判空/判重之后、`typeVal/flow/status/trafficRatio` 默认化之前，补与 `tunnelUpdate` 同形的守卫：
  - `presentNumericFieldInvalid(req, "flow")` → `flow 参数无效`；
  - `presentFloatFieldInvalid(req, "trafficRatio")` → `trafficRatio 参数无效`；
  - 均为点名拒绝、不落库。范围严格按任务书：仅 `flow` / `trafficRatio`，未扩展到 `type` / `status`；update 侧守卫不动。
- 先红后绿（临时探针测试，验证后已删除、不入提交；探针以 `tunnel/create` API 直调、与 T-d 同形节点负载）：
  - 修复前（RED）：`flow:"abc"`、`flow:1.5`、`trafficRatio:"abc"` 三格均 `code 0（操作成功）` 且各落库 1 行——即静默默认旧态实证；
  - 修复后（GREEN）：三格均被点名拒绝（`code != 0`）且各落库 0 行；合法值 `flow:2, trafficRatio:1.5` 照常入库（`code 0`，1 行）。

## T-d 契约夹具 — 关闭

- 改动：仅 `tunnel_ip_preference_contract_test.go:48` create 负载 `"flow":99999 → "flow":1`；`:105` update 负载不动（见上）。
- 修复前（RED，基线实测）：`TestTunnelCreateWithIPPreferenceContract` 整体 PASS 但三子测全部 SKIP（`tunnel not created (nodes offline), skipping DB verification`）——即 R12X-004 所指 vacuous skip 实证，`ip_preference` 落库断言未被执行。
- 修复后（GREEN）：三子测全部 PASS（非 Skip），`v4-preference` / `v6-preference` / `empty-preference` 的 `ip_preference` 落库断言实际执行并通过；`TestTunnelUpdateIPPreferenceContract` 同批 PASS。

## 回归结论

- 后端：`go build ./...` 通过；`go vet ./internal/http/handler/ ./internal/store/repo/ ./tests/contract/`（零输出）通过；`go test ./internal/http/handler/` ok（438.9s）、`./internal/store/repo/` ok（190.6s）、`./tests/contract/` 全量 ok（46.7s）。
- 前端：`tsc --noEmit` 通过（exit 0）。
- 未触碰有意设计护栏：备份恢复逐字落库、存量行语义、`validateTunnelFlow` 调用点、update 侧守卫均未动。

## 四断言关闭登记

| 断言 | 状态 | 关闭证据 |
|---|---|---|
| T-a 展示三态 | 关闭（代码级；真机待门 #1 补验） | 三态逐值演算 `0/1/2/3 → 未知/单向/双向/未知` + tsc 通过 |
| T-b 联邦值 0→1 | 关闭 | 新建联邦隧道落库 `flow=1` 实测 + 完备性 grep（无第二处） |
| T-c create 守卫 | 关闭 | 先红后绿：三格拒 + 合法值入库 |
| T-d 夹具 :48 | 关闭 | 契约测试不再 Skip，三子测 PASS，`:105` 未动 |

## 待协调者 / 未关闭项

1. 门 #1（dashboard 卡片真机逐值）与门 #2（联邦建隧道计费面真机）需 live 环境补验——本侧无 live browser，报告中 T-a 真机项如实标为未做，不冒充。
2. 提交号：见最终汇报（提交后以汇报中的提交号为准）。
3. 临时探针测试文件 `go-backend/tests/contract/zz_r12bt_red_test.go` 已删除，最终提交仅含上述 4 文件。

— R12-FIX-BT END
