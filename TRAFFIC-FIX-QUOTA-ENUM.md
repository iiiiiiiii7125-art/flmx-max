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
