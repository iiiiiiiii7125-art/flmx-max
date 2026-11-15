# INTEGRATE-R2 — 流量修复第二轮集成台账

基线：traffic-fix/integrate 尖端 5da8aa10（2026-11-15，34 commits above ab1d1381）。
集成尖端：48fcc59e（6 路全部合入，见下）。门禁均在 48fcc59e 上跑通；本台账提交本身只加文档，不改代码。

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

## 门禁（尖端 48fcc59e）

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

## 保留事项（必须原样携带，不许写成已闭合）

**C-(4) 面板驱动端到端未验**：kill 分支（fd166ded）的 C-(4) 面板驱动端到端未验——隔离 SIGHUP 机制已证，面板驱动 disable 5 次反向；跟进路 traffic-fix2/kill-e2 正在并行定因。kill 分支照原样合入；e2 分支落地后由集成路再合一次 fixup 并重跑门禁。本集成回报不把 C-(4) 写成已闭合。

## 未启动的后续阶段

真机总验与第二轮全量回头看是之后的独立阶段，本集成路不启动。
