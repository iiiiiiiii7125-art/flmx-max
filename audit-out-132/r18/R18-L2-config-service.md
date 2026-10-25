# R18-L2 · 管理 API（x/api）盲 append 再审计——新候选立案核查

- 基线：main @ `a0c18a0`（完整 `a0c18a0dc90b2f9c7a67e8a2a016cc1d97505fb5`，2026-10-25 01:33:23 +0000，「docs(r17-d): integration report + acceptance evidence (R17-X-01)」；R17 修复 fix-r17-a/b/c 已合入）。
- 任务：R18 再审计 L2 路——新候选立案核查（只读审计，不改产品代码、不打 tag、不发版）。
- 核查对象：`go-gost/x/api/config_service.go:84-85` 与 `:169-171` 的同型盲 append（与 R17-X-01 同款问题——写 gost.json 前不去重）。
- 护栏：N-01…N-37 全程有效；裁定只认落盘原始日志 + 名单存档。
- 读数方法：全部行号均以 `git show a0c18a0:` / `git grep a0c18a0` 校验。工作树 HEAD 为 `feat-latency-hint @ a99d68d`，比基线旧（a0c18a0 是 HEAD 的后代，含 fix-r17-a/b/c），故工作树读数只用于与基线 `git diff` 为空的文件；凡基线与 HEAD 有差异的文件（`program.go`、`main.go`、`socket/service.go`、`persist.go`、`control_plane.go`、`install.sh`）一律以基线版本为准。
- 依据偏差（须协调者知悉）：任务书所列依据 `R17-FIX-A/B/C/INT-REPORT.md` 在仓内不存在——全仓 `find` 仅得 `audit-out-132/r17/R17-FIX-PLAN.md`。R17-X-01 的修复内容改以 `R17-FIX-PLAN.md` + 基线内 fix-r17 合并提交（`c922e5d` fix-r17-a、`0efbbf8` fix-r17-b、`5125ff5` fix-r17-c）+ 代码实读为准，未虚构四份报告的存在。

## 0. 结论先行

| 项 | 结论 |
|---|---|
| 两处盲 append（`:85`、`:171`）| 代码在位，与 R17-X-01 **同型**（§1）|
| 两处可达性 | **均不可达**——管理 API 在安装脚本与面板后端中均无启用点；面板只走 WebSocket（§2，全集枚举 16 点）|
| 是否立案 | **立案，建议 R18-X-02，产品 P3**（§3）。R18-X-01 已被 L3/A4 路占用（A4-008 携入，P3），不得撞号；X-02/03/04 在写稿时无占用，须协调者最终确认无并发撞号 |
| 可达升级（任务项 4）| 未触发——未发现任一可达路径，不升级；升级条件见 §4 |
| 其他同型位点 | 全集枚举见 §5：services 类盲 append 仅此 2 处（另 1 处原型已修复）；**非 services 对象盲 append 18 处**，其中 **3 处经面板 WebSocket 可达**（socket chain/limiter，建议 R18-X-03，P3）、15 处与主项同一不可达 API（建议 R18-X-04，P3）|
| 实测 | 基线 agent 实测：默认启动零监听（§2 腿 A）；`-api` 启动监听在位、createService 生效（腿 B）；paused 漂移 → 盲 append → gost.json 双份落盘，**毒化实测复现**（腿 C'）；基线 R17-A 自愈去重并告警（腿 D'）。日志 `R18-L2-empirical.log` |

## 1. 两处盲 append 的代码现状（与 R17-X-01 同型判定）

### 1.1 第一处：`createService`（单条创建）——`config_service.go:84-85`

```go
// go-gost/x/api/config_service.go:60-88（基线 a0c18a0）
func createService(ctx *gin.Context) {              // 路由：POST /config/services（api.go）
	var req createServiceRequest
	ctx.ShouldBindJSON(&req.Data)                   // 注：body 直接绑进 ServiceConfig（见 §6 O-4）
	name := strings.TrimSpace(req.Data.Name)
	if name == "" { /* 400 */ }
	req.Data.Name = name
	if registry.ServiceRegistry().IsRegistered(name) {   // :65 唯一守卫：仅查 registry
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, ...))
		return
	}
	svc, err := parser.ParseService(&req.Data)       // 解析
	...
	if err := registry.ServiceRegistry().Register(name, svc); err != nil { ... }
	go svc.Serve()
	config.OnUpdate(func(c *config.Config) error {   // :84
		c.Services = append(c.Services, &req.Data)   // :85 盲 append：append 前不查 c.Services 是否已有同名
		return nil
	})
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}
```

### 1.2 第二处：`createServices` 第 4 阶段（批量创建）——`config_service.go:169-171`

```go
// go-gost/x/api/config_service.go:95-178（基线 a0c18a0）
func createServices(ctx *gin.Context) {             // 路由：POST /config/services/batch
	...
	// 第一阶段：验证所有服务配置
	for _, serviceConfig := range req.Data {
		...
		if registry.ServiceRegistry().IsRegistered(name) {   // :127 唯一守卫：仅查 registry
			... ErrCodeDup ...
		}
		parsedServices = append(parsedServices, ...)          // :138 临时切片，非配置
	}
	// 第二阶段：注册所有服务（失败回滚已注册者）
	// 第三阶段：启动所有服务
	// 第四阶段：更新配置
	config.OnUpdate(func(c *config.Config) error {   // :169
		for _, ps := range parsedServices {
			c.Services = append(c.Services, &ps.config)  // :171 盲 append：逐条 append，前无同名检查
		}
		return nil
	})
	ctx.JSON(http.StatusOK, Response{Msg: "OK"})
}
```

### 1.3 与 R17-X-01 的同型对照

R17-X-01 原型：`go-gost/x/socket/service.go` 的 `createServices` 第 4 阶段 append（R17 基线 5473377 时 `:83-85`），防重复完全依赖第 1 阶段 `IsRegistered` 检查；候选对首次启动同步时把转发 6 以重复形态写入 `/etc/flux_agent/gost.json`（378 行 vs pristine 328 行），致任何版本 agent 重启即失败。基线 a0c18a0 该处已按 R17-A 修复为同名替换（`socket/service.go:82-106`，注释 `R17-A：同名则替换……不用无去重 append`，`kept` 重建 + `c.Services = kept`，见 `:91`、`:106`）。

| 对照点 | R17-X-01 原型（5473377） | 本两处（a0c18a0） | 同型？ |
|---|---|---|---|
| 守卫 | 仅 registry `IsRegistered` | 仅 registry `IsRegistered`（`:65`、`:127`） | 是 |
| append 前查 `c.Services` 同名 | 无 | 无 | 是 |
| 写入路径 | `config.OnUpdate` → `persist()` 落盘 gost.json | 同（`x/config/config.go:134` `OnUpdate`；`persist.go` `persist()`；持久化路径 `main.go:146` `SetConfigPersistPath("gost.json")`，`program.go:95` `EnableConfigPersist()`） | 是 |
| 毒化后果 | 重名 → `loader.Load` 第二次 `Register` 失败 → 启动失败（`loader.go:265`） | 同（见 §1.4） | 是 |
| 入口 | socket WS `AddService`（面板常规下发） | x/api HTTP（§2 证实不可达） | 否（入口不同） |
| 触发机理 | 面板差集补发竞态 / 暂停后 loader 跳过注册致 config⊋registry（R17-FIX-PLAN） | **暂停漂移，确定性**（§1.4，不依赖竞态） | 机理更确定 |

**判定：同型。** 两处与 R17-X-01 是同一缺陷模式在另一入口的复现：创建时防重复完全依赖 registry，配置成员身份从不校验，append 无条件落盘。

### 1.4 毒化机理（静态证明 + 实测复现）

静态链（全部基线行号）：

1. `loader.go:257`：加载时跳过 paused 服务——`if paused, _ := svcCfg.Metadata["paused"].(bool); paused { continue }`。故 paused 服务**在配置中、不在注册表里**，config⊋registry 的漂移是加载语义本身造成的，无须竞态。
2. `config_service.go:65` / `:127`：`IsRegistered(name)` 对该名字返回 false，放行。
3. `Register` 成功（注册表里确实没有），`go svc.Serve()` 启动。
4. `:85` / `:171`：盲 append 把第二份同名服务写进内存配置；`OnUpdate` 返回后 `persist()` 立即原子写回 gost.json（`persist.go`：临时文件 + rename）。
5. 下次启动：`loader.go:265` 第二次 `Register(svcCfg.Name, ...)` 报 `registry: duplicate object`（或监听器 eager 绑定 `address already in use`，见 `dedupe.go` 注释），`loader.Load` 失败 → `program.Start` 返回错误 → agent 退出。与 R17-X-01 的“重启必死”同一后果。

缓释（基线在位，不改变“缺陷真实”的定性）：`program.go:76-77` 与 `:303` 在启动 / SIGHUP 重载时先调 `healPersistedConfig`（`program.go:318-325`），经 `x/config/dedupe.go` `HealPersistedServices` 去重并重新落盘（保留首次出现）。故基线上下次启动会被自愈、不至于自砖；但毒化文件仍已落盘，且自愈只覆盖 Services（chains/limiters 不覆盖，见 §5.2(b)）。

实测复现：见 §2 腿 C'（gost.json 双份落盘，`grep -c` = 2）与腿 D'（自愈去重 + 告警）。

## 2. 可达性：管理 API（x/api）全集枚举

### 2.1 启用门（任一成立即启用）

`go-gost/program.go:146`：`if cfg.API != nil { buildApiService(cfg.API) ... }`（`:337`）。`cfg.API` 非空的全部来源（`x/config/parsing/parser/parser.go`）：

- (a) 命令行 `-api`（`main.go:99`，`flag.StringVar(&apiAddr, "api", "", ...)`，默认 `""`；`parser.go:104-127`）；
- (b) 环境变量 `GOST_API`（`parser.go:77-81`）；
- (c) 配置文件 `api:` 段（`parser.go:55-66`：`-C` 给定的 JSON/文件，或 `cfg.Load()` 读到的 gost.json 含 `api` 键；`parser.go:66` 仅当无 `-L` 服务、无 `-api`、且 `cfg.API==nil` 时才 `Load()`）。

### 2.2 引用点全集（逐一判定，不得以“未发现”代替枚举）

| # | 引用点（基线行号） | 性质 | 判定 |
|---|---|---|---|
| R-01 | `go-gost/x/api/api.go:28` `Register(r *gin.Engine, ...)` | 路由定义：`POST /config/services`、`POST /config/services/batch` 等（`:60-71` 服务路由；其余对象路由 `:73-127`）；`GlobalInterceptor()`（`middleware.go`）要求 Basic 头，`mwBasicAuth` 无 Auther 时放行任意 Basic 凭证 | 本身不是启用点；需 HTTP server 承载。**不可达** |
| R-02 | `go-gost/x/api/service/service.go:63` `api.Register(r, ...)` | 独立 API server 构造器 `NewService`；唯一调用方 `program.go:337` `buildApiService`，受 `cfg.API != nil` 门控（`:146`） | 门控在位。**不可达** |
| R-03 | `go-gost/x/handler/api/handler.go:44` `api.Register(r, ...)` | handler 类型 `api` 的服务处理器（`init()` 注册进 HandlerRegistry）；需某服务配置的 `handler.type == "api"` 才实例化 | 面板下发的 handler 类型只有固定字面量 `"tcp"`/`"udp"`（`control_plane.go:2026-2028`，`buildForwardServiceConfigs`）与 `"relay"`（`mutations.go` `buildTunnelChainServiceConfig`）。面板无任何路径下发 `"api"`。**产品路径不可达**；残余见 §6 O-1（手工改 gost.json） |
| R-04 | `go-gost/program.go:17` `api_service "github.com/go-gost/x/api/service"` | 仅编译期 import，不启动监听 | **不可达** |
| R-05 | `go-gost/main.go:99` `-api` flag | 默认 `""`；`install.sh:476` `ExecStart=$INSTALL_DIR/flux_agent`（裸二进制，无参数）；unit 唯一 `Environment=` 是 `GODEBUG=disablethp=1`（`install.sh:477`）；`panel_install.sh` 不安装 agent（全文无 agent 相关行） | **不可达** |
| R-06 | `parser.go:77-81` `GOST_API` | 全仓 `git grep GOST_API` 仅此一处定义；安装脚本、compose、文档、unit 均未设置 | **不可达** |
| R-07 | 配置文件 `api:` 段 | gost.json 由面板 `AddService`/`UpdateService` 写 services + `OnUpdate` 全量写回；面板从不写 `api` 段；仓内无含 `"api"` 键的 gost.json 夹具（`git grep '"api"' -- '*.json'` 仅 `skills/flmx-api/package.json` 的 npm 字段，无关） | **不可达** |
| R-08 | `go-gost/config.go` `LoadConfig`（面板集成 `config.json`） | 字段仅 `addr`/`secret`/`http`/`tls`/`socks`/`proxy`，无 api | **不可达** |
| R-09 | `go-backend`（面板后端） | `/config/services` 全仓零引用；面板→节点唯一通道是 WebSocket `sendNodeCommand`（`control_plane.go:788-789`），`AddService`（`:448-465`、`:516-518`、`:556-558`，R17-B 幂等守卫）走 `socket/service.go`，不走 x/api；`go-backend` 内 HTTP client 均为对外服务（GitHub/通知/升级），无 agent 端点 | **不可达** |
| R-10 | `vite-frontend` | `/config/services` 零引用；前端不直连节点 | **不可达** |
| R-11 | `doc/`、`skills/` | 无启用管理 API 的指引；`skills/flmx-api` 是**面板自身** REST API 技能，与 agent 管理 API 无关（`SKILL.md`、references 内无 `-api`/`GOST_API`） | **不可达** |
| R-12 | `docker-compose-v4.yml`、`docker-compose-v6.yml` | 无 `GOST_API`、无 `-api`（仅 `DB_PATH`、`gost-network` 等面板相关） | **不可达** |
| R-13 | `go-gost/tests/lifecycle/lifecycle_test.go:175` | 测试内 `POST /config/services`；测试以 `-api` 显式拉起 API（`:42` `apiAddr`） | **测试专用，生产不可达** |
| R-14 | `go-gost/x/api/swagger.yaml`、`/docs` | 仅随 API server 挂载的静态文档；无 server 则不存在 | **不可达** |
| R-15 | `go-gost/x/api/config.go:117` `saveConfig`（`POST /config`） | 同一 API 内端点，随 API server 共存亡；且只写出当前内存配置，不新增服务条目 | 不构成独立启用点。**不可达** |
| R-16 | `go-gost/x/api/config.go:47` `getConfig` 内 `OnUpdate` | 只写各服务的 `Status` 字段，不 append | 不构成启用点。**不可达** |

**可达性结论：两处均不可达。** 管理 API 在安装脚本与面板后端中均无启用点；面板只走 WebSocket；三条启用门（`-api` / `GOST_API` / 配置文件 `api:` 段）在产品部署中全部关闭。任务项 4 的升级条件未触发。

### 2.3 实测腿（基线二进制，日志 `R18-L2-empirical.log`）

构建：`git archive a0c18a0 go-gost` 快照，`go1.25.1`，`CGO_ENABLED=0 go build`，产物 50,270,617 字节，`-V` 输出 `gost dev (go1.25.1 linux/amd64)`（未传 ldflags 版本，属预期）。运行目录 `~/workspace/.r18-l2-run`，`config.json` 指向不可达面板 `127.0.0.1:9`（仅使 WS 上报重试，不影响结论）。

- **腿 A（默认启动，无 `-api`）**：agent 存活；`ss -ltn` 输出**零监听**；`curl http://127.0.0.1:18080/config` → `000`、exit 7（connection refused）。——默认不起 API 监听，实测证实。
- **腿 B（`-api 127.0.0.1:18080`）**：`ss` 见 `LISTEN 127.0.0.1:18080`；`POST /config/services`（flat body，见 §6 O-4）→ `{"msg":"OK"}`；gost.json 落盘 1× `svc-a`。——`-api` 是有效启用门，实测证实；同时证实 persist 会把 `api: {addr}` 一并写进 gost.json（**启用具有粘性**，见 §6 O-2）。
- **腿 C（初版，方法纠正）**：`-api` 启动后 `pause` 报 `40004 not found`、重建只写 1 份——根因是 `parser.go:66`：传了 `-api` 时**跳过 gost.json 加载**，重启后的 agent 根本没读盘。该腿无效，方法已纠正，不计入结论；但它本身证实了 `-api` 路径不读盘这一解析语义。
- **腿 C'（纠正后，毒化复现）**：利用腿 B 落盘的 `api:` 段，**裸启动**（不传 `-api`，文件照常加载、API 由文件内 `api:` 段启用）：`GET /config/services/svc-a/status` → `running`；`POST .../pause` → `OK`，gost.json 出现 `"metadata": {"paused": true}`；重启裸启动 → `GET status` → `40004 "service svc-a not found"`（loader 已跳过 paused 服务，registry 为空）；`POST /config/services` 同名重建 → `{"msg":"OK"}`；gost.json 中 `svc-a` 计数 **2**（第 4 行 paused=true、第 17 行无 metadata）。——**盲 append 毒化落盘，实测复现。**
- **腿 D'（自愈复现）**：再次裸启动 → gost.json `svc-a` 计数回落至 **1**；日志出现 `{"level":"warning","msg":"startup config self-heal: removed duplicate service(s) [svc-a] from persisted config"}`。——基线 R17-A 自愈在位且生效，实测证实。

环境注记：任务中途沙箱 `/tmp`（512M tmpfs）曾满至 100%（首轮构建的 go build 临时目录所致），随后被清空，我方 `/tmp` 构件（含 `/tmp/r18-a0c18a0` worktree）随之消失；全部实测遂改在 `~/workspace/.r18-l2-*` 下完成。其他路的 `/tmp` worktree（如 `/tmp/r18-l5/tree`）可能同样受影响，请协调者知悉。

## 3. 立案：R18-X-02（建议）

- **建议编号：R18-X-02**（R18-X-01 已被 L3/A4 路占用，见 §0；X-02/03/04 写稿时无占用，须协调者最终确认）。
- **级别：产品 P3。** 级别理由：缺陷真实且与 R17-X-01 同型、毒化机理确定（paused 漂移，不依赖竞态，§1.4 + 腿 C' 实测）；但入口在产品部署中**不可达**（§2 全集枚举），无生产触发路径；且基线启动自愈（`HealPersistedServices`）覆盖 Services 毒化，进一步缓释。按“不可达→P3”口径，不升 P1/P2。
- **修复口径（与 R17-X-01 同款去重）**：两处 `OnUpdate` append 前按名去重，找到则替换、未找到才 append，与 `updateService`/`updateServices` 及 `socket/service.go` R17-A（`:82-106`）同口径；`createServices` 批内亦清除配置里已存在的多余同名副本（R17-A 的 `kept` 重建形态可直接复用）。建议同批覆盖 §5 的 R18-X-03/R18-X-04。
- **回归测试口径（先红后绿）**：
  1. `x/api` 去重单元测试：配置中已有同名 paused 服务（registry 未注册）时 `createService`/`createServices` 重复创建只留一份（红：落盘 2 份；绿：1 份）；
  2. 批内重复下发（同一 batch 含同名两次）只留一份；
  3. 毒化 gost.json（双份同名）经 API 路径重建后不再新增第三份；
  4. 修后 `go build` / `go vet` / `go test ./...`（go-gost）全绿。
  5. 不须测试机行为腿（入口不可达，无运行时行为面）；不须复现 harness（无生产触发路径）。

## 4. 升级条件（未触发，写明以备后查）

未发现任一可达路径，故不按任务项 4 升级。若未来出现以下任一启用点，本项**自动升级为产品 P1**（与 R17-X-01 同级：节点自砖），并须立即修复，不得按 P3 处置：

- 安装脚本/部署流程传入 `-api`；
- 部署环境设置 `GOST_API`；
- 面板或部署流程向 gost.json 写入 `api:` 段；
- 面板下发 `handler.type == "api"` 的服务（见 §6 O-1）。

## 5. 其他同型盲 append：全集枚举与逐一判定

方法：全仓 `git grep a0c18a0` 三组模式——`Services = append`（`*.go`）、`c.<Slice> = append(c.<Slice>, ...)`（`go-gost/x/api/*.go`、`go-gost/x/socket/*.go`）、`append(c.<Slice>, &req.Data)`；逐项读上下文判定是盲 append、去重感知 append，还是过滤重建。

### 5.1 services 类（严格同型：写 gost.json 的 services append）

| 位点（基线） | 形态 | 判定 |
|---|---|---|
| `x/api/config_service.go:85` | `createService` 盲 append | **盲；不可达；R18-X-02 主项** |
| `x/api/config_service.go:171` | `createServices` 第 4 阶段盲 append | **盲；不可达；R18-X-02 主项** |
| `x/api/config_service.go:409`、`:483` | `deleteService`/`deleteServices` 过滤重建（排除被删名后逐条 append） | 非盲：只删不增，不引入重复 |
| `x/api/config_service.go:252`、`:335` | `updateService`/`updateServices` 按名替换（找到首个即替换，`break`；无 append） | 非盲：不新增条目 |
| `x/socket/service.go:82-106` | `createServices` 第 4 阶段 | **已修复**（R17-A 同名替换，基线在位；原型即 R17-X-01） |
| `x/socket/service.go:209` | `updateServices` 第 3 阶段 `if !found { append }` | 非盲：去重感知（先查名，替换或补 append） |
| `x/socket/service.go:334` | `deleteServices` 过滤重建 | 非盲 |
| `x/config/cmd/cmd.go:186` | CLI `-L` 构建：`cfg.Services = append(...)`，名按 `service-%d` 合成 | 非盲（单次调用内名唯一）；但 `parser.go:185` `mergeConfig` 的 `Services: append(cfg1.Services, cfg2.Services...)` 合并 file+CLI 两源时**无去重**——生产不用 `-L`（`install.sh` 裸启动），且只影响启动时内存配置、不写盘。观察项，不立案（§6 O-3） |
| `x/config/loader/reload_test.go:52` | 测试夹具构造 | 测试，不立案 |
| `go-backend/.../mutations.go:6022`、`:6038` | `createdServices = append(...)`（`[]int64` 节点 ID 收集） | 与 gost.json 无关 |
| `x/api/config_service.go:138`、`:158`、`:890`、`:930`、`:1031`、`:1083` | `parsedServices` / `registeredServices` / `skippedServices` / `pausedServices` / `resumedServices` 临时切片 | 非配置 append |

**services 类小结：盲 append 仅 2 处（主项），原型 1 处已修复，其余全部非盲。**

### 5.2 非 services 对象·同型（同一 `OnUpdate → persist` 写盘路径，同一 registry-only 守卫）

**（a）x/api 其余 15 类对象的 create 盲 append——均不可达（与主项同一 API），建议 R18-X-04（P3）：**

| 文件 | 行 | 切片 | 守卫（同型：仅 registry） |
|---|---|---|---|
| `config_admission.go` | `:60` | `c.Admissions` | `:47` `AdmissionRegistry().IsRegistered` |
| `config_auther.go` | `:59` | `c.Authers` | `:47` `AutherRegistry().IsRegistered` |
| `config_bypass.go` | `:60` | `c.Bypasses` | `:47` `BypassRegistry().IsRegistered` |
| `config_chain.go` | `:65` | `c.Chains` | `:48` `ChainRegistry().IsRegistered` |
| `config_conn_limiter.go` | `:59` | `c.CLimiters` | `:47` `ConnLimiterRegistry().IsRegistered` |
| `config_hop.go` | `:65` | `c.Hops` | `:48` `HopRegistry().IsRegistered` |
| `config_hosts.go` | `:60` | `c.Hosts` | `:47` `HostsRegistry().IsRegistered` |
| `config_ingress.go` | `:60` | `c.Ingresses` | `:47` `IngressRegistry().IsRegistered` |
| `config_limiter.go` | `:60` | `c.Limiters` | `:47` `TrafficLimiterRegistry().IsRegistered` |
| `config_observer.go` | `:60` | `c.Observers` | `:47` `ObserverRegistry().IsRegistered` |
| `config_rate_limiter.go` | `:60` | `c.RLimiters` | `:47` `RateLimiterRegistry().IsRegistered` |
| `config_recorder.go` | `:59` | `c.Recorders` | `:47` `RecorderRegistry().IsRegistered` |
| `config_resolver.go` | `:64` | `c.Resolvers` | `:47` `ResolverRegistry().IsRegistered` |
| `config_router.go` | `:60` | `c.Routers` | `:47` `RouterRegistry().IsRegistered` |
| `config_sd.go` | `:60` | `c.SDs` | `:47` `SDRegistry().IsRegistered` |

毒化后果同级：`loader.go:94-265` 对各类对象同样 `Register` 失败即 `Load` 失败（重名即启动失败）。各文件 `update*` 均为按名替换（无 append，如 `config_chain.go:130-140`，先 `!IsRegistered` 报错再原地替换）、`delete*` 为过滤重建（`:174` 行 `c.X = append(c.X, s)`）——均非盲。建议与 R18-X-02 同批修复（同一去重口径逐类复用），单独立案 R18-X-04、P3，理由与主项同（不可达）。

**（b）x/socket 的 create 盲 append——3 处经面板 WebSocket 可达，建议 R18-X-03（P3）：**

| 位点（基线） | 函数 / 面板命令 | 形态 |
|---|---|---|
| `x/socket/chain.go:35` | `createChain` / 面板 `AddChains`（`mutations.go:6048`，`tolerateExists=true`） | 盲 `c.Chains = append`；守卫仅 `:19` `ChainRegistry().IsRegistered` |
| `x/socket/limiter.go:29` | `createLimiter` / 面板 `AddLimiters`（`control_plane.go:2277`、`:2299`） | 盲 `c.Limiters = append`；守卫仅 `:24` `TrafficLimiterRegistry().IsRegistered` |
| `x/socket/limiter.go:118` | `createConnLimiter` / 面板 `AddCLimiters`（`control_plane.go:2195`） | 盲 `c.CLimiters = append`；守卫仅 `:113` `ConnLimiterRegistry().IsRegistered` |

可达性：面板常规下发即达（隧道建链发 `AddChains`、限速下发发 `AddLimiters`/`AddCLimiters`；already-exists 时面板回退 `Update*`，具幂等形态）。**但触发机理未立**，故不升 P1：

- WS 变更命令在 agent 侧串行处理：`websocket_reporter.go:1011-1021` `runMutationCommands` 单消费者 draining `mutationQueue`（`:242` 单次启动）；`dispatchCommand` 注释明言“Mutations share process-wide registries and configuration, so running them concurrently can corrupt the persisted config”。两条同名 `AddChains`/`AddLimiters` 无法交错 `IsRegistered → Register → append` 序列，第二条必见 `already exists`。
- chains/limiters 无 paused 式 loader 跳过：`loader.go` 对 chains（`:245`）、limiters（`:201`、`:210`、`:219`）全量注册，无 config⊋registry 的已知漂移源。
- 因此：可达但无已证实的触发路径，定 P3；修复口径与 R17-X-01 同款（append 前按名去重）。另须注意不对称风险：启动自愈 `HealPersistedServices` **仅覆盖 Services**（`dedupe.go`），chains/limiters 一旦毒化即无自愈、重启必死——建议修复时把自愈扩展到 chains/limiters，或至少在修复说明中登记该不对称。

同文件的非盲位点：`updateChain :69`、`updateLimiter :60`、`updateConnLimiter :143` 均为 replace-or-append（`found` 则替换，否则 append）；`deleteChain :90`、`deleteLimiter :81`、`deleteConnLimiter :162` 均为过滤重建。

**（c）其他 `OnUpdate` 位点（已核查，非盲）：** `x/api/config.go:47` `getConfig` 只写 `Status`；`config.go:117` `saveConfig` 只写出内存配置；`x/socket/service.go` 的 pause/resume 各 `OnUpdate` 只改 `metadata["paused"]`。

### 5.3 计数

- services 盲 append：**2 处**（R18-X-02），原型 1 处已修复；
- 其他对象盲 append：**18 处**（x/api 15 处 R18-X-04 + x/socket 3 处 R18-X-03）；
- 其中**可达 3 处**（均为 x/socket，经面板 WS 常规下发，但触发机理未立）、**不可达 17 处**（同一 x/api 管理 API）。

## 6. 观察项（不立案）

- **O-1（handler `api` 服务向量）**：gost.json 手工写入 `handler.type == "api"` 的服务即可暴露管理 API；且无 Auther 配置时 `middleware.go` 的 `mwBasicAuth`（`auther == nil` 即放行）+ `GlobalInterceptor`（只要求 Basic 头存在）意味着任意 Basic 凭证均可调用。手工改文件属运维动作、非产品路径，不立案；建议后续在文档或启动校验中提示。
- **O-2（启用粘性）**：腿 B 实测——`persist()` 会把 `cfg.API` 一并写进 gost.json；一旦用 `-api` 启动过一次，后续裸启动也会因文件内 `api:` 段而继续启用 API。生产从未传 `-api`，故不影响本次可达性结论；但任何一次人工 `-api` 调试都会留下粘性启用点，运维须知。
- **O-3（`mergeConfig` 无去重）**：`parser.go:185` 合并 file+CLI 两源时 `Services`/`Chains`/… 均无去重；仅启动时内存形态、不写盘，且生产不用 `-L`。不立案。
- **O-4（`createService` 的 body 形态与 swagger 不符）**：腿 B 初版以 `{"data": {...}}` 调用得 `40001 "service name is required"`——handler 实为 `ShouldBindJSON(&req.Data)`（body 直接是 ServiceConfig），而 swagger 注解称 body 为 `{data: ...}`。属 API 文档/实现不一致的旁支发现，未深究，不立案；修复本项时可顺带对齐。
- **O-5（`saveConfig` 写任意路径）**：`POST /config` 接受 `?path=` 任意写出配置文件。同一不可达 API 内，不立案。
- **O-6（依据缺失）**：见 §0——`R17-FIX-A/B/C/INT-REPORT.md` 不存在。

## 7. 证据清单

- 行号证据：本报告全部行号均引自 `git show a0c18a0:` / `git grep a0c18a0`，工作树 HEAD（`a99d68d`）比基线旧，凡差异文件以基线为准。
- 实测日志：`audit-out-132/r18/R18-L2-empirical.log`（腿 A/B/C/C'/D' 全程输出；构建产物 50,270,617 字节，`go1.25.1`，`CGO_ENABLED=0`）。
- 枚举命令：`git grep -n 'config/services' a0c18a0`；`git grep -n 'go-gost/x/api' a0c18a0`；`git grep -n 'GOST_API' a0c18a0`；`git grep -n 'Services = append' a0c18a0 -- '*.go'`；`git grep -n -E 'c\.[A-Za-z]+ = append\(c\.[A-Za-z]+' a0c18a0 -- 'go-gost/x/api/*.go' 'go-gost/x/socket/*.go'`；`git grep -n -E 'append\(c\.[A-Za-z]+, &req\.Data\)' a0c18a0 -- 'go-gost/x/api/*.go' 'go-gost/x/socket/*.go'`。
- 未做：未改产品代码、未打 tag、未发版；未跑测试机行为腿（入口不可达，无行为面）；未在生产机操作。
— R18-L2-config-service END · 自证：本行之前正文字节数 27620 · END
