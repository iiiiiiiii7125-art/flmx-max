# R18 修复 F-02+F-04 报告：管理 API create 盲 append 按名去重

- 基线：`main @ a0c18a0`（`a0c18a0dc90b2f9c7a67e8a2a016cc1d97505fb5`）
- 分支：`fix-r18-f02f04`（worktree `~/workspace/.fixr18-f02f04`，从 main @ a0c18a0 切出）
- 提交：一次提交（分支尖端即本报告所指提交；哈希见父级交付说明，不在正文内嵌以免改后失真）
- 未合 main、未打 tag、未发版、未改版本字面量、未改动 `go-gost/x/socket/` 下任何文件（F-03 的路）、未碰测试机、未动管理 API 三启用门（`-api` / `GOST_API` / 配置文件 `api:` 段）
- Go 工具链：`~/go/bin/go`（go1.25.1）
- 依据：`~/workspace/flvx-max/audit-out-132/r18/R18-FIX-PLAN.md`（F-02、F-04）、`R18-X-ledger.md`（R18-X-02、R18-X-04）、`R18-X-verdicts.md`（V-03、V-05）；同款改法参考 R17-A（`go-gost/x/socket/service.go` 第 4 阶段按名 upsert，见 `audit-out-132/r17/R17-FIX-A-REPORT.md`）

## 1. 定位结论（实测，非假说复述）

17 处盲 append 全部位于 `go-gost/x/api/` 的 create handler 内、registry `IsRegistered`
守卫之后、`config.OnUpdate` 闭包之中。成员已在配置中、但不在注册表里时（例如
暂停后 loader 跳过注册，见 `go-gost/x/config/loader/loader.go:256` 的 `paused`
跳过分支；或注册表与配置漂移），守卫放行，`append` 把第二份同名项写进配置并
经 `OnUpdate → persist()` 落盘，毒化 gost.json。R17-A 启动自愈只覆盖 Services，
其余 15 类无自愈兜底——但三启用门全关，入口不可达（V-03/V-05，产品 P3）。

逐类枚举（create 函数行号为修后值，append 点行号为修前值 → 修后 upsert 行号见 §2）：

| # | 类 | 文件 | create 函数 | 盲 append（修前） | 配置切片 |
|---|---|---|---|---|---|
| 1 | service（单） | `config_service.go` | `createService :44` | `:85` | `c.Services` |
| 2 | service（批，第 4 阶段） | `config_service.go` | `createServices :96` | `:171` | `c.Services` |
| 3 | admission | `config_admission.go` | `createAdmission :26` | `:60` | `c.Admissions` |
| 4 | auther | `config_auther.go` | `createAuther :26` | `:59` | `c.Authers` |
| 5 | bypass | `config_bypass.go` | `createBypass :26` | `:60` | `c.Bypasses` |
| 6 | chain | `config_chain.go` | `createChain :27` | `:65` | `c.Chains` |
| 7 | conn_limiter | `config_conn_limiter.go` | `createConnLimiter :26` | `:59` | `c.CLimiters` |
| 8 | hop | `config_hop.go` | `createHop :27` | `:65` | `c.Hops` |
| 9 | hosts | `config_hosts.go` | `createHosts :26` | `:60` | `c.Hosts` |
| 10 | ingress | `config_ingress.go` | `createIngress :26` | `:60` | `c.Ingresses` |
| 11 | limiter | `config_limiter.go` | `createLimiter :26` | `:60` | `c.Limiters` |
| 12 | observer | `config_observer.go` | `createObserver :26` | `:60` | `c.Observers` |
| 13 | rate_limiter | `config_rate_limiter.go` | `createRateLimiter :26` | `:60` | `c.RLimiters` |
| 14 | recorder | `config_recorder.go` | `createRecorder :26` | `:59` | `c.Recorders` |
| 15 | resolver | `config_resolver.go` | `createResolver :26` | `:64` | `c.Resolvers` |
| 16 | router | `config_router.go` | `createRouter :26` | `:60` | `c.Routers` |
| 17 | sd | `config_sd.go` | `createSD :26` | `:60` | `c.SDs` |

以上 17 处之外，`x/api` 内无其他 create 侧盲 append（`update*` 均已按名
替换；剩余 `append(c.…)` 见 §5，均为 delete 过滤重建）。

## 2. 改动

### 2.1 新 helper：`go-gost/x/api/config_upsert.go:16-38` `upsertNamed`

泛型按名 upsert（R17-A 同款口径）：同名则用新项替换首次出现的那一份、
并清除其余同名副本；不存在则追加；`nil` 项原样保留不 panic；`nil` 入项
不改动；名按 `TrimSpace` 后比较（与 R17-A `DeduplicateServices` 同口径）。
以配置成员为准去重——`createServices` 第 1 阶段的 registry `IsRegistered`
守卫拦不住 paused 重建等漂移（见 §1），不依赖它。

### 2.2 F-02：`go-gost/x/api/config_service.go`（2 处）

- `createService`：`:87`（原 `:85` 盲 append）改为
  `c.Services = upsertNamed(c.Services, &req.Data, …)`。
- `createServices` 第 4 阶段：`:176`（原 `:171`）改为逐项
  `upsertNamed(c.Services, &cfgCopy, …)`。附带修掉同闭包内的
  `&ps.config` 别名问题：原代码取复用循环变量字段的地址，批量多服务将
  全指向最后一项；现逐项取副本 `cfgCopy := ps.config`（与 R17-A socket
  侧第 4 阶段同写法）。第 1–3 阶段（验证/注册/启动）未动。

### 2.3 F-04：其余 15 类 create（各 1 处）

每类把 `OnUpdate` 内的盲 append 改为同款 `upsertNamed`（只改该行 + 2 行
注释，registry 守卫、解析、注册逻辑均未动）：

| 类 | upsert 行号（修后） |
|---|---|
| admission | `config_admission.go:62` |
| auther | `config_auther.go:61` |
| bypass | `config_bypass.go:62` |
| chain | `config_chain.go:67` |
| conn_limiter | `config_conn_limiter.go:61` |
| hop | `config_hop.go:67` |
| hosts | `config_hosts.go:62` |
| ingress | `config_ingress.go:62` |
| limiter | `config_limiter.go:62` |
| observer | `config_observer.go:62` |
| rate_limiter | `config_rate_limiter.go:62` |
| recorder | `config_recorder.go:61` |
| resolver | `config_resolver.go:66` |
| router | `config_router.go:62` |
| sd | `config_sd.go:62` |

## 3. 测试证据（先红后绿，日志落盘）

回归文件：`go-gost/x/api/r18_fix_f02f04_test.go`（新）。
驱动方式：gin test engine 直挂 17 个 create handler（与 `api.go`
Register 同路径，不走鉴权中间件）；每测 seed 全局配置（成员已在配置中、
注册表为空，模拟 paused/漂移），POST 一次后断言内存配置与落盘文件
（`SetPersistPath` 指向 `t.TempDir()` + `EnablePersist`）中同名仅一份。
注意：handler 用 `ctx.ShouldBindJSON(&req.Data)` 把请求体直接绑进配置
结构（无 `"data"` 包裹）；batch 直接绑进 `[]ServiceConfig`。

### 3.1 红（修前，基线 a0c18a0）

日志：`audit-out-132/r18/R18-FIX-F02F04-red.log`（6542 字节）。

- F-02 三形全 FAIL：paused 重建内存 2 份、批量同名 2 份、毒化文件 3 份。
- F-04 十五形全 FAIL：每类内存 2 份。
- 红日志中途曾有 18 条 `400 name is required`：系测试初版请求体误包
  `"data"` 层；修正体形后重跑红，失败转为真实的计数断言
  （`expected exactly 1 … got 2/3`），见同名日志最终版。

### 3.2 绿（修后，`fix-r18-f02f04` 尖端）

日志：`audit-out-132/r18/R18-FIX-F02F04-green.log`（4756 字节）。

| 测试 | 文件：行 | 结果 |
|---|---|---|
| `TestR18F02CreateServiceDedupesPausedRebuild`（paused 重建 → 内存/落盘各 1 份） | `r18_fix_f02f04_test.go:110` | PASS |
| `TestR18F02CreateServicesBatchDedupesExistingConfigEntry`（批量：同名旧项 1 份 + 新项 1 份，内存/落盘） | `r18_fix_f02f04_test.go:135` | PASS |
| `TestR18F02CreateServiceDedupesAlreadyPoisonedConfig`（毒化 2 份 + 再 create → 内存/落盘各 1 份） | `r18_fix_f02f04_test.go:171` | PASS |
| `TestR18F02UpsertNamed`（helper 单元：批内同名连续 upsert、毒化列表收敛、空白名/nil 项） | `r18_fix_f02f04_test.go:196` | PASS |
| `TestR18F04CreateDedupesExistingConfigEntry`（15 子测各一形：admission/auther/bypass/chain/conn_limiter/hop/hosts/ingress/limiter/observer/rate_limiter/recorder/resolver/router/sd，重复 create 后配置中仅一份） | `r18_fix_f02f04_test.go:362` | PASS（15/15 子测） |

### 3.3 全量验证（修后）

- `go-gost` 模块：`go build ./...` exit 0；`go vet ./...` exit 0；
  `go test ./... -count=1` exit 0（`github.com/go-gost/gost` [no test files]、
  `tests/lifecycle` ok）。日志：`R18-FIX-F02F04-fulltest-gogost.log`（217 字节）。
- `go-gost/x` 模块：`go build ./...` exit 0；`go vet ./...` exit 0；
  `go test ./... -count=1` exit 0：27 个包 ok、179 个包 [no test files]、
  0 行 FAIL，产品失败 0。日志：`R18-FIX-F02F04-fulltest-gostx.log`（11692 字节）。
- `go-backend` 模块：未改动任何文件；`go build ./...` exit 0（复核无连带影响）。
- `gofmt -l go-gost/x/api/`：仅 `doc.go` 有格式差异——基线已存在、本次未改动，
  不动它。

## 4. 入口仍不可达（复核）

管理 API 三启用门一律未动、未启用：`api.go`、`config.go`、
`-api`/`GOST_API` 相关代码 0 行 diff；本次回归只在测试进程内直挂
handler，未启动真实 API server、未改任何部署配置。修复只改变
“已漂移配置 + 重复 create”路径下的落盘结果；正常路径（新名 create）
行为不变——upsert 在无同名时即为原 append。

## 5. 修后全仓同型再搜索

对 `go-gost/x/api/` 全仓 grep `append(c.`（修后）：剩余 17 处全部位于
各 `delete*` handler 的过滤重建循环（把非删除名逐个 append 回新切片），
不是 create 侧盲 append，不改：

- `config_admission.go:176`、`config_auther.go:174`、`config_bypass.go:176`、
  `config_chain.go:186`、`config_conn_limiter.go:175`、`config_hop.go:185`、
  `config_hosts.go:176`、`config_ingress.go:176`、`config_limiter.go:176`、
  `config_observer.go:176`、`config_rate_limiter.go:176`、
  `config_recorder.go:175`、`config_resolver.go:184`、`config_router.go:176`、
  `config_sd.go:176`、`config_service.go:414`（`deleteService`）、
  `:488`（`deleteServices`）。
- `go-gost/x/socket/` 0 行改动（F-03 的路，本次未碰）。

## 6. 未验证项（明文登记，留 R19）

1. **真机验证一律未做**：按任务约束，本次修复的真机验证留 R19 独占
   窗口；本地无测试机验证——管理 API 在真机上的实际行为、面板
   WebSocket 下发链路与 agent 侧的联动均未验证。
2. **批内同名两项的 HTTP 端到端形未立**：`createServices` batch 内
   `[同名, 同名]` 在第 2 阶段 registry 注册时即失败回滚、到不了第 4 阶段，
   故 HTTP 层无“批内重复直达 append”的可达形；第 4 阶段循环的同名
   upsert 语义只由 `TestR18F02UpsertNamed` 在 helper 层断言。
   批量回归形取的是可达的漂移形（batch 内某项与配置已有成员同名）。
3. **其余 15 类的落盘面只在内存断言**：F-04 每类一形断言内存配置仅一份；
   落盘文件逐类断言只在 F-02 三形做（同为 `OnUpdate → persist()` 路径，
   helper 与落盘共用）。
4. **R17 修复报告 §10 剩余未验证项继续结转**：远端/非 Debian 内核、
   浏览器 DOM 腿、订阅 1 流量计数器响应性（与本次改动无关）。

## 7. 交付文件清单（本分支一次提交）

- 改动 16 文件：`go-gost/x/api/config_service.go` + 15 类 `config_*.go`
  （`git diff --stat`：16 files changed, 52 insertions, 17 deletions）。
- 新增 2 文件：`go-gost/x/api/config_upsert.go`（38 行）、
  `go-gost/x/api/r18_fix_f02f04_test.go`。
- 报告与日志 5 文件：`audit-out-132/r18/R18-FIX-F02F04-REPORT.md`（本文件）、
  `R18-FIX-F02F04-red.log`、`R18-FIX-F02F04-green.log`、
  `R18-FIX-F02F04-fulltest-gogost.log`、`R18-FIX-F02F04-fulltest-gostx.log`。
- 只用明确文件列表 `git add`，未用 `git add -A`。

— R18-FIX-F02F04-REPORT END · 自证：本行之前正文字节数 11323 · END
