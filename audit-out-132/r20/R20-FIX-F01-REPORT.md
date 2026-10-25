# R20-FIX-F01 修复报告：更新路径孤儿 climiter（R20-X-01）

- 测试机窗口（UTC）：2026-10-25T06:58:10Z acquire → 2026-10-25T07:07:13Z release（lane `fix-r20-f01`；前序 F03 06:28:25Z–06:58:10Z、R20-F04 06:52:00Z 起为同一把锁的交接序列，本 lane 按 `--wait` 等待、无覆盖）
- 基线：main @ b8fb567（`b8fb5678e0859da03e8fc8ecfcf50ce675480296`，R19 修复已合入并推 origin）
- 分支：`fix-r20-f01`（worktree `~/workspace/.fixr20-f01`，未合 main、未打 tag、未发版、未改版本字面量）
- 依据：`R20-FIX-PLAN.md`（F-01）、`R20-X-ledger.md`（R20-X-01）、`R20-X-verdicts.md`（V-02）
- 结论：**修复已落地，沙箱先红后绿 + 真机端到端双闭合**。更新去限流后失势的 `rule_conn_limit_<id>` 在服务推送成功后删除；更新-删除序列后删除兜底按 ID 名再发一次；直接删回归与 L2 转发 33 同款一致。

## §1 机理复核（与 V-02 pin 死的机理逐项对过，无漂移）

- 更新路径无 DeleteCLimiters 生产方：基线全仓生产方仅 `control_plane.go:2257` 的删除漏斗（`deleteForwardConnLimiterOnNode`）；`syncForwardServicesWithWarnings` 只 ensure 新配置（`ensureConnLimiterOnNode` → Add/UpdateCLimiters），失势旧名无人删。
- 删除按当前记录值取名：`forwardRuleConnLimiterName` 在 `MaxConn<=0 且 IPMaxConn<=0` 时返 `""`，调用方跳过；更新清零后删除再也算不出旧名。名本身只由 ID 决定（`rule_conn_limit_<id>`），值只决定“是否取名”——这正是兜底可按 ID 取名的前提。
- 唯一能清限流的 DB 写入是 `forwardUpdate`（`mutations.go:3566` 的 `UpdateForward` 取请求值）；其余 `UpdateForward` 调用方（`kernel_external.go:411`、`kernel_protect.go:170`、`kernel_relay.go:293`、`tunnel_delete_resolution.go:641`）均原样透传现值，`ChangeForwardTunnelAndPorts` 不碰限流列。故更新路径只需在持有更新前快照的三处接线，其余重推路径无快照、无清理。
- agent 侧接收端完好：`deleteConnLimiter`（`go-gost/x/socket/limiter.go:210`）删不存在者为 noop（注册表未注册即跳过、`OnUpdate` 遍历无匹配即原样保留），重复发送安全；agent 无 ListCLimiters 命令，故兜底不走“现存枚举”（无此协议面），走确定性 ID 取名。
- `user_conn_limit_*` 为同用户共享：新旧名均不含它，本次两件改动均不触碰。

## §2 改动（只改 panel 后端；agent 不动，frontend/文档/版本不动）

### 2.1 `go-backend/internal/http/handler/control_plane.go`

- 新增 `forwardRuleConnLimiterNameByID`：确定性名，只由转发 ID 决定，与当前限流值无关（R20-X-01 ② 的取名底座）。
- `deleteForwardConnLimiterOnNode` 改按 ID 取名（② 删除兜底）：不再按当前记录值 gating；`forward == nil` 时返回，ID<=0 时名空返回。两删除漏斗（`controlForwardServices` 的 DeleteService 分支、`deleteForwardServicesOnNodeBatch`）经同一 helper 收敛，行为同改。无限制转发删除时多发一次 DeleteCLimiters，agent 侧为 noop。
- 新增 `deleteStaleForwardConnLimiterOnNode(old, new, nodeID)`（① 的判定）：旧带 rule 限流且新已无时才删；新仍带限流时不删（新 limiter 由 ensure 原地 Add/Update）。
- `syncForwardServicesWithWarnings` 签名加可选变参 `oldForwards ...*forwardRecord`（既有调用方零改动可编译）：循环内任一节点非离线失败即整函数返回错误、调用方回滚 DB，故失势 limiter 不在循环内删；改为收集本轮推送成功的入口节点（去重，离线跳过的不进集），循环全部成功后逐节点删（先推服务后删 limiter）。nft/realm 分支在循环前提前返回，不到清理。

### 2.2 接线点（三处持有更新前快照）

- `mutations.go:3602`（`forwardUpdate`）：传更新前快照 `forward`。已核对：该记录自 `resolveForwardAccess` 取行后至同步前无任何字段赋值，确为更新前值。
- `mutations.go:4469`（`forwardBatchChangeTunnel`）：同上，传 `forward`。
- `tunnel_delete_resolution.go:671`：传 `plan.forward`（本路径只改 RemoteAddr、限流原样透传，失势判定恒为否；为将来口径变化留口子）。
- 刻意不接：`deleteForwardServicesOnNode`（`control_plane.go:567`，转发存活场景——更新回退清理、自占端口重绑、realm 引擎切换）仍不删 limiter，与 R19-F03 §2.4 同口径；创建/限速重同步/relay 修复等无快照的重推路径传空，不触发清理。

## §3 先红后绿（沙箱，`newCleanupAgent` 真实 WS 传输 + 模拟节点即时 ACK）

新测试 `go-backend/internal/http/handler/r20_fix_f01_test.go`（4 测），红日志 `R20-FIX-F01-red-f01-update.log` / `R20-FIX-F01-red-f02-delete.log`，绿日志 `R20-FIX-F01-green-r19r20.log`：

| 测试 | 红 | 绿 |
|---|---|---|
| ② `TestR20F01DeleteAfterLimitsClearedSendsDeleteCLimiters`（建 MaxConn=7 → 清零 → batch 删） | FAIL：`expected exactly 1 DeleteCLimiters command, got 0: []` | PASS（恰 1 条，同名） |
| ① `TestR20F01UpdateDropsLimitsDeletesStaleLimiter`（清零后 sync 传旧快照） | 编译红：`too many arguments in call to syncForwardServicesWithWarnings`（3 处调用点同错，基线签名无旧记录参数） | PASS（恰 1 条，UpdateService 之后） |
| ① 反向守卫 `TestR20F01UpdateKeepsLimitsSendsNoDeleteCLimiters`（新旧均带限流） | 同上编译红 | PASS（0 条 DeleteCLimiters；AddCLimiters 同名在位；未触 `user_conn_limit_*`） |
| 全序列 `TestR20F01UpdateThenDeleteLeavesNoOrphan`（更新去限流 → 删转发） | 同上编译红 | PASS（恰 2 条，各在 UpdateService / DeleteService 之后） |

R19 旧测试同步绿 3/3（`TestR19F03*`）。

### 断言更新登记（1 项，裁定点名驱动）

- `r19_fix_f03_test.go`：`TestR19F03DeleteForwardWithoutLimitsSendsNoDeleteCLimiters` 改名 `TestR19F03DeleteForwardWithoutLimitsStillDeletesRuleLimiterByID`，断言由“两漏斗均 0 条”改为“两漏斗各恰 1 条同名”。驱动点：FIX-PLAN F-01 ② 明文“删除路径兜底：不得只按当前记录值取名”——旧断言钉的正是被推翻的旧口径（无限制即不发），与修复互斥，非修复不得改。除此之外无任何断言改动。

## §4 回归

- `go build ./...`：go-backend、go-gost 均 exit 0；`go vet ./...`（go-backend）exit 0；前端 `tsc --noEmit` exit 0（空日志 `R20-FIX-F01-tsc.log`，本次无前端改动，仅确认）。
- `go test -timeout 30m ./...`（go-backend，`R20-FIX-F01-green-gotest.log`）：16 包 ok，唯一 FAIL 为已知沙箱 fake-IP DNS 环境红 `TestValidateMonitorTCPTarget/公网域名放行`（`example.com:443` 被判内网/保留地址），与基线同签名（基线日志同款 1 行 `--- FAIL`，`grep -c "^--- FAIL"` 基线与修后均为 1）。handler 包 820.724s（默认 10m 超时不够用，沿用 R19-F03 同款 `-timeout 30m`；非产品失败）。
- `go-gost/x`：27 包 ok，exit 0（`R20-FIX-F01-green-gostx.log`）；`go-gost`：`tests/lifecycle` ok，exit 0（`R20-FIX-F01-green-gost.log`）。
- 基线全量首跑曾被污染（并行跑的多个 `go test` 与中途一次 `go clean -cache` 致 `internal/ws` / `tests/contract` 报 `could not import ... open $WORK/.../_pkg_.a: no such file or directory`、handler 包 10m 超时掐断）：属本 lane 并行误操作，非产品失败；已 kill 后顺序重跑（`-timeout 30m`、无并发 go 进程），污染项全部消失，只剩上述 DNS 一格。教训：全量测试期间不并发跑 go 命令、不清缓存。
- `gofmt -l`：`control_plane.go` 仍有一处既存格式差异（`:167` 注释 `//（R5-B5…` 的空格，基线即有，与本次改动无关，未动）；本次改动行本身无格式问题。

## §5 真机端到端（测试机，面板 1.3.5 + pristine agent aff338e3）

部署：本地构建 paneld（`7902009e6cdc737792212039cce70259`，44MB），镜像 `flmx-backend:r20-f01`（`FROM flmx-backend:r1gate-1.3.5` + `COPY paneld`），容器内 `/app/paneld` md5 核对一致，backend healthy、GET / 6366 → 200，gost 仍 pristine `d48f09e1`。agent 二进制与单元本次未动。部署日志 `R20-FIX-F01-deploy.log`。过程插曲：首版验证步 `docker run --rm <image> md5sum /app/paneld` 未覆写 ENTRYPOINT，致 paneld 常驻启动、SSH 断开；杂散容器 `musing_kepler` 已 kill 清除（无卷挂载，未触真实 DB），验证步改为 `--entrypoint md5sum` 后重跑通过。

e2e 脚本 `R20-FIX-F01-e2e.py`（tunnel 1、远端 `203.0.113.9:9001`、maxConn=5，L2 转发 32/33 同款），跑两遍：

- 第一遍（转发 36/37，`R20-FIX-F01-e2e.log`）：gost 四腿全对——建后 `rule_conn_limit_36` 在位且被服务引用；更新去限流后该名消失、服务仍在、无引用；删除后 gost 回 pristine、forward 数回 6；直接删对照 37 同款。但 journal 探针 3 项 0 命中：根因是 pristine agent 单元 `StandardOutput=null`，journal 里根本无 agent 输出（非修复问题）。
- 第二遍（转发 38/39，`R20-FIX-F01-e2e-2.log`，先装 L1/L2 同款 journal 单元 `05449a73…` 并重启 agent，gost 仍 pristine）：**17 项断言 0 失败**。journal 顺序证据（`R20-FIX-F01-journal.log`，20 行）：
  - 建：`AddCLimiters {"limits":["$ 5"],"name":"rule_conn_limit_38"}` → `UpdateService`（服务带 `"climiter":"rule_conn_limit_38"`）；
  - 改：`UpdateService`（服务已无 `climiter` 键）→ `DeleteCLimiters {"limiter":"rule_conn_limit_38"}`（先推服务后删 limiter）；
  - 删：`DeleteService ["38_1_0_tcp","38_1_0_udp","38_1_0"]` → `DeleteCLimiters {"limiter":"rule_conn_limit_38"}`；
  - 对照 39：`AddCLimiters` → `UpdateService` → `DeleteService` → `DeleteCLimiters {"limiter":"rule_conn_limit_39"}`，各恰 1 次。
- 存量孤儿 `rule_conn_limit_2 ["$ 1"]` / `rule_conn_limit_12 ["$ 7"]`（F-04 范围）全程在位、值不变，本次未动。

还原（`R20-FIX-F01-restore.log` + `R20-FIX-F01-counts.log`）：pristine agent 单元恢复（`a12afd9c…`，StandardOutput=null），agent 重启 active；compose 切回 `flmx-backend:r1gate-1.3.5`，backend healthy，容器内 paneld 为 pristine 1.3.5 构建（`6c67f9f7670b69bd1d78061688f2da17`）；自建镜像已删（`773724c8…`）；agent `aff338e3`、gost `d48f09e1`（328 行、12 服务、climiters 仅存量两项、refs 空）；计数 node=1 / tunnel=4 / forward=6（status 全 1）/ inbound=578 / subscription=18 全 PASS；GET / 6366 → 200；磁盘 2.8G。计数脚本首版 subscription 误取 `data.list` 得 0——该接口回 `{"items": [...]}`，已修正重跑 PASS（脚本探针问题，非漂移）。

## §6 未验证项、残余风险与后续项（不属本次修复）

1. 多入口节点/多端口转发的失势清理去重逻辑只在沙箱单节点单端口覆盖，真机多节点形态未验证。
2. 节点离线时更新去限流：失势 limiter 本轮不删（服务未推），留待下次成功同步或删除兜底——该分支未在真机复现（需节点下线）。
3. 切隧道 + 去限流组合路径（旧节点 batch 删已清 limiter、同步后再发一次 noop）只经代码推演，未真机复现。
4. realm/nft 隧道：同步在循环前提前返回、不走 climiter 清理，属设计内（无 gost climiter 可清），未单独立项验证。
5. `forwardForceDelete` 的 gost 路径不推 DeleteService（删库优先、残留进审计日志提示人工清理），属既有口径，不属本次两漏斗；其 limiter 兜底未接。
6. 存量孤儿 `rule_conn_limit_2` / `rule_conn_limit_12` 未清理，属 F-04。
7. 残余风险（小）：DeleteCLimiters 本身瞬时失败（如节点抖动）时只记日志、无重试——与既有 best-effort 清理同口径；离线节点受 S-04 阻断删库、重连清理重试，不受此影响。

## §7 交付清单

- 分支 `fix-r20-f01`（自 main @ b8fb567），提交 1 个（见 §7.1），未合 main、未推远端、未打 tag、未发版。
- 改动文件（`git add` 逐文件明确加入，禁用 `git add -A`；`*.log` 以 `git add -f` 逐文件显式入库）：
  - `go-backend/internal/http/handler/control_plane.go`（② 按 ID 取名 + ① 判定 helper + sync 变参快照与循环后清理）
  - `go-backend/internal/http/handler/mutations.go`（forwardUpdate、批量换隧道传旧快照）
  - `go-backend/internal/http/handler/tunnel_delete_resolution.go`（隧道删除决议传旧快照）
  - `go-backend/internal/http/handler/r19_fix_f03_test.go`（§3 登记的 1 项断言更新）
  - `go-backend/internal/http/handler/r20_fix_f01_test.go`（新，先红后绿 4 测）
  - `audit-out-132/r20/R20-FIX-F01-REPORT.md`（本报告）
  - `audit-out-132/r20/R20-FIX-F01-e2e.py` / `R20-FIX-F01-counts.py` / `R20-FIX-F01-deploy.sh` / `R20-FIX-F01-recon.sh` / `R20-FIX-F01-restore.sh`（新，真机脚本）
  - `audit-out-132/r20/R20-FIX-F01-red-f01-update.log` / `R20-FIX-F01-red-f02-delete.log` / `R20-FIX-F01-green-r19r20.log` / `R20-FIX-F01-green-gotest.log` / `R20-FIX-F01-green-gostx.log` / `R20-FIX-F01-green-gost.log` / `R20-FIX-F01-tsc.log` / `R20-FIX-F01-recon.log` / `R20-FIX-F01-deploy.log` / `R20-FIX-F01-e2e.log` / `R20-FIX-F01-e2e-2.log` / `R20-FIX-F01-journal-unit.log` / `R20-FIX-F01-journal.log` / `R20-FIX-F01-restore.log` / `R20-FIX-F01-counts.log`（新，红绿与真机原始日志）
- 未入库：`stage/`（本地构建的 paneld 二进制）、`.gocache-build/`（隔离构建缓存），均属本 lane 工作区暂存，不随分支走。

### 7.1 提交

- 提交信息：`fix(r20-f01): drop orphaned rule conn limiter on forward update (R20-X-01)`
- 提交后 `git status` 干净（除上述未入库暂存外无其他未跟踪文件）。
— R20-FIX-F01-REPORT END · 自证：本行之前正文字节数 13949 · END
