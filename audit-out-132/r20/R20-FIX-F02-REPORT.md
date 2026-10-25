# R20-FIX-F02 修复报告

基线：main @ b8fb567（`b8fb5678e0859da03e8fc8ecfcf50ce675480296`）。分支：`fix-r20-f02`（独立 worktree `~/workspace/.fixr20-f02`）。对象：R20-X-02（P2，入站编辑丢 connIdleSeconds）。只修、不合 main、不打 tag、不发版。

## 一、结论

- F-02 已修复并回归通过（先红后绿，红绿日志均在盘，见 §五）。
- 全仓同型复核完成：68 处 `Updates` 调用点逐一对账（59 处内联 `map[string]interface{}` + 9 处变量 map），另附带核查 11 处同型 `UpdateColumns(map...)` 与全结构 `Save` 路径；除本项外无其他漏列；另发现同列第二道闸（伴生同步 dirty-check）一并补齐，见 §三。
- `go build` / `go vet` / 前端 `tsc --noEmit` 全绿；`go test ./...`（go-backend）除已知沙箱 DNS 一格外全部通过，go-gost/x 27 包 ok、go-gost 1 包 ok，见 §六。
- 无 BC：恢复编辑应有效果，属缺陷修复；无既有行为依赖被改变（显式 0 清空语义与 inboundSave 指针约定一致）。

## 二、根因（已 pin 死，与 V-03 一致）

`inboundSave`（`handler/kernel.go:935-939`）从请求取 `connIdleSeconds` 赋给 `in`，`applyInboundToModel`（`:216`）回拷给 `row.ConnIdleSeconds`，但 `SaveNodeInbound`（`store/repo/repository_kernel.go:311-350`）的 `Updates` 白名单漏 `"conn_idle_seconds"`，编辑报 code 0 却不落库；创建路径走全结构插入故生效。BUG-A2-1 同类复发（该函数内两处注释正为同类漏列立的规矩）。

## 三、改动（2 文件 +10 行，1 个新测试文件）

### 3.1 `go-backend/internal/store/repo/repository_kernel.go` —— 白名单补列

`SaveNodeInbound` 的 `Updates` map 增加：

```go
"conn_idle_seconds": in.ConnIdleSeconds,
```

附 BUG-A2-1 同口径注释（R20-X-02）。

### 3.2 `go-backend/internal/http/handler/subscription_metering.go` —— 伴生同步第二道闸

`syncCompanion` 的 dirty-check 增加 `updated.ConnIdleSeconds == comp.ConnIdleSeconds`。机理：伴生行的超时经 `applyInboundToModel` 从 origin 镜像而来，但 dirty-check 不点名该列时，origin 仅改超时会使 `syncCompanion` 直接 return false（不保存），白名单修复对此调用方无效，伴生行永久持旧值。同列同类漏列，随本项一并补齐；行为面仅影响伴生行与 origin 超时的一致性，不改其他同步语义。

### 3.3 新回归测试 `go-backend/internal/http/handler/connidle_save_test.go`

- `TestConnIdleEditPersistsAndRenders`：建入站 connIdle=5 → 按 inboundSave 同款路径（`inboundFromModel` → 改 10 → `applyInboundToModel` → `SaveNodeInbound`）编辑 → list 显 10 → Xray 渲染 `policy.levels["1"].connIdle == 10`；另覆显式 0 清空回 0。
- `TestConnIdleCompanionSyncMirrorsOrigin`：origin SS 入站 connIdle=10、伴生行先一致（基线 no-op 校验夹具无杂散差异）→ 仅伴生行漂为 5 → `syncCompanion` 须返回 true 且落库回 10。
- 未改任何既有断言；无夹具 setup 值改动。

## 四、全仓同型复核（全集枚举，66 处）

方法：枚举全部 `.Updates(` 调用点（内联 map 字面量 59 处 + 变量 map 9 处，共 68 处），按调用形态分三类对账——（A）结构体入参的全量保存：白名单须覆盖调用方赋值过的全部可编辑列；（B）标量入参的保存：白名单即参数表，按签名逐项核对；（C）窄口径定向写（状态/计数/流量/生命周期）：只须覆盖其命名意图的列，其余列另有专属写路径。模型列取自 `internal/store/model/` 各结构体定义；调用方赋值以 grep + 逐函数读码确认。另附带核查 11 处同型 `UpdateColumns(map...)`（§4.5）。

### 4.1 A 类：全量保存（结构体入参）——6 处

| # | 位置 / 函数 | 模型 | 白名单与调用方赋值对账 | 结论 |
|---|---|---|---|---|
| A-1 | `repository_kernel.go:315` `SaveNodeInbound` | NodeInbound（31 列） | 调用方 9 处赋值全覆盖，唯 `conn_idle_seconds` 漏列；`kernel_id`/`node_id` 为创建时身份、无调用方重赋值；`upload_bytes`/`download_bytes` 为流量、函数注释明言不碰；`created_time` 不可变；`source_id` 无调用方赋值（同步新建分支走 Create） | **漏列 1 项，已补（§3.1）** |
| A-2 | `repository_remote_source.go:39` `SaveRemoteSource` | RemoteSource | 调用方（`remote_source.go:448`）赋值 name/url/interval_hours/relay_only/kind/role/enabled/updated_time 全部在列；`last_fetch`/`last_error`/`node_count`/`userinfo` 由专属 `UpdateRemoteSourceStatus` 写（调用方传参已核对） | 通过 |
| A-3 | `repository.go:1694` `UpdatePeerShare` | PeerShare | 调用方赋值 name/max_bandwidth/expiry_time/port_range_start/port_range_end/is_active/allowed_domains/allowed_ips/updated_time 全部在列；relay 凭据走专属 `UpdatePeerShareRelayCredentials`；token/node_id/current_flow 无调用方赋值 | 通过 |
| A-4 | `repository.go:1817` `UpdatePeerShareRuntime` | PeerShareRuntime | 调用方 4 处赋值 reservation_id/binding_id/role/chain_name/service_name/protocol/strategy/port/target/applied/status/updated_time 全部在列；resource_key/release_pending/share_id/node_id 无调用方赋值 | 通过 |
| A-5 | `repository_mutations.go:414` `UpdateNode`（变量 map） | Node | 白名单覆盖调用方全部 17 个参数；`secret` 创建时定、`version` 由心跳 `UpdateNodeOnline` 写、`status`/`inx` 各有专属方法、`is_remote`/`remote_url`/`remote_token`/`remote_config` 创建时定、`latency_targets` 由专属 `SetNodesLatencyTargets` 写、`expiry_reminder_dismissed` 编辑时恒清 0（有意） | 通过 |
| A-6 | `repository.go:829` `UpsertAnnouncement` | Announcement | content/enabled/updated_time 全覆盖；id/created_time 不可编辑 | 通过 |

### 4.2 B 类：标量入参保存（白名单即参数表）——15 处（14 内联 + 1 变量 map）

| # | 位置 / 函数 | 核对 | 结论 |
|---|---|---|---|
| B-1 | `repository_mutations.go:137` `UpdateUserWithPassword` | 参数 user/pwd/flow/flow_mib/num/exp_time/flow_reset_time/status/max_conn/password_changed_at/updated_time 逐一在列；role_id 不在编辑面参数内（创建时定，无调用方赋值） | 通过 |
| B-2 | `repository_mutations.go:160` `UpdateUserWithoutPassword` | 同 B-1 减 pwd 两列，逐一在列 | 通过 |
| B-3 | `repository_mutations.go:646` `UpdateTunnelTx` | 参数 name/type/flow/traffic_ratio/status/in_ip/ip_preference/protocol/probe_target_host/probe_target_port/updated_time 逐一在列；engine 走专属 `SetTunnelEngineTx`，inx 走 `UpdateTunnelOrder` | 通过 |
| B-4 | `repository_mutations.go:1008` `UpdateUserTunnel` | 参数 flow/flow_mib/num/exp_time/flow_reset_time/speed_id/status 逐一在列 | 通过 |
| B-5 | `repository_mutations.go:1107` `updateUserTunnelFields` | 与 B-4 同列，逐一在列 | 通过 |
| B-6 | `repository_mutations.go:1137` `UpdateForward` | 参数 name/tunnel_id/remote_addr/strategy/speed_id/max_conn/ip_max_conn/ip_speed_id/proxy_protocol×3/updated_time 逐一在列；user_id/user_name/status/inx 不在编辑参数内（回滚路径 `RollbackForwardFields` 另含 user_id/user_name/status，系快照恢复口径） | 通过 |
| B-7 | `repository_mutations.go:1349` `RollbackForwardFields` | user_id/user_name/name/tunnel_id/remote_addr/strategy/status/speed_id/max_conn/ip_max_conn/ip_speed_id/proxy_protocol×3/updated_time 逐一在列 | 通过 |
| B-8 | `repository.go:670` `UpdateUserNameAndPassword` | user/pwd/password_changed_at/updated_time 逐一在列 | 通过 |
| B-9 | `repository_mutations.go:181` `UpdateUserPassword` | pwd/password_changed_at/updated_time 逐一在列 | 通过 |
| B-10 | `repository.go:984/994` `UpdateNodeOnline` / `UpdateNodeStatus` | status/version/http/tls/socks/updated_time；status/updated_time 逐一在列 | 通过 |
| B-11 | `repository.go:1710` `UpdatePeerShareRelayCredentials` | relay_username/relay_password/updated_time 逐一在列 | 通过 |
| B-12 | `repository_auth.go:42` `RevokeUserSessions` | password_changed_at/updated_time 逐一在列 | 通过 |
| B-13 | `repository_passkey.go:49` `UpdatePasskeyCredential` | credential_json/last_used_at 逐一在列 | 通过 |
| B-14 | `repository_mutations.go:1431` `UpdateSpeedLimit`（变量 map） | 参数 name/speed/status/updated_time 逐一在列；`tunnel_id`/`tunnel_name` 写 NULL——有意：该两列为废弃绑定列，创建时即 NULL（`CreateSpeedLimit`），启动迁移 `migrateSpeedLimitTunnelBinding` 统一清零，编辑面无 tunnel 参数，写 NULL 与全仓口径一致，不属漏列 | 通过 |

### 4.3 C 类：窄口径定向写——40 处

均为命名意图明确的单用途写，其白名单即意图列；其余列另有专属写路径或不在该操作语义内。逐项如下（位置 — 意图列 — 结论）：

- `node_seq.go:70/138` `CheckAndUpdateSeq` / `advanceSeqTx` — last_seq/updated_time — 通过
- `peer_share_runtime_lifecycle.go:15/22` `SetPeerShareRuntimeReleasePending` / `CompletePeerShareRuntimeRelease` — release_pending / status/applied/release_pending + updated_time — 通过
- `repository.go:1793` `ResetPeerShareCurrentFlow` — current_flow/updated_time — 通过
- `repository.go:1832` `MarkPeerShareRuntimeReleased` — status/updated_time — 通过
- `repository.go:2036` `UpdatePeerShareRuntimeServiceName` — service_name/applied/updated_time — 通过
- `repository.go:2060` `markPeerShareRuntimeReleasedByPortTx` — status/applied/service_name/updated_time — 通过
- `repository.go:2081` `MarkForwardPeerShareRuntimeReleasedByServiceName` — 同上 — 通过
- `repository.go:5146` `ResetUserMonthlyFlow` — in_flow/out_flow/flow_last_reset_time — 通过
- `repository.go:5174` `ResetUserTunnelMonthlyFlow` — in_flow/out_flow/flow_last_reset_time — 通过
- `repository_federation_cleanup.go:34/42` `MarkFederationTunnelBindingPendingRelease` / `SetFederationTunnelBindingStatus` — status/updated_time — 通过
- `repository_flow.go:128` `UpdateForwardStatus` — status/updated_time — 通过
- `repository_kernel.go:358` `ClearNodeInboundRouting` — outbound_id/split_rules/updated_time（R14-A5-08 毒化行清理，意图列） — 通过
- `repository_kernel.go:397` `ResetNodeInboundTraffic` — upload_bytes/download_bytes/updated_time — 通过
- `repository_kernel.go:639` `applyInboundCounterChunk` — upload_bytes/download_bytes — 通过
- `repository_mutations.go:194` `PropagateUserFlowToTunnels` — flow/flow_mib/num/exp_time/flow_reset_time — 通过
- `repository_mutations.go:296/303/312` `ResetUserFlowByUser`（User + UserTunnel 两段）/ `ResetUserFlowByUserTunnel` — in_flow/out_flow（+updated_time）— 通过；注：手动清零不写 `flow_last_reset_time`，该列语义为“每日任务实际执行的月度重置时刻”，月度任务 `ResetUserMonthlyFlow` 专属写，手动清零不在其语义内，不属漏列
- `repository_mutations.go:321` `ResetForwardFlow` — in_flow/out_flow/updated_time — 通过
- `repository_mutations.go:484/637/1159` `UpdateNodeOrder` / `UpdateTunnelOrder` / `UpdateForwardOrder` — inx/updated_time — 通过
- `repository_mutations.go:1172` `updateForwardTunnelTx` — tunnel_id/updated_time — 通过
- `repository_mutations.go:2285` `BatchUpdateForwardStatus` — status/updated_time — 通过
- `repository_nftables.go:150` `MarkNftRuleBindingError` — status/last_error/updated_time — 通过
- `repository_subscription.go:261` `SaveSubscriptionWithResetTime` — traffic_reset_time/traffic_since — 通过
- `repository_subscription.go:423` `ApplySubscriptionCounterSamples` — upload_bytes/download_bytes — 通过
- `repository_subscription.go:469` `ResetSubscriptionTraffic` — upload_bytes/download_bytes/traffic_reset_time/traffic_since — 通过
- `repository_user_quota.go:245/258/333/345` `AddUserQuotaUsage` / `addUserQuotaUsageBatchTx`（各两段）— daily_used_bytes/monthly_used_bytes/day_key/month_key/updated_time — 通过
- `repository_user_quota.go:372` `MarkUserQuotaDisabled` — disabled_by_quota/disabled_at/paused_forward_ids/updated_time — 通过
- `repository_user_quota.go:400` `UpdateUserQuotaPausedForwardIDs` — paused_forward_ids/updated_time — 通过
- `repository_user_quota.go:453/497` `ResetUserQuotaUsage` / `RollUserQuotaWindows` — daily/monthly_used_bytes/day_key/month_key/disabled_by_quota/disabled_at/paused_forward_ids/updated_time — 通过；限额列 daily/monthly_limit_gb 由专属 `SaveUserQuotaConfigTx`（`repository_user_quota.go:158/183` 变量 map，daily_limit_gb/monthly_limit_gb/updated_time + 首建 day_key/month_key）写 — 通过
- `repository_user_subscription.go:100` `MarkSubscriptionPendingDelete` — pending_delete/enabled — 通过

### 4.4 变量 map / 动态 map 补充（9 处间接调用点，8 行；其中 `UpdateNode` 计入 A-5、`UpdateSpeedLimit` 计入 B-14，不重复计）

| # | 位置 / 函数 | 核对 | 结论 |
|---|---|---|---|
| D-1 | `repository_kernel.go:119` `UpdateNodeKernelFields`（调用方传参 map） | 调用方 8 处传参均只含意图列（enabled/version/status/config_hash/applied_hash 等单列或小集合），无全量保存语义 | 通过（动态 API，按调用方核对） |
| D-2 | `repository_remote_source.go:49` `UpdateRemoteSourceStatus`（调用方传参 map） | 调用方传 last_fetch/last_error/node_count/userinfo，与 SaveRemoteSource 的分工闭合 | 通过 |
| D-3 | `repository_subscription.go:87` `shareOwnSubscriptions`（迁移） | kind/applied_active（+ 缺失时 uuid/password），迁移意图列 | 通过 |
| D-4 | `repository_mutations.go:1500` `groupUpdateTx` | name/status/updated_time；TunnelGroup/UserGroup 模型仅含 id/name/created/updated/status，全覆盖 | 通过 |
| D-5 | `repository_inbound_backup.go:937` 恢复后剥离 fixup | 仅 outbound_id/split_rules/metering_enabled 三列条件剥离；恢复本体为全结构 OnConflict upsert，其 `DoUpdates` 列单明确含 `conn_idle_seconds`（`:575`），备份导出/导入三处接线均已含该列（`backup_connidle_test.go` 覆盖） | 通过 |
| D-6 | `repository_user_quota.go:183` `SaveUserQuotaConfigTx` | 见 §4.3 末行 | 通过 |
| D-7 | `repository_mutations.go:442` `UpdateNode` | 见 A-5 | 通过 |
| D-8 | `repository_mutations.go:1447` `UpdateSpeedLimit` | 见 B-14 | 通过 |

### 4.5 非 map 写路径（同类排查附带）

- `UpdateColumns(map...)` 11 处（同型列 map，全数核查）：`repository.go:166/175/184` 流量累计（`AddFlow` 前段：Forward/User/UserTunnel 的 in_flow/out_flow `gorm.Expr` 累加）、`repository.go:1007/1014/1022`（`AddFlow` 三段同上）、`repository.go:1777` `AddPeerShareCurrentFlow`（current_flow 累加 + updated_time）、`repository.go:5392` `migrateSpeedLimitTunnelBinding`（迁移清零 tunnel_id/tunnel_name，见 B-14）、`repository_nft_pending_delete.go:51/73/190`（PendingDelete 重试 bookkeeping：status/next_retry_at/retry_count/last_error/updated_at）。全部为窄口径计数/状态写，无全量保存语义，无漏列 — 通过。
- `repository_inbound_backup.go:766` 恢复订阅 `tx.Save(&item)`：全结构 upsert，备份字段逐一赋值，无白名单 — 通过。

- `saveSubscriptionTx`（`repository_subscription.go:246`）：`Omit("upload_bytes","download_bytes","traffic_reset_time","traffic_since").Save(s)`——流量列有意保留，注释在位；其余列全量写，无白名单漏列问题 — 通过。
- `UpdateServiceMonitor`（`repository.go:6205`）：全结构 `Save`，无白名单 — 通过。
- `SetTunnelEngineTx` / `SetNodesLatencyTargets`：单列 `.Update()`，意图列 — 通过。
- 备份导出/导入：`InboundBackup` 结构体、导出、恢复三处均含 `ConnIdleSeconds`，既有 `TestBackupConnIdleRoundTrip` 覆盖 — 通过。

**复核总账：68 处 `Updates`（A 6 + B 15 + C 40 + D 余 7）与 11 处 `UpdateColumns` 全部核对完毕；新增漏列 1 项（A-1 的 `conn_idle_seconds`，已补）+ 同列第二道闸 1 项（§3.2，已补）；其余 66 处 `Updates` 与 11 处 `UpdateColumns` 无漏列。**

## 五、回归（先红后绿）

- 红：`R20-F02-red.log`——`TestConnIdleEditPersistsAndRenders` FAIL（`list connIdleSeconds = 5, want 10 (edit silently dropped)`，与 R20-X-02 真机现象同签名）；`TestConnIdleCompanionSyncMirrorsOrigin` FAIL（`syncCompanion must propagate the origin timeout change to the companion`）。两测试均为本轮新增，基线无此测试。
- 绿：`R20-F02-green.log`——两测试均 PASS（0.77s / 1.36s），exit=0。
- 计时腿：按任务书只作行为对照，不钉 2× 值（2× 语义属 R20-X-03，另案处理）；未做计时腿。

## 六、go test / build / vet / tsc

- `go test ./...`（go-backend，全量，首跑）：`R20-F02-gotest-full.log`——13 包 ok；FAIL 4 项，分解如下：
  - `internal/health`：唯一产品红 `TestValidateMonitorTCPTarget/公网域名放行`——已知沙箱 fake-IP DNS（example.com 落 198.18/15 保留段），与基线同签名核对通过（见下）。
  - `internal/http/handler`：非产品失败——默认 10m 超时告警（`panic: test timed out after 10m0s`，1014 测试；告警时正在跑 `TestR2B10NodeRecoveryFailureResendsOnce`，58s，仍在运行未失败；超时前该包零 FAIL）。属沙箱跑速不足，非产品失败。
  - `internal/ws` / `tests/contract`：首跑 `[build failed]`——Go 构建缓存损坏（`could not import go-backend/internal/auth (open ../../../.cache/go-build/04/...-d: no such file or directory)`），环境问题非产品问题；重跑即过（见下）。
- 基线同签名核对：`R20-F02-baseline-dns.log`——在全新 pristine worktree（b8fb567，未改动）单跑 `TestValidateMonitorTCPTarget`，同为仅 `公网域名放行` 一格 FAIL（`example.com:443 err=目标地址不允许（内网/保留地址）`），与修复后全量跑的签名完全一致。
- 重跑：`R20-F02-gotest-ws-contract.log`——`internal/ws` ok（19.6s）、`tests/contract` ok（156.1s），exit=0。
- `R20-F02-gotest-handler-30m.log`——handler 包 `-timeout 30m` 重跑：`ok go-backend/internal/http/handler 730.592s`，exit=0（1014 测试全过；首跑的 10m 超时为沙箱跑速所致，`-timeout` 只放宽运行器时限，未动任何断言）。
- `R20-F02-gotest-gostx.log`——go-gost/x 全量：27 包 ok，0 FAIL，exit=0（与 R19“27 包 ok”一致）。
- `R20-F02-gotest-gost.log`——go-gost 主模块：`tests/lifecycle` ok，exit=0。
- `go vet ./...`：exit=0（`R20-F02-vet.log`，仅 GOPATH/GOROOT 同目录警告）。
- `go build ./...`（go-backend）：exit=0（`R20-F02-build.log`，仅同警告）。
- `go build ./...`（go-gost、go-gost/x）：exit=0（未改动模块，仅编译确认）。
- 前端 `tsc --noEmit`：exit=0，空日志（`R20-F02-tsc.log`）；本项无前端改动。

## 七、未验证清单

- 真机腿：未做（本项为沙箱内先红后绿 + 全量回归；R20 任务书的真机腿属 R21 重审面）。R20-X-02 的真机现象（list 显旧值、渲染旧 policy）已由本轮沙箱链路测试同构覆盖（建 5 → 改 10 → list 10 → 渲染 `{connIdle:10}`）。
- 计时腿（idle=N 实际关断时长）：未测，按任务书属 R20-X-03 另案。
- 浏览器 DOM 腿：不适用（后端修复，无前端改动）。

## 八、交付

- 分支：`fix-r20-f02`，修复提交 `af2c24102e8a4ac259a8c9fb514d95f90ad75f0b`（`af2c241`）；本报告为随后单独提交，分支尖端见 `git log`
- 提交内容：2 个改动文件 + 1 个新测试文件 + 本报告 + 11 个日志文件（`git add -f` 显式入库，`*.log` 在 `.gitignore` 内）。
- 未合 main，未动 main，未打 tag，未发版。另建的 pristine 基线 worktree `~/workspace/.fixr20-baseline` 仅用于 DNS 签名核对，未作任何改动。
— R20-FIX-F02-REPORT END · 自证：本行之前正文字节数 20054 · END
