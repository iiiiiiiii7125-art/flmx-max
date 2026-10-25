# R19-L2 未决缺口裁定 + 文档抽查

- 基线（pin 死）：main @ `d66042c`（`d66042cc0b7ac011ddfad09f3f2709b90dafda2e`）；读数树 `~/workspace/.r19-tree` @ 同提交（`git rev-parse HEAD` 核对一致）；R18 修复 fix-r18（三路 + INT 补丁）已含于该基线。
- 依据：读数树内 `audit-out-132/r18/R18-FIX-INT-REPORT.md` §6（未验证项 2–5）、`R18-FIX-F01-REPORT.md` §7、`R18-FIX-F02F04-REPORT.md` §6、`R18-FIX-F03-REPORT.md` §6、`R18-X-verdicts.md`。
- 护栏：N-01…N-37；只读审计——未改产品代码、未打 tag、未发版、未碰测试机。
- 方法：代码 pin 死行号 + 沙箱复现。复现一律在 `/tmp` 内的基线复本上跑（`/tmp/l2-gost`、`/tmp/l2-backend`，`cp -a` 自读数树），原树零写；复现测试源码与原始日志随本报告归档于 `audit-out-132/r19/`（见 §7）。Go 工具链 `~/go/bin/go`（go1.25.1）。
- 结论总览：立案 2 项（L2-CAND-01 P3、L2-CAND-02 P2）；不立案 3 项（第 3 项纯测试覆盖缺口；第 4、5 项登记观察）。

## 1. F-01 nft 分支同类缺口——立案 L2-CAND-01，产品 P3

与 R18-X-01 同类（同一缺陷型：前进式多步删除中途硬失败、回执不披露已删进度），且 F-01 只修了 gost 分支、两处同类点原样残留。证据（基线树行号）：

### 1a. nft 分支删库失败仍返裸 err

`go-backend/internal/http/handler/tunnel_delete_resolution.go:787-790`：

```go
for i := range forwards {
    if err := h.deleteForwardByID(forwards[i].ID); err != nil {
        log.Printf("forward delete: db delete after nft reconcile failed ...")
        return err   // 裸 err，无已删披露
    }
}
```

到达该点时：入口节点 reconcile 已成功（全部 nft 规则已下线）、此前循环的转发库行已删。回执路径直通 `tunnelDelete`（`mutations.go:2590`，`response.Err(-2, err.Error())`），`deleteForwardByID` 的原始 DB 错误原样进 `msg`，运维只看到删库错误、不知道已删几条、数据面已全下线。同一分支内自相矛盾：reconcile 失败点（`:784`）已披露计数（`节点不可达，%d 条转发已加入待清理队列，后台将自动重试，%s未删除`），删库失败点却不披露；gost 分支三处失败点（`:805` 离线删库、`:811` 数据面、`:816` 删库）F-01 已全部改用 `formatDeletedForwardsSummary` 披露。日志（`:788`）不等于回执（V-06 口径：日志不得作回执依据）。

同函数同分支的邻接点（同候选修复范围，不另立案）：`MarkForwardPendingDelete` 失败（`:770-772`）同样 `return err`——此时部分转发已标 pending、部分未标，数量亦未披露。

R18-F01 的 9 个新测试无一覆盖 nft 分支删库失败（该文件内 `nft` 命中仅为借 `nft_counter_state` 删表模拟 gost 分支删库失败，见 `r18_f01_tunnel_delete_disclosure_test.go:33,80,379`）。

### 1b. `deleteTunnelByID` 失败时「转发已全删」未披露

`deleteTunnelAndCleanup`（`tunnel_delete_resolution.go:831-833`）：

```go
if err := h.deleteTunnelByID(tunnelID); err != nil {
    return err   // 裸 err；此前 cleanupTunnelForwardsDataPlane 已返回 nil，即全部转发已删
}
```

单删路径同病（`mutations.go:2606-2610`）：`cleanupTunnelForwardsDataPlane` 成功后 `deleteTunnelByID` 失败，回执 `Err(-2, err.Error())`；该处注释（`:2607`）自认「转发已全删、删隧道行失败时同理补刷」——白名单补刷做了，回执披露没做。`delete-with-forwards` 路径（`:391/:404/:465`）经同一 `deleteTunnelAndCleanup`，同病。修复形状与 F-01 相同：复用 `formatDeletedForwardsSummary`（或「全部转发已删除」定额文案）补披露，不动控制流与不回滚语义。

定级 P3（与 R18-X-01 同级）：仅回执披露缺失，不改数据面/库语义；触发需删库腿硬失败（低频），但一旦触发运维会被误导以为「什么都没删」而重复操作。

## 2. `update*` nil panic 仍在——立案 L2-CAND-02，产品 P2

F-03 报告 §6 第 2 项所指三处经复现证实，且同型面远大于三处：update*/delete*/pause/resume/status 全族 + loader 全族 + 一个放大器（`OnUpdate` 非 defer 解锁）。

### 2a. 复现：update*/delete* 在 nil 项上 panic（沙箱复本实测）

复现测试 `R19-L2-nil-socket_test.go.txt`（配置中置 `[nil, 目标项]`，调对应 update/delete）：

| 函数 | 位置 | 结果 |
|---|---|---|
| `updateChain` | `go-gost/x/socket/chain.go:85`（`c.Chains[i].Name`） | PANIC：`runtime error: invalid memory address or nil pointer dereference` |
| `updateLimiter` | `go-gost/x/socket/limiter.go:75`（`c.Limiters[i].Name`） | PANIC，同上 |
| `updateConnLimiter` | `go-gost/x/socket/limiter.go:182`（`c.CLimiters[i].Name`） | PANIC，同上 |
| `updateServices` | `go-gost/x/socket/service.go:202`（`c.Services[j].Name`，第 3 阶段） | PANIC，同上 |
| `deleteChain` | `go-gost/x/socket/chain.go:110`（`s.Name`） | PANIC，同上 |

日志：`R19-L2-nil-socket.log`（5 项全现；各单测逐个进程跑——全量同进程跑会在首个 panic 后卡死，见 §2d）。

同型普查（读码核对，未逐项跑，均无 nil guard）：
- `x/socket` 另 4 处：`deleteLimiter`（`limiter.go:100`）、`deleteConnLimiter`（`limiter.go:203`）、`deleteServices`（`service.go:328`）、`resumeServices`（`service.go:483`，`s.Name`）。
- `x/api` 全部 17 类 `update*` handler（`config_service.go:259` `updateService`、`:343` `updateServices`；其余 15 类 `config_<kind>.go` 各 1 处，约 `:119-:131`）与全部 17 类 `delete*` handler（`config_service.go:411/:482`；其余 15 类约 `:171-:183`）均直接取 `c.X[i].Name` / `s.Name`；`config_service.go` 另有 `pauseService`（`:557/:571`）、`resumeService`（`:614/:666`）、`getServiceStatus`（`:731`）、`rollbackPausedServices`（`:776`）6 处同型。合计 `x/api` 约 40 处、`x/socket` 9 处。
- 对照：三处 create 已在 F-03 加 guard（`existing != nil`），`serviceConfigUnchanged`（`service.go:264`）与 `reloadConfig`（`program.go:310-313`）亦有 `!= nil` 守卫——同文件内口径不一。

### 2b. 可达性：正常下发流注不进 nil；唯一注入向量是落盘文件含 JSON null，而该路径上 loader 先 panic

- 面板 WebSocket / 管理 API 的请求体绑进值结构（`req.Data` 为值非指针），create/update 只 append/replace 非 nil 项；`dedupeNamed`/`upsertNamed` 保留 nil 但不产生 nil。故正常流无法把 nil 写进 live 配置。
- 注入向量：`gost.json` 含 `null` 项（手改、外部工具、损坏落盘）。`parser.Parse`（`x/config/parsing/parser/parser.go:25`）为裸 `json.Unmarshal`，不滤 nil；`mergeConfig` 原样 append；自愈 `dedupeNamed` 明确保留 nil 项（`dedupe.go:44` 附近与 `upsertNamed` 测试断言）。即 nil 项一旦进文件，自愈不清除、落盘原样写回。
- 该向量上 loader 先于任何 update* 触发：`loader.register()`（`x/config/loader/loader.go`）对 17 类逐项取 `.Name`，仅 services 有显式 guard（`:254` `if svcCfg == nil { return fmt.Errorf("service config is nil") }`）。复现（`R19-L2-nil-loader_test.go.txt`，日志 `R19-L2-nil-loader-batch.log`）：
  - `loader.Load` + nil chain → PANIC（nil pointer dereference）；
  - `loader.Load` + nil limiter → PANIC；
  - `loader.Load` + nil service → 干净错误 `service config is nil`（PASS，对照组，证实口径不一）。
  - 其余 15 类（loggers/authers/admissions/bypasses/resolvers/hosts/ingresses/routers/SDs/observers/recorders/climiters/rlimiters/hops）与 chains/limiters 同形（Parse* 均 `cfg == nil` 返回 nil 值、随后 `.Name` 解引用），未逐项跑，读码同型。
- `program.Start`（`program.go:71-87`）与 `reloadConfig`（`:293-318`，经 `loader.Reload`）均无 recover：启动/重载时 loader panic = 进程崩溃，agent 无法起成（且自愈不覆盖 nil，崩溃循环只能手修文件）。

### 2c. 缓解与放大器

- 缓解（进程不死）：面板命令经 `routeCommandSafely`（`websocket_reporter.go:1022-1030`）recover，单命令失败回 `命令处理异常: ...`；管理 API 有 gin `Recovery()`（`api.go:40`）。故 §2a 的 update* panic 经面板下发时 agent 进程本身不崩溃。
- 放大器（比崩溃更坏）：`config.OnUpdate`（`config/config.go:134-142`）用非 defer 解锁——`globalMux.Lock(); err := f(global); globalMux.Unlock()`。闭包内 panic 时写锁永不释放，recover 之后 `Global()`（`RLock`）、`Set()`、`OnUpdate()` 全部永久阻塞。实测：`TestL2DeleteChainNilPanics` 在 panic 被 recover 后，`t.Cleanup` 的 `config.Set` 死锁，`panic: test timed out after 30s`，转储栈顶为 `config.Set` → `sync.(*RWMutex).Lock`（见 `R19-L2-nil-socket.log` 末段；全量同进程跑首测后卡死 6 分钟以上亦为同一机制）。即：一次 nil 触发的 update panic → 该命令失败 + agent 配置子系统永久 wedged（须重启进程，且重启后若文件仍含 nil 则走 §2b 启动崩溃）。

### 2d. 定级 P2（附与 R18-X-03 P3 口径的偏差说明）

立案为一族（同根：配置切片迭代全程 nil-unsafe + `OnUpdate` 非 defer 解锁），含三面：(a) update*/delete*/pause/resume/status 的 nil panic（运行时潜伏）；(b) loader 的 nil panic（落盘 null 可达、启动崩溃）；(c) `OnUpdate` panic 后永久 wedged（放大器）。

定 P2 而非 P3 的理由（与 R18-X-03 的 P3 口径偏差，主控可降级）：触发只需落盘文件一个 JSON null（单字符级损坏，无需多步漂移）；后果是启动崩溃循环（自愈明确不覆盖 nil，只能手修文件）与运行期永久 wedged（比干净报错更坏）；但未定 P1——正常面板/API 流注不进 nil，需文件级外部成因。修复方向建议（供修复路参考，不属本报告处置）：parse/load 边界统一拒 nil 或跳过并告警（以 services 的 `service config is nil` 为口径样板）、update*/delete* 全族补 `!= nil` guard（与 create 口径对齐）、`OnUpdate` 改 defer 解锁。

## 3. F-02F-04 §6 第 4 项：批内同名 `[同名, 同名]` HTTP 端到端形——不立案（纯测试覆盖缺口，产品缺口不存在）

核查结论：该形在 HTTP 层**构造上不可达**，不是产品缺陷；第 4 阶段 upsert 语义的可达面已有 HTTP 级覆盖。

- 代码 pin 死：`createServices`（`go-gost/x/api/config_service.go:96-196`）第 1 阶段逐项 `IsRegistered` 检查（批内两项均未注册，双双放行；名已 TrimSpace，空白变体亦同名）；第 2 阶段逐项 `Register`，第二项命中 `registry` 的 `LoadOrStore`（`x/registry/registry.go:67-69`）返 `ErrDup` → 回滚已注册的首项并 `400 ErrCodeDup` 返回，永不到第 3/4 阶段。`x/socket/service.go` 的 `createServices`（`:23-119`）同构（第 2 阶段同款注册+回滚）。
- 实测（沙箱复本，`R19-L2-batch-dup_test.go.txt`，日志 `R19-L2-nil-loader-batch.log` 末段）：`POST /config/services/batch` 体 `[同名, 同名]` → 400，配置中该名 0 项、注册表空——第 2 阶段拦截 + 回滚均如设计。
- 覆盖现状：helper 单元测试 `TestR18F02UpsertNamed`（`r18_fix_f02f04_test.go:196`）断言第 4 阶段循环语义（连续同名 upsert 只留一份、取末值）；可达的漂移形（批内某项与配置已有成员同名）已有 HTTP 级内存+落盘双断言（`TestR18F02CreateServicesBatchDedupesExistingConfigEntry`，`:135`）。唯一未立的是不可达形的 HTTP 断言——无产品行为可验。
- 不立案理由：无产品行为缺口；15 个单 create 类无 batch 路径，无同型项。登记为测试说明：建议后续在 F-02 回归集中补一形「批内同名 → 400 + 配置/注册表双空」（本轮已在沙箱验证通过，见上），作为第 2 阶段 fail-closed 的回归钉子，不作立案。

## 4. 既有测试 `TestR1B3TunnelDeleteOnlineRealFailureAborts` 未设放行 env——不立案，登记观察（测试基建面）

核查结论：该测试确实在「撒谎」（通过但未覆盖其题述分支）；但其钉死的行为另有 R18-F01 的 9 个测试真实覆盖，故不立案，登记观察并附已验证的一行修复。

- 证据（代码）：测试（`go-backend/internal/http/handler/r1_b3_tunnel_delete_offline_test.go:130-176`）以 httptest 伪装远端节点（`127.0.0.1` 回环），题述「节点在线但数据面清理真实失败（远程节点明确返回失败）」，全文件无 `FLMX_FETCH_ALLOW_PRIVATE_NETWORKS` 设置；联邦传输层默认拦截私网目标（`internal/http/client/federation_transport.go:106` 须 env 恰为 `"true"` 才放行，`:134` 否则报 `resolves to private/reserved address %s, blocked`）。调用链：`tunnelDelete` → `cleanupForwardsDataPlane` → `controlForwardServices` → `sendNodeCommand` → 远端命令（`control_plane.go:808-826`，`IsRemote==1` 走 `sendRemoteNodeCommandWithTimeout`，即联邦传输）。
- 实测（沙箱复本，远端 handler 内置命中计数，日志 `R19-L2-r1b3-probe.log`）：
  - 不设 env（提交态）：`PASS`，但 `remote handler hits=0`——伪装节点一次未被命中，测试靠 SSRF 拦截错误走「在线真实失败中止」分支通过，从未走到 `success=false` 分支；
  - 补 `FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true`：`PASS`，`hits=1`——同一测试真正命中伪装节点并走 `success=false` 分支。
- 不立案理由：(a) 其钉死的行为（在线真实失败中止 + 行保留）已被 R18-F01 的 9 个测试真实覆盖——新测试均设 env，且回执断言含伪装节点才产出的 `simulated real failure: permission denied` 文本（F-01 报告 §3 实测回执），不是宽松断言；(b) F-01 报告 §7 第 4 项作者原判即「不立案，仅记录」；(c) 修复为一行 `t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")` 且本轮已验证补后仍 PASS。加重因素（故列为高优先级观察而非静默记录）：契约测试注释明言该行为「由 handler 级 `TestR1B3TunnelDeleteOnlineRealFailureAborts` 钉死」（`tests/contract/tunnel_delete_resolution_contract_test.go:64` 附近）——指定钉子是空的，虽另有 F-01 测试承重，仍建议顺手补 env 并加命中/文案断言，使指定钉子名实相符。

## 5. 文档抽查——不立案（脚本 PASS，无出入；relay.md 缺口登记观察）

- `scripts/check-doc-consistency.sh` 在基线树重跑：**PASS，exit 0**，7 项全 OK（nodes.md XHTTP 两行、技能包覆盖全部 197 条 `/api/v1` 路由、技能包版本三处一致 1.4.8、install.md 菜单 6 项、Caddy 反代目标 127.0.0.1、默认账号 `admin@user`）。日志：`R19-L2-doc-consistency.log`。
- R18 回执文案 vs `doc/` 指南：`doc/guide/` 全文 grep `已删除.*条转发|隧道未删除|删除失败`——新回执文案（`已删除 N 条转发：名(#ID)`）在指南中零命中；但**无任何指南语句被新文案证伪**：`doc/guide/relay.md:55-61`「删除隧道与节点离线时的语义」述的是离线路径（删库继续、接口成功），F-01 未改该路径行为，只改了失败路径的错误文本；`doc/guide/forward.md:66-67`「删除与端口回收」述的是单转发删除成功路径，亦未被改动。F-02/F-04 的管理 API 在产品部署中三启用门全关、技能包路由覆盖已由脚本核过（197 条）；F-03 的启动自愈为内部韧性行为，指南无对应承诺、无矛盾。
- 不立案理由：无出入（不一致）——只有沉默。登记观察 O-01：建议 `relay.md`「删除隧道与节点离线时的语义」节补 2–3 句，写明在线真实失败时删除中止、且回执披露已删转发（`已删除 N 条转发`，含 nft 分支与删隧道行失败的同款披露随 L2-CAND-01 落地后一并写），否则按现文「接口直接返回成功」运维会误以为删除永不失败。

## 6. 候选立案清单

| 编号 | 级别 | 缺口 | 关键证据 |
|---|---|---|---|
| L2-CAND-01 | 产品 P3 | 删隧道回执披露缺口残留两处：nft 分支删库失败返裸 err（`:789`，附带 `MarkForwardPendingDelete` 失败 `:772`）；`deleteTunnelByID` 失败时「转发已全删」未披露（`tunnel_delete_resolution.go:832`、`mutations.go:2609`） | 与 R18-X-01 同类；gost 分支已修、同分支 reconcile 失败已披露计数，唯此两处裸返；回执直通 `Err(-2)` |
| L2-CAND-02 | 产品 P2 | 配置切片 nil 项全族无 guard + `OnUpdate` 非 defer 解锁：update*/delete*/pause/resume/status 约 49 处 panic（实测 5 处）；loader 17 类中 16 类 nil 即 panic（实测 chain/limiter，services 干净报错作对照）；panic 后 globalMux 写锁永不释放、配置子系统永久 wedged（实测死锁转储） | `R19-L2-nil-socket.log`（5/5 panic + `config.Set` 死锁）、`R19-L2-nil-loader-batch.log`（loader 2/2 panic + services 对照 PASS）；注入向量为落盘 JSON null（自愈保留 nil、parse 不滤） |

不立案项：§3（批内同名不可达，纯覆盖缺口；第 2 阶段 fail-closed 已沙箱验证通过）、§4（R1B3 测试空转，登记观察；一行 env 修复已验证）、§5（脚本 PASS、无出入；relay.md 补述登记观察 O-01）。

## 7. 证据归档（本轮落盘，`audit-out-132/r19/`）

| 文件 | 内容 | 字节 |
|---|---|---|
| `R19-L2-gaps.md` | 本报告 | 见尾行自证 |
| `R19-L2-nil-socket.log` | §2a 复现：5 项 nil panic + deleteChain 后 `config.Set` 死锁转储 | 3040 |
| `R19-L2-nil-loader-batch.log` | §2b loader nil 复现 + §3 批内同名 400 验证 | 1104 |
| `R19-L2-r1b3-probe.log` | §4 R1B3 命中计数：无 env hits=0 PASS / 有 env hits=1 PASS | 726 |
| `R19-L2-doc-consistency.log` | §5 脚本重跑 PASS（exit 0） | 489 |
| `R19-L2-nil-socket_test.go.txt` | §2a 复现测试源码（沙箱复本内文件） | 3950 |
| `R19-L2-nil-loader_test.go.txt` | §2b loader 复现测试源码 | 1788 |
| `R19-L2-batch-dup_test.go.txt` | §3 批内同名探针测试源码 | 1329 |

未验证/未做：真机验证一律未做（按任务约束；L2-CAND-01 的 nft 分支与删隧道行失败、L2-CAND-02 的启动崩溃均未上测试机，留 R19 独占窗口）；§2 的 loader 其余 15 类 nil panic 为读码同型、未逐项跑；§1 的 nft 分支未做 HTTP 级复现（代码 pin 死 + 回执直通已足）。
— R19-L2-gaps END · 自证：本行之前正文字节数 18618 · END
