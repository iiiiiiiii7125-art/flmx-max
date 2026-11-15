# F2 流量审计修复 — 配额暂停与恢复：枚举（基线 ab1d1381 / 2.1.13）

工作树 `wt-traffic-fix-quota`，分支 `traffic-fix/quota`。本文件是三项立案的机械枚举全集；
修复范围＝全集，不许只修登记点。所有行号均为基线 ab1d1381。

## C-03（中）截断整核重启误伤同核会话

### Apply 全部重启触发点（go-gost/x/kernel/manager.go）
- `Apply` :438；`changed := inst.req.Hash != req.Hash || inst.bins[""] != mainBin || len(inst.bins) != len(bins)` :518。
- `case changed || !running` :577 → `stopProcesses` + `startProcesses`（整实例全进程）。
- `!req.Enabled` → 只停不停启；`startBlocked`（nft guard 同步失败）→ 不启；
  `stopGuardedInstances` sweep（syncErr 时）会停掉其它被 guard 的实例。
- 回滚路径：prevGood 时用旧配置重启整实例。崩溃重拉（process.go `relaunchGuard`）
  是按进程的，不属 Apply。
- `stopProcesses` :934：SIGTERM（每 500ms 重发）→ grace 后 SIGKILL，硬上限——杀整实例全部进程。
  `startProcesses` :868：按 `inst.req.processSpecs()`（types.go:343；默认每类型一个进程）启动。
- 面板侧任何入站变更都经同一 Apply 下发（Restore manager.go:219；WS KernelApply
  kernel_commands.go:38）；hash 一变即整实例重启。受害者是直连入站（sing-box/xray）会话；
  gost 转发与 realm 在独立进程，不受 sing-box 重启影响。

### 逐服务重载可行性（已实测否定）
- adapter 结构（types.go:188）只有 `versionArgs` + `fixFile`；go-gost/x/kernel 内无
  reload/SIGHUP/SIGUSR。sing-box 1.14.4 `--help` 无 reload 命令；`sing-box run --help` 无 reload 参数。
- SIGHUP 实测（测试机 scratch，已清）：server-side 1KB/s 滴灌，HUP 前 4096B→8192B（10s，curl 存活）；
  HUP 后下载冻结于精确 10,000B（三采样），curl 退出；同 PID 存活；新入站 24600 已监听且 HTTP 200；
  旧入站 24599 仍接受新连接；sb.log 显示 s1 tcp server 在 reload 时重启；坏配置 + SIGHUP 时进程
  以旧配置存活。**结论：SIGHUP 确会重载配置（新入站生效，同 PID），但杀已建立连接。原生重载不是 C-03 修复。**
- Xray：`xray api` 有 `adu`/`rmu`（增删用户）、`adi`/`rmi`（增删入站）、`lsi`——Xray 具备逐服务
  运行时管理；但伴生入站是 sing-box SS2022，Xray API 解决不了登记场景。Xray 同样无配置重载命令。

### 修复设计：伴生实例分离
- agent：`adapters` 加 `"singbox-companion"` 一项（与 singbox 同 `versionArgs: ["version"]`）。
  Manager 对 Type 完全通用（instances/runDir/binDir/connlog 均按 type 分键）；ApplyRequest 校验
  `adapters[req.Type]` 即放行。伴生实例用同一 sing-box 二进制（面板下发同款 Download，BinName
  `sing-box`），但存于自己 type 的 binDir（多一份 ~20MB 副本，符合按安装隔离原则）。
- 面板 catalog（go-backend/internal/kernel/capabilities.go）：加 `TypeSingboxCompanion =
  "singbox-companion"` + Info 项（Implemented=true，Protocols 含 shadowsocks，Versions/Download
  复用 singbox）。**注意**：`Info.Relay` 只是“UI 不列入添加内核列表”，但
  buildKernelApplyPayloadWith :2592 遇 Relay 即走 `buildRealmApplyPayload`——不能直接复用 Relay
  隐藏伴生类型；须另加 `Managed/Hidden` 标志 + 列表过滤，或在 Relay 分支前特判伴生类型。
  （实现时先核列表面：前端/列表 API 如何过滤 relay。）
- `RenderWith`（render.go:99）只 switch TypeXray/TypeSingbox；伴生类型须映射到 renderSingbox
  （同 config.json + `run -c {RUN_DIR}/config.json`）。
- 伴生 NodeKernel 行：每节点一行（node_kernel 唯一索引 (node_id, kernel_type) 天然保证），
  Version 与该节点 sing-box 内核同值；reconcileSSMetering 首次建伴生时 ensure，不可由用户安装/删除。
  伴生入站的 KernelID 改指伴生行；存量伴生行须一次性迁移 KernelID（触发一次主配置重启 + 伴生实例启动）。
- `ListNodeInboundsByKernel` 按 `kernel_id = ?` 分区——分区后主配置天然不再含伴生，伴生配置只含伴生。
- `originEnabled` 门（kernel.go buildKernelApplyPayloadWith）：现只在同 kernel 的 rows 内查源入站；
  伴生 kernel 的 rows 里没有源入站 → 须改为按节点查源入站启用态，否则伴生恒不渲染。
- reconcileSSMetering 已按 affected KernelID 逐个 `applyKernel`——伴生行进入 affected 集后即单独 Apply；
  主核 Apply 不再含伴生变更。
- 版本门：旧 agent 对未知 type 返回“不支持的内核类型”。面板只向 agent 版本 ≥ 本修复发版号
  （暂定 2.1.14，沿用 `nftIsolatedAgentMinVersion = "2.1.13"` 的常量形态；发版号定下后复核常量）
  的节点下发伴生实例；旧 agent 回退为现状（伴生仍在主配置内，随主核重启——行为不变，非回归）。
- 残留边界（明示，非回归）：Xray 主入站编辑后仍整 xray 重启；本修复只隔离伴生计量面。

### 回归测试
- agent 层：Manager 测试，证对 "singbox-companion" 的 Apply 不重启 "singbox" 实例进程
  （假二进制或真实 sing-box 二进制二选一，以测试内既有 harness 为准）。
- 真机：测试机上慢速滴灌见证走**另一订阅/入站**，触发一订阅截断（伴生用户移除），见证不断流；
  伴生端口新连接应被拒/旧伴生会话按截断语义处理。

### 实施注记（复核结论，2026-11-15；只读核查，非推测）
- agent 实例按 req.Type 分键（manager.go `m.instances[req.Type]`、runDir/binDir 均按 type）——
  伴生 Apply 天然不碰主实例。`adapters` 项是必需的：`ApplyRequest.validate()`（types.go:229）
  拒一切不在 adapters 内的 Type；manager.go:548 版本探测读 `adapters[req.Type].versionArgs`。
  伴生实例用自己 type 的 binDir（`ensureBinary` 会把同款 sing-box 再下载一份；无 binDir 共享特判，
  实例间完全独立）。
- 面板 catalog：**不**进 `Catalog()`（否则安装 UI 会列出伴生内核）。改为 `Lookup` 特判：
  归一化后 `singboxcompanion` 返回 singbox Info 的克隆（Type 覆写为 `singbox-companion`，
  Relay=false——故不走 `buildRealmApplyPayload`；Download 复用 singbox）。`CanonicalKernelType`
  经 Lookup 亦得伴生类型。`Info.Relay` 不复用（见原注记）。
- `EffectiveType(kernelType)`（伴生→singbox）：渲染侧一切 `!= TypeSingbox` / `!= TypeXray`
  比较都须用它，否则伴生类型被误判——实锤地雷：`NormalizeOutboundIPStrategy`（egress.go:30）
  对非 auto 策略 + 非 Xray/singbox 类型直接报错，而伴生行会从源入站镜像 OutboundIPStrategy，
  且 `RenderWith` 逐入站调 `ValidateOutboundIPExtra`；另 model.go:351 ACME、:378 idle、:392
  proxy-protocol、:400 encryption、`kernelVersionGate` AnyTLS 分支、`validateOutboundIPVersion`
  1.12 门。今日伴生皆 SS-only、无上述字段，行为上全是 no-op，但归一化把不变量写死。
  `ComboSupported` 经 Lookup 特判已通，无需另改。`RenderWith` 仍加显式
  `case TypeSingbox, TypeSingboxCompanion:`（唯一生产调用方是 buildKernelApplyPayloadWith:2769，
  会传 effective type；显式 case 供直接调用与测试）。
- `buildKernelApplyPayloadWith`：payload 的 `"type"` 字段保持 `k.KernelType` 原文（agent 实例键）；
  `originEnabled` 改为 `ListNodeInboundsByNode(k.NodeID)` 全节点建表（伴生 kernel 的 rows 里
  无源入站，同 kernel 建表必空——即原注记的 bug）；`RenderWith`/三处复检传 effective type。
- `kernelViews`（kernel.go:340）：过滤伴生行，不进 UI 内核列表（counts 已跳过 CompanionOf>0 入站）。
- 迁移（版本门控、旧 agent 回退）：`ensureCompanionKernel(nodeID)` 挂在 `reconcileNodeKernels`
  循环之前——节点有已启用伴生、agent ≥ 伴生门版本（暂定 2.1.14，发版号定下后复核常量；
  常量形态沿用 `agentSupportsNftIsolation` / `agentSupportsPortProbe`）且无伴生 NodeKernel 行时，
  建行（Version 取主 sing-box 行）并把全部伴生行 KernelID 移过去；随后循环按
  `ListNodeKernels` 的 `node_id ASC, id ASC` 顺序先主后伴生 Apply（端口交接顺序；迁移本身
  造成一次主重启——端口须在进程间搬家，一次性、可接受，此后主变更永不碰伴生）。旧 agent 不建行、
  伴生留在主配置（行为不变，非回归）；升级后下次 reconcile 执行分离。
  `createCompanion`（subscription_metering.go:206）按“分裂是否已激活”（伴生行存在）选 KernelID；
  `reconcileSSMetering` 新建分支须标记**新建伴生行**的 KernelID（重读 `GetCompanionInbound`），
  而非源入站的（同步/删除分支已标伴生自身 KernelID，分区安全）。`syncCompanion` 的 dirty-check
  不含 KernelID——迁移须显式搬，不依赖 sync。
  主 sing-box 内核删除时连带删伴生 NodeKernel 行——已实现：`removeCompanionKernel`
  （kernel.go，remove 流程 `DeleteNodeKernel` 成功后调用；仅主 sing-box 类型触发；
  先向 agent 下发伴生类型 remove、再删伴生行——删行级联其伴生入站；下发失败时按主流程
  同口径登记孤儿重查标记）。
- `checkKernelVersionGates` 内 naive 扫描的 `kernelType == kernel.TypeSingbox` 比较亦传
  effective type（伴生无出站目标，今日 no-op）。
- 分区安全已核：`reconcileSSMetering` 的 meteringActive 经 `kernelByID` + `kernelConfigApplied`
  （后者调 `buildKernelApplyPayload(k)`，随 (3) 修复而通）；`connlogChainTargetInboundIDs`
  全节点查（ListNodeInbounds）；`pollNodeKernels` 已核对齐：agent 的 `Manager.Status`
  （manager.go:1083）按 `m.instances` 键上报类型——伴生实例上报 `singbox-companion`，
  与 DB 行 `KernelType` 同值，故 `known` 集含伴生、hash 追踪与 drift 补推均通；DB 行已删而
  agent 仍在跑伴生实例时，孤儿逻辑会下发 remove 收敛。
- **实施中抓获：`SaveNodeInbound` 白名单漏 `kernel_id`（BUG-A2-1 同族再现）**——该方法用
  显式列白名单（repository_kernel.go:340），`kernel_id` 不在其中；迁移的 KernelID 赋值被
  静默丢弃，两路测试当场抓获（`TestC03CompanionPayloadPartition`：主配置仍含伴生端口；
  `TestC03EnsureCompanionKernelSplitsOnNewAgent`：伴生仍在主内核 1 上）。修法：白名单加
  `"kernel_id": in.KernelID`（同 inx/outbound_ip_strategy/conn_idle_seconds 前例；
  其余调用方先读行再写回，写回即存量值，不变）。
- gofmt 注：`kernel.go:2796` 的 `UserMarkBase` 对齐差一空格是**基线已存**（HEAD 即然），
  非本次改动，不顺手改。

## C-04（中）配额暂停可连续失败，失败期转发继续跑

### 失败全集（实测根因，C lane 会话 2026-11-15 12:49:58/12:50:03 UTC，彩排面板）
- 报错原文：`applyUserQuotaBlock: pause service failed forward_id=168 err=节点 audit-c-scratch
  下发失败: create service 168_46_19_tcp failed: listen tcp 0.0.0.0:24510: bind: address
  already in use action=skip`；`quota pause failed op=dplane forward_id=168`（flow_policy.go:308）。
  转发 168 保持 status=1；169/170 已暂停；`paused_forward_ids` 被修剪为 '169,170'。
- 机制链：`controlForwardServices`（control_plane.go:766）→ `controlForwardServicesOnNode`
  （:872）→ `controlForwardServiceCommand` 遍历 bases × variants（`base_tcp`、`base_udp`，
  另 Pause/ResumeService 有 legacy `base`）。全未命中且 err 为 NotFound →
  `shouldSelfHealForwardServiceControl`（:926；PauseService 与 ResumeService 均 true）→
  `syncForwardServices(forward, "UpdateService", true)` 重建服务 → agent `updateServices`
  （socket/service.go:122）创建 `168_46_19_tcp` → bind EADDRINUSE（端口已被一个不在
  registry 该名字下的监听者占用）。自愈自身的错误直接返回，永不到达重试。
- 服务 base 名 = `{forwardID}_{userID}_{userTunnelID}`（`buildForwardServiceBase` :2088），
  依赖可变的 user_tunnel 行 id；候选集 = 首选 + 本隧道 + 该用户全部 user_tunnels + 0。
  `isNotFoundError`（:2137）大小写不敏感子串匹配 "not found"/"不存在"。
- agent `pauseServices`（socket/service.go:388）是事务批处理：阶段 1 任一服务缺失即整批失败
  （`service %s not found`）；阶段 2 `svc.Close()`（返回值丢弃）+ `kill.ForceClosePortConnections(addr)`
  （返回值以 `_ =` 丢弃）；阶段 3 置 metadata paused=true。
- `updateServices`：服务已注册且配置未变 → 跳过；否则 Unregister（registry.go:74，调 `Close()`，
  故注销即释放监听）→ 解析 → Register + `go svc.Serve()`。bind 只在端口持有者不在 registry
  该名字下时失败（孤儿/旧名监听者，如以已删 userTunnelID 建的服务，或重复 agent 进程）。
  `restoreServiceRuntime`（:230）为回滚路径。
- 已建立连接 fate：`ForceClosePortConnections`（go-gost/x/internal/util/port/port.go:11）跑
  `tcpkill -i any port N`，睡 2s，杀 tcpkill，**恒返回 nil**（panic/启动失败亦然）；killer
  goroutine 永不 `Wait()` → 僵尸进程（亦为审计 low-9）。测试机有 tcpkill（/usr/sbin/tcpkill，
  dsniff 2.4b1，已核），故 2 分钟排干不是“tcpkill 缺失”——tcpkill 只支持 IPv4，端口上 IPv6
  连接不受影响（下载排干 ~2 分钟、上传见证立即死的最强解释）。调用方全以 `_ =` 丢弃：
  socket/service.go:468（pause）、:609（resume 回滚）、api/config_service.go:593、:691、:1013、:1168。
- 配额路径处理：`applyUserQuotaBlock`（user_quota.go:176）记 `action=skip` 后继续；
  `pauseForwardRecords`（flow_policy.go:307-312）记 `quota pause failed op=dplane` 后继续；
  nft 路径按节点对账（30s 超时，缺口记日志）。循环后 paused 列表确被修剪为实际已暂停 ID
  （alreadyPaused ∪ pausedSet 的有序子序列；注释称修剪写失败时保留超集，resume 对运行中转发 no-op）。
  今日唯一重试是定期的 `reconcileUserQuotaEnforcement`（jobs.go:441，handler.go:3220），
  只对仍超配额用户重跑——即观测到的 ~30s 重试。

### 修复清单
1. PauseService 永不走自愈重建：`shouldSelfHealForwardServiceControl` 去掉 "pauseservice"，
   ResumeService 保留。
2. NotFound 暂停后以既有 agent 命令 `CheckPortListening`（port_listening.go:109，扫 /proc/net；
   面板已有包装 `checkLivePortsOccupied`，port_live_probe.go:48）验端口：仍监听 → 精确冲突错，
   不重建、不假成功；已静默 → 成功（本就已暂停/不存在）。
3. `updateServices` 创建失败须区分端口冲突（错误中带端口与冲突语义，供面板侧识别）。
4. `ForceClosePortConnections`：返回真实错误；reap tcpkill（杀 low-9 僵尸）；补 IPv6 覆盖
   （如 `ss -K` 回退）；暂停后验证端口确实已死。
5. 配额路径：失败保持大声且可重试（失败转发不出 pausedSet，DisabledByQuota 保留，令 backstop
   继续重试；复核 reconcile 间隔）。

### 回归测试
- 面板 control-command 单测 + agent pause 单测：NotFound 暂停不重建、端口冲突明错、
  已静默即成功、暂停后端口死且无僵尸 tcpkill、IPv6 连接亦断。

### 实施注记（2026-11-15；提交 601c5733）
- `ForceClosePortConnections` 重写：同步、有真实错误；首选 `ss -K state established
  '( sport = :PORT )'`（IPv4+IPv6，`Run()` 回收）；回退 `tcpkill -i any port N`（仅 IPv4）
  2 秒窗口后 Kill + **Wait() 回收**（杀 low-9 僵尸）。两者皆败 → 报错并点名两者。
  **真机实证**：测试机上 spaced 形式杀已建立连接，客户端得 ECONNRESET（104）；沙箱
  netns 受限，`ss -K` 只能列不能杀（RTNETLINK Invalid argument）——沙箱杀连接不可验，
  以假 ss 测试调度逻辑、真机验真杀。
- `pauseServices` 阶段 2：kill 目标取 live `Addr()`（`pauseKillAddr`：live 端口优先，
  覆盖配置 `:0` 临时端口；无可用端口则跳过）；Close 前取值；仅当 `/proc/net` 扫描到该端口有
  ESTABLISHED 连接时才调 killer（`pauseKillNeeded`；扫描失败按有连接处理、严格尝试），
  无连接时跳过——R19 的 `:0` 服务即此形，killer 缺失也不得拖垮本可成功的暂停。
  kill 失败即返回错误（不回滚：重注册撞 ErrDup；Close/kill 跨重试幂等）。
  全量 socket 包曾打坏两路 R19 测试（`TestR19F02PauseServicesNil` /
  `TestR19F02ResumeServicesNil`，配置 `:0` 触发旧版 port 0 报错）——上述两改修好。
- `updateServices` 创建失败带 `port_conflict:` 标记（保留底层 bind 原文）。
- `CheckPortListening` 回包加 `Established []int`（恒 present；`parseProcNetEstablished` /
  `scanProcNetEstablished` 只认 TCP st=01）。
- 面板：`shouldSelfHealForwardServiceControl` 只留 ResumeService；NotFound 暂停分支——
  agent ≥2.1.13（`agentSupportsPortProbe`，CheckPortListening 随 2.1.13 出，cac080bd）
  时 `verifyForwardPortsDeadOnNode` 验端口（静默即成功不重建；仍监听/有已建立连接即明错；
  探针本身失败则 fail-closed）；旧 agent 走 legacy 自愈重建（过渡态）。暂停成功后
  `verifyPausedPortsDead`（~3s 轮询）验端口已死，失败即明错，探针失败则 fail-open（仅记日志）。
  三档降级：2.1.14+ agent 监听+已建立双验；2.1.13 仅监听（无 established 字段时跳过）；
  2.1.13 前 legacy 自愈。`portProbeAgentMinVersion = "2.1.13"` 与
  `nftIsolatedAgentMinVersion` 同值是巧合，各自由引入提交背书，不合并。
- 配额路径本身（第 5 项）经读码确认无需改动：`applyUserQuotaBlock` 已增量执法（失败转发不进
  pausedSet、DisabledByQuota 保留，下次对账重触发）。

## C-05＝D-01（中）重置用户配额转发不恢复还报“已恢复”

### 释放路径全部静默否决点（go-backend/internal/http/handler/user_quota.go）
- `ResetUserQuotaUsage`（repository_user_quota.go:486）：同一事务内清用量 **并** 清
  `PausedForwardIDs` + `DisabledByQuota`（不再超限时）——恢复依据在任何 resume 尝试前已被销毁。
- `applyUserQuotaRelease`（:233）无返回值；跳过点：
  - V1 `getForwardRecord` err/nil → `continue`，**无日志**；
  - V2 pending_delete → 有日志；
  - V3 tunnel_not_enabled → 有日志；
  - V4 `ensureUserTunnelForwardAllowed` err → `continue`，**无日志**（:258，C-05 登记点——
    隧道流量门否决，agent 侧 ResumeService 命令数为 0）；
  - V5 mixed_nft_state → 有日志；
  - V6 `ResumeService` err → `continue`，**无日志**；
  - V7 ActivateForwardIfNotPending err → 有日志；
  - V8 nft reconcile 超时 → 有日志（"quota release gap"）；
  - V9 db_write_after_reconcile err → 有日志。
- 重置 handler（:651-660）：调 `ResetUserQuotaUsage` → 调 `applyUserQuotaRelease`（结果丢弃）→
  `if release.UnblockUser { notifyUserQuotaRecovered(...) }` + `response.OKEmpty()`——无条件成功回执
  与“已恢复”通知。`resetUserQuotaWindows`（:338，维护，jobs.go:440）→ `RollUserQuotaWindows` →
  同样 release + 按 UnblockUser 通知。`notifyUserQuotaRecovered`（:358）本身可靠（pending 标记 +
  checkUserReminders 重试）——错在按 UnblockUser 就发，不在通知机制。
- 失败释放无重试：`reconcileUserQuotaEnforcement`（:297）只对**仍超配额**用户重跑执法；已重置用户
  不再超限，失败释放永无自动恢复机会（“面板无任何自动恢复机会”）。
- `ResetUserQuotaUsage` 唯一调用方是重置 handler（:651）；`applyUserQuotaRelease` 调用方：:338 与 :657。

### 修复清单
- F1 拆分重置：清用量 + 清 DisabledByQuota（新转发放行），但**保留 PausedForwardIDs 作待恢复清单**；
  新增 repo 方法，只删除已成功恢复的 ID（重复释放幂等：运行中服务的 ResumeService 与
  ActivateForwardIfNotPending 均为 no-op）。
- F2 `applyUserQuotaRelease` 返回逐转发结果；V1/V4/V6 补日志。
- F3 重置 handler 与窗口滚动如实回执失败、仅在全部恢复时发“已恢复”（否则点名仍暂停的转发）。
- F4 待恢复 backstop：对未超限但 PausedForwardIDs 非空的用户重试释放。

### 回归测试
- repo + handler 层：重置后 PausedForwardIDs 保留、逐项恢复后逐项清除、V4 否决记日志且回执
  含失败、通知仅在全恢复时发、backstop 覆盖未超限待恢复用户。

## 跟进修（2026-11-15，完工前追加）

### F4 移交：离线节点释放谎报 Active（C-05 释放路径机制，45ed3c3b 未覆盖）
- 核查结论：**未覆盖**。C-05 的 V6 只看到 `controlForwardServices` 的返回值，而
  非删除分支（control_plane.go:839 循环）对离线/超时错全部命令一律 `continue` 吞掉、
  返回 nil——V6 根本看不到离线错，随即 `ActivateForwardIfNotPending` 把行标 Active，
  Resumed 清单又把它从 PausedForwardIDs 摘除，无重试载体。F4 真机实证：节点 14 全程
  离线，reset 后转发 29 由 0→1，agent 从未收到 resume。离线错文本：ws/server.go:993
  `errors.New("节点不在线")`，`shouldDeferTunnelRuntimeApplyError`（mutations.go:8589）
  命中该串 → `isNodeOfflineOrTimeoutError` 为真，吞没成立。
- 同机制全集（ResumeService 调用方，均在控制函数返回值上推进 DB 状态）：user_quota.go:306
  释放、mutations.go:6477 手动恢复、mutations.go:6837 批量恢复（serviceCmd）。PauseService
  四处调用方（flow_policy.go:307、user_quota.go:176、mutations.go:6356、
  control_plane.go:600 syncForwardServicesWithWarnings 尾部保暂停）**不是**同向受害者：
  暂停＋离线 → nil → 标 status=0 即期望态（数据面已停、暂停行不重推）。
- nft 分支已 fail-closed：`reconcileNodeWithTimeout` 离线返回 false → Failed
  （reconcile_timeout），无此缺口。
- 根治（745eee19）：吞没只许 PauseService（`isPauseServiceCommand` 划界，本循环除删除
  分支外只到 Pause/Resume，故即全集）；ResumeService 离线/超时明错返回；V6 离线单列
  reason `node_offline`。重试收敛性：agent `resumeServices` 未暂停即跳过（幂等），
  backstop 每分钟重跑同一释放函数；重连重推（redeployLocalNodeRuntime，ListForwardIDsByNode
  无状态过滤）是第二重收敛、不是替代——行标 Active 期间 DB 仍说谎、回执仍错、清单已摘，
  故根治不可省。同族先例：删除分支 S-04 收集 offlineNodes 返 `nodeOfflineError`；
  syncForwardServicesWithWarnings 离线写警告标记、重连重推视跳过为失败重试（Lane A）。
- 回归测试（quota_release_offline_f4_test.go）：离线节点 ResumeService 明错“不在线”且
  agent 零命令、PauseService 仍吞；释放路径 Failed[node_offline]、行保持 status=0、
  PausedForwardIDs 保留、ListPendingQuotaReleases 仍列该用户、agent 零 ResumeService。

### C-03 跟进：订阅凭据推送够不到伴生内核（C-03 分离引入的回归）
- 真机实证：q-sub1 禁用后伴生配置仍列两用户（u1/u2）、文件 mtime 不变、agent 日志无
  `singbox-companion` KernelApply；主内核已重推（同 hash）。根因：`subInboundIDs`
  只滤原入站（CompanionOf==0），`syncSubscriptions` 经 `applyKernelsForInbounds`
  只推原入站内核；分离前伴生同住主内核、免费重渲染，分离后够不到。
- 同机制全集：`applyKernelsForInbounds` 调用方共五处——syncSubscriptions:579（禁用/启用/
  超限状态同步）、subscriptionSave:1351、respondAfterSubPush:1366、
  completePendingSubscriptionDelete:1427、rotateSubscriptionCredentials:1498——五路
  皆只传原入站 ID，皆须伴生内核。
- 根治：在 `applyKernelsForInbounds` 内把每个原入站扩展到其伴生内核（一次
  `ListCompanionInbounds` 建表；伴生行同样过 external/seen 去重；分离前伴生与原入站
  同 KernelID、去重后仍单次下发）。伴生列表读失败 fail-closed（markRepoError），与
  R3-MR-01“下发集不可知不得空转成功”同口径。`reconcileSSMetering` 不覆盖本缺口：
  其 dirty-check 只比行内存字段、不含渲染期 users，订阅禁用时 clean 不推（测试钉死）。
- 回归测试（subscription_companion_push_c03_test.go）：推原入站下发主＋伴生两 KernelApply；
  分离前去重为一次；禁用双订阅之一后 syncSubscriptions 推伴生内核、新渲染含幸存订阅
  的派生 uPSK（SSUserPSK）、不含已禁用订阅的 uPSK、AppliedActive 归 0。注意：渲染的
  SS2022 用户口令是派生 uPSK，不是订阅口令原文，断言须算 SSUserPSK。
