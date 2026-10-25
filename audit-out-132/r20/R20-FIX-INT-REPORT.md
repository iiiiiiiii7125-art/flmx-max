# R20 修复集成报告（FIX-INT-REPORT）

基线：main @ b8fb567（R19 修复 fix-r19 已合入并推 origin）。集成树：`fix-r20`，独立 worktree `~/workspace/.fixr20`。未合 main、未打 tag、未发版。

## 一、合入（四路零冲突，按序合入）

| 顺序 | 分支 @ 提交 | 内容 |
|---|---|---|
| 1 | `fix-r20-f02` @ `3e68339` | F-02（P2）：`SaveNodeInbound` 白名单补 `conn_idle_seconds` + 全仓白名单对账 + `syncCompanion` 第二道闸 |
| 2 | `fix-r20-f01` @ `b85402c` | F-01（P3）：更新路径补删失势 climiter + 删除按 ID 取名兜底 |
| 3 | `fix-r20-f03` @ `51cdaf3` | F-03（P3）：空闲超时文案改称 Xray 窗口化 N–2N 实测语义 |
| 4 | `fix-r20-f04` @ `2d779bf` | F-04（P3）：启动自愈剪无引用 limiter |

合入提交：`23fee24`（F-02）、`13114bf`（F-01）、`c7a5953`（F-03）、`7452e74`（F-04，集成 tip）。合并过程零冲突。集成 diff（相对 b8fb567）：100 文件 +20143/−19；产品文件 15 个（见 §二），其余为四份修复报告 + 探针/回归日志。

## 二、逐路验盘（分支 tip、报告字节数、结论均已逐项核对）

### F-02（P2，优先）——入站编辑丢 connIdleSeconds
- 分支 `fix-r20-f02` @ `3e68339`；报告 `R20-FIX-F02-REPORT.md` 20,134 字节（正文 20,054），尾行 END 在位。
- 白名单补 `"conn_idle_seconds"`（`repository_kernel.go`）；全仓同型复核：68 处 `Updates`（59 内联 map + 9 变量 map）+ 11 处 `UpdateColumns(map...)` 逐一对账，除本项外无其他漏列；逐项核对表在报告 §四。
- 附带第二道闸：`syncCompanion` dirty-check 加 `ConnIdleSeconds` 比对（`subscription_metering.go` +6）——origin 仅改超时会直接 return false，伴生行永久持旧值。
- 先红后绿：红 `list connIdleSeconds = 5, want 10`（与真机同签名）→ 绿两用例 PASS（建 5 → 改 10 → list 显 10 → 渲染 `policy.levels["1"].connIdle == 10`，另覆显式 0 清空）。handler 包 `-timeout 30m` 重跑 ok 730.592s；DNS 红已在 pristine worktree 核对同签名。

### F-01（P3）——更新路径孤儿 climiter
- 分支 `fix-r20-f01` @ `b85402c`；报告 `R20-FIX-F01-REPORT.md` 14,029 字节（正文 13,949），尾行 END 在位。
- 更新路径：`syncForwardServicesWithWarnings` 加可选变参旧记录，三处持有更新前快照的调用点传入（`forwardUpdate`、`forwardBatchChangeTunnel`、隧道删除决议）；失势 `rule_conn_limit_<id>` 在全部入口节点服务推送成功后删除（先推服务后删 limiter；循环内不删；离线节点跳过）。
- 删除兜底：新增 `forwardRuleConnLimiterNameByID`，删除按 ID 取名，不再按当前记录值 gating；两删除漏斗经同一 helper 收敛。`user_conn_limit_*` 全程未碰。附带核实：唯一能清限流的 DB 写入是 `forwardUpdate`。
- 先红后绿：新测试 4/4 + R19 旧测试 3/3 = 7/7。真机端到端两遍，第二遍 17 项断言 0 失败；journal 顺序证实 `UpdateService` → `DeleteCLimiters`、`DeleteService` → `DeleteCLimiters` 各恰 1 次；还原后全量对账 PASS（agent `aff338e3`、gost `d48f09e1`、1/4/6/578/18）。
- **断言更新 1 项（FIX-PLAN F-01② 点名驱动，已登记）**：`r19_fix_f03_test.go` 的 `TestR19F03DeleteForwardWithoutLimitsSendsNoDeleteCLimiters` 改名 `…StillDeletesRuleLimiterByID`，断言由“无限制即不发”改为“两漏斗各恰发 1 条同名 DeleteCLimiters”（见集成 diff，注释写明 F-01 推翻旧断言的理由）。除此之外无断言改动。

### F-03（P3）——VLESS 空闲超时 2×connIdle 语义
- 分支 `fix-r20-f03` @ `51cdaf3`；报告 `R20-FIX-F03-REPORT.md` 14,360 字节（正文 14,280），尾行 END 在位。
- 2× 来源已 pin 死（非面板算错）：面板下发值逐字原样（`render_xray.go:45` policy `connIdle = ConnIdleSeconds`，`flmx-idle-<id>` 出站 `userLevel` 逐字；真机渲染核验 `{1..3:{connIdle:5}, 4..5:{connIdle:10}}` 相符）；唯一计时器是 freedom 出站 `CancelAfterInactivity`（xray-core v1.260327.0 `proxy/freedom/freedom.go:169`）；2× 是上游 `ActivityTimer` 窗口化检查粒度（`SetTimeout(N)` 每 N 秒检查一次，仅整格无 `Update()` 才关闭）。一般语义为自末次数据起 (N, 2N]、相位决定，非恒 2N、非“会话起算 2N”。
- **修法 ②（文案改称实测语义），行为零变化，无 BC**：① 不可行——Xray 只暴露 `connIdle` 秒数，粒度在上游实现里，面板不 vendoring Xray core；减半下发会让静默会话提前到 N/2 断（更坏）。UI 表单说明改为“Xray 以该秒数为一格检查空闲——全程无数据约该秒数断开，有数据时自末次数据起 1–2 倍该秒数后断开”；`doc/guide/nodes.md` 同步为 N–2N 窗口语义并删旧“约为设定值的 2 倍”恒定表述；`render_xray.go` 加 R20-F03 锚注防“减半修复”重蹈。
- 真机 7 腿复测与 R20 一致（行为未变，属预期）；新增相位双腿证实窗口模型（burst2 于 9.5s / 5.5s 到达，同在 15.21s / 15.20s 关闭，空闲 5.36s≈N 与 9.34s≈2N；“会话起算 2×”预测的 10.2s 被双腿证伪）。离场 pristine 已对账。

### F-04（P3）——存量孤儿 climiter 清理路径
- 分支 `fix-r20-f04` @ `2d779bf`；报告 `R20-FIX-F04-REPORT.md` 11,652 字节（正文 11,572），尾行 END 在位；证据日志 37 个 + 3 个 gost.json 快照在 `audit-out-132/r20/f04-logs/`。
- **方案 ①（启动自愈加剪无引用 limiter），报主控**：自动清理（随 agent 重启/升级自然发生）、覆盖全部孤儿生产方（含 F-01 未修前的更新路径孤儿）、只动 agent 一处沿用 Heal* 模式、无新端点/路由/前端；面板是唯一生产方且同步会重发 `AddCLimiters`，误剪亦可自愈。方案 ② 需三处新表面且面板算出的名单可能与落盘文件不一致。实现中无结构性障碍：全仓 grep 证实 conn limiter 唯一消费方是服务 `CLimiter` 字段。
- 改动：`dedupe.go` 新增 `PruneUnreferencedCLimiters`（纯函数）+ `HealPersistedOrphanCLimiters`（剪后立即落盘，落盘失败只告警）；`user_conn_limit_*` 永不剪，只动 `CLimiters` 一类，nil 项原样保留；`program.go` 的 `healPersistedConfig` 五类去重之后接剪除一步（引用集按去重后存活服务算）；`control_plane.go` 仅注释，修正“自愈只按名去重”的旧断言。
- 先红后绿：新测试先行编译失败（undefined 符号 6 处）→ 实现后 5/5 PASS，含无引用 `user_conn_limit_7` 必留分支。真机腿：修复版 agent 重启后 gost 328→314 行、`conn_limit` 0 行，60s+ 同步周期后仍干净；Phase B scratch 判定表：剪掉的恰为 `{rule_conn_limit_12, rule_conn_limit_2, manual-climiter}`，保留 `{rule_conn_limit_5, rule_conn_limit_42, user_conn_limit_9, 无引用 user_conn_limit_7}`。锁干净交接给 F-01（06:58:09Z 释放 → 06:58:10Z 接手）。

## 三、BC 注记

**无 BC 项。** F-03 选修法 ②（只改文案，行为零变化）；F-01/F-02/F-04 均为缺陷修复、无既有行为依赖。FIX-PLAN 的 F-03 BC 条件（改计时语义）未触发，发版说明无需单列行为变化。

## 四、集成回归（集成树 fix-r20 @ 7452e74，顺序跑，TMPDIR 指大盘）

日志：`audit-out-132/r20/R20-INT-gotest.log`（19,638 字节，含补验追加行）；回归脚本 `audit-out-132/r20/R20-INT-run.sh`。

| 段 | 结论 |
|---|---|
| go-backend build | exit 0 |
| go-backend vet | exit 0 |
| go-backend `go test ./... -count=1 -timeout 30m` | 16 包 ok（handler 373.506s、repo 110.523s、contract 47.126s、ws 7.132s 等）；唯一 FAIL 为 `internal/health` 的 `TestValidateMonitorTCPTarget/公网域名放行` 一格——已知沙箱 fake-IP DNS，与基线同签名；整包 exit 1 仅由此一格所致 |
| go-gost build/vet/test | exit 0（lifecycle ok 9.164s） |
| go-gost/x build/vet/test | exit 0（27 包，ok / 无测试文件） |
| 前端 | 首跑失败已如实登记并补验（见下）；补验 `pnpm install` exit 0、`pnpm run build`（tsc && vite build）exit 0，PWA precache 65 entries |

**产品失败 0**（health 一格为已知环境红，非产品失败）。

### 前端首跑失败与补验（诚实登记）
首跑 `pnpm run build` 在全新 worktree 内因 `node_modules` 缺失而失败（`sh: 1: tsc: not found`）；且回归脚本的管道写法使该失败被 `tail` 的 exit 0 掩盖，日志首版误记 `frontend exit: 0`。已纠正：补跑 `pnpm install`（Done in 16s，exit 0）+ `pnpm run build`（exit 0），并在日志尾追加补验行；脚本管道掩盖问题已记入本报告，不再沿用该写法。补验证据：构建产物 `dist/assets/inbound-form-modal-*.js` 含新文案“以该秒数为一格检查空闲”，旧文案“约为设定值的 2 倍”在 dist assets 内 0 命中。

### 新测试在集成树上均在位
`connidle_save_test.go`（F-02）、`r20_fix_f01_test.go`（F-01）、`r20_f03_test.go`（F-03）、`r20_fix_f04_test.go`（F-04）均随合入进入集成树，集成全量中 handler/kernel/config 各包均 ok（含上述新测试）。

## 五、未验证清单（留 R21）

任务书点名结转：远端/非 Debian 内核、浏览器 DOM 腿、kill-freeze K2（方法本身可能失效，须重设计触发）、Trojan 腿复测、真实 Gist 端到端、单族 sock_diag 真机形态。
各路结转：F-04 的 SIGHUP 重载路径未单独验（与启动共用同一 `healPersistedConfig` 调用点）；F-04 的 `user_conn_limit_*` 保留分支在真机存量文件上无法验证（由沙箱单元测试 + Phase B scratch 覆盖）；F-01 的多节点/多端口去重、节点离线时失势清理跳过分支、切隧道+去限流组合、`forwardForceDelete` gost 路径（既有口径，未接兜底）、realm/nft 面（设计内无 climiter）；F-02 的真机腿（沙箱链路测试已同构覆盖 list/渲染两端）；F-03 的文档站 VitePress 全站构建未在本地跑（纯 prose 改动，Go 测试已断言关键句）、Trojan 腿未复测（本次不改行为，不受影响）。

## 六、集成 diff 对账

相对 b8fb567：100 文件 +20143/−19。四路报告字节数与 §二逐项相符；产品文件 15 个，分工无重叠（`control_plane.go` 由 F-01 改代码、F-04 改注释，合并零冲突已验证）；R20 审计产物随本次提交归档进库（本次提交只含 fix-r20 集成树文件，不含共享工作树的未跟踪审计文件）。

— R20-FIX-INT-REPORT 正文结束（END 行另起）
— R20-FIX-INT-REPORT END · 自证：本行之前正文字节数 10677 · END
