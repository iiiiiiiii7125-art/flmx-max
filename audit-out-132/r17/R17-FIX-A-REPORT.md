# R17 修复 A 路报告：agent 侧去重 + 启动自愈

- 基线：`main @ 5473377`（`fix(r16): userList pagination overflow, sheetProbe null guard, drop dead inboundProbeView; archive r15/r16 audit records`）
- 分支：`fix-r17-a`（worktree `~/workspace/.fixr17-a`，从 main 切出，tip 已核对为 5473377）
- 提交：`c922e5d` `fix(r17-a): dedupe services on create + self-heal poisoned gost.json at startup`（8 文件，+951/−1；只用明确文件列表 `git add`，未用 `git add -A`）
- 未合 main、未打 tag、未发版、未改版本字面量、未改任何 `*.pb.go`
- Go 工具链：`~/go/bin/go`（go1.25.1）

## 1. 定位结论（实测，非假说复述）

毒化文件原件 `~/workspace/flvx-max/audit-out-132/r17/R17-L4-evidence/gost.json-candidate-written.json`
（md5 `e381299b2aa931d2f7c097661ead6702`）：14 个服务，`6_1_0_tcp`（第 11、13 位）、`6_1_0_udp`（第 12、14 位）各重复一次。

启动失败点已用临时探针实测 pin 死（探针已删除，不在提交内）：毒化配置进入 `loader.Load`
（`go-gost/x/config/loader/loader.go:253-270`）时，第 13 个服务（第二个 `6_1_0_tcp`）即失败——
`ParseService` 内监听器 eager 绑定同一地址，报 `listen tcp 0.0.0.0:14601: bind: address already in use`；
若地址不同则走到 `registry.ServiceRegistry().Register` 报 `registry: duplicate object`
（`go-gost/x/registry/registry.go:63-70`，`ErrDup`）。任一分支都使 `loader.Load` 返回错误 →
`program.Start`（`go-gost/program.go:71-72` `parser.Parse()` 后）返回错误 → agent 退出。
另：`loader.Load` 失败时不回滚，已注册的前 12 个服务留在注册表内（`program.Start` 的
`p.stopRuntime()` 兜底清理，行为未变）。

任务书中的机理仍按“假说，未证实”处理：本次改动不依赖它，只按 FIX-PLAN 落两件修复。
补充一个实测相关的机制注记：`createServices` 第 1 阶段的 `IsRegistered` 门只拦“已在注册表里”
的名字；服务已在配置中、但不在注册表里时（例如暂停后 `loader.Load` 跳过注册，见
`loader.go:256` 的 `paused` 跳过分支），重复 `AddService` 会直通第 4 阶段——这就是第 4 阶段
无去重 append 能毒化落盘的成立条件。

## 2. 改动一：`createServices` 第 4 阶段按名去重

文件：`go-gost/x/socket/service.go:79-111`（第 4 阶段注释 79-85 行，`originalConfig` 86 行，
`config.OnUpdate` 87 行，upsert 循环 88-108 行）。

- 原：`c.Services = append(c.Services, &ps.config)`（无去重）。
- 现：同名则替换（与 `updateServices` 第 3 阶段 `service.go:196-214` 同口径，找到则替换、
  未找到则追加）；配置里已有的多余同名副本一并清除（毒化配置 + 一次重复下发后落盘仍只留一份）。
  与 `updateServices` 的两处有意差异：循环内对 `nil` 配置项做 guard（`updateServices` 直接取
  `.Name`，`nil` 项会 panic；此处保留原项不 panic）。
- D1-06 回滚语义未动：第 4 阶段 `OnUpdate` 落盘失败仍走 `config.Set(originalConfig)` +
  注销已注册服务（`service.go:109-119`，原文未改）。

## 3. 改动二：agent 启动时落盘配置去重自愈

### 3.1 新函数（`go-gost/x/config`）

- `go-gost/x/config/dedupe.go:36-62` `DeduplicateServices(cfg *Config) []string`：保留首次出现，
  去掉后续同名项（名按 `TrimSpace` 后比较），返回被去掉的服务名（按去掉顺序）；无重复或 `nil`
  配置返回 `nil`，不改动配置；`nil` 服务项原样保留。
- `go-gost/x/config/dedupe.go:12-24` `HealPersistedServices(cfg *Config) ([]string, error)`：
  去重 + 去重后立即重新落盘；落盘失败时返回已去掉的名单与错误（内存配置已愈合，调用方只告警、
  不阻塞启动，下次启动会再次自愈）。
- `go-gost/x/config/persist.go:48-60` `PersistNow(cfg *Config) error`：不依赖 `EnablePersist`，
  把给定配置原子写入 `PersistPath()`（临时文件 + rename）；路径未设置或配置为 `nil` 时返回错误。
  `persist()` 的落盘体抽为共用的 `writeConfigFile`（`persist.go:83-118`），`persist()` 的可观测
  行为（消息文案、加锁范围）未变。

### 3.2 接线（`go-gost/program.go`）

- `program.go:318-335` `healPersistedConfig(cfg *config.Config) []string`：调
  `config.HealPersistedServices`，去重了则以 `Warn` 日志写明被去掉的服务名
  （`startup config self-heal: removed duplicate service(s) [...] from persisted config`），
  落盘失败再记一条 `Warn`，不阻塞启动。
- 调用点：`program.go:76-77`（`Start()`，`parser.Parse()` 之后、`loader.Load(cfg)` 之前）；
  `program.go:302-303`（`reloadConfig()`，SIGHUP 重载走同一加载路径，同样先自愈）。
  此时 `PersistPath()` 已就绪：`main.go:121` 的 `socket.SetConfigPersistPath("gost.json")` 在
  `svc.Run` 之前执行，且 `parser.Parse()` 内的 `cfg.Load()` 还会以 viper 解析到的实际文件
  覆盖 `PersistPath`。

## 4. 测试证据（先红后绿，日志落盘）

回归夹具：`go-gost/x/config/testdata/r17-poisoned-gost.json`，为毒化文件原件的复制件，
测试内先核对 md5 `e381299b2aa931d2f7c097661ead6702` 再使用（`dedupe_test.go:26-40`、
`loader/r17_fix_a_test.go:22-38` 各自核对）。

### 4.1 红（修前，基线 5473377）

日志：`~/workspace/.fixr17-a/audit-out-132/r17/R17-FIX-A-red.log`（6349 字节）。

- `x/socket`：`TestCreateServicesDedupesExistingConfigEntry` FAIL——内存配置同名留了 2 份；
  `TestCreateServicesDedupesAlreadyPoisonedConfig` FAIL——同名留了 3 份。毒化行为复现。
- `x/config`：`DeduplicateServices`/`PersistNow` 未定义，测试包编译失败（函数尚不存在）。
- `x/config/loader`：临时探针（已删除）实测 `loader.Load(毒化配置)` 失败，
  `listen tcp 0.0.0.0:14601: bind: address already in use`（见 §1）；正式测试文件当时因
  `DeduplicateServices` 未定义编译失败。
- `go-gost` 主包：`healPersistedConfig` 未定义，测试包编译失败。

### 4.2 绿（修后，`fix-r17-a @ c922e5d`）

日志：`~/workspace/.fixr17-a/audit-out-132/r17/R17-FIX-A-green.log`（6021 字节）。

| 测试 | 文件：行 | 结果 |
|---|---|---|
| `TestCreateServicesDedupesExistingConfigEntry`（配置已有陈旧项 + 重复下发 → 内存/落盘各 1 份） | `go-gost/x/socket/r17_fix_a_test.go:22` | PASS |
| `TestCreateServicesDedupesAlreadyPoisonedConfig`（配置已毒化 2 份 + 下发 → 内存/落盘各 1 份） | `go-gost/x/socket/r17_fix_a_test.go:86` | PASS |
| `TestDeduplicateServicesRemovesPoisonedDuplicates`（去 `6_1_0_tcp`/`6_1_0_udp`，余 12 个，保留首次出现位置） | `go-gost/x/config/dedupe_test.go:44` | PASS |
| `TestDeduplicateServicesNoDuplicatesIsNoop` | `go-gost/x/config/dedupe_test.go:72` | PASS |
| `TestPersistNowWritesDedupedConfig`（去重后落盘 12 个、无重复名；不依赖 `EnablePersist`） | `go-gost/x/config/dedupe_test.go:88` | PASS |
| `TestPersistNowWithoutPathFails` | `go-gost/x/config/dedupe_test.go:124` | PASS |
| `TestHealPersistedServices`（去掉名单 + 内存 12 个 + 落盘文件 12 个无重复） | `go-gost/x/config/dedupe_test.go:136` | PASS |
| `TestHealPersistedServicesCleanIsNoop`（干净配置不触发落盘写） | `go-gost/x/config/dedupe_test.go:178` | PASS |
| `TestLoadPoisonedConfigFailsWithDuplicate`（毒化配置加载失败，失败点定性） | `go-gost/x/config/loader/r17_fix_a_test.go:43` | PASS |
| `TestLoadHealedConfigSucceeds`（去重后加载成功，注册 12 个服务） | `go-gost/x/config/loader/r17_fix_a_test.go:70` | PASS |

### 4.3 全量验证（修后）

日志：`~/workspace/.fixr17-a/audit-out-132/r17/R17-FIX-A-fulltest.log`（11946 字节）。

- `go-gost` 模块：`go build ./...` exit 0；`go vet ./...` exit 0；
  `go test ./... -count=1`：`github.com/go-gost/gost` [no test files]、
  `github.com/go-gost/gost/tests/lifecycle` ok（3.993s）。
- `go-gost/x` 模块：`go build ./...` exit 0；`go vet ./...` exit 0；
  `go test ./... -count=1`：28 个包 ok（含 `x/socket`、`x/config`、`x/config/loader`），
  全日志 0 行 FAIL，产品失败 0。

## 5. 修后全仓同型再搜索

对 `go-gost` 两模块全仓 grep `c.Services = append` 与全部 `config.OnUpdate` 调用点，
分类如下（同型 = 服务实体上的无去重 append）：

- 已修：`x/socket/service.go:87`（本任务改动一）。
- 已有去重：`x/socket/service.go:196`（`updateServices` 第 3 阶段 upsert）。
- 安全（只替换/只过滤/只改元数据，无新增 append）：`x/socket/service.go:322`（delete）、
  `430`（pause）、`557`（resume）、`604`/`633`（回滚 helper）；
  `x/api/config_service.go:252`（updateService，只替换）、`335`（updateServices，只替换）、
  `402`/`471`（delete，过滤重建）、`564`/`659`/`769`/`799`/`938`/`1087`（pause/resume，只改 `paused` 元数据）。
- **同型残留（本次未改，见 §6 处置）**：`x/api/config_service.go:84-85`（`createService` 单个，
  无去重 append）与 `:169-171`（`createServices` 批量，无去重 append）。
- 其他实体（链/限流器等，非服务实体，不属本次同型范围，仅记录）：
  `x/socket/chain.go:34`（createChain 盲 append；updateChain `:59` 已 upsert）、
  `x/socket/limiter.go:28`/`74`/`117`/`136`/`155`（各类 create*Limiter 盲 append；对应 update 已 upsert）；
  `x/api/config_chain.go`、`config_limiter.go` 等同理。`x/config/cmd/cmd.go:186` 与
  `x/config/parsing/parser/parser.go:166` 为启动时 CLI/文件配置合并处的 append，启动自愈
  （`healPersistedConfig` 在 `Parse()` 之后）已覆盖其结果。

## 6. 需父级裁定的发现：管理 API 的两处同型盲 append

`x/api/config_service.go:84-85` 与 `:169-171` 与修前的 `createServices` 第 4 阶段形状完全一致
（`IsRegistered` 门 → 注册 → 无去重 append）。本次按任务书“改动两件”未动它们，理由与证据：

- 不可达（当前部署）：API 服务只在 `cfg.API != nil` 时才由 `program.go` 的 `run()` 构建；
  `cfg.API` 仅来自 `-api` 启动参数、`GOST_API` 环境变量或 `gost.json` 内的 `api` 段。
  全仓检索安装脚本与面板后端均未设置其中任何一项（`install.sh`/`panel_install.sh`、
  `go-backend/` 内无 `GOST_API`/`-api` 启用点）；面板与 agent 只走 WebSocket 命令通道。
  因此这两处不是 R17 的在用毒化向量。
- 若后续启用管理 API（`-api` 或 `GOST_API`），这两处会成为同一毒化向量；修复与本次改动一
  同款（upsert 替换盲 append），建议父级单独立项或纳入下一修复轮，不要与本次两件混批。

## 7. 未验证项

1. **真机端到端**：未在 Lightsail 测试机上跑——本次只在沙箱做单元/全量测试。建议父级在测试机
   上验证：把毒化 `gost.json` 放到 agent 目录后启动（应自愈启动、日志出现去重名单、落盘后 12 个
   服务），以及面板对暂停态服务重复 `AddService` 后落盘无重复。未做的原因：任务书未要求真机验证。
2. **`program.Start` 接线本身**：`healPersistedConfig` 的调用点（`program.go:77`、`303`）只有
   代码走查 + 构建验证，无直接单元测试——`go-gost` 主包的 `init()` 内有 `flag.Parse()`，
   `go test` 的 `-test.*` 参数会使其直接报错（`flag provided but not defined: -test.paniconexit0`），
   主包历来无测试文件。为此把自愈编排抽到 `x/config.HealPersistedServices` 并在 `x/config`
   内全覆盖（§4.2）；接线仅为 3 行调用 + 日志。
3. **面板侧差集补发逻辑**：任务书假说中“面板按上报服务表做差集补发”的面板侧行为未核查——
   不属 agent 侧本次范围；agent 侧现已做到重复下发不毒化、存量毒化自愈。
4. **链/限流器等其他实体的同名重复**：`DeduplicateServices` 只处理服务；`loader.Load` 对其他
   实体的同名重复仍会失败。属另一问题族，未纳入本次。

## 8. 文件清单（提交 `c922e5d`）

- `go-gost/program.go`（改，+24 行内含自愈接线与函数）
- `go-gost/x/config/dedupe.go`（新，`DeduplicateServices` + `HealPersistedServices`）
- `go-gost/x/config/persist.go`（改，+`PersistNow` 与 `writeConfigFile` 抽取）
- `go-gost/x/config/dedupe_test.go`（新，6 个测试）
- `go-gost/x/config/loader/r17_fix_a_test.go`（新，2 个测试）
- `go-gost/x/config/testdata/r17-poisoned-gost.json`（新，毒化原件复制件，md5 已核对）
- `go-gost/x/socket/service.go`（改，第 4 阶段 upsert）
- `go-gost/x/socket/r17_fix_a_test.go`（新，2 个测试）
- 日志（未提交，落盘在 worktree 内）：`audit-out-132/r17/R17-FIX-A-red.log`、
  `audit-out-132/r17/R17-FIX-A-green.log`、`audit-out-132/r17/R17-FIX-A-fulltest.log`、
  本报告 `audit-out-132/r17/R17-FIX-A-REPORT.md`。

本报告共 13213 字节（含本行）。
END
