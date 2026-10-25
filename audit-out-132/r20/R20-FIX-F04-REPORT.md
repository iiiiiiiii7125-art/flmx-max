# R20-F04 修复报告：存量孤儿 climiter 清理路径

测试机窗口（UTC）：2026-10-25T06:52:00Z – 06:58:10Z（锁 R20-F04，`scripts/testmachine-lock.sh`，每次重启/部署前 `check` 均通过，06:58:09Z 释放，锁状态 free）。
基线：main @ b8fb567（`b8fb5678e0859da03e8fc8ecfcf50ce675480296`）。分支：`fix-r20-f04`（独立 worktree `~/workspace/.fixr20-f04`）。不合 main、不打 tag、不发版。

## 一、所选方案：① 启动自愈加剪无引用 limiter

报主控：选方案 ①，弃方案 ②。

理由：
- 自动：存量孤儿在 agent 下次启动/重载时自愈，无需人工点击、无需新接口；面板升级/部署本就会重启 agent，清理随升级自然发生。
- 覆盖面：不只清本次两例存量孤儿——更新路径孤儿（F-01 未修前仍会产生）及任何未来孤儿在下次启动时同样被剪；方案 ② 只清面板当时算出的名单。
- 最小改动：只动 agent 一处（`x/config/dedupe.go` 新函数 + `program.go` 一行接线），沿用已证稳的 Heal* 模式；无后端新端点、无路由、无前端改动、无协议变化。
- 误剪可自愈：面板是 climiter 的唯一生产方，下次同步会对仍需要的 limiter 重新发 `AddCLimiters`（已存在则走 `UpdateCLimiters`），故即使剪错亦可恢复；但本修仍按任务要求对 `user_conn_limit_*` 永不剪（见安全线）。
- 方案 ② 的短板：需新端点 + 路由 + 前端按钮三处新表面，且面板按 DB 算出的“无引用”名单与 agent 落盘文件可能不一致（联邦/手动改动），多一路分歧源。

实现中未遇结构性障碍，故未改选。关键前提经全仓 grep 证实：conn limiter 的唯一消费方是服务 `ServiceConfig.CLimiter` 字段（逗号分隔名，`x/config/parsing/service/parse.go:211` 经 `resolveConnLimiter` 解析，逗号切分/TrimSpace/空段丢弃）；链/跳点/admission 均无 climiter 字段；CLI 建服务亦经同一 CLimiter 字段挂接（`x/config/cmd/cmd.go:298-302`）；handler 的 `Limiter` 字段挂的是 traffic limiter，不在此列。

## 二、改动（3 文件改 + 1 测试文件新）

| 文件 | 改动 |
|---|---|
| `go-gost/x/config/dedupe.go` | 新增 `PruneUnreferencedCLimiters`（纯函数）与 `HealPersistedOrphanCLimiters`（剪后立即 `PersistNow`，与 Heal* 同口径：落盘失败只告警、不阻塞启动），及常量 `userConnLimiterPrefix = "user_conn_limit_"` |
| `go-gost/program.go` | `healPersistedConfig` 内五类去重之后接剪除一步（引用集按去重后的存活服务算）；函数注释同步更新 |
| `go-backend/internal/http/handler/control_plane.go` | 仅注释：`deleteForwardConnLimiterOnNode` 上“agent 启动自愈只按名去重、不剪除无引用 limiter”的旧断言已失效，改述为 R20-F04 起自愈亦剪无引用 limiter |
| `go-gost/x/config/r20_fix_f04_test.go`（新） | 5 个测试（见 §三） |

剪除判定（`PruneUnreferencedCLimiters`）：
1. 引用集 = 全部存活服务 `CLimiter` 字段的逗号分隔名（TrimSpace、空段丢弃；nil 服务跳过）。
2. `CLimiters` 中无引用者剪掉；`user_conn_limit_*` 前缀者永不剪（先判引用、再判前缀，两者独立）。
3. 只动 `CLimiters` 一类：traffic / rate limiter 一律不动；nil climiter 项原样保留（与 `Deduplicate*` 同口径；`healPersistedConfig` 内 `StripNil` 先行）。
4. 无剪除时返回 nil、不改动配置、不触发写。

## 三、回归：先红后绿

红（`f04-logs/red-unit.log`）：新测试先行，编译失败——`undefined: PruneUnreferencedCLimiters` / `undefined: HealPersistedOrphanCLimiters`，6 处。

绿（`f04-logs/green-unit.log`）：实现后 5/5 PASS：
- `TestPruneUnreferencedCLimiters`：毒化配置含真实存量孤儿之名（`rule_conn_limit_12 ["$ 7"]`、`rule_conn_limit_2 ["$ 1"]`），剪掉的恰为 `[rule_conn_limit_12 rule_conn_limit_2 manual-climiter]`；保留：有引用 `rule_conn_limit_5`、逗号复合引用 `rule_conn_limit_42`、有引用 `user_conn_limit_9`、**无引用 `user_conn_limit_7`**（共享豁免分支）、nil 项原样保留；traffic/rate limiter 不动。
- `TestPruneUnreferencedCLimitersCleanIsNoop` / `TestPruneUnreferencedCLimitersNilSafe`（nil cfg、nil 服务/项、全 nil 服务时仍按规则剪）/ `TestHealPersistedOrphanCLimiters`（剪后落盘，落盘文件 2 条、无孤儿）/ `TestHealPersistedOrphanCLimitersCleanIsNoop`（干净不写盘）。

全量（`go test ./...` + vet + 前端）：
- `go-gost/x`：27 包全绿（`green-x-all2.log`，exit 0）。首跑 `x/socket` 2 红（`TestTuneBdpLegacyConfRemoved`、`TestTuneProbeDoesNotWrite`，均为 `no space left on device` 写 `/tmp`）——根因为 /tmp tmpfs（512M）被两处陈旧 `go-build*` 占 282M；清掉后两测试重跑绿（`green-x-socket-rerun.log`），全量重跑 27/27 绿。环境问题，非产品失败。
- `go-gost` 根模块：`tests/lifecycle` ok；`go vet ./...` 0（`vet-gost.log`）。
- `go-backend`：除以下 3 处环境红外全部 ok（`green-backend-all.log`）；`go vet ./...` 0。3 处红逐一与基线同签名核对，均非本修引入（本修对后端仅 3 行注释）：
  1. `internal/health`：`TestValidateMonitorTCPTarget/公网域名放行`——任务书点名的唯一可接受红（沙箱 fake-IP DNS），与同日 F-01 lane 基线日志（`/tmp/r20f01-baseline-gotest.log`）同签名。
  2. `internal/http/handler`：600s 超时（`panic: test timed out after 10m0s`，超时瞬间在跑 `TestR1B8_XP317_BatchChangeTunnelAtomicPerItem`）——F-01 lane 同日 600.352s、F-03 lane 同日 600.480s，同一签名、同一包；沙箱多 lane 并行致包内累计超时，非产品失败。触及文件区域另以子集验证：limiter 相关 10 测试全绿（`green-backend-handler-subset.log` 7/7、`green-backend-handler-limiter.log` 3/3）。
  3. `internal/ws` 与 `tests/contract`：编译失败 `could not import … (open …/.cache/go-build/…-d: no such file or directory)`——多 lane 共享 GOCACHE 被他 lane 清理致缓存缺失；F-01/F-03 同日日志同一签名；缓存重建后重跑两者均 ok（ws 14.65s、contract 139.8s，`green-backend-ws-contract-rerun.log`）。
- 前端：无改动；`pnpm install` 后 `pnpm run build`（`tsc && vite build`）exit 0（`green-frontend-tsc.log`）。
- `gofmt`：`dedupe.go`、`program.go` 干净；`control_plane.go` 有 1 处既存 gofmt 异议（`:167` `//（R5-B5 D15）` 缺空格），基线文件同样被标出，非本修引入，未动。

## 四、真机腿（Lightsail Debian 12，16.146.199.68 / tailnet 100.80.182.11）

Pristine 快照（窗口内实测，`machine-pristine.log` / `machine-counts-pristine.log`）：
- `gost.json` md5 `d48f09e1…`（328 行，12 服务），与 R20 总账一致；`climiters` 恰为两例存量孤儿 `rule_conn_limit_2 ["$ 1"]`、`rule_conn_limit_12 ["$ 7"]`；全部服务 `climiter=None`（均无引用）；无 `user_conn_limit_*`。
- agent 二进制 md5 `aff338e3…`（与总账一致）。
- DB 行数：node 1 / tunnel 4 / forward 6 / inbound 578 / subscription 18（`docker cp` 面板 sqlite + WAL 后只读计数；表 51 张含 `sqlite_sequence`，与总账口径闭合）。
- 6 条存量转发 `max_conn=0, ip_max_conn=0`，全部用户 `max_conn=0`——面板同步不可能重建这两例孤儿（转发 2 仍在库但限流已清零，属 R20-X-01 更新路径孤儿；转发 12 已不存在）。

Phase A——存量孤儿启动后清除（`machine-phaseA2.log`）：
- 修复版 agent（`CGO_ENABLED=0` 构建，sha256 `2ee5004f…`，`-X main.version=fix-r20-f04`）部署；首次 `cp` 覆盖运行中二进制报 `Text file busy`——该次重启跑的仍是旧二进制，gost 未变（已记录，非产品问题）；改为停服 → 覆盖 → `reset-failed` → 启动后生效。
- 重启后 `gost.json`：md5 `febff27b…`，328 → 314 行（恰为两例 climiter 块），`grep conn_limit` 0 行，`climiters: None`；服务仍 12 条，traffic limiter `1` 在位。
- 语义 diff（`gost-pristine.json` vs `gost-after-phaseA.json`）：唯一差异是 `climiters` 键被移除，services 逐项相等。
- 60s+ 同步周期后复查（06:56:30Z，`machine-phaseA-sync2.log`）：仍 314 行、0 行 `conn_limit`、12 服务——面板未重建孤儿（md5 变为 `55777bfe…` 仅为同步重落盘的序列化差异，内容与上一份一致）。
- 说明：agent 单元 `StandardOutput=null` / `StandardError=null`，自愈 Warn 日志按设计被丢弃，journal 无对应行；证据以落盘文件前后 diff 为准（强于日志行）。

Phase B——判定表真机验证（`machine-phaseB.log`，scratch 文件 `gost-scratch.json`）：
- 毒化文件：服务挂 `1_2_1_tcp.climiter=rule_conn_limit_5`、`2_5_2_tcp.climiter="user_conn_limit_9, rule_conn_limit_42"`；climiters 7 条（孤儿 12/2、无引用 `manual-climiter`、有引用 5/42、有引用 `user_conn_limit_9`、**无引用 `user_conn_limit_7`**）。
- 修复版 agent 启动后：剪掉的恰为 `{rule_conn_limit_12, rule_conn_limit_2, manual-climiter}`；保留 `{rule_conn_limit_5, rule_conn_limit_42, user_conn_limit_9, user_conn_limit_7}`；服务 12 条，traffic limiter `1` 在位。判定表逐项符合 §二。
- 附注：启动约 6s 后面板重连同步把两服务 `climiter` 字段覆写回 pristine（服务与 pristine 逐项相等，`machine-phaseB-check.log`）——剪除已在启动时按 scratch 引用集完成（否则 5/42 早被剪），证实剪除读的是启动文件、不受随后同步影响。

恢复对账（`machine-restore.log` / `machine-counts-final.log` / `machine-cleanup.log`）：
- 原二进制恢复（md5 `aff338e3…`）、`gost.json` 恢复 pristine（md5 `d48f09e1…`，328 行，2 行 `conn_limit` 即两例孤儿原样在位）、服务 active、agent 与面板 :6366 重建 ESTAB。
- DB 重计：node 1 / tunnel 4 / forward 6 / inbound 578 / subscription 18，与入场一致。
- 面板容器 healthy；机上临时文件（`flux_agent.f04bak`、`/root/f04-gost-pristine.json`、DB 拷贝）已清；面板凭据未入任何日志/报告。

## 五、未验证清单

- SIGHUP 重载路径：与启动共用同一 `healPersistedConfig` 调用点（`reloadConfig`），代码同一路径；真机只验了启动，未单独验 SIGHUP。
- `user_conn_limit_*` 保留分支在真机存量文件上无法验证（pristine 文件内本无该类 limiter）；由沙箱单元测试 + Phase B scratch 文件（含无引用 `user_conn_limit_7`）覆盖。
- 面板一键清扫入口：方案 ① 不需要，清理改为自动；未建面板入口是设计选择，不是遗留缺口。
- 本修不涉及 Gist、Trojan、kill-freeze K2 等 R20 未验证项，不改变其状态。

## 六、结论

- F-04 修复完成：存量孤儿 climiter 在 agent 下次启动/重载时由自愈剪除，`user_conn_limit_*` 永不剪，有引用 rule 与 traffic/rate limiter 不受影响。
- 回归：新测试 5/5（先红后绿）；`go-gost/x` 27 包、`go-gost` lifecycle、`go-backend`（除 3 处已与基线同签名核对的环境红外）、前端 `tsc && vite build` 全绿。
- 真机：Phase A 存量孤儿清除且过同步周期不复发；Phase B 判定表逐项通过；离场与 pristine 全量一致（gost md5 `d48f09e1…`、二进制 `aff338e3…`、DB 1/4/6/578/18）。
- 证据：`audit-out-132/r20/f04-logs/`（37 个日志 + 3 个 gost.json 快照）。
— R20-FIX-F04-REPORT END · 自证：本行之前正文字节数 11572 · END
