# R18 修复集成报告（FIX-INT-REPORT）

- 基线：main @ `a0c18a0`（R17 修复 fix-r17 已合入）
- 集成树：`fix-r18` @ `356d473`（三路零冲突合入 + 集成补丁 1 提交），未合 main、未打 tag、未发版、未改版本字面量
- 依据：`audit-out-132/r18/R18-FIX-PLAN.md`（F-01…F-04 + 流程项 P-01）、`R18-X-ledger.md`、`R18-X-verdicts.md`
- 产品 diff（相对 a0c18a0，不含 audit-out）：27 文件 +1819/−37

## 1. 三路合入

| 路 | 分支 @ 提交 | 内容 | 合入 |
|---|---|---|---|
| F-01 | fix-r18-f01 @ `9d14d39` | 删隧道 gost 分支中途失败回执披露已删转发 | merge `56b56cf`，零冲突 |
| F-02+F-04 | fix-r18-f02f04 @ `7ab3a07` | x/api 17 类 create 按名去重（2 services + 15 类） | merge `2bc569e`，零冲突 |
| F-03 | fix-r18-f03 @ `3a1eb2d` | x/socket 三处盲 append 去重 + 自愈扩展到 chains/limiters/climiters | merge `95c10b5`，零冲突 |
| INT | — @ `356d473` | RLimiters 进自愈（见 §2） | 直接提交于 fix-r18 |

三路 worktree 各自独立、基线同为 a0c18a0；产品文件无重叠（F-01 仅 go-backend、F-02F-04 仅 x/api、F-03 仅 x/socket + x/config + program.go、INT 仅 x/config + program.go），合入无冲突、无手工裁决。

## 2. 集成裁定：RLimiters 进自愈（R18-X-03 跟进）

F-03 报告 §6 请主控裁定：`x/socket` 内无 rate limiter create 路径，但 `loader.Load` 会加载 `cfg.RLimiters`（`loader.go:218-221`），第二个同名项在 `RateLimiterRegistry.Register` 处报 `registry: duplicate object`、agent 退出——与服务/链/限流器毒化同型。**裁定：纳入。**理由：同一毒化向量、同款约 20 行工作；F-04 已在 x/api 侧给 rate_limiter create 按名去重，只缺自愈一侧，漏掉它自愈即不完整。

改动（`356d473`，3 文件 +108/−4）：
- `go-gost/x/config/dedupe.go`：新增 `DeduplicateRLimiters` / `HealPersistedRLimiters`（共用 `dedupeNamed` helper，口径与三类一致：保留首次出现、TrimSpace 比较、nil 项保留）。
- `go-gost/program.go`：`healPersistedConfig` 增调 `heal("rate limiter", config.HealPersistedRLimiters)`；注释「四类/最多 4 次原子写」订正为「五类/最多 5 次」。
- `go-gost/x/config/r18_fix_f03_test.go`：新增 `TestDeduplicateRLimitersRemovesDuplicates` 与 `TestHealPersistedRLimiters`（落盘再读核对 2 份）。

先红后绿：红——新测试在 F-03 原 tip（`3a1eb2d`）上编译失败（`undefined: DeduplicateRLimiters / HealPersistedRLimiters`，临时 detached worktree 实测）；绿——集成树上两测试 PASS（`go test ./config/ -run 'TestDeduplicateRLimiters|TestHealPersistedRLimiters'`）。

## 3. 各路改动摘要（详见各路报告）

### F-01（R18-X-01，`R18-FIX-F01-REPORT.md`，9049 字节）
`tunnel_delete_resolution.go`：gost 循环新增 `deleted` 追踪（只记录已完成「数据面 + 删库」两步的转发）；新增 `formatForwardDeleteLabel`（`名(#ID)`）与 `formatDeletedForwardsSummary`（恒含已删数，0 条时为 `已删除 0 条转发`）；三处硬失败返回全部补披露——数据面失败、删库失败（注明「数据面服务已清理、数据库记录仍保留」）、离线分支删库失败（注明「数据面未清理，待重连对账」）。已删项不回滚；离线放行（X-P1-1）与在线硬失败中止的控制流未动；`scope` 与 `offlineTolerant` 区分原样保留。调用链无改动：错误文本原样进回执（单删 `Err(-2)`、批量 `appendBatchFailure` Reason 透传）。实测回执例：`{"code":-2,"msg":"转发 r18f01-b(#2) 数据面清理失败，隧道未删除：simulated real failure: permission denied；已删除 1 条转发：r18f01-a(#1)"}`。新测试 9 项全 PASS（中途 N=2/3、最后一条 N=3/3、首条零已删、删库腿中途失败、批量 reason、成功路径不变、离线分支删库失败、删用户路径 scope「用户」）。

### F-02+F-04（R18-X-02/X-04，`R18-FIX-F02F04-REPORT.md`，11406 字节）
新增泛型 helper `x/api/config_upsert.go:16-38` `upsertNamed`（同名替换首份、清除其余副本、无则追加；nil 项保留；名 TrimSpace 后比较）。F-02：`config_service.go` `createService :87` 与 `createServices` 第 4 阶段 `:176`；附带修掉同闭包内 `&ps.config` 循环变量别名问题（批量多服务曾全指向最后一项）。F-04：其余 15 类 create 各 1 处（admission `:62`、auther `:61`、bypass `:62`、chain `:67`、conn_limiter `:61`、hop `:67`、hosts `:62`、ingress `:62`、limiter `:62`、observer `:62`、rate_limiter `:62`、recorder `:61`、resolver `:66`、router `:62`、sd `:62`）。registry 守卫、解析、注册逻辑均未动；三启用门 0 行 diff。先红后绿：F-02 三形（paused 重建、批内重复、毒化文件不再增份）+ helper 单元测试 + F-04 15 子测全部 PASS（19 个断言面）。修后全仓再搜索：`x/api` 剩余 17 处 `append(c.…)` 全部位于各 `delete*` 过滤重建循环，非盲 append，不改。

### F-03（R18-X-03，`R18-FIX-F03-REPORT.md`，15034 字节）
三处盲 append 改按名 upsert（同名替换）：`x/socket/chain.go:34-60`（createChain）、`limiter.go:28-53`（createLimiter）、`:139-164`（createConnLimiter）；口径与 R17-A 的 createServices 一致（TrimSpace 比较、配置里多余同名副本一并清除、nil 项 guard 原样保留不 panic）。启动自愈扩展评估结论：chains/limiters/climiters 三类全部可扩展、无结构性障碍（同一 gost.json、同一 PersistNow 路径、同一 program.go 接线点；loader.Load 在三类上同样于第二个同名项 Register 处报 `registry: duplicate object`）。新增 `DeduplicateChains/Limiters/CLimiters` + Heal（`x/config/dedupe.go:63-178`，共用 `dedupeNamed`）；`program.go:330-351` 的 `healPersistedConfig` 依次调四类 Heal、按类记 Warn（services 行文案与 R17-A 逐字一致）、落盘失败只告警不阻塞启动。先红后绿：11 个新测试全 PASS（三类各一形「同名重复下发后内存/落盘仅一份且为新内容」；毒化文件 `loader.Load` 失败点 pin 死为 `registry.ErrDup`，去重后加载成功、三类注册表各 1 项；干净配置不触发写）；R17 回归重跑 10 项全 PASS（毒化原件夹具 md5 核对在位）。

## 4. 集成全量回归（集成树 fix-r18 @ 356d473）

| 检查 | 结果 | 证据 |
|---|---|---|
| go-backend `go build ./...` / `go vet ./...` | exit 0 | — |
| go-backend `go test ./... -count=1` | 16 包 ok；唯一 FAIL 为已知沙箱环境红（见下） | `R18-FIX-backend-fulltest.log`（68 行） |
| go-gost `go build` / `go vet` / `go test ./... -count=1` | exit 0，1 包 ok，0 FAIL | `R18-FIX-gost-fulltest.log` |
| go-gost/x `go build` / `go vet` / `go test ./... -count=1` | exit 0，27 包 ok，0 FAIL | `R18-FIX-gostx-fulltest.log` |
| 前端 `tsc --noEmit` / `vite build` | 双 exit 0（本轮 vite-frontend diff 0 行；node_modules 以符号链接借用共享工作树，验后已删） | `R18-FIX-tsc.log` / `R18-FIX-vitebuild.log` |
| gofmt（6 个改动产品文件） | 干净 | — |

**产品失败 0。**唯一 FAIL：`go-backend/internal/health` 的 `TestValidateMonitorTCPTarget/公网域名放行`——沙箱假 DNS 把 `example.com` 解析到保留地址所致，与修复无关；F-01 路已在未改动的 main checkout（a99d68d）同命令复现，同签名（见 F-01 报告）。handler 包 ok 405.724s、repo 包 ok 128.326s、contract 包 ok 46.988s。

## 5. 流程：lane 间互斥排期（R18-PROC-01）

### 5.1 本次修复的执行纪律（已执行）
本次修复三路**全程未碰测试机**：全部验证均为本地单元/集成测试（gin test engine 直挂 handler、loader/dedupe 单元测试、httptest 伪装远端节点），真机腿一律留 R19 独占窗口。测试机零上线、零重启、零文件清理，故不存在 lane 间污染。

### 5.2 R19 起的执行纪律（写进本报告，供 R19 任务书引用）
1. **测试机实行 lane 间互斥排期**：协调者排期，独占窗口内其他 lane 不得上线面板、不得重启 agent、不得清理/替换测试机文件；窗口起止与 lane 归属写入各路报告首行。
2. **操作前核对三件**：面板二进制版本、agent 二进制版本（md5）、重启归属（本次重启由哪一路发起、为何重启）。
3. **日志中写明每次重启的发起方**：每次 agent/面板重启在日志首行记 `initiator=<lane>` 与原因；事后对账以重启发起方为准，不以时间推测。
4. **改期重跑项**：L5 items 2–5（VLESS 用户凭证腿、SS 直连腿、Trojan 最小对照、下游供数/1MB 批量腿）与 kill-freeze 改期到独占窗口重跑（R19 任务书事项，本次不代做）。

## 6. 未验证项（结转 R19）

1. **真机验证一律未做**（本次三路 + 集成均未碰测试机）：F-01 实删/离线重连对账、F-03 毒化文件一次起成 + journal 告警行、面板重复下发落盘一份、SIGHUP 重载自愈、面板 WebSocket 实下发链路——留 R19 独占窗口。
2. **F-01 nft 分支同类缺口未修**（作用域外）：nft 分支删库失败仍返裸 err；`deleteTunnelByID` 失败时「转发已全删」亦未披露——待 R19 裁定。
3. **F-01 附带观察**：既有测试 `TestR1B3TunnelDeleteOnlineRealFailureAborts` 未设 `FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true`，其远端调用在 federation 传输层即被 SSRF 拦截，并未真正走到伪装节点的 `success=false` 分支（仅因断言宽松而通过）——供 R19 考虑补 env。
4. **F-02F-04**：批内同名两项 `[同名, 同名]` 的 HTTP 端到端形未立（第 2 阶段 registry 注册即失败回滚、到不了第 4 阶段；第 4 阶段循环同名 upsert 语义只由 helper 单元测试断言）；F-04 每类一形只断言内存配置，落盘逐类断言只在 F-02 三形做（同为 `OnUpdate → persist()` 路径）。
5. **F-03**：`updateChain`/`updateLimiter`/`updateConnLimiter` 的 nil 项 panic 仍在（本次只给 create 路径加了 guard）；主包 `healPersistedConfig` 接线无可运行测试（`main.go:init()` 的 `flag.Parse()` 拒绝 `go test` 的 `-test.*` 参数，R17-A 同病；接线由读码核对 + 三类 Heal/loader 测试间接覆盖）。
6. **R17 报告 §10 剩余项继续结转**：远端/非 Debian 内核、浏览器 DOM 腿、订阅 1 流量计数器响应性。

## 7. 归档

R18 审计产物（`R18-00-baseline.md`、`R18-00-sentinel.md`、X-ledger、X-verdicts、FIX-PLAN、FINAL 四件套、L1–L5 报告与全部探针日志，共 91 文件）随本次集成提交进库；三路修复报告与红/绿/全量日志一并在位。`*.log` 受 `.gitignore` 的 `*.log` 通配规则影响，以 `git add -f` 逐文件显式入库（非 `git add -A`），否则归档将缺失 N-37 裁定依据的原始日志。

## 8. 结论

四项产品修复（F-01…F-04）全部完成、先红后绿、集成全量回归产品失败 0；流程项 P-01 的本次执行纪律已落实、R19 纪律已写入 §5.2。建议开 R19 重审，不发版。
— R18-FIX-INT-REPORT END · 自证：本行之前正文字节数 11210 · END
