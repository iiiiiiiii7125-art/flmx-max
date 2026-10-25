# R19 修复 F-02 报告：`update*` nil panic 全族 + `OnUpdate` 锁泄漏

- 基线：main @ `d66042c`（`d66042cc0b7ac011ddfad09f3f2709b90dafda2e`）
- 分支：`fix-r19-f02`（独立 worktree `~/workspace/.fixr19-f02`，未合 main、未打 tag、未发版、未改版本字面量）
- 依据：`audit-out-132/r19/R19-FIX-PLAN.md`（F-02 节）、`R19-X-ledger.md`（R19-X-02）、`R19-X-verdicts.md`（V-03）、`R19-L2-gaps.md`（§2）
- 窗口：沙箱。测试机未用（无 R19 独占窗口；真机验证留 R20）

## 1. 定位结论（实测，非假说复述）

### 1.1 L2 计数订正：同族不是约 49 处，是 60 处运行时点 + 17 处 loader

L2 称「x/api 约 40 处、x/socket 9 处」，本轮逐文件普查后订正为 **60 处运行时点**：

| 面 | L2 计数 | 实查 | 差额来源（L2 漏数） |
|---|---|---|---|
| x/api 15 类 update+delete | 30 | 30 | — |
| x/api services update/delete | 4 | 4 | — |
| x/api services pause/resume/status/rollback | 6 | 12 | 批量 pauseServices（3 处：读循环、建 map、第 3 阶段）、批量 resumeServices（2 处）、rollbackResumedServices（1 处） |
| x/socket | 9 | 14 | pauseServices（2 处：建 map、第 3 阶段）、resumeServices 第 3 阶段（1 处）、rollbackPausedServices（1 处）、rollbackResumedServices（1 处） |
| **合计** | **约 49** | **60** | **漏 11 处，全部已补守卫（见 §5 全族处置表）** |

- loader：17 类循环，16 类无守卫（services 早有 `service config is nil`，作对照组）。
- `OnUpdate`：`x/config/config.go:134-142` 非 defer 解锁，确认为放大器。

### 1.2 注入向量实测：落盘 JSON null 确实读成内存 nil（viper 路径）

红态前先做探针（临时测试，跑后已删）：`cfg.ReadFile()` 读含 `null` 的 gost.json，`services[0]==nil`、`chains[0]==nil`、`limiters[0]==nil` 均为 true。L2「落盘一个 JSON null 即可致启动崩溃循环」的前提成立，不是假设。

### 1.3 三件缺一不可，缺一件即不闭合

- ① 运行时入口只跳过 nil、不清除——若只修此处，毒化文件仍在，下次启动照样崩；
- ② loader 守卫只报错、不清除——若只修此处，启动仍失败（只不过从 panic 变成干净错误退出，systemd 重启仍是循环）；
- ③ 自愈剥离 nil 并重新落盘——只有此处能让旧毒化文件一次起成；
- `OnUpdate` defer 解锁——没有它，一次 panic 的 recover 之后配置子系统永久 wedged，比崩溃更坏。

## 2. 设计口径（四条，全族统一）

1. **运行时入口（update/delete/pause/resume/status/rollback）**：nil 项跳过并记 `Warnf` 日志，**原样保留**——与 R18-F03 create 守卫、`dedupeNamed`/`upsertNamed` 的「nil 项原样保留」口径一致；清除只走启动/重载自愈，不在业务操作里顺手改写无关项。
2. **loader**：nil 项 fail-closed，返回干净错误 `"<kind> config is nil"`，与 services 既有口径逐字同款；不得 panic，不得静默跳过（跳过会掩盖文件损坏）。
3. **自愈**：`StripNilConfigItems` 剥离全部 17 类 nil 项并重新落盘（nil 项拒绝落盘、不得进自愈保留）；落盘失败只告警、不阻塞启动。`Deduplicate*` 的「nil 项原样保留」已证稳口径不动。
4. **`OnUpdate`**：闭包放内层函数、defer 解锁；`persist()` 仍在解锁后执行（它经 `Global()` 取读锁，与写锁同持会自死锁，故解锁顺序不得变）。

## 3. 改动（按文件，行号为修后）

### 3.1 `go-gost/x/config/config.go:134-145`——`OnUpdate` 改 defer 解锁

```go
err := func() (err error) {
    globalMux.Lock()
    defer globalMux.Unlock()
    return f(global)
}()
if err == nil {
    err = persist()
}
```

panic 时内层 defer 释放写锁，panic 继续上抛给 `routeCommandSafely` recover；成功路径行为与原来逐字相同（先解锁、再 persist）。

### 3.2 `go-gost/x/config/dedupe.go:195-277`——新增 `StripNilConfigItems` + `stripNil`

`StripNilConfigItems(cfg *Config) []string`：剥离全部 17 类命名切片中的 nil 项，返回按类记的清除描述（如 `"2 nil service(s)"`）；无清除返回 nil、不改动配置；nil cfg 返回 nil。`stripNil` 为泛型 helper（复用 `DeduplicateServices` 的 `[:0]` 原地 idiom；仅在有清除时才回写切片）。

### 3.3 `go-gost/program.go:320-350`——启动/重载自愈先剥离 nil

`healPersistedConfig` 开头先调 `StripNilConfigItems`，有清除则 Warn 日志 + `PersistNow` 重新落盘（失败只告警、不阻塞启动），再走原有五类去重。SIGHUP `reloadConfig` 走同一函数，故重载腿同款覆盖。

### 3.4 `go-gost/x/api/config_reload.go:42-51`——API 重载同款自愈

`reloadConfig` handler 在 `parser.Parse()` 后、`loader.Reload` 前同样剥离 nil 并重新落盘。SIGHUP 重载有自愈、API 重载没有，是同型缺口，本项一并堵上（只加 nil 剥离，不加去重——去重仍只走启动/SIGHUP 路径）。

### 3.5 `go-gost/x/config/loader/loader.go`——16 类补守卫

每类循环顶加 `if xCfg == nil { return fmt.Errorf("<kind> config is nil") }`，置于 `Parse*` 调用之前：logger `:94-97`、auther `:108-110`、admission `:121-123`、bypass `:134-136`、resolver `:151-153`、hosts `:168-170`、ingress `:181-183`、router `:194-196`、sd `:207-209`、observer `:220-222`、recorder `:233-235`、limiter `:246-248`、conn limiter `:259-261`、rate limiter `:272-274`、hop `:285-287`、chain `:302-304`。services `:320` 为既有守卫，未动。

### 3.6 `go-gost/x/socket/`——14 处

- `chain.go`：updateChain `:85`（搜索循环跳过 nil）、deleteChain `:115`（nil 项原样保留）。
- `limiter.go`：updateLimiter `:77`、deleteLimiter `:107`、updateConnLimiter `:195`、deleteConnLimiter `:221`；另补 `core/logger` import。
- `service.go`：updateServices 第 3 阶段 `:203`、deleteServices 第 3 阶段 `:332`、pauseServices 建 map `:413` 与第 3 阶段 `:450`、resumeServices 查配置 `:505` 与第 3 阶段 `:587`、rollbackPausedServices `:638`、rollbackResumedServices `:672`；另补 `core/logger` import。

### 3.7 `go-gost/x/api/`——46 处

- 其余 15 类 update+delete 各 2 处（`:122/:179` 前后，chain/hop `:130/:187` 前后，recorder `:121/:178` 前后，resolver `:130/:187` 前后——行号因 import 行数略有出入，见 `git diff`）：admission、auther、bypass、chain、conn_limiter、hop、hosts、ingress、limiter、observer、rate_limiter、recorder、resolver、router、sd。13 个文件补 `core/logger` import（chain/hop 原有）。
- `config_service.go` 16 处：updateService `:260`、updateServices `:349`、deleteService `:422`、deleteServices `:497`、pauseService 读循环 `:580` 与 OnUpdate `:599`、resumeService 读循环 `:647` 与 OnUpdate `:704`、getServiceStatus `:774`、rollbackPausedServices `:824`、rollbackResumedServices `:859`、pauseServices 读循环 `:944`、建 map `:979` 与 OnUpdate `:1014`、resumeServices 读循环 `:1088` 与 OnUpdate `:1173`；另补 `core/logger` import。
- `config_reload.go` 见 §3.4。

## 4. 测试证据（先红后绿，日志落盘 `audit-out-132/r19/`）

### 4.1 红（修前，基线 d66042c）

| 形 | 结果 | 日志 |
|---|---|---|
| socket L2 5/5 panic（updateChain/updateLimiter/updateConnLimiter/updateServices/deleteChain） | 5/5 PANIC，且随后 `config.Set` 均 5s 超时（锁已 wedged） | `R19-F02-red-socket-l2.log`（1739 B） |
| loader 17 类 | 16/17 PANIC（services 对照组干净报错 `service config is nil`，PASS） | `R19-F02-red-loader.log`（1885 B） |
| `OnUpdate` panic 后 `config.Set` | 死锁：`config.Set deadlocked after OnUpdate panic (globalMux wedged)` | `R19-F02-red-wedge.log`（230 B） |
| api 抽样（chain update/delete、updateService） | 3/3 FAIL（gin Recovery 返 500；updateService 另现 wedge 超时） | `R19-F02-red-api-sample.log`（385 B） |
| 真机形态沙箱 boot：基线二进制 + 毒化 gost.json | **exit 2，SIGSEGV**，栈顶 `loader.register loader.go:201`（limiter 循环）；gost.json 原样未动——重启必再崩（崩溃循环实证） | `R19-F02-red-boot.log`（996 B） |

### 4.2 绿（修后，`fix-r19-f02` 尖端）

| 集 | 结果 | 日志 |
|---|---|---|
| socket R19 回归 12 测 | 12/12 PASS（含 L2 5 形 + deleteLimiter/deleteConnLimiter/deleteServices/pause/resume/rollback×2） | `R19-F02-green-socket.log`（1081 B） |
| api R19 回归 44 断言 | 44/44 PASS（15 类 update/delete 各 15 子测 + 服务 11 测 + reload 1 测） | `R19-F02-green-api.log`（2311 B） |
| loader R19 回归 17 类 + 位置变体 | 19/19 PASS（17 子测 + 位置变体 1 测，顶测计入） | `R19-F02-green-loader.log`（1076 B） |
| config 回归（StripNil 2 测 + OnUpdate 锁 1 测） | 3/3 PASS | `R19-F02-green-config.log`（199 B） |
| 四包终验合跑 | 4/4 ok | `R19-F02-green-final.log`（164 B） |
| 真机形态沙箱 boot：修复后二进制 + 同一毒化 gost.json | **12s 后仍存活**；日志 `startup config self-heal: removed 1 nil service(s), 1 nil chain(s), 1 nil limiter(s)`；gost.json 已重写、无 null、有效项全在；SIGTERM 干净停 | `R19-F02-green-boot.log`（1734 B） |

新回归测试共 78 个 PASS 断言（12 + 44 + 19 + 3，子测计入）。

### 4.3 全量验证（修后）

- `go-gost/x`：`go test ./...` **0 FAIL**（27 包 ok，其余无测试文件）——`R19-F02-green-x-test.log`（11572 B）。
- `go-gost` 根：`go build ./...` exit 0、`go vet ./...` exit 0、`go test ./...` ok（`tests/lifecycle`）。
- `go-backend`：`go build ./...` exit 0、`go vet ./...` exit 0；`go test ./...` 16 包 ok，**1 包 FAIL（唯一红，见 §4.4）**——`R19-F02-green-backend-test.log`（6244 B）。
- `go vet`（x 四包 + go-gost 根）：exit 0。`gofmt -l`：本次改动文件 0 命中（列出的 7 个文件均为修前已存在的格式漂移，未动）。

### 4.4 唯一可接受红：沙箱 fake-IP DNS 环境红（签名写明）

`go-backend/internal/health` 的 `TestValidateMonitorTCPTarget/公网域名放行`：

```
monitor_ssrf_test.go:39: ValidateMonitorTCPTarget("example.com:443")
err=目标地址不允许（内网/保留地址）: example.com, wantErr=false
```

沙箱 fake DNS 把 `example.com` 解析到内网/保留地址，SSRF 守卫据此拒绝——与产品代码无关。本次 diff **零 go-backend 文件**（`git diff --stat` 24 个文件全在 go-gost），该失败与本次改动无因果；属任务书允许的唯一环境红。

### 4.5 两处测试期发现（均已处理，非产品问题）

1. **package main 无法放 `go test`**：`go-gost/main.go:88-101` 的 `init()` 调 `flag.Parse()`，测试二进制的 `-test.*` 参数直接致其 `os.Exit(2)`（实测 `R19-F02-red-main.log` 残留即此——该日志已删除，见 §7）。故毒化文件启动腿改由真实二进制 boot 检查（§4.1/§4.2 boot 腿）覆盖，不另立 main 包测试；此为既有结构限制，未改动 `init()`。
2. **测试二进制内 logger `atomic.Value` 类型纪律**：core logger 的 `Default` 经 `atomic.Value` 存放，同进程只能存一种具体类型；R18 测试存 `Nop`，而 `loader.Load` 内 `ParseLogger` 存 `NewLogger`——两型混存即 panic。api reload 测试因此配 `"log": {"output": "none"}` 使 `ParseLogger` 同样返回 `Nop`，与全二进制类型一致（注释写在测试内）。生产侧 `main()` 先存 `NewLogger`、`Load` 亦存 `NewLogger`，不受影响。

## 5. 全族处置表（逐类登记）

口径：**守卫** = 本次加 nil 守卫并记日志；**已覆盖** = 修前已有守卫/安全形态；**不适用** = 无配置切片解引用。运行时点守卫一律「跳过 + Warnf + 原样保留」；loader 守卫一律「干净报错」。

### 5.1 x/api 运行时点（46 处，全部守卫）

| 类 | update | delete | 其余 | 处置 |
|---|---|---|---|---|
| admission | `config_admission.go:122` | `:179` | — | 守卫 ×2 |
| auther | `config_auther.go:120` | `:177` | — | 守卫 ×2 |
| bypass | `config_bypass.go:122` | `:179` | — | 守卫 ×2 |
| chain | `config_chain.go:131` | `:188` | — | 守卫 ×2 |
| conn_limiter | `config_conn_limiter.go:121` | `:178` | — | 守卫 ×2 |
| hop | `config_hop.go:130` | `:187` | — | 守卫 ×2 |
| hosts | `config_hosts.go:122` | `:179` | — | 守卫 ×2 |
| ingress | `config_ingress.go:122` | `:179` | — | 守卫 ×2 |
| limiter | `config_limiter.go:122` | `:179` | — | 守卫 ×2 |
| observer | `config_observer.go:122` | `:179` | — | 守卫 ×2 |
| rate_limiter | `config_rate_limiter.go:122` | `:179` | — | 守卫 ×2 |
| recorder | `config_recorder.go:121` | `:178` | — | 守卫 ×2 |
| resolver | `config_resolver.go:130` | `:187` | — | 守卫 ×2 |
| router | `config_router.go:122` | `:179` | — | 守卫 ×2 |
| sd | `config_sd.go:122` | `:179` | — | 守卫 ×2 |
| service | `config_service.go:260`（单）、`:349`（批） | `:422`（单）、`:497`（批） | pause `:580/:599`、resume `:647/:704`、status `:774`、rollbackPaused `:824`、rollbackResumed `:859`、batch pause `:944/:979/:1014`、batch resume `:1088/:1173` | 守卫 ×16 |

### 5.2 x/socket 运行时点（14 处，全部守卫）

| 类 | 点 | 处置 |
|---|---|---|
| chain | updateChain `:85`、deleteChain `:115` | 守卫 ×2 |
| limiter | updateLimiter `:77`、deleteLimiter `:107`、updateConnLimiter `:195`、deleteConnLimiter `:221` | 守卫 ×4 |
| service | updateServices 第 3 阶段 `:203`、deleteServices 第 3 阶段 `:332`、pauseServices 建 map `:413` 与第 3 阶段 `:450`、resumeServices 查配置 `:505` 与第 3 阶段 `:587`、rollbackPausedServices `:638`、rollbackResumedServices `:672` | 守卫 ×8 |

### 5.3 loader 17 类（16 守卫 + 1 已覆盖）

| 类 | 处置 | 行号 |
|---|---|---|
| logger | 守卫 | `loader.go:94-97` |
| auther | 守卫 | `:108-110` |
| admission | 守卫 | `:121-123` |
| bypass | 守卫 | `:134-136` |
| resolver | 守卫 | `:151-153` |
| hosts | 守卫 | `:168-170` |
| ingress | 守卫 | `:181-183` |
| router | 守卫 | `:194-196` |
| sd | 守卫 | `:207-209` |
| observer | 守卫 | `:220-222` |
| recorder | 守卫 | `:233-235` |
| limiter | 守卫 | `:246-248` |
| conn limiter | 守卫 | `:259-261` |
| rate limiter | 守卫 | `:272-274` |
| hop | 守卫 | `:285-287` |
| chain | 守卫 | `:302-304` |
| service | 已覆盖（既有 `service config is nil`，`:320`，未动） | `:320` |

### 5.4 放大器与自愈（3 处）

| 点 | 处置 |
|---|---|
| `config.OnUpdate`（`x/config/config.go:134-145`） | 改 defer 解锁（内层函数 + defer，persist 仍在解锁后） |
| 启动/重载自愈（`go-gost/program.go:336-350`） | `StripNilConfigItems` + 重新落盘 |
| API 重载（`x/api/config_reload.go:42-51`） | 同款剥离 + 重新落盘 |

### 5.5 已覆盖 / 不适用（核查过，无改动）

| 点 | 结论 | 依据 |
|---|---|---|
| 全部 17 类 create（x/api `upsertNamed`、x/socket 内联 `existing != nil`） | 已覆盖 | R18-F02/F-04 修过；`upsertNamed`（`config_upsert.go:16-38`）对 nil 项与 nil 入项均安全 |
| `serviceConfigUnchanged`（`x/socket/service.go`） | 已覆盖 | 循环内 `current == nil` 跳过 |
| `updateServices` 第 2 阶段旧配置查找（x/socket） | 已覆盖 | `current != nil` 守卫在位 |
| `program.go reloadConfig`、`x/api/config_reload.go` 末段 | 已覆盖 | `if svc != nil` 守卫在位（本次只在 reload 入口前补剥离） |
| `x/api/config.go getConfig` | 已覆盖 | `if svc == nil { continue }` |
| `x/service/traffic_reporter.go getConfigData` | 已覆盖 | `if svc == nil { continue }` |
| 全部 17 个 `Parse*`（`x/config/parsing/*`） | 已覆盖 | 均有 `if cfg == nil` 入口守卫；loader 的 panic 点在 `Register(xCfg.Name, …)`，故守卫落在 loader 层（单点 choke） |
| `dedupeNamed` / `upsertNamed` | 已覆盖 | nil 项原样保留、不 panic（R18 测试断言在位，本次未改语义） |
| `mergeConfig`（`x/config/parsing/parser/parser.go`） | 不适用 | 只 append、不解引用；nil 靠自愈层清除 |
| `websocket_reporter.go` handleUpdate*（`:1377/:1445/:1509`） | 不适用 | 值结构（`config.ChainConfig` 等）非指针，无 nil 解引用 |
| `x/api/config_transaction.go` | 不适用 | 中间件包装，无配置切片迭代 |
| `x/socket/service.go:370` 注释掉的旧循环 | 不适用 | 注释代码，未动 |
| `config.Set` / `cloneConfigValue` | 已覆盖 | `Set` 早有 defer 解锁；reflect clone 处理 nil 指针 |

## 6. 未验证项（明文登记，留 R20）

1. **真机验证未做**：本次全部证据在沙箱；未上 Lightsail 测试机（无 R19 独占窗口）。面板在线下发 update/delete 遇 nil、SIGHUP 重载毒化文件、生产 gost.json 含 null 的启动，均未在真机复核。
2. **面板联动启动未验**：沙箱 boot 检查的 reporter 指向不可达面板（`127.0.0.1:9`），只证启动本身；启动后面板命令流（WebSocket 下发 update/delete）未在真机跑。
3. **R19 其余未验证项继续结转**：与本次无关，见 `R19-X-ledger.md` §七（SS 直连腿、远端/非 Debian 内核、浏览器 DOM 腿、kill-freeze K2、VLESS 1MB 10.21s 观察）。
4. **前端**：本次零前端改动，`tsc` 未跑（任务书允许，须写明）。

## 7. 交付文件清单（本分支一次提交，只 add 明确文件，未用 `git add -A`）

产品（24 文件，`+551/-6`）：

- `go-gost/program.go`、`go-gost/x/config/config.go`、`go-gost/x/config/dedupe.go`、`go-gost/x/config/loader/loader.go`
- `go-gost/x/api/config_admission.go`、`config_auther.go`、`config_bypass.go`、`config_chain.go`、`config_conn_limiter.go`、`config_hop.go`、`config_hosts.go`、`config_ingress.go`、`config_limiter.go`、`config_observer.go`、`config_rate_limiter.go`、`config_recorder.go`、`config_reload.go`、`config_resolver.go`、`config_router.go`、`config_sd.go`、`config_service.go`
- `go-gost/x/socket/chain.go`、`limiter.go`、`service.go`

回归测试（4 新文件）：

- `go-gost/x/socket/r19_fix_f02_test.go`（12 测）
- `go-gost/x/api/r19_fix_f02_test.go`（13 顶测 / 44 断言）
- `go-gost/x/config/loader/r19_fix_f02_test.go`（2 顶测 / 19 断言）
- `go-gost/x/config/r19_fix_f02_test.go`（3 测）

证据日志（13 文件，`audit-out-132/r19/`）：`R19-F02-red-socket-l2.log`、`R19-F02-red-loader.log`、`R19-F02-red-wedge.log`、`R19-F02-red-api-sample.log`、`R19-F02-red-boot.log`、`R19-F02-green-socket.log`、`R19-F02-green-loader.log`、`R19-F02-green-config.log`、`R19-F02-green-api.log`、`R19-F02-green-final.log`、`R19-F02-green-x-test.log`、`R19-F02-green-backend-test.log`、`R19-F02-green-boot.log`（共 13 个，见 §4）。

本报告：`audit-out-132/r19/R19-FIX-F02-REPORT.md`。

未纳入本次提交：R19 审计原始产物（`R19-L*-*`、`R19-X-*`、`R19-FIX-PLAN.md` 等）仍在主 checkout 未入库状态，不属 F-02 单项交付，由父级集成时归档；本次不合 main、不打 tag、不发版、未改任何版本字面量（`git diff --stat` 无 `skills/`、无版本文件）。

## 8. 结论

R19-X-02（P2）三件全部落地且先红后绿：60 处运行时点守卫 + 17 类 loader 守卫（1 类既有）+ `OnUpdate` defer 解锁 + 启动/API 两路自愈剥离。L2 的 5/5 panic 形不再 panic、panic 后 `config.Set` 不死锁、JSON null 落盘经自愈一次起成且文件被清理。`go build`/`go vet` 全绿，`go test ./...` 产品失败 0（唯一红为 §4.4 沙箱 fake DNS 环境红，签名已写明）。
— R19-FIX-F02-REPORT END · 自证：本行之前正文字节数 19755 · END
