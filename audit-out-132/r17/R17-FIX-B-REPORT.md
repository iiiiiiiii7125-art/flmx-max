# R17 修复 B 路报告：面板侧补发幂等守卫 + 下发记录

- 任务：R17 修复 B 路——面板侧补发幂等守卫 + 下发记录（只改 go-backend，不合 main、不打 tag、不发版）
- 基线：main @ 5473377（`fix(r16): userList pagination overflow, sheetProbe null guard, drop dead inboundProbeView; archive r15/r16 audit records`）
- 分支：`fix-r17-b`（从 main 切出，独立 worktree `~/workspace/.fixr17-b`）
- 代码提交：`6d4a51aa0ca3c1891d4711b3818aba6962cb69dd`（父提交 5473377，4 个文件，+637/-4）
- 报告提交：见本文件落盘后的第二次提交（仅报告；日志因 *.log 被 gitignore 忽略而未入提交，存于 worktree 磁盘路径）
- 证据原件：`~/workspace/flvx-max/audit-out-132/r17/R17-L4-evidence/gost.json-candidate-written.json`，md5 已复核 `e381299b2aa931d2f7c097661ead6702`；14 个服务，`6_1_0_tcp` / `6_1_0_udp` 各重复一次，位于索引 10-13（尾部）
- 约束遵守：未用 `git add -A`（两次提交均为显式文件列表）；未改版本字面量；未合 main；handler 只调 repo 方法（本次新增代码不触 DB，未调 `repo.DB()`）；Go 工具链 `~/go/bin/go`（全程 `export PATH=$HOME/go/bin:$PATH`）

## 1. 定位：补发代码路径（实查，假说已证伪）

### 1.1 任务书假说不成立：面板侧不存在“按 agent 上报服务表做差集”的代码

- agent 侧无服务表查询命令：`go-gost/x/socket/websocket_reporter.go` 命令分发表（约 1051-1235 行）含 `AddService/UpdateService/DeleteService/PauseService/ResumeService/AddChains/.../TuneStatus/TuneApply/TuneProbe/UdpPing/Traceroute/ServiceMonitorCheck/SetProtocol/UpgradeAgent/RollbackAgent/KernelApply/KernelControl/KernelStatus`，无 `ListServices/GetServices/QueryServices` 类命令；全仓 grep `GetServices|ListServices|QueryServices|FetchServices|ServiceList`（go-backend + go-gost）在产品代码中零命中。
- 面板侧从不读取 agent 当前服务表：`sendNodeCommand`（`go-backend/internal/http/handler/control_plane.go:792` 起）只发命令等同步响应；`internal/ws/server.go:787` `SendCommand` 无 per-node 串行化（每调用只注册 pending 请求后写 WS，无“等上一条确认”的等待）。
- 结论：所谓“面板按 agent 上报的服务表做差集、转发 6 未完成注册被当作缺失而补发”这一机理的前提在基线代码中不存在。重复 AddService 的面板侧可证实窗口见 1.2-1.3；agent 侧 `createServices` 先查后写的竞态与落盘去重缺失属 A 路（`go-gost/x/socket/service.go:23` `createServices` 第 1 阶段 `IsRegistered` 检查、第 4 阶段 `config.OnUpdate` 无去重 append；对照 `updateServices`（`service.go:100`）第 3 阶段按名去重替换）。

### 1.2 实际存在的三处 AddService 补发（control_plane.go）

| # | 位置（修复后行号） | 触发条件 | 原行为 |
|---|---|---|---|
| 1 | `syncForwardServicesWithWarnings`，`control_plane.go:465` | `UpdateService` 报错且 `allowFallbackAdd`（仅 not-found 文本时先删旧服务，`control_plane.go:455-460`） | 直发 `AddService`，无去重、无确认等待、无记录 |
| 2 | `fallbackForwardPortToDefaultBind`，`control_plane.go:518` | `UpdateService` 报 cannot-assign-address 后回退默认监听（删 + 150ms 盲 sleep 后发） | 同上 |
| 3 | `rebindForwardServiceOnSelfOccupiedPort`，`control_plane.go:558` | `UpdateService` 报 address-already-in-use 后重绑（删 + 150ms 盲 sleep 后发） | 同上 |

补充：`updateServices` 是 upsert（`service.go:100` 起注释“存在则更新，不存在则创建”），故缺失服务本不会使 `UpdateService` 报 not-found；1 号回退实际由错误文本含 `not found`/`不存在` 触发（`isNotFoundError`，`control_plane.go:1913` 起），属错误文本匹配而非服务表差集。

### 1.3 首次启动/重连同步入口与重复触达面

- 入口：`handler.go:207` `SetNodeOnlineHook(h.onNodeOnline)` → `onNodeOnline`（`upgrade.go:648`）→ `startNodeOnlineRedeploy`（`upgrade.go:742`，仅护在线重部署记账 map：in-flight + 冷却排队）→ `reconcileSharedNodeRuntime` + `redeployLocalNodeRuntime`（`upgrade.go:853`）。
- `redeployLocalNodeRuntime` 每轮对同一转发推两次：先逐隧道 `redeployTunnelAndForwards`（`mutations.go:2792`，内调 `syncForwardServices(UpdateService, true)`），再逐转发直接调 `syncForwardServices(UpdateService, true)`（`upgrade.go:885`）；失败再进 `retryFailedRedeploys`（`upgrade.go:913`，最多 3 次，1s/2s/4s 盲 sleep）。
- 跨入口无串行化：`redeployLocalNodeRuntime` 还可由升级重部署（`upgrade.go:679,735,836`）、备份导入重布（`backup_import_repush.go:91`）、手动批量重部署（`mutations.go:2838,4213` 等 `syncForwardServices` 直调点）并发触发；`upgradeMu` 只护记账 map，不串行化重部署本身。因此同一 `(node, 服务)` 可并发走到 1 号回退 AddService，而两次补发之间不存在确认等待，只有盲 sleep；`sendNodeCommand` 的 `tolerateExists=true`（`control_plane.go:792-820`）会把 agent 的 already-exists 吞成 nil，重复 AddService 在面板侧不可见，且此前无任何逐服务下发记录。
- 隧道服务不在本次范围：`addTunnelServiceOnNodeWithMode`（`mutations.go:6228`，`<tunnel>_tls` 服务，自带删后重试）未改，见第 5 节。

## 2. 改动（逐文件、逐行号）

### 2.1 新增 `go-backend/internal/http/handler/add_service_guard.go`（279 行）

- `r17AddServiceLogf`（:71）：守卫日志出口，默认 `fmt.Printf`，与 `upgrade.go` 重部署路径的 `fmt.Printf` 口径一致；仅测试可替换为记录器。
- `addServiceGuardJoinTimeout`（:78）：30s，等待在途 AddService 结果的上界（生产发送只有 6s 默认超时，30s 意味着在途条目已僵死）。
- `extractAddServiceNames`（:93）：从载荷取去重、去空、排序后的服务名；取不到名字返回空，调用方 fail-closed（直接发）。
- `noteAddServiceConfirmed`（:136）：agent 成功响应确认的服务名记入确认集。
- `invalidateAddServiceNames`（:155）：作废指定服务确认（新鲜否定证据）。
- `invalidateAddServiceNode`（:173）：作废整节点确认（节点离线）。
- `logR17AddServiceGuard`（:193）：每条判定一行，格式 `r17-addservice-guard: node=<id> decision=<send|sent-ok|sent-failed|skip-confirmed|skip-joined> reason=<...> services=[<名>,...] err=<...>`，按服务名 grep 可得一次补发的完整判定链。
- `guardedAddService`（:207）：三处转发 AddService 回退的唯一出口。语义：同 `(nodeID, 服务名集合)` 在途时后到者等待（join），在途成功则跳过、在途失败则自行补发；在途等待超时则清掉疑似僵死条目再补发；发送成功记确认，失败不记确认。

### 2.2 `go-backend/internal/http/handler/control_plane.go`（+26/-4）

- :448-452：`UpdateService`/`AddService` 成功后记确认（`noteAddServiceConfirmed`；UpdateService 为 upsert 语义，成功即注册确认）。
- :461-465：1 号回退改走 `guardedAddService(node.ID, services, "update-fallback", true)`。`freshAbsence` 一律为真（见 2.4 的回归教训）。
- :515-518：2 号回退改走 `guardedAddService(node.ID, defaultServices, "default-bind-fallback", true)`（刚删过旧服务，新鲜缺失证据）。
- :555-558：3 号回退改走 `guardedAddService(node.ID, services, "self-occupied-rebind", true)`（同上）。
- :605-612：`deleteForwardServiceBasesOnNode` 在 `DeleteService` 成功后作废这些服务名的确认（已不存在也被容忍为成功，作废仍正确）。转发删除全路径（`controlForwardServices` 的 DeleteService 分支、`deleteForwardServicesOnNode`）均经此函数；`controlForwardServicesOnNode` 只发 Pause/Resume，不发 DeleteService，已核查。

### 2.3 `go-backend/internal/http/handler/handler.go`（+17/-1）

- :109-116：Handler 增加守卫状态 `addSvcMu` / `addSvcInFlight` / `addSvcConfirmed`（确认集只由 agent 成功响应建立、无 TTL；零值可用、惰性初始化）与发送缝 `addSvcSend`（默认 nil 走 `sendNodeCommand`，仅测试注入假发送器，生产代码不得设置）。
- :220-224：离线 hook 包一层——先 `invalidateAddServiceNode` 作废该节点确认集，再走原有 `notifyNodeOffline`。`SetNodeOfflineHook` 全仓仅此一处调用。

### 2.4 判定口径原文（add_service_guard.go 文件头注释，与代码同文，节选关键段）

> 以什么为准：“agent 已确认注册态” = 本面板进程内由 agent 对 AddService/UpdateService 命令的成功响应建立的确认。agent 仅在注册 + 落盘成功后才回成功（go-gost/x/socket/service.go createServices/updateServices；already-exists 经 tolerateExists 归一化为成功，错误文本本身就是注册态的确认）。确认集无 TTL、无时间窗条目——绝不用“刚发过就跳过”的纯时间窗代替注册态确认。
>
> 等什么确认：同一 (nodeID, 服务名集合) 已有在途 AddService 时，后到的补发不另发，等待在途那次的结果（join）：在途成功 → 跳过（已确认）；在途失败/超时 → 自行补发（fail-closed）。等待有界（addServiceGuardJoinTimeout），超时后先清掉疑似僵死的在途条目再自行补发，并记日志。
>
> 否定证据优先：本次同步内刚收到的 UpdateService 失败（无论错误文本是 not-found、超时还是 bind 失败）、显式 DeleteService 成功、节点离线，立即作废对应确认条目——新鲜的否定证据永远压过旧的肯定确认。三处回退路径调用本守卫时 freshAbsence 一律为真：刚失败的 UpdateService 本身就是否定证据（失败 = 期望注册态未达成）；仅 not-found 才作废是错的——R3-GATE-F3/R4B6 回归证明 setup 期成功的确认会让 bind 失败后的补发被跳过、把失败误判为成功。因此 stale 确认绝不可能拦住一次真正需要的补发。
>
> 超时/无确认时如何 fail-closed：发送超时/失败/无响应 → 不记确认、不跳过；等待者收到失败结果后自行补发；面板/进程重启 → 确认集为空 → 一律补发（agent 的 already-exists 拒绝使重复发送安全，落盘去重属 agent 侧 A 路）。
>
> 查-发-核三段式说明：因 agent 无服务表查询命令，本路无法实现“补发前向 agent 查实时表”；三段式在本路退化为——查：本次同步的 UpdateService 结果 + 内存确认集；发：守卫放行后才发；核：AddService/UpdateService 成功响应即核（记确认）+ 下一次同步的 UpdateService 成功再次核。须 agent 侧新增 ListServices 命令才能做真查询，此为未实现项。

诚实补充：三处生产回退路径均带新鲜失败证据（刚失败的 UpdateService 或刚删过服务），故 `skip-confirmed` 在当前生产路径上不会触发；守卫在生产上的实际作用是 (a) 并发重复 AddService 合并为一次在途发送（join），(b) 全量判定日志，(c) fail-closed 的作废纪律。`skip-confirmed` 只对无新鲜失败证据的直接重推生效（守卫 API 级别，测试覆盖），属防御纵深。

## 3. 测试证据（先红后绿，日志落盘）

日志目录：`~/workspace/.fixr17-b/audit-out-132/r17/R17-FIX-B-logs/`（随报告提交）。

### 3.1 基线（修复前，clean tree）

- `baseline-build.log`：`go build ./...` exit=0。
- `baseline-vet-handler.log`：`go vet ./internal/http/handler/` exit=0。
- `baseline-test-handler.log`：`go test ./internal/http/handler/` FAIL，唯一失败 `TestSSMeteringReconcileLifecycle`（`subscription_metering_test.go:127: database or disk is full (13)`）——/tmp tmpfs 满导致的环境红，与产品无关（见 3.4）。

### 3.2 红：守卫测试先红

- `red-guard-test.log`：新增 `add_service_guard_test.go` 后、`add_service_guard.go` 落地前，`go test ./internal/http/handler/ -run TestR17B` 编译失败 exit=1（`h.addSvcSend / r17AddServiceLogf / noteAddServiceConfirmed / guardedAddService undefined`）。

### 3.3 绿（第一轮）：守卫落地，9/9 通过，但全量回归暴露设计错误

- `green-build.log`：`go build ./...` exit=0；`green-vet-handler.log`：vet exit=0。
- `green-guard-test.log`：`TestR17B_*` 9/9 PASS（0.232s）。
- `green-guard-test-race.log`（第一次）：`-race` 链接失败 `no space left on device`——/tmp 满导致的环境失败，非产品失败；改用 `TMPDIR=/home/hatch/tmp-go` 后重跑 exit=0（`ok go-backend/internal/http/handler 1.271s`），见 3.4。
- `green-test-handler.log`：全量 handler FAIL，2 个新失败（基线没有）：
  - `TestR3GateF3FailedTunnelSwitchRepushesWhitelistAfterRollback`（`r3_gate_f3_failed_switch_whitelist_test.go:223: forwardUpdate must be rejected when service sync fails, got success`）
  - `TestR4B6TunnelReplaceMigrationFailureRepushesWhitelist`（`r4_b6_rollback_wrapup_test.go:202: replace must fail when migration sync fails, got success`）
- 根因（已定位、已修复）：第一版守卫只在 not-found 文本时作废确认；两测试的 mock agent 在 setup 期 UpdateService 成功（留下确认），切换期以 bind 错误（R3）/ permission denied（R4B6）失败——非 not-found 文本不作废，补发被 `skip-confirmed` 跳过，失败被误判为成功。修复：1 号回退 `freshAbsence` 一律为真（`control_plane.go:461-465` 注释已写明），并新增锁死测试 `TestR17B_GuardFailedUpdateInvalidatesConfirmedEvenWithoutNotFound`。该回归的红方证据即本轮全量日志，绿方证据见 3.5。

### 3.4 环境说明：/tmp tmpfs 满

- 现象：`/tmp` 为 512M tmpfs，使用率 87%（剩 68M），内为其他轮次/并行 lane 的产物（r15/r17c 等），未擅自清理；导致基线 `TestSSMeteringReconcileLifecycle` 磁盘满失败与首轮 `-race` 链接失败。
- 处理：本次全部 `go test` 改用 `TMPDIR=/home/hatch/tmp-go`（家目录 100G 盘，66G 空闲）；该目录在仓库外，不进入提交。基线红与首轮 race 链接失败均为环境红，非产品红。

### 3.5 绿（第二轮，修复后最终态）

- `green-build2.log`：`go build ./...` exit=0；`green-vet-handler2.log` / `green-vet-handler3.log`（注释行号修正后重跑）：vet exit=0。
- `green-guard-plus-r3r4b6.log`：定向 `TestR17B|TestR3GateF3...|TestR4B6...` exit=0，12/12 PASS：10 个 `TestR17B_*`（含新增回归锁死测试）+ 2 个 R3/R4B6 回归测试。
- `green-test-handler2.log`：`go test ./internal/http/handler/ -count=1` exit=0，`ok go-backend/internal/http/handler 320.134s`，产品失败 0（基线的磁盘满红亦消失，因 TMPDIR 改道）。
- `green-test-all.log`：`go test ./... -count=1`（go-backend），除已知沙箱红外全绿：`handler 354.766s ok`、`repo 95.433s ok`、`tests/contract 43.444s ok` 等；唯一失败为任务书点名的已知沙箱 fake-IP DNS 环境红，签名一致：
  - `--- FAIL: TestValidateMonitorTCPTarget/公网域名放行 (0.01s)`，`monitor_ssrf_test.go:39: ValidateMonitorTCPTarget("example.com:443") err=目标地址不允许（内网/保留地址）: example.com, wantErr=false`，包 `go-backend/internal/health`（本次未改动该包，`git status` 仅 handler 文件）。
- 守卫测试清单（`add_service_guard_test.go`，319 行，10 项）：
  1. `TestR17B_GuardSkipsWhenAgentConfirmed`——已确认态下不再补发（发送 0 次，日志含 skip-confirmed 与服务名）
  2. `TestR17B_GuardSendsOnceWhenUnregistered`——未注册态下补发一次
  3. `TestR17B_GuardRecheckSkipsAfterSend`——补发后复核发现已存在时不再重复补发
  4. `TestR17B_GuardFreshNotFoundInvalidatesConfirmed`——新鲜 not-found 作废确认后补发
  5. `TestR17B_GuardConcurrentDuplicateJoinsInFlight`——并发重复补发只发一次（第二路 join，发送总数 1）
  6. `TestR17B_GuardFailedSendDoesNotConfirm`——发送失败不记确认，下次仍补发
  7. `TestR17B_GuardInvalidateOnDeleteAndOffline`——Delete/离线作废确认
  8. `TestR17B_GuardEmptyNamesFailClosed`——取不到服务名时 fail-closed 直接发
  9. `TestR17B_GuardConfirmedIsPerNode`——确认集按节点隔离
  10. `TestR17B_GuardFailedUpdateInvalidatesConfirmedEvenWithoutNotFound`——R3/R4B6 回归锁死（bind 失败等非 not-found 失败同样作废确认）

## 4. 下发记录留存（验证）

- 格式（`add_service_guard.go:193`）：`r17-addservice-guard: node=<id> decision=<send|sent-ok|sent-failed|skip-confirmed|skip-joined> reason=<...> services=[<名>,...] err=<...>`，沿用 `upgrade.go` 重部署路径的 `fmt.Printf` 口径。
- 实测样本（第二轮全量日志 `green-test-handler2.log` / `green-test-all.log` 内可 grep）：
  - `r17-addservice-guard: node=2 decision=send reason=update-fallback services=[2_1_0_tcp,2_1_0_udp] err=-`
  - `r17-addservice-guard: node=2 decision=sent-failed reason=update-fallback services=[2_1_0_tcp,2_1_0_udp] err=r4b6 simulated dispatch failure: permission denied`
  - `r17-addservice-guard: node=2 decision=send reason=update-fallback-join-failed services=[2_1_0_tcp,2_1_0_udp] err=r4b6 simulated dispatch failure: permission denied`
- 按服务名（如 `2_1_0_tcp`）grep 可得一次补发的完整判定链（send → sent-ok/sent-failed，或 skip-confirmed/skip-joined）。

## 5. 未验证项

1. 真机验证未做：本路未在 Lightsail 测试机上做真机验证（无 agent 联调、无生产节点重连重放）。面板侧守卫的真机行为（重连风暴下重复 AddService 是否收敛为一次、日志是否落盘可查）待测试机重验；毒化文件本身的复现与根治属 A 路（agent 落盘去重），不在本路范围。
2. agent 侧无 ListServices 命令：面板无法向 agent 查询实时服务表，“查-发-核”中的“查”只能依赖本次同步结果 + 内存确认集（见 2.4）。真查询须 agent 新增命令 + 面板调用，属跨模块变更，本路未做。
3. 隧道服务未覆盖：`mutations.go:6228` `addTunnelServiceOnNodeWithMode`（`<tunnel>_tls` 服务）仍直发 AddService，未接守卫；其自带删后重试语义与转发回退不同，需单独评估后接入。
4. `control_plane.go` 基线已有 gofmt 漂移（`git show main:... | gofmt -l` 亦列出，如 :166 注释空格），本次未顺手格式化，避免无关 diff；新增的两个文件已 `gofmt -w` 且 `gofmt -l` 干净。
5. 守卫确认集为面板进程内存：面板重启即清空（fail-closed：一律补发，agent already-exists 拒绝兜底）；节点离线已接作废，但若离线 hook 未触发（如进程被杀），残留确认只影响无新鲜失败证据的直接重推，三处生产回退路径均不受影响（freshAbsence 一律为真）。
6. 毒化机理（“转发 6 未完成注册被当作缺失而补发”）仍未证实：本路证伪了其前提（面板无服务表差集），面板侧可证实的重复窗口是多路并发重部署 + 回退 AddService 无去重；agent 侧检查-注册-落盘的竞态窗口是否真实触发过，需 A 路结合 agent 日志取证。

## 6. 交付清单

- 分支 `fix-r17-b`，代码提交 `6d4a51a`（4 文件，见第 2 节行号）；本报告单独提交（同分支，不合 main、不打 tag、不发版）；日志因 `*.log` 被 gitignore 忽略而未入提交，存于 worktree 磁盘路径，路径见第 3 节。
- 日志：`~/workspace/.fixr17-b/audit-out-132/r17/R17-FIX-B-logs/`（baseline-*.log、red-guard-test.log、green-*.log 共 15 个文件）。
- 未改：版本字面量、main 分支、agent（go-gost）、前端、隧道服务路径。

---
自证：本文件 END 行之前字节数 19904（校验：head -n -2 R17-FIX-B-REPORT.md | wc -c 应等于该数）
END
