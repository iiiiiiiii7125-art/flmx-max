# R18 修复 F-03 报告：x/socket 链/限流器去重 + 启动自愈扩展

- 基线：`main @ a0c18a0`（`a0c18a0dc90b2f9c7a67e8a2a016cc1d97505fb5`）
- 分支：`fix-r18-f03`（worktree `~/workspace/.fixr18-f03`，从 main 切出，tip 已核对为 a0c18a0）
- 提交：`3a1eb2d` `fix(r18-f03): dedupe chains/limiters on create + self-heal poisoned gost.json at startup`（7 文件，+739/−17；只用明确文件列表 `git add`，未用 `git add -A`）
- 未合 main、未打 tag、未发版、未改版本字面量、未改任何 `*.pb.go`、未动 `go-gost/x/api/`（F-02/F-04 的路）与 `go-backend/`（F-01 的路）
- Go 工具链：`~/go/bin/go`（go1.25.1）
- 报告与日志落盘在 worktree 内（R17-A 同例：报告未进本次提交，由集成归档；`*.log` 在 `.gitignore` 内，不可提交）：
  - 报告：`audit-out-132/r18/R18-FIX-F03-REPORT.md`（本文件）
  - 红日志：`audit-out-132/r18/R18-FIX-F03-red.log`（3223 字节）
  - 绿日志：`audit-out-132/r18/R18-FIX-F03-green.log`（8731 字节）
  - 全量日志：`audit-out-132/r18/R18-FIX-F03-fulltest.log`（11958 字节）

## 1. 定位结论（实测，非假说复述）

三处盲 append 的毒化机理与 R17-X-01 同型，启动失败点已用测试实测 pin 死：

- `createChain` / `createLimiter` / `createConnLimiter` 先查注册表（`IsRegistered` 门），再 `config.OnUpdate` 里盲 append。链/限流器已在配置中、但不在注册表里时（与 R17-A 的服务漂移同型：暂停/loader 跳过/注册表与配置漂移），面板一次重复下发就会把第二份同名项写进配置并落盘，毒化 `gost.json`。红测复现：陈旧项 + 一次重复下发后内存里留了新旧两份（见 §4.1）。
- 毒化文件进 `loader.Load`（`go-gost/x/config/loader/loader.go:200-213` 限流器、`240-248` 链）时，第二个同名项在 `registry.Register` 处报 `registry: duplicate object`（`x/registry/registry.go:63-70`，`ErrDup`）→ `loader.Load` 返回错误 → `program.Start` 返回错误 → agent 退出。实测：`TestLoadPoisonedChainsLimitersFailsWithDuplicate` 断言错误恰为 `registry.ErrDup`（见 §4.2）。与服务毒化的唯一差异：链/限流器无监听器 eager 绑定，故只有 `duplicate object` 这一支，没有 `address already in use` 分支。
- V-04 的「触发机理未立」仍成立：本次修复不依赖触发机理，只堵住毒化向量（重复下发不再落盘）与后果（毒化文件启动自愈），与 R17-A 的处置一致。面板 WebSocket 常规下发可达性未变（`websocket_reporter.go:1354-1355` AddChain、`1422-1423` AddLimiter、`1489-1490` AddConnLimiter 均调这三处）。

## 2. 改动一：三处 create 按名去重（upsert）

口径（与 `service.go` 的 R17-A 改动一致，并写明）：**同名则替换**（新下发内容覆盖旧项），不是跳过；配置里已有的多余同名副本一并清除；名字按 `TrimSpace` 后比较；`nil` 项做 guard、原样保留不 panic。

与各自 `update*` 的关系（`updateChain` / `updateLimiter` / `updateConnLimiter` 本就“找到则替换、未找到则追加”，本次 create 改为同一条规则），两处有意差异：

- 名字比较用 `TrimSpace`（`update*` 用裸 `==`；落盘旧文件里的名字可能带空白；与 `DeduplicateServices` / 新增的三类去重同口径）。
- `nil` 配置项 guard（`update*` 直接取 `.Name`，`nil` 项会 panic；此处保留原项不 panic，按任务书 R17-A 的处置）。`update*` 本身的 `nil` 风险未动（见 §6 残余项）。

| 位置 | 改动 |
|---|---|
| `go-gost/x/socket/chain.go:34-60`（`createChain` 的 `config.OnUpdate`，注释 :35-40，upsert 循环 :41-57，`c.Chains = kept` :58） | 盲 `append` 改为 upsert：首个同名项替换为新下发内容，其余同名副本丢弃，`nil` 项原样保留；未找到则追加 |
| `go-gost/x/socket/limiter.go:28-53`（`createLimiter`，注释 :29-34，`c.Limiters = kept` :51） | 同上，对象为 `c.Limiters`（与 `updateLimiter` 同口径） |
| `go-gost/x/socket/limiter.go:139-164`（`createConnLimiter`，注释 :140-145，`c.CLimiters = kept` :162） | 同上，对象为 `c.CLimiters`（与 `updateConnLimiter` 同口径） |

三处均只改 `OnUpdate` 闭包内行为：注册表门、解析、注册顺序与落盘失败语义未变；`create*` 没有 `createServices` 的 D1-06 式回滚（基线如此，本次不引入）。

## 3. 改动二：启动自愈扩展到 chains / limiters / climiters

### 3.1 评估结论：三类均可扩展，无结构性障碍

按 FIX-PLAN 口径逐项评估，三类全部扩展，理由是四类共享同一结构：

- 同一落盘文件：`Chains` / `Limiters` / `CLimiters` 与 `Services` 同在 `config.Config`（`x/config/config.go:643`、`654-655`），同一 `gost.json`、同一 `PersistNow` 原子写路径。
- 同一失败点：`loader.Load` 里三类都在 `Register` 处报 `ErrDup` 即返回错误（见 §1），自愈的“先去重再加载”对三类同样成立。
- 同一接线点：`program.go` 的 `healPersistedConfig` 在 `parser.Parse()` 之后、`loader.Load(cfg)` 之前（`Start` :76-78；SIGHUP `reloadConfig` :303-305），`PersistPath()` 此时已就绪（R17-A 已论证），三类去重无需新接线。

### 3.2 新函数（`go-gost/x/config/dedupe.go`）

`DeduplicateServices` / `HealPersistedServices` 保持原实现不动（R17 已证稳，零回归风险）。三类新函数共用泛型 helper `dedupeNamed`（`:157-178`，first-wins，`TrimSpace` 比较，`nil` 项原样保留；无重复返回 `(nil, nil)`，调用方不碰配置）：

| 函数 | 行号 | 行为 |
|---|---|---|
| `DeduplicateChains` | `dedupe.go:88-98` | `cfg.Chains` 去重，返回被去掉的链名（未 trim 的原名，按去掉顺序） |
| `DeduplicateLimiters` | `dedupe.go:114-124` | `cfg.Limiters` 去重，返回被去掉的限流器名 |
| `DeduplicateCLimiters` | `dedupe.go:140-150` | `cfg.CLimiters` 去重，返回被去掉的连接限流器名 |
| `HealPersistedChains` / `HealPersistedLimiters` / `HealPersistedCLimiters` | `dedupe.go:74-86` / `:100-112` / `:126-138` | 与 `HealPersistedServices` 同口径：去重 + 去重后立即 `PersistNow`；落盘失败返回已去掉名单与错误（内存已愈合，调用方只告警、不阻塞启动，下次启动再次自愈）；干净配置不触发写 |

### 3.3 接线（`go-gost/program.go`）

- `healPersistedConfig`（`:330-351`，注释 `:318-329`）：依次调四类 `HealPersisted*`（services → chains → limiters → conn limiters），按类记一条 `Warn`（`startup config self-heal: removed duplicate <kind>(s) [...] from persisted config`），落盘失败再记一条 `Warn`，不阻塞启动；返回四类合并的被去掉名单。services 的 Warn 行文案与 R17-A 逐字一致（`heal("service", …)` 产出同一行）。
- 调用点未变：`Start()` `:76-78` 与 `reloadConfig()` `:303-305`（注释各补一句 R18-F03 扩展说明）。
- 实现取舍（已记录，供复审）：四类各调一次 `Heal*`、各落盘一次，毒化多类时最多 4 次原子写；最终落盘文件含全部四类的愈合结果。若某类落盘失败，内存仍已愈合、Warn 已记，下次启动再次自愈——与 R17-A 的 services 语义一致。

## 4. 测试证据（先红后绿，日志落盘）

### 4.1 红（修前，基线 a0c18a0）

日志：`audit-out-132/r18/R18-FIX-F03-red.log`（3223 字节）。

- `x/socket`：3 个新测试全 FAIL——陈旧项 + 一次重复下发后内存配置里新旧两份并存（断言在“应为新下发内容”处失败，内存项仍为陈旧内容 `Hops: nil` / `Limits: ["$ 1"]`），毒化行为复现。
- `x/config`：`DeduplicateChains` / `DeduplicateLimiters` / `DeduplicateCLimiters` / `HealPersisted*` 未定义，测试包编译失败（函数尚不存在）。
- `x/config/loader`：同上，测试包编译失败（`config.Deduplicate*` 未定义）；毒化加载失败点在绿测中才可断言（见 §4.2）。
- `go-gost` 主包：未放测试文件——`main.go:init()` 调 `flag.Parse()`（`main.go:101`），`go test` 的 `-test.*` 参数会被当作未定义 flag 拒绝，**主包内任何测试都跑不起来**（实测 `flag provided but not defined: -test.paniconexit0`）。这是基线既有的结构性障碍，非本次引入；R17-A 同样未在主包放测试（其提交 c922e5d 的 8 文件中无主包测试）。`healPersistedConfig` 的接线因此只由 §4.2 的三类 Heal/loader 测试 + §5 的读码核对覆盖（见 §7 未验证项第 2 条）。

### 4.2 绿（修后，`fix-r18-f03 @ 3a1eb2d`）

日志：`audit-out-132/r18/R18-FIX-F03-green.log`（8731 字节），`--- FAIL` 0 行。

| 测试 | 文件：行 | 结果 |
|---|---|---|
| `TestCreateChainDedupesExistingConfigEntry`（配置已有陈旧链 + 重复下发 → 内存/落盘各 1 份，且为新内容 hop-fresh） | `go-gost/x/socket/r18_fix_f03_test.go:23` | PASS |
| `TestCreateLimiterDedupesExistingConfigEntry`（陈旧限流器 + 重复下发 → 内存/落盘各 1 份，Limits 为新值 `$ 2`） | `go-gost/x/socket/r18_fix_f03_test.go:97` | PASS |
| `TestCreateConnLimiterDedupesExistingConfigEntry`（陈旧连接限流器 + 重复下发 → 内存/落盘各 1 份，Limits 为新值 `$ 2`） | `go-gost/x/socket/r18_fix_f03_test.go:157` | PASS |
| `TestDeduplicateChainsRemovesDuplicates`（去重、保留首次出现位置、nil 项保留、`" chain-a "` 按 trim 后判重） | `go-gost/x/config/r18_fix_f03_test.go:18` | PASS |
| `TestDeduplicateChainsNoDuplicatesIsNoop`（无重复 noop；`nil` 配置返回 nil） | `go-gost/x/config/r18_fix_f03_test.go:44` | PASS |
| `TestDeduplicateLimitersRemovesDuplicates`（去重、nil 项保留、nil 配置返回 nil） | `go-gost/x/config/r18_fix_f03_test.go:57` | PASS |
| `TestDeduplicateCLimitersRemovesDuplicates`（去重、nil 配置返回 nil） | `go-gost/x/config/r18_fix_f03_test.go:79` | PASS |
| `TestHealPersistedChainsLimiters`（三类各去 1 份 + 内存 2/1/2 + 落盘文件同数无重复） | `go-gost/x/config/r18_fix_f03_test.go:99` | PASS |
| `TestHealPersistedChainsLimitersCleanIsNoop`（干净配置三类均不触发写） | `go-gost/x/config/r18_fix_f03_test.go:182` | PASS |
| `TestLoadPoisonedChainsLimitersFailsWithDuplicate`（毒化配置三类各两份 → `loader.Load` 失败且错误恰为 `registry.ErrDup`，即启动失败点） | `go-gost/x/config/loader/r18_fix_f03_test.go:61` | PASS |
| `TestLoadHealedChainsLimitersSucceeds`（去重后加载成功，三类注册表各 1 项） | `go-gost/x/config/loader/r18_fix_f03_test.go:77` | PASS |

R17 回归重跑（同在绿日志内，`--- FAIL` 0 行）：`TestDeduplicateServicesRemovesPoisonedDuplicates` / `TestDeduplicateServicesNoDuplicatesIsNoop` / `TestPersistNowWritesDedupedConfig` / `TestPersistNowWithoutPathFails` / `TestHealPersistedServices` / `TestHealPersistedServicesCleanIsNoop`（R17 毒化原件夹具，md5 `e381299b2aa931d2f7c097661ead6702` 核对在位）、`TestCreateServicesDedupesExistingConfigEntry` / `TestCreateServicesDedupesAlreadyPoisonedConfig`、`TestLoadPoisonedConfigFailsWithDuplicate` / `TestLoadHealedConfigSucceeds`——现有 Services 自愈行为不变。

## 5. 修后全量验证（修后）

日志：`audit-out-132/r18/R18-FIX-F03-fulltest.log`（11958 字节）。

- `go-gost` 模块：`go build ./...` exit 0；`go vet ./...` exit 0；`go test ./... -count=1`：`github.com/go-gost/gost` [no test files]、`tests/lifecycle` ok（17.965s）。
- `go-gost/x` 模块：`go build ./...` exit 0；`go vet ./...` exit 0；`go test ./... -count=1`：27 个包 ok（含 `x/socket`、`x/config`、`x/config/loader`）。
- 合计 ok 包 28 个，与 R17-A 全量日志的 ok 包集合逐项比对差集为 ∅；全日志 0 行 FAIL，产品失败 0。
- `gofmt -l` 改动的 4 个文件：无输出（格式干净）。唯一工具链告警是 `go` 的 `GOPATH and GOROOT are the same directory` 提示（环境既有，与本次改动无关）。

## 6. 需父级裁定的发现 / 残余项（本次未改）

1. **RLimiters（rate limiter）未进自愈**：`x/socket` 内无任何 rate limiter 的 create 路径（全仓 grep `RLimiter|RateLimiter` 在 `x/socket/*.go` 零命中；`websocket_reporter.go` 亦无 AddRateLimiter），故 F-03 的三处之外没有第四处 socket 盲 append。但 `loader.Load` 会加载 `cfg.RLimiters`（`loader.go:215-223`），毒化文件里若有同名 RLimiters 同样在 `Register` 处报 `ErrDup`、agent 退出，而本次自愈不覆盖它。处置建议：该类只能经配置文件或管理 API 进入（F-04 的 `x/api` rate_limiter create 在其修复范围内），是否把 `RLimiters` 也纳入自愈由父级定——补一个 `DeduplicateRLimiters` + `HealPersistedRLimiters` 是同款 20 行工作，本次按任务书口径（chains/limiters）未擅自扩大。
2. **`updateChain` / `updateLimiter` / `updateConnLimiter` 的 `nil` 项 panic 仍在**：三处 update 的 `OnUpdate` 闭包直接取 `c.Chains[i].Name` 等（`chain.go:82`、`limiter.go:72`、`:180`），配置里有 `nil` 项即 panic；本次只给 create 路径加了 guard（任务书口径）。`nil` 项进落盘文件本身需经 `OnUpdate` 写盘，正常下发路径不会产生，风险低，是否顺手修由父级定。
3. **自愈多类落盘为多次原子写**（见 §3.3）：毒化多类时最多 4 次写；中途崩溃只会留下“部分类已愈合”的文件，下次启动继续自愈，无不一致窗口。单次合并写是可选优化，未做。

## 7. 未验证项（按任务书：不碰测试机，一律留 R19 独占窗口）

1. **真机端到端**：未在 Lightsail 测试机上跑——本次只在沙箱做单元/全量测试。待 R19 独占窗口验证：毒化 `gost.json`（链/限流器重复）上测试机后 agent 一次起成、journal 出现 `removed duplicate chain(s)/limiter(s)` 告警行；面板一次重复 AddChain/AddLimiter 下发后落盘仅一份；SIGHUP 重载走同一自愈路径。
2. **`healPersistedConfig` 的主包接线无可运行测试**：`main.go:init()` 的 `flag.Parse()` 使主包测试二进制无法启动（见 §4.1），R17-A 同病。接线正确性目前由读码核对（`Start` :76-78 与 `reloadConfig` :303-305 均在 `loader.Load` 之前调 `healPersistedConfig`）+ 三类 Heal/loader 单元测试间接覆盖；真机启动验证（第 1 条）是该接线的直接证据。
3. **面板 WebSocket 实下发链路**：重复 AddChain/AddLimiter/AddConnLimiter 的实下发只在沙箱经 `create*` 直调验证，未走面板 → agent 的 WebSocket 加密命令通道实测；留 R19。
4. R17 修复报告 §10 剩余未验证项（远端/非 Debian 内核、浏览器 DOM 腿、订阅 1 流量计数器响应性）继续结转，与本次无关。
— R18-FIX-F03-REPORT END · 自证：本行之前正文字节数 14954 · END
