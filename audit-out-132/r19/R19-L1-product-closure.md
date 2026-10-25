# R19-L1 产品关闭验证报告（R18 四项）

- 任务：R19-L1——R18 四项关闭验证（只读审计，不改产品代码、不打 tag、不发版）。
- 基线（pin 死）：main @ `d66042c`（`d66042cc0b7ac011ddfad09f3f2709b90dafda2e`，`docs(r18): archive R18 audit records + integration report`）。读数树 `~/workspace/.r19-tree`，`git rev-parse HEAD` = `d66042c`；四路修复提交均为祖先（`git merge-base --is-ancestor` 全 YES）：F-01 `9d14d39`、F-02F-04 `7ab3a07`、F-03 `3a1eb2d`、INT `356d473`。产品 diff（`a0c18a0..d66042c`，不含 audit-out）：27 文件 +1819/−37，与 FIX-INT-REPORT 一致。
- 依据：树内 `audit-out-132/r18/R18-X-ledger.md`、`R18-FIX-PLAN.md`、`R18-FIX-F01-REPORT.md`、`R18-FIX-F02F04-REPORT.md`、`R18-FIX-F03-REPORT.md`、`R18-FIX-INT-REPORT.md`、`R18-X-verdicts.md`，或 `git show d66042c:<path>`。
- 护栏：N-01…N-37。测试复跑在 `/tmp/r19-l1-wt-green`（d66042c 的 `go-backend` + `go-gost` 副本）与 `/tmp/r19-l1-wt-red`（产品文件回退到 `a0c18a0` 的红重构副本）内进行；读数树与 `~/workspace/flvx-max` 产品代码零改动。
- 裁定口径：只认落盘原始日志 + 名单存档（本报告引用的每个结论后附日志文件名）；不继承 R18 结论；销账按探针对象本身对账，名称/对象/断言三对齐（见 §1–§3 对齐表）。
- 测试机：Lightsail Debian 12（公网 16.146.199.68 / tailnet 100.80.182.11，root）。独占窗口 1，本路为首个窗口持有者。锁：本路 2026-10-25T02:56:37Z 创建 `L1 2026-10-25T02:56:37Z`（当时无锁）；2026-10-25T03:26:47Z 删除。**锁在 02:56:52Z 被 L5 覆盖为 `L5 2026-10-25T02:56:52Z`（见 §5，L1-CAND-04）。**
- 部署说明（为真机腿所必需）：运行中的面板后端（`flmx-backend:r1gate-1.3.5`）与 agent（`aff338e3…`）均早于 R18 修复，字符串核对证实不含修复（面板 `已删除` 仅 1 处无关串，agent `startup config self-heal` 0 处）。按 R17D 剧本部署 d66042c 构建的修复版二进制后跑真机腿，完事全量恢复原始（见 §6）。

## 部署的修复版二进制（本地构建，go1.25.1）

| 二进制 | md5 | 修复特征串核对 |
|---|---|---|
| agent（`go-gost`，`CGO_ENABLED=0 go build -trimpath`） | `053b99fa0d706fb68f19198080de2a77` | 含 `startup config self-heal: removed duplicate %s(s) %v from persisted config`（1 处） |
| paneld（`go-backend`，`go build -trimpath ./cmd/paneld`） | `a824ee651222acd5985c36ddf6c5a060` | `已删除` 3 处，含 `已删除 0 条转发` |

部署：agent 换装 + R17C journal unit（`05449a73…`，`StandardOutput=journal`，否则自愈 Warn 无处可查）→ 后端镜像 `flmx-backend:r19l1`（`9d802e0680b9…`，`FROM flmx-backend:r1gate-1.3.5` + `COPY paneld`）→ compose 仅后端切 tag → `reset-failed` 后启动。部署日志 `R19-L1-deploy.log`：agent active、MainPID 742899、NRestarts=0、gost.json 仍 `d48f09e1…`、后端 healthy、`:6366` HTTP 200、部署后计数 1/4/6/578/18 不变。

## 裁定总表

| 典则编号 | 修复 | 结论 | 关键新证据 |
|---|---|---|---|
| R18-X-01 | F-01 | **CLOSE** | 沙箱 9/9 绿 + 7/2 红重构；真机成功删 + 离线删/重连对账（DeleteService 27/28_1_0） |
| R18-X-02 | F-02 | **CLOSE** | 17 处 upsert 在位（2 services + 15 类）；三启用门 0 diff；沙箱三形 + helper 绿、红 4 FAIL |
| R18-X-03 | F-03 + INT | **CLOSE** | 3 处 socket 去重 + dedupe.go 五类 Heal + program.go 五路接线在位；沙箱 23/23；真机毒化一次起成 + 4 告警、WS 三发一份、SIGHUP 自愈 |
| R18-X-04 | F-04 | **CLOSE** | 15 类 create 各 1 处 upsert 在位；沙箱 15 子测绿、红 FAIL；其余与 X-02 同据 |

---

## §1 R18-X-01（F-01）：删隧道 gost 分支中途失败回执披露已删转发 — CLOSE

### 1.1 对齐表（名称/对象/断言）

| 名称 | 对象（d66042c） | 断言 | 新证据 |
|---|---|---|---|
| 汇总格式 | `go-backend/internal/http/handler/tunnel_delete_resolution.go:736-753`（`formatForwardDeleteLabel` `:736`、`formatDeletedForwardsSummary` `:745`） | 恒含已删数；>0 附 `名(#ID)`；0 条为 `已删除 0 条转发` | 绿测 `TestR18F01DeletedForwardsSummaryFormat` PASS（`R19-L1-f01-green.log`） |
| 离线分支删库失败 | 同文件 `:805` | 回执含已删汇总 + 失败转发名 + `数据面未清理（节点离线，待重连对账）` | 绿测 `TestR18F01OfflineBranchDbDeleteFailureDisclosesDeleted` PASS；真机离线腿 §1.3 |
| 数据面硬失败 | 同文件 `:811` | 回执含已删数与标识 + 失败转发名 | 绿测 `TestR18F01MidLoopDataPlaneFailureDisclosesDeleted`（N=2/3）与 `TestR18F01LastForwardDataPlaneFailureDisclosesDeleted`（N=3/3）PASS |
| 删库失败 | 同文件 `:816` | 回执含已删汇总 + `数据面服务已清理、数据库记录仍保留` | 绿测 `TestR18F01MidLoopDbDeleteFailureDisclosesDeleted` PASS |
| 已删追踪 | 同文件 `:794`（`deleted` 仅在「数据面 + 删库」两步成功后追加） | 当前条失败不计入已删；已删项不回滚 | 绿测首条失败零已删 `TestR18F01FirstForwardDataPlaneFailureDisclosesZeroDeleted` PASS |
| 成功/全删路径不变 | 同文件控制流未动；调用方 `tunnelDelete`/`tunnelBatchDelete`/`userDelete` 原样透传 `Err(-2)`/`failures[].reason` | 成功路径 code 0；批量 reason 透传 | 绿测 `TestR18F01SuccessPathUnchanged`、`TestR18F01BatchDeleteFailureReasonDisclosesDeleted`、`TestR18F01UserDeletePathDisclosesDeleted`（scope `用户未删除`）PASS |

### 1.2 沙箱重跑（先红后绿）

- 绿：`R19-L1-f01-green.log`——`go test ./internal/http/handler/ -run 'TestR18F01' -count=1`，**9/9 PASS，0 FAIL**，包 ok 3.7s（`-run` 过滤；R18 报告的 327.5s 为全包时间，不可直接比）。
- 红（本轮重构）：`R19-L1-f01-red.log`——同一测试集在「仅三处披露返回还原为 `a0c18a0` 原文、helpers 与 `deleted` 追踪保留」的变体上，**7 FAIL / 2 PASS**（通过：汇总格式、成功路径）。失败样本逐字命中原缺陷：`msg="转发 r18f01-b 数据面清理失败，隧道未删除: simulated real failure: permission denied"`，零披露。
- 红证据缺口（O-L1-1，不阻塞关闭）：树内**无 F-01 红日志落盘**；F-01 报告称先红 `6 FAIL / 1 PASS`，但该数字按字面无法复现——纯 `a0c18a0` 产品文件 + 新测试文件根本无法编译（`R19-L1-f01-red-naive-buildfailed.log`：`r18_f01_tunnel_delete_disclosure_test.go:170: undefined: formatDeletedForwardsSummary`，因汇总格式测试直调新 helper）。本轮 7/2 重构是可复现的最近似红；关闭依据为本轮绿测 9/9 + 红重构 + 真机腿，不依赖报告的 6/1 数字。

### 1.3 真机腿（部署的修复版 paneld `a824ee65…` + agent `053b99fa…`）

- 成功路径：新建隧道 `r19l1-t1`（id 5）+ 转发 25（`r19l1-f1a`，:3287）、26（`r19l1-f1b`，:6674），agent 服务数 12→16（新增 `25_1_0_tcp/udp`、`26_1_0_tcp/udp`，`R19-L1-gost-after-fwd.json`）→ `POST /api/v1/tunnel/delete {"id":5}` 回 `{"code":0,"msg":"操作成功"}` → 计数 1/4/6/578/18，agent 服务回到 12 个原始名（`R19-L1-gost-after-del.json`）。
- 离线删 + 重连对账：新建隧道 `r19l1-t2`（id 6）+ 转发 27/28（:8824/:9092），agent 服务 16 → 停 agent（initiator=L1，03:15:34Z，`R19-L1-f01-offline-stop.log`）→ 删隧道回 `code 0`（0.97s）→ **agent 停机期间 DB 行已删（4/6），gost.json 仍留 4 条孤儿服务 `27_1_0_*`/`28_1_0_*`**（`R19-L1-gost-offline-del.json`，即离线分支「数据面未清理、待重连对账」语义）→ `reset-failed` 后启动 agent（PID 743803，NRestarts=0）→ 03:16:21 面板下发 `DeleteService`（`27_1_0_tcp/udp`、`28_1_0_tcp/udp`）→ :8824/:9092 监听消失、服务回到 12 个原始名、计数 1/4/6/578/18（`R19-L1-f01-offline-restart.log`、`R19-L1-f01-offline-evidence.log`、`R19-L1-gost-offline-recon.json`）。
- 三处硬失败回执的实测在沙箱完成（§1.2；httptest 伪装远端 + 删表模拟删库硬失败）；真机腿按任务书为「实删/离线重连对账」，已覆盖。

**裁定：R18-X-01 CLOSE。**

---

## §2 R18-X-02 / R18-X-04（F-02 + F-04）：x/api 17 类 create 按名去重 — CLOSE / CLOSE

### 2.1 对齐表

| 名称 | 对象（d66042c） | 断言 | 新证据 |
|---|---|---|---|
| 泛型 helper | `go-gost/x/api/config_upsert.go:16-38`（`upsertNamed`：同名替换首份、清其余副本、无则追加；`TrimSpace` 比较；nil 项保留） | 17 处共用同一口径 | 代码在位；绿测 `TestR18F02UpsertNamed` PASS |
| services 两处 | `config_service.go:87`（`createService`）、`:176`（`createServices` 第 4 阶段） | 已在配置中的同名项不再重复落盘 | 绿测三形 PASS（§2.2） |
| 其余 15 类各 1 处 | admission `:62`、auther `:61`、bypass `:62`、chain `:67`、conn_limiter `:61`、hop `:67`、hosts `:62`、ingress `:62`、limiter `:62`、observer `:62`、rate_limiter `:62`、recorder `:61`、resolver `:66`、router `:62`、sd `:62` | 同上 | 绿测 15 子测 PASS（§2.2） |
| `&ps.config` 别名修复 | `config_service.go:174-176`（逐项 `cfgCopy := ps.config` 再 `upsertNamed(..., &cfgCopy, ...)`） | 批量多服务不再全指向最后一项 | 全仓 grep `&ps.config` 仅剩注释提及，代码引用 0 处 |
| 残留 append | `x/api` 内剩余 17 处 `append(c.…)` | 全部位于各 `delete*` 过滤重建循环，非 create 盲 append | 逐行核对：admission `:176`、auther `:174`、bypass `:176`、chain `:186`、conn_limiter `:175`、hop `:185`、hosts `:176`、ingress `:176`、limiter `:176`、observer `:176`、rate_limiter `:176`、recorder `:175`、resolver `:184`、router `:176`、sd `:176`、service `:414`（`deleteService`）、`:488`（`deleteServices`），与 F-02F-04 报告 §5 逐行一致 |
| 三启用门不动 | `-api`（`go-gost/main.go:99` flag）、`GOST_API`（`x/config/parsing/parser/parser.go:77`）、配置文件 `api:` 段 | 0 行 diff；管理 API 仍不可达 | `git diff a0c18a0..d66042c -- go-gost/main.go go-gost/x/api/api.go go-gost/x/api/config.go go-gost/x/config/parsing/parser/parser.go install.sh panel_install.sh` 为空；部署为裸启动、面板只走 WebSocket（V-03 口径未变） |

### 2.2 沙箱重跑（先红后绿）

- 绿：`R19-L1-f02f04-green.log`——`go test ./api/ -run 'TestR18F02|TestR18F04' -count=1`，**全 PASS**：`TestR18F02CreateServiceDedupesPausedRebuild`、`TestR18F02CreateServicesBatchDedupesExistingConfigEntry`、`TestR18F02CreateServiceDedupesAlreadyPoisonedConfig`、`TestR18F02UpsertNamed`、`TestR18F04CreateDedupesExistingConfigEntry`（**15/15 子测**，grep 计数 15 行），包 ok 0.138s。
- 红：`R19-L1-f02f04-red.log`——16 个 `config_*.go` 回退到 `a0c18a0`（helper 与测试保留），**4 FAIL**（三形 F-02 + F-04），helper PASS；与归档红日志 `R18-FIX-F02F04-red.log`（同为 4 FAIL）一致。
- 真机：管理 API 三启用门全关、入口不可达，真机腿按设计不适用（N/A）；x/api 去重由沙箱 gin 直挂 handler 验证；面板→agent 的 WebSocket 下发走 x/socket，见 §3。
- 已知残余（非新立，见 F-02F-04 报告 §6 与 INT §6.4，不阻塞关闭）：批内同名两项 `[同名, 同名]` 的 HTTP 端到端形未立（第 2 阶段注册即失败回滚、到不了第 4 阶段）；F-04 每类一形只断言内存配置，落盘逐类断言只在 F-02 三形做（同为 `OnUpdate → persist()` 路径）。

**裁定：R18-X-02 CLOSE；R18-X-04 CLOSE。**

---

## §3 R18-X-03（F-03 + INT 裁定）：x/socket 去重 + 启动自愈扩展 + RLimiters — CLOSE

### 3.1 对齐表

| 名称 | 对象（d66042c） | 断言 | 新证据 |
|---|---|---|---|
| socket 三处去重 | `go-gost/x/socket/chain.go:34-60`（`createChain`）、`limiter.go:28-53`（`createLimiter`）、`limiter.go:139-164`（`createConnLimiter`） | 同名替换（`TrimSpace` 比较）、多余副本清除、nil 项 guard | 绿测三形 PASS（§3.2）；真机 WS 腿 §3.3 |
| 自愈函数 | `go-gost/x/config/dedupe.go`：`HealPersistedServices :12` / `DeduplicateServices :36`、`HealPersistedChains :74` / `DeduplicateChains :88`、`HealPersistedLimiters :100` / `DeduplicateLimiters :114`、`HealPersistedCLimiters :126` / `DeduplicateCLimiters :140`、`HealPersistedRLimiters :159` / `DeduplicateRLimiters :173`，共用 `dedupeNamed :190` | 五类同口径（first-wins、TrimSpace、nil 保留；干净不写盘） | 绿测 6 项 Deduplicate + 4 项 Heal PASS；真机毒化/SIGHUP 腿 §3.3 |
| 自愈接线 | `go-gost/program.go:330-351`（`healPersistedConfig` 依次调五类 Heal，`:343-347`，按类记 Warn；落盘失败只告警不阻塞） | 启动与 SIGHUP 重载同一路径 | 调用点 `Start :76-78` 与 `reloadConfig :303-305` 在 `loader.Load` 之前；真机两腿实证 |
| RLimiters 进自愈（INT 裁定） | `dedupe.go:159-189`（`DeduplicateRLimiters`/`HealPersistedRLimiters`）+ `program.go:347`（`heal("rate limiter", …)`） | 毒化 RLimiters 同样自愈，不漏第五类 | 绿测 `TestDeduplicateRLimitersRemovesDuplicates`、`TestHealPersistedRLimiters` PASS；真机毒化腿含 RLimiters（§3.3） |
| socket 残留 append | `chain.go` / `limiter.go` / `service.go` | create 路径无盲 append；剩余为 `update*` 未找到追加（`chain.go:92`、`limiter.go:82/:187`，文档化行为）与 delete 过滤重建（`chain.go:113`、`limiter.go:103/:206`）及 `kept` 重建循环 | 全仓 grep 核对 |

### 3.2 沙箱重跑（先红后绿）

- 绿：`R19-L1-f03-green.log`——`go test ./socket/ ./config/ ./config/loader/ -run '<23 个测试名>' -count=1`，**23/23 PASS，0 FAIL**：F-03 11 项（socket 三形 + config 六项 + loader 两项）+ RLimiters 2 项 + R17 回归 10 项（`TestDeduplicateServices*`×2、`TestPersistNow*`×2、`TestHealPersistedServices*`×2、`TestCreateServicesDedupes*`×2、`TestLoadPoisonedConfigFailsWithDuplicate`、`TestLoadHealedConfigSucceeds`；分包 socket 5 / config 14 / loader 4）。
- 红：`R19-L1-f03-red.log`——`chain.go`、`limiter.go`、`dedupe.go` 回退到 `a0c18a0`，**socket 3 FAIL + `x/config`、`x/config/loader` 编译失败**（函数未定义），与归档红日志 `R18-FIX-F03-red.log` 一致。
- 毒化形状预验证：沙箱内以临时测试（用后删除）确认本轮真机毒化文件的愈合形（1 链 + 1 限流器 + 1 连接限流器 + 1 rate limiter，`"$ 1"`）可经 `loader.Load` 成功注册，避免真机误判。

### 3.3 真机腿（部署的修复版 agent `053b99fa…`）

- 毒化文件一次起成：毒化 `gost.json`（md5 `c04a0095…`：services 12、chains 2、limiters 2、climiters 3、rlimiters 2）→ `reset-failed` 后启动，**一次起成**，active/running、MainPID 744358、**NRestarts=0** → 愈合后 services 12、chains 1、limiters 1、climiters 2、rlimiters 1（`R19-L1-f03-poison-start.log`）→ journal 4 条 Warn（03:17:57）：
  - `removed duplicate chain(s) [r19l1-chain]`
  - `removed duplicate limiter(s) [1]`
  - `removed duplicate conn limiter(s) [rule_conn_limit_2]`
  - `removed duplicate rate limiter(s) [r19l1-rlimiter]`
  
  零启动错误。
- 面板 WebSocket 重复下发落盘一份：新建转发 29（`r19l1-f3a`，maxConn=5）→ agent 收到 `AddCLimiters rule_conn_limit_29 ["$ 5"]`，落盘 **1 份**；`forward/update` maxConn 5→7 → 面板再次下发同名 `AddCLimiters`，agent 以 AlreadyExists 改发 `UpdateCLimiters`，落盘仍 **1 份**（`["$ 7"]`）；再改回 5 → 第三次 `AddCLimiters`，落盘仍 **1 份**（`["$ 5"]`）。journal 三次 `AddCLimiters` 在位（`R19-L1-f03-ws-journal.log`，`R19-L1-gost-ws1/2/3.json`）。机制注记：重复下发经注册表守卫走 Update 兜底，socket upsert 本体由单元测试（漂移形）覆盖；WS 腿验证的是端到端落盘不变量。链经 WS 不可达：单节点无 `AddChains` 触发面（仅多节点链式隧道），记 N/A；链去重由 socket 单元 + 毒化启动腿覆盖。
- SIGHUP 重载自愈：运行中重毒化 4 类（chains/limiters/climiters/rlimiters 各添 1 份）→ `kill -HUP 744358` → **PID 不变（744358）、NRestarts=0、active** → 03:20:05 新出 4 条 Warn（conn limiter 为 `[rule_conn_limit_29]`）→ 愈合后 chains 1、limiters 1、climiters 3、rlimiters 1、services 14 → 监听 `:8618`（测试转发）与 `:13101`（原始）仍在（`R19-L1-f03-sighup.log`）。

**裁定：R18-X-03 CLOSE（含 INT 的 RLimiters 进自愈裁定）。**

---

## §4 真机腿总览与污染事件

| 腿 | 时间（UTC） | 结果 | 证据 |
|---|---|---|---|
| F-01 成功删 | 03:13–03:14 | PASS（code 0，服务 16→12） | §1.3、`R19-L1-gost-after-fwd.json` / `after-del.json` |
| F-01 离线删/重连对账 | 03:15–03:16 | PASS（离线删 DB 行、孤儿残留、重连 DeleteService 清理） | §1.3、`R19-L1-f01-offline-*.log` |
| F-03 毒化一次起成 | 03:17:57 | PASS（NRestarts=0，4 告警） | §3.3、`R19-L1-f03-poison-start.log` |
| F-03 WS 重复下发 | 03:18:32–03:19:00 | PASS（三发一份） | §3.3、`R19-L1-f03-ws-journal.log` |
| F-03 SIGHUP 自愈 | 03:20:05 | PASS（PID 不变，4 告警） | §3.3、`R19-L1-f03-sighup.log` |
| t3 测试隧道清理删 | 03:20:21 | **受污染**（见下，终态收敛正确） | `R19-L1-f01-offline-evidence.log`、`R19-L1-restart-forensics*.log` |

### 污染：L5 在窗口 1 内未持锁上机（R18-PROC-01 复发）

- 02:56:37Z 本路建锁 `L1 2026-10-25T02:56:37Z`（建锁时无锁）；**02:56:52Z 锁文件被覆盖为 `L5 2026-10-25T02:56:52Z`**（03:22:44Z 读取时发现）。L5 的抢锁未做原子检查，15 秒内覆盖。
- L5 于 03:18–03:20 在机上布署 `/root/r19/`（`R19-L5-deploy.sh`、`R19-L5-fastpoll.sh`、`R19-L5-leg4-*.sh`、`R19-L5-killwatch.py`、`R19-L5-killfreeze.sh`、`R19-L5-restore.sh`、`stage.tgz`、`flux_agent-r19d`——其 agent md5 `053b99fa…` 与本路部署的修复版 agent **逐字节相同**，同为 d66042c 构建，故二进制层面无分歧）。
- L5 两次 agent 重启：03:20:20 停/起（PID 745041）、03:21:08 停/起（PID 745326），后又有 03:21:57 停/起（PID 745626）；运行进程 `python3 - /root/r19 A2`（03:21）→ `bash /root/r19/R19-L5-fastpoll.sh A3` + `python3 - /root/r19 A3`（03:21，PID 745031/745623）。fastpoll 为「重启 agent 后 50ms 轮询 gost.json 45 秒、猎取瞬态重复窗口」。
- 直接影响仅一腿：03:20:21 本路删测试隧道 t3（id 7）时 agent 正处 L5 重启窗口，面板日志证实走了离线分支——`11:20:21 tunnel delete: node offline, forward marked for reconnect cleanup tunnel_id=7 forward_id=29 err=S-04: 节点离线，转发未完全删除，离线节点: [1]`；随后重连清理于 03:20:36 下发 `DeleteService 29_1_0`，终态收敛正确（服务回到 12 个原始名、计数 1/4/6/578/18）。该腿不作为「在线删」证据（在线删已由 t1 腿干净证实）；它意外地二次验证了离线分支端到端行为。03:20:26 的 `r17-addservice-guard` 行（`update-fallback 7_1_0` → `sent-failed bind: address already in use` → `self-occupied-rebind sent-ok`）为 L5 重启后的面板重推行为，R17-A 守卫按设计工作，终态 `7_1_0` 在位。
- 其余腿均结束于 03:20:05（含）之前，不受污染。03:23:37 复核 `NO-L5-PROCS`、agent PID 稳定后，本路在静默窗口执行恢复（03:24:24Z 完成）。
- 处置见 L1-CAND-04。

---

## §5 新候选立案与观察

### 候选（编号 L1-CAND-01…）

| 编号 | 内容 | 级别/来源 |
|---|---|---|
| L1-CAND-01 | F-01 作用域外缺口：`cleanupForwardsDataPlane` **nft 分支**删库失败仍返裸 `err`，未披露已删数（F-01 报告 §7.2、INT §6.2 待 R19 裁定） | 产品 P3 |
| L1-CAND-02 | F-01 作用域外缺口：`deleteTunnelByID` 失败时「转发已全删」未披露（同上） | 产品 P3 |
| L1-CAND-03 | 真机实查：原始 `gost.json` 即含孤儿连接限流器 `rule_conn_limit_12`（`["$ 7"]`），对应 forward 12 已不存在（现存 forward id 为 1/2/3/4/6/7）；疑似删转发未清 climiter。本轮未动它，恢复后仍在。需核对删转发路径是否清理 climiter | 产品 P3（待核实） |
| L1-CAND-04 | **流程 P1（R18-PROC-01 复发）**：L5 在独占窗口 1 内未持锁上机——锁文件建锁 15 秒后被覆盖、agent 被重启三次、fastpoll 在本路窗口内运行，污染 t3 清理腿。建议锁抢占改为原子操作（`mkdir` 式）并由协调者重申排期纪律；L5 受影响腿（fastpoll A2/A3 窗口内的重启归属）需自行复核 | 流程 P1（不计产品收敛判据） |

### 观察（不立案）

- O-L1-1：F-01 红日志未归档、报告的 `6 FAIL / 1 PASS` 按字面不可复现（见 §1.2）；关闭不依赖该数字。
- O-L1-2：前端容器 `vite-frontend` 在本路侦察时已为 `Up 53 minutes (unhealthy)`，恢复后仍 `unhealthy`；后端 healthy、`:6366` HTTP 200。既存状态，非本轮引入。
- O-L1-3：任务书「49 表」与实测不符——起止两次 SQLite 普查均为 **51 表（含 `sqlite_sequence`）/ 50 表（不含）**，表名清单差集为 ∅；R18 对账亦为「表 50」。任务书数字滞后，非恢复失败。
- O-L1-4：`node_traffic` 行数 10→11，为面板流量记账自然增长，非测试残留。
- O-L1-5：既有测试 `TestR1B3TunnelDeleteOnlineRealFailureAborts`（`r1_b3_tunnel_delete_offline_test.go:130`）全文件 0 次设置 `FLMX_FETCH_ALLOW_PRIVATE_NETWORKS`（F-01 报告 §7.4 的观察属实，F-01 测试内 3 处设置）；其远端调用在传输层即被 SSRF 拦截，并未真正覆盖「远端明确回失败」路径。测试卫生项，可随下一批补 env。
- O-L1-6：`updateChain` / `updateLimiter` / `updateConnLimiter` 的 nil 项 panic 仍在（F-03 报告 §6.2，已登记）；主包 `healPersistedConfig` 接线无可运行测试（`main.go:init()` 的 `flag.Parse()` 拒 `go test` 参数，R17-A 同病）——本次真机启动 + SIGHUP 两腿为该接线的直接证据。

---

## §6 全量恢复与对账（initiator=L1，03:24:24Z 完成）

恢复脚本 `R19-L1-restore.log`：停 agent → 恢复原始 agent 二进制、原始 unit、原始 gost.json、原始 compose → 后端切回 `r1gate-1.3.5` → `reset-failed` 后启动 → 删除 `flmx-backend:r19l1` 镜像。

| 项目 | 恢复前（原始） | 恢复后 | 对账 |
|---|---|---|---|
| agent 二进制 | `aff338e3676bc56d0f3986a1a62f8fdd` | `aff338e3676bc56d0f3986a1a62f8fdd` | 一致 |
| agent unit | `a12afd9c36fba7b76287210efaa199cb`（`StandardOutput=null`） | 同左 | 一致 |
| gost.json | `d48f09e132ebe99fe97a619dd72d42d4`（328 行，services 12 / limiters 1 / climiters 2） | 同左（328 行，同上） | 一致 |
| compose | `3afbac1115e4f69a991448767b89d24e` | 同左 | 一致 |
| 后端镜像 | `flmx-backend:r1gate-1.3.5`（`620be8ddd1ba`，healthy） | 同左（healthy）；`r19l1` 镜像已删 | 一致 |
| 前端镜像 | `flmx-frontend:r1gate-1.3.5`（`a58da904428a`） | 同左 | 一致 |
| agent 状态 | active、NRestarts=0、MainPID 731512 | active、NRestarts=0、MainPID 746627 | 运行态一致（PID 为重启后新值） |
| 面板 | `:6366` HTTP 200 | `:6366` HTTP 200 | 一致 |
| 计数（API） | node 1 / tunnel 4 / forward 6 / inbound 578 / subscription 18 | 同左（恢复后两次复核一致） | 一致 |
| 计数（SQLite） | 同左（`R19-L1-sqlite-census.log` START） | 同左（FINAL）；`r19l1` 命名行 0/0 | 一致 |
| 表 | 51（含 `sqlite_sequence`）/ 50（不含），清单差集 ∅ | 同左 | 一致 |
| 修复特征串 | paneld `已删除` 1 处；agent `self-heal` 0 处 | 同左（`R19-L1-final-checks.log`） | 一致（已回到修复前） |
| 磁盘 | 3.6G 可用 | 3.2G 可用 | 差值说明：构建缓存可回收 420.3MB + `/root/r19`（L5，72M）+ `/root/r19-l1`（本路备份，180M，含原始二进制备份，仿 R17D 留存）；未删 L5 文件；磁盘 84% 未达阈值 |

原始备份留存于机上 `/root/r19-l1/`（`flux_agent.pristine`、`gost.json.pristine`、`flux_agent.service.pristine`、`docker-compose.yml.pristine`、`paneld.r17d`），与 `/root/r17d` 同例。锁文件已删除（2026-10-25T03:26:47Z；删除时文件内容为 L5 的戳，见 §4）。

## 证据清单（`audit-out-132/r19/`）

- 沙箱：`R19-L1-f01-green.log`（9/9）、`R19-L1-f01-red.log`（7/2 重构）、`R19-L1-f01-red-naive-buildfailed.log`、`R19-L1-f02f04-green.log`、`R19-L1-f02f04-red.log`、`R19-L1-f03-green.log`（23/23）、`R19-L1-f03-red.log`。
- 真机原始日志：`R19-L1-backup.log`、`R19-L1-deploy.log`、`R19-L1-f01-offline-stop.log`、`R19-L1-f01-offline-restart.log`、`R19-L1-f01-offline-evidence.log`、`R19-L1-f03-poison-start.log`、`R19-L1-f03-ws-journal.log`、`R19-L1-f03-sighup.log`、`R19-L1-t3del-recheck.log`、`R19-L1-restore.log`、`R19-L1-final-checks.log`、`R19-L1-sqlite-census.log`，及 `R19-L1-recon1.log`、`R19-L1-dbcounts1.log`、`R19-L1-bincheck.log`、`R19-L1-paneld-strings.log`、`R19-L1-sqlite1.log`、`R19-L1-envcheck.log`、`R19-L1-unitcheck.log`、`R19-L1-prodservice.log`。
- 污染取证：`R19-L1-restart-forensics.log`、`R19-L1-restart-forensics2.log`、`R19-L1-restart-forensics3.log`、`R19-L1-foreign-lane.log`、`R19-L1-foreign-state.log`、`R19-L1-l5scripts.log`、`R19-L1-quiesce1.log`、`R19-L1-r17d-deploy-ref.sh`。
- 名单存档：`R19-L1-api-*-list.json`（恢复后终态）、`R19-L1-gost-*.json`（pristine / after-fwd / after-del / t2 / offline-del / offline-recon / after-t3del / ws1 / ws2 / ws3 / now）。
- 凭据说明：面板凭据仅读自 `~/workspace/w10-run/panel-cred.txt`（600），未入本报告与任何日志；token 仅存 `/tmp/r19-l1-token`（600）。

## 结论

R18 四项（X-01、X-02、X-03、X-04）**全部 CLOSE**，无 OPEN 项；新候选立案 4 项（L1-CAND-01…04，其中产品 3 项、流程 1 项），观察 6 项。测试机已全量恢复原始并对账一致。
— R19-L1-product-closure END · 自证：本行之前正文字节数 26248 · END
