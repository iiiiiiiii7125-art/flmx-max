# R22-L1 关闭验证 — R21 三项（R21-X-01 / X-02 / X-03）

**测试机窗口：2026-10-25T13:50:01Z → 13:59:00Z｜lane：R22-L1｜基线：main @ b823421（`b82342137cb3157197ad43906c41ccbc6145b64d`，R21 修复 fix-r21 已合入并推 origin）｜只读审计，未改产品代码、未合 main、未打 tag、未发版。**

- 依据：`R21-X-ledger.md` §二/§三、`R21-X-verdicts.md` V-02/V-03/V-04、`R21-FIX-PLAN.md`（F-01…F-03）、`R21-FIX-F01-REPORT.md`（34,730 字节）、`R21-FIX-F02-REPORT.md`、`R21-FIX-F03-REPORT.md`、`R21-FIX-INT-REPORT.md`（§5 前提订正）；任务书 `flmx-audit-catalog-v132.md` 正本；护栏 N-01…N-37 全程有效。
- 读数：只读 worktree `~/workspace/.r22-tree` @ b823421，全程 `git status` 干净（收尾复核为空）；探针跑测在私有快照副本 `~/workspace/.r22-l1-snap/go-backend`，探针测试文件 `r22_l1_probe_test.go`（10 项独立探针），跑后已删（快照内 `grep -c r22` = 0）；长跑 TMPDIR 指大盘 `~/workspace/.r22-l1-tmp`。
- 部署态复核（未重部署，R22-00-baseline 的 new4 即审对象）：运行中 paneld md5 `1ca4572b454bf6450455f057bcc8bb55`、served index md5 `57ab941e7fc6025b52700b2bf36f2241`、GET / 6366 → 200、三容器 healthy（backend/frontend new4、postgres），与基线报告逐字一致；agent active，gost 空态 md5 `23ca5d88ba3bca475f24ee48c47e0fc9`；DB 备份（腿前，python sqlite3 在线备份）`/root/r22-l1/gost.db.pre` md5 `9608f0c7ea8a9b7a9c1aef57465ccd3b`，gost.json pristine 备份同目录。
- 锁：`testmachine-lock.sh`，R22-L5 持有至 13:50:01Z，本 lane `--wait 3600` 排队等 24 轮后抢到（`R22-L1-lock-wait.log`），每次部署/重启前均 `check` 通过，13:59:00Z 释放、状态 free；未覆盖他人锁、未用 break。
- 结论总览：**R21-X-01 / X-02 / X-03 全部 CLOSE（3/3）；新立案 0（产品 P0/P1/P2/P3 均为 0）。** 每项均有独立新证据（沙箱先红后绿 + 真机腿），不得继承 R21 结论——下文逐项给文件行号 + 实测输出。

## 一、R21-X-01（产品 P3）：switchTunnelEngine gost→realm 遗留 rule_conn_limit_<id> 孤儿 — CLOSE

### 1.1 立案口径与修复代码 pin 死（本轮独立复核）

- 立案（R21-X-ledger §三 / V-02）：`realm.go:317` gost→realm 分支调 `deleteForwardServicesOnNode`（`control_plane.go:591–601`）只发 DeleteService，从不发 DeleteCLimiters；切换是唯一一条“服务已删、limiter 未删”的产品路径。
- 修复在位（b823421，`realm.go:363–382`）：gost→realm 分支在 `DeleteService` 成功后补发 `h.deleteForwardConnLimiterOnNode(&forwards[i], nodeID)`（按 ID 兜底，与删转发漏斗同口径）；离线节点删除失败由 `collect` 记 deferred、不清 limiter；清理只落切换点、不进共享 helper（注释钉死设计理由，另两个调用方为删后重建路径）。反向 realm→gost 分支无改动：realm 渲染无 limit 字段、切回时 `syncForwardServices` 经 `ensureConnLimiterOnNode` 重建同名 limiter。
- diff 对账：`git diff 88f10db b823421 --stat`：realm.go +20/−1、control_plane.go +4/−1、upgrade.go +66、mutations.go +20/−1，另新建 `r21_fix_f01_test.go` / `r21_fix_f02_test.go` / `r21_fix_f03_test.go`；与 INT §2 的集成 diff 一致。

### 1.2 沙箱先红后绿（独立探针，与 F-01 lane 测试不同种子/断言角度）

探针 `r22_l1_probe_test.go`（快照副本内，种子：单转发、ipMaxConn=4、端口 22010–22012；F-01 lane 用 maxConn=7 双转发）：

- 绿 1（`R22-L1-probe-green1.log`，10/10 PASS，包耗时 16.128s）：`TestR22L1SwitchGostToRealmDeletesIpLimitedRuleLimiter` 1.57s、`TestR22L1SwitchRealmToGostRecreatesIpLimitedRuleLimiter` 1.40s、`TestR22L1SwitchOfflineNodeSendsNoLimiterCleanup` 0.99s 等 10 项全绿。
- 红（`R22-L1-probe-red.log`，exit=1，6 FAIL / 4 PASS）：快照内临时回退三处修复（realm.go 删 F-01 调用、upgrade.go 删三处 F-02 调用、mutations.go 掏空 F-03 gost 块）后重跑——X-01 两项 FAIL（`expected exactly 1 DeleteCLimiters, got 0: []`），与立案机理逐字一致；4 个 PASS 均为“断言不存在”的守卫腿（离线不删、仍带限流不删、realm 不走 gost 清理、普通删回归），基线态亦过、符合预期。红相文件已恢复（`diff -r` 对照 .r22-tree 除探针文件外零差异，RED 标记计数 0）。
- 绿 2（`R22-L1-probe-green2.log`，10/10 PASS，0 FAIL）：恢复后重跑全绿。
- 离线守卫新证据：节点离线时切换发 0 次 DeleteService、0 次 DeleteCLimiters（服务未删、limiter 仍被引用，不成孤儿）——与 F-01 报告 §2.1 的 deferred 口径一致。

### 1.3 真机腿（新机 ip-172-26-4-158，隧道 2、转发 8）

- 建：隧道 `r22-l1-t1` id 2（engine `''`）；带限流转发 `r22-l1-sw1` id 8（maxConn=5，`203.0.113.9:9001`，inPort 44876）：gost services=2，climiters `['rule_conn_limit_8']`，refs `['rule_conn_limit_8']`（`R22-L1-tm-x01-mkfwd.log`）。
- 正向切 realm（13:52:08Z，`R22-L1-tm-x01-switch-realm.log`）：`tunengine 2 realm` code 0，engine 持久化为 `realm`；**切后 gost services=0、climiters=[]、refs=[]，`rule_conn_limit_8` 已删、无孤儿**，gost md5 回 `23ca5d88…`；realm 配置含 1 个 endpoint（`[::]:44876` → `203.0.113.9:9001`）。journal（`R22-L1-tm-x01-switch-realm-journal.log`）：切换窗口内 `DeleteCLimiters` 匹配 2 行 = **恰 1 条命令**，13:52:09 `requestId=1_1792936329009086664`，`{"limiter":"rule_conn_limit_8"}`；`user_conn_limit` 匹配 0。
- 反向切回 gost（13:52:27Z，`R22-L1-tm-x01-switch-back.log`）：code 0，engine 持久化回 `''`；gost services 回 2，`rule_conn_limit_8` 重建且被引用；realm 配置收敛为 0 endpoint。journal（`…-switch-back-journal.log`）：`AddCLimiters` 恰 1 条命令（`{"limits":["$ 5"],"name":"rule_conn_limit_8"}`，maxConn=5 原值），`DeleteCLimiters` **0 条**——切回无多发、无同型残留。
- **X-01 结论：CLOSE。** 正向孤儿清零（状态面 + 命令级 journal 双证据）、反向重建且零多发；与 R21 立案态（切后 limiter 残留）逐项反转。

## 二、R21-X-02（产品 P3）：离线更新的孤儿在重连重推时不被清理 — CLOSE

### 2.1 修复 lane 选了哪条（先读 F-02 报告确认）

- F-02 报告 §0/§2：**选 ①（重连重推带旧快照等效清理）**，但不用“带旧快照”字面形式——重推时 DB 已是离线更新后的新态、无更新前快照可传；判定为“当前记录已无限流 ⇒ 同名 limiter 失势”，按 ID 补删（agent 侧删不存在者为 noop，与 R20-X-01 ② 同口径）。另补隧道重试缺口（`deleteRedeployStaleRuleConnLimitersForTunnel`）与转发重试补删。
- 本轮按选 ① 验证（真机腿 + 沙箱），另做口径文本逐字核对（见 2.2 末）。

### 2.2 修复代码 pin 死（本轮独立复核）

- `upgrade.go:885–893`（`redeployLocalNodeRuntime` forward 循环）：重推成功后调 `deleteRedeployStaleRuleConnLimiter(forward, nodeID)`；`:937–951` helper：`forwardRuleConnLimiterName(forward) != ""`（当前仍带限流）时直接返回，否则按 ID 删；注释写明“DB 已是新态、无更新前快照可传”“服务 climiter 字段只由当前记录的限流值赋值”。
- `upgrade.go:899–935` 隧道重试补删 helper（仅清本节点上有入口端口的转发、跳过 pending-delete 行）；`:985–993` 隧道重试成功分支与 `:1005–1013` 转发重试成功分支各调一次补删。
- 前提复核：`control_plane.go:2074` `service["climiter"] = runtimeLimiters.ConnLimiter`（仅当非空时赋值）；`control_plane.go:2264–2268` `forwardRuleConnLimiterName` 按当前记录限流值取名（清零返 `""`）、`:2271–2274` 按 ID 取名说明。当前无限流时节点上同名 limiter 若仍存在必为孤儿——前提成立。
- 口径文本核对：`control_plane.go:505–509` 现为“离线节点本轮未推，不删，留待下次成功同步——重连重推 `redeployLocalNodeRuntime` 在重推成功后按 ID 补删（R21-X-02 `deleteRedeployStaleRuleConnLimiter`）——或删除兜底 ②”——“留待下次成功同步”现为真（有兑现路径），与 F-02 报告 §2.1 的改法逐字一致。`R20-FIX-F01-REPORT.md:77` 的同句（“留待下次成功同步或删除兜底”）是历史报告、记录当时口径，F-02 报告已明言不改——本轮复核确认该行仍在原处、未被误改。`doc/` 全仓 grep “留待下次”仅命中上述两处（代码注释 + R20 历史报告），无用户文档沿用旧口径。

### 2.3 沙箱先红后绿（独立探针）

- 绿 1 / 红 / 绿 2 同 §1.2（同一批探针运行）：X-02 四项——主回归（ipMaxConn=5 清零后重推，恰 1 次 DeleteCLimiters 且在 UpdateService 之后）、隧道重试腿、转发重试腿、仍带限流守卫（0 次）——绿相全 PASS；红相主回归/隧道重试/转发重试三项 FAIL（`expected exactly 1 DeleteCLimiters, got 0: []` / `got 0`），与立案机理（重连窗口内 DeleteCLimiters 计数 0）逐字一致；守卫腿红相仍 PASS（符合预期）。

### 2.4 真机腿（R21 Leg B 同序列、预期反转；转发 9）

- 建：带限流转发 `r22-l1-off2` id 9（maxConn=5，`203.0.113.9:9002`，inPort 18340）：gost services=4，climiters `['rule_conn_limit_8','rule_conn_limit_9']`，md5 `d748ede0…`（`R22-L1-tm-x02-mkfwd.log`）。
- 离线更新（13:52:58Z，`R22-L1-tm-x02-offline-update.log`）：`agentstop`（state inactive）→ `updfwd 9 {"maxConn":0,"ipMaxConn":0}` code 0、回执警告“节点 w10-test 不在线，已跳过下发”→ gost md5 **不变** `d748ede0…`，limiter 9 仍在且被引用（离线更新只改 DB，未推节点——与立案 B 腿前半段一致）。
- 重连重推（13:53:06Z 起，`R22-L1-tm-x02-reconnect-wide.log`）：`agentstart` 后 agent active；**13:53:08（重连后 2 秒）**面板即下发重推：`UpdateService` 两次（转发 9 的新配置 `9_1_0_tcp/udp` 已无 `climiter` 字段；转发 8 仍带 `rule_conn_limit_8`）→ 同秒 `DeleteCLimiters {"limiter":"rule_conn_limit_9"}`（`requestId=1_1792936388707746913`）。窗口内（since 13:52:58）`DeleteCLimiters` 匹配 2 行 = **恰 1 条命令**，且为 limiter 9；`rule_conn_limit_9` 匹配 1 行（详情行）；自愈/heal/孤儿行 0 提及——清理出自面板重连重推，非 F-04 自愈。重推后 gost：climiters `['rule_conn_limit_8']`、refs `['rule_conn_limit_8']`，md5 `3c462e89…`；节点 status 回 1。
- 探针事故登记（已处理，不影响结论）：首版轮询脚本以 13:53:20 为 journal 窗口起点、150 秒轮询 0 命中——重推实际发生在 13:53:08，比窗口起点早 12 秒（本轮 onNodeOnline 重推即时触发，无 F-02 报告所述 30 秒冷却排队；该 30 秒为其轮询容限、非固定延迟）。宽窗口（since 13:52:58）重查后命令级证据完整在位。
- **X-02 结论：CLOSE。** 选 ① 的行为已兑现：离线更新 → 重连 → 重推成功后补删失势 limiter，journal 恰 1 条命令、gost 孤儿清零、自愈 0 介入；与 R21 Leg B（重启后 limiter 41 仍在、DeleteCLimiters 计数 0）逐项反转。

## 三、R21-X-03（产品 P3）：forwardForceDelete gost 路径不发任何节点清理命令 — CLOSE

### 3.1 立案口径与修复代码 pin 死（本轮独立复核）

- 立案（R21-X-ledger §三 / V-04）：`mutations.go:3708–3792` 先删库；realm 走 syncRealmNodes；nft 走 reconcile；gost 面无节点命令。真机 Leg D：建 43 → force-delete code 0 → gost 仍 14 服务、limiter 43 在且被引用、服务仍监听 13285、journal 0 次清理命令。
- 修复在位（b823421，`mutations.go:3778–3792`）：`if !isRealm && !nftMode` 时逐节点 best-effort 调 `deleteForwardServicesOnNodeBatch`（与普通删转发同漏斗：先 DeleteService、后按 ID 兜底 DeleteCLimiters）；节点离线只记 `force_delete_forward_cleanup_skipped` 日志、不硬失败；realm/nft 分支行为不变。

### 3.2 沙箱先红后绿（独立探针）

- 绿 1 / 红 / 绿 2 同 §1.2：X-03 三项——主回归（ipMaxConn=4 限流转发 force-delete，DeleteService ≥1 + 恰 1 次 DeleteCLimiters 且顺序在后）、realm 守卫（realm 隧道 force-delete 发 0 次 DeleteService / 0 次 DeleteCLimiters、DB 行仍删除）、普通删转发回归——绿相全 PASS；红相主回归 FAIL（`expected DeleteService from force-delete (gost path), got 0`），与立案机理逐字一致；两守卫腿红相仍 PASS（符合预期）。

### 3.3 真机腿（转发 10；普通删回归转发 11）

- 建：带限流转发 `r22-l1-fd3` id 10（maxConn=5，`203.0.113.9:9003`，inPort 32584）：gost services=6，climiters `['rule_conn_limit_10','rule_conn_limit_8']`，refs 同集；删前 `*:32584 LISTEN`（flux_agent pid 141602）（`R22-L1-tm-x03-mkfwd.log`、`R22-L1-tm-x03-force.log`）。
- force-delete（13:56:19Z）：`forcefwd 10` code 0；**gost services=4、climiters=`['rule_conn_limit_8']`、refs=`['rule_conn_limit_8']`**；端口 32584 `NO_LISTENER_32584`（腿脚本 listen 断言报 FAIL 即“无监听”的期望态，见 §5 事故注）。journal（`R22-L1-tm-x03-force-journal.log`）：同秒先 `DeleteService {"services":["10_1_0_tcp","10_1_0_udp","10_1_0"]}`（`requestId=…28312984`）→ 后 `DeleteCLimiters {"limiter":"rule_conn_limit_10"}`（`requestId=…287196439`），顺序正确；窗口内各恰 1 条命令。
- 普通删转发回归：新建转发 `r22-l1-del4` id 11（maxConn=5，limiter 11 在且被引用）→ `delfwd 11` code 0 → gost 回 4 服务、climiters `['rule_conn_limit_8']`（`R22-L1-tm-x03-normaldel.log`）。F-03 修复未改动普通删除路径。
- **X-03 结论：CLOSE。** force-delete gost 路径现发 DeleteService + DeleteCLimiters、服务已删、limiter 已清、端口已停监听；与 R21 Leg D（gost 仍 14 服务、limiter 在且被引用、journal 0 次清理命令）逐项反转。

## 四、全量回归（沙箱，快照副本）

- `go test ./... -count=1 -timeout 30m -p 1`（`R22-L1-gotest-full.log`）：handler `ok 435.064s`、repo `ok 135.865s`、contract `ok 49.621s`、ws/kernel/logredact/metrics/monitoring/netguard/nftables/security 等其余包 ok；**产品失败 0**，唯二 FAIL 均已归因（见下）。
  - `internal/health` FAIL：仅 `TestValidateMonitorTCPTarget/公网域名放行` 一格（`monitor_ssrf_test.go:39`，`example.com:443` 被判内网/保留地址）——已知沙箱 fake-DNS 环境红，与 R21 修复无关；任务书点名的唯一合法红，留签名单列。
  - `internal/kernel` 首跑 FAIL：`TestR20F03IdleCopyStatesWindowedSemantics`（`r20_f03_test.go:80`，读 `vite-frontend/...` 报 no such file）——**探针快照缺件，非产品失败**：快照只拷了 `go-backend/`，该测试读仓根相对路径文件。补 `vite-frontend` 与 `doc/` 软链后重跑（`R22-L1-kernel-rerun2.log`）：`ok 0.229s`，exit 0。首跑日志 `R22-L1-kernel-rerun.log` 保留为缺件证据。
- R21 修复测试逐字重跑（`R22-L1-r21tests.log`）：**11/11 PASS**（F-01 3 个、F-02 5 个、F-03 3 个），exit 0。
- `go vet ./internal/http/handler/` exit 0、`go build ./...` exit 0（`R22-L1-vet-build.log`，仅 GOPATH/GOROOT 同目录工具链警告，与代码无关）。
- 探针文件已删：快照内 `r22_l1_probe_test.go` 删除后 `grep -c r22` = 0；`.r22-tree` 全程干净。

## 五、探针事故登记（均已处理，不影响结论）

1. 腿脚本隧道 id 传错：首版 X-01 `mkfwd` 传隧道 1（实为 2，id 自增延续），`code=-1 隧道不存在`；改传 2 后一次通过（`R22-L1-tm-x01-mk.log` 首腿 4 FAIL 为此探针错误，正式腿在 `R22-L1-tm-x01-mkfwd.log`）。
2. X-02 轮询窗口起晚 12 秒（见 §2.4）：重推实际在 13:53:08，首版轮询自 13:53:20 起算致 150 秒 0 命中；宽窗口重查闭合，无挑拣、无重跑腿。
3. kernel 首跑快照缺件（见 §四）：缺 `vite-frontend/` 与 `doc/`，补软链后重跑 ok。
4. RED-3 临时回退切块事故：首版 mutations.go 回退切掉了多余的闭括号致编译失败一次（`syntax error`）；修正为“保留空 if 体”后红相正常跑出 6 FAIL / 4 PASS。回退只在快照副本内，`.r22-tree` 未动。
5. 一次 `go vet` 会话被沙箱打断（aborted，无输出）：文件本身无损，重跑 vet exit 0（§四）。
6. 一次 SSH final-check 断管（Broken pipe）：重跑后最终态逐项验证通过（§六）。

## 六、恢复对账与离场

- 清理腿（`R22-L1-tm-cleanup.log`）：`delfwd 8` → gost 2 服务/0 limiter；`delfwd 9` → 0 服务/0 limiter；`deltunnel 2` code 0、隧道从列表消失；counts 回 **1/0/0/1/1 PASS**；gost md5 回 **`23ca5d88ba3bca475f24ee48c47e0fc9`**（与腿前 pristine 逐字一致）；节点 `w10-test` status 1、`dev (debian/amd64)`、forwardMode agent。
- 最终复核（`R22-L1-tm-final.log`，同步周期后）：三容器 healthy（backend/frontend new4），运行中 paneld md5 `1ca4572b…`，served index `57ab941e…`，GET / 6366 → 200，agent active，gost md5 `23ca5d88…`。
- 机上残留：`/root/r22-l1/`（legs.py、DB/gost 备份、日志外）未清——为可复核的部署痕迹，不影响面板运行；是否清理请主控定。机上凭据 `/root/panel-cred.txt` 与 `.token` 已删（`CRED-CLEANED`）；日志中 token 0 出现（凭据只读文件、绝不打印）。
- 锁已释放（13:59:00Z，free 已确认）。

## 七、观察项（登记，不立案）

- OBS-R22-L1-01（承继 OBS-F03-1，非新立案）：force-delete 成功后审计日志的 `residue_note` 仍为静态文案“节点规则可能残留，需人工确认清理”（本轮绿跑日志亦见该行，`R22-L1-probe-green1.log` 内 force-delete realm 腿审计行）。清理已实际下发，该提示现为误导性残留文案；是否收敛由主控定，不属 R21 修复域、不影响三项关闭。
- 未验证（结转）：多节点真机腿（测试机仅 1 节点；切换/重推/force-delete 的逐节点循环均与删转发漏斗同口径，沙箱为单节点 mock）；realm/nft 路径真机 force-delete（本轮只动 gost 腿；沙箱既有测试覆盖两分支行为不变）；远端节点/非 Debian 内核、浏览器 DOM 腿、真实 Gist 端到端（沿用 R21 未验证清单，与本修复无关）。

## 八、证据索引（同目录落盘）

| 文件 | 内容 |
|---|---|
| `R22-L1-legs.py`（14,235 字节） | 真机腿驱动（F-03 腿脚本模式 + F-01 tunengine 腿 + agent 停/启、realm 配置、端口监听腿） |
| `R22-L1-probe-green1.log`（12,377 字节） | 沙箱绿 1：独立探针 10/10 PASS（16.128s） |
| `R22-L1-probe-red.log`（12,914 字节） | 沙箱红：6 FAIL / 4 PASS，失败文案与立案机理逐字一致 |
| `R22-L1-probe-green2.log`（12,311 字节） | 沙箱绿 2：恢复后 10/10 PASS，0 FAIL |
| `R22-L1-gotest-full.log` | `go test ./...` 全量：handler ok 435.064s；唯一 FAIL 为 health 环境红 + kernel 快照缺件首跑 |
| `R22-L1-kernel-rerun.log` / `R22-L1-kernel-rerun2.log` | kernel 缺件首跑 FAIL / 补件后 ok 0.229s |
| `R22-L1-r21tests.log`（13,320 字节） | R21 修复测试 11/11 PASS |
| `R22-L1-vet-build.log` | vet exit 0 / build exit 0 |
| `R22-L1-lock-wait.log` | 锁 `--wait` 记录：R22-L5 持有，等 24 轮，13:50:01Z 抢到 |
| `R22-L1-tm-pristine.log` / `R22-L1-tm-pristine2.log` | 面板 new4 复核（paneld/served md5）/ counts 1/0/0/1/1、DB 备份 md5 `9608f0c7…` |
| `R22-L1-tm-x01-mk.log` / `R22-L1-tm-x01-mkfwd.log` | X-01 建隧道/转发（首版隧道 id 传错 4 FAIL；正式腿全绿） |
| `R22-L1-tm-x01-switch-realm.log` / `…-journal.log` | X-01 切 realm：gost 0/0/0、realm 1 endpoint、journal 恰 1 条 DeleteCLimiters（rule_conn_limit_8） |
| `R22-L1-tm-x01-switch-back.log` / `…-journal.log` | X-01 切回：limiter 重建（AddCLimiters "$ 5"）、DeleteCLimiters 0 条 |
| `R22-L1-tm-x02-mkfwd.log` / `R22-L1-tm-x02-offline-update.log` | X-02 建转发 9 / 离线更新 code 0 带警告、gost md5 不变 |
| `R22-L1-tm-x02-reconnect.log` / `R22-L1-tm-x02-reconnect-wide.log` | X-02 重连：首版轮询 150s 0 命中（窗口起晚）/ 宽窗口：13:53:08 恰 1 条 DeleteCLimiters（rule_conn_limit_9） |
| `R22-L1-tm-x03-mkfwd.log` / `R22-L1-tm-x03-force.log` / `R22-L1-tm-x03-force-journal.log` | X-03 建转发 10、监听 32584 在位 / force-delete code 0、gost 只剩 limiter 8、端口停监听 / journal 先 DeleteService 后 DeleteCLimiters |
| `R22-L1-tm-x03-normaldel.log` | X-03 普通删转发回归（转发 11 建删后 gost 回 4 服务） |
| `R22-L1-tm-cleanup.log` / `R22-L1-tm-final.log` | 清理恢复（counts 1/0/0/1/1、gost md5 回 `23ca5d88…`）/ 最终复核（容器 healthy、paneld/served md5、agent active） |

— R22-L1-close-r21 END · 自证：本行之前正文字节数 21137 · END
