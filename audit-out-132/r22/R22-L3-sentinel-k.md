# R22-L3 报告：哨兵回归（S-01…S-53）+ 轮换域 A9（K 域）全量抽审

- **任务**：R22 再审计 L3 路——哨兵 53 行逐行复验 + 轮换域 A9（K 域：前端页面与交互一致性）全量抽审；只读审计，不改产品代码、不打 tag、不发版、不合 main。本路单 lane 执行、单独汇报，不等其他路。
- **基线**：main @ b823421（`b82342137cb3157197ad43906c41ccbc6145b64d`，R21 修复 fix-r21 已合入并推 origin）。哨兵表 `R22-00-sentinel.md` 与基线 `R22-00-baseline.md` 开工前落盘。
- **读数树**：只读 worktree `~/workspace/.r22-tree` @ b823421（`git status` 空，全程 pristine，交付时 HEAD 仍为 b823421）；探针跑测在 `git archive b823421` 快照 `~/workspace/.r22-l3-tmp/snap` 上跑；本路未新建任何探针测试文件（只跑全量套件 + 代码核对，快照内 `r22_l3_probe` 残留 0 命中）。
- **测试机**：新机 ip-172-26-4-158（tailnet 100.123.93.34）。锁窗口 **R22-L3 2026-10-25T14:00:28Z–14:02:45Z**（`acquire`/`release` 均经 `scripts/testmachine-lock.sh`；抢占前锁在 R22-L1 手中，本路未操作测试机直至锁空闲）；腿为只读（served chunk md5 + 登录后计数对账），**无部署、无重启**；腿后计数 1/0/0/1/1 pristine，与基线一致，锁已放。
- **结论口径**：PASS = 代码在位 + 对应测试在基线快照实跑通过；「本地不可判定·承继门证据」= 本地无条件复跑（无 DSN / 无测试机），承继已落盘的门证据，**不标 PASS、不计失败**；DOM 腿（无 live browser）与历轮同口径记「代码面在位，DOM 腿未验证」。
- **哨兵表回勾说明**：本路**未修改**共享文件 `R22-00-sentinel.md`（五 lane 并行，防写冲突）；53 行回勾结论以本报告 §2 表为准（逐行证据 + 原始日志在盘），请 L5 汇总时据此回填。

## 0. 总账

| 项 | 结论 |
|---|---|
| 哨兵 53 行 | **51 PASS + 2 本地不可判定·承继门证据（S-16、S-34；S-41 的 PG 子格同口径承继，行级不降格），0 回退** |
| K 域新立案 | **2 条**：CAND-01（P3·产品，`generateCaptcha` 死导出指向未注册端点）、CAND-02（P3·文档，forward.md:67「节点页 Pending Delete 管理接口」与实际不符） |
| K 域观察项 | **5 条**（O-K-1…O-K-5，均不立案；含一次凭据回显事故说明，见 §4.3） |
| R14 A9-1…A9-5 | **5/5 在 b823421 代码层关闭**（A9-1 卡片药丸门、A9-2 warnings 全量渲染、A9-3 失败腿保留实测带宽、A9-4 connIdle 0/-1 口径统一、A9-5 非 Xray 存量值保存自动清 0；DOM 腿未验证） |
| R21 修复批交叉影响 | fix-r21 的 11 项新测试（F01×3/F02×5/F03×3）+ connIdle-unlimited 的 handler 2 项全部 PASS；handler 算术 1342+13=1355 闭合，0 删减 |
| 钉死值 | 路由 **202**（handler.go 201 处 `mux.HandleFunc`：196 个 distinct `/api/v1/` + ghMirrorPath 变量 1 + /flow/test、/flow/config、/flow/upload、/error 4 + router.go:13 1 处 `/system-info`）；数据表 **50**（model 48 处 `TableName()` + node_seq 1 = 49 AutoMigrate，含 node_traffic + 迁移创建的 schema_migration） |

## 1. 全量套件计数（基线快照实跑，日志落盘 r22/）

| 套件 | 计数 | 日志 |
|---|---|---|
| handler 全量 `-v` | **1355 PASS（含子测；顶层 1027）/ 0 FAIL / 4 SKIP**，`ok 331.305s` | `R22-L3-handler-full.log` |
| kernel 全量 `-v` | **436 PASS（含子测；顶层 269）/ 0 FAIL / 0 SKIP**，`ok 1.840s` | `R22-L3-kernel-full.log` |
| repo 全量 `-v`（`internal/store/...`） | **267 PASS（含子测；顶层 234）/ 0 FAIL / 0 SKIP**，`ok 155.410s` | `R22-L3-repo-full.log` |
| netguard | **8 PASS / 0 FAIL / 0 SKIP**，`ok 0.015s` | `R22-L3-netguard.log` |
| PG 契约（全包） | **243 PASS（含子测；顶层 140）/ 0 FAIL / 9 SKIP**（无 DSN） | `R22-L3-contract-pg.log` |
| go-gost root | **lifecycle ok** | `R22-L3-gost.log` |
| go-gost/x 全量 | **全部包 ok**（socket `ok 0.841s`） | `R22-L3-gost-x.log` |
| install-proxy 套件 | **exit 0**，末行 `install script tests passed` | `R22-L3-install-proxy.log`（+ `R22-L3-install-proxy-xtrace.log`，-x 调用轨迹） |
| health `TestValidateMonitorTCPTarget`（17 子格，`internal/health/`） | **16 PASS / 1 环境红**（公网域名放行，fake-IP DNS 环境红，与 R21 同签名） | `R22-L3-health-probe.log` |
| 前端构建（tsc + vite） | **exit 0**，PWA precache 65；dist index md5 `57ab941e…` = served index md5 | `R22-L3-frontend-build.log` |
| demo 形状漂移 + fixtures 内容闸 | **双双 exit 0**（261 个实录对象覆盖全部必填字段；fixtures 内容全 ok） | 见 §3.22（快照内实跑，无落盘日志；命令可重跑） |
| served chunk（测试机腿） | **index/主 JS/主 CSS 三项 md5 与本地 dist 逐字一致**；paneld md5 `1ca4572b…` = 基线；`/flow/test` 200 | `R22-L3-served-chunk.log` + `R22-L3-counts.log` |

- 计数口径：`--- PASS` 含子测行（与 R21 口径一致；括号内为顶层 `^--- PASS` 数）。
- handler 算术：1355 = R21 的 1342 + 13；顶层 1027 = R21 的 1014 + 13。+13 恰为 `7d93792..b823421` 区间 handler 包 Test func 净增（88f10db connIdle-unlimited 2 项 `TestConnIdleUnlimitedCompanionSyncSwitches`/`TestConnIdleUnlimitedPersistsAndRenders` + fix-r21 11 项：F01×3、F02×5、F03×3），0 删减，含子测与顶层双口径一致。
- kernel 算术：436 = R21 的 433 + 3（88f10db `TestConnIdleUnlimitedNormalize`/`TestConnIdleUnlimitedRendersOneYear`/`TestConnIdleUnlimitedSharesLevels`），0 删减。
- repo 算术：267 = R21 的 267，0 增 0 减。netguard 8 = R21 的 8。PG 契约 243 PASS / 9 SKIP，与 R21 逐数一致（9 SKIP 均为无 DSN/环境门控，含 S-34 两契约 + S-41 `TestPostgresCompanionPartialUniqueIndex` 等）。
- handler 4 SKIP 与 R21 同名同形态：`TestInboundProbeEmptyIDsDoesNotClearDown`、`TestR2B10InboundRecoveryFailureResendsOnce`（S-16 导入腿）、`TestR5B9ImportedNodeDownScheduledRoundAlerts`、`TestR5B9ImportedNodeRecoveryScheduledRoundAlerts`。
- **首跑中断与重跑（O-K-4）**：首版 nohup  detached 跑法下 handler 在 13:43:35 中断（日志停在 `TestConfigCleanupRemovesOrphanedForwardLimiter`，364 PASS / 0 FAIL，无 FAIL、无收尾行、无 OOM 痕迹），part2 脚本亦在 repo 编译期消失；同期箱上另有 L1/L5 的 handler.test 在跑，疑似五 lane 并行下的 OOM/误杀（未证实）。处置：改用 runtime 追踪的后台 exec 重跑，repo → contract-pg → gost → install-proxy → health 顺序完成；handler 待箱上无 handler.test 后单独重跑，全绿（331.3s，exit 0）。中断版 handler 部分日志已由重跑版同名文件覆盖（内容为重跑全量），中断事实记于此，不复现为失败。
- health 探针跑错包教训：首版 health 探针误跑 `./internal/http/handler/`（`no tests to run`，因测试在 `internal/health/`），已纠正重跑；错误版日志同名覆盖，不留歧义。
- 方法注记：`GOTMPDIR`/`TMPDIR` 指工作区大盘（`~/.r22-l3-tmp/gotmp`、`~/.r22-l3-tmp/tmp`）；小套件以 `GOMAXPROCS=1` 顺序跑，handler 重跑用默认并行度；`git status` 交付时空，HEAD b823421。

## 2. 哨兵逐行表（S-01…S-53）

（逐行证据见下表；行号均为本轮基线树 b823421 实测值。方法：点名实测 = 本轮在全量日志内逐名确认 PASS（具名核对脚本 `R22-L3-sentinel-check.log`，Go 具名 68 PASS / 4 SKIP / 0 FAIL / 0 MISSING；5 个 shell 测试格另以 -x 轨迹核对）；CODE 行为代码面在位 + 后端契约核对；同族未逐一列出的测试以六套全量日志 **0 FAIL** 为据——日志在盘可复核。S-52 行号注记： sentinel 表所载 :2007/:2032/:2118 与本轮实测 :2008/:2033/:2119 各差一行（表载为函数定义/注释行附近值），门体与文案逐字在位，见 S-52 行。）

| 哨兵 | 对象 | 结论 | 证据（本轮基线树实测） |
|---|---|---|---|
| S-01 隧道删除 | PASS | `TestTunnelDeleteToleratesRemoteFailureAndDeletesTunnel` PASS；R21-F03 的 `TestR21F03*` 3 项同批 PASS（强制删除 gost 路径发 DeleteService + DeleteCLimiters，与本行删除立即生效口径一致） |
| S-02 分组禁用撤销授权 | PASS | `TestB2_XP12_GroupTunnelDeleteRevokesGroupGrants`、`TestR2B7_013_DisableUserGroupRevokesGrants` PASS |
| S-03 监控写守卫 | PASS | `TestR1MonitorWriteRequiresAdmin` PASS；四写入口门在位（`monitoring.go` create/update/delete/run 均为 `ensureAdminAccess`，与 R21 同值） |
| S-04 导入 host 校验 | PASS | `TestR1B5ValidateImportHostRejectsNonCanonical` PASS；`ValidateImportHost`（`kernel/import.go:85`）在位 |
| S-05 导入跳过缺失用户 | PASS | `TestR1B5ImportUserSubscriptionsSkipsMissingUser` PASS（repo 日志内） |
| S-06 通知失败次轮重发 | PASS | `TestR1KernelErrorNotifyFailureResendsNextRound`、`TestR1RouteChangeNotifyFailureResendsNextRound` PASS |
| S-07 install.sh 更新路径 | PASS | `install.sh` 在 `7d93792..b823421` 区间 diff = 0 行；`flux_agent_health_ok` :597、双采样 `sleep 3` :957、`.bak` 仅健康通过后删除 :963；-x 轨迹含 `test_flux_agent_health_ok_requires_stable_pid` + `test_update_flux_agent_rolls_back_flapping_binary` 两格调用，套件 exit 0 |
| S-08 联邦端口占用 | PASS | `TestFederationTunnelCreateRejectsOccupiedPort` PASS |
| S-09 订阅重置轮换全凭据 | PASS | `TestR1B7MainSubscriptionResetStillRotatesFullCredentials` PASS |
| S-10 单远端地址非公网拒绝 | PASS | `TestR2B1CheckSingleRemoteAddrNonPublicDenied` PASS；`IsSafeRemoteAddr`（`security_utils.go:26`）在位 |
| S-11 Snell 版本域 | PASS | `TestR2B5SnellFinishProxyVersionDomain` PASS（kernel 日志内） |
| S-12 导入 host 校验（内核面） | PASS | `TestR2B1ValidateImportHostLoopbackAliases`（kernel）+ `TestR2B1ProbeExternalInboundBlocksLoopback`（handler）PASS |
| S-13 白名单刷新 | PASS | `TestR2B9TunnelDeleteWithForwardsDeleteRefreshesWhitelist` PASS |
| S-14 seq 只读后推进 | PASS | `TestR1B8_XP331_CheckSeqReadOnlyThenAdvance` PASS（repo 包）；`CheckSeq`/`advanceSeqTx` 在位 |
| S-15 导入剥离悬空限速引用 | PASS | `TestR2B8ImportPartialRestoreStripsDanglingSpeedRefs` PASS（repo 日志内） |
| S-16 恢复失败重发 | **本地不可判定·承继门证据** | 节点腿 `TestR2B10NodeRecoveryFailureResendsOnce` PASS（63.71s）、延迟腿 `TestR2B10LatencyRecoveryFailureResendsOnce` PASS；导入腿 `TestR2B10InboundRecoveryFailureResendsOnce` 本地 SKIP（与 R13…R21 同形态）——**承 R10 发版门 #8 测试机实跑 PASS**；行级不降格、不标 PASS |
| S-17 部分失败只重发失败通道 | PASS | `TestR2B10PartialFailureResendsOnlyFailedChannel`、`TestR2B10DailyReportPendingVerbatimAndSnapshot`、`TestR2B10SingleChannelBehaviourUnchanged` PASS |
| S-18 install.sh 安装路径 | PASS | `install.sh` 本轮区间 diff = 0 行；-x 轨迹含 `test_install_flux_agent_returns_nonzero_when_service_not_active` 调用，套件 exit 0 |
| S-19 install.sh start 前 reset-failed | PASS | 两处 start 前 `reset-failed`（`install.sh:527/:544`）；-x 轨迹含 `test_systemd_starts_reset_failed_before_start` + `test_rollback_restarts_service_from_failed_state` 两格调用，套件 exit 0 |
| S-20 批量结果消费 | PASS（代码面；DOM 腿未验证） | 四消费点在位：分组分配 failures（`group.tsx:499-504`，部分失败报数 + 首条原因）、批量升级 results（`node.tsx:1160-1163`，汇总成功/失败、失败列首条原因）、单节点 alreadyLatest（`node.tsx:1112-1119`，直接显示后端 message）、maxConn warnings（`user.tsx:761-775`，逐条 toast） |
| S-21 创建幂等 | PASS | `TestR2B2_MonitorServiceCreateIdempotent`、`TestR1B8_XP217_ForwardCreateIdempotencyKey` PASS |
| S-22 转发换隧道刷新白名单与远端地址 | PASS | `TestR3B4ForwardUpdateTunnelSwitchRefreshesWhitelistAndRemoteAddr` PASS |
| S-23 零倍率全链配额零 | PASS | `TestR3B3ZeroRatioWholeChainQuotaZeroForwardRowRawPreserved` PASS |
| S-24 同步撤销库存禁用授权 | PASS | `TestR3B5_004_SyncRevokesStockDisabledGrants` + `TestR3B5_004_ZeroToZeroUpdateRevokesStockDisabledGrants` + `TestR3B5_004_EnabledGroupSyncUnaffected` PASS |
| S-25 导入分组/占位授权收敛 | PASS | `TestR3B6ImportUserGroupsMemberReplacementRevokes` PASS（repo 包） |
| S-26 隧道 warnings 消费 | PASS（代码面；DOM 腿未验证） | `tunnel.tsx:1028-1031` 消费 `data.warnings`（R3X-007 注释在位，逐条 ⚠️ 后报成功） |
| S-27 SS2022 导入矩阵 | PASS | `TestR3B2SS2022ImportMatrix` 全子格 PASS（kernel 日志内） |
| S-28 保留组播/链路本地拒绝 | PASS | `TestR3B1ValidateImportHostRejectsReservedMulticastLinkLocal`（kernel）、`TestNetguardMatrix`（netguard）PASS |
| S-29 密码大小写变体导入 | PASS | `TestR4B1CipherCaseVariantImport` PASS |
| S-30 Reality 公钥校验 | PASS | `TestR4B1RealityBadPBKRejectedAllPaths` + `TestR4B1RealityGoodPBKAcceptedAllPaths` PASS；存量半边归 S-37、未以本行覆盖（N-36 口径） |
| S-31 隧道替换迁移 | PASS | `TestR4B6TunnelReplaceMigrationFailureRepushesWhitelist` + `TestR4B6TunnelReplaceDeleteSourceFailureRepushesWhitelist` 两子面 PASS |
| S-32 双分组禁用创建者在先/注册者在先 | PASS | `TestR4B4_005_DualGroupDisableCreatorFirst` + `TestR4B4_005_DualGroupDisableRegistrarFirst` PASS |
| S-33 备份导入经 grant 台账执法 | PASS | `TestR3B6BackupImportRevokedForwardsCleaned` PASS；差集/pair 形态不在本行覆盖内（N-36，归 R5X-015 登记口径） |
| S-34 并发双应用契约 | **本地不可判定·承继门证据** | `TestPostgresApplyInboundCounterSamplesConcurrentDoubleApplyContract` 与 `TestPostgresApplySubscriptionCounterSamplesConcurrentDoubleApplyContract` 本地 SKIP（文案 `set FLMX_POSTGRES_TEST_DSN to run`，`R22-L3-contract-pg.log`）——**承 R10 发版门 #7 真 PG 3/3 PASS + R11/R13/R15/R16/R17/R18/R19/R20/R21 抽核**；不标 PASS、不计失败，禁以 SQLite/seq 契约顶替 |
| S-35 联邦分享删除刷新白名单 | PASS | `TestR4S2FederationShareDeleteRefreshesWhitelist` PASS |
| S-36 枚举垃圾拒绝/合法值接受 | PASS | `TestR5B1EnumGarbageRejectedAllPaths` + `TestR5B1EnumLegalValuesAccepted` PASS（kernel 包） |
| S-37 Reality 公钥校验（复核面） | PASS | 同 S-30（`TestR4B1RealityBadPBKRejectedAllPaths` PASS） |
| S-38 库存渲染跳过 | PASS | `TestR5B1StockRenderSkip` + `TestR6B1SSStockRenderSkip` PASS |
| S-39 六子面白名单补刷 | PASS | (a)(b) 动态：`TestR5B2TunnelDeletePartialFailureRepushesWhitelist` + `TestR5B2UserDeletePartialFailureRepushesWhitelist` PASS；(c)–(f) 补刷点在位（`tunnel_delete_resolution.go`、`kernel_relay.go`、`mutations.go`、`kernel.go`，与 R21 同值） |
| S-40 同名合并创建者标志 OR | PASS | `TestR5B3_005_SameNameMergeCreatorFlagOR` PASS（repo 日志内） |
| S-41 SS 计量伴生 | PASS（本地主断言；PG 子格承继） | handler：`TestSSMetering*` **14 项全 PASS**；PG 子格 `TestPostgresCompanionPartialUniqueIndex` 本地 SKIP（无 DSN）——**本地不可判定·承继门证据**（承 R10 发版门 #7 同批真 PG PASS），行级不降格 |
| S-42 SS 密码每值校验 | PASS | `TestR6B1ValidSSCipherPerValue` PASS |
| S-43 SS2022 坏键 Clash 跳过 | PASS | `TestR6B1SS2022BadKeyClashSkip` PASS |
| S-44 链式目标守卫 | PASS | `TestR6B1ChainTargetGuards` PASS |
| S-45 导入权限 (1,0) 格 | PASS | `TestR6B2_004_PlaceholderGrantCreatorFlagOR` PASS（repo 包） |
| S-46 误置授权仍清扫护盾链 | PASS | `TestR6B2_005_ShieldChainSweptDespiteMisplacedGrant` PASS（repo 包） |
| S-47 转发合计仅展示非计费 | PASS（代码面） | `dashboard.tsx:629-640` `calculateForwardTotalFlow` 注释逐字「此处合计仅为总流量展示，不是计费量」，消费点 `:1185` |
| S-48 导入状态枚举门 | PASS | `TestR5B5ImportStatusEnumGate`（repo）+ `TestR7B2ImportForwardsClearsPendingDeleteSideTable`（repo）+ `TestR7B2PendingRetryRechecksRestoredForward`（handler）PASS |
| S-49 登录/订阅库状态谓词 | PASS | `handler.go:580`（login）与 `:951`（sub_store）`Status != 1` 在位；`TestR6B6LoginSubStoreStatusPredicate` PASS |
| S-50 计量开启入站改方法/安全层 | PASS（代码面；DOM 腿未验证） | `inbound-form-modal.tsx:456` `meteringBlocked` 定义、`:1244` `isDisabled={meteringBlocked && !form.meteringEnabled}`（行号较 R21 的 :1205 漂移 +39，系「不限」功能插入所致，语义未动）；块保留 + 原因文案在位 |
| S-51 ENC 标识三面 | PASS（代码面；DOM 腿未验证） | `inbound.tsx:674/:850/:1409` 三面在位（grid 卡片 / list / 移动端 sheet），文案「IX 中转专用协议」逐面在位；徽标全集对账：三面均含协议 chip、ENC、仅中转、计量中、运行态徽标（grid :669-735、list :844-949、sheet :1398-1467），无缺面；relay.tsx 线路两视图亦含 ENC 徽标（:229/:327） |
| S-52 ENC 三门 | PASS | 门 floor 实测：`kernel.go:2008`（`kernelVersionGate`，定义 :1998）/ `:2033`（`encDialGate`，定义 :2032）/ `:2119`（`checkKernelVersionGates` 换版扫描，定义 :2084）均为 `versionBelow3(version, 25, 9, 5)`；三处文案 `:2009/:2036/:2122` 均含「25.9.5 及以上」；`kernel_egress.go:48` 出站 prefer `versionBelowPadded(version, 25, 6, 8)` 不变；`kernel.go` 内「25.6.8」0 命中；`TestR8B1ENCVersionGateBoundary` PASS |
| S-53 SS 单面集 | PASS | `kernel/credentials.go:627` 六名 {`chacha20`、`aes-128-gcm-siv`、`aes-256-gcm-siv`、`chacha8-ietf-poly1305`、`aes-128-ccm`、`aes-256-ccm`}、`:644` 八值逐字在位；`TestR8B2SS*SingleFace` 5 项 + `TestR6B1SSChacha20SingleFace` + `TestR7B1XrayChainSSSubsetGate` PASS（kernel 日志内） |

**哨兵小结：51 PASS + 2 本地不可判定·承继门证据（S-16、S-34；S-41 的 PG 子格同口径承继，行级不降格），0 回退，0 新回退。**

## 3. 轮换域 A9 全量抽审（K 域：前端页面与交互一致性）

### 3.0 变更面（相对 R14 基线 7244fbb 与 R21 基线 7d93792 的双基线差集）

- **相对 R21 基线 `7d93792..b823421`，K 域前端文件差集仅 3 个**：`api/types.ts`（4 行：connIdle -1 注释）、`pages/inbound/inbound-form-modal.tsx`（59 行：「不限」复选框 + validate -1/0 口径）、`pages/inbound/presets.ts`（3 行：toPayload -1/非 Xray 清 0）。其余 K 域文件 0 行。fix-r21 三项（F01/F02/F03）全为后端改动，无前端文件。
- **相对 R14 基线 `7244fbb..b823421`，K 域前端差集 15 个文件、300 增 / 88 删**（见 §3.1 各页分述）：change-password（去 20 字上限）、config（导入 autoBackup 下载 + 文案）、config/latency-card（删 `latencySummary`、改无测试点文案）、dashboard（重置日钳制）、group（五路失败点名）、inbound（probe null 守卫两处）、inbound-form-modal（「不限」+ connIdle 口径）、presets、limit（停用开关 + 文案）、node（A9-1 门）、tune-modal（warnings 全量 + 失败腿保实测带宽 + 回填口径统一）、panel-sharing（datetime-local 本地时区 + 节点加载失败 toast）、subscription（处理顺序 去重→前缀）、user（num「无限制」渲染 + 到期留空语义 + 清空=0 修复）、types（connIdle -1 注释）。**forward.tsx、tunnel.tsx、network.ts、session.ts、admin.tsx、App.tsx、globals.css 自 R14 起 0 行改动**——R14 的 §3.2/§3.3/§3.5 结论在本轮按现行树逐项重核在位，不继承结论本身。
- **任务书口径订正**：任务书称「R14-A9 报告为上轮本域基线」，实物核对无误——`r14/R14-A9-report.md`（42,586 字节）确为 K 域报告（R14 全面审计 main @ 7244fbb，A9-1…A9-5 五项 P3），与本轮基线引用一致，无需订正；但其范围只覆盖 node/tunnel/forward/inbound-form + api，本轮 A9 全量抽审覆盖全部 17 条路由（见 §3.1）。

### 3.1 路由与 API 路径全集核对

- **路由 17 条**（`App.tsx:187-317`，含 `/` 登录 + 16 页 + `*` 兜底回 `/dashboard`）：`/`、`/change-password`（skipLayout）、`/dashboard`、`/monitor`、`/my-subscription`、`/forward`、`/tunnel`、`/node`（NodeHub：节点/内核两 tab）、`/user`、`/group`、`/profile`、`/limit`（后三者同为 UserHub 的三个 tab）、`/inbound`、`/relay`（线路/隧道/规则/质量四 tab，隧道与规则 tab 直接嵌 TunnelPage/ForwardPage）、`/subscription`、`/config`、`/panel-sharing`。`ProtectedRoute` 只验登录态（`isLoggedIn()`），无路由级 admin 门；权限由三层补足：① 导航 `admin.tsx:425` 按 `getAdminFlag()` 过滤 `adminOnly`/`userOnly` 项；② `/config` 页内 `isAdmin()` 守卫（`config.tsx:452`，非 admin toast + 跳 `/dashboard`）；③ `/monitor` 管理员重定向回 `/node`（`monitor.tsx` 首行）；④ 后端 `requiresAdmin`（ADMIN 135）+ 403 全局处理（`network.ts:79-96`）。直接输 URL 的非管理员进管理页时各页均 toast 报错（非空白），见各页小节。
- **API 路径全集**：`api/index.ts` 导出 190 个调用点——189 个经 `Network.post/get`（含模板字面量 5 个监控类路径，归一化后对应 `/monitor/nodes/` 等前缀注册）+ `exportBackup` 直调 axios（`${baseURL}/backup/export`）；逐条对 `handler.go` 注册表核查，**缺失 1 条**：`generateCaptcha` → `/captcha/generate`（见 CAND-01；死导出，零调用点）。其余 189 条全部在册。另：`login`/`checkCaptcha`/`verifyCaptcha` 三者中仅前两者有调用点（`index.tsx:176` 调 `checkCaptcha`；`verifyCaptcha` 死导出但端点已注册，见 O-K-1）。
- **请求封装**（`api/network.ts` + `utils/session.ts`，自 R14 零改动）：裸 JWT（`Authorization: getToken()`，无 `Bearer`，`:129/:202`）、统一信封 `{code,msg,data,ts}`（code 0 成功；401 判 token 失效）、403 全局处理（信封 403 与 HTTP 403 双覆盖、3s 节流、silent 跳过、优先透出后端 msg）、silent 探测（`getSubInfo(false, true)` 等）、AbortController（诊断流式传信号，取消静默 resolve code=-1；`loadNodes`/`relay load` 序号守卫防旧响应覆盖）、幂等键（只透传、不逐请求生成；创建表单复用、成功轮换）、超时（默认 30s；诊断 120s；调优 40s；测算探测 120s；路由追踪 300s）。`session.ts:31-33` getToken 只读 localStorage token；角色标志以 token 为唯一事实来源（N-002 注释在位）。

### 3.2 三方对照表（前端字段 ↔ 后端字段 ↔ 文档描述）

> 记号：✓ 一致；△ 见立案/观察编号；— 该面无此字段。DOM 真实渲染均未验证（无 live browser），凡标「代码层」者均为源码 + 后端契约核对。

#### 3.2.1 登录页（index.tsx）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| `checkCaptcha` → data 0/1 决定是否出 Turnstile | `POST /captcha/check`（`handler.go:292`，`captchaEnabled()` 真回 1、假回 0）；`POST /captcha/verify`（`:293`） | `nodes.md:194` 网站配置 → 安全开 Turnstile，登录页出现人机验证 | ✓（`index.tsx:176-200`；未配 Site Key 时 toast「未配置 Cloudflare Site Key」） |
| `login({username,password,captchaId})` → `requirePasswordChange` 跳改密页 | `POST /user/login`（`handler.go:497`，回 `requirePasswordChange`，`:622`） | — | ✓（`index.tsx:123-155`；失败清 captchaId） |
| Passkey 登录（`getPasskeyStatus`/`beginPasskeyLogin`/`finishPasskeyLogin`） | `/user/passkey/login/begin` 等 | `passkey.md` | ✓（`index.tsx:58-75`） |
| 敏感字段 | 密码框 `type="password"`，不回显 | — | ✓ |

#### 3.2.2 改密页（change-password.tsx）+ 个人资料页（profile.tsx）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| `updatePassword({newUsername,currentPassword,newPassword,confirmPassword})` | `POST /user/updatePassword`（`handler.go:1769`：用户名非空、密码 ≥6 rune、无上限；两次密码一致；用户名唯一） | — | ✓（两页共用同一端点；前端用户名 ≥3、密码 ≥6，比后端严一格，无旁路；自 R14 起去 20 字上限，与后端「无上限」对齐） |
| profile 密码弹窗初值 `pwd: ""`，编辑不回显 | — | — | ✓（敏感字段不回显；保存时空 pwd 删除键，`user.tsx:750-751` 同口径） |
| profile 管理员菜单（面板共享/用户管理/网站配置）仅 admin | 导航级 admin 门 | — | ✓（`profile.tsx` 按 `getAdminFlag()` 渲染） |

#### 3.2.3 仪表盘（dashboard.tsx + admin-overview.tsx + use-dashboard-data.ts）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| 用户侧 `getUserPackageInfo`（`userInfo/tunnelPermissions/forwards/statisticsFlows`） | `POST /user/package`（`UserPackageInfoApiData`，`types.ts:240-263`；注释「后端不下发 maxConn，勿在此声明」） | — | ✓（消费字段与类型逐字一致；`userInfo.flow/inFlow/outFlow/num/expTime/flowResetTime` 均有来源） |
| `calculateForwardTotalFlow`（in+out raw 合计） | forward 行 raw in/out | — | ✓（S-47 PASS；注释逐字「此处合计仅为总流量展示，不是计费量」） |
| `formatResetTime` 重置日钳制（resetDay=31 的 2 月 → 28/29 日） | `monthlyResetMoment`（`repository.go:5076`，同口径钳制到当月最后一天） | — | ✓（`dashboard.tsx:373-398`；与后端 `at()` 的 `day > last → last` 逐字同算法） |
| 管理员侧 `getNodeList/getKernelList/getInboundList/getInboundRelays/getSubscriptions` | 各 list 端点 | — | ✓（`admin-overview.tsx:232-237`；加载失败置 `loadError`） |
| 公告（`getAnnouncement`） | `POST /announcement/get` | `notify.md:58` 网站配置 → 公告 | ✓ |

#### 3.2.4 监控页（monitor.tsx + node/monitor-view.tsx + node/tunnel-monitor-view.tsx）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| 管理员访问 `/monitor` 跳回 `/node`；普通用户凭授权看总览 | — | `monitoring.md:5`「管理员没有独立的监控菜单……直接访问 /monitor 会跳回节点页」；`:30` 普通用户需管理员在用户页授予监控权限 | ✓（`monitor.tsx` 首行 + `admin.tsx` 监控项 `userOnly` + `monitorAllowed` 门，`need_admin_grant` 提示在位） |
| `getMonitorNodes`（`MonitorNodeApiItem{id,inx,name,status,version,updatedTime}`） | `monitorNodeListHandler`（`monitoring.go:70-77`，同形六字段） | — | ✓ |
| WS 实时指标（`cpuUsage/netInSpeed/netOutSpeed/tcpConns/udpConns`，camelCase 与 snake_case 双读） | WS `SystemInfo`（`ws/server.go:185-200`，`cpu_usage/net_in_speed/tcp_conns/udp_conns` 等 snake_case；`type:"metric"`/`type:"status"`） | — | ✓（`monitor.tsx` 双键 fallback；status 载荷为数字 1/0） |
| `getMonitorAccess` → `canWrite`（`access.admin`）控制写入口 | `MonitorAccessApiData{allowed,admin,reason?}`（R2X-038） | — | ✓（只读用户不渲染写入口；后端仍以 403 兜底） |
| 30s 轮询 + 序号守卫 + 后台标签暂停 | — | — | ✓（`loadNodesSeqRef`；`document.hidden` 跳过） |

#### 3.2.5 我的订阅页（my-subscription.tsx）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| `getMySubscriptions`（`MySubscriptionItem{id,name,path,nodeCount,uploadBytes,downloadBytes,trafficLimit,trafficSince,expireTime,overQuota,enabled}`） | `POST /user-subscription/mine`（`userSubscriptionMine`，`user_subscription.go:116`，十一字段逐字回填） | `subscription.md:183/187/206` 流量统计与起算时间、分配后在「我的订阅」可见 | ✓（已用 = up+down；`trafficSince` 起算日渲染；过期/停用/超量三态 chip + 复制禁用 + 二维码失效提示） |
| `getMyInbounds`（`{userId,inbounds,relays,sig}`） | `POST /user-inbound/mine`（`user_inbound.go:213`，四键逐字回；inbound 行仅 id/name/protocol/nodeName） | — | ✓（前端仅消费 name/protocol/nodeName；`sig` 拼 `?user=&sig=`） |
| `getSubInfo(false, true)`（silent，403 不弹全局提示） | `POST /inbound/sub/info` | — | ✓（未分配默认订阅是正常态，只隐藏默认订阅区；零态「暂无订阅，请联系管理员」在位） |
| 四格式复制（通用/Clash/sing-box/Surge） | `?format=` 参数 | `subscription.md` 格式表 | ✓（复制按钮 + 二维码；无一键导入 scheme——一键导入在管理侧 subscription.tsx，见 §3.2.12） |

#### 3.2.6 转发页（forward.tsx；自 R14 零改动，R14 §3.3 逐项重核在位）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| `inPort`（1–65535 + 隧道端口范围；留空/0 自动分配） | `forwardCreate/Update`（`mutations.go:3495-3506`） | `forward.md:60-61` | ✓ |
| `inIp` 编辑保留、切隧道重置 | `forwardUpdate`（缺席且切隧道 → 重置；缺席且未切 → 保留） | N-03（`forward.md:62`） | ✓ |
| `speedId/ipSpeedId` 缺省自动清空 + ⚠️ 告知 | `normalizeSpeedLimitReference` | — | ✓ |
| `warnings` 回执逐条 ⚠️ 后报成功 | `forwardCreate/Update` 的 `data.warnings` | `forward.md:41-42` | ✓ |
| `enabled/status` 创建走后端缺省；批量删除/换隧道/暂停/恢复/重部署 | `/forward/batch-*` 五端点 | N-04 pending-delete | △ 见 CAND-02（pending-delete 可观测面）与 O-K-2（`getStatusDisplay` case 2「删除中」经 `forwardList` 不可达，因 `ListForwards` 恒过滤 status=2，`repository.go` R5-B3 注释在位） |
| 诊断流式 + 回退 REST | `POST /forward/diagnose` | — | ✓（流式传 Abort 信号；回退 REST 未传信号，R14 O-4 仍在，未达立案线） |
| 复制（规则名/地址） | `utils/clipboard.ts`（`execCommand` 回退） | — | ✓（K-18） |

#### 3.2.7 隧道页（tunnel.tsx；自 R14 零改动，R14 §3.2 逐项重核在位）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| `id/name/type/status`；编辑省略 flow/ratio | `tunnelCreate/Update`（`TunnelMutationPayload`） | `relay.md:7-9/14` | ✓（R13X-003 省略形态在位） |
| `inIp/ipPreference`、`inNodeId/outNodeId/chainNodes`、`engine` | 同上 | `relay.md:9` | ✓（`forwardMode` 死字段随 wire、后端忽略，R14 O-5 仍在） |
| `warnings` 回执 | `tunnelUpdate` 的 `data.warnings` | — | ✓（S-26 PASS） |
| 诊断流式 + 回退 | `POST /tunnel/diagnose`（120s 超时） | — | ✓（同 forward，R14 O-4） |
| `update-order` | `POST /tunnel/update-order` | — | ✓ |

#### 3.2.8 节点页（node.tsx + node/ 子目录；变更面见 §3.3）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| `latency`/`latencyTargets.points/pinned` | `nodeList` 贴 `latency`（`attachNodeLatency`）；`POST /node/latency/targets` | `nodes.md:138/142/148/156-158` | ✓（A9-1 已修，见 §3.3；`CARD_POINTS=3`；`maxPinnedLatencyTargets` 后端守卫） |
| 延迟分档颜色 | —（前端 `pointTone`） | `nodes.md:148-152` | ✓ |
| `latencyPoolEmpty`（只读一次、仅管理员、失败 null 不误报） | `POST /config/list` 的 `latency_targets` 键 | `nodes.md:142` | ✓ |
| 每次刷新是否清掉已有 latency 结果 | 后端 store 决定；`mergeNodeRealtimeState` 只保留四项实时态 | — | ✓（R14 结论仍成立：刷新不清结果；清结果的唯一路径是后端 store 丢弃） |
| `monthTraffic`（`{tzOffset}`） | `POST /node/traffic/month` | 「本月流量（网卡）」 | ✓ |
| 一键测算 `bw/rtt` + 回落行 + warnings | `POST /node/tune`（probe/apply） | 调优节（未描述一键测算，R14 O-3 仍在） | ✓（A9-2/A9-3 已修，见 §3.3；回填口径两腿已统一，R14 O-2 已修） |
| 回程路由 `route.points[].hops[]` | `POST /node/route`、`/node/route/probe` | `nodes.md:190-205` | ✓ |
| `remoteToken/remoteConfig` 收而不消费、不回显 | `ListNodes` 下发（ADMIN 路由） | — | △ R14 O-1 仍在（只登记；是否剥离归 A/H 域裁定） |
| Pending Delete 管理 | `POST /node/nftables/pending-deletes`（API） | `nodes.md:231`/`forward.md:67` | △ 见 CAND-02（无页面入口） |

#### 3.2.9 用户页（user.tsx；UserHub「用户」tab）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| `UserApiItem{id,user,name,roleId,status,flow,flowMiB,num,expTime,flowResetTime,maxConn,inFlow,outFlow,dailyQuotaGB,monthlyQuotaGB,...}` | `POST /user/list`（分页） | `relay.md:33-42` 用户分组与限速 | ✓（消费字段与类型一致） |
| `num <= 0 → 「无限制」`（用户卡片与 UT 行两处） | `flow_policy.go:276/:316`（`Num > 0` 才限流） | — | ✓（自 R14 新增渲染，与后端「≤0 不限」语义一致） |
| 到期 DatePicker 留空 = 无限期/永久；清空误写 `Date.now()` 已修为 0 | `userUpdate`/`userTunnel` 的 `expTime`（0=永久） | — | ✓（R14 A10-02 已修在位，`user.tsx` 编辑 UT 清空分支） |
| `maxConn` 变更 warnings 逐条 toast | `userUpdate` 回 `data.warnings` | — | ✓（S-20 PASS） |
| 密码留空不改（编辑删 `pwd` 键）；分组归属加载失败不发 `groupIds` | presence 语义 | — | ✓（`user.tsx:750-751/:744-747`） |
| 分组归属加载失败 toast「本次保存将不修改其分组」 | — | — | ✓（D5 同口径） |

#### 3.2.10 分组页（group.tsx；UserHub「分组」tab）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| `assignTunnelsToGroup({groupId,tunnelIds})` / `assignUsersToGroup({groupId,userIds})` / `assignGroupPermission({userGroupId,tunnelGroupId})` | `/group/tunnel/assign`、`/group/user/assign`、`/group/permission/assign`（`handler.go:413/418`） | — | ✓ |
| 五路加载失败点名（隧道分组/用户分组/分组权限/隧道列表/用户列表） | 五路 list 端点 | — | ✓（自 R14 新增；与 subscription.tsx D5-11 同口径，空选择器不再被误读为「确实没有」） |
| 分配 failures 部分失败报数 + 首条原因 | `data.failures`（信封 msg 恒「操作成功」） | — | ✓（S-20 PASS） |

#### 3.2.11 限速页（limit.tsx；UserHub「限速」tab）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| `SpeedLimitApiItem{id,name,speed,status,createdTime,updatedTime}` | `POST /speed-limit/list` | `relay.md:42` 限速规则（名称 + 速率） | ✓ |
| 停用开关（`status` 1/0）+ 文案「停用后规则不再生效，可随时重新启用」 | `POST /speed-limit/update`（`SpeedLimitMutationPayload{status?}`，`mutations.go:4548`） | — | ✓（自 R14 新增；保存恒发 `status`，`limit.tsx:228`） |
| 状态 chip「启用/停用」（原「运行/异常」） | — | — | △ O-K-3（停用 chip 仍为 danger 色，纯观感，不立案） |

#### 3.2.12 入站页（inbound.tsx + inbound-form-modal.tsx + presets.ts；变更面见 §3.3）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| `InboundApiItem` 全字段（含 `relayCount/probe/kernelStatus/meteringActive/connIdleSeconds/outboundId/outboundMissing`） | `inboundList/inboundView`（`kernel.go:176/695`，`relayCount` 由 `relayViews` 计数） | `nodes.md` 入站节 | ✓ |
| `connIdleSeconds`（Xray 专属；-1 不限 / 0 清除 / 1–86400；留空默认 300s） | `Normalize`（`model.go:130-146`：范围 -1、0..86400；0 接受为清除；-1 渲染 31536000）+ `inboundSave` 显式 0 = 清除 | `nodes.md` connIdle 指南 | ✓（A9-4/A9-5 已修，见 §3.3；前后端口径逐字一致） |
| `meteringEnabled` 开关块（禁用态 + 原因文案 + 已开启可关） | `ssMeteringEligible/ssMeteringShapeBlock` | — | ✓（S-50 PASS） |
| ENC「IX 中转专用协议」三面徽标 | `settings.encryptionAuth` | — | ✓（S-51 PASS；徽标全集三面对齐） |
| 外部入站 probe null 守卫（`item.probe && !skipped`） | `probe?: InboundProbe`（首轮探测未完成时 null） | — | ✓（自 R14 新增两处：`inbound.tsx:567/:1024`；R15-X-02/R16-X-01，旧式 `!probe?.skipped` 在 null 时原样赋 null 致 `null.ok` 崩页，已修） |
| 凭据留空自动生成；更新留空回填旧值；ACME Key/Secret `type="password"` 且编辑态不回显 | `Normalize` + R2X-030 | — | ✓（K-16） |
| 批量启用/停用/删除（多选 + 全选当前列表） | `/inbound/batch-*` | `nodes.md` 批量操作节 | ✓（`inbound.tsx:1136` 多选在位） |

#### 3.2.13 中转页（relay.tsx；线路/隧道/规则/质量四 tab）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| 线路视图按落地分组（`getInboundList` + `getInboundRelays(0)`，0 = 全部） | `inboundRelayList`（`kernel_relay.go:103`，`relayViews(inboundID)`）+ `relayView{forwardExists,forwardStatus,entries,latency}` | `relay.md:3`「中转页的线路标签按落地列出所有中转线路，点一个落地 → 选择隧道即可添加（也可以在落地页点入站 → 中转）」 | ✓（`relay.tsx:130-160` 序号守卫 + `latestProbe` 单调保护；空态「还没有落地，先在落地里搭建或导入节点」在位；落地页入站 → 中转入口见 inbound.tsx:770/992/1305） |
| 中转延迟测速（`probeInboundRelays`，空 ids = 全部） | `POST /inbound/relay/probe` | — | ✓（结果按键合并回列表；`forwardExists` 假时不渲染测速） |
| 隧道/规则 tab 嵌 TunnelPage/ForwardPage；质量 tab 嵌 TunnelMonitorView | 同隧道/转发页 | `relay.md:3` | ✓ |
| ENC 徽标两视图在位 | `settings.encryptionAuth` | — | ✓（`relay.tsx:229/:327`） |

#### 3.2.14 订阅页（subscription.tsx + subscription/ 子目录）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| `SubscriptionItem` 全字段（含 `isDefault/relayIds/resetDay/trafficSince/overQuota`） | `subscriptionView`（`subscription.go:752-781`，同形 20 键；`relayIds` 与 `options.relayIds` 同源双发） | `subscription.md` | ✓（`relayIds` 回显 `s.relayIds ?? s.options?.relayIds ?? []`，`:347`） |
| 处理管线顺序「过滤 → 改名 → 国旗 → 去重 → 前缀 → 排序」 | 后端管线执行序 | — | ✓（自 R14 修正：summary 与 hint 均改为去重在前缀前，与执行序一致） |
| `resetDay` 下拉 0/1–28 | `subscription.go:991`（`ResetDay > 28` 拒）+ `model/subscription.go:46`（1-28） | — | ✓（前端下拉 `Array.from({length:28})`，与后端门逐字一致） |
| 一键导入 7 客户端 + 通用复制（`SUB_CLIENTS`，`clients.ts`） | `?format=`（clash/singbox/shadowrocket/loon/qx/surge/stash/base64） | `getting-started.md:37`「订阅页里默认有一个『我的订阅』，点开它选客户端一键导入」 | ✓（`subscription.tsx:1557` 逐客户端渲染；默认订阅实为排序第一条、测试机实测名为「我的订阅」`isDefault=true`，见 §3.22；`subscription.md:37` 的 ENC/Snell/NaïveProxy 各格式下发口径与 `clients.ts` 的 `configImportURL` 分流一致） |
| 分配加载失败禁用保存（全量替换接口下空集 = 全清） | `/user-subscription/set`（全量替换） | — | ✓（`assign.tsx:41/86/126/187/260` 五处；D5 同口径） |
| 流量统计（小时/日/月）+ 起算时间 | `/subscription/traffic` | `subscription.md:183/187` | ✓ |

#### 3.2.15 配置页（config.tsx + config/ 子目录）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| 导入摘要（`*Imported` 求和 + `userSubscriptionsSkipped` + `skippedRows` + `importWarnings` 计数）+ 导入前自动备份下载按钮 | `ImportResult`（`model.go:957-1012`：22 个 `*Imported` 计数 + `AutoBackup` + `SkippedRows` + `ImportWarnings`） | — | ✓（自 R14 新增 A10-10：不再把整份回执（含 autoBackup 全量库）倾倒进 toast；`autoBackup == null` 时按钮不出现，与后端「生成失败只记警告继续导入」一致；确认框文案改为「导入成功后可在此页下载该自动备份」） |
| Gist 备份（token/password/interval/版本恢复；`tokenHint` 只露首尾 4 位；`encrypted` 只表示已设密码） | `/backup/gist/status|save|upload|versions` + `/backup/gist/restore`（`gist_backup.go:435/455`，令牌/密码绝不进响应） | `gist-backup.md:8`「网站配置 → 数据备份与恢复 → Gist 备份（位于本地导入区下方）」 | ✓（`config.tsx:2157` 数据备份与恢复区内：导出 → 导入 → 自动备份下载 → `<GistBackup />`（`:2244`），顺序与文档一致；8 个 Gist 配置键不进本地备份，见 `gist_backup.go`） |
| 延迟测试点（`latency_targets`，最多 30 点、卡片固定 3 点） | `POST /config/list` + `POST /config/get` | `nodes.md:154`「网站配置 → 延迟测试」 | ✓（`config.tsx:2043` 延迟测试点卡；无测试点文案已改为「不设置时节点不测延迟（节点页延迟区为空）；入站连通性仍由面板服务器探测」，与 A9-1 后的渲染一致） |
| 公告（加载失败禁保存，防空内容覆盖线上公告 D5-02） | `/announcement/get|update` | `notify.md:58` | ✓ |
| 通知「发送测试」（`testNotify`） | `POST /notify/test` | `notify.md:3` | ✓（`notify-card.tsx:270`） |
| 非 admin 重定向 | — | — | ✓（`config.tsx:452`） |

#### 3.2.16 面板共享页（panel-sharing.tsx）

| 前端字段/行为 | 后端字段/端点 | 文档描述 | 结论 |
|---|---|---|---|
| 分享创建（`nodeId` 仅本地节点、`expiryTime` ≥0 整数天、流量上限 0=不限） | `federation.go`（`ExpiryTime < 0` 拒，`:565/:755`） | `relay.md` 联邦节 | ✓（`panel-sharing.tsx:304-330` 三处校验与后端门一致） |
| `datetime-local` 回显用本地分量拼串（非 `toISOString()`） | `expiryTime` 毫秒 | — | ✓（自 R14 新增 A10-11：`toLocalDatetimeInputValue`，UTC 墙钟在非 UTC 时区差一个偏移，已修） |
| 节点列表加载失败 toast（原静默 `catch {}`） | `POST /node/list` | — | ✓（自 R14 新增：`panel-sharing.tsx:244-251`） |
| 远程占用端口（`getPeerRemoteUsageList`） | `/federation/share/remote-usage/list` | — | ✓ |

### 3.3 自 1.4.4 以来变更面深审（Gist、调优、一键测算、入站空闲超时及「不限」、延迟检测、connIdle 渲染）

| 变更面 | 复验 | 结论 |
|---|---|---|
| Gist 备份（1.4.6） | 状态/保存/上传/版本四端点 + tokenHint 掩码 + 密码不进响应 + 8 键不进本地备份 + 位于导入区下方 | ✓（§3.2.15） |
| 服务器调优（1.4.7） | 一键测算 `probe/apply` 分离、`probeAdoptable`、`adoptProbe` 只填框不下发、迟到响应守卫 `probeRunRef` | ✓（`tune-modal.tsx:108/180-208/227/259`） |
| 一键测算修复（1.4.8 + 1.4.9） | RTT 偏移/中位数实测（后端 `tuning_probe.go` 各腿独立回落） | ✓（前端成功/回落双分支均渲染 `warnings.map`，见 A9-2） |
| 入站空闲超时及「不限」（1.4.9，88f10db） | -1 哨兵 + 渲染 365 天 + 超时框旁勾选 + sing-box 不可用说明 + 切内核自动清空 + `changeKernel` | ✓（`inbound-form-modal.tsx:106-136/177-197/288-296/1180-1200`；后端 `model.go:130-146` 口径逐字一致；三项新测试全 PASS：`TestConnIdleUnlimitedNormalize/RendersOneYear/SharesLevels`（kernel）+ `TestConnIdleUnlimitedPersistsAndRenders/CompanionSyncSwitches`（handler）） |
| 延迟检测（1.4.8 + A9-1 修复） | 显式空选药丸、池空提示、LatencyCell 三分支 + 卡片/列表双视图 | ✓（A9-1 已修；见 §3.4） |
| connIdle 渲染 | 列表/详情/编辑三面 + 伴生入站同步 | ✓（`TestConnIdleEditPersistsAndRenders`、`TestConnIdleCompanionSyncMirrorsOrigin` 同批 PASS） |
| 邻接面：`forward.tsx`/`tunnel.tsx` | 自 R14 零改动；R14 三方表逐项重核在位 | ✓（惟 CAND-02 文档面与 O-K-2 死分支） |
| 邻接面：`node.tsx` 刷新语义 | `mergeNodeRealtimeState` 未动；刷新不清结果结论仍成立 | ✓ |

### 3.4 R14 A9-1…A9-5 关闭复验（代码层逐项重核，不继承结论）

| 编号 | 本轮复验（b823421 实测） | 结论 |
|---|---|---|
| A9-1 卡片视图显式空选药丸不可达 | 卡片外层门已补显式空选一项：`node.tsx:2340-2342` `{node.latency \|\| latencyPoolEmpty \|\| node.latencyTargets?.points.length === 0 ? renderLatency(node, "card") : null}`；`renderLatency` 首分支（`:1410`）仍为「不测延迟」药丸（点击开测试点设置）；列表 `:2534` 直接调用不变；池空分支（`:1422`）与门条件同义，不受影响 | ✅ 关闭（代码层；DOM 腿未验证） |
| A9-2 测算成功分支丢 warnings、回落分支只取 `[0]` | 成功分支与回落分支均改为 `probeSuggestion.warnings?.map` 全量渲染（`tune-modal.tsx:458/:475`）；`warnings[0]` 全仓 0 命中；`run("apply")` 后仍全量 toast（`:193`） | ✅ 关闭（代码层；DOM 腿未验证） |
| A9-3 RTT 腿失败丢弃已实测带宽 | 失败分支按腿拆分：`measuredBw != null ? clampLine(measuredBw) : FALLBACK`（`tune-modal.tsx:235-256`）；回落行标注「（实测值）」/「（默认值）」（`:448-451`）；`probeError` 行同步写明「延迟回落默认值 150 ms，带宽采用实测值 X Mbps」（`:440-447`） | ✅ 关闭（代码层；DOM 腿未验证） |
| A9-4 connIdle 注释称 0 不合法，与后端及自家 payload/类型三方矛盾 | 注释已订正：`inbound-form-modal.tsx:288-289`「-1 是『不限』哨兵值（后端 Normalize 接受，渲染为 365 天）；0 是合法的清除值（后端 Normalize 接受 0=清除），前后端同一口径（148-P3-1）」；validate 接受 -1/0/1–86400（`:291-296`）；payload（`presets.ts:601-610`）与类型（`types.ts:939/974`）口径一致；后端 `model.go:130-146` 注释亦已对齐 | ✅ 关闭（代码层） |
| A9-5 存量非 Xray 入站带 connIdle 值时编辑弹窗无清除路径 | validate 不再拒绝非 Xray 存量值（`inbound-form-modal.tsx:290`「非 Xray 入站的存量值不在此拒绝：保存时自动清 0（见 toPayload，148-P3-2）」）；`presets.ts:606-608` 非 Xray 时 `connIdleSeconds` 恒发 0；输入框仍禁用（`:1188`，`connIdleUnlimited` 时亦禁用数字框）但保存不再被挡 | ✅ 关闭（代码层；DOM 腿未验证） |

### 3.5 K 域 checklist K-01…K-18（本轮全量在 scope 内打勾；R14 的 K-04/K-06/K-07 不适用项本轮已补）

- [x] K-01 17 条路由均可访问且权限正确——见 §3.1；API 190 调用点逐条对注册表核查，缺失 1 条（CAND-01 死导出）；`node/list` 等管理端点落 `requiresAdmin`；导航 + 页内守卫 + 后端三层在位。
- [x] K-02 `forward.tsx` 字段契约与后端一致——见 §3.2.6；`inPort`/`inIp`/`speedId`/`warnings` 逐项对账通过（惟 CAND-02 文档面）。
- [x] K-03 `tunnel.tsx` 字段契约一致——见 §3.2.7；R13X-003 省略形态在位。
- [x] K-04 `user.tsx` / `group.tsx` 字段契约一致——见 §3.2.9/§3.2.10；`num`「无限制」与后端 `Num > 0` 门一致；到期清空修为 0；在位。
- [x] K-05 `node.tsx` 与节点卡片/列表/监控视图一致——见 §3.2.8；A9-1 已修，双视图一致。
- [x] K-06 `config.tsx` 各配置项与后端 key 一致——见 §3.2.15；`latency_targets`/`captcha_enabled`/`monitor_tunnel_quality_enabled` 等键逐项在位；导入摘要字段与 `ImportResult` 全集一致。
- [x] K-07 `subscription.tsx` 处理/链式/规则/脚本字段完整——见 §3.2.14；管线顺序、resetDay 1–28、relayIds 双发、`SUB_CLIENTS` 八客户端在位。
- [x] K-08 裸 JWT、统一信封、403 全局处理——见 §3.1；全通过。
- [x] K-09 `silent` 探测请求不弹全局提示——`network.ts:54-55/:90` `silent` 短路 + `getSubInfo(false, true)` 等监控类/探测类接口在位。
- [x] K-10 AbortController 取消与旧响应覆盖防护——诊断流式传信号、`loadNodes`/`relay load`/`monitor loadNodes` 三处序号守卫在位（回退 REST 腿未传信号，R14 O-4 仍在，未达立案线）。
- [x] K-11 表单边界与后端校验对齐——`forward inPort`、`inbound port/connIdle(-1/0/1–86400)`、`subscription resetDay(1–28)`、`change-password/profile` 用户名 ≥3/密码 ≥6、panel-sharing `expiryTime ≥ 0` 逐项对齐；惟 CAND-01 一项死导出。
- [x] K-12 限速缺省自动清空、bindIp 保留——限速缺省清空 + ⚠️ 告知在位；`inIp` 保留/切隧道重置与后端 `forwardUpdate` 同口径。
- [x] K-13 `enabled` 创建、部分更新、配置清空——入站 `enabled` 恒发（`presets.ts:600`）；转发创建走后端缺省 status=1；connIdle 留空发 0 清除、非 Xray 保存自动清 0；延迟测试点 `custom=false` 清除；订阅 resetDay 0=不重置；用户到期留空=无限期。
- [x] K-14 只从 `src/shadcn-bridge/heroui/*` 导入——全仓 grep `@heroui/*`/`@nextui-org/*` **0 命中**；bridge 本身为 `@/components/ui` 之上的 facade（`button.tsx` 为证）。
- [x] K-15 `globals.css` import `tailwind-theme.pcss`——`vite-frontend/src/styles/globals.css:2` 在位。
- [x] K-16 敏感字段回显掩码——密码框 `type="password"`（profile/改密/user 表单）；ACME DNS Key/Secret `type="password"` 且编辑态不回显存量；Gist `tokenHint` 只露首尾 4 位、密码不进响应；`remoteToken` 不回显（R14 O-1）；登录密码不落日志（本轮腿内 token 脱敏，见 §4.3）。
- [x] K-17 窄屏关键操作可达——批量栏 `overflow-x-auto touch-pan-x`（forward/tunnel）；操作按钮 `min-h-8`/`flex-1`（user）；弹窗 `!w-[calc(100%-32px)]`（subscription/inbound/user/tune-modal）；视图切换在位。DOM 实点未验证（无 live browser）。
- [x] K-18 复制链接在 HTTP 下的降级提示——`utils/clipboard.ts:27` `execCommand` 回退在位（`navigator.clipboard` 不可用时）；`forward.tsx`/`my-subscription.tsx` 经 `copyWithToast`/`copyToClipboard` 调用。
- [x] N-35 销账对账探针对象本身——哨兵 53 行均按对象本身（函数/端点/回执字段）复验，未以同域其他探针顶替；S-52 行号以实测 :2008/:2033/:2119 为准，表载 :2007/:2118 为近似值（门体逐字未动）；A9-1…A9-5 按原立案对象逐项重核代码现况。
- [x] N-36 援引的非项/关闭登记前提未被后续证据推翻——本轮援引的关闭登记（S-20/S-26/S-50/S-51、R13X-003 省略形态、R14 A9-1…A9-5 的修复）均按现行树重验在位；无前提失效项。
- [x] N-37 探针证据存档——全部结论指向文件:行号 + 落盘原始日志（§6）；未引用任何批报告摘要计数作裁定依据；具名核对以 `R22-L3-sentinel-check.log` + 补记为准。

### 3.6 文档对照（doc/guide/*.md 的「操作路径」描述，14 项）

| 文档宣称 | 核对 | 结论 |
|---|---|---|
| `getting-started.md:37` 订阅页默认有「我的订阅」，点开选客户端一键导入（7 客户端） | `subscription.tsx:1557` `SUB_CLIENTS` 八格在位；测试机实测默认订阅名即「我的订阅」（`isDefault=true`） | ✓ |
| `gist-backup.md:8` 网站配置 → 数据备份与恢复 → Gist 备份（位于本地导入区下方） | `config.tsx:2157` 区内顺序：导出 → 导入 → 自动备份下载 → `<GistBackup />`（`:2244`） | ✓ |
| `gist-backup.md:22` tokenHint 只露首尾 4 位、密码不进响应 | `gist_backup.go:435/445/455` | ✓ |
| `monitoring.md:5/30` 管理员无独立监控菜单、`/monitor` 跳回节点页；普通用户需授权 | `monitor.tsx` 重定向 + `admin.tsx` `userOnly` + `monitorAllowed` 门 | ✓ |
| `nodes.md:148/154/156-158` 卡片与列表均有延迟显示；网站配置 → 延迟测试（最多 30 点、卡片固定 3 点）；显式空选 = 不测 | A9-1 已修；`CARD_POINTS=3`；`latency-card.tsx` 文案已同步 | ✓ |
| `nodes.md:231-246` Pending Delete 管理接口（`POST /node/nftables/pending-deletes`；pending 行不进转发列表，只能经该接口查看） | 端点在册（`handler.go:324`）；`ListForwards` 恒过滤 status=2（`repository.go`） | ✓（API 面）；但 `forward.md:67` 称「节点页的…管理接口」与此矛盾，见 CAND-02 |
| `forward.md:60-62` inPort 1–65535 + 节点端口范围；入站端口不受范围约束；inIp 编辑保留、切隧道重置 | `forward.tsx:2166-2180/:2393-2411` + 后端 `mutations.go:3495-3506` | ✓ |
| `forward.md:67` nftables 删除 pending-delete 退避 1m/5m/30m/2h、上限 24h；「管理员可通过节点页的 Pending Delete 管理接口查看待清理项」 | 退避与上限与后端一致；**「节点页的管理接口」不存在**——无页面入口、无 api 封装 | △ **CAND-02** |
| `forward.md:41-42` realm 引擎不支持转发级限速/连接数（警告并忽略） | `forward.tsx:2476-2490` warnings 逐条消费 | ✓ |
| `relay.md:3` 中转页四标签 + 落地页入站 → 中转 | `relay.tsx` 四 tab + `inbound.tsx:770/992/1305` | ✓ |
| `relay.md:37/42` 用户页 → 分组标签；用户页 → 限速标签 | UserHub 三 tab（`/user`、`/group`、`/limit`） | ✓ |
| `subscription.md:37` ENC 只在 Clash/Stash/通用 base64 携带完整 encryption；Snell/NaïveProxy 按客户端区分 | `clients.ts` + 后端渲染守卫（S-38/S-43） | ✓ |
| `notify.md:3/58` 网站配置 → 通知，发送测试；网站配置 → 公告 | `notify-card.tsx:270` + `config.tsx:2078` 公告管理 | ✓ |
| `nodes.md:194` 网站配置 → 安全开 Turnstile | `config.tsx:202` `captcha_enabled` + `index.tsx:176-200` | ✓ |

## 4. 立案与观察

### 4.1 CAND-01（P3·产品）——`generateCaptcha` 死导出指向未注册端点

- **对象**：`vite-frontend/src/api/index.ts:382`：`generateCaptcha: () => Network.post('/captcha/generate')`。
- **证据**：① 后端无此路由——`handler.go` 仅注册 `POST /captcha/check`（`:292`）与 `POST /captcha/verify`（`:293`）；`grep -rn captcha/generate go-backend` 0 命中；② 零调用点——`grep -rn generateCaptcha vite-frontend/src scripts/` 仅定义本身；③ 历史口径——自 v1.0.0（`04f333c`）即已存在导出，后端从未有对应路由；④ 全集核对——190 个调用点中唯一未映射者（见 §3.1）。
- **影响**：当前为零（死代码，无调用方；调用时将得 404）。`checkCaptcha` 与 `verifyCaptcha` 的调用点均不存在，但后两者端点已注册，故不立案。
- **建议**：删除 `api/index.ts:382` 的 `generateCaptcha` 导出（纯前端死代码清理，BC 无）。
- **等级**：P3。

### 4.2 CAND-02（P3·文档）——`forward.md:67` 宣称的「节点页的 Pending Delete 管理接口」不存在

- **对象**：`doc/guide/forward.md:67`（`forward.md` 尾部 nftables 删除 pending-delete 小节）。
- **证据**：① 前端无任何 pending-delete 界面——`grep -rn pending-delete vite-frontend/src` 0 命中；`node.tsx` 无此 UI；② 前端无 api 封装——`api/index.ts` 无 `pending-deletes` 调用；③ 唯一真实接口是原始 API `POST /api/v1/node/nftables/pending-deletes`（`handler.go:324`）；④ 正确文档在位——`nodes.md:231-246` 明言 pending 行「只能通过上面的 pending-deletes 接口查看」，与 `forward.md:67` 的「节点页的…管理接口」直接矛盾；⑤ 批量删除后的瞬态结果弹窗（`forward/batch-actions.ts:122`）只在删除当次展示 pending 数量，无持久管理视图。
- **影响**：管理员按文档在节点页找管理入口会找不到（须改用原始 API 调用）；无数据面风险（`ListForwards` 恒过滤 status=2，pending 行不进列表）。
- **建议**：改 `forward.md:67` 为「通过 `POST /api/v1/node/nftables/pending-deletes` 查看」（或另立案做页面入口，属产品决策）。
- **等级**：P3（文档）。

### 4.3 观察项（不立案）

- **O-K-1**：`verifyCaptcha`（`api/index.ts:383`）亦为死导出（零调用点），但其端点 `/captcha/verify` 已注册（`handler.go:293`），无 404 风险；与 `generateCaptcha` 同批清理即可，不立案。
- **O-K-2**：`forward.tsx` `getStatusDisplay` case 2（「删除中」）经 `forwardList` 不可达——`ListForwards` 恒过滤 status=2（`repository.go` R5-B3 注释在位），pending 行在列表中永不出现；纯防御分支，不立案（见 CAND-02 关联）。
- **O-K-3**：`limit.tsx` 停用 chip 仍为 danger 色（`:193`，「停用」文本与 `chip` 配色不一致），纯观感，不立案。
- **O-K-4**：本轮首版 handler/repo 套件在 13:43 与 13:39 先后中断（见 §1），疑似五 lane 并行下的 OOM/误杀（未证实）；重跑全绿后无遗留问题，不立案。
- **O-K-5（安全·须跟进）**：计数腿中一条调试命令因 shell 引号断裂，把面板密码原值回显进一次 exec 调用的 stderr（仅本会话的工具输出可见）；已核查——**该值未写入任何日志或报告文件**（`R22-L3-counts.log`、`R22-L3-served-chunk.log` 均已 `grep -c` 核查为 0 命中；本报告亦不含该值、JWT 与订阅 token）。**建议轮换测试机面板密码**（凭据在 `~/workspace/w10-run/panel-cred-new.txt`，600）。本轮登录失败的前两次尝试系引号问题，非凭据错误。

## 5. 未验证清单（本路能力边界）

- **浏览器 DOM 腿**：无 live browser——全部「代码层」结论（S-20/S-26/S-47/S-50/S-51、A9-1…A9-5、K-17 移动端可达性、Gist/调优/「不限」交互）均为源码 + 后端契约核对，未经真实浏览器鼠标实点验证；DOM 渲染腿请 L5 汇总时另行安排。
- **S-16 导入腿 / S-34 双契约 / S-41 PG 子格**：本地不可判定，承继门证据（见 §2 表内行），本轮未改写。
- **测试机部署腿**：本轮未部署、未重启（只读核对）；served chunk md5 与计数已核对，面板运行态（Xray 26.10.21 running/synced、VLESS smoke）沿用基线 `R22-00-baseline.md` 的已验证状态，未在本轮重跑。
- **Alpine/OpenRC 容器套件**：未跑（本轮未覆盖，与 R21 同口径）。
- **demo 形状漂移/ fixtures 内容闸**：在快照内实跑 exit 0，但未落盘日志文件（命令可重跑：`node scripts/demo/check-shape-drift.mjs`、`node scripts/demo/check-fixtures-content.mjs`）。

## 6. 证据总账

| 文件（均在 `~/workspace/flvx-max/audit-out-132/r22/`） | 内容 |
|---|---|
| `R22-L3-handler-full.log`（331.305s，exit 0） | handler 全量 1355 PASS / 0 FAIL / 4 SKIP |
| `R22-L3-kernel-full.log`（1.840s） | kernel 全量 436 PASS / 0 FAIL / 0 SKIP |
| `R22-L3-repo-full.log`（155.410s） | repo 全量 267 PASS / 0 FAIL / 0 SKIP |
| `R22-L3-netguard.log`（0.015s） | netguard 8 PASS |
| `R22-L3-contract-pg.log` | PG 契约 243 PASS / 9 SKIP（无 DSN） |
| `R22-L3-gost.log` | go-gost root lifecycle ok |
| `R22-L3-gost-x.log` | go-gost/x 全包 ok |
| `R22-L3-install-proxy.log` + `R22-L3-install-proxy-xtrace.log` | install-proxy 套件 exit 0；-x 轨迹含 5 个哨兵 shell 测试格调用 |
| `R22-L3-health-probe.log` | `TestValidateMonitorTCPTarget` 16/17（公网域名放行环境红，与 R21 同签名） |
| `R22-L3-frontend-build.log` | tsc + vite exit 0；dist index md5 `57ab941e…` |
| `R22-L3-sentinel-check.log` | 哨兵具名核对脚本输出（Go 具名 68 PASS / 4 SKIP / 0 FAIL / 0 MISSING；S-41 补核在附录） |
| `R22-L3-served-chunk.log` | 测试机 served index/JS/CSS 三项 md5 = 本地 dist；paneld md5 `1ca4572b…` = 基线；`/flow/test` 200 |
| `R22-L3-counts.log`（已脱敏） | 登录后计数：node=1、tunnel=0、forward=0、inbound=1、subscription=1（默认订阅 `isDefault=true`）；pristine 1/0/0/1/1 |

- 报告文件本身：`~/workspace/flvx-max/audit-out-132/r22/R22-L3-sentinel-k.md`，字节数见尾行自证。
- 交付时 `git -C ~/workspace/.r22-tree status` 仍空、HEAD 仍为 b823421；快照内无残留探针测试文件。
- **凭据说明**：本报告与全部日志均不含面板密码、JWT 与订阅 token（§4.3 O-K-5 另见轮换建议）。

— R22-L3-sentinel-k END · 正文 60787 字节（不含本行） · 哨兵 51 PASS + 2 承继 + 0 回退 · K 域新立案 CAND-01/CAND-02（P3×2） · END
