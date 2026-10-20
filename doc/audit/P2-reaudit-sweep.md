# P2/P3 修复复审报告（P2-reaudit-sweep）

- 复审时间：2026-10-20 11:31 CST
- 复审基线：main@152ac29（1.0.8）+ 工作区未提交改动（45 文件，+1358/−237）
- 复审方式：逐项代码核查（文件:行号证据）+ 全量回归测试 + 干净 worktree 基线对照
- 复审员立场：只读复审，不改代码

## 结论

**4 路修复共核查 40+ 项，39 项真实落地且无回归；1 项漏修：O1-002。**

- 新发现 P0：0
- 新发现 P1：0
- 新发现 P2：1（O1-002 漏修，属原清单未完成项）
- 回归：无新增失败（见回归结果节）

**发版建议：先修 O1-002 再发 1.0.9；其余部分可安全发版。**

## 逐域核查

### 域 1（A 路：订阅/认证/前端）— 9/9 通过

| 项 | 证据 | 结论 |
|---|---|---|
| K-002 sig v2 绑定 token | user_inbound.go:45 `subURLSigForToken`（`flvx-subsig-v2:{token}:{userID}`）；:67 `verifySubURLSigForToken` 先 v2、回退 v1、常量时间比较；kernel_relay.go:561 验证点已切换；v1 通过时 `logSubSigV1Used()` 弃用警告（:24） | ✅ 旧链接兼容 |
| K-003 渐进延迟 | login_rate_limit.go:288 `progressiveDelay`（1s 起翻倍、上限 5s）；:403-417 仅 violations 新递增时 sleep | ✅ 粒度合理（避免正常请求被拖 5s） |
| K-004 XFF fail-closed | login_rate_limit.go:94-103：链长不足忽略全部 XFF、回退 RemoteAddr、log 警告 | ✅ |
| K-005/O11-P2-1 Passkey 限流 | passkey.go begin/finish 各调 `checkLoginRateLimit`（2 处） | ✅ |
| O11-P3-1 删 passkey 吊销 JWT | passkey.go 删除后 `RevokeUserSessions` 推进 password_changed_at | ✅ |
| O4-005 NormalizeRuleGroups | subscription.go:91 `p.RuleGroups = kernel.NormalizeRuleGroups(p.RuleGroups)`（在 normalize() 保存路径） | ✅ |
| N-002 session.ts 旧键清理 | session.ts:12 `LEGACY_SESSION_KEYS=["role_id","admin"]`，:33 `getRoleId` 改从 token 解析 | ✅ |
| N-003 403 全局处理 | network.ts:68 `handleForbidden` 覆盖业务信封 code=403 与 HTTP 403；401 逻辑未动 | ✅ |
| H-002 测试期望 | share_clients_test.go:43,62 期望 `www.amd.com`；kernel 包测试全过 | ✅ |

### 域 2（B 路：备份/用户/Repository）— 全部通过

| 项 | 证据 | 结论 |
|---|---|---|
| O7-006 ExpiryReminderDismissed | model.go:657 struct 字段；repository.go:2266 导出、:2812 导入 | ✅ |
| O7-007 announcement 备份 | repository.go:2437 `exportAnnouncements`，ExportAll:2059 / Import:2169 已接入 | ✅ |
| O7-008 monitor_permission 备份 | repository.go:2459 `exportMonitorPermissions`，:2066/:2176 已接入 | ✅ |
| O7-009 版本校验 | repository.go:2543 `backup.Version != "1.0"` 拒绝导入 | ✅ |
| O7-010 自动备份失败继续 | handler.go:1815：log 警告后继续导入（旧逻辑中断恢复） | ✅ |
| O2-003 chain_tunnel 预检 | repository_nft_pending_delete.go `FindOtherForwardOnNodePortTx` 追加 chain_tunnel (node_id, port) 查询，注释标注 O2-003 | ✅ |
| O2-004 ReplaceForwardPorts 锁 | repository_mutations.go:983 per-node 端口分配锁 + 冲突中文错误 | ✅ |
| O2-005 限速 status | repository_flow.go:375 `SpeedLimitExists`、:406 `GetSpeedLimitSpeed` 均加 `status = 1`；调用方 control_plane.go:331/346 均为 `err == nil && speedVal > 0` 才生效，禁用规则正确回退到隧道级限流 | ✅ 语义复核通过 |
| O5-002 用户名长度 | mutations.go:49（create）、:173（update）1-100 校验 | ✅ |
| O5-003 分页 | handler.go:618：current/size 分页，未传参保持全量兼容 | ✅ |
| O5-004 建用户事务 | mutations.go:114：BeginTx + CreateUserTx + SaveUserQuotaConfigTx + AddUserToGroupsTx + Commit，defer Rollback | ✅ |
| O5-005/006/007/008 | mutations.go:70/81/88/102（create 与 update 双份校验） | ✅ |
| O11-P3-2 updateConfigs | handler.go:1140 批量先全量校验 + 事务 | ✅ |
| O11-P3-3 重命名 | handler.go:573 `isAuthenticated` | ✅ |

### 域 3（C 路：转发/计费/节点/升级）— 1 项漏修

| 项 | 证据 | 结论 |
|---|---|---|
| O2-002 死代码删除 | flow_policy.go:230 `scaleFlowByTunnel` 已删（仅剩说明注释） | ✅ |
| O2-006 配额边界 | flow_policy.go:276/345 两处统一 fail-closed（等于上限也拦截） | ✅ |
| O6-002 InFlow/OutFlow 统一 | flow_upload_batch.go:83：InFlow=item.U（上传）、OutFlow=item.D（下载），quotaUsage=scaledIn+scaledOut 总额不变。**深度复核**：订阅头 `buildSubscriptionHeader(upload=OutFlow...)` 的参数交换逻辑与新语义恰好一致（header upload=InFlow=上传 ✓）；nftables 路径 InFlow=BytesIn（上传）两路统一 | ✅ 无回归，反而修正 gost 路径订阅头方向 |
| L-003 ASN 缓存上限 | node_route.go:58 `maxHopASNCacheSize=10000`，:92 超限清空 | ✅ |
| O3-002 relayTarget IPv6 | kernel_relay.go:160 IPv4 为空回退 ServerIPV6 | ✅ |
| J-005 tune 白名单 | node_tune.go:17/50 handler 侧 group 白名单 | ✅ |
| O9-003 下载前校验 | system_upgrade.go:747 version 确定后立即校验 | ✅ |
| O9-004 语义版本比较 | system_upgrade.go:644/707 compareVersions | ✅ |
| O9-005 节点降级拒绝 | upgrade.go:214（单个）、:306（批量） | ✅ |
| O9-007 ".." 拒绝 | system_upgrade.go:229 全点号版本拒绝 | ✅ |
| **O1-002 nodeCreate TOCTOU** | **mutations.go:453 仍为 `nodeID, err := h.findCreatedNodeID(name, serverIP)`，git diff 无此项改动** | ❌ **漏修** |

### 域 4（D 路：通知/导入/联邦）— 全部通过

| 项 | 证据 | 结论 |
|---|---|---|
| O8-001 启动离线扫描 | notify.go:460 `checkStartupOfflineNodes`；notify_reports.go 启动调用 | ✅ |
| O8-002 重试 + 落盘时机 | notify.go:187 `sendWithRetry`；notify_reports.go:191 先发成功再落盘 | ✅ |
| O8-003/O12-002 forgetNode | notify.go:406 `forgetNode`、:423 `pruneStaleNodeAlerts`、:510 删除前校验 | ✅ |
| O8-004 过期跳过 | notify.go:41 24h 阈值；notify_reports.go 用户/订阅跳过已过期 | ✅ |
| O8-005 内核错误去重 | notify.go:78 `recentSent`、:149 去重窗口、:215 统一入口 | ✅ |
| O8-006 "none" 兼容 | notify.go:130 字面量显式视为禁用 | ✅ |
| O12-003 联邦并发上限 | federation.go:1942 信号量限 10 | ✅ |
| IMP-003 RelayOnly 同步 | remote_source.go:161/169/275/316 | ✅ |
| IMP-004 去重 | kernel_external.go:335 externalKey 去重 + 中文错误 | ✅ |
| IMP-005 死代码删除 | kernel_external.go:71（validateSubscriptionURL 仅剩删除说明注释，全仓无引用） | ✅ |
| IMP-006 http 提示 | remote_source.go:330/342 | ✅ |
| IMP-009 大小写不敏感 | remote_source.go:70/123 EqualFold | ✅ |
| IMP-002/008/010 | import.md UA 与 500 上限说明；proxy_parse.go:496 别名注释 | ✅ |

### 父代理合并与补修 — 全部通过

| 项 | 证据 | 结论 |
|---|---|---|
| K-002 验证点合并 | kernel_relay.go:561 `verifySubURLSigForToken` | ✅ |
| K-005 合并 | passkey.go 2 处 `checkLoginRateLimit` | ✅ |
| L-002 ctx 快速返回 | node_latency.go probe 函数 ctx select | ✅ |
| O1-004/J-003 探测收敛 | node_latency.go `maxProbeAllNodes=50`、截断响应 | ✅ |
| J-004 targets 上限 | node_latency.go `maxLatencyTargetSetIDs=200` | ✅ |
| O1-005/L-008 节点存在性 | node_latency.go targets 设置前 ListMonitorNodes 校验 | ✅ |
| J-002 镜像限流（补修） | gh_mirror.go:46/56/69 每 IP 60/min + 桶数上限 10000 fail-closed | ✅ |
| O3-003 批量删隧道白名单（补修） | mutations.go tunnelBatchDelete 收集受影响入站并 `refreshInboundWhitelist` | ✅ |

### 测试改动复核（清单外文件）

- audit_regression_20261019_test.go、flow_ownership_test.go、flow_upload_batch_test.go、nftables_traffic_test.go、notify_test.go、repository_backup_test.go、passkey_contract_test.go、kernel_external_h15_test.go：均为配合新语义的期望值同步，注释说明充分。抽查 flow_ownership_test 的注释论断属实（repository_flow.go:84 缺失隧道行 TunnelFlow 默认归 1）。
- repository_auth.go：A 路 RevokeUserSessions（已披露）。repository_control.go / repository_flow.go / repository_nft_pending_delete.go：B 路 O2-003/O2-005 的实际落点（函数本就在这些文件），属域内合理改动。
- 无清单外业务代码被误改。

## 新发现问题

### P2-1（漏修）：O1-002 nodeCreate 并发 TOCTOU 未修复

- 文件：`go-backend/internal/http/handler/mutations.go:453`
- 现象：`nodeCreate` 在 `CreateNode` 成功后仍用 `findCreatedNodeID(name, serverIP)` 按名+IP 反查 ID，并发创建同名同 IP 节点时可能拿到错误节点的 ID，后续 `persistNodeSSHConfig` 写到错误节点上。
- 复现：并发两个相同 name+serverIP 的 nodeCreate 请求（管理员操作，概率低但路径存在）。
- 修复方向：`CreateNode` 改签名为返回 `(int64, error)`（全仓仅 mutations.go:423 一个调用方，改动面小），或 CreateNode 内部用 GORM Create 后回填 ID。修复后需重跑 handler 测试。
- 分级：P2（与原 O1 审计定级一致；管理员触发、概率低，但属本轮承诺修复项，必须补修后才算循环收敛）。

## 回归结果

- `go test ./internal/... -count=1`：失败 4 个，**全部预存**：
  - `TestDownloadReleaseAssetUsesGithubProxyWhenEnabled`、`TestDownloadReleaseAssetRejectsOversizedBody` — known-baseline ② TLS 环境类（沙箱 DNS/证书），多轮复现一致。
  - `TestOpenBackfillsSQLiteLegacyTunnelProbeTargetColumns`、`TestOpenBackfillsSQLiteLegacyForwardColumns` — 本复审用干净 worktree（HEAD=152ac29，无工作区改动）实测**同样失败**（`auto migrate: NOT NULL constraint failed: tunnel__temp.name`），确认与本轮改动无关，为 1.0.8 基线既存失败（测试夹具旧表缺 NOT NULL 默认值）。
- `TestLoonList` / `TestQXList`：**已变绿** ✅（kernel 包 ok，H-002 修复生效）。
- `go build ./...` ✅；`npx tsc --noEmit` ✅（exit 0）。
- handler 包其余测试全过（含 TestA001/TestD11-13 登录限流合同、passkey 合同、计费相关测试）。

## 总结

本轮 P2/P3 修复整体质量高：40+ 项中 39 项真实落地、语义复核通过、无回归、无清单外误改；stash 事故的合并与两处补修（J-002/O3-003）均已正确落位。唯一未完成项 **O1-002**（P2）需补修后复审该单项，通过即可发 1.0.9。
