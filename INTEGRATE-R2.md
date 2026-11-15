# INTEGRATE-R2 — 流量修复第二轮集成台账

基线：traffic-fix/integrate 尖端 5da8aa10（2026-11-15，34 commits above ab1d1381）。
集成尖端：48fcc59e（6 路全部合入，见下）。门禁均在 48fcc59e 上跑通；本台账提交本身只加文档，不改代码。
Fixup 尖端：4d17bfa3（合入 traffic-fix2/kill-e2 694ff553，见“Fixup 合入（e2）”一节；代码尖端即 4d17bfa3，门禁在其上重跑）。
Fixup 尖端（vd）：174be261（合入 traffic-fix3/vd d0f5431b，见“Fixup 合入（vd）”一节）；其后因 C03 契约过期另有一测试修正提交 59fce6d7（只改 tests/contract，不动产品代码），最终尖端 59fce6d7。

## 合入顺序（实际）

| # | 分支 | 提交 | 内容 | 合并提交 | 冲突 |
|---|------|------|------|----------|------|
| 1 | traffic-fix2/mark | b6ceb462 | E-01 跨面板 mark 碰撞（高） | a1cec16d | 无 |
| 2 | traffic-fix2/metering | be1c0d98 | B-NEW 入站行丢增量＋A4SS 文档 | 3bf8a638 | 无 |
| 3 | traffic-fix2/display | 4910d2b8 | D-02-NEW 超限文案对调 | 0fc7a4ab | 无 |
| 4 | traffic-fix2/quota | 4a471982 | C-(1) 恢复清单、C-(5) 轮换门、C-(6)① flow_mib | 20e43d4b | 无 |
| 5 | traffic-fix2/lows | 57ace14b | E-02～E-06、C-(6)③ 小数配额 | 2e928f75 | 无 |
| 6 | traffic-fix2/kill | fd166ded | C-(4) SIGHUP 重载＋A-NEW 暂停杀连接 | 48fcc59e | 无 |

6 路全部为 merge commit 合入，6 条分支均已是 HEAD 祖先（merge-base --is-ancestor 全过）。
5da8aa10..48fcc59e 差集：50 文件 +2559/−216，与 6 路各自差集之和（+2559/−216）逐项一致，无修复丢失。
冲突点：0。凡动到修复逻辑的冲突点：无（未发生冲突，故无）。

## Fixup 合入（e2）

| 分支 | 提交 | 内容 | 合并提交 | 冲突 |
|------|------|------|----------|------|
| traffic-fix2/kill-e2 | 694ff553 | C-(4) 定因修完：伴生 Clash API 选择性杀连接＋墓碑兜底＋面板 Companion 修正 | 4d17bfa3 | 无 |

合入核验：`git merge-base --is-ancestor 694ff553 HEAD` 通过；逐文件 numstat 对照 `fd166ded..694ff553` 与 `48fcc59e..4d17bfa3` 完全一致（唯一差额是本台账文件 INTEGRATE-R2.md，来自 06d95ff0 台账提交，与 e2 无关）。5da8aa10..4d17bfa3 差集：56 文件 +3292/−220（= 6 路 +2559/−216 ＋ e2 +702/−23 ＋台账 +50/−0 － 行级重叠 19 行：kill 路新增、e2 又改的行在合并差集中只计一次；逐文件 numstat 已对过，无修复丢失）。
冲突点：0。凡动到修复逻辑的冲突点：无（未发生冲突，故无）。

## 门禁（Fixup 尖端 4d17bfa3）

| 门禁 | 结果 | 日志 |
|------|------|------|
| check-doc-consistency.sh | PASS（EXIT=0） | ~/workspace/tmp/int-r2e2/doc-consistency.log |
| go vet（go-backend） | PASS（EXIT=0，零输出） | ~/workspace/tmp/int-r2e2/vet-backend.log |
| go vet（go-gost/x） | PASS（EXIT=0，零输出） | ~/workspace/tmp/int-r2e2/vet-x.log |
| K11 计数核对 | PASS（实数 tested=90 / untested=123，与 ci-build.yml 声称 90/123 一致；e2 新增 clashapi_test.go 落在已有 tested 包 kernel 内，计数不动） | doc 门禁内含 K11 对账；独立 find 复算见正文 |
| pnpm build | PASS（EXIT=0） | ~/workspace/tmp/int-r2e2/pnpm-build.log |
| handler 全包 | PASS（ok，845.082s，EXIT=0；运行中遇一次运行时重启，handler 日志落盘完整、repo/x 随后重跑） | ~/workspace/tmp/int-r2e2/handler-full.log |
| repo 全包 | PASS（ok，245.594s，EXIT=0） | ~/workspace/tmp/int-r2e2/repo-full.log |
| go-gost/x 全包 | PASS（首跑即全绿，EXIT=0；90 包 ok＋123 包无测试文件＝213，与 K11 实数一致；internal/net/udp ok 8.899s，无抖动复跑） | ~/workspace/tmp/int-r2e2/x-full.log |

前置：vite-frontend/node_modules 已是软链 → 主检出 ~/workspace/flvx-max/vite-frontend/node_modules（typescript 在场，集成前已核实）。

| 门禁 | 结果 | 日志 |
|------|------|------|
| check-doc-consistency.sh | PASS（EXIT=0） | ~/workspace/tmp/int-r2/doc-consistency.log |
| go vet（go-backend） | PASS（EXIT=0，零输出） | ~/workspace/tmp/int-r2/vet-backend.log |
| go vet（go-gost/x） | PASS（EXIT=0，零输出） | ~/workspace/tmp/int-r2/vet-x.log |
| K11 计数核对 | PASS（实数 tested=90 / untested=123，与 ci-build.yml 声称 90/123 一致） | doc 门禁内含 K11 对账；独立 go list 复算见正文 |
| pnpm build | PASS（EXIT=0） | ~/workspace/tmp/int-r2/pnpm-build.log |
| handler 全包 | PASS（ok，859.288s，1943 个顶层 Test 函数，EXIT=0） | ~/workspace/tmp/int-r2/handler-full.log |
| repo 全包 | PASS（ok，249.880s，EXIT=0） | ~/workspace/tmp/int-r2/repo-full.log |
| go-gost/x 全包 | PASS（重跑后全绿，EXIT=0；90 包 ok＋123 包无测试文件＝213，与 K11 实数一致） | ~/workspace/tmp/int-r2/x-full.log（首跑）＋~/workspace/tmp/int-r2/x-full-rerun.log（重跑全绿） |

前置：vite-frontend/node_modules 已是软链 → 主检出 ~/workspace/flvx-max/vite-frontend/node_modules（typescript 在场，2026-11-15 09:25 建，集成前已核实）。

### go-gost/x 首跑红包说明（已闭合，未改代码）

首跑唯一红包：`internal/net/udp`（TestListenerZeroGapBurstAbsorbed 10s 超时、TestPoolOutboundOnlyActivityKeepsSession）。定因：
- 该包不在第二轮差集内（5da8aa10..48fcc59e 对该包差集为空），且其 import 闭包（bypass、config/parsing/bypass、internal/idle、internal/net、eventlog、logger）不含任何一路改动的包（kernel、socket、internal/util/port），合并不可能改变其行为。
- 基线对照：同一包在基线尖端 5da8aa10 的临时 worktree（~/workspace/tmp/int-r2-base，已删）上一次即绿（ok 8.910s，日志 ~/workspace/tmp/int-r2/x-udp-base.log）。
- 合并尖端单包重跑：第一次仍红（仅 Burst 一项，238/300，与首跑数字相同），第二次即绿（ok 8.871s，日志 ~/workspace/tmp/int-r2/x-udp-rerun2.log）；随后全量重跑一次全绿（EXIT=0，零 FAIL 行）。
- 结论：沙箱负载抖动型 flake（零间隔 300 包突发，listenLoop 被节流时内核收包缓冲溢出丢包），按 2.1.7 同包同族先例处置——复跑转绿，不修代码。两失败项的数字在两次红跑中相同（238/300），是同一节流水平下的固定缓冲容量所致，不是代码回归。

## C-(4)——已定因并修复、真机重证通过（e2 闭合，原保留事项作废）

定因：面板驱动 disable 反向，是因为伴生重载本身不是选择性断开——重载会同时断开该实例上全部订阅的已有连接，凭据移除却走了重载路径。修法（694ff553）：伴生 Clash API 选择性杀连接（只断被移除订阅的会话，不重载配置）＋墓碑兜底（被移除订阅重连时也被断开，直到下一次重载／重启刷新配置）＋面板 Companion 修正；连接级关闭不可用（如伴生未暴露管理接口）时回退到重载。doc/guide/subscription.md 已按此改写。

真机重证（测试机，e2 路，面板驱动 disable）：被截订阅 u2 死在 19,842,533B（dl-a.bin 落盘该尺寸，未跑满 52,428,800B）；保留订阅 u3 跑满 52,428,800B；伴生进程 pid 不变、restarts 0。u2 落盘尺寸与 e2 路会话记录 /home/hatch/agents/agent-0bbecf2a-bd00-40a3-86e2-662603ad668f 内 reproof1 输出一致（dl-a.bin 19842533，E2_BYTES=52428800）。

## Fixup 合入（vd）

| 分支 | 提交 | 内容 | 合并提交 | 冲突 |
|------|------|------|----------|------|
| traffic-fix3/vd | d0f5431b | V-D 超限截断失效：墓碑用户 mark 在 nft 输出链首条加 reject（新拨号握手即死）；Apply 重排为 diff＋连接级关闭＋建墓碑在前、nft 同步在后；重加用户先清墓碑 | 174be261 | 无 |

合入核验：`git merge-base --is-ancestor d0f5431b HEAD` 通过；逐文件 numstat 对照 `628d3953..d0f5431b` 与 `628d3953..174be261` 完全一致。628d3953..174be261 差集：13 文件 +282/−93（= vd 差集本身），无修复丢失。
冲突点：0。凡动到修复逻辑的冲突点：无（未发生冲突，故无）。

## 门禁（Fixup 尖端 174be261；最终尖端 59fce6d7 只改 tests/contract，见 C03 一节，产品代码与 174be261 逐字节相同）

| 门禁 | 结果 | 日志 |
|------|------|------|
| check-doc-consistency.sh | PASS（EXIT=0） | ~/workspace/tmp/int-r3vd/doc-consistency.log |
| go vet（go-backend） | PASS（EXIT=0，仅 wrapper 行，零诊断） | ~/workspace/tmp/int-r3vd/vet-backend.log |
| go vet（go-gost/x） | PASS（EXIT=0，仅 wrapper 行，零诊断） | ~/workspace/tmp/int-r3vd/vet-x.log |
| K11 计数核对 | PASS（实数 tested=90 / untested=123，与 ci-build.yml 声称 90/123 一致；注：只计 TestGoFiles 会得 87/126，另 3 包 connector/router、connector/tunnel、internal/util/stats 只有外部测试文件 XTestGoFiles，计入后为 90/123） | 独立 go list 复算（TestGoFiles＋XTestGoFiles） |
| pnpm build | PASS（EXIT=0） | ~/workspace/tmp/int-r3vd/pnpm-build.log |
| handler 全包（`go test ./...`，含 tests/contract） | handler/repo 等包全部 ok（handler 包 893.382s、repo 包 286.185s）；唯一红项 tests/contract，见 C03 一节（已修，整包重跑绿） | ~/workspace/tmp/int-r3vd/handler-full.log |
| repo 全包 | PASS（ok，EXIT=0；首跑 286.185s 已在 handler-full.log 内，独立重跑 cached） | ~/workspace/tmp/int-r3vd/repo-full.log |
| go-gost/x 全包 | PASS（重跑后全绿；首跑唯一红包 internal/net/udp，见下节；89 包 ok＋123 包无测试文件＋1 包重跑绿＝213，与 K11 实数一致；vd 所改 kernel 包单跑 ok 102.243s） | ~/workspace/tmp/int-r3vd/x-full.log（首跑）＋~/workspace/tmp/int-r3vd/x-udp-rerun.log（udp 重跑绿）＋~/workspace/tmp/int-r3vd/x-kernel-rerun.log |

前置：vite-frontend/node_modules 已是软链 → 主检出 ~/workspace/flvx-max/vite-frontend/node_modules（typescript 在场，e2 fixup 时已建，本轮复核仍在）。

### go-gost/x 首跑红包说明（已闭合，未改代码）

首跑唯一红包：`internal/net/udp`（TestListenerZeroGapBurstAbsorbed 10s 超时）。定因：
- 该包不在 vd 差集内（628d3953..174be261 对该包差集为空），且 vd 所改 kernel 包不 import internal/net/udp，合并不可能改变其行为。
- 基线对照：同一包在基线尖端 628d3953 的临时 worktree（/tmp/wt-vd-base，已删）上一次即绿（ok 8.869s，日志 ~/workspace/tmp/int-r3vd/x-udp-base.log）。
- 合并尖端单包重跑一次即绿（ok 8.886s，日志 ~/workspace/tmp/int-r3vd/x-udp-rerun.log）。
- 结论：沙箱负载抖动型 flake，与 R2 集成首跑同一测试同族（彼时 238/300 突发丢包），按 2.1.7 同包同族先例处置——复跑转绿，不修代码。

### C03 契约过期（F2-F 遗留，集成轮首次以 `go test ./...` 全树口径跑出）

`go-backend/tests/contract` 单测 TestC03UserCreateBadNumericRejected 之 quota-float 格红：monthlyQuotaGB 1.5 创建返回 Code 0，测试仍要求拒绝。定因：
- F2-F（57ace14b，C-(6)③）把配额解析改为 asFloat，小数 GB 为合法值（真机验过 0.01/1.5 落库、“abc” 仍拒），但 C03 契约仍按整数口径钉死“1.5 必须拒绝”。二者直接矛盾。
- 此红与 vd 无关：go-backend 在 4d17bfa3..174be261 间差集为空；单测在当前树上确定性复现（-count=1 亦红）。
- 此前各轮“handler 全包”只跑了 handler 包本身（845–893s），tests/contract 从未被跑过，故 F2-F 起即红而无人发现；仓库 AGENTS.md 的口径本就是 `go test ./...` 含 contract 测试，本轮按此口径跑出。
- 修正（59fce6d7，只改测试文件，不动产品代码）：quota-float 移出拒绝表，改为正向格——monthlyQuotaGB 1.5 创建成功且恰落一行 user_quota；quota-string（"abc"）仍拒绝且不落库。最终尖端上 tests/contract 整包重跑绿（ok 0.475s 单测复核；整包重跑见下表）。

| 门禁（最终尖端 59fce6d7） | 结果 | 日志 |
|------|------|------|
| tests/contract 整包 | PASS（EXIT=0） | ~/workspace/tmp/int-r3vd/contract-full.log |
| check-doc-consistency.sh（重跑） | PASS（EXIT=0） | ~/workspace/tmp/int-r3vd/doc-consistency-rerun.log |
| go vet（go-backend，重跑） | PASS（EXIT=0） | ~/workspace/tmp/int-r3vd/vet-backend-rerun.log |

handler/repo/x 三份绿日志取自 174be261：最终尖端相对 174be261 的唯一差额是 tests/contract 测试文件（`git diff 174be261..59fce6d7 --stat` 仅此一文件），产品代码逐字节相同，三份日志继续有效，不重复跑 handler（893s）与 x 全量。

## V-D——已定因并修复、真机重证通过（vd 闭合）

定因：协调路“截断流没走仅移除用户路径”的假说被推翻——截断流确实建了墓碑，漏点是墓碑优雅关闭不彻底：缓冲／在途字节继续漏出（慢速下载漏约 6.5MB），且旧凭据仍可开新连接。修法（d0f5431b）：墓碑用户 mark 在 nft 输出链首条加 reject（新拨号握手即死）；Apply 重排为 diff＋连接级关闭＋建墓碑在前、nft 同步在后；重加用户先清墓碑；doc/guide/subscription.md 同步改写。

真机重证（测试机，vd 路）：超限后旧凭据探测 3 次均为 rc 35 零字节（新连接打不开）；保留订阅 u1 下 5MB 全通 200/5242880；伴生 pid 400510 未变（未重启）。

## 未启动的后续阶段

真机总验与第二轮全量回头看是之后的独立阶段，本集成路不启动。
