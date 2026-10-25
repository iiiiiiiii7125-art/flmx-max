# R19-FIX-F03 修复报告：删转发补清 climiter（R19-X-03）

- 基线：main @ d66042c（`d66042cc0b7ac011ddfad09f3f2709b90dafda2e`）
- 分支：`fix-r19-f03`（worktree `~/workspace/.fixr19-f03`，未合 main、未打 tag、未发版、未改版本字面量）
- 依据：`R19-FIX-PLAN.md`（F-03 节）、`R19-X-ledger.md`（R19-X-03）、`R19-X-verdicts.md`（V-04）
- 结论：**机理证实，修复已落地**。删转发路径确实从不清理 climiter；修后删转发会向节点补发 `DeleteCLimiters`，先红后绿闭合。

## §1 机理取证（机理先行，三线证据 + 一项阴性对照）

### 1.1 代码取证：创建只加、删除只删服务，中间没有删 climiter 的路

- 创建/更新：`pushForwardServicesToNode` 在 `go-backend/internal/http/handler/control_plane.go:395,437` 对每份 conn limiter 配置调 `ensureConnLimiterOnNode`（`control_plane.go:2196`），下发 `AddCLimiters`，已存在时改发 `UpdateCLimiters`；服务配置带 `"climiter"` 键（`control_plane.go:2041-2042`）。
- 删除：全部删转发漏斗只发 `DeleteService`，无任何 `DeleteCLimiters`：
  - `controlForwardServices` 的 `DeleteService` 分支（`control_plane.go:643` 起，分支内只调 `deleteForwardServiceBasesOnNode`，`control_plane.go:602`，只发 `DeleteService`）；
  - `deleteForwardServicesOnNodeBatch`（`mutations.go:2241`，只发 `DeleteService`）；
  - `deleteForwardServicesOnNode`（`control_plane.go:567`）同样只删服务。
- 全仓 grep：除 federation peer-share 路径（`federation_resources.go:112,345`，`peerResourceDeleteCommand("climiter") → "DeleteCLimiters"`，只用于跨面板共享资源）外，**没有任何删转发代码发过 `DeleteCLimiters`**。即：`AddCLimiters` 有生产方，`DeleteCLimiters` 在转发生命周期内没有生产方。
- 自愈不兜底：agent 启动自愈 `healPersistedConfig`（`go-gost/program.go:330`）只按名去重（`go-gost/x/config/dedupe.go` 的 `HealPersistedCLimiters`/`DeduplicateCLimiters`），从不剪除无引用的 limiter；孤儿一旦落盘，重启、SIGHUP 都清不掉。
- 接收端完好：agent 侧 `handleDeleteCLimiter`（`go-gost/x/socket/websocket_reporter.go:1520`）→ `deleteConnLimiter`（`go-gost/x/socket/limiter.go:192`）从注册表注销并从 `c.CLimiters` 剔除，经 `config.OnUpdate`（`go-gost/x/config/config.go:134-144`，成功即 `persist()` 落盘）。缺的只是发出方，不是接收方。

### 1.2 真机复现（R19-L1 已采集的现场证据，本次复核原始文件确认）

forward 29 的完整生命周期写在 agent 日志里（`R19-L1-f03-ws-journal.log`、`R19-L1-t3del-recheck.log`、`R19-L1-gost-now.json`，测试机 ip-172-26-9-119，d66042c 构建）：

1. 03:18:32 `AddCLimiters {"limits":["$ 5"],"name":"rule_conn_limit_29"}`，随即 `UpdateService` 下发 `29_1_0_tcp/udp`，服务带 `"climiter":"rule_conn_limit_29"`；
2. 03:18:51 `AddCLimiters {"limits":["$ 7"],...}` 后改发 `UpdateCLimiters`（已存在回退）；
3. 03:20:05 SIGHUP 自愈：`removed duplicate conn limiter(s) [rule_conn_limit_29]`——只去重、保留首份，不剪孤儿；
4. 03:20:36 重连清理两次 `DeleteService ["29_1_0_tcp","29_1_0_udp","29_1_0"]`，服务消失；
5. 终态 `R19-L1-gost-now.json`：服务 12 个、无 `29_*`，但 `climiters` 仍含 `rule_conn_limit_29`。**建带限流转发 → 删转发 → gost.json 残留 climiter，复现成立。**

阴性对照：R19 全部 L1/L5 日志与 JSON 里 `DeleteCLimiters` 出现 **0 次**，而 `AddCLimiters`/`UpdateCLimiters` 在 WS 日志中出现 6 次。只加不删不是推测，是日志里的事实。

### 1.3 原始孤儿 `rule_conn_limit_12` 的定性

- `R19-L1-gost-pristine.json`：`climiters` 为 `rule_conn_limit_2 ["$ 1"]` 与 `rule_conn_limit_12 ["$ 7"]`；12 个服务**无一带 `climiter` 键**；`R19-L1-api-forward-list.json` 中现存 6 个转发（id 1/2/3/4/6/7）`maxConn` 与 `ipMaxConn` **全为 0**。
- `rule_conn_limit_12`：对应 forward 12 已不存在，符合 §1.1/§1.2 的删转发残留机理（删时只删服务、不删 climiter）。**机理证实，本项按计划走修复分支，不降级为观察。**
- `rule_conn_limit_2`：对应 forward 2 **仍存活**，但现有限制全为 0、服务已无 `climiter` 引用——这是**第二种孤儿机理**：改转发去掉限流时，`syncForwardServicesWithWarnings`（`control_plane.go:395-437`）只 ensure 新配置，从不删除失势的旧 rule 名。属同一根因在更新路径的表现，但**不在本次任务范围**（任务只定删转发路径），本次不修，记入 §5 后续项。存量孤儿亦不由本次清理（面板侧已无 forward 12 的落点记录，无从安全定位其曾在节点；forward 2 的清理属更新路径后续项）。

## §2 改动（只改后端删转发路径；agent 侧接收端本就完好，不动）

### 2.1 新增：`forwardRuleConnLimiterName` + `deleteForwardConnLimiterOnNode`

`go-backend/internal/http/handler/control_plane.go:2238-2260`：

- `forwardRuleConnLimiterName`（`:2238`）：仅当 `MaxConn>0 或 IPMaxConn>0` 时返回 `rule_conn_limit_<id>`，与 `buildConnLimiterConfigs` 发出该名的条件一致；无限制返回 `""`，调用方跳过。`user_conn_limit_<uid>` 是同用户全部转发共享的，永不在此删除。
- `deleteForwardConnLimiterOnNode`（`:2252`）：服务已删后发 `DeleteCLimiters {"limiter": <name>}`（`tolerateNotFound=true`）。失败只记 `log.Printf`、不向调用方返回错误：服务已删、删库不应被 cosmetic 残留挡住；节点离线时调用方本就走 S-04/重连清理分支、会经同一漏斗重试。agent 侧对不存在的名字亦返回成功，故重复发送无害。
- 顺手收敛命名：`buildConnLimiterConfigs` 内两处 `fmt.Sprintf("rule_conn_limit_%d", ...)`（`:2222`、`:2229`）改调 `forwardRuleConnLimiterName`，防日后漂移；行为不变（已有 `control_plane_test.go` 的 `buildConnLimiterConfigs` 用例覆盖）。

### 2.2 接线点一：`controlForwardServices` 的 `DeleteService` 分支

`control_plane.go:681-697`（调用在 `:697`）：每节点 `DeleteService` 成功后补清 climiter；服务已不在且调用方容忍缺失（`tolerateNotFound && isNotFoundError`）时仍清 climiter 收敛；节点离线（`isNodeOfflineOrTimeoutError`）时跳过——该节点已记入 `offlineNodes`、调用方不删库，重连清理经同一漏斗重试。覆盖调用方：单删/批量删转发（`mutations.go:3653,4049`）、`removeForwardWithServices`（`kernel_relay.go:600`）、隧道删除（`tunnel_delete_resolution.go:797`）、重连/孤儿清理（`mutations.go:7434`）。

### 2.3 接线点二：`deleteForwardServicesOnNodeBatch`

`go-backend/internal/http/handler/mutations.go:2291-2297`（调用在 `:2296`）：`DeleteService` 成功返回前补清 climiter；失败直接返回、不清（服务可能还在）。nft/realm 分支在上方已提前返回，不到这里。覆盖调用方：换隧道旧节点清理（`mutations.go:3576,3599,4450,4465`，旧节点清、新节点由随后 `syncForwardServices` 重建 limiter，无冲突）、隧道删除迁移（`tunnel_delete_resolution.go:660,680`）、节点删除（`mutations.go:6799`）、入口节点移除（`mutations.go:2363`）。

### 2.4 刻意不接的点（防误伤，转发仍存活）

- `deleteForwardServicesOnNode`（`control_plane.go:567`）**不接**：调用方均为转发存活场景——更新回退清理（`control_plane.go:455`）、自占端口重绑（`:510`，删后立即重建服务）、realm 引擎切换（`realm.go:363`）。在此删 climiter 会把活转发的限流删掉。
- 更新路径（§1.3 的 `rule_conn_limit_2` 机理）本次不修，见 §5。

## §3 先红后绿

### 3.1 红（修前，基线 d66042c）

- 后端 `go-backend/internal/http/handler/r19_fix_f03_test.go`（新）：`newCleanupAgent` 真实 WS 传输 + 模拟节点即时 ACK，断言删转发时发出的命令。
  - `TestR19F03DeleteForwardControlRemovesConnLimiter`：**FAIL**——`expected exactly 1 DeleteCLimiters command, got 0`（`controlForwardServices` 漏斗）。
  - `TestR19F03DeleteForwardBatchRemovesConnLimiter`：首跑因 `/tmp`（512M tmpfs）写满报 `auto migrate: database or disk is full`（环境红，与产品无关；改 `TMPDIR` 到 workspace 后重跑见 §3.2）。
  - `TestR19F03DeleteForwardWithoutLimitsSendsNoDeleteCLimiters`：PASS（无限制不发，红态即符合预期）。
- agent 侧 `go-gost/x/socket/r19_fix_f03_test.go`（新）：`TestR19F03DeleteCLimiterRemovesFromGlobalConfig` 与 `TestR19F03DeleteCLimiterAbsentIsNoop` 修前即 **PASS**——接收端本就完好，作为钉子测试，证明链路断点只在面板发出方。

### 3.2 绿（修后）

- 后端 3/3 PASS（`go test ./internal/http/handler/ -run 'TestR19F03' -count=1`，2.7s）：
  - Batch 漏斗：`DeleteService` 后恰 1 条 `DeleteCLimiters {"limiter":"rule_conn_limit_<id>"}`，顺序先服务后 climiter；
  - Control 漏斗（仅 `IPMaxConn=5` 形）：同上，覆盖 `IPMaxConn` 单独成名的分支；
  - 无限制转发（`MaxConn=0, IPMaxConn=0`）：两漏斗均 **0 条** `DeleteCLimiters`。
- agent 侧 2/2 PASS（`go test ./socket/ -run 'TestR19F03' -count=1`）：`handleDeleteCLimiter` 清注册表 + 全局配置；删不存在的名字返回 nil 且不碰其他 limiter（双漏斗重复发送安全）。

## §4 回归

- `go build ./...`：go-backend、`go-gost`、`go-gost/x` 三模块均 exit 0。
- `go vet ./...`：go-backend、`go-gost`、`go-gost/x`（`./socket/ ./config/`）均 exit 0。
- `go test ./...`（`-count=1`）：
  - go-backend：除 handler 包外全部 `ok`（middleware、kernel、logredact、metrics、monitoring、netguard、runtime/nftables、security、store/repo 278s、ws、tests/contract 186s）；handler 包全量在默认 10m 超时下被 `go test` 掐断（**非产品失败**：超时 panic 栈停在无关测试 `TestR11B4…` 的 `repo.Open` → `bcrypt` 种子处，系包体量大、沙箱慢），以 `-timeout 30m` 重跑：**`ok go-backend/internal/http/handler 649.426s`，0 FAIL**。
  - `go-gost/x`：全部 `ok`，exit 0（含 socket 2.773s、config、kernel 16.441s 等）。
  - `go-gost`：`ok tests/lifecycle 20.817s`，exit 0。
  - 产品失败 0；本次回归中未出现沙箱 fake-IP DNS 环境红（故无须援引该豁免）。
- `gofmt -l`：`control_plane.go` 有一处既存格式差异（`:167` 注释 `//（R5-B5…` 的空格，基线即有，与本次改动无关，未动）；本次改动行本身无格式问题。`mutations.go` 不在 `gofmt -l` 名单内。

## §5 未验证项、残余风险与后续项（不属本次修复）

1. **真机端到端未验证**：沙箱以 `newCleanupAgent` 模拟节点断言命令发出、agent 侧断言配置清除；面板删转发 → 真机 `gost.json` 无孤儿的端到端腿，须 R20 在测试机上跑（建带限流转发 → 删 → 查 `gost.json`）。本次未动测试机（R19-PROC-01 锁纪律亦要求不跨 lane 上机）。
2. **存量孤儿未清理**：测试机 `gost.json` 现存 `rule_conn_limit_12`（forward 12 已删）与 `rule_conn_limit_2`（forward 2 存活、限制已清）仍在。本修复只防新增孤儿，不清存量；`rule_conn_limit_12` 的清理须在测试机上人工发一次 `DeleteCLimiters`（或随 R20 真机腿验证时一并处理）。
3. **更新路径孤儿（`rule_conn_limit_2` 机理）**：改转发去掉限流不删 climiter，须另立项修（在 `syncForwardServicesWithWarnings` 服务推送成功后，比对新旧配置、删失势的 rule 名；须注意先推服务后删 limiter 的顺序）。本次任务书只定删转发路径，故不越界。
4. **残余风险（小）**：`DeleteCLimiters` 本身瞬时失败（如节点抖动）时只记日志、无重试——服务已删成功的前提下，孤儿可能以小概率残留；与既有 best-effort 清理同口径，接受为残余风险。离线节点不受此影响（S-04 阻断删库、重连清理重试）。
5. **同型相邻面未动**：`rule_traffic_limit_<id>`（IP 限速）与 `user_conn_limit_<uid>`（用户级共享）存在同型残留可能（删转发/改限制均不删），本次只修 `rule_conn_limit_<id>`；如需处理须另核共享语义（`user_conn_limit_*` 尤其不可随单转发删除），不建议顺手扩围。
6. **前端/文档/版本字面量**：均未改；无 BC。

## §6 交付清单

- 分支 `fix-r19-f03`（自 main @ d66042c），提交 1 个（见 §6.1），未合 main、未推远端、未打 tag、未发版。
- 改动文件（`git add` 逐文件明确加入，禁用 `git add -A`）：
  - `go-backend/internal/http/handler/control_plane.go`（+`log` 导入、DeleteService 分支接线、命名收敛、`forwardRuleConnLimiterName` + `deleteForwardConnLimiterOnNode`）
  - `go-backend/internal/http/handler/mutations.go`（Batch 接线）
  - `go-backend/internal/http/handler/r19_fix_f03_test.go`（新，后端先红后绿 3 测）
  - `go-gost/x/socket/r19_fix_f03_test.go`（新，agent 侧钉子 2 测）
  - `audit-out-132/r19/R19-FIX-F03-REPORT.md`（本报告）

### 6.1 提交

- 提交信息：`fix(r19-f03): delete forward conn limiter on node (R19-X-03)`
- 提交后 `git status` 干净（除报告外无其他未跟踪文件）；`git log --oneline -2` 见提交记录。
— R19-FIX-F03-REPORT END · 自证：本行之前正文字节数 13494 · END
