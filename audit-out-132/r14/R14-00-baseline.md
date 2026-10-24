# R14-00 基线报告（1.4.8 全面审计 · 基线员，只读）

- **时间**：2026-10-25（Asia/Shanghai）
- **性质**：只读基线，未改任何产品代码。本轮写入仅：哨兵表 `R14-00-sentinel.md`（**第一动作先行落盘**，见 §4）+ 本报告 + 路由原料表 `R14-00-routes-raw.csv` + 数据表原料表 `R14-00-tables-raw.txt` + 基线量测日志 `R14-00-baseline-test.log` + health 隔离全量输出 `R14-00-health-iso-full.txt` + probe 夹具隔离日志 `R14-00-probe-fixture-isolation.log` + 跑测脚本 `run-baseline.sh`（均同目录）。跑测在快照树上进行；只读 worktree `~/workspace/.r14-tree` 全程保持 pristine（本员复核 `git status` 空）。
- **环境**：Go 1.25.1（`go version` 实测 go1.25.1 linux/amd64，日志首段；`PATH=$HOME/go/bin:$PATH`）、node v24.22.1。快照与 `TMPDIR`/`GOTMPDIR` 均指工作区大盘（`~/workspace/.r14-tmp`；`/tmp` 本轮实测 tmpfs 512M、0 已用，未满，但不以此为长跑路径）。快照取码：`git archive 7244fbb | tar -x -C ~/workspace/.r14-tmp/r14base`，抽查 `handler.go` sha256 与 `git show 7244fbb:` 逐字节一致（`c69564fa…`）。
- **全量结论纪律（N-37）**：本报告全部全量测试结论只认落盘日志文件 `audit-out-132/r14/R14-00-baseline-test.log`，逐项引用其行号；不采信任何回执转述数字。health 隔离的 `-count=3` 全量输出另存 `R14-00-health-iso-full.txt`（主日志内隔离段为 tail-40 的末次迭代，见 §3.1 说明）。
- **收敛定位（写死）**：R13 决胜轮已正式收敛（`r13/R13-FINAL-summary.md` §3：R12+R13 连续两轮归零；其后终局候选版审计执行、1.4.5/1.4.6/1.4.7/1.4.8 相继发布）。**R14 为 1.4.8 发布后的全面审计轮**（任务钉死），基线 main @ 7244fbb；任务未钉 R14 收敛判据，基线员不自造口径。

## 1. 基线身份（钉死）与 main 关系对账

| 项 | 值 | 证据 |
|---|---|---|
| 审计对象（钉死） | **`main` @ `7244fbb`** | 任务钉死；本员复核见下 |
| 完整提交号 | **`7244fbbaa769d8262405c5a13021f415e29169a7`**「merge(fix-connidle-level): enforce per-inbound idle timeout via exit-side userLevel」，2026-10-24 17:48:28 +0000 | `git rev-parse 7244fbb` / `git show -s` |
| 与 main 关系 | **本地 `refs/heads/main` = `refs/remotes/origin/main` = `7244fbb`**（本员实测，`git log -1 origin/main` 同值）；只读 worktree `~/workspace/.r14-tree` @ `7244fbb`（detached HEAD，`git status` 空）；共享工作树现处 `feat-latency-hint` @ `a99d68d` 且有未提交审计文件残留，**不得用作读数树** | 本员实测 |
| 树哈希 | `7244fbb^{tree}` = **`a39952f00929f6bc1d07c19532f86f85d98bd45c`** | 本员实测 |
| 最近发布 tag | **`1.4.8`**（`bd9f99d629aff7b7ad58f03830dd0e65abf8373e`，「release(1.4.8): sync literals to 1.4.8, connIdle guide, release notes」，2026-10-24 18:36:14 +0000） | `git rev-parse 1.4.8` |
| tag 与 main 关系 | **release 分支 = main + 1 提交**（`git log 7244fbb..bd9f99d` 仅该 1 提交；diff 5 文件：`audit-out-132/final/RELEASE-148-NOTES.md`（新增 61 行）、`doc/ai-skill.md`、`doc/guide/nodes.md`、`skills/flmx-api/SKILL.md`、`skills/flmx-api/package.json`——版本字面量与发版说明，**产品代码差集 = 0**）；故 main @ 7244fbb 的产品代码与已发布 1.4.8 同一。另注：1.4.5 tag（`544dc0a`）**不是** main 祖先（发布分支 lineage），S-52/S-53 关闭版本的核对以发布树内代码在位为准（见哨兵表口径条），不以祖先关系冒充 | 本员实测 |
| 取码方式 | `git archive 7244fbb \| tar -x -C ~/workspace/.r14-tmp/r14base/`（快照置工作区大盘，快照内 `BASELINE_HEAD.txt` = `7244fbb…`）；一切测试在此快照上做；快照文件 sha256 抽查与 `git show 7244fbb:` 逐字节一致（handler.go `c69564fa…`） | 跑测脚本 `run-baseline.sh` 首段 + 探针日志 |

基线增量构成（相对 R13 基线 `60a4392`「docs(r12-int): R12 fix report」）：

| 段 | 提交数 | 文件数 | 增减 |
|---|---|---|---|
| R13 段后至本轮 `60a4392..7244fbb`（本轮实测） | 39 | 87 | +7771/−179 |
| 其中产品文件（`go-backend`/`vite-frontend`/`go-gost`） | — | 66 | +6564/−175 |
| 自 1.4.4 起 `d03c2ab..7244fbb`（本轮实测，供 §6 认领） | 103 | 169 | +14592/−433 |

- 段内合并提交（本轮实测，`git log --merges`）：R13 修复 `8e055c4`/`099c318`、port-3342 Gist 侧 `625f487`/`ccd427d`/`7e5715a`、调优侧 `15443bf`/`92d4606`/`b65b801`、1.4.6 字面量并入 `09f1111`、测算修复 `8ba125e`、空闲超时 `b813b99`、延迟提示 `3d180ed`、延迟断链 `75ce035`、空闲超时修复 `7244fbb`（本轮基线 tip 本身）。

## 2. 路由 / 数据表普查（钉死值：202 = 197 + 5 / 49，与 R13 口径差集 = +5 Gist / 表 ∅）

### 2.1 路由：202 条，相对 R13 仅 +5 Gist；鉴权分布重数

- 提取方法（基线树实测，机械脚本 `/tmp/r14_census.py`）：`.r14-tree` `go-backend/internal/http/handler/handler.go` 的 `Register(mux)` 全部 `mux.HandleFunc` = **201 条**（字面量 200 条 + 经 `ghMirrorPath` 常量注册 1 条，常量 `gh_mirror.go:32 ghMirrorPath = "/api/v1/open_api/gh/"`，注册行 handler.go:303）+ `internal/http/router.go:13 mux.Handle("/system-info", …)` 1 条 = **202 条**。
- 构成：**`/api/v1` 197 条 + 非 API 面 5 条**（`/flow/test`、`/flow/config`、`/flow/upload`、`/error`、`/system-info`）。任务书括号猜测「/api/v1 201 + 非 API 1」**实测不成立**，以实测为准。
- 鉴权分布（对原料表逐行机械计数，分类器逐字复刻 `middleware/auth.go` 的 `shouldSkip` / `allowsOptionalAuth` / `requiresAdmin`，并对 197 条旧行逐行复现 R13 值——复现率 100%）：**ADMIN 135 / JWT 44 / PUBLIC 17 / OPTIONAL-AUTH 1 / 非 API 5**（合计 202；ADMIN 较 R13 +5，即新增 Gist 5 条全 ADMIN）。
- 方法守卫分布（逐 handler 函数体机械推导，`r.Method` 检查行逐字复核；197 条旧行与 R13 值逐行一致）：**POST only 131 / ANY（no method check in body）53 / GET only 13 / not(Get,Post) 1 / GET,HEAD 2 / GET,POST 1 / GET (WS upgrade) 1**（合计 202；POST only 较 R13 +5，即 Gist 5 条全 POST only——`gist_backup.go` 五 handler 均含 `r.Method != http.MethodPost`）。`method_guard` 列统一记允许方法集（R14 A2-4 订正：`open_api/sub/` 与 `open_api/gh/` 均为 GET,HEAD）。
- 与 R13 口径逐条对账：**R14−R13 = 5 条**（Gist，见下）；**R13−R14 = ∅**；公共路径集合顺序逐行一致；197 条旧行的 auth / method_guard / handler_func 三列与 R13 表逐行一致（分类器 + 守卫推导双重机械复现，非沿用）。
  - 新增 5 条（`handler.go:266-270`，全 `ADMIN (JWT + role=admin)` + `POST only`，`/api/v1/backup/` 前缀落 `requiresAdmin`）：`/api/v1/backup/gist/status`（`gistBackupStatus`）、`/api/v1/backup/gist/save`（`gistBackupSave`）、`/api/v1/backup/gist/upload`（`gistBackupUpload`）、`/api/v1/backup/gist/versions`（`gistBackupVersions`）、`/api/v1/backup/gist/restore`（`gistBackupRestore`）。
- **原料表勘误（非产品，转录修正）**：R13 表 7 条 federation `authPeer` 行 handler 列缺右括号（存为 `h.authPeer(h.federationConnect`），R14 写全字面量 `h.authPeer(h.federationConnect)`；三列其余值逐行一致。勘误只涉原料表转录，不涉产品代码与鉴权结论。
- 原料表：`R14-00-routes-raw.csv`（203 行含表头；路径集由基线树独立重建，鉴权/守卫两列由分类器与函数体推导机械复现 R13 值后独立生成）。

### 2.2 数据表：49 个模型，集合与顺序 diff = ∅

- 提取方法（基线树实测）：`.r14-tree` `go-backend/internal/store/repo/repository.go:327` 的 AutoMigrate 模型集 `models := []interface{}{…}` 逐项机械抽取 = **49 个模型**（48 个 `model.*` + 仓库内 `NodeSeq`）；与 R13 原料表逐项比对 → **集合 diff = ∅、顺序逐行一致**（模型集本体自 `60a4392` 至 `7244fbb` diff = 0 行；行号 326→327 仅因文件首部新增 `"math"` import 一行。`repository.go` 本轮 diff 的 4 处 hunks 均在 Export/Import/备份归一函数内，未触模型集）。
- **基线树（相对 R13 基线）新增表 0 张。**
- 原料表：`R14-00-tables-raw.txt`（49 行，逐名清单）。

**钉死值：路由 202（197+5；ADMIN 135/JWT 44/PUBLIC 17/OPTIONAL-AUTH 1/非 API 5）/ 表 49（与 R13 口径 diff 为路由 +5 Gist、表 ∅）。**

## 3. 全量测试（三 module 基线实跑，只认落盘日志）

日志：`audit-out-132/r14/R14-00-baseline-test.log`（385 行）。

| 面 | 命令 | 结果（日志行号） |
|---|---|---|
| go-backend build+vet | `go build ./... && go vet ./...` | ✅ exit 0（日志 :11 / :14） |
| go-gost build+vet | 同上 | ✅ exit 0（日志 :17 / :20） |
| go-gost/x build+vet | 同上 | ✅ exit 0（日志 :23 / :26） |
| go-gost test | `go test ./... -timeout 30m -count=1` | ✅ exit 0（日志 :31；1 包 ok / 1 包无测试文件） |
| go-gost/x test | 同上 | ✅ exit 0（日志 :240；**27 包 ok / 179 包无测试文件 / 0 FAIL**，与 R10/R12/R13 计数逐字一致） |
| go-backend test（全量） | `go test ./... -timeout 90m -count=1` | ⚠️ exit 1（日志 :310）：**16 包 ok / 4 包无测试文件 / 1 包 FAIL**（handler 371.872s、repo 113.448s、contract 48.205s 均 ok），失败用例恰 1 条，定性见 §3.1 |
| 前端 tsc | `./node_modules/.bin/tsc --noEmit`（快照树，--noEmit 不写盘；node_modules 以 gitignored 符号链接接通，未入版本库） | ✅ exit 0（日志 :368） |
| demo 形状漂移 | `node scripts/demo/check-shape-drift.mjs`（cwd = vite-frontend） | ❌ **exit 1（日志 :384）**：12 缺失必填字段，见 §3.2——**本轮新发现**，R13 同检查 exit 0 |

注：环境提示 `warning: both GOPATH and GOROOT are the same directory` 每条 go 命令均有，仅警告（与 R1–R13 同）。

### 3.1 环境红与不可判定项（隔离证据口径：不计失败、不计通过）

**环境红 1 格 —— 沙箱 fake-IP DNS（历轮同签名，本轮已复现）**：

- 现象：`TestValidateMonitorTCPTarget/公网域名放行`：`ValidateMonitorTCPTarget("example.com:443") err=目标地址不允许（内网/保留地址）: example.com, wantErr=false`（全量段 `monitor_ssrf_test.go:39`，日志 :287–288，与 R10/R11/R12/R13 同断言行）；health 包为全量中已见的 FAIL 包（日志 :292）。
- 识别特征：本机 `getent hosts example.com` 返回 **`198.18.113.179`**（跑测日志首段）——198.18.0.0/15 基准测试保留段即 fake-IP 代理 DNS 签名；netguard 按保留段拒绝是正确执法，错在沙箱 DNS。
- 隔离证据：`go test ./internal/health/ -run 'TestValidateMonitorTCPTarget$' -v -count=3`（主日志 :312 起隔离段为 tail-40 的末次迭代：17 子格中**恰该 1 格 FAIL、其余 16 格全 PASS**；`-count=3` 全量输出另存 `R14-00-health-iso-full.txt`（115 行）：3 次迭代 ×（16 PASS / 1 FAIL 子格）= 48 PASS / 3 FAIL 子格行，逐次同签名）。定性：**环境红，单列、不计产品失败、不计通过、不改产品代码**（与 R2–R13 裁定一致）。
- 跑测脚本如实登记：隔离段以保存的 `$?` 取包 exit（沿 R13 修正口径），本轮 `health isolation exit=1`（主日志 :312）是真实的包 FAIL exit，与包结果行 `FAIL go-backend/internal/health` 一致，可引用。主日志隔离段仅含末次迭代是 `tail -n 40` 的截断产物，全量 3 次迭代证据以 `R14-00-health-iso-full.txt` 为准——两文件结论一致，无冲突。

**夹具不可判定 1 条 —— RFC1918 前提不在场（如实登记，不冒充 Pass）**：

- 对象：`TestInboundProbeEmptyIDsDoesNotClearDown`（handler 包）。全量段 handler 包 **ok**（371.872s）；单独隔离复跑 `-count=3`（独立日志 `R14-00-probe-fixture-isolation.log`，12 行，主日志 :354 `probe fixture isolation exit=0`）→ **3/3 SKIP**，Skip 文案逐字在案：「本机无非 loopback RFC1918 地址，probe 夹具密封性待 CI/真实网络验收（R2 登记 #3）」（`audit_r1_fix_test.go:75`）。定性与 R7–R13 逐字同形态：**本地不可判定，不计入产品预存失败、亦不计入通过**；归口沿历轮：I 域（probe/监控面）与 L 域（夹具面）认领观察。

**环境红类别单列说明 —— `/tmp` 满致 Chrome 伪失败（本轮未发生，类别在案）**：

- 本轮 `/tmp` 实测为 tmpfs 512M、0 已用（未满），跑测一律走大盘 TMPDIR，故本轮**无** `/tmp` 满伪失败项。历轮口径登记在案：`/tmp` 满时 Chrome/demo 相关伪失败属环境红类别，出现时须单列隔离证据、不计失败不计通过，不得与产品失败混计；本轮该类别计数为 0（未发生 ≠ 已验证通过）。
- 同理，Chrome 152 LNA 环境项（`ERR_BLOCKED_BY_LOCAL_NETWORK_ACCESS_CHECKS`，demo check 须以 Playwright chromium-1187 口径判读，承 R10 §7）属环境判读口径，不计产品失败；本轮 demo 段结果按此口径判读——本轮 demo 段 FAIL 是形状漂移断言失败（§3.2），非 LNA 拦截，不适用该口径。

### 3.2 新发现：demo 形状漂移 FAIL（基线员登记 1 项，建议 P3·产品演示面，定级待认领域复核）

- 现象：`demo drift exit=1`（主日志 :384）；12 行 FAIL（:370–381）逐字均为 `InboundApiItem.connIdleSeconds: missing in fixtures admin "inbound/list" (object id=N), file: src/demo/fixtures.json`（N=1…12）；汇总行：`shape drift: 12 missing required field(s) across 252 recorded object(s). Sync the fixtures (re-record or backfill) — see scripts/demo/README.md.`。
- 根因核对（基线员实读，非修复）：`vite-frontend/src/api/types.ts:880` `InboundApiItem` 中 `:940` `connIdleSeconds: number;` 为**必填**（无 `?`；同文件 :974 另一接口的同名字段为可选，不相干）；`src/demo/fixtures.json` 内 `connIdleSeconds` 命中 **0 次**；`scripts/demo/check-shape-drift.mjs` 以「无 `?` 即必填」为判据。引入沿：connIdle 变更（`8805f18`/`b813b99`/`23043ba`）给入站列表 API 加了该必填字段（生产侧 `handler/kernel.go:694` inboundList 已下发 `ConnIdleSeconds: row.ConnIdleSeconds`），demo 夹具未同步重录/回填。R13 同检查 exit 0（同 252 对象全覆盖），故漂移为 1.4.5–1.4.8 增量引入、**本轮基线首次捕获**。
- 影响面：仅 demo 夹具数据 stale（demo 模式下入站列表对象缺该必填字段）；生产 API 本身已下发该字段，非生产回归。修复面有界：按脚本提示重录或回填夹具即可（`scripts/demo/README.md`）。
- 定级：基线员建议 **P3·产品演示面**（演示面夹具与 API 形状不一致，无生产面影响、无权限/计费敞口）；**定级权归认领域/协调者**，本报告只登记事实与证据，不以建议定级冒充裁定；该项归属 connIdle 变更面（§6 第 4 项），由认领域复核确认。

### 3.3 预存失败对账结论

- **Go 全量产品预存失败 = 0：成立（带证据边界）**。全量实跑全部红共 1 条且为 fake-IP DNS 环境红（隔离证据齐备、单列于上）；probe 夹具一条为前提不在场的不可判定项（独立日志 3/3 SKIP，已交接归口）。除此之外三 module build/vet 全绿、go-gost 与 go-gost/x 全绿、go-backend 其余 16 包 ok、前端 tsc 绿。
- **演示面预存失败 = 1 项（§3.2 demo 形状漂移，待定级）**：12 缺失必填字段，全系 `InboundApiItem.connIdleSeconds`，夹具未同步。
- 证据边界：PG 契约未本地实跑（无本地 PG 实例，沿历轮归 CI/发版门判读；哨兵 S-34/S-41 PG 子格相应维持本地不可判定，见 §4）；跑测墙钟不作判定依据（handler 371.872s、repo 113.448s 为本机单跑读数）。

## 4. 回归哨兵抽样表（先行落盘，N-37 细则①③）

- 文件：`R14-00-sentinel.md`（25758 字节、哨兵行 53、尾行「— R14-00-sentinel END · 53 行（承 R13 的 53 行，本轮无新增预登记行），A–L 全覆盖，先行落盘（N-37 细则①③），回勾状态列落盘时全部留空」自证在位），**落盘时点先于本报告与全量日志**（本员第一动作，细则③执行序：协调者收到落盘回执后才派各域）。
- 内容：**53 行** = 承 R13 表 **53 行**逐字沿用（S-01…S-53，被测对象逐字、认领分域不变、基线更新为本轮基线树 `main` @ `7244fbb`；逐行 diff 仅 S-52/S-53 两行有意改动）；S-52/S-53 关闭版本栏写实为 **1.4.5**（核对：1.4.4 树内 ENC 门 0 处、`aes-128-gcm-siv` 0 处；1.4.5 树逐字在位；tag 文案「R8-R13 24 BC」）；S-52 行号按本轮基线树重钉 +17（`kernelVersionGate` ENC 门 :2007 / 文案 :2008、`encDialGate` 门 :2032 / 文案 :2035、换版扫描面 ENC 格 :2118 / 文案 :2121，函数定义 :1997/:2031/:2083；出站 prefer `versionBelowPadded(25, 6, 8)` `kernel_egress.go:48` 不变）；S-53 行号 `credentials.go:627/:644` 与 R13 表逐字同值（diff = 0 行）。
- **新增预登记查核结论：本轮无新增预登记行**。已查 1.4.5–1.4.8 各修复批指定文件全集（`port-3342/PORT-3342-REA-UDIT.md`、`PORT-3342-TUNING-REA-UDIT.md`、`PORT-3342-PROBE-FIX-GATE2.md`、`PORT-148-REA-UDIT.md`、`PORT-148-GATE.md`、`PORT-148B-GATE.md`、`final/RELEASE-146-NOTES.md`、`final/RELEASE-147-NOTES.md`（tag 1.4.7 树直读）、`final/RELEASE-148-NOTES.md`（tag 1.4.8 树直读）、`r13/R13-FIX-REPORT.md` 全文与 `r13/` 目录）：无 `R14-sentinel-prereg.md` 类文件、无 S-54 起预登记行；`R13-FIX-REPORT.md` §5 的 8 项靶向验证门为真机门项、非哨兵预登记行，不定号并入（N-35）；R13 关闭项未升哨兵行，其关闭复核由各域按对象本身承担。哨兵表末列「R14 回勾状态」落盘时**全部留空**，待各域回勾。
- **S-16 / S-34 本地不可判定承继形态（哨兵表内已写死，本节摘要）**：S-16 导入腿本地 SKIP（沙箱无非 loopback RFC1918 地址），承继 R10 发版门 #8 测试机实跑 PASS + R11 门抽验无回归信号 + R13 L7 抽核；S-34 两条真 PG 契约本地 SKIP（无 DSN，禁以 SQLite/seq 顶替），承继 R10 发版门 #7 真 PG 3/3 PASS + R11 门抽验无回归信号 + R13 抽核；S-41 PG 子格分格登记（不另占行）。三者在本轮均**不标 PASS、不计失败**，只有真机/真 PG 实跑方改写状态。
- 分域分布（不变）：A 2 / B 4 / C 4 / D 10 / E 5 / F 4 / G 4 / H 7 / I 3 / J 3 / K 4 / L 3，合计 53。分轮：R1:9 / R2:12 / R3:7 / R4:7 / R5+SS:6 / R6:9 / R7:1 / R8:2 / R11:0 / R12:0 / R13:0（R13 关闭项未升哨兵行，其复核由各域按对象本身承担）。

## 5. 代码规模（catalog §0 口径，量测树 = main @ 7244fbb）

| 面 | 行数 / 文件数（本轮实测） | 1.4.4 实测（catalog §0） | 增量 |
|---|---|---|---|
| backend internal | **144,860 行 / 558 文件** | 135,181 行 / 517 文件 | +9,679 行 / +41 文件 |
| frontend src | **63,092 行 / 189 文件** | 61,838 行 / 188 文件 | +1,254 行 / +1 文件 |
| go-gost | **79,607 行 / 593 文件** | 78,285 行 / 587 文件 | +1,322 行 / +6 文件 |

- 口径（catalog §0 写死）：行数 = 对文件集逐个 `cat` 后 `wc -l`；文件集 = backend `go-backend/internal` 下全部 `*.go`、frontend `vite-frontend/src` 下全部 `*.ts`/`*.tsx`、go-gost `go-gost` 下全部 `*.go`；文件数为同集计数。量测于 `.r14-tree` @ `7244fbb`。
- 注：catalog §0 现行值仍为 1.4.4 实测（见 §7）；本轮量测为 1.4.8 实测值，**未回填进 catalog**（代际回填不在本轮任务书内，见 §7）。

## 6. 自 1.4.4 以来的变更清单（供各域认领）

范围：`d03c2ab..7244fbb`，103 提交 / 169 文件 +14592/−433。 headline 7 项逐项落点如下（另 R11–R13 修复批见末条）：

1. **Gist 5 端点（1.4.6）**：提交 `c75bbb8`（b1 上传子集）、`f559a54`（g-backend：versions 端点 + 明文上传拒收 T-4 + 双信封解密）、`eabee27`（g-frontend：versions 列表 + 恢复弹窗）、`c63719f`（g-recovery：指定版本恢复）、`7e5715a`（集成）、`7740f8a`（文档）、`0f0c4fe`（1.4.6 字面量 + 技能包覆盖）。落点文件：`go-backend/internal/http/handler/gist_backup.go`（新建）、`handler.go`（注册 :266–270 + `getConfigs` 令牌/密码下发屏蔽）、`handler/jobs.go`、`internal/store/repo/config_policy.go`；前端 `src/api/index.ts`、`src/pages/config/gist-backup.tsx`、`src/pages/config.tsx`、`src/config/sensitive-keys.ts`、`src/pages/config/notify-card.tsx`；文档 `doc/guide/gist-backup.md`；技能包 `skills/flmx-api/references/backup.md`。路由增量即 §2.1 的 5 条。
2. **调优（含一键测算，1.4.7）**：提交 `b29f981`（b2：上游 3.3.42 BDP 服务端调优）、`15443bf`（api：NodeTuneLine/Sizes + nodeTune 行参）、`0bc7efa`（t-agent：能力探测门代硬 1.4.5 版本门 + TuneProbe 一键行探测）、`a50f4a8`（t-panel：调优弹窗行输入 + 一键探测建议）、`92d4606`/`b65b801`（集成）。落点文件：`go-backend/internal/http/handler/node_tune.go`（+ `node_tune_bdp_test.go`）、`go-gost/x/socket/tuning.go`、`tuning_probe.go`、`tuning_probe_linux.go`、`tuning_probe_other.go`、`websocket_reporter.go`；前端 `src/pages/node/tune-modal.tsx`、`src/api/index.ts`。
3. **测算修复（1.4.8）**：提交 `afc7bd9`（RTT 读 tcpi_rtt 偏移 68、回落展示）、`dbb4b82`（分族 sock_diag dump）、`8ba125e`（合并）。落点文件：`go-gost/x/socket/tuning_probe_linux.go`（+ `tuning_probe_linux_test.go`）、前端 `src/pages/node/tune-modal.tsx`。门重验结论见 MEMORY（1.4.8 门：中位数 4/5ms、10 样本真实测量）。
4. **空闲超时 connIdle（1.4.8）**：提交 `8805f18`（逐入站 `policy.levels[].connIdle`）、`b813b99`（合并）、`23043ba`（出口侧 userLevel 落点：专用 freedom 出站 `flmx-idle-<id>` + inboundTag 规则）、`7244fbb`（合并，本轮基线 tip）。落点文件：`go-backend/internal/http/handler/kernel.go`（字段接入：inboundView :115、inboundRequest :163、inboundFromModel :175、applyInboundToModel :216、inboundList :694、inboundSave :930/:938）、`internal/kernel/model.go`、`render_xray.go`、`chain.go`、`users.go`、`split.go`、`internal/store/model/model.go`、`node_kernel.go`、`internal/store/repo/repository_inbound_backup.go`；前端 `src/pages/inbound/inbound-form-modal.tsx`、`presets.ts`、`src/api/types.ts`。**demo 形状漂移（§3.2）归属本面**，由认领域复核定级。
5. **延迟断链修复（1.4.8）**：提交 `45ef048`、`75ce035`（合并）。落点文件：`go-backend/internal/http/handler/handler.go`（nodeList）、`handler/node_latency.go`（+ `node_latency_list_test.go`）、`internal/store/repo/repository_monitor_nodes.go`。修复内容：nodeList 贴最新探测结果与显式自选（无自选则省略）。
6. **延迟提示（1.4.8）**：提交 `a99d68d`、`3d180ed`（合并）。落点文件：前端 `src/pages/config.tsx`、`src/pages/node.tsx`（延迟测试点池为空时提示）。注：`a99d68d` 同时是当前共享工作树 HEAD（`feat-latency-hint`），但其内容已随 `3d180ed` 合入 main、在本轮基线树内；读数以 `.r14-tree` 为准。
7. **DOC 增补**：提交 `7740f8a`（Gist 备份指南并入）、`249edee`（R13 门 8/8 + FINAL 四件套）、`dbacf7f`/`60a4392`（R12/R13 修复报告）、`9571433`（R12X-002 重定草稿 + BC-R13-01/02 草稿，无产品改动）、`0f0c4fe`/`09f1111`（1.4.6 字面量与说明并入 main）、`bd9f99d`（`audit-out-132/final/RELEASE-148-NOTES.md`，新增 61 行）。
- 另：**R11–R13 修复批亦在范围内**（`integrate-r11` 六批 b1–b6、`integrate-r12` bX/bT、`integrate-r13` bR/bT2/bDoc；合并提交 `c690396`/`863516e`/`e8afc45`/`876c42e`/`29660eb`/`fdc192f`、`f4bfd9a`/`f49dc0d`、`8e055c4`/`099c318`），各域按 R11/R12/R13 既有编号认领；其关闭复核见 §7。

## 7. 沿袭登记基座（供各域认领，不代判）

| 沿袭项 | 所在（正本位置） | 现状（本员复核） |
|---|---|---|
| **144 格清单**（无二进制四面客户端后果未验证格）+ R11X-013 沿袭项 | 清单正本：`r9/R9-B-report.md` §2 与 `r9/R9-FINAL-summary.md` §5/§6；R11X-013 去向：`r11/R11-FINAL-summary.md` §5/§7 与 `r12/R12-00-baseline.md` §5 | 维持未验证登记、不硬判不销账；R13-FINAL-summary §5/§7 同口径维持（本轮无新增可验官方二进制；L5/L2 二进制证据不改变该格状态）；R13 不重复占号，本轮同然 |
| **墓碑三触发条件**（`group_revocation_tombstone` 差集撤销方案，R5X-015） | 正本：`r9/R9-FINAL-summary.md` §5 + `r9/R9-G-report.md` §4；R12 附带对账：`r12/R12-FINAL-summary.md` §5 | R11-X-ledger §5 与 R13 沿袭：逐条对账仍均未满足、维持缓做；本轮各域如有新触发证据须以新证据另行登记，不得以旧登记阻挡或顶替（N-36） |
| **VG-09**（Snell 独立内核服务端版本 `snellServes` + 安装封闭清单 {5.0.1 / 6.0.0 / 4.1.1}） | 正本：`r12/R12-L3-report.md` §1 VG-09 行（`handler/kernel.go:2126` `snellServes`、门内调用 :2002、换版面调用 :2147；`kernel/capabilities.go:234`）；R13 复核：`r13/R13-L3-report.md` §1 | **维持未验证（无反证）**：snell-server 闭源、无在盘二进制（R13 全根搜索 0 hits）；判定逻辑用例 `TestSnellServes`/`TestKernelVersionGateSnell` PASS 仅证代码自洽。本轮承 R13 形态，基线员不代判；行号重钉归 L3 域 |
| **demo check Chrome 版本口径**（R9 门 §3 观察 5） | 正本：`r9/R9-RELEASE-GATE.md` §1 #9 与 §3 观察 5；catalog 附则 B 第 2/3 条 | 在案口径：本机 Chrome 152 下 CDP 导航 127.0.0.1 被 LNA 拦截、候选与基线树同姿势同败 = 环境性质、非候选回归；可用口径 = Playwright chromium-1187 作 `CHROME` 跑本体。本轮 demo 段 FAIL 为形状漂移断言失败（§3.2），非 LNA 拦截，不适用该口径 |
| **R12X-002 重定（已执行）** | 建议来源：`r13/R13-FINAL-summary.md` §1.3；执行：`final/FINAL-CANDIDATE-AUDIT.md` :44 | **终局候选版审计已采纳重定为 P2（历史定级修正）**：R12 X-0 与 R13 L6 两路 Xray 25.9.5 复探均证实逐字变体配置 exit 23、规范对照 exit 0；不溯改 R12 ledger、不回填 R13 计数、不改变收敛结论、不产生新修复面。本轮只登记执行结果，不重议 |
| **R13 关闭回溯**（各域复验对象，非基线员代判） | `r13/R13-FIX-REPORT.md`（关闭 7/7：批 R 4/4 + 批 T2 3/3）+ `r13/R13-FINAL-summary.md`（定稿 7 条：P2=1/P3=6；R13X-001 P2 为发布阻断项——阻断「不带修复的发布」，非收敛结论）+ 文档批 D-01/D-02/D-03 | 7 条代码均在本轮基线树内（R13 集成合并 `8e055c4`/`099c318` 在 §1 段内）；关闭复验归各域按对象本身（N-35），基线员只建基座、不下关闭结论；R13X-001 修复面（备份恢复重推）在树内，其真机终态复验归各域 |
| **三正本头部代际状态**（N-37 细则③④在位） | `~/workspace/user/files/flmx-audit-catalog-v132.md`、`flmx-audit-checklist-v132.md`、`flmx-audit-prompt-dispatch-v132.md`（均为现行 v132 版） | catalog 标题仍「基线 1.4.4 / tag d03c2ab」；checklist A-01 = 197 条、H-12 = 49 表（均为 1.4.4 实测口径）；dispatch 头部「基线随当轮发布态，具体基线号由每轮任务钉死」——本轮由任务钉死为 `main` @ `7244fbb`，与头部形态自洽。**本轮不回填新代际**：代际回填不在本轮任务书内，基线员不擅自改三正本；现行头部值（路由 197 / 表 49）与本轮实测（路由 202 / 表 49）已出现差集（路由 +5 Gist），回填与否留待协调者裁定 |

## 8. 基线员计数（按面别分列）与判定

本基线员本轮**新发现 1 项**（§3.2 demo 形状漂移，建议 P3·产品演示面，定级待认领域复核确认；其余基线身份、路由、表三项对账无产品缺陷）。

| 面别 | P0 | P1 | P2 | P3 |
|---|---|---|---|---|
| 产品 | 0 | 0 | 0 | 0 |
| 产品·演示面 | 0 | 0 | 0 | **1（§3.2，建议定级，待复核）** |
| 文档 | 0 | 0 | 0 | 0 |
| 测试基建 | 0 | 0 | 0 | 0 |
| 流程基建 | 0 | 0 | 0 | 0 |
| **合计** | **0** | **0** | **0** | **1** |

- 非缺陷登记（不计入上表）：① health fake-IP DNS 环境红 1 格（§3.1，隔离证据：3 迭代 48 PASS / 3 FAIL 子格）；② probe 夹具前提不在场 1 条（§3.1，独立日志 3/3 SKIP，交 I/L 域观察）；③ `/tmp` 满伪失败类别本轮计数 0（未发生，§3.1）；④ R13 原料表 7 行 authPeer 右括号转录勘误 1 处（§2.1，原料表修正、非产品）；⑤ 三正本头部代际差集登记 1 项（§7，路由 197→202，回填待协调者裁定）。
- 注：上表仅为**基线员一域**的计数；R14 全轮计数待各域报告与交叉复核汇总，本报告不预判全轮结论。

## 9. 基线判定

- **基线身份**：审计对象钉死 **`main` @ `7244fbb`**（= origin/main；tag 1.4.8 = main + 1 发布字面量提交，产品代码差集 0）；只读 worktree `~/workspace/.r14-tree` 供各域读数、永不修改，全程 pristine。
- **路由**：**202 条（/api/v1 197 + 非 API 5）**，鉴权分布 ADMIN 135/JWT 44/PUBLIC 17/OPTIONAL-AUTH 1；相对 R13 仅 +5 Gist（§2.1 逐条列出），其余 197 条集合与顺序 diff = ∅（§2.1）。
- **数据表**：**49 个模型**，与 R13 口径集合与顺序 diff = ∅、新增 0 张（§2.2）。
- **哨兵抽样**：53 行（R1:9 / R2:12 / R3:7 / R4:7 / R5+SS:6 / R6:9 / R7:1 / R8:2 / R11:0 / R12:0 / R13:0），A–L 十二域全覆盖，**已先行落盘**（§4）；本轮无新增预登记行；S-52/S-53 关闭版本写实为 1.4.5；回勾状态列落盘时全部留空。
- **代码规模**：backend internal 144,860 行 / 558 文件；frontend src 63,092 行 / 189 文件；go-gost 79,607 行 / 593 文件（catalog §0 口径，§5）。
- **构建/测试**：三 Go module build/vet 全绿；go-gost 全过、go-gost/x 27 包 ok；go-backend 全量 16 ok / 4 无测试 / 1 FAIL 包，唯一红为 health fake-IP DNS 环境红一格（隔离复跑 3 迭代同签名）；前端 tsc 绿；**demo 形状漂移 FAIL 1 项**（§3.2，12 缺失必填字段，全系 `InboundApiItem.connIdleSeconds`，夹具未同步）。**Go 面产品预存失败 = 0；演示面待定级 1 项**（§3.3，含证据边界声明）。全部结论只认落盘日志 `R14-00-baseline-test.log`（385 行）与 probe 夹具独立日志 `R14-00-probe-fixture-isolation.log`（12 行）及 health 隔离全量文件 `R14-00-health-iso-full.txt`（115 行）。
- 本报告 + `R14-00-sentinel.md` + `R14-00-routes-raw.csv` + `R14-00-tables-raw.txt` + `R14-00-baseline-test.log` + `R14-00-health-iso-full.txt` + `R14-00-probe-fixture-isolation.log` 为 R14 各域共同起点；各域引用基线数字以本报告为准。

— R14-00 基线报告完（基线 main @ 7244fbb，tag 1.4.8 @ bd9f99d = main + 1 发布字面量提交、产品代码差集 0，Go 面产品预存失败=0，演示面 demo 漂移待定级 1 项，路由 202=197+5，49 表，哨兵 53 行先行落盘、本轮无新增预登记行、S-52/S-53 关闭版本写实为 1.4.5、回勾列留空）
