# R19-L3 报告：哨兵回归（S-01…S-53）+ 轮换域 A6 全量抽审

- **轮换域为 A6**（订阅核心与订阅源，catalog §2 B 域 + C 域订阅源更新 C-12…C-16；域定义见 `R14-A6-report.md` §头，R15=A5、R16=A2、R17=A3、R18=A4，本轮轮换 A6）。
- **任务**：R19 再审计 L3 路——哨兵回归 + 轮换域 A6 全量抽审；只读审计，不改产品代码、不打 tag、不发版。
- **基线**：main @ d66042c（`d66042cc0b7ac011ddfad09f3f2709b90dafda2e`，2026-10-25 02:44:56 +0000，「docs(r18): archive R18 audit records + integration report (FIX-INT-REPORT)」；origin/main 实测一致待核不在本路口径内，读数树 pin 死 d66042c）。哨兵表 `R19-00-sentinel.md` 与基线 `R19-00-baseline.md` 开工前落盘（N-37 细则①③）。
- **读数树**：只读 worktree `~/workspace/.r19-tree` @ d66042c（detached HEAD，`git status` 仅 `?? audit-out-132/r19/` 未跟踪审计文件，全程 pristine）；测试在 `git archive d66042c` 快照 `~/workspace/.r19-tmp/r19snap` 上跑，探针文件跑后即删（N-37 细则④）；共享工作树（`~/workspace/flvx-max`）未用作读数树。
- **结论口径**：PASS = 代码在位 + 对应测试在基线快照实跑通过；「本地不可判定·承继门证据」= 本地无条件复跑（无 DSN / 无测试机），承继已落盘的门证据，**不标 PASS、不计失败**；DOM 腿（无 live browser）与历轮同口径记「代码面在位，DOM 腿未验证」。

## 0. 总账

| 项 | 结论 |
|---|---|
| 哨兵 53 行 | **51 PASS + 2 本地不可判定·承继门证据（S-16、S-34；S-41 的 PG 子格同口径承继，行级不降格），0 回退** |
| A6 新立案 | **0 条**（L3-CAND-01… 无；见 §3） |
| A6 观察项 | **1 条维持**：A6-观察-01（SIP023 多段残余按节点来源拆分登记，承 R14，不占立案号） |
| R13 关闭 7 项（R13X-001…007） | **7/7 维持关闭**（见 §4） |
| FINAL-P2-1 七段谓词 | **维持关闭**（谓词七段在位，行为测试 4/4 PASS，见 §4） |
| R18 修复在位复核 | R18-F01（删隧道中途失败披露已删条数）9 项新测全 PASS，披露码 `:747/:753` 在位；F02/F03/F04 未触 A6 文件（`a0c18a0..d66042c` A6 文件差集 = 0，见 §3.1） |
| 探针事故（已处置，不属产品失败） | kernel 全量首跑 4 FAIL 均为本路自写 A6 探针的写法错误（链式双边口径、脚本顶层 return、base64 体未解码、ss URI 切分），已逐项修正并在独立日志重跑 6/6 + 4/4 全绿；常驻测试 0 FAIL（见 §1 方法注记） |

## 1. 全量套件计数（基线快照实跑，日志落盘 r19/）

| 套件 | 计数 | 日志 |
|---|---|---|
| handler 全量 `-v` | **1325 PASS（含子测；顶层 997）/ 0 FAIL / 4 SKIP**，`ok 617.514s` | `R19-L3-handler-full.log` |
| kernel 全量 `-v` | **433 PASS（含子测；顶层 266）/ 4 FAIL（全为本路探针写法错误，见注记）/ 0 SKIP**，首跑 exit 1 | `R19-L3-kernel-full.log` |
| repo 全量 `-v` | **267 PASS（顶层 234）/ 0 FAIL / 0 SKIP**，`ok 180.567s` | `R19-L3-repo-full.log` |
| netguard | **8 PASS / 0 FAIL / 0 SKIP** | `R19-L3-netguard.log` |
| PG 契约（全包） | **243 PASS（含子测；顶层 140）/ 0 FAIL / 9 SKIP**（无 DSN，见 S-34/S-41），`ok 114.605s` | `R19-L3-contract-pg.log` |
| go-gost | **root lifecycle ok + x 模块 27 ok / 0 FAIL** | `R19-L3-gost.log` + `R19-L3-gost-x.log` |
| A6 探针重跑 kernel | **6/6 PASS**，`ok 1.014s` | `R19-L3-a6-probe-kernel.log` |
| A6 探针重跑 handler | **4/4 PASS**，`ok 0.309s` | `R19-L3-a6-probe-handler.log` |

- 计数口径：`--- PASS` 含缩进子测试行（与 R16/R17/R18-L3 报告口径一致；括号内为顶层 `^--- PASS` 数，供交叉核对）。
- handler 算术：1325 = R18 的 1316 + 9，顶层 997 = R18 的 988 + 9；+9 恰为 R18-F01 新增测试文件 `r18_f01_tunnel_delete_disclosure_test.go` 的 9 个顶层 Test func（`git diff a0c18a0..d66042c` 实测新增 Test func 9 个），9 项在日志内逐名 PASS（见 §3.1）。其余 handler 测试 0 增 0 减。
- kernel 算术：433 = R18 的 431 常驻 + 本路 6 探针中首跑通过的 2 个；顶层 266 = R18 的 264 + 2。4 FAIL 全为本路探针（`TestR19L3A6ChainNamesBothMatch` / `ScriptSandbox` / `RenderMatrix` / `SIP023FourFaces`），根因逐项为探针写法错误、非产品行为：①链式探针未给纯中转节点致 `chainGroups` 双边不全按 B-12 口径清空角色；②脚本用了顶层 `return`（goja 要求 `function operator(nodes)`）；③base64 体未解码就查明文名；④ss URI 按 SIP002 base64 userinfo 切分，实为明文 URL 转义形。修正后独立重跑 6/6 PASS（`R19-L3-a6-probe-kernel.log`），常驻 431 与 R18 逐数一致。
- handler 4 SKIP 与 R18 同名同形态：`TestInboundProbeEmptyIDsDoesNotClearDown`、`TestR2B10InboundRecoveryFailureResendsOnce`（S-16 导入腿）、`TestR5B9ImportedNodeDownScheduledRoundAlerts`、`TestR5B9ImportedNodeRecoveryScheduledRoundAlerts`。
- PG 契约本轮跑全包（R18 为靶向 3 SKIP）：9 SKIP 均为无 DSN/环境门控（含 S-34 两契约 + S-41 `TestPostgresCompanionPartialUniqueIndex` + `TestPostgresLegacySeqContract` / `TestPostgresSeqConcurrentDoubleApplyContract` / `TestPostgresNodeCreateRepairsMissingIDDefaultContract` / `TestExternalProbeContract` / `TestNotifyImportedNodeDownContract` / `TestIPPreferenceCoalesceNullSafety`），其余 243 PASS。
- 方法注记：套件顺序执行（handler → kernel → repo → netguard → contract-pg → gost），`GOTMPDIR`/`TMPDIR` 改到持久盘（`~/.r19-tmp/gotmp`、`~/.r19-tmp/tmp`），吸取 R18 首跑 /tmp 512M 撑满的教训；本轮无磁盘满、无 VM 重启中断。handler `-timeout 30m` 单跑。探针文件带域标 `r19_l3_a6_probe_test.go`，跑后即删，快照内 grep 残留 0 命中。
- 钉死值复核：路由 **202**（`handler.go` 201 处 `mux.HandleFunc` + `router.go:13` 1 处 `mux.Handle` `/system-info`，与 R18 钉死值一致）；数据表 **49**（48 处 `TableName()` 在 `model/*.go` + `NodeSeq.TableName()` 在 `repo/node_seq.go`，与 R18 一致）。

## 2. 哨兵逐行表（S-01…S-53）

（逐行证据见下表；行号均为本轮基线树 d66042c 实测值，相对 R18 有漂移处逐行注记。）

| 哨兵 | 对象 | 结论 | 证据（本轮基线树实测） |
|---|---|---|---|
| S-01 隧道删除 | PASS | `TestTunnelDeleteToleratesRemoteFailureAndDeletesTunnel`、`TestR1B3TunnelDeleteMigrationRecomputesRelayRemoteAddr`、`TestR1B3TunnelDeleteGostNodeOfflineSucceeds`、`TestR1B3TunnelDeleteNftNodeOfflineGoesPending`、`TestR1B3TunnelDeleteOnlineRealFailureAborts` 5/5 PASS；`tunnelDelete` + `tunnel_delete_resolution.go` 在位 |
| S-02 分组禁用撤销授权 | PASS | `TestB2_XP12_GroupTunnelDeleteRevokesGroupGrants`、`TestR2B7_013_DisableUserGroupRevokesGrants` / `DisableTunnelGroupRevokesGrants` 3/3 PASS |
| S-03 监控写守卫 | PASS | 四写入口 `monitoring.go:442/:582/:703/:731`（create/update/delete/run）均在位，与 R18 同值；`ensureMonitoringAccess` 在 `monitoring.go` 内 10 处调用在位；`TestR1MonitorWriteRequiresAdmin` PASS |
| S-04 导入 host 校验 | PASS | kernel：`TestR1B5ValidateImportHostRejectsNonCanonical`、`TestR2B1ValidateImportHostLoopbackAliases` / `HardDenyLiterals` / `PrivateLiteralsStillAllowed`、`TestN01ValidateImportHostLinkLocalV6Rejected` / `ULAStillAllowed` 全 PASS |
| S-05 导入跳过缺失用户 | PASS | repo：`TestR1B5ImportUserSubscriptionsSkipsMissingUser`、`TestBackupImportUserSubscriptionsSkipsUnresolvable` PASS |
| S-06 通知失败次轮重发 | PASS | `TestR1KernelErrorNotifyFailureResendsNextRound`、`TestR1RouteChangeNotifyFailureResendsNextRound` PASS |
| S-07 install.sh 更新路径 | PASS | `install.sh` 本轮 diff（`a0c18a0..d66042c`）= 0 行；哨兵段本体未动、行号与 R18 同值：`flux_agent_health_ok` :597、双采样 `sleep 3` 后复检 :957-958、`.bak` 仅健康通过后删除 :963（失败分支先 `return 1`，:958-970） |
| S-08 联邦端口占用 | PASS | `TestFederationTunnelCreateRejectsOccupiedPort`、`TestFederationRuntimeReservePortRejectsWhenShareFlowExceeded` PASS；锁内复检 `validateForwardPortAvailabilityTx` 在位 |
| S-09 订阅重置轮换全凭据 | PASS | `TestR1B7MainSubscriptionResetStillRotatesFullCredentials`、`TestR1B7LegacySubInfoResetRotatesFullCredentials`、`TestSubInfoResetStaysAdminOnlyAndRotates` PASS |
| S-10 单远端地址非公网拒绝 | PASS | `TestR2B1CheckSingleRemoteAddrNonPublicDenied` PASS；`IsSafeRemoteAddr`（`security_utils.go:26`）在位 |
| S-11 Snell 版本域 | PASS | kernel：`TestR2B5SnellFinishProxyVersionDomain` / `TestR2B5SnellRenderersVersionDomain` / `TestR2B5SnellChainTargetRenderWith` / `TestR2B5SnellV6ModeRoundTrip` 4/4 PASS |
| S-12 导入 host 校验（内核面） | PASS | 同 S-04 测试族（kernel 日志内全 PASS） |
| S-13 白名单刷新 | PASS | R2B9 白名单刷新 6 项 PASS：`TunnelDeleteWithForwardsDeleteRefreshesWhitelist`、`TunnelBatchDeleteWithForwardsDeleteRefreshesWhitelist`、`TunnelDeleteWithForwardsReplaceRefreshesWhitelist`、`ForwardBatchChangeTunnelRefreshesWhitelist`、`UserDeleteRefreshesWhitelist`、`NodeUpdateAddressRefreshesWhitelist` |
| S-14 seq 只读后推进 | PASS | `TestR1B8_XP331_CheckSeqReadOnlyThenAdvance`、`TestNodeSeqConcurrentFirstReports` PASS；`CheckSeq`（`node_seq.go:92`）/ `advanceSeqTx`（`node_seq.go:121`）在位，与 R18 同值 |
| S-15 导入剥离悬空限速引用 | PASS | `TestR2B8ImportPartialRestoreStripsDanglingSpeedRefs`、`TestR2B8ImportFullRestoreKeepsSpeedRefs` PASS |
| S-16 恢复失败重发 | **本地不可判定·承继门证据** | 节点腿 `TestR2B10NodeRecoveryFailureResendsOnce` PASS、延迟腿 `TestR2B10LatencyRecoveryFailureResendsOnce` PASS；导入腿 `TestR2B10InboundRecoveryFailureResendsOnce` 本地 SKIP（与 R13/R14/R15/R16/R17/R18 同形态）——**承 R10 发版门 #8 测试机实跑 PASS**；行级不降格、不标 PASS |
| S-17 部分失败只重发失败通道 | PASS | `TestR2B10PartialFailureResendsOnlyFailedChannel` PASS |
| S-18 install.sh 安装路径 | PASS | 安装路径服务未 active 时 `return 1`（R2X-019 注释在位；`install.sh` 本轮 diff = 0 行，不涉安装路径） |
| S-19 install.sh start 前 reset-failed | PASS | 两处 start 前 `reset-failed`（`install.sh:527/:544`，与 R18 同值，本体未动） |
| S-20 批量结果消费 | PASS（代码面；DOM 腿未验证） | 四消费点在位：分组分配 failures（`group.tsx:499-504`）、批量升级 results（`node.tsx:1160-1163`）、单节点 alreadyLatest（`node.tsx:1112-1119`）、maxConn warnings（`user.tsx:761-775`，逐条 toast），与 R18 同值 |
| S-21 创建幂等 | PASS | `TestR2B2_MonitorServiceCreateIdempotent`、`TestR2B2_FederationShareCreateIdempotent`、`TestR2B2_RemoteSourceSaveCreateIdempotent`、`TestR2B2_InboundRelayCreateIdempotent`、`TestR1B8_XP217_ForwardCreateIdempotencyKey` 全 PASS |
| S-22 转发换隧道刷新白名单与远端地址 | PASS | `TestR3B4ForwardUpdateTunnelSwitchRefreshesWhitelistAndRemoteAddr` PASS |
| S-23 零倍率全链配额零 | PASS | `TestR3B3ZeroRatioWholeChainQuotaZeroForwardRowRawPreserved`（handler）、`TestR3B3FlowUploadMetasZeroRatioPreserved`（repo）PASS |
| S-24 同步撤销库存禁用授权 | PASS | `TestR3B5_004_SyncRevokesStockDisabledGrants`、`TestR3B5_004_ZeroToZeroUpdateRevokesStockDisabledGrants` PASS |
| S-25 导入分组/占位授权收敛 | PASS | repo 5 facet 全 PASS：`TestR3B6ImportUserGroupsMemberReplacementRevokes`、`TestR3B6ImportTunnelGroupsMemberReplacementRevokes`、`TestR3B6ImportGroupPlaceholdersRepointPermissions`、`TestR3B6ImportRelayPlaceholderRepointsUserInbound`、`TestR3B6ImportDisabledUserGroupConvergesGrants` |
| S-26 隧道 warnings 消费 | PASS（代码面；DOM 腿未验证） | `tunnel.tsx:1028-1031` 消费 `data.warnings`（逐条 ⚠️ 后报成功），与 R18 同值 |
| S-27 SS2022 导入矩阵 | PASS | `TestR3B2SS2022ImportMatrix` 全子格 PASS |
| S-28 保留组播/链路本地拒绝 | PASS | kernel：`TestR3B1ValidateImportHostRejectsReservedMulticastLinkLocal`、`TestR3B1ValidateImportHostStillAllowsPrivateCGNAT`；netguard：`TestNetguardMatrix`、`TestNetguardClassifyTranslations`、`TestR3B1HardDenyOrLoopbackRejectsReservedMulticastLinkLocal`、`TestR3B1OtherPoliciesUnchangedForReservedMulticast`；handler：`TestR3B1ProbeRejectsReservedBeforeDial`、`TestR3B1ProbeRejectsMulticastAliasAfterResolution` 全 PASS |
| S-29 密码大小写变体导入 | PASS | `TestR4B1CipherCaseVariantImport` PASS |
| S-30 Reality 公钥校验 | PASS | `TestR4B1RealityBadPBKRejectedAllPaths` / `TestR4B1RealityGoodPBKAcceptedAllPaths` / `TestR4B1RealityShortIDPredicate` PASS |
| S-31 隧道替换迁移 | PASS | `TestR4B6TunnelReplaceMigrationFailureRepushesWhitelist`、`TestR4B6TunnelReplaceDeleteSourceFailureRepushesWhitelist` PASS |
| S-32 双分组禁用创建者在先/注册者在先 | PASS | `TestR4B4_005_DualGroupDisableCreatorFirst` / `RegistrarFirst` PASS |
| S-33 备份导入经 grant 台账执法 | PASS | `TestR3B6BackupImportRevokedForwardsCleaned` PASS + S-25 五 facet；差集/pair 形态不在本行覆盖内（N-36，归 R5X-015 登记口径） |
| S-34 并发双应用契约 | **本地不可判定·承继门证据** | `TestPostgresApplyInboundCounterSamplesConcurrentDoubleApplyContract` 与 `TestPostgresApplySubscriptionCounterSamplesConcurrentDoubleApplyContract` 本地 SKIP（文案 `set FLMX_POSTGRES_TEST_DSN to run`，`R19-L3-contract-pg.log`）——**承 R10 发版门 #7 真 PG 3/3 PASS + R11/R13/R15/R16/R17/R18 抽核**；不标 PASS、不计失败，禁以 SQLite/seq 契约顶替 |
| S-35 联邦分享删除刷新白名单 | PASS | `TestR4S2FederationShareDeleteRefreshesWhitelist` PASS |
| S-36 枚举垃圾拒绝/合法值接受 | PASS | `TestR5B1EnumGarbageRejectedAllPaths`、`TestR5B1EnumLegalValuesAccepted` PASS |
| S-37 Reality 公钥校验（复核面） | PASS | 同 S-30（`TestR4B1RealityBadPBKRejectedAllPaths` PASS） |
| S-38 库存渲染跳过 | PASS | `TestR5B1StockRenderSkip`、`TestR6B1SSStockRenderSkip` PASS |
| S-39 六子面白名单补刷 | PASS | (a)(b) 动态：`TestR5B2TunnelDeletePartialFailureRepushesWhitelist`、`TestR5B2UserDeletePartialFailureRepushesWhitelist` PASS；(c) `tunnel_delete_resolution.go:392/:405` R5X-004(c) 注释在位；(d) `kernel_relay.go:631` 注释 + `:637/:641` `refreshInboundWhitelist` 在位；(e) `mutations.go:515/:523/:527` 补刷在位（R18 的 `:522` 漂移 +1，注释与调用本体未动）；(f) `kernel.go:523` 注释 + `:531` `applyKernel` 补刷在位 |
| S-40 同名合并创建者标志 OR | PASS | `TestR5B3_005_SameNameMergeCreatorFlagOR`（4 子格）、`TestR5B3_005_SameNameMergeTunnelGroupSide` PASS |
| S-41 SS 计量伴生 | PASS（本地主断言；PG 子格承继） | handler：`TestSSMetering*` 14 项全 PASS；PG 子格 `TestPostgresCompanionPartialUniqueIndex` 本地 SKIP（无 DSN）——**本地不可判定·承继门证据**（承 R10 发版门 #7 同批真 PG PASS），行级不降格 |
| S-42 SS 密码每值校验 | PASS | `TestR6B1ValidSSCipherPerValue` PASS |
| S-43 SS2022 坏键 Clash 跳过 | PASS | `TestR6B1SS2022BadKeyClashSkip` PASS |
| S-44 链式目标守卫 | PASS | `TestR6B1ChainTargetGuards` PASS |
| S-45 导入权限 (1,0) 格 | PASS | `TestR6B2_004_PlaceholderGrantCreatorFlagOR` 四格全 PASS；`TestR3B6ImportPermissionsWithBackupGroupIDs`、`TestR3B6ImportPermissionsSkipsDisabledPair` PASS |
| S-46 误置授权仍清扫护盾链 | PASS | `TestR6B2_005_ShieldChainSweptDespiteMisplacedGrant` PASS |
| S-47 转发合计仅展示非计费 | PASS（代码面） | `dashboard.tsx:629-640` `calculateForwardTotalFlow` 注释逐字「此处合计仅为总流量展示，不是计费量」，消费点 `:1185` |
| S-48 导入状态枚举门 | PASS | `TestR5B5ImportStatusEnumGate` PASS |
| S-49 登录/订阅库状态谓词 | PASS | `handler.go:580`（login）与 `:951`（sub_store）`Status != 1` 在位，与 R18 同值；`TestR6B6LoginSubStoreStatusPredicate` PASS |
| S-50 计量开启入站改方法/安全层 | PASS（代码面；DOM 腿未验证） | `inbound/inbound-form-modal.tsx:1205` `isDisabled={meteringBlocked && !form.meteringEnabled}`（`meteringBlocked` 定义 `:441`）；空闲超时文案 `:280`「空闲超时须为 0–86400 秒的整数，留空或填 0 为默认 300 秒」 |
| S-51 ENC 标识三面 | PASS（代码面；DOM 腿未验证） | `inbound.tsx:674/:850/:1409` 三面在位（grid 卡片 / list / 移动端 sheet），徽标文案「IX 中转专用协议」逐面在位，与 R18 同值 |
| S-52 ENC 三门 | PASS | 行号与 R18 表逐字同值：`handler/kernel.go:2007/:2032/:2118`，函数定义 `:1997/:2031/:2083`，文案 `:2008/:2035/:2121` 均含「25.9.5 及以上」；`kernel_egress.go:48` 出站 prefer `versionBelowPadded(25,6,8)` 不变；`TestR8B1ENCVersionGateBoundary` PASS |
| S-53 SS 单面集 | PASS | `kernel/credentials.go:627` 六名 {`chacha20`、`aes-128-gcm-siv`、`aes-256-gcm-siv`、`chacha8-ietf-poly1305`、`aes-128-ccm`、`aes-256-ccm`}、`:644` 八值逐字在位；`TestR8B2SS*SingleFace` 5 项（AES128/256GCMSiv、Chacha8IETFPoly1305、AES128/256CCM）+ `TestR6B1SSChacha20SingleFace` + `TestR6B1ValidSSCipherPerValue` PASS |

**哨兵小结：51 PASS + 2 本地不可判定·承继门证据（S-16、S-34；S-41 的 PG 子格同口径承继，行级不降格），0 回退，0 新回退。**

## 3. 轮换域 A6 全量抽审（R14 全量轮口径）

### 3.1 变更面（相对 R14 全量轮基线 7244fbb 与 R18 基线 a0c18a0）

A6 域文件在 `7244fbb..d66042c` 区间仅 4 个文件有改动（`git diff --stat` 实测）：

| 文件 | 改动 | A6 相关性 |
|---|---|---|
| `go-backend/internal/kernel/render_xray.go` | +56/−17（R14-F03） | connIdle 空闲出站集单点计算（`idleOutboundSet`，防悬空引用）；D 域修复，1.4.8 已验证 |
| `go-backend/internal/kernel/chain.go` | +7/−3（R14-F03） | `xrayChainRoutes`/`xraySplitRoutes` 透传 `idleEmitted` 集；同上 |
| `vite-frontend/src/pages/subscription.tsx` | +2/−2（R14-F05） | 流水线顺序展示修正为「过滤 → 改名 → 国旗 → 去重 → 前缀 → 排序」（`pipelineSummary` 与 Section hint），与后端 `SubOptions` 注释口径对齐；展示修正，非行为变更 |
| `doc/guide/nodes.md` | +8（R14-F10） | 新增「空闲超时（秒）」一节（Xray 仅可用、sing-box 禁用、已知 2 倍时延行为）；D 域文档 |

其余 A6 核心文件（`handler/subscription.go`、`kernel/sub_options.go`、`sub_rules.go`、`sub_script.go`、`sub_chain.go`、`render_singbox.go`、`render_snell.go`、`render.go`、`share.go`、`share*.go`、`subscription_metering.go`、`credentials.go`、`proxy_parse.go`、`proxy_produce.go`、`import.go`、`remote_source.go`、`user_subscription*.go`、`kernel_relay.go`、`doc/guide/subscription.md`、`my-subscription.tsx`、`api/index.ts`）在 `7244fbb..d66042c` 区间 diff = 0 行。**相对 R18 基线 `a0c18a0..d66042c`，A6 文件差集 = 0 行**（本轮区间仅 R18 修复批：F01 隧道删除披露、F02/F04 管理 API 去重、F03 链/限速器去重与自愈，均未触 A6 文件）。故本轮 A6 抽审以「R14 全量结论复验 + 新探针重跑 + 原观察项复核」为主。

### 3.2 B-01…B-24 打勾表（新探针重跑 + 常驻集复验，全部 ✅）

| 项 | 结论 | 证据（本轮基线树实测） |
|---|---|---|
| B-01 `subPipeline` 全字段反序列化 | ✅ | `handler/subscription.go:755-772` 视图与 `options()` 映射逐字段核对（Script/Rules/RuleGroups/ChainLandingNames/ChainRelayNames/ChainLandingSources/ChainRelaySources/ChainLandingOwn/ChainNoneNames/ChainHideRelays/ChainSourceFilters/ChainDropUnused/RelayIDs 全映射；`SingboxSnell`/`SingboxNaive` 由 openSubscription 从 UA 派生），文件 diff = 0 |
| B-02 `normalize()` 长度/枚举校验 | ✅ | 新探针 `TestR19L3A6SubPipelineNormalize` PASS（`R19-L3-a6-probe-handler.log`）：Rename >10000、Prefix >50 rune、Script >20000 rune、Include/Exclude/Chain 表达式 >1000、名称名单 >1000 条、RelayIDs >1000 条均被拒；未知 Sort/Lines 归空；未知 Region/Protocol 修剪（`🇭🇰 香港` 保留、`bogus` 剔除）；RuleGroups 修剪为 `["ads","ai"]` |
| B-03 `NormalizeRuleGroups` 只保留已知 key 并按展示顺序 | ✅ | 新探针 `TestR19L3A6NormalizeRuleGroups` PASS：`["ai","bogus","ads","ai"]` → `["ads","ai"]` |
| B-04 处理顺序严格为 过滤→改名→国旗→去重→前缀→排序 | ✅ | 新探针 `TestR19L3A6PipelineOrderDedupeSort` PASS：改名后去重、去重后前缀、前缀后自然序；另 `subscription.tsx` 展示文案已与后端口径对齐（§3.1） |
| B-05 去重键只含 地址/端口/协议，不含名称 | ✅ | 同 B-04 探针：同址同口不同名（香港 10 / 香港 02）去重后只留第一个 |
| B-06 改名 `a => b`、`/正则/`、右边留空删除 | ✅ | 常驻 `TestApplySubOptionsRenameAndSort` PASS（kernel 全量日志内）；空名保留原名分支在位 |
| B-07 名称包含/排除 `\|` 分隔与 `/正则/` 语法 | ✅ | 代码面未动（diff = 0）；常驻 `TestRegionOf` 等 matcher 集 PASS |
| B-08 国旗识别不重复添加已有国旗 | ✅ | 代码面未动；常驻集 PASS |
| B-09 排序自然序 | ✅ | 常驻 `TestApplySubOptionsRenameAndSort` PASS（`X 1, X 02, X 10` 序） |
| B-10 按地区分组显式开关；与节点分组标题互不干扰 | ✅ | 代码读数：`groups := opts.RegionGroups \|\| opts.Rules`（`sub_options.go`），`Subscription()` 不输出节点分组标题；文件 diff = 0 |
| B-11 链式：两项都匹配算落地；按来源角色优先于名称表达式 | ✅ | 新探针 `TestR19L3A6ChainNamesBothMatch` PASS：同名进落地+中转名单 → 落地（须另有纯中转节点使 `chainGroups` 双边成立，否则按 B-12 清空）；来源 1=落地/2=中转压过反向名称表达式；单侧表达式角色整体清空 |
| B-12 链式失效不报错、不出分组（已知行为） | ✅ | 同 B-11 探针第三格：单侧落地 → 角色全部清空；`sub_chain.go:112` 同代码路径 |
| B-13 规则引擎：Clash 输出 proxy-groups+rules；sing-box 输出 route+rule_set；Loon/QX/SR 输出完整配置 | ✅ | 常驻回归集 PASS：`TestSubscriptionRulesOffRegression`、`TestSubscriptionRulesOnConfig`、`TestSubscriptionSurgeRulesGroupClosure`、`TestStashRulesYAMLAndBodyParity`、`TestStashRulesOffByteIdenticalToClash` |
| B-14 只开规则未开地区分组时仍附带地区分组（预期） | ✅ | 代码读数：`groups := RegionGroups \|\| Rules`，符合 N-12 预期；文件 diff = 0 |
| B-15 规则组名与节点名不冲突（预留） | ✅ | 代码读数：`uniqueNames` 在 rules 开启时预留规则组名（`share.go`）；文件 diff = 0 |
| B-16 goja 沙箱：无网络/文件/定时器；1s 超时；20000 字符上限 | ✅ | 新探针 `TestR19L3A6ScriptSandbox` PASS：`setTimeout` 不存在（报错）；死循环约 1.0s 被终止（报错含“超过”）；`MaxScriptLength = 20000`（`sub_script.go:32`） |
| B-17 脚本只有 `name` 改动被采纳；失败不影响其他步骤 | ✅ | 同 B-16 探针：脚本改 `server`/`port`/`protocol` 全部被丢弃，只采纳 `name`；`operator(nodes)` 返回数组决定集合与顺序（`sub_script.go:94`） |
| B-18 客户端 × 协议下发矩阵与文档排除表逐格一致 | ✅ | 新探针 `TestR19L3A6RenderMatrix` PASS（见 §3.3） |
| B-19 Snell v4/v5/v6：Clash/Stash/SR 按 v4 口径，v6 不下发，sing-box 需 1.14+ UA | ✅ | 同 §3.3 探针：v4/v5 在 Clash/Stash/SR 下发、v6 不下发；sing-box `SingboxSnell=true` 三版全下发，缺省全不下发 |
| B-20 `?format=singbox` 不含 Snell（不带 UA 版本） | ✅ | 同 §3.3 探针：`FormatSingbox` 且 `SingboxSnell=false` 时计数恰少 3（全部 Snell） |
| B-21 VLESS ENC 只在 Clash/Stash/base64 出现 | ✅ | 同 §3.3 探针：ENC 在 Clash/Stash/base64 下发，sing-box/SR/Loon/QX/Surge 全部不下发 |
| B-22 订阅流量统计：小时/日/月聚合边界、时区、月重置日 | ✅ | 新探针 `TestR19L3A6SubTrafficBuckets` PASS：小时 24 / 日 30 / 月 12 桶；DST 日（America/New_York 2026-03-08）日桶仍 30 个；+5:45 非整点时区日桶起点为当地 0 点；非法 TZ 回落 TZOffset、越界 offset 归 0 |
| B-23 流量统计只计独立凭据协议 | ✅ | 哨兵 S-41 复验：`TestSSMetering*` 14 项全 PASS；共享凭据协议不产生按订阅归属行（代码面 diff = 0） |
| B-24 SIP023 多段口径 | ✅（前提修正见观察项） | 新探针 `TestR19L3A6SIP023FourFaces` PASS（见 §3.5）；catalog §1.2 注现行表述仍为“Surge/Loon/QX/URI 四面原样”，未区分节点来源——与 R14 登记的 A6-观察-01 同口径，维持观察项、不立案 |

### 3.3 客户端 × 协议渲染矩阵（B-18/B-19/B-20/B-21 探针实证）

探针 `TestR19L3A6RenderMatrix`（`R19-L3-a6-probe-kernel.log`），9 节点（VLESS 明文、VLESS ENC、Trojan、SS、Hy2、TUIC、Snell v4/v5/v6）× 9 面，与 `doc/guide/nodes.md:118-126` 客户端×协议排除表及 `subscription.md:32-35` 逐格对齐，全部一致：

- Snell v4/v5：Clash/Stash/SR 按 v4 下发，v6 不下发；sing-box 1.14+（`SingboxSnell=true`）三版全下发，缺省全不下发；Loon/QX/base64 不下发 Snell。
- VLESS ENC：只在 Clash/Stash/base64；sing-box/SR/Loon/QX/Surge 不下发。
- Surge 无 VLESS；Loon 无 TUIC；QX 无 Hy2/TUIC；普通 VLESS 在其余面均下发。
- 计数核对（`SubscriptionNodeCount`）：Loon 4（9 − TUIC − 3 Snell − ENC）、QX 3（9 − TUIC − Hy2 − 3 Snell − ENC）、Clash 8（9 − Snell v6），与实得逐值一致；与 R14 §4 的 6 节点口径（Loon 5 / QX 4）同算法、无漂移。
- `?format=singbox` 的 Snell 下发仍按 UA 判定，与文档一致（B-20）。

### 3.4 C 域订阅源更新（C-12…C-16）

探针 `TestR19L3A6RemoteSourceFileSync`（`R19-L3-a6-probe-handler.log`），文件源实测：

| 项 | 结果 |
|---|---|
| C-12 ✅ 先按名称、再按地址匹配；地址变更同步中转 | v1 新增 2 → v2 只改名（地址不变，按地址对上更新，Updated 1）+ 新增 1（Added 1）→ v2b 只改地址（名称不变，地址跟着改，Updated 1）。注：改名与改址同时发生时按设计无法匹配（新行 + 旧行删除），属匹配口径本身，非缺陷 |
| C-13 ✅ 消失节点删除，但挂中转的保留 | v3 中 NodeB 不在源内但挂中转行 → Kept 1、Removed 0；删中转行与分配行后重同步 → Removed 1 |
| C-14 ✅ 拉取失败/空内容保留现有节点并显示错误 | 空内容同步报错，2 节点全部保留；拉取失败路径代码读数：`syncRemoteSource` 只在成功解析后写库（`remote_source.go:90`），失败不落盘（失败路径本身未本地实测，见 §5） |
| C-15 ✅ 文件源上限 5MB、不自动更新、需手动重新上传 | 代码读数：`remote_source.go:374-403`，上传超 5MB 拒收（“文件超过 5MB”）；文件源无定时同步入口，须手动重新上传（5MB 边界值未本地实测，见 §5） |
| C-16 ✅ `Subscription-Userinfo` 解析与展示 | 后端：`kernel_relay.go:945` 下发 `upload=%d; download=%d; total=%d; expire=%d`；前端：`vite-frontend/src/api/index.ts:976-977` 以 `userinfo: string` 承接该响应头并展示。两端对账一致（代码面 diff = 0） |

另：订阅分配/撤销语义（N-20，认 token 不认人）——`handler/user_subscription.go:173` 代码读数与 `subscription.md`「分配给用户与撤销」节一致：删分配/禁用/删用户只收回面板可见性，已复制链接继续有效；断链只能重置或删订阅。本轮无行为变更，未重复探针。

### 3.5 附则 A / A-2 回勾与 R9/R10 关闭项复验

- **附则 A 协查·订阅渲染面**：`TestR19L3A6RenderMatrix` 覆盖 6 协议 URI 形态（SS/VMess/VLESS/Trojan/Hy2/TUIC 导入形态经 Proxy 端点）+ ENC/Snell 分面；计数与实得一致（§3.3）。
- **附则 A-2**：R10 批 1/2/3 已集成在位——`finishProxy` 的 client-fingerprint 小写愈合 + 合法集、`xhttp-opts.mode` 枚举门、SS `plugin-opts.mode` {http,tls}、`forEndpoint` 的 Snell obfs/mode 愈合、`proxyURI` 的 VLESS ENC 谓词门（R10X-010）、R10X-013/C-01 的 Surge/Loon VMess `encrypt-method` 补搬回归用例在位；常驻 `TestR10B4SurgeVMessEncryptMethodCarried / DefaultAuto / GarbageRejected` 3/3 PASS，`TestR4B1RealityShortIDPredicate`、`TestR9B3LegalLowercaseVerbatim` PASS。`proxy_parse.go` 自 R14 起 diff = 0。
- **R4×C4 格翻转实证（本轮复验）**：`proxy_parse.go:1092` VMess 分支已读 `encrypt-method`（缺省 `auto`，`:1076` 亦读）；R10 登记的该格“未搬运”已由 R10X-013 修复批翻转为搬运，本轮复验确认在位。
- **R9 关闭项抽验**：R9X-002（C5×R1 搬运）、R9X-004（forEndpoint 四字段愈合）常驻用例 PASS；R9X-005（Naive 门）由渲染矩阵探针复证（sing-box Naive 面经 `SingboxNaive` 门，代码面 diff = 0）；R9X-003（ENC 谓词）属 D 主责、B 仅协查谓词面（S-52 PASS）。
- **R10-B-01 复验**：订阅头忽略 FlowMiB 已在 R11 修复，常驻 `TestSubStoreHeaderTotalUsesFlowMiBR10X005`（6 子格）本轮 PASS。
- **faces35 全表**：本轮未逐值重跑——`proxy_parse.go` / `share.go` 自 R14 起 diff = 0，R14 `TestR14A6Faces35` 全表结论（与 R10 §2.1 差集 ∅）无代码变更可推翻；本轮以 §3.3 九节点矩阵 + 单面集 S-53 逐值（`TestR6B1ValidSSCipherPerValue` PASS）为抽查锚点。客户端后果仍维持未验证（无新可验官方二进制）。

### 3.6 A6-观察-01 维持（观察项，不立案，不占 L3-CAND 号）

- **承 R14 登记**：SIP023 多段残余须按节点来源拆分登记。复现证据：全链探针 `TestR19L3A6SIP023FourFaces` PASS（与 R14 `TestR14A6SIP023FourFaces` 同对象复现）。导入节点经 `endpointProxy → forEndpoint` 直通，Loon/QX 仍得原样未填充段；但**自建 SS2022 入站**经 `ClashProxy`（`share.go:274` 用 `NormalizedSS2022PasswordSegments`）归一，故 Loon/QX 同样得填充 Std 形。Surge（`NormalizedSS2022Password` 单段 helper）与 URI（明文 URL 转义形，`ss://method:url-escaped-password@host:port#name`）两面始终原样。
- **结论**：Loon/QX 残余面缩窄为仅导入节点；Surge/URI 残余不变；客户端后果仍未验证。不违反任何用户文档承诺（`subscription.md` 未提 SIP023），故维持观察项、不立案。catalog §1.2 注现行表述仍未区分节点来源，登记口径修正仍待编目侧收口（非本轮产品缺陷）。
- **根因**：两条渲染路径不对称——自建入站走 `ClashProxy` 归一，导入节点走 `forEndpoint` 直通。
- **修法**：仅登记口径修正，按 N-36 以拆分后口径为准（自建面已归一 / 导入面残余 / Surge·URI 残余）；是否把导入面也归一属产品决策，本轮不主张、不立案。

### 3.7 常设回归集本域项

- **第 1 项 订阅列表/详情字段完整性** ✅：后端列表/详情视图（`handler/subscription.go:897-902`）返回 ID/Name/Kind/Token/Path/ExpireTime/Enabled/Active/Remark/UploadBytes/DownloadBytes/TrafficLimit/ResetDay/TrafficSince/OverQuota/RelayIDs；前端类型（`vite-frontend/src/api/index.ts:911/933/1127`）与消费面（`subscription.tsx:127/136/144`、`my-subscription.tsx:218`、`admin-overview.tsx:383`）逐字段对账，消费字段全部在位，无缺字段、无多余字段；`trafficLimit > 0` 门在两页均在位。
- **第 5 项 limit=0 语义** ✅：新探针 `TestR19L3A6LimitZeroSemantics` PASS（`TrafficLimit=0` 在 `subOverQuota` 须 `>0`（`subscription.go:343-344`）、`subActive` 均读作不限量；`used == limit` 判超额）；四面同向：执法面（`>0` 守卫）、通知面（`checkSubQuota` 的 `limit <= 0` 跳过）、展示面（`subscription.tsx:127/144`、`my-subscription.tsx:218` 的 `trafficLimit > 0` 门）、预警面（`admin-overview.tsx:383` 的 `s.trafficLimit &&` 门）；传输面订阅头 total=0（R10-B-01 修复，6 子格 PASS）。用户/UT 的 `Flow/FlowMiB` 0=不限（R9X-001）常驻 `TestFlowLimitZeroUserNotPausedR9X001` PASS。

### 3.8 R18-F01 在位复核（A6 邻接面，本轮区间唯一 handler 新增测试）

| 编号 | 对象 | 本轮复验 | 结论 |
|---|---|---|---|
| R18-X-01（P3）删隧道 gost 分支逐条清理半完成披露 | `tunnel_delete_resolution.go:747/:753` 披露码（`已删除 0 条转发` / `已删除 %d 条转发：…`）+ `r18_f01_tunnel_delete_disclosure_test.go` 9 项 | 9 项逐名 PASS（`TestR18F01DeletedForwardsSummaryFormat`、`TestR18F01MidLoopDataPlaneFailureDisclosesDeleted`、`TestR18F01LastForwardDataPlaneFailureDisclosesDeleted`、`TestR18F01FirstForwardDataPlaneFailureDisclosesZeroDeleted`、`TestR18F01MidLoopDbDeleteFailureDisclosesDeleted`、`TestR18F01BatchDeleteFailureReasonDisclosesDeleted`、`TestR18F01SuccessPathUnchanged`、`TestR18F01OfflineBranchDbDeleteFailureDisclosesDeleted`、`TestR18F01UserDeletePathDisclosesDeleted`）；handler +9 顶层计数恰为此 9 项 | ✅ 在位（关闭验证归 L1 路，本路仅记在位复核） |

**A6 新立案：0 条。** 抽审范围：B-01…B-24 全项 + C-12…C-16 全项 + 附则 A/A-2 协查 + R9/R10 关闭项抽验 + 常设回归集本域 2 项；新探针 10 项（kernel 6 + handler 4）全绿，常驻集 0 FAIL。

## 4. R13 关闭 7 项与 FINAL-P2-1 七段谓词维持关闭复核（逐项点名）

| 编号 | 对象 | 本轮复验 | 结论 |
|---|---|---|---|
| R13X-001（P2）备份导入后在线节点转发数据面重推 | `backupImportNeedsForwardRepush` / `repushForwardRuntimeAfterImport`（`backup_import_repush.go:38/:58`）+ `ImportResult.repushNodeIds/repushFailedNodeIds` + 调用点 `handler.go:2278`（撤销清理之后） | 常驻 `TestR13BRBackupImportForwardsRepushesOnlineNode` / `TestR13BRRepushSkipsWhenNoLiveAgent` / `TestR13BRRepushTrigger` / `TestR13BRBackupImportNodesResyncsRelayRemoteAddr` 全 PASS | ✅ 维持关闭 |
| R13X-002（P3）`tunnelCreate` 补 `type`/`status` 守卫 | `tunnelCreate` 四键守卫（`mutations.go:1219` 起，`:1261-1280` `type/flow/status` 数值门 + `trafficRatio` 浮点门，与 `tunnelUpdate` 同形；函数入口行相对 R18 的 :1261 漂移，守卫本体未动） | 常驻 `TestR13T2aTunnelCreateRejectsUnparsableTypeAndStatus`（4 子格）+ `TestR13T2aTunnelCreateAcceptsLegalTypeAndStatus` PASS | ✅ 维持关闭 |
| R13X-003（P3）编辑表单省略域外 ratio/flow | `tunnel.tsx:951/:957`（`omitFlow`/`omitRatio`，`delete payload.flow` / `delete payload.trafficRatio` 仅编辑态；行号相对 R18 的 :1019-1020 漂移，本体未动） | 代码面在位；DOM 实点未验证，与 R13 门 #3 同口径，归真机/浏览器 | ✅ 维持关闭（代码面；DOM 腿未验证） |
| R13X-004（P3）inbounds 段子段无导入计数 | `ImportResult` 七子段计数（`model.go:973-976` `InboundKernelsImported`/`InboundRelaysImported` 等）+ 赋值点 `repository.go:3150-3153` 取自真实 counts | 常驻 `TestR13BRInboundSubsectionCounts`（repo 包）PASS | ✅ 维持关闭 |
| R13X-005（P3）未知类型字符串静默忽略 | `validBackupTypes` 16 值（`repository.go:2288-2297`）+ `validateBackupTypes` 点名未知值 | 16 值逐字在位（users/nodes/tunnels/forwards/userTunnels/speedLimits/tunnelGroups/userGroups/permissions/configs/inbounds/userInbounds/announcements/monitorPermissions/userSubscriptions/serviceMonitors）；`TestR13BRUnknownBackupTypeRejected`（repo 包）PASS | ✅ 维持关闭 |
| R13X-006（P3）导入型入站 Protocol 与 Extra 失配 | `repository_inbound_backup.go:537-551`（导入型入站 kernel_id≤0 时 `kernel.ProxyProtocol(p)` 派生 Protocol，`:551` 派生点在位） | 常驻测试 PASS（repo 全量日志内） | ✅ 维持关闭 |
| R13X-007（P3）隧道 Flow/TrafficRatio 备份恢复面无保存面门 | `validateImportedTunnelFlow`（`{1,2}`）/ `validateImportedTunnelTrafficRatio`（`[0,100]`，NaN/Inf 拒）（`repository.go:3625-3642`，相对 R18 的 :3620 漂移 +5，门体未动），门在预删前、跳过逐行点名 | 常驻 `TestR13T2cImportTunnelsSkipsOutOfDomainFlowAndRatio`（repo 包）PASS | ✅ 维持关闭 |
| FINAL-P2-1（P2）备份导入重推谓词漏限速相关段 | 七段谓词 `backupImportNeedsForwardRepush`（`backup_import_repush.go:38-47`）：Forwards / Tunnels / Nodes / Inbounds / InboundRelays / **SpeedLimitsImported** / **UserTunnelsImported**，七段逐字在位 | 新增断言 `backup_final_p2_1_test.go` 4 项全 PASS：`TestFinalP21RepushTrigger`（谓词表：speedLimits-only → true、userTunnels-only → true；kernels-only / subscriptions-only → false；forwards 锚点 → true；nil → false）、`TestFinalP21SpeedLimitsOnlyImportRepushesLimiter`、`TestFinalP21UserTunnelsOnlyImportRepushesLimiter`、`TestFinalP21SubscriptionOrKernelOnlyImportDoesNotRepush` | ✅ 维持关闭 |

## 5. 观察项（不立案）

- **O-L3-1**：A6-观察-01 维持（SIP023 多段残余按节点来源拆分登记，见 §3.6；承 R14，不占立案号）。
- **未验证清单（维持）**：144 格四面客户端后果（无新可验官方二进制）；SIP023 残余面（缩窄后：Surge/URI 两面 + Loon/QX 导入节点面）的客户端后果；S-16 导入腿与 S-34 两契约本地不可判定（承继门证据）；S-41 PG 子格本地未实跑（承继门证据）；真实节点/真实客户端下的订阅拉取端到端（本轮为快照探针，无真实节点）；DOM 腿未验证（无 live browser）；C-14 拉取失败路径与 C-15 文件源 5MB 边界值仅代码读数，未实测；faces35 全表本轮未逐值重跑（`proxy_parse.go`/`share.go` 自 R14 起 diff = 0，见 §3.5）。

## 6. 证据总账

| 文件 | 字节数 | 内容 |
|---|---|---|
| `R19-00-sentinel.md` | 18,191 | 哨兵表 53 行（承 R17，开工前落盘） |
| `R19-00-baseline.md` | 584 | 基线 main @ d66042c |
| `R19-L3-handler-full.log` | 950,120 | handler 全量 `-v` 原始日志（exit 0，1325 PASS / 0 FAIL / 4 SKIP） |
| `R19-L3-kernel-full.log` | 49,605 | kernel 全量 `-v` 原始日志（首跑 exit 1：常驻 431 PASS / 0 FAIL + 本路探针 4 FAIL 写法错误；修正后重跑见探针日志） |
| `R19-L3-repo-full.log` | 282,985 | repo 全量 `-v` 原始日志（exit 0，267 PASS / 0 FAIL） |
| `R19-L3-netguard.log` | 1,024 | netguard 原始日志（8 PASS / 0 FAIL） |
| `R19-L3-contract-pg.log` | 226,105 | PG 契约全包 `-v` 原始日志（exit 0，243 PASS / 9 SKIP，无 DSN） |
| `R19-L3-gost.log` | 217 | go-gost root（lifecycle ok）原始日志 |
| `R19-L3-gost-x.log` | 11,692 | go-gost/x 模块 27 包 ok 原始日志 |
| `R19-L3-a6-probe-kernel.log` | 678 | A6 探针重跑 kernel（6/6 PASS）原始日志 |
| `R19-L3-a6-probe-handler.log` | 1,516 | A6 探针重跑 handler（4/4 PASS）原始日志 |
| 本文件 | 见落盘自验 | R19-L3 合成报告 |

探针文件 `r19_l3_a6_probe_test.go`（kernel + handler 两处）已删除，删除后快照内 grep 残留 0 命中；读数树 `~/workspace/.r19-tree` 交付时 `git status` 仅未跟踪 `audit-out-132/r19/`，HEAD `d66042cc0b7ac011ddfad09f3f2709b90dafda2e`。
自证：正文共 39693 字节（不含本行及尾行），尾行 END。
— R19-L3-sentinel-a6 END · 哨兵 53 行（51 PASS / 2 承继 / 0 回退）· A6 新立案 0 · R13 关闭 7 项与 FINAL-P2-1 七段谓词维持关闭 · END
