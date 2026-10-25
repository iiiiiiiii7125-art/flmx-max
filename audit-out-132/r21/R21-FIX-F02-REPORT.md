# R21-F02 修复报告（F-02：离线更新的孤儿在重连重推时不被清理）

基线：main @ 88f10db（分支 `fix-r21-f02`，工作区 `~/workspace/.fixr21-f02`）。
依据：`audit-out-132/r21/R21-FIX-PLAN.md` F-02、`R21-X-ledger.md` R21-X-02、
`R21-X-verdicts.md` V-03。修法：选 ①（重连重推带旧快照等效清理——按
“当前记录已无限流”判定失势并补删，非字面传旧快照，见 §2 判定说明）。
只修、不合 main、不打 tag、不发版。窗口：2026-10-25T12:29:46Z 起，
锁 R21-F02 持有 12:30:16Z–放锁（见 `R21-FIX-F02-tm.log`）。

## §0 结论

- 修复完成并提交（提交号见 §7）。R21-X-02 关闭的沙箱 + 真机证据齐备。
- 沙箱：新测试 5/5 PASS（先红后绿，见 §3）；handler 全包 -v：1021 PASS /
  0 FAIL / 4 SKIP，`ok 457.545s`；`go build ./...`、`go vet ./...`、
  前端 `tsc --noEmit` 全绿；其余包在首轮 `go test ./...` 中全部 ok。
- 真机：同一离线序列跑 3 遍有效腿（转发 2/3/4），重连重推后 journal
  每次恰 1 条面板下发的 `DeleteCLimiters rule_conn_limit_<id>`，
  gost 孤儿每次清零，自愈行 0 提及；最终全绿一遍 failures=0。
- 无 BC。无断言更新（0 条）；未动任何有意设计护栏；未删/放宽任何测试。

## §1 自验现状（不继承旧结论）

接手时核查（`git status` / `git diff` / 通读测试与被调函数）：

- 分支 `fix-r21-f02` 已在 main @ 88f10db，未提交改动 3 件，与任务书一致：
  `M control_plane.go`（注释 4 行）、`M upgrade.go`（首轮 + 转发重试
  两处补删 + helper）、`?? r21_fix_f02_test.go`（4 个测试）。
- 代码意图复核通过：`deleteRedeployStaleRuleConnLimiter` 仅在重推成功后
  调用（先推服务后删 limiter）；以 `forwardRuleConnLimiterName(forward)
  == ""`（当前已无限流）判定失势；取名只按 ID
  （`forwardRuleConnLimiterNameByID`，control_plane.go），agent 侧删不
  存在者为 noop，与 R20-X-01 ② 同口径；`user_conn_limit_*` 永不删。
- 机制复核通过：`syncForwardServicesWithWarnings` 的失势清理确需
  oldForward（control_plane.go:505–513），而 `redeployLocalNodeRuntime`
  调 `syncForwardServices`（无快照版）故 oldForward 恒 nil——“带旧快照”
  的字面形式在此无快照可传；“当前无限流 ⇒ 失势”成立的前提亦复核通过：
  服务 `climiter` 字段只由当前记录的限流值赋值
  （`buildForwardServiceConfigs` 内，`service["climiter"]`），故当前
  无限流时节点上同名 limiter 若仍存在必为孤儿。
- 一处引用过期（本 lane 修正，见 §2）：helper 注释称
  “control_plane.go:2072”，实测 `service["climiter"]` 在 2074 行；
  已改为引函数名、不再钉行号。
- 一处真缺口（本 lane 补上，见 §2）：隧道重试分支无清理。

## §2 修复内容

### 2.1 已有实现（接手时在位，本 lane 自验采纳）

`upgrade.go` `redeployLocalNodeRuntime`：逐转发 `syncForwardServices`
成功后调 `deleteRedeployStaleRuleConnLimiter(forward, nodeID)`；
`retryFailedRedeploys` 转发重试成功后同口径补删。
`control_plane.go:505–513` 注释：把“留待下次成功同步”改为“重连重推
`redeployLocalNodeRuntime` 在重推成功后按 ID 补删（R21-X-02）”——
“留待下次成功同步”口径自此为真；R20-FIX-F01-REPORT.md §6.2 的同句
是历史报告（记录当时口径），不改。

### 2.2 本 lane 新增：隧道重试补删（缺口 + 修复）

- 缺口：`retryFailedRedeploys` 里隧道失败后，其下转发首轮既不同步
  （`redeployLocalNodeRuntime` 的 forward 循环 `continue` 跳过）、
  亦不进 `failedForwards`；隧道重试成功时转发在
  `redeployTunnelAndForwards`（mutations.go:2807）内重推成功，但该
  路径不带失势清理——失败-重试成功的隧道下转发仍留孤儿。
- 修复：隧道重试成功分支调新增 helper
  `deleteRedeployStaleRuleConnLimitersForTunnel(tunnelID, nodeID)`
  （upgrade.go），逐条按“当前已无限流”补删；仅清本节点上有入口端口
  的转发（多节点转发由各节点自己的重连重推各清各的）；
  pending-delete 行跳过（删除漏斗接管）；列举/取端口失败只记日志、
  不挡重推回执（与 `deleteForwardConnLimiterOnNode` 同口径）。
- 判定说明（写给 R22 重审）：本修复选 ①，但不用“带旧快照”的字面形式。
  重推时 DB 已是离线更新后的新态，更新前快照在重连时不存在、无法凭空
  恢复；唯一不依赖旧快照的判定就是“当前无限流 ⇒ 同名 limiter 失势”
  （§1 前提已复核）。从未带过限流的转发在重推时同样会收到一次
  DeleteCLimiters（noop），该取舍由
  `TestR21F02RedeployNeverLimitedForwardSendsNoopDelete` 钉死。

### 2.3  diff 规模

- `control_plane.go`：+3/−1（仅注释）。
- `upgrade.go`：+55/−0（首轮补删调用 1 处、隧道重试 helper + 调用、
  转发重试补删调用、helper 注释行号修正）。
- `r21_fix_f02_test.go`：新建，5 个测试（§3）。

## §3 沙箱回归（先红后绿）

Go 工具链 `~/go/bin/go`；`TMPDIR` 指大盘 `~/workspace/.fixr21-f02-tmp`。

### 3.1 红（基线 88f10db，测试在位、修复 stash）

`go test ./internal/http/handler/ -run 'TestR21F02' -count=1 -v`：

| 测试 | 基线 |
|---|---|
| `...RedeployAfterOfflineUpdateDeletesStaleLimiter`（主回归） | FAIL（`got 0 DeleteCLimiters`，即立案机理） |
| `...RedeployKeepsLimitsSendsNoDeleteCLimiters`（反向守卫） | PASS（基线亦过，符合预期：断言的是“不删”） |
| `...RedeployRetrySuccessDeletesStaleLimiter`（转发重试腿） | FAIL |
| `...RedeployNeverLimitedForwardSendsNoopDelete`（口径钉死） | FAIL |
| `...RedeployTunnelRetrySuccessDeletesStaleLimiter`（§2.2 新腿，修复前） | FAIL（`expected exactly 1 … got 0: []`，单独先红） |

### 3.2 绿（修复后）

同命令 5/5 PASS（`ok 4.092s`，各测试 0.33–1.70s；全量 -v 日志内
同名 5 项亦 PASS，见 §3.3）。

### 3.3 全量回归

- `go build ./...`：BUILD-OK。`go vet ./...`：无 findings（仅一条
  GOPATH/GOROOT 同目录的工具链警告，与代码无关）。
- 首轮 `go test ./... -count=1`：除 handler 外全部 ok（middleware、
  kernel、logredact、metrics、monitoring、netguard、runtime/nftables、
  security、store/repo 176.165s、ws、tests/contract 54.780s）；
  handler 包报 FAIL 于 600.361s——实为 go test 默认 10 分钟超时被
  触发，非测试断言失败（panic 栈仅为超时时存活的 goroutine，
  含 notify worker 的 select 等待）。
- handler 包单独重跑 `-timeout 20m`：`ok 475.526s`，exit 0。
- handler 包 `-timeout 20m -v`（归档 `R21-FIX-F02-fulltest.log`，
  976,854 字节）：**1021 PASS / 0 FAIL / 4 SKIP**，`ok 457.545s`。
  同族复核全绿：R19-F03 3/3、R20-F01 4/4、R21-F02 5/5、
  `TestStartNodeOnlineRedeploy*` / `TestNextNodeOnlineRedeployFireAt*`
  5 项、批量重推 nftables 2 项；`onNodeOnline` 唯一既有直调点
  （peer_share_runtime_lifecycle）亦 PASS。
- 前端 `tsc --noEmit`（`node_modules/.bin/tsc`，node v24.22.1）：
  exit 0。前端文件未动（0 行），仅完整性确认。
- 已知例外：任务书所列 health 沙箱 fake-IP 环境红——本轮全量中未
  出现可归因于它的 FAIL（0 FAIL）；首轮 handler 超时已如上归因，
  不计产品失败。
- gofmt：`upgrade.go` 与测试文件干净；`control_plane.go` 唯一
  gofmt 差异在 167 行注释空格（`//（R5-B5 D15）`），基线已存在、
  不在本 diff 内，未动。

## §4 真机腿（新机 ip-172-26-4-158，tailnet 100.123.93.34）

锁：R21-F01 持有至 12:30:16Z；本 lane `--wait` 排队、未抢占，
12:30:16Z 拿到 R21-F02，每次重启/部署前均 `check` 通过；做完放锁，
状态 free。geo 排查并行用机，全程无冲突。

### 4.1 部署（修复版 paneld）

- 构建：沙箱 go1.25.1，`CGO_ENABLED=0 -trimpath`，产物
  43,856,906 字节，md5 `00af1291b498fb8b2b8d36f41604810e`；
  二进制含 `deleteRedeployStaleRuleConnLimiter` 符号（strings 6 命中）。
- 部署 12:31:49–12:32:05Z（`R21-FIX-F02-deploy.sh`，仅换 backend）：
  镜像 `flmx-backend:r21-f02`（sha256:e9f06e66…），容器内与运行中
  paneld md5 均 `00af1291…`，backend healthy，`GET / 6366 http=200`，
  agent active，gost.json md5 不变。
- 预态（与任务书一处出入）：运行中 paneld 实测为
  `69d166cc2d0923ef72f974912767bfa8`（7d93792 构建，`flmx-backend:new1`），
  并非任务书所称“88f10db 已恢复”。88f10db 与 7d93792 的差仅为空闲
  超时「不限」功能，与本腿对象（转发 climiter 重推）无关，腿结论
  不受影响；恢复时按实际预态（new1）对账，见 §5。
- 预态其余：agent `27787cbf52e9390685e75399ff9da821`（F-04 构建），
  gost services=0、climiters=[]、forward 0、tunnel 0（空库）。
  其自愈只在 Start/SIGHUP 时跑（`program.go healPersistedConfig`，
  UpdateService 路径不触发），故不能掩盖本腿；腿内另 grep 自愈行归因。

### 4.2 腿脚本与序列

`R21-FIX-F02-e2e.py`（机上 `/root/r21-f02/` 运行，凭据经 stdin，
不入日志）：建 type-1 隧道（入口 w10-test id 1）→ 建带限流转发
（`203.0.113.9:9001`，maxConn=5）→ 停 agent → 离线更新去限流 →
重启 agent → 轮询 journal 至多 150s（含 30s onNodeOnline 冷却排队）
→ 删转发 → 删隧道。断言与 R21 立案 Leg B 同序列、预期反转。

### 4.3 结果（3 遍有效腿，转发 2/3/4）

| 遍 | 转发 | B 离线更新 | C 重连重推 journal | C gost | D 恢复 |
|---|---|---|---|---|---|
| run2 12:33:38Z | 2 | code 0、警告“不在线”、gost md5 不变 `3264932f…`、limiter 仍在且被引用 | `DeleteCLimiters rule_conn_limit_2` ×1，12:33:44，requestId `1_1792931624302706104` | limiter 消失、服务 `2_1_0_tcp/udp` 仍在、无引用；自愈行 0 提及 | 删转发 code 0；D 后段因脚本 bug 崩溃（见 4.4） |
| run3 12:34:08Z | 3 | 同上，md5 `17521640…` 不变 | ×1，12:34:14，requestId `1_1792931654301595444` | 同上 | 删转发/删隧道 code 0；隧道计数检查 FAIL（见 4.4） |
| run4 12:34:48Z | 4 | 同上，md5 `29ffd802…` 不变 | ×1，12:34:54，requestId `1_1792931694146492291` | 同上 | **failures=0，全绿** |

每遍另查：`user_conn_limit_*` 的 DeleteCLimiters 行在 1 小时窗口内
为 0。最终日志 `R21-FIX-F02-e2e.log` 为 run4 全绿一遍。

### 4.4 两起脚本事故（均非产品失败）

1. run1 12:32:12Z ABORT：脚本硬编码 tunnel 1，新机空库无此隧道，
   建转发报 `code=-1 隧道不存在`。修正：腿内新建 type-1 隧道、
   基线改相对计数。未建任何转发，无残留。
2. run2 D 段崩溃：删光转发后 gost.json 里 `services` 为 null 而非
   `[]`，`data.get("services", [])` 取到 null 致 TypeError。修正：
   `or []` 兜底。此为探针 bug，C 腿结论不受影响。
3. run3 D 段 `tunnel count back to pre` FAIL：run2 残留的隧道被
   run3 复用，删后计数 0 ≠ 预态 1。直接探针证实当时隧道 []、转发
   []——删除本身成功，是脚本相对基线算错。修正：记
   `tunnel_created_here`，复用时预期为 pre−1。run4 全绿证实。

三遍的 C 腿（立案反转点）全部一次通过，未重跑、无挑拣；run2/run3
的日志被 tee 覆盖，仅 run4 落盘，run2/run3 结果以本报告逐字记录
的 live 输出为准（时间戳与 requestId 如上）。

## §5 恢复对账（pristine）

恢复 12:35:09–12:35:16Z：compose backend 标签换回 `new1`，容器
healthy，运行中 paneld md5 `69d166cc2d0923ef72f974912767bfa8`
（= §4.1 实际预态），`GET / 6366 http=200`，agent active。
恢复后只读复核：节点 1 w10-test status 1；tunnel 0、forward 0；
gost services/climiters 均为 null（语义为空，与预态 0/0 一致；
md5 `23ca5d88…`，与预态 `8a80554c…` 的差仅为空数组与 null 的
序列化形态）。三容器 Up healthy。锁已释放，状态 free。
机上留存 `/root/r21-f02/`（stage + 脚本 + 日志外）未清——为可复核
的部署痕迹，不影响面板运行；是否清理请主控定。

## §6 残余风险与未验证

- 永久重推失败：若重推本身持续失败（`retryFailedRedeploys` 返回
  false），孤儿留待下次成功重连；与 R20-X-01 在线语义一致，
  F-04 启动/SIGHUP 自愈为后备。本腿未造永久失败场景。
- realm/nft 转发：重推清理在 `syncForwardServices` 成功后无条件
  发 DeleteCLimiters；realm/nft 转发本无 rule climiter，agent 侧
  删不存在者为 noop（与从未带限流者同口径），未另加类型门——
  多一噪音命令、无行为风险；若 R22 要求零噪音可再加门。
- 多节点：本 lane 单机单节点；多节点转发各节点各清的分支由
  `deleteRedeployStaleRuleConnLimitersForTunnel` 的端口过滤覆盖
  代码面，未跑真机多节点腿（无第二节点）。
- 隧道重试腿（§2.2）仅沙箱覆盖，未单独跑真机隧道失败-重试腿
  （真机上造隧道级失败需破坏隧道运行时，风险大于收益；首轮 +
  转发重试两条真机可达路径已覆盖）。

## §7 交付清单

- 分支 `fix-r21-f02`（基线 88f10db）+ 本提交；提交号见主控汇报。
- 产品：`go-backend/internal/http/handler/control_plane.go`、
  `upgrade.go`；测试：`r21_fix_f02_test.go`（5 项）。
- 报告与脚本：`audit-out-132/r21/R21-FIX-F02-REPORT.md`（本文件）、
  `R21-FIX-F02-e2e.py`、`R21-FIX-F02-deploy.sh`。
- 日志（`git add -f`）：`R21-FIX-F02-tm.log`、
  `R21-FIX-F02-deploy.log`、`R21-FIX-F02-e2e.log`（run4 全绿）、
  `R21-FIX-F02-restore.log`、`R21-FIX-F02-fulltest.log`
  （handler -v，1021 PASS / 0 FAIL）。
- 未合 main、未打 tag、未发版。下一步：R22 重审本修复。
— R21-FIX-F02-REPORT END · 自证：本行之前正文字节数 14267 · END
