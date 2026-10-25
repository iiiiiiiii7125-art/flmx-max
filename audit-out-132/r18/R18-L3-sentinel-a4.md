# R18-L3 报告：哨兵回归（S-01…S-53）+ 轮换域 A4 全量抽审

- **轮换域为 A4**（转发与计费；R15=A5、R16=A2、R17=A3，本轮轮换 A4）。
- **任务**：R18 再审计 L3 路——哨兵回归 + 轮换域 A4 全量抽审；只读审计，不改产品代码、不打 tag、不发版。
- **基线**：main @ a0c18a0（`a0c18a0dc90b2f9c7a67e8a2a016cc1d97505fb5`，2026-10-25 01:33:23 +0000，「docs(r17-d): integration report + acceptance evidence (R17-X-01)」；树哈希 `f11ac9e88f5f684078be63d166af72dfbca96e33`；origin/main 与本地 main 一致）。哨兵表 `R18-00-sentinel.md` 与基线 `R18-00-baseline.md` 开工前落盘（N-37 细则①③）。
- **读数树**：只读 worktree `~/workspace/.r18-tree` @ a0c18a0（detached HEAD，`git status` 空，全程 pristine）；测试在 `git archive a0c18a0` 快照 `~/workspace/.r18-tmp/r18snap` 上跑，探针文件跑后即删；共享工作树（feat-latency-hint @ a99d68d）未用作读数树。
- **结论口径**：PASS = 代码在位 + 对应测试在基线快照实跑通过；「本地不可判定·承继门证据」= 本地无条件复跑（无 DSN / 无测试机），承继已落盘的门证据，**不标 PASS、不计失败**；DOM 腿（无 live browser）与历轮同口径记「代码面在位，DOM 腿未验证」。

## 0. 总账

| 项 | 结论 |
|---|---|
| 哨兵 53 行 | **51 PASS + 2 本地不可判定·承继门证据（S-16、S-34；S-41 的 PG 子格同口径承继，行级不降格），0 回退** |
| A4 新立案 | **1 条：R18-X-01（P3，产品）**——A4-008 携入未闭合（gost 删隧道逐条清理半完成披露）；其余 A4 原立案 9 条（A4-001…A4-007 确定 + A4-009/A4-010 疑似）本轮全部复核为已闭合/结构性消除（见 §2） |
| R13 关闭 7 项（R13X-001…007） | **7/7 维持关闭**（见 §3） |
| FINAL-P2-1 七段谓词 | **维持关闭**（谓词七段在位，行为测试 4/4 PASS，见 §3） |
| R17 修复在位复核 | R17-A（agent 去重 + 毒化自愈）、R17-B（AddService 幂等守卫 + dispatch record）、R17-C（agent 启动日志可查）在位；守卫三处调用点全为 freshAbsence=true，离线钩子作废确认集（见 §2.4） |
| 观察项（不立案） | 2 条：O-L3-1（dedupe first-wins 在启动去重面）、O-L3-2（guardedAddService skip-confirmed 分支生产不可达） |

## 1. 全量套件计数（基线快照实跑，日志落盘 r18/）

| 套件 | 计数 | 日志 |
|---|---|---|
| handler 全量 `-v` | **1316 PASS（顶层 988）/ 0 FAIL / 4 SKIP**，`ok 315.061s` | `R18-L3-handler-full.log`（重跑；首跑磁盘满污染日志另存 `R18-L3-handler-first-run-diskfull.log`） |
| kernel 全量 `-v` | **431 PASS（顶层 264）/ 0 FAIL / 0 SKIP** | `R18-L3-kernel-full.log`（首跑，exit 0，重启动前完成） |
| repo 全量 `-v` | **267 PASS（顶层 234）/ 0 FAIL / 0 SKIP**，`ok 76.832s` | `R18-L3-repo-full.log`（重跑；重启中断的部分日志另存 `R18-L3-repo-partial-reboot.log`，中断前 229 PASS / 0 FAIL） |
| netguard | **8 PASS / 0 FAIL / 0 SKIP** | `R18-L3-netguard.log` |
| PG 契约靶向 | **3 SKIP**（无 DSN，本地不可判定，见 S-34/S-41） | `R18-L3-contract-pg.log`（包路径 `tests/contract`） |
| go-gost | **root lifecycle ok + x 模块 27 ok / 0 FAIL** | `R18-L3-gost.log` + `R18-L3-gost-x.log` |

- 计数口径：`--- PASS` 含缩进子测试行（与 R16/R17-L2 报告口径一致；括号内为顶层 `^--- PASS` 数，供交叉核对）。
- handler 算术：1316 = R17 的 1306 + R17-B 新增守卫测试净增 10（`add_service_guard_test.go` 恰 10 个顶层 Test func，顶层 978→988 同 delta）；kernel/repo/netguard 与 R17 逐数一致（本轮 A4 核心文件 diff = 0，见 §3.1）。handler 4 SKIP 与 R17 同名同形态：`TestInboundProbeEmptyIDsDoesNotClearDown`、`TestR2B10InboundRecoveryFailureResendsOnce`（S-16 导入腿）、`TestR5B9ImportedNodeDownScheduledRoundAlerts`、`TestR5B9ImportedNodeRecoveryScheduledRoundAlerts`。
- 方法注记（两次环境红，均已处置、均不属产品失败）：
  - 首跑 handler exit=1、86 FAIL：根因为沙箱 `/tmp` 为 512M tmpfs，`go test` 构建工作目录 + SQLite 临时库将其撑满，日志内 `database or disk is full` 101 处、尾行 `testlog.txt: no space left on device`；86 个 FAIL 均为该时段内建临时库的用例（含 S-31/S-32、A4-004/A4-010 相关用例），非产品回归。处置：重跑时 `GOTMPDIR`/`TMPDIR` 改到持久盘（`~/.r18-tmp/gotmp`、`~/.r18-tmp/tmp`），重跑 0 FAIL、首跑污染日志原样保留为 `R18-L3-handler-first-run-diskfull.log`。
  - 首跑 repo 在 01:46:18 被 VM 重启中断（`uptime` 显示重启约 01:46，runner 进程与测试进程同亡，无 exit 行；中断前 229 PASS / 0 FAIL，日志保留为 `R18-L3-repo-partial-reboot.log`）；重跑 exit=0。kernel（01:43:04–01:43:22，exit 0）在重启前完成，日志有效。
  - 套件顺序执行（handler → kernel → repo → netguard → contract-pg → gost），吸取 R17 并行首跑跑满默认 `-timeout 10m` 中断的教训；handler 以 `-timeout 30m` 单跑。PG 契约靶向初跑误指 `internal/store/repo` 包（`no tests to run`），纠正为 `tests/contract` 包后 3 SKIP（R17 同形态）。
- 钉死值复核：路由 **202**（`handler.go` 201 处 `mux.HandleFunc` + `router.go:13` 1 处 `mux.Handle` `/system-info`；快照内 `mux.HandleFunc` 去重 203 含 2 处测试文件内注册，生产口径 202，与 R17 钉死值一致）；数据表 **49**（48 处 `TableName()` 在 `model/*.go` + `NodeSeq.TableName()` 在 `repo/node_seq.go`，与 L4 路 `R18-L4-tables-raw.txt` / `R18-L4-routes-raw.csv` 交叉一致）。

## 2. 哨兵逐行表（S-01…S-53）

（逐行证据见下表；行号均为本轮基线树 a0c18a0 实测值，相对 R17 有漂移处逐行注记。）

| 哨兵 | 对象 | 结论 | 证据（本轮基线树实测） |
|---|---|---|---|
| S-01 隧道删除 | PASS | `TestTunnelDeleteToleratesRemoteFailureAndDeletesTunnel`、`TestR1B3TunnelDeleteMigrationRecomputesRelayRemoteAddr`、`TestR1B3TunnelDeleteGostNodeOfflineSucceeds`、`TestR1B3TunnelDeleteNftNodeOfflineGoesPending`、`TestR1B3TunnelDeleteOnlineRealFailureAborts` 5/5 PASS；`tunnelDelete` + `tunnel_delete_resolution.go` 在位 |
| S-02 分组禁用撤销授权 | PASS | `TestB2_XP12_GroupTunnelDeleteRevokesGroupGrants`、`TestR2B7_013_DisableUserGroupRevokesGrants` / `DisableTunnelGroupRevokesGrants` 3/3 PASS |
| S-03 监控写守卫 | PASS | 四写入口 `monitoring.go:442/:582/:703/:731`（create/update/delete/run）均在位，与 R17 同值；`ensureMonitoringAccess` 在 `monitoring.go` 内 10 处调用在位；`TestR1MonitorWriteRequiresAdmin` PASS |
| S-04 导入 host 校验 | PASS | kernel：`TestR1B5ValidateImportHostRejectsNonCanonical`、`TestR2B1ValidateImportHostLoopbackAliases` / `HardDenyLiterals` / `PrivateLiteralsStillAllowed`、`TestN01ValidateImportHostLinkLocalV6Rejected` / `ULAStillAllowed` 全 PASS |
| S-05 导入跳过缺失用户 | PASS | repo：`TestR1B5ImportUserSubscriptionsSkipsMissingUser`、`TestBackupImportUserSubscriptionsSkipsUnresolvable` PASS |
| S-06 通知失败次轮重发 | PASS | `TestR1KernelErrorNotifyFailureResendsNextRound`、`TestR1RouteChangeNotifyFailureResendsNextRound` PASS |
| S-07 install.sh 更新路径 | PASS | `install.sh` 本轮 diff 仅 R17-C 日志路由（systemd `StandardOutput/StandardError: null→journal`、OpenRC 日志 `/dev/null→/var/log/flux_agent*.log`，:476-504 段）；哨兵段本体未动、行号 +8：`flux_agent_health_ok` :597、双采样 `sleep 3` 后复检 :957-958、`.bak` 仅健康通过后删除 :963（失败分支先 `return 1`，:958-970） |
| S-08 联邦端口占用 | PASS | `TestFederationTunnelCreateRejectsOccupiedPort`、`TestFederationRuntimeReservePortRejectsWhenShareFlowExceeded` PASS；锁内复检 `validateForwardPortAvailabilityTx` 在位 |
| S-09 订阅重置轮换全凭据 | PASS | `TestR1B7MainSubscriptionResetStillRotatesFullCredentials`、`TestR1B7LegacySubInfoResetRotatesFullCredentials`、`TestSubInfoResetStaysAdminOnlyAndRotates` PASS |
| S-10 单远端地址非公网拒绝 | PASS | `TestR2B1CheckSingleRemoteAddrNonPublicDenied` PASS；`IsSafeRemoteAddr`（`security_utils.go:26`）在位 |
| S-11 Snell 版本域 | PASS | kernel：`TestR2B5SnellFinishProxyVersionDomain` / `TestR2B5SnellRenderersVersionDomain` / `TestR2B5SnellChainTargetRenderWith` / `TestR2B5SnellV6ModeRoundTrip` 4/4 PASS |
| S-12 导入 host 校验（内核面） | PASS | 同 S-04 测试族（kernel 日志内全 PASS） |
| S-13 白名单刷新 | PASS | R2B9 白名单刷新 6 项 PASS：`TunnelDeleteWithForwardsDeleteRefreshesWhitelist`、`TunnelBatchDeleteWithForwardsDeleteRefreshesWhitelist`、`TunnelDeleteWithForwardsReplaceRefreshesWhitelist`、`ForwardBatchChangeTunnelRefreshesWhitelist`、`UserDeleteRefreshesWhitelist`、`NodeUpdateAddressRefreshesWhitelist` |
| S-14 seq 只读后推进 | PASS | `TestR1B8_XP331_CheckSeqReadOnlyThenAdvance`、`TestNodeSeqConcurrentFirstReports` PASS；`CheckSeq` / `advanceSeqTx`（`node_seq.go:92/:121`）在位；另见 §2.3（A4-009 单事务） |
| S-15 导入剥离悬空限速引用 | PASS | `TestR2B8ImportPartialRestoreStripsDanglingSpeedRefs`、`TestR2B8ImportFullRestoreKeepsSpeedRefs` PASS |
| S-16 恢复失败重发 | **本地不可判定·承继门证据** | 节点腿 `TestR2B10NodeRecoveryFailureResendsOnce` PASS、延迟腿 `TestR2B10LatencyRecoveryFailureResendsOnce` PASS；导入腿 `TestR2B10InboundRecoveryFailureResendsOnce` 本地 SKIP（与 R13/R14/R15/R16/R17 同形态）——**承 R10 发版门 #8 测试机实跑 PASS**；行级不降格、不标 PASS |
| S-17 部分失败只重发失败通道 | PASS | `TestR2B10PartialFailureResendsOnlyFailedChannel` PASS |
| S-18 install.sh 安装路径 | PASS | 安装路径服务未 active 时 `return 1`（R2X-019 注释在位；`install.sh` 本轮 diff 仅 R17-C 日志路由段，不涉安装路径；行号随 +8 漂移，见 S-07） |
| S-19 install.sh start 前 reset-failed | PASS | 两处 start 前 `reset-failed`（`install.sh:527/:544`，相对 R17 的 :519/:536 漂移 +8，系 R17-C 日志路由 hunk 所致，本体未动） |
| S-20 批量结果消费 | PASS（代码面；DOM 腿未验证） | 四消费点在位：分组分配 failures（`group.tsx:499-504`）、批量升级 results（`node.tsx:1160-1163`）、单节点 alreadyLatest（`node.tsx:1112-1119`）、maxConn warnings（`user.tsx:761-775`，逐条 toast） |
| S-21 创建幂等 | PASS | `TestR2B2_MonitorServiceCreateIdempotent`、`TestR2B2_FederationShareCreateIdempotent`、`TestR2B2_RemoteSourceSaveCreateIdempotent`、`TestR2B2_InboundRelayCreateIdempotent`、`TestR1B8_XP217_ForwardCreateIdempotencyKey` 全 PASS |
| S-22 转发换隧道刷新白名单与远端地址 | PASS | `TestR3B4ForwardUpdateTunnelSwitchRefreshesWhitelistAndRemoteAddr` PASS |
| S-23 零倍率全链配额零 | PASS | `TestR3B3ZeroRatioWholeChainQuotaZeroForwardRowRawPreserved`（handler）、`TestR3B3FlowUploadMetasZeroRatioPreserved`（repo）PASS |
| S-24 同步撤销库存禁用授权 | PASS | `TestR3B5_004_SyncRevokesStockDisabledGrants`、`TestR3B5_004_ZeroToZeroUpdateRevokesStockDisabledGrants` PASS |
| S-25 导入分组/占位授权收敛 | PASS | repo 5 facet 全 PASS：`TestR3B6ImportUserGroupsMemberReplacementRevokes`、`TestR3B6ImportTunnelGroupsMemberReplacementRevokes`、`TestR3B6ImportGroupPlaceholdersRepointPermissions`、`TestR3B6ImportRelayPlaceholderRepointsUserInbound`、`TestR3B6ImportDisabledUserGroupConvergesGrants` |
| S-26 隧道 warnings 消费 | PASS（代码面；DOM 腿未验证） | `tunnel.tsx:1028-1031` 消费 `data.warnings`（逐条 ⚠️ 后报成功） |
| S-27 SS2022 导入矩阵 | PASS | `TestR3B2SS2022ImportMatrix` 全子格 PASS |
| S-28 保留组播/链路本地拒绝 | PASS | kernel：`TestR3B1ValidateImportHostRejectsReservedMulticastLinkLocal`、`TestR3B1ValidateImportHostStillAllowsPrivateCGNAT`；netguard：`TestNetguardMatrix`、`TestNetguardClassifyTranslations`、`TestR3B1HardDenyOrLoopbackRejectsReservedMulticastLinkLocal`、`TestR3B1OtherPoliciesUnchangedForReservedMulticast`；handler：`TestR3B1ProbeRejectsReservedBeforeDial`、`TestR3B1ProbeRejectsMulticastAliasAfterResolution` 全 PASS |
| S-29 密码大小写变体导入 | PASS | `TestR4B1CipherCaseVariantImport` PASS |
| S-30 Reality 公钥校验 | PASS | `TestR4B1RealityBadPBKRejectedAllPaths` / `TestR4B1RealityGoodPBKAcceptedAllPaths` / `TestR4B1RealityShortIDPredicate` PASS |
| S-31 隧道替换迁移 | PASS | `TestR4B6TunnelReplaceMigrationFailureRepushesWhitelist`、`TestR4B6TunnelReplaceDeleteSourceFailureRepushesWhitelist` PASS |
| S-32 双分组禁用创建者在先/注册者在先 | PASS | `TestR4B4_005_DualGroupDisableCreatorFirst` / `RegistrarFirst` PASS |
| S-33 备份导入经 grant 台账执法 | PASS | `TestR3B6BackupImportRevokedForwardsCleaned` PASS + S-25 五 facet；差集/pair 形态不在本行覆盖内（N-36，归 R5X-015 登记口径） |
| S-34 并发双应用契约 | **本地不可判定·承继门证据** | `TestPostgresApplyInboundCounterSamplesConcurrentDoubleApplyContract` 与 `TestPostgresApplySubscriptionCounterSamplesConcurrentDoubleApplyContract` 本地 SKIP（文案 `set FLMX_POSTGRES_TEST_DSN to run`，`R18-L3-contract-pg.log`）——**承 R10 发版门 #7 真 PG 3/3 PASS + R11/R13/R15/R16/R17 抽核**；不标 PASS、不计失败，禁以 SQLite/seq 契约顶替 |
| S-35 联邦分享删除刷新白名单 | PASS | `TestR4S2FederationShareDeleteRefreshesWhitelist` PASS |
| S-36 枚举垃圾拒绝/合法值接受 | PASS | `TestR5B1EnumGarbageRejectedAllPaths`、`TestR5B1EnumLegalValuesAccepted` PASS |
| S-37 Reality 公钥校验（复核面） | PASS | 同 S-30（`TestR4B1RealityBadPBKRejectedAllPaths` PASS） |
| S-38 库存渲染跳过 | PASS | `TestR5B1StockRenderSkip`、`TestR6B1SSStockRenderSkip` PASS |
| S-39 六子面白名单补刷 | PASS | (a)(b) 动态：`TestR5B2TunnelDeletePartialFailureRepushesWhitelist`、`TestR5B2UserDeletePartialFailureRepushesWhitelist` PASS；(c) `tunnel_delete_resolution.go:392/:405` R5X-004(c) 注释在位（与 R17 同值）；(d) `kernel_relay.go:631` 注释 + `:637/:641` `refreshInboundWhitelist` 在位；(e) `mutations.go:511/:522` 注释 + `:515/:522/:527` 补刷在位；(f) `kernel.go:523` 注释 + `:531` `applyKernel` 补刷在位 |
| S-40 同名合并创建者标志 OR | PASS | `TestR5B3_005_SameNameMergeCreatorFlagOR`（4 子格）、`TestR5B3_005_SameNameMergeTunnelGroupSide` PASS |
| S-41 SS 计量伴生 | PASS（本地主断言；PG 子格承继） | handler：`TestSSMetering*` 14 项全 PASS；PG 子格 `TestPostgresCompanionPartialUniqueIndex` 本地 SKIP（无 DSN）——**本地不可判定·承继门证据**（承 R10 发版门 #7 同批真 PG PASS），行级不降格 |
| S-42 SS 密码每值校验 | PASS | `TestR6B1ValidSSCipherPerValue` PASS |
| S-43 SS2022 坏键 Clash 跳过 | PASS | `TestR6B1SS2022BadKeyClashSkip` PASS |
| S-44 链式目标守卫 | PASS | `TestR6B1ChainTargetGuards` PASS |
| S-45 导入权限 (1,0) 格 | PASS | `TestR6B2_004_PlaceholderGrantCreatorFlagOR` 四格全 PASS；`TestR3B6ImportPermissionsWithBackupGroupIDs`、`TestR3B6ImportPermissionsSkipsDisabledPair` PASS |
| S-46 误置授权仍清扫护盾链 | PASS | `TestR6B2_005_ShieldChainSweptDespiteMisplacedGrant` PASS |
| S-47 转发合计仅展示非计费 | PASS（代码面） | `dashboard.tsx:629-640` `calculateForwardTotalFlow` 注释逐字「此处合计仅为总流量展示，不是计费量」，消费点 `:1185` |
| S-48 导入状态枚举门 | PASS | `TestR5B5ImportStatusEnumGate` PASS |
| S-49 登录/订阅库状态谓词 | PASS | `handler.go:580`（login）与 `:951`（sub_store）`Status != 1` 在位（相对 R17 的 :565/:936 漂移 +15/+15，系 fix-r17-b 的 handler.go +17 行改动所致，谓词本体未动）；`TestR6B6LoginSubStoreStatusPredicate` PASS |
| S-50 计量开启入站改方法/安全层 | PASS（代码面；DOM 腿未验证） | `inbound/inbound-form-modal.tsx:1205` `isDisabled={meteringBlocked && !form.meteringEnabled}`（`meteringBlocked` 定义 `:441`）；空闲超时文案 `:280`「空闲超时须为 0–86400 秒的整数，留空或填 0 为默认 300 秒」 |
| S-51 ENC 标识三面 | PASS（代码面；DOM 腿未验证） | `inbound.tsx:674/:850/:1409` 三面在位（grid 卡片 / list / 移动端 sheet），徽标文案「IX 中转专用协议」逐面在位，与 R17 同值 |
| S-52 ENC 三门 | PASS | 行号与 R17 表逐字同值：`handler/kernel.go:2007/:2032/:2118`，函数定义 `:1997/:2031/:2083`，文案 `:2008/:2035/:2121` 均含「25.9.5 及以上」；`kernel_egress.go:48` 出站 prefer `versionBelowPadded(25,6,8)` 不变；`TestR8B1ENCVersionGateBoundary` PASS |
| S-53 SS 单面集 | PASS | `kernel/credentials.go:627` 六名 {`chacha20`、`aes-128-gcm-siv`、`aes-256-gcm-siv`、`chacha8-ietf-poly1305`、`aes-128-ccm`、`aes-256-ccm`}、`:644` 八值逐字在位；`TestR8B2SS*SingleFace` 5 项（AES128/256GCMSiv、Chacha8IETFPoly1305、AES128/256CCM）+ `TestR6B1SSChacha20SingleFace` + `TestR6B1ValidSSCipherPerValue` PASS |

**哨兵小结：51 PASS + 2 本地不可判定·承继门证据（S-16、S-34；S-41 的 PG 子格同口径承继，行级不降格），0 回退，0 新回退。**

## 3. 轮换域 A4 全量抽审（R14 全量轮口径）

### 3.1 变更面（相对 R14 全量轮基线 7244fbb）

A4 域文件在 `7244fbb..a0c18a0` 区间仅 3 个文件有改动（`git diff --stat` 实测）：

| 文件 | 改动 | A4 相关性 |
|---|---|---|
| `handler/control_plane.go` | +26/−11（R17-B） | AddService 回退走幂等守卫；见 §3.4 |
| `handler/mutations.go` | 2 行（fix-r14 F-08） | R14-A4-001 两行修复：`:4180`/`:4204` `err`→`statusErr`；见 §3.2 |
| `vite-frontend/src/pages/dashboard.tsx` | +16/−8（fix-r14 F-06） | 月重置日钳制（`daysInMonth`/`clampedDay`，与后端 `monthlyResetMoment` 同口径）；非 A4 核心面 |

其余 A4 核心文件（`flow_upload_batch.go`、`flow_policy.go`、`nftables_traffic.go`、`realm.go`、`user_quota.go`、`tunnel_delete_resolution.go`、`kernel/realm.go`、`runtime/nftables/renderer.go`、`repository_flow.go`、`repository_nft_counter.go`、`repository_mutations.go`、`forward.tsx`、`tunnel.tsx`）在 `7244fbb..a0c18a0` 区间 diff = 0 行；相对 R17 基线亦 diff = 0 行（A4 核心面本轮零改动）。故本轮 A4 抽审以「R14 全量结论复验 + R17-B 守卫邻接面 + 原立案携入复核」为主。

### 3.2 原立案复核（A4-forward.md，v1.3.2，A4-001…A4-010）

| 原编号 | 本轮结论 | 证据（a0c18a0 实测） |
|---|---|---|
| A4-001（P1，配额执法无 nft 分支） | ✅ 已闭合 | `user_quota.go:162/:245` `enforceUserQuotaIfNeeded` 内 `tunnelUsesNftables` 分支在位 |
| A4-002（P2，forwardCreate 丢 warnings） | ✅ 已闭合 | `mutations.go:3180` 起 `forwardCreate` 走 `syncForwardServicesWithWarnings`，有 warnings 时 `response.OK({"warnings": createWarnings})`（`:163-170`）；前端 `forward.tsx:2476-2477` 共用消费点在位 |
| A4-003（P2，switchTunnelEngine 吞错恒成功） | ✅ 已闭合 | `realm.go:317` `switchTunnelEngine` 返回 error；调用点 `mutations.go:2149` 判 err（`if err := h.switchTunnelEngine(...); err != nil`） |
| A4-004（P2，realm 策略静默改写） | ✅ 已闭合（强于原建议） | `realm.go:68-94` `validateRealmForwardStrategy`：realm 多目标且策略 ∉ {round, hash} 时**明确拒绝**（非仅告警）；调用点 `mutations.go:3306`（create）、`:3430`（update）、`:4423`（批量换隧道）；专测 `realm_strategy_a4004_test.go` 5 断言在位 |
| A4-005（P2，nft 渲染忽略 BindIP） | ✅ 已闭合 | `runtime/nftables/renderer.go:30-34` prerouting 按 BindIP 加 `daddr` 匹配、`:62-69` 计数面同口径收紧、`:102-104` BindIP 进 `RuleHash` |
| A4-006（P2，换隧道旧 nft 节点规则无人清理） | ✅ 已闭合 | `mutations.go` `deleteForwardServicesOnNodeBatch` 内 A4-006 分支：旧隧道 nft 模式时走 `reconcileNftablesNodeByRequest(nodeID)`（与 S-06 同款声明式收敛） |
| A4-007（P2，隧道页「gost 不计费」文案与文档/代码矛盾） | ✅ 已闭合 | `tunnel.tsx` 全仓 grep「不计入用户配额/不计费」0 命中；现行文案 `:2465-2466`：「realm……计费与面板转发一致，按隧道计费模式扣用户配额」「面板转发（gost）……流量按隧道计费模式计入用户配额（与 realm 同口径）」，与 `doc/guide/forward.md` 同口径 |
| A4-008（P3，gost 删隧道逐条清理半完成披露） | ❌ **仍在，携入立案 R18-X-01** | 见 §3.3 |
| A4-009（疑似 P3，gost 计费两段非原子 + seq 先行） | ✅ 结构性消除 | `handler.go:1357` 注释钉死新语义「seq 先只读校验、入账同 tx 推进」；`flow_upload_batch.go:219` 单调用 `repo.ApplyFlowUploadAccounting(deltas, quotaUsage, now, seqAdvance)`；`repository_flow.go:592` 起单 `Transaction` 内含 `advanceSeqTx`（`:603`）；专测 `TestR1B8_XP331_ApplyFlowUploadAccountingAtomicWithSeq` 在位 |
| A4-010（疑似 P3，引擎切换 realm 漏计窗口） | ✅ 结构性消除 | `realm.go:339/:373` 两向切换均 `settleRealmCountersAtSwitch`（`:486` 起，切换前主动拉内核状态钉基线）；离线节点打持久标记 `pending_realm_settle:<nodeID>`（`:384-390`），重连先结算后重下发；`TestRealmEngineSwitchConverges` 本轮复跑 PASS |

### 3.3 新立案

#### R18-X-01 · [P3 · 产品] 删隧道 gost 分支逐条清理仍半完成：中途失败已删转发数不披露（A4-008 携入）

- 文件：`go-backend/internal/http/handler/tunnel_delete_resolution.go:766-780`（`cleanupForwardsDataPlane` gost 分支）
- 代码（本轮基线树实读）：

```go
for i := range forwards {
    fwd := forwards[i]
    if err := h.controlForwardServices(&fwd, "DeleteService", true); err != nil {
        if offlineTolerant && isNodeUnreachableError(err) { /* X-P1-1：删行继续 */ continue }
        return fmt.Errorf("转发 %s 数据面清理失败，%s未删除: %w", fwd.Name, scope, err)
    }
    if err := h.deleteForwardByID(fwd.ID); err != nil { return err }
}
```

- 现象：隧道挂 ≥2 条 gost 转发，删除时靠后一条数据面清理硬失败（非离线类），前序转发已数据面 + DB 双删、不可回滚，隧道本体保留；回执仅「转发 X 数据面清理失败，隧道未删除」，不披露已删条数/剩余条数，与批量删除接口普遍提供的逐条成败明细（`batchOperationResult`）不一致。nft 分支（同函数 :743-765）因「先全标 pending → 一次 reconcile → 再删库」无此问题，属同函数内两种原子性。
- 携入史：原 A4-008（P3，`A4-forward.md`，v1.3.2）；`X2-datapath.md:150` 仍登记为「△ 半完成披露问题 = A4-008（P3），方向 fail-closed，无孤儿监听」；全仓 grep：A4-008 仅见于 `A4-forward.md` / `X2-datapath.md` / `X4-docs-final.md` / `handoff/A4.md`，**无任何一轮 ledger / FIX-REPORT / FINAL 摘要将其关闭**（R14-A4 报告亦未复核此条）。本轮按 R14 口径重审确认代码面逐字仍在，故以新号 R18-X-01 重新立案，不继承旧号、不溯改旧账。
- 定级理由：P3——失败条件需节点异常（硬失败），且保留隧道本体避免了孤儿监听这一更坏结果（fail-closed 方向正确），问题仅在完成度披露；与原 A4-008 定级一致。
- 修复方向（不执行，仅登记）：报错文案附已删除条数/剩余条数，或把 gost 分支改为两段式（先全量数据面清理、再统一删库），后者与 nft 分支同形但会延长持锁外窗口，需主控权衡。

### 3.4 R17-B 守卫邻接面（A4 直接相关的新代码）

- 接线：`control_plane.go:451` UpdateService 成功即 `noteAddServiceConfirmed`；三处回退（`:465` update-fallback、`:518` default-bind-fallback、`:558` self-occupied-rebind）全走 `guardedAddService(..., freshAbsence=true)`；`deleteForwardServiceBasesOnNode`（`:611`）DeleteService 成功即 `invalidateAddServiceNames`；节点离线钩子 `handler.go:221` `SetNodeOfflineHook` 内 `invalidateAddServiceNode`。
- 核查结论：守卫语义与转发数据面一致——freshAbsence=true 时先作废确认再决定，绝不跳过；确认集仅由 agent 成功响应建立；离线即整节点作废，下次同步重建。**无新缺陷**。两点观察（见 §5）：
  - O-L3-1：`DeduplicateServices`（`go-gost/x/config/dedupe.go:36`）first-wins——启动去重仅处理毒化文件；若同一服务名两副本内容不同，先副本胜出、后副本丢弃。但该路径只在启动时跑（`program.go:326`），且面板下次 UpdateService（删后重建）可纠正；无 differing-duplicate 产生的证据，不立案。
  - O-L3-2：`guardedAddService` 的 skip-confirmed 分支生产不可达——全部三处生产调用点 freshAbsence 恒为 true；该分支现为防御性死代码（仅测试触及）。不立案；若未来新增 freshAbsence=false 调用点，须重审确认集新鲜性。

### 3.5 计费口径对照（现行树复核，与 R14-A4 §4 同结论，无漂移）

| 数据面 | 入（InFlow） | 出（OutFlow） | 单向（Tunnel.Flow=1） | 倍率 | 落账 |
|---|---|---|---|---|---|
| gost（`flow_upload_batch.go`） | agent 线 `D` | 线 `U` | 仅 scaledIn | scaled=D×ratio / U×ratio | forward 行 raw、user/UT 行与 quota 落 scaled，同事务 `ApplyFlowUploadAccounting`（seq 同 tx 推进） |
| nftables（`nftables_traffic.go`） | `CounterDirectionToTarget`（BytesIn） | FromTarget（BytesOut） | quota=scaledIn、scaledOut 恒 0 | `scaleNftTrafficBytes`；ratio=0 产零计费并 warn | 基线推进 + 落账同事务 `ApplyNftTrafficAccounting`；per-node 入账锁串行化 |
| realm（`handler/realm.go` `applyRealmCounters` → 共用 `accountForwardCounterSamples`） | agent realm 计数器 Dir="in" → ToTarget | Dir="out" → FromTarget | 同 nft（共用 meta 的 TunnelFlow 选择器） | 同 meta ratio | 同 nft 共用入账段 |

- 三面方向一致性常驻 `TestFlowDirectionParityAcrossEngines` 本轮复跑 **PASS**；`TestRealmForwardRenderAndTraffic` **PASS**；`TestRealmEngineSwitchConverges` **PASS**。
- 前端消费对账（feature-chain）：`ListForwards` 返回 `inFlow`/`outFlow`（forward 行 raw 口径）；`forward.tsx` 以 ↑/↓ 展示（入=↑、出=↓，与 `forward.md:50` 一致）；dashboard `calculateForwardTotalFlow`（`dashboard.tsx:629-640`）返回 raw 入+出合计、注释逐字声明「仅为总流量展示，不是计费量」（S-47 口径）；零态：`forward.tsx:939/1188/1233/1978/2283/4121` 在流量为 0 时禁用重置/清空类操作、有流量才渲染 ↑/↓ 芯片——零态不画空区，路径在位；DOM 腿无 live browser，未验证。
- limit=0 语义（R9X-001 修复对象，按对象本身复验）：执法面与通知面 `flowLimitBytes`（`flow_policy.go:19-31`）直接调用点现行树仍为 **7 处**（E1 `:264-269`、E2 `:309-312`、E3 `:335-339`、E4 `:353-356`、N1 `notify_reports.go:95`、T1 `handler.go:974/:996`、写入面 `parseTrafficLimit`），0=不限、等号拦截，无第三种读法；常驻 `TestFlowLimitZeroUserNotPausedR9X001` 等 PASS。
- pending-delete 全生命周期：`MarkForwardPendingDelete` 幂等 + 退避 1m/5m/30m/2h、24h 上限转人工（`next_retry_at=0` 哨兵）+ `ListDueNftPendingDeletes` 排除 + `ListForwards` 隐藏，代码面与 R14 §6 同形，未变；常驻探针复跑 PASS（handler 全量日志内）。

### 3.6 R14-A4-001 关闭复核（按对象本身，N-35）

| 编号 | 对象 | 本轮复验 | 结论 |
|---|---|---|---|
| R14-A4-001（P3）批量暂停/恢复 DB 写失败回执丢真因 | `mutations.go:4180`（gost 分支）、`:4204`（nft 分支）`appendBatchFailure(..., statusErr)` | 两处均已由 `err` 改为 `statusErr`（fix-r14 F-08，`e215fd7`，在 `7244fbb..a0c18a0` 区间 diff 中逐字可见）；同函数其余 `appendBatchFailure(..., err)` 调用点（`:4044`/`:4051`/`:4055`/`:4137` 等）仍为当次真实错误，未变。诚实注记：R14 的行为复现探针 `TestR14A4ProbeBatchResumeDbWriteFailureReason` 为跑后即删的一次性探针文件（R14 报告 §2 明言），不在本轮树内，**本轮未重跑该探针**；关闭维持的依据为代码面两行修复 + R14 探针日志（`R14-A4-probe.log`）仍为行为证据 | ✅ 关闭维持（代码面；行为探针未重跑，见注记） |

## 4. R13 关闭 7 项与 FINAL-P2-1 七段谓词维持关闭复核（逐项点名）

| 编号 | 对象 | 本轮复验 | 结论 |
|---|---|---|---|
| R13X-001（P2）备份导入后在线节点转发数据面重推 | `backupImportNeedsForwardRepush` / `repushForwardRuntimeAfterImport`（`backup_import_repush.go`）+ `ImportResult.repushNodeIds/repushFailedNodeIds` | 常驻 `TestR13BRBackupImportForwardsRepushesOnlineNode` / `TestR13BRRepushSkipsWhenNoLiveAgent` / `TestR13BRRepushTrigger` / `TestR13BRBackupImportNodesResyncsRelayRemoteAddr` 全 PASS；调用点在撤销清理之后，使重推下发的是导入终态 | ✅ 维持关闭 |
| R13X-002（P3）`tunnelCreate` 补 `type`/`status` 守卫 | `tunnelCreate` 四键守卫（`mutations.go:1261-1280`，`type/flow/status` 数值门 + `trafficRatio` 浮点门，与 `tunnelUpdate` 同形） | 常驻 `TestR13T2aTunnelCreateRejectsUnparsableTypeAndStatus`（4 子格）+ `TestR13T2aTunnelCreateAcceptsLegalTypeAndStatus` PASS | ✅ 维持关闭 |
| R13X-003（P3）编辑表单省略域外 ratio/flow | `tunnel.tsx:1019-1020`（`omitFlow`/`omitRatio`，`delete payload.flow` / `delete payload.trafficRatio` 仅编辑态） | 代码面在位；DOM 实点未验证，与 R13 门 #3 同口径，归真机/浏览器 | ✅ 维持关闭（代码面；DOM 腿未验证） |
| R13X-004（P3）inbounds 段子段无导入计数 | `ImportResult` 七子段计数（`model.go:973-976` `InboundKernelsImported`/`InboundRelaysImported` 等）+ 赋值点 `repository.go:3150-3153` 取自真实 counts | 常驻 `TestR13BRInboundSubsectionCounts`（repo 包）PASS | ✅ 维持关闭 |
| R13X-005（P3）未知类型字符串静默忽略 | `validBackupTypes` 16 值（`repository.go:2288-2297`）+ `validateBackupTypes` 点名未知值 | 16 值与前端 `BACKUP_TYPE_OPTIONS` 逐值集合相等（R17 已脚本重算，差集 ∅；本轮 16 值逐字在位，未变）；`TestR13BRUnknownBackupTypeRejected`（repo 包）PASS | ✅ 维持关闭 |
| R13X-006（P3）导入型入站 Protocol 与 Extra 失配 | `repository_inbound_backup.go:537-551`（导入型入站 kernel_id≤0 时 `kernel.ProxyProtocol(p)` 派生 Protocol） | 常驻测试 PASS（repo 全量日志内） | ✅ 维持关闭 |
| R13X-007（P3）隧道 Flow/TrafficRatio 备份恢复面无保存面门 | `validateImportedTunnelFlow`（`{1,2}`）/ `validateImportedTunnelTrafficRatio`（`[0,100]`，NaN/Inf 拒）（`repository.go:3620-3642`），门在预删前、跳过逐行点名 | 常驻 `TestR13T2cImportTunnelsSkipsOutOfDomainFlowAndRatio`（repo 包）PASS | ✅ 维持关闭 |
| FINAL-P2-1（P2）备份导入重推谓词漏限速相关段 | 七段谓词 `backupImportNeedsForwardRepush`（`backup_import_repush.go:38-47`）：Forwards / Tunnels / Nodes / Inbounds / InboundRelays / **SpeedLimitsImported** / **UserTunnelsImported**，七段逐字在位 | 新增断言 `backup_final_p2_1_test.go` 4 项全 PASS：`TestFinalP21RepushTrigger`（谓词表：speedLimits-only → true、userTunnels-only → true；kernels-only / subscriptions-only → false；forwards 锚点 → true；nil → false）、`TestFinalP21SpeedLimitsOnlyImportRepushesLimiter`、`TestFinalP21UserTunnelsOnlyImportRepushesLimiter`、`TestFinalP21SubscriptionOrKernelOnlyImportDoesNotRepush` | ✅ 维持关闭 |

## 5. 观察项（不立案）

- **O-L3-1**：`DeduplicateServices` first-wins（`go-gost/x/config/dedupe.go:36`）——启动去重仅处理毒化文件；若同一服务名两副本内容不同，先副本胜出。路径只在启动时跑（`program.go:326`），面板下次 UpdateService 可纠正；无 differing-duplicate 产生的证据，不立案。
- **O-L3-2**：`guardedAddService` 的 skip-confirmed 分支生产不可达——全部三处生产调用点 freshAbsence 恒为 true（`control_plane.go:465/:518/:558`）；该分支现为防御性死代码（仅测试触及）。不立案；若未来新增 freshAbsence=false 调用点，须重审确认集新鲜性。

## 6. 证据总账

| 文件 | 字节数 | 内容 |
|---|---|---|
| `R18-00-sentinel.md` | 18,191 | 哨兵表 53 行（承 R17，开工前落盘） |
| `R18-00-baseline.md` | 2,550 | 基线 main @ a0c18a0 |
| `R18-L3-handler-full.log` | 938,251 | handler 全量 `-v` 原始日志（重跑，exit 0） |
| `R18-L3-handler-first-run-diskfull.log` | 831,735 | handler 首跑原始日志（/tmp 撑满污染，86 FAIL，exit 1；审计轨迹保留） |
| `R18-L3-kernel-full.log` | 48,685 | kernel 全量 `-v` 原始日志（首跑，exit 0） |
| `R18-L3-repo-full.log` | 282,902 | repo 全量 `-v` 原始日志（重跑，exit 0） |
| `R18-L3-repo-partial-reboot.log` | 278,360 | repo 首跑部分日志（VM 重启中断前 229 PASS / 0 FAIL） |
| `R18-L3-netguard.log` | 1,024 | netguard 原始日志 |
| `R18-L3-contract-pg.log` | 220 | PG 契约靶向（3 SKIP）原始日志 |
| `R18-L3-gost.log` | 见落盘 | go-gost root（lifecycle ok）原始日志 |
| `R18-L3-gost-x.log` | 见落盘 | go-gost/x 模块 27 包 ok 原始日志 |
| 本文件 | 见落盘自验 | R18-L3 合成报告 |

— R18-L3-sentinel-a4 END · 哨兵 53 行（51 PASS / 2 承继 / 0 回退）· A4 新立案 1（R18-X-01，P3）· R13 关闭 7 项与 FINAL-P2-1 七段谓词维持关闭 · END
