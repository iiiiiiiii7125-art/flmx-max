# R16-L3 哨兵回归 + 轮换域 A2 全量抽审报告

- **任务**：R16 哨兵回归（S-01…S-53）+ 轮换域 A2（API）全量抽审；只读审计，不改产品代码、不打 tag、不发版。
- **基线（pin 死）**：`main @ b54b44f`（完整 `b54b44f772d8b4efdd6d08b1fbbbc40c8091a143`，2026-10-24 21:55:16 +0000，「docs(r15): archive V-06 verdict corrections」）。
- **基线对照**：R15 基线 `main @ 5bd5a90`。`5bd5a90..b54b44f` 仅 2 提交（`583cec0` fix(r15): errors.md stale 500 row + demo fixture；`b54b44f` docs 归档），产品代码差集 = `inbound.tsx` 探针空值修复一处 + `errors.md` 码表一行 + demo 夹具/门脚本，其余产品代码零改动。
- **日期**：2026-10-25（Asia/Shanghai）。
- **读数与测试隔离**：共享工作树在 `feat-latency-hint @ a99d68d`，全程未 checkout、未改文件；读数树 `~/workspace/.r16-tree @ b54b44f`（终态 `git status` 空，pristine）；测试在隔离快照 `~/workspace/.r16-tmp/r16l3base`（`git archive b54b44f` 解包）跑；Go 为 `~/go/bin/go`（go1.25.1）。
- **护栏**：N-01…N-37 全程有效；销账对账探针对象本身（N-35）；探针结论只认落盘原始日志 + 完整名单（N-37）；探针文件带域标、跑后即删（已删，快照内 `R16L3` 残留 grep 0 命中）。

## 结论总览

| 面 | 结论 |
|---|---|
| 哨兵 53 行 | **51 PASS + 2 本地不可判定·承继门证据（S-16、S-34；S-41 的 PG 子格同口径承继，行级不降格），0 回退** |
| A2 域全量抽审 | **新发现 1 项候选 L3-CAND-01（P3，userList 分页乘法溢出 panic）**；路由 202 / 数据表 49 双向差集 ∅；信封九码发射点与 errors.md 码表一致（500 零发射点）；其余抽审面无新增问题 |
| R13 关闭 7 项 | **维持关闭 7/7**（对象本身复核，见 §3） |
| FINAL-P2-1 七段谓词 | **维持关闭**（谓词七段在位，行为测试 4/4 PASS，见 §3） |
| R15 关闭 2 项邻接复核 | R15-X-01（errors.md 500 行）修复提交 `583cec0` 为 b54b44f 祖先，码表与发射点一致；R15-X-02（demo 夹具缺 `backup/gist/status`）夹具与录制脚本均已含该项 |
| 新立案（临时编号） | **L3-CAND-01（P3）1 项**（由协调者统一编号，不得自编 R16-X-##） |
| 观察项（不立案） | 3 条：O-L3-1（FIX-PLAN-132 A2 行过期）、O-L3-2（S-51 行号注记）、O-L3-3（S-39(c) 注释行 +1 漂移） |

## 全量回归计数（基线 b54b44f，隔离快照）

| 包 | 结果 | 日志 |
|---|---|---|
| kernel 全量 `-v` | **264 PASS / 0 FAIL / 0 SKIP**（`=== RUN` 431 行，与 R15 计数口径一致） | `R16-L3-kernel-full.log` |
| handler 全量 `-v` | **1306 PASS / 0 FAIL / 4 SKIP**，`ok 458.383s`（与 R15 逐数一致：1306 PASS 行、同一 4 个 SKIP） | `R16-L3-handler-full.log` |
| repo 全量 `-v` | **234 PASS（顶层）/ 0 FAIL / 0 SKIP**（`=== RUN` 267 行，与 R15 一致） | `R16-L3-repo-full.log` |
| netguard | **8 PASS / 0 FAIL / 0 SKIP** | `R16-L3-netguard.log` |
| PG 契约靶向 | **3 SKIP**（无 DSN，本地不可判定，见 S-34/S-41） | `R16-L3-contract-pg.log` |
| A2 自写探针 | **1/1 PASS**（两溢出形均如预期 panic、两对照均 code 0） | `R16-L3-a2-probe.log` |

handler 4 SKIP 与 R15 同名同形态：`TestInboundProbeEmptyIDsDoesNotClearDown`、`TestR2B10InboundRecoveryFailureResendsOnce`（S-16 导入腿）、`TestR5B9ImportedNodeDownScheduledRoundAlerts`、`TestR5B9ImportedNodeRecoveryScheduledRoundAlerts`。

---

## 1. 哨兵逐行表（S-01…S-53）

结论口径：PASS = 代码在位 + 对应测试在基线快照实跑通过；「本地不可判定·承继门证据」= 本地无条件复跑（无 DSN / 无测试机），承继已落盘的门证据，**不标 PASS、不计失败**；DOM 腿（无 live browser）与历轮同口径记「代码面在位，DOM 腿未验证」。

| 行 | 结论 | 证据 |
|---|---|---|
| S-01 隧道删除 | PASS | handler：`TestTunnelDeleteToleratesRemoteFailureAndDeletesTunnel`、`TestR1B3TunnelDeleteMigrationRecomputesRelayRemoteAddr`、`GostNodeOfflineSucceeds`、`NftNodeOfflineGoesPending`、`OnlineRealFailureAborts` 5/5 PASS |
| S-02 分组禁用撤销授权 | PASS | `TestB2_XP12_GroupTunnelDeleteRevokesGroupGrants`、`TestR2B7_013_DisableUserGroupRevokesGrants` / `DisableTunnelGroupRevokesGrants` 3/3 PASS |
| S-03 监控写守卫 | PASS | 四写入口 `monitoring.go:442/:582/:703/:731`（create/update/delete/run）均在位；`TestR1MonitorWriteRequiresAdmin` PASS；`ensureMonitoringAccess` 调用点 10 处在位 |
| S-04 导入 host 校验 | PASS | kernel：`TestR1B5ValidateImportHostRejectsNonCanonical`、`TestR2B1ValidateImportHostLoopbackAliases` / `HardDenyLiterals` / `PrivateLiteralsStillAllowed`、`TestN01ValidateImportHostLinkLocalV6Rejected` / `ULAStillAllowed` 全 PASS |
| S-05 导入跳过缺失用户 | PASS | repo：`TestR1B5ImportUserSubscriptionsSkipsMissingUser`、`TestBackupImportUserSubscriptionsSkipsUnresolvable` PASS |
| S-06 通知失败次轮重发 | PASS | `TestR1KernelErrorNotifyFailureResendsNextRound`、`TestR1RouteChangeNotifyFailureResendsNextRound` PASS |
| S-07 install.sh 更新路径 | PASS | `install.sh:589` 起 `flux_agent_health_ok` 双采样（is-active + PID 跨 2s 稳定）；`.bak` 仅健康通过后删除（`:950-955`，失败分支先 `return 1`） |
| S-08 联邦端口占用 | PASS | `TestFederationTunnelCreateRejectsOccupiedPort`、`TestFederationRuntimeReservePortRejectsWhenShareFlowExceeded` PASS；锁内复检 `validateForwardPortAvailabilityTx`（`mutations.go:2369`） |
| S-09 订阅重置轮换全凭据 | PASS | `TestR1B7MainSubscriptionResetStillRotatesFullCredentials`、`TestR1B7LegacySubInfoResetRotatesFullCredentials`、`TestSubInfoResetStaysAdminOnlyAndRotates` PASS |
| S-10 单远端地址非公网拒绝 | PASS | `TestR2B1CheckSingleRemoteAddrNonPublicDenied` PASS |
| S-11 Snell 版本域 | PASS | kernel：`TestR2B5SnellFinishProxyVersionDomain` / `RenderersVersionDomain` / `ChainTargetRenderWith` / `V6ModeRoundTrip` 4/4 PASS |
| S-12 导入 host 校验（内核面） | PASS | 同 S-04 测试族（kernel 日志内全 PASS） |
| S-13 白名单刷新 | PASS | R2B9 白名单刷新 6 项 PASS：`TunnelDeleteWithForwardsDeleteRefreshesWhitelist`、`TunnelBatchDeleteWithForwardsDeleteRefreshesWhitelist`、`TunnelDeleteWithForwardsReplaceRefreshesWhitelist`、`ForwardBatchChangeTunnelRefreshesWhitelist`、`UserDeleteRefreshesWhitelist`、`NodeUpdateAddressRefreshesWhitelist` |
| S-14 seq 只读后推进 | PASS | `TestR1B8_XP331_CheckSeqReadOnlyThenAdvance`、`TestNodeSeqConcurrentFirstReports` PASS |
| S-15 导入剥离悬空限速引用 | PASS | `TestR2B8ImportPartialRestoreStripsDanglingSpeedRefs`、`TestR2B8ImportFullRestoreKeepsSpeedRefs` PASS |
| S-16 恢复失败重发 | **本地不可判定·承继门证据** | 节点腿 `TestR2B10NodeRecoveryFailureResendsOnce` PASS、延迟腿 `TestR2B10LatencyRecoveryFailureResendsOnce` PASS；导入腿 `TestR2B10InboundRecoveryFailureResendsOnce` 本地 SKIP（与 R13/R14/R15 同形态）——**承 R10 发版门 #8 测试机实跑 PASS**；行级不降格、不标 PASS |
| S-17 部分失败只重发失败通道 | PASS | `TestR2B10PartialFailureResendsOnlyFailedChannel` PASS |
| S-18 install.sh 安装路径 | PASS | 安装路径服务未 active 时 `return 1`（`install.sh:796`，R2X-019 注释 `:784-786` 在位） |
| S-19 install.sh start 前 reset-failed | PASS | 两处 start 前 `reset-failed`（`install.sh:519/:536`） |
| S-20 批量结果消费 | PASS（代码面；DOM 腿未验证） | 四消费点在位：分组分配 failures（`group.tsx:499-504`）、批量升级 results（`node.tsx:1160-1163`）、单节点 alreadyLatest（`node.tsx:1112-1119`）、maxConn warnings（`user.tsx:761-775`，逐条 toast） |
| S-21 创建幂等 | PASS | `TestR2B2_MonitorServiceCreateIdempotent`、`TestR2B2_FederationShareCreateIdempotent`、`TestR2B2_RemoteSourceSaveCreateIdempotent`、`TestR2B2_InboundRelayCreateIdempotent`、`TestR1B8_XP217_ForwardCreateIdempotencyKey` 全 PASS |
| S-22 转发换隧道刷新白名单与远端地址 | PASS | `TestR3B4ForwardUpdateTunnelSwitchRefreshesWhitelistAndRemoteAddr` PASS |
| S-23 零倍率全链配额零 | PASS | `TestR3B3ZeroRatioWholeChainQuotaZeroForwardRowRawPreserved`（handler）、`TestR3B3FlowUploadMetasZeroRatioPreserved`（repo）PASS |
| S-24 同步撤销库存禁用授权 | PASS | `TestR3B5_004_SyncRevokesStockDisabledGrants`、`TestR3B5_004_ZeroToZeroUpdateRevokesStockDisabledGrants` PASS |
| S-25 导入分组/占位授权收敛 | PASS | repo 5 facet 全 PASS：`TestR3B6ImportUserGroupsMemberReplacementRevokes`、`TestR3B6ImportTunnelGroupsMemberReplacementRevokes`、`TestR3B6ImportGroupPlaceholdersRepointPermissions`、`TestR3B6ImportRelayPlaceholderRepointsUserInbound`、`TestR3B6ImportDisabledUserGroupConvergesGrants` |
| S-26 隧道 warnings 消费 | PASS（代码面；DOM 腿未验证） | `tunnel.tsx:1028-1031` 消费 `data.warnings` |
| S-27 SS2022 导入矩阵 | PASS | `TestR3B2SS2022ImportMatrix` 全子格 PASS |
| S-28 保留组播/链路本地拒绝 | PASS | kernel：`TestR3B1ValidateImportHostRejectsReservedMulticastLinkLocal`、`TestR3B1ValidateImportHostStillAllowsPrivateCGNAT`；netguard：`TestNetguardMatrix`、`TestNetguardClassifyTranslations`、`TestR3B1HardDenyOrLoopbackRejectsReservedMulticastLinkLocal`、`TestR3B1OtherPoliciesUnchangedForReservedMulticast`；handler：`TestR3B1ProbeRejectsReservedBeforeDial`、`TestR3B1ProbeRejectsMulticastAliasAfterResolution` 全 PASS |
| S-29 密码大小写变体导入 | PASS | `TestR4B1CipherCaseVariantImport` PASS |
| S-30 Reality 公钥校验 | PASS | `TestR4B1RealityBadPBKRejectedAllPaths` / `TestR4B1RealityGoodPBKAcceptedAllPaths` / `TestR4B1RealityShortIDPredicate` PASS |
| S-31 隧道替换迁移 | PASS | `TestR4B6TunnelReplaceMigrationFailureRepushesWhitelist`、`TestR4B6TunnelReplaceDeleteSourceFailureRepushesWhitelist` PASS |
| S-32 双分组禁用创建者在先/注册者在先 | PASS | `TestR4B4_005_DualGroupDisableCreatorFirst` / `RegistrarFirst` PASS |
| S-33 备份导入经 grant 台账执法 | PASS | `TestR3B6BackupImportRevokedForwardsCleaned` PASS + S-25 五 facet；差集/pair 形态不在本行覆盖内（N-36，归 R5X-015 登记口径） |
| S-34 并发双应用契约 | **本地不可判定·承继门证据** | `TestPostgresApplyInboundCounterSamplesConcurrentDoubleApplyContract` 与 `TestPostgresApplySubscriptionCounterSamplesConcurrentDoubleApplyContract` 本地 SKIP（文案 `set FLMX_POSTGRES_TEST_DSN to run`，`R16-L3-contract-pg.log`）——**承 R10 发版门 #7 真 PG 3/3 PASS + R11/R13/R15 抽核**；不标 PASS、不计失败，禁以 SQLite/seq 契约顶替 |
| S-35 联邦分享删除刷新白名单 | PASS | `TestR4S2FederationShareDeleteRefreshesWhitelist` PASS |
| S-36 枚举垃圾拒绝/合法值接受 | PASS | `TestR5B1EnumGarbageRejectedAllPaths`、`TestR5B1EnumLegalValuesAccepted` PASS |
| S-37 Reality 公钥校验（复核面） | PASS | 同 S-30（`TestR4B1RealityBadPBKRejectedAllPaths` PASS） |
| S-38 库存渲染跳过 | PASS | `TestR5B1StockRenderSkip`、`TestR6B1SSStockRenderSkip` PASS |
| S-39 六子面白名单补刷 | PASS | (a)(b) 动态：`TestR5B2TunnelDeletePartialFailureRepushesWhitelist`、`TestR5B2UserDeletePartialFailureRepushesWhitelist` PASS；(c) `tunnel_delete_resolution.go:392/:405` R5X-004(c) 注释在位（行号较 R15 +1，见 O-L3-3）；(d) `kernel_relay.go:637/:641` `inboundRelayDelete` 内 `refreshInboundWhitelist` 在位；(e) `mutations.go:515-527` R5X-004(b) 补刷在位；(f) `kernel.go:523` R5X-004(f) 注释 + 补刷在位 |
| S-40 同名合并创建者标志 OR | PASS | `TestR5B3_005_SameNameMergeCreatorFlagOR`（4 子格）、`TestR5B3_005_SameNameMergeTunnelGroupSide` PASS |
| S-41 SS 计量伴生 | PASS（本地主断言；PG 子格承继） | handler：`TestSSMetering*` 14 项全 PASS（含 `CompanionWriteGuards`、`SaveMeteringEligibilityGate`、`SplitSaveGateAndReconcile`、`ReconcileLifecycle`、`PayloadGate`、`WantedMatrix`、`OriginDeleteCascades` 等）；PG 子格 `TestPostgresCompanionPartialUniqueIndex` 本地 SKIP（无 DSN）——**本地不可判定·承继门证据**（承 R10 发版门 #7 同批真 PG PASS），行级不降格 |
| S-42 SS 密码每值校验 | PASS | `TestR6B1ValidSSCipherPerValue` PASS |
| S-43 SS2022 坏键 Clash 跳过 | PASS | `TestR6B1SS2022BadKeyClashSkip` PASS |
| S-44 链式目标守卫 | PASS | `TestR6B1ChainTargetGuards` PASS |
| S-45 导入权限 (1,0) 格 | PASS | `TestR6B2_004_PlaceholderGrantCreatorFlagOR` 四格全 PASS；`TestR3B6ImportPermissionsWithBackupGroupIDs`、`TestR3B6ImportPermissionsSkipsDisabledPair` PASS |
| S-46 误置授权仍清扫护盾链 | PASS | `TestR6B2_005_ShieldChainSweptDespiteMisplacedGrant` PASS |
| S-47 转发合计仅展示非计费 | PASS（代码面） | `dashboard.tsx:629-640` `calculateForwardTotalFlow` 注释逐字「此处合计仅为总流量展示，不是计费量」，消费点 `:1185` |
| S-48 导入状态枚举门 | PASS | `TestR5B5ImportStatusEnumGate` PASS |
| S-49 登录/订阅库状态谓词 | PASS | `handler.go:564`（login）与 `:929`（sub_store）`Status != 1` 在位；`TestR6B6LoginSubStoreStatusPredicate` PASS |
| S-50 计量开启入站改方法/安全层 | PASS（代码面；DOM 腿未验证） | `inbound/inbound-form-modal.tsx:1205` `isDisabled={meteringBlocked && !form.meteringEnabled}`；空闲超时文案 `:273/:280`「空闲超时须为 0–86400 秒的整数，留空或填 0 为默认 300 秒」 |
| S-51 ENC 标识三面 | PASS（代码面；DOM 腿未验证） | `inbound.tsx:672/:843/:1403` 三面在位（grid 卡片 / list / 移动端 sheet），徽标文案「IX 中转专用协议」在 `:674/:850/:1405`（行号见 O-L3-2；本轮唯一产品代码改动即此文件探针空值修复，徽标本体未动） |
| S-52 ENC 三门 | PASS | b54b44f 行号与 R15 表逐字同值：`handler/kernel.go:2007/:2032/:2118`，函数定义 `:1997/:2031/:2083`，文案 `:2008/:2035/:2121` 均含「25.9.5 及以上」；`kernel_egress.go:48` 出站 prefer `versionBelowPadded(25,6,8)` 不变；`TestR8B1ENCVersionGateBoundary` PASS |
| S-53 SS 单面集 | PASS | `kernel/credentials.go:627` 六名 {`chacha20`、`aes-128-gcm-siv`、`aes-256-gcm-siv`、`chacha8-ietf-poly1305`、`aes-128-ccm`、`aes-256-ccm`}、`:644` 八值逐字在位；`TestR8B2SS*SingleFace` 5 项 + `TestR6B1SSChacha20SingleFace` + `TestR6B1ValidSSCipherPerValue` PASS |

**哨兵小结：51 PASS + 2 本地不可判定·承继门证据（S-16、S-34；S-41 的 PG 子格同口径承继，行级不降格），0 回退，0 新回退。**

---

## 2. 轮换域 A2 全量抽审（API）

方法：路由普查脚本复刻 R15-L4 口径（`R15-L4-census.py` 仅改读数树路径为 `~/workspace/.r16-tree`），对 b54b44f 重做普查并与 R15 原料表双向对账；信封码发射点全仓普查；方法守卫抽点核对函数体；错误路径与分页/批量语义逐面抽核；A2-01…A2-06 等 R1 深审遗留项按对象本身复验（见 §2.5）。

### 2.1 路由全集：202 条，双向差集 ∅

| 项 | 本轮实测 | 与 R15 原料表双向差集 |
|---|---|---|
| 路由总数 | **202**（handler.go 201 + router.go 1；`/api/v1` 197 + 非 API 5） | ∅（路径/ handler / 守卫 / 鉴权四列逐行一致；`diff` 空） |
| 鉴权分布 | ADMIN 135 / JWT 44 / PUBLIC 17 / OPTIONAL-AUTH 1 / 非 API 5 | ∅（与 R15 分布逐字一致） |
| 方法守卫分布 | POST only 131 / ANY 53 / GET only 13 / not(Get,Post) 1 / GET,HEAD 2 / GET,POST 1 / GET (WS upgrade) 1 | ∅（与 R15 分布逐字一致） |
| 数据表总数 | **49**（48 个 `model.*` + 仓库内 `NodeSeq`） | ∅（集合与顺序逐行一致） |

- 鉴权分类器复核：普查脚本的 `shouldSkip` / `requiresAdmin` 与 `middleware/auth.go`（`shouldSkip` `:119-144`、`requiresAdmin` `:146-213`、`allowsOptionalAuth` 仅 `/api/v1/config/get`）逐字比对一致，含 `/api/v1/config/get` 在 shouldSkip 中显式 `false`、联邦 7 路 skip、`/api/v1/inbound/sub/info` 与 `/api/v1/tunnel/user/tunnel*` 的非 admin 例外、`user/quota/reset` 与 `user-inbound/list|set`、`user-subscription/list|set` 的 admin 收紧——差集 ∅ 是重推导结论，非抄旧数。
- 方法守卫抽点：`storageSummary`（`storage.go:10`，`r.Method != Get && != Post` → census 标 `not(Get,Post)`，全集唯一）；`userList` 等 POST only（`handler.go:482/:611/:672` 同形）；`/system-info` 为 router.go WS 升级入口。抽点与普查标签一致。
- 原料表：`R16-L3-routes-raw.csv`（203 行含表头）、`R16-L3-tables-raw.txt`（49 行）。

### 2.2 信封码全集：九码发射点与 errors.md 码表一致（R15-X-01 邻接复核）

发射点普查（`go-backend/internal/http/response/r.go` 为唯一信封构造点；`ErrDefault` = `Err(-1)`，`OK/OKEmpty` = code 0）：

| 码 | errors.md 表述 | 发射点（非测试代码） | 一致 |
|---|---|---|---|
| `0` | Success | `response.OK(` 145 处 + `response.OKEmpty(` 83 处 | ✅ |
| `-1` | Business error | `ErrDefault` 703 处 + `Err(-1` 10 处 | ✅ |
| `-2` | Server/DB error | `Err(-2` 337 处 | ✅ |
| `-3` | Target address validation failed（监控 SSRF 检查） | `Err(-3` 4 处，均在 `monitoring.go:490/:501/:660/:669` | ✅ |
| `401` | Unauthorized | `Err(401` 42 处（含中间件未登录/无效 token/受限 token 改密前置） | ✅ |
| `403` | Forbidden | `Err(403` 45 处（含 `requiresAdmin` 非 admin、PwdChangeRequired 受限 token） | ✅ |
| `409` | Same request already in progress | `Err(409` 1 处（`idempotency.go:238`，「相同请求正在处理中，请稍后重试」） | ✅ |
| `429` | Rate limited | `Err(429` 3 处（`handler.go:897/:1200`、`login_rate_limit.go:367`），三处均前置 `Retry-After: 60` 头 | ✅ |
| `500` | 仅历史注记 | **零发射点**（`Err(500` / `Code: 500` / `code: 500` 全仓 0 命中）；前端 `code === 500` 引用 0 处 | ✅ |

- **R15-X-01 维持关闭**：修复提交 `583cec0` 为 b54b44f 祖先（`git merge-base --is-ancestor` 验证）；`errors.md` 现行表 8 行（0/-1/-2/-3/401/403/409/429）+ 表下历史注记「500 was once the parameter-error code… 500 has no emission point」，与发射点普查逐一对上，无失实表述。
- 非信封面注记（非不一致）：`http.Error` 直出 18 处（BadGateway 3 / BadRequest 2 / Forbidden 3 / InternalServerError 3 / MethodNotAllowed 2 / NotFound 1 / TooManyRequests 4，集中于 `gh_mirror.go`、订阅直出、`kernel_relay.go:835/:872`）走 HTTP 状态码而非信封 `code` 字段，不在 errors.md 码表口径内；`WriteHeader` 仅 NoContent/NotFound 各 1 处。前端 401 消费在位（`api/index.ts:541` 信封 code 401 分支、`utils/jwt.ts:76` / `session.ts:86` 401 跳转/登出）。

### 2.3 handler 方法守卫与错误路径抽核

- 方法守卫：全集分布与 R15 逐字一致（§2.1）；抽点 3 类（POST only / not(Get,Post) / WS）均与函数体原文相符，无标签漂移。
- 错误路径：参数错误统一 `-1`（`ErrDefault` 703 处为主通道，F-08 口径）；DB/服务端错误 `-2`（337 处）；监控 SSRF 校验 `-3`（4 处）；鉴权失败 401、越权 403 均经中间件 + handler 双层（中间件 `auth.go` 先行，handler 内 `ensureMonitoringAccess` 等二次守卫在位）。未发现信封码表外的新码。

### 2.4 分页 / 批量语义抽核

- **分页**：全仓唯一带 `current/size` 分页的 list 接口是 `userList`（`handler.go:702-751`）；`nodeList` / `tunnelList` / `forwardList` 等其余 list 接口均全量返回、无分页参数（前端侧处理，属既有设计）。`userList` 分页逻辑：未传参数（0）保持全量兼容；`current/size > 0` 时切片，未做上溢防护——**见 L3-CAND-01（§4）**。其余分页形字段：`gist_backup.go:217` 的 `Size` 为 GitHub API 回包字段，非分页。
- **批量**：14 条批量路由（`forward/batch-*` 5 条、`inbound/batch-delete|batch-toggle`、`node/batch-delete|batch-upgrade`、`tunnel/batch-delete|batch-delete-preview|batch-delete-with-forwards|batch-redeploy`、`tunnel/user/batch-assign`）全数在路由全集内；`batchOperationResult{SuccessCount, FailCount, Failures, …}` 回执在 `kernel.go` / `mutations.go` / `tunnel_delete_resolution.go` 共 10 处构造点在位，含已修复的 `nodeBatchDelete`（`mutations.go:1150-1203`，逐条收集成败、A2-05 注释在位）。

### 2.5 R1 深审 A2 遗留项按对象本身复验（非哨兵顶替）

| 项 | 现状（b54b44f） |
|---|---|
| A2-01 latency_targets 校验接线 | ✅ 已修：`normalizeLatencyTargetsConfig` 已接线（`handler.go:1565-1567`，「本为此写就，现正式接线」），`parseLatencyTargets`（`node_latency.go:63-104`）校验上限 30 / 卡片固定上限 3，坏值保存即拒绝 |
| A2-02 公告首建 enabled=0 被吞 | ✅ 已修：`UpsertAnnouncement`（`repository.go:804-824`）首建后 enabled==0 写回，A2-02 注释在位 |
| A2-03 服务监控创建 enabled=0 被吞 | ✅ 已修：`CreateServiceMonitor`（`repository.go:6181-6196`）创建后 enabled==0 写回，A2-03 注释在位 |
| A2-05 nodeBatchDelete 吞错虚报 | ✅ 已修：见 §2.4（逐条回执，A2-05 注释在位） |
| A2-06 userUpdate groupIds / userTunnelBatchAssign | ✅ 已修：`replaceErr` 上抛 `Err(-2)`（`mutations.go:390-393`，X-P2-6/X-P1-2 注释）；`userTunnelBatchAssign`（`mutations.go:2969` 起）整批单事务 + 同步失败补偿回滚（X-P3-22 注释） |
| A2-04 真部分更新 | 未在本轮复核（FIX-PLAN 仍标待修，见 O-L3-1）；不作已验/未验表述 |

### 2.6 A2 域小结

路由/鉴权/方法守卫/数据表四项钉死值全部维持，双向差集 ∅；信封九码与码表一致；批量回执口径在位；R1 深审 A2 遗留 5 项已修在位。**新增候选 1 项：L3-CAND-01（P3，§4）。**

---

## 3. R13 关闭 7 项维持关闭复核（按对象本身，不得以哨兵行顶替）

| 编号 | 对象 | 复核结论 |
|---|---|---|
| R13X-001（P2）备份导入后补推 | PASS | `TestR13BRBackupImportForwardsRepushesOnlineNode`、`TestR13BRRepushSkipsWhenNoLiveAgent`、`TestR13BRRepushTrigger`、`TestR13BRBackupImportNodesResyncsRelayRemoteAddr` 均 PASS；调用点 `handler.go:2256` 在撤销清理（`:2248-2250`）之后，使重推下发的是导入终态；谓词见 §4 |
| R13X-002（P3）tunnelCreate 四键守卫 | PASS | `TestR13T2aTunnelCreateRejectsUnparsableTypeAndStatus`（4 子格）+ `AcceptsLegalTypeAndStatus` PASS；代码 `mutations.go:1261-1280` 在位 |
| R13X-003（P3）编辑表单省略域外 ratio/flow | PASS（代码面；DOM 实点未验证，与 R13 门 #3 同口径，归真机/浏览器） | `tunnel.tsx:951-1020`（`omitFlow`/`omitRatio`，`delete payload.flow` / `delete payload.trafficRatio` 仅编辑态）在位 |
| R13X-004（P3）ImportResult 7 项实落计数 | PASS | 字段在位（`model.go:973-979`）；赋值点在位（`repository.go:3149-3156`）：`InboundsImported`/`InboundKernelsImported`/`InboundRelaysImported`/`InboundCountersImported`/`InboundSubscriptionsImported`/`InboundSubscriptionCountersImported`/`InboundSourcesImported`/`InboundLegacyUsersImported` 均取自真实 counts；`TestR13BRInboundSubsectionCounts` PASS |
| R13X-005（P3）validBackupTypes 16 值双侧 | PASS | 后端 `repository.go:2288` 起 16 值（users/nodes/tunnels/forwards/userTunnels/speedLimits/tunnelGroups/userGroups/permissions/configs/inbounds/userInbounds/announcements/monitorPermissions/userSubscriptions/serviceMonitors），`validateBackupTypes` 点名未知值；前端 `BACKUP_TYPE_OPTIONS`（`config.tsx:252-269`）16 值逐值一致（双侧排序后集合相等）；`TestR13BRUnknownBackupTypeRejected` PASS |
| R13X-006（P3）Protocol 重推导 | PASS | `repository_inbound_backup.go:539-551`：仅 `KernelID <= 0` 导入型行才经 `kernel.ProxyProtocol` 重推导，Extra 为空或推导不出时保留原值，Extra 逐字落库不变；`TestR13BRImportedInboundProtocolRederived` PASS |
| R13X-007（P3）importTunnels 门前置于链路预删前 | PASS | 门在预删之前：`validateImportedTunnelFlow`/`validateImportedTunnelTrafficRatio`（`repository.go:3625/:3632`，调用 `:3645/:3650`）先过滤出 `validTunnels`，预删（`:3675` 起）只覆盖通过门的行；`TestR13T2cImportTunnelsSkipsOutOfDomainFlowAndRatio` PASS（`SkippedRows["tunnels"] = 5`） |

## 4. FINAL-P2-1 七段谓词维持关闭复核

- 谓词在位（`backup_import_repush.go:38-46`）：`ForwardsImported>0 || TunnelsImported>0 || NodesImported>0 || InboundsImported>0 || InboundRelaysImported>0 || SpeedLimitsImported>0 || UserTunnelsImported>0`——七段齐全。注意：纯内核/纯订阅不触发是因触发看顶层 `InboundsImported` 而非子计数 `InboundKernelsImported`/`InboundSubscriptionsImported`，与 FINAL-P2-1 门探针 C1/C2 一致，无矛盾。
- 行为测试 4/4 PASS：`TestFinalP21RepushTrigger`、`TestFinalP21SpeedLimitsOnlyImportRepushesLimiter`、`TestFinalP21UserTunnelsOnlyImportRepushesLimiter`、`TestFinalP21SubscriptionOrKernelOnlyImportDoesNotRepush`。

---

## 5. 新发现：L3-CAND-01（P3）—— userList 分页乘法溢出可致 slice panic

- **位置**：`go-backend/internal/http/handler/handler.go:740`，`userList` 内：
  ```go
  start := (req.Current - 1) * req.Size
  if start > len(users) { start = len(users) }
  end := start + req.Size
  if end > len(users) { end = len(users) }
  users = users[start:end]
  ```
  两处乘法/加法均无上溢防护；`start` / `end` 为负时钳制分支（`> len`）不生效，直接以负下标切片。
- **探针证据**（自写探针 `TestR16L3UserListPaginationOverflow`，真 handler + 真 SQLite 库，`R16-L3-a2-probe.log`）：
  - 对照 1 `{"current":1,"size":10}` → code 0，1 条用户正常返回；
  - 对照 2 `{"current":2147483648,"size":2147483648}`（大值但不溢出）→ code 0，空页；
  - 溢出形 1 `{"current":4611686018427387904,"size":3}` → panic：`runtime error: slice bounds out of range [:-4611686018427387904]`（`(2^62-1)*3` 回绕为负）；
  - 溢出形 2 `{"current":2,"size":9223372036854775807}` → panic：`runtime error: slice bounds out of range [:-9223372036854775808]`（`start` 被钳制为 len 后 `end = 1 + MaxInt64` 回绕为负）。
  - 探针修正手记（诚实记录）：初版以 `Username` 字段建用户致编译失败（模型字段实为 `User`）；首轮两溢出形其一未触发，系探针用户 `RoleID` 默认为 0、被 `ListUsers` 的 `role_id != 0` 过滤致列表为空、第二形被空表钳制掩盖；补 `RoleID: 1` 后两形均复现。两修正均为探针侧问题，非产品行为问题。
- **生产症状**：不 crash、不泄漏数据——`middleware/recover.go:14` 兜底记服务端日志并回 code `-2`（"internal error"）。即管理员以溢出分页参数调 `/api/v1/user/list` 只会得到一次 `-2`，其余行为不受影响。
- **影响面**：须登录且仅管理员可调（`requiresAdmin` 含 `/api/v1/user/list`）；触发须主动构造溢出量级的 `current/size`，正常分页（含超大合法页）不受影响；与 R1 深审 A2-api.md 所述「疑似 P3」定级一致，故定 **P3**。
- **新立案依据**：该项在 `audit-out-132/` 全仓仅见于 `A2-api.md:214`（「疑似 P3」，未编号立案）；`FIX-PLAN-132.md`、R13/R15 总账与各轮报告全文 grep `A2-S1` 0 命中——从未立案、从未修、从未复核，本轮 A2 分页语义抽审首次实证确认，故以临时编号 L3-CAND-01 登记，由协调者统一编号。
- **修复建议**（供协调者定）：在 `current/size` 解码后钳制（如 `current > 1<<20 || size > 1<<10` 拒绝/截断）或以 `int64` 乘前判溢出（`current-1 > MaxInt64/size` 时按空页/报错处理）；改动面仅 `userList` 一处，全仓唯一分页算术点（已全仓 grep 确认）。

---

## 6. 观察项（不立案）

| # | 内容 |
|---|---|
| O-L3-1 | **FIX-PLAN-132.md 的 A2 行过期**：A2-01（latency_targets 接线）、A2-02（公告 enabled=0）、A2-03（监控 enabled=0）、A2-05（nodeBatchDelete 回执）、A2-06（groupIds 替换 / 批量单事务）在 b54b44f 代码中均已修复在位（证据见 §2.5），但 FIX-PLAN 仍标「待修」。属计划文档过期，非产品问题；建议协调者更新该表或注明口径。A2-04（真部分更新）本轮未复核，不在此列 |
| O-L3-2 | **S-51 行号注记**：b54b44f 实测徽标在 `inbound.tsx:672/:843/:1403`（文案 `:674/:850/:1405`）；5bd5a90 同文件实测为 `:666/:837/:1397`，+6 恰为 R15 修复的探针空值 hunk（`+8/-2`）所致，徽标本体未动。R15-L3 报告所引 668/844/1399 与 5bd5a90 实测差 2 行，属该报告行号引用偏差，非产品问题；本轮以 b54b44f 实测为准 |
| O-L3-3 | **S-39(c) 注释行 +1 漂移**：R5X-004(c) 注释现为 `tunnel_delete_resolution.go:392/:405`（R15 记 `:391/:404`），内容逐字未变，属行号漂移 |

## 7. 未验证项清单

| # | 项 | 原因与口径 |
|---|---|---|
| U-1 | S-16 导入腿 `TestR2B10InboundRecoveryFailureResendsOnce` | 本地 SKIP（与 R13/R14/R15 同形态）；承 R10 发版门 #8 测试机实跑 PASS；不标 PASS、不计失败 |
| U-2 | S-34 并发双应用契约两测试 | 本地 SKIP（无 DSN）；承 R10 发版门 #7 真 PG 3/3 PASS + R11/R13/R15 抽核；禁以 SQLite/seq 契约顶替 |
| U-3 | S-41 PG 子格 `TestPostgresCompanionPartialUniqueIndex` | 本地 SKIP（无 DSN）；承 R10 发版门 #7 同批真 PG PASS；行级不降格 |
| U-4 | 前端 DOM 腿（S-20/S-26/S-50/S-51、R13X-003） | 无 live browser；代码面在位，与历轮同口径，归真机/浏览器 |
| U-5 | 5 秒空闲超时计时行为腿 | 测试机范围，本轮未做计时观测 |
| U-6 | A2-04 真部分更新 | 本轮未复核，不作已验/未验表述 |

## 8. 证据清单

`~/workspace/flvx-max/audit-out-132/r16/` 内本轮落盘日志：

| 文件 | 内容 |
|---|---|
| `R16-L3-kernel-full.log` | kernel 包全量 `-v`（264 PASS / 0 FAIL / 0 SKIP；`=== RUN` 431 行） |
| `R16-L3-handler-full.log` | handler 包全量 `-v`（1306 PASS / 0 FAIL / 4 SKIP，`ok 458.383s`） |
| `R16-L3-repo-full.log` | repo 包全量 `-v`（234 顶层 PASS / 0 FAIL / 0 SKIP；`=== RUN` 267 行） |
| `R16-L3-netguard.log` | netguard 包（8 PASS / 0 FAIL / 0 SKIP） |
| `R16-L3-contract-pg.log` | PG 契约靶向（3 SKIP，无 DSN） |
| `R16-L3-a2-probe.log` | A2 自写探针 `TestR16L3UserListPaginationOverflow`（1/1 PASS：两溢出形 panic、两对照 code 0） |
| `R16-L3-routes-raw.csv` | 路由原料表：203 行含表头（202 条；与 R15 原料表 `diff` 空） |
| `R16-L3-tables-raw.txt` | 数据表原料表：49 行（与 R15 原料表 `diff` 空） |

探针文件 `r16_l3_probe_test.go` 仅存于隔离快照，已删除（快照内 `R16L3` 残留 grep 0 命中）；读数树 `~/workspace/.r16-tree` 终态 `git status` 空；共享工作树未动（`feat-latency-hint @ a99d68d`）。

---

END
