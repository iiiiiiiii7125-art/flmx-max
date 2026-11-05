# FLMX flmx-max 全量审计终版报告（AUDIT-REPORT）

- 对象：flmx-max（FLMX 转发＋节点面板），基线 **1.9.9 @ `00762abaee3a28f4ebf982dab256f3b784e1f0ae`**（= main = tag 1.9.9）。
- 修复分支：`fix/full-audit-20261105`（自基线出，**未 push、未打 tag、未发版**，停在本地待用户验收）。
- 本报告提交前的分支尖端：`8c5d56dd`；基线后共 **221 笔提交**，本报告为第 222 笔（提交清单见 §7，含本报告笔的说明）。
- 口径：判定以换位复审（R2）与非作者红证复验（VERIFY-R2）、终尖端收割（HARVEST）的实证为准；裁定以 CONSOLIDATED-DRAFT §9/§11/§12/§13/§14/§15/§16/§17 为准。引注更正一处：提交 `2c546c5c` 的作者路是 **FIX-R2-4**（FIX-R2-3 报告误写 FIX-R2-2，VERIFY-R2 §4 已点名），本报告按实况著录。

---

## 1. 总账

### 1.1 轮次与规模

| 阶段 | 内容 | 规模 |
|---|---|---|
| 基线钉定 | 1.9.9 @ 00762aba；仓库约 969 个 Go 文件、约 201 个前端 TS/TSX、plans/ 77 文件 | 主工作树在其他分支，全程以 `git show/grep/ls-tree` 钉版取证 |
| 基线特征化 | BL-GO / BL-AGENT / BL-FE-SH 三路基线测试账＋LIVE-BASE 真机基线 | handler 包基线红为默认 10m 超时形态（2829 pass／断言失败 0）；PG 契约基线未跑（无 DSN）已明示 |
| 第一轮审计（R1） | 24 分区（A1–A4、B1–B9、C1–C4、D1–D3、E1–E2、F1–F2）只读并行，每路七维（功能正确性/安全/并发一致性/数据安全/兼容性/健壮性/测试真实性） | **143 条次 → 去重 133 根**（同根合并 10） |
| 交叉核销 | XC-1..XC-4 对主聊 1.9.10 集成支 `integrate-1910-r1 @ dbcd501a` 逐条核对（该支不在基线内） | FX 32 提交行为在位 32/32、回归覆盖 29/32；点名 A2-02 等「声称已修实未修」项 |
| 真机基线批 | LIVE-A（沙箱脚本面 5/5）、LIVE-B（测试机，含 INC-01 事故与同轮恢复）、LIVE-C（scratch SQLite 面板 5 景）、LIVE-D（scratch PG 面板 5/5） | 两 P1（B7-01、F2-01）真机坐实；新增根 **T-INC-01（P2）** |
| 修复批 | FIX-0 cherry-pick 已验 FX 32 提交（35 笔，零冲突）＋14 条修复路（FIX-S1/S2/S2B、FIX-B1/B2/B2B/B3/B5/B6/B7/B7B、FIX-G1/G2/G3、FIX-CI/FE/DOC），一根一提交 | MERGE-3 合流尖端 `6f39a58b`，零文本冲突 |
| 门禁修复批 | GATE-FIX（A–G 8 笔）、GATE-DOC、GATE-FIX2（XFF 旧钉迁移 4 笔）、GATE-FIX3（套件夹具 4 笔） | 尖端 `a2cb5977`；GATE-FIX C 组查实 7 条 repo 红为 FIX-B5 两处过修并纠正 |
| 修复后真机批 | LIVE2-PANEL-A 6/6、LIVE2-PANEL-B 5/5、LIVE2-AGENT 五组双树对照、LIVE2-SCRIPT 容器隔离全套 | 全 PASS（细节见各条关账证据列） |
| 换位复审（R2） | 14 路换位（无人复审自家分区）、全仓非仅 diff、功能链骨架＋异法重推差集 | 新立案：P2 ×4、P3 ×17、信息级 ×1、账目类 ×3（§13 处置）＋§14/§15/§16 三组升报 |
| 复修批 | FIX-R2-1..FIX-R2-10（23 笔，含 FIX-R2-8 的纯文档笔） | 尖端 `8c5d56dd` |
| 复验＋收割 | VERIFY-R2 非作者红证复验（回退实验只在 scratch 克隆）；HARVEST 按 PV-14 在终尖端实跑收割 | 红证全过；PV-1..PV-14 全数关闭 |
| 终版报告 | 本文件（REPORT-FINAL） | — |

### 1.2 严重度总表（基线 → 终局）

| 级别 | 基线（条次） | 基线（根） | 终局关账 |
|---|---|---|---|
| P0 | 0 | 0 | 0 |
| P1 | 11 | 11 | **11/11 已修，复审＋红证＋真机三重证，零残留** |
| P2 | 53 | ≈48 | 47 已修；1 根（R-WS-TOKEN）缓解已落、根治转需人工决策（§3-1） |
| P3 | 79 | ≈74 | 已修/文档关闭为绝大多数；登记遗留 8 项（§4）、待决策 1 根（R-MD5，§3-4）、书面豁免 1 根（C2-03，§3-3） |
| 新增 T-INC-01 | — | P2 ×1 | 已修（套件宿主安全门＋隔离纪律） |
| 复审新立案 | — | P2 ×4 / P3 ×17 / 信息 ×1 | P2 4/4 已修（红证成立）；P3 除 R2-03-02/03 登记注记外全修；锚分层四面（BC 级）已执行待验收确认（§3-5） |

**退出判据达成情况**：复审零 P0、零 P1；P2 全部已修或书面转决策；终尖端全门禁绿（§5）。

---

## 2. 逐条关账表

判定词：**已修**＝修复提交在分支＋回归/真机证据在位；**文档关闭**＝按裁定以文档定稿关闭；**登记遗留**＝点名入 §4；**待决策**＝入 §3。提交号均为短哈希，全量清单见 §7。

### 2.1 A 簇（脚本与 CI）

| 根项 | 级别 | 判定 | 修复提交 | 回归测试 / 证据 |
|---|---|---|---|---|
| A1-01 | P2 | 已修 | 6c563b1a | 套件 `test_panel_download_routed_total_budget` |
| A1-02 | P2 | 已修 | b3b3b504 | 套件 `test_panel_installer_script_curl_uses_timeouts` |
| A1-03 | P3 | 已修 | b7894d9d（+0e680ebb、d5f1682b 两笔文法调整） | 套件 `test_panel_explicit_version_format_validated` |
| A1-04 | P3 | 已修 | fa846582 | 套件 `test_panel_probe_route_uses_channel_tag_grammar` |
| A1-05 | P3 | 已修 | ce52277d | 套件 `test_panel_normalize_proxy_prefix_strips_all_trailing_slashes` |
| A1-06 | P3 | 已修 | 9a3ba1da | 套件 `test_panel_docker_ipv6_uses_mktemp_not_predictable_tmp`（静态形态断言，修复报告已披露口径） |
| A2-01 | P1 | 已修 | eb6b9a66＋f3d536b3（PORT 主体）＋a7ed1eb3（残面：探针三态） | 套件事故门三例；VERIFY 同族形态由 R2-14 红证 #2（A2-01 残面）强红成立 |
| A2-02 | P1 | 已修 | b0e66563 | 套件 `test_panel_update_pg_volume_check_rolls_back_on_mismatch`；R2-14 红证 #1 强红成立 |
| A2-03 | P2 | 已修 | 944f77d8 | 套件 `test_panel_pin_no_live_read_no_volume_key` 等 |
| A2-04 | P2 | 已修 | eb6b9a66＋eeaddda5（CRLF 补钉） | 套件 `test_env_var_reading_crlf_shape` |
| A2-05 | P2 | 已修 | 2a130033 | 套件 `test_panel_caddyfile_without_site_keeps_shared_addresses` |
| A2-06 | P2 | 已修 | fe079282 | 套件 `test_panel_uninstall_without_docker_cleans_full_residual_set` |
| A2-07 | P3 | 已修 | 2c4649c2 | 套件 `test_panel_run_lock_and_unique_caddy_backup_names` |
| A2-08 | P3 | 已修 | e6bf9b34 | 套件 `test_panel_invalid_instance_id_hard_fails_on_manage_paths` |
| A2-09 | P3 | 已修 | eb6b9a66（主体）＋86eeb382（残面：多网络不钉＋告警） | 套件 `test_panel_pin_network_multi_network_not_concatenated` |
| A3-01 | P2 | 已修 | 889c4e79 | 套件 `test_install_script_latest_resolution_requires_corroboration` |
| A3-02 | P2 | 已修 | c3dabe60（+7e231e3a、ce40aef3 夹具对齐） | 套件 `test_install_script_rejects_malformed_explicit_versions`；R2-14 登记一致性核对在位 |
| A3-03 | P2 | 已修 | 89397b64 | 套件 `test_install_script_binary_anchor_{passes,mismatch_rejects,degrades}` |
| A3-04 | P2 | 已修 | f26b76bc | 套件 `test_install_flmx_agent_rolls_back_over_existing` |
| A3-05 | P3 | 已修 | 03c2d323 | 套件 `test_delete_self_pipe_form_preserves_cwd_namesake` |
| A3-06 | P3 | 已修 | 2705fa0e | 套件 `test_install_lock_rejects_concurrent_run` |
| A3-07 | P3 | 已修 | fca51ba6 | 套件 `test_install_script_accepts_secret_from_env` |
| A3-08 | P3 | 已修 | ef27d2c6（+a2cb5977 harness 修复 ALP-RED-1） | 套件 `test_openrc_pidfile_uses_configured_value`；alpine 套件终跑 exit 0 |
| A3-09 | P3 | 部分修 → 登记遗留 | 缺口 1/2/3/5 已钉（见 A3-02/04/05 各例） | 缺口 4（Alpine 真跑）入 §4 |
| A4-01 | P2 | 已修 | 846775e5 | pins 门＋selftest 变体亲跑 PASS（R2-01） |
| A4-02 | P2 | 已修 | bdc8aa7b | pins 门下限防真空亲证（副本缺件 exit 2 点名） |
| A4-03 | P2 | 已修 | 707da602（内容面 11/11 digest）＋85097d62（R2-01-03 补 workflow `image:` 面） | pins 门容器普查 12 处全 digest；selftest 变体 8 |
| A4-04 | P2 | 已修 | 47b72383（机械推导）＋449a2f4d（R2-01-04 补 nullable 集机械推导） | 契约门 C1–C7 PASS；selftest 变体 3 |
| A4-05 | P2 | 已修 | c80c3294（PORT）＋673be779（XC4-B2 抽自测脚本） | `dual-push-legacy.sh` 自测 9/9 边界格 |
| A4-06 | P2 | 已修 | a1a8da92 | ci-build.yml 顶层 `permissions: contents: read` 终树实读 |
| A4-07 | P3 | 已修 | 6edbb6bf | CI PG 服务镜像与随版 compose digest 三处同值亲核 |
| A4-08 | P3 | 已修 | ad9b4d36 | requirements.lock 全量 `==` 钉版亲核 |
| T-INC-01 | P2 | 已修 | a61335fe | 套件宿主安全门＋哨兵例（LIVE2-SCRIPT 隔离复跑 PASS）；其后套件一律容器隔离（INC-01：基线裸跑曾销毁在跑 agent，同轮已完整恢复，三容器与面板 DB 零损伤） |

### 2.2 B 簇（后端 handler）

| 根项 | 级别 | 判定 | 修复提交 | 回归测试 / 证据 |
|---|---|---|---|---|
| B1-01 | P1 | 已修 | d5f5fccd＋47ba9f5e＋8fd699f4（FX-D，FIX-0 拣入） | `initial_admin_test.go`（R2-14 父本法弱红定级、终树 9 例全 PASS）；HARVEST T1 七例实跑 PASS |
| B1-02 | P2 | 已修 | eba99b3a | `password_b102_test.go` ×3 终树实跑 PASS |
| R-MD5（B1-03＋F1-04） | P3 | 待决策 | — | 入 §3-4（§13 裁定） |
| B1-04 | P3 | 登记遗留（本报告补登） | — | 入 §4；终树 `config.go` JWT secret 接受面与基线同形、无长度/强度门（REPORT-FINAL 钉版直读） |
| B1-05＋E1-01（根 R-WS-TOKEN） | P2 | 缓解已落＋待决策 | eb1a7e5f（前端 nginx 日志形态）＋48d70e3a（Caddy 过滤）＋54499db8（指引面） | 入 §3-1；LIVE-C 分层取证（随版前端/后端日志 `secret=` 计数 0） |
| B2-01＋B8-01（根 R-B8-01） | P1 | 已修（角色面）＋status 面由 R2-08-01 补修 | ac35206d（FX-E）＋29cb5c47 | `import_admin_guard_r1_test.go`；LIVE2-PANEL-B 真机整单拒（code −2）；R2-14 红证 #4 强红 |
| B2-02 | P3 | 已修 | b4887bcb＋88e16343（互补两半） | `TestB202ResetFlowWriteFailureSurfaces`（行为级） |
| B3-01＋F1-06（根 R-INSTALL-CMD） | P3 | 已修 | c0142bc3（生成面）＋67c42368（导入面） | 生成面字符集门＋shellQuote 覆盖两形态；导入面 `TestImportNodeSecretCharsetGate` |
| B3-02 | P3 | 已修 | 0c329e54 | 回归三例在位（R2-04 核）；机制差集经基线实证恰一处 |
| B4-01＋B6-03＋B8-04（根 R-B4-01） | P1 | 已修 | e5e6ddb8（合法集单点）＋bb258c08（四面接门）＋2ff1af99（存量重演残面） | `chain_strategy_test.go`＋存量 "banana" 重部署探针（基线可过、修复后点名拒） |
| B4-02＋B8-03（根 R-B4-02） | P2 | 已修 | 4e020b23 | 两序＋跨表三形状回归（正序洞形/反序/链路·入站跨表） |
| B4-03 | P2 | 已修 | 713fcc78 | 修复自带 fake-agent 线束 3/3（LIVE2-PANEL-B 采信并注明界限：回执与保留语义） |
| B4-04 | P2 | 已修（核心）＋残面 R2-05-01 | c859c3b6 | `TestCreateForwardTxDuplicateEntryNodesDoNotDeadlock`；残面见 §2.7 |
| B4-05 | P2 | 已修 | 844e6721（哨兵重写）＋baa85721（紧凑形态残面＋变异钉） | `TestK9DecisionShapedPredicateVariants`（10 变体＋负控制）；R2-05 独立文本普查差集 ∅ |
| B4-06 | P3 | 文档关闭 | 9d4ae2dd | §9.11 裁定；`doc/guide/forward.md:109-110` 与 NOTES S-04 行实址核验（R2-05） |
| B5-01 | P2 | 已修 | 5d658bb0（FX） | `TestOpenSubscriptionAcceptsLegacyTokenLengths`；LIVE2-PANEL-A 场景 5（40 字符 200/未知 404） |
| B5-02＋E2-01（根 R-B5-02） | P2 | 已修 | aa9994d0（后端）＋1da892be/4780c438（FX 前端/demo） | `TestUserSubscriptionMineMarksPendingDelete`（HARVEST T5 组内 PASS） |
| B5-03 | P2 | 已修 | 53fbb5ef | `TestFixB6ScriptResultCountCapped`（HARVEST T2 隔离复跑 0.04s PASS，OBS-2 定性环境竞速）＋同族组 |
| B5-04＋F1-05（根 R-GOJA-CACHE） | P3 | 已修 | bcdb7956 | `TestFixB6SubScriptCacheBounded`（HARVEST T12 PASS） |
| B5-05 | P3 | 已修 | 53fbb5ef（同笔钉） | `TestFixB6ScriptTimeoutTerminates`／`TestFixB6ScriptTopLevelTimeoutTerminates`／`TestFixB6ScriptSandboxConfinement` |
| B5-06 | P3 | 已修（文档） | a614ab1b＋42d5339a（FX） | subscription.md:36、nodes.md:152 与代码口径一致（R2-06 核） |
| B6-01 | P1 | 已修 | 6abebf7e（本体）＋465487ee（同族补全：kcp 配置/SSH 密钥/诱饵面） | `TestB601*` 基础组（R2-14 红证 #5 强红）＋`TestB601Family*`；LIVE2-PANEL-B 场景 4 真机 PASS |
| B6-02 | P2 | 已修 | fa8ff802＋2b06140d（XC4-B3 tick 钉） | tick 级用例；注记：修复后双面板到期真机景未复跑（§4 注记项） |
| B6-04 | P3 | 已修 | fa8ff802 | 提供方三因拆文案＋消费方按因映射（R2-07 核） |
| B6-05 | P3 | 已修 | 5d7376b2 | 用量同步合并入存量 RemoteConfig（R2-07 核） |
| B6-06 | P3 | 已修 | ea462a95 | 存量复检/resume 全集校验（R2-07 核）；本族形态另见 R2-07-01 存量例 |
| B7-01 | P1 | 已修 | 935c6736（FX-A） | `TestNormalizeDBType`／`TestDBTypeIsPostgres`（R2-14 弱红定级）；LIVE2-PANEL-B 别名真部署（PG 49 表）＋本体探针 4/4 |
| B7-02 | P2 | 已修 | d2fa5126（FX） | `releaseMatchesChannel` 四分类面（R2-08 核） |
| B7-03 | P2 | 已修（披露半边，§9.5 裁定范围） | e68da283 | `TestB703` ×3（MERGE-2D 验绿）；快照本体维持既有切出裁定（§3 关联注记） |
| B7-04 | P3 | 已修 | 06290f03 | `TestParseReleaseChecksumToleratesFormats` |
| B7-05 | P3 | 已修 | 0e680128＋4095cd41（GATE-FIX A 改钉正向断言） | `TestB705` ×2 |
| B8-02 | P2 | 已修 | 7b114c99＋27e96c0c/cff45040（GATE-FIX C 过修纠正，R2-08 复核通过） | `TestImportServiceMonitorsMirrorWriteGates`／`TestImportServiceMonitorsLimitsFromConfig` |
| B8-05 | P3 | 已修 | 519e8c5c＋27e96c0c（Type=0 保真纠正） | `TestImportTunnelTypeStatusGate`（改钉后断言）；R5B5 原钉零改动经 git log 实核 |
| B8-06 | P3 | 已修 | （段数帽/端口格式 FX-B 镜像）＋5608bb4c（MaxConn 残面） | `TestImportForwardMaxConnClamp` |
| B8-07 | P3 | 已修 | 0e780c14（FX） | 本地字段名入回执＋Gist 段普查点名（R2-08 核） |
| B8-08 | P3 | 已修 | eb4e319a | `TestImportRemoteSourceMirrorGates` |
| B8-09 | P3 | 已修 | 51ed9d0e | `TestImportSubscriptionTemplateMirrorGates` |
| B8-10 | P3 | 已修 | 38f7937c | `TestImportPeerShareAllowedIPsNormalization` |
| B8-11 | P3 | 已修 | 1cee6b6c | `TestImportNodeRenewalForwardModeNormalization` |
| B8-12 | P3 | 已修 | d70bb5b0 | `TestImportConfigsDroppedKeysNamedInReceipt` |
| B8-13 | P3 | 已修 | 4e3385f8 | `TestGistUploadPatchEmptyIDKeepsStoredID` |
| B8-14 | P3 | 已修 | 57147847＋f9b5a815（users 大档 50000，R2-08 复核维持） | `TestImportSectionRowBudgets`（13 段全覆盖，R2-13 子路正证） |
| B9-01 | P3 | 待决策 | — | 入 §3-2 |
| B9-02 | P3 | 待决策 | — | 入 §3-2（与 B9-01 同族） |
| B9-03＋C4-02（根 R-FED-CLIENT） | P3 | 已修（联邦腿）＋gist 孪生由 R2-02-02 补修 | 4d34138b（B9-03）＋edba19ac（C4-02）＋6aec34b3（gist 腿） | `TestC402RedirectSchemeDowngradeStripsCredentials`；gist 腿见 §2.7 |
| XC4-B1 | 附带 | 已修 | 9b8bea76 | 监控两面 respondInternalError 普查钉（`monitoring_internal_error_xc4_test.go`） |
| XC4-B2 | 附带 | 已修 | 673be779 | `dual-push-legacy.sh`＋自测（见 A4-05） |
| XC4-B3 | 附带 | 已修 | 2b06140d | tick 级到期回收用例（见 B6-02） |

### 2.3 C 簇（存储 / 内核 / 运行时 / 网络面）

| 根项 | 级别 | 判定 | 修复提交 | 回归测试 / 证据 |
|---|---|---|---|---|
| C1-01 | P2 | 已修 | 125a848e | `migration_status_c101_test.go`（SeesV3/SeesV4/NamesV2）；HARVEST T9 组内 PASS。注：L-11 scratch 活体定量未见执行证据，机制由代码＋索引手术测试证（R2-11 注记） |
| C1-02 | P2 | 已修 | ca3e881f | `TestSeedCreateFailureSetsAdminBootstrapRequired`（HARVEST T1 组内 PASS） |
| C1-03 | P3 | 登记遗留 | — | 入 §4（§13 补登） |
| C1-04 | P3 | 登记遗留 | — | 入 §4（§13 补登） |
| C1-05 | P3 | 已修 | 8524ca95 | `export_snapshot_c105_test.go` 两例（HARVEST T9 PASS）；PG 侧证据限度入 §4 注记 |
| C2-01 | P3 | 已修 | 417ee8db | 内核包单点校验回归在位（R2-04 核） |
| C2-02 | P3 | 部分修 → 登记遗留 | f6418976（Xray 半：GitHub API 资产 digest 锚） | `TestFixB6EveryKernelSpecHasStrongAnchor`；GeoData 半入 §4（§12 裁定） |
| C2-03 | P3 | 书面豁免 | — | 入 §3-3 |
| C3-01 | P2 | 已修 | 57a698ef | `ValidateTargetHost`/`ValidatePlan` 单点；生产 RenderTable 调用点全量枚举无旁路（R2-04） |
| C3-02 | P1 | 已修 | dd4a4232＋1076f848（DTO 补口）＋d5adaeff（前端表单面） | TOFU 全语义复核（R2-04：写钉仅两点、resetHostKey 唯一重钉入口）；LIVE2-PANEL-B 真 sshd 全链 PASS；R2-14 红证 #7（DTO 面）强红 |
| C3-03 | P2 | 已修 | 3c64ff78（FX-E） | `TestCheckerRunOnceUpdatesLatestCache` |
| C3-04 | P2 | 已修 | 7b23b8f9 | `TestCheckTCPSharesOneDeadlineAcrossIPs`（HARVEST T10 PASS） |
| C3-05 | P3 | 已修 | 66e2fb06 | `TestC305RejectsWrappingCounters`／`TestC305ClampsOutOfRangeValues`（HARVEST T11 PASS） |
| C3-06 | P3 | 已修 | 3935bf9c | `TestC306FlushFailureRequeuesWindow`（HARVEST T11 PASS） |
| C3-07 | P3 | 部分修 → 登记遗留 | (b) 行为面已由 LIVE-B 真机两证关闭 | (a)/(b) 仓内面入 §4（§13 补登） |
| C4-01 | P2 | 已修 | dcba430d（clientip 收窄）＋98adfa7c（.env 模板）＋6f39a58b（PV-11 compose 透传）＋54499db8（升级披露文档）＋905b8421/9a37efaf/71b7f6ec/b248be2d（GATE-FIX2 旧钉迁移） | `clientip_c401_test.go` ×3 终树实跑 PASS；LIVE2-PANEL-A 场景 6 两态真机 PASS；套件 compose 透传例（R2-14 登记核对） |
| C4-03 | P3 | 已修 | 264d75fa | `logredact_c403_test.go` ×5 终树实跑 PASS（R2-02B） |

### 2.4 D 簇（agent：go-gost 本体与 x）

| 根项 | 级别 | 判定 | 修复提交 | 回归测试 / 证据 |
|---|---|---|---|---|
| D1-01 | P2 | 已修 | 3e589010＋fad8e427（生命周期假面板改加密信封） | `TestD1PlaintextCommandsRejectedWhenSecretConfigured`；LIVE2-AGENT 场景 4 双树对照 PASS；R2-14 红证 #12 强红；§12 非 BC 定性经 R2-09 两端对读复核成立 |
| D1-02 | P3 | 已修 | 076e92ca | `TestD1EncryptedEnvelopeReplayRejected`／`TestD1SeenCommandIDsClaimSemantics`；残余观察 R2-09 OBS-1 入 §4 |
| D1-03 | P2 | 已修 | ab7d68e3 | `TestD1InboundFrameReadLimit` |
| D1-04 | P3 | 已修 | 3828aef5 | `TestD1CommandSetContract`（30 型双向；R2-09 独立复算漂移 ∅） |
| D1-05 | P3 | 已修 | b537101e | 按 §12 裁定硬拒（发送方 URL 不得为校验锚、expectedSha256 强制在位）；R2-09 同族全仓复扫差集 ∅ |
| D1-06 | P3 | 已修 | a822f031 | `TestD1ExpiredBudgetReadCommandRefused`；覆盖面逐型点名（R2-09：11 型预算内、18 变更类设计内豁免） |
| D2-01 | P2 | 已修 | b6291069 | needWrap 原子化（R2-09 终树实读） |
| D2-02 | P3 | 已修 | d7ecd79f | §9.10 改判 FIX-NEW：managed 构建执行器拒绝执行（R2-09 核：parse 层第一道门未被推翻） |
| D2-03 | P3 | 已修 | 616926e9 | v1/config 两 POST 路径 4096 帽（R2-09 核） |
| D2-04 | P3 | 已修 | 9fbfa1ce | CommandContext＋WaitDelay（R2-09 核） |
| D2-05 | P3 | 已修 | 621ecf1b | `TestSupervisorHonoursStopRequestedBeforeFirstLaunch`／`TestStopTerminatesRunningProcess`；30s 硬顶后无孤儿推演（R2-09） |
| D3-01 | P1 | 已修 | 6d864417 | `pool_ttl_test`；LIVE2-AGENT 场景 1（旧版 panic、新版存活回声）；R2-14 红证 #10 强红（含 panic 复现） |
| D3-02 | P1 | 已修 | 09948303 | `service_panic_recover_test`＋`matcher_host_test`＋`udp_localaddr_test`；LIVE2-AGENT 场景 2 双面 PASS；R2-14 红证 #11 强红。触发口径按 §12 修正（纯空白 Host） |
| D3-03 | P2 | 已修 | 3708a289 | `internal/net/transport_test.go`；LIVE2-AGENT 场景 3（旧 0 字节、新 524288 字节 sha 一致） |
| D3-04 | P2 | 已修 | 4a96a867 | 优先级过滤＋重试排除名单端到端接通（R2-10 核，附阈值口径注记） |
| D3-05 | P2 | 已修 | 56f514e1 | parse fail-closed 回归在位（R2-10 核） |
| D3-06 | P3 | 已修 | 27d92428 | `selector/filter_test.go`（stdout 捕获） |
| D3-07 | P3 | 已修 | e1b62495 | 成功 attempt 的 cancel 随连接转移（R2-10 核） |
| D3-08 | P3 | 已修 | 760b8b11 | 分支测试＋相邻 Put 路径对称性（R2-10 认可定性：归还不可外观测、无红绿判别器） |
| D3-09 | P3 | 已修 | c99a3759 | IsIPv4 字面判定回归表（R2-10 核） |
| D3 断言残余清单 | — | 已复核 | — | 58 处未检断言 AST 同口径复算（基线单值 59→终树 58、触发点消失、修复未引入新单值断言）；三处 recover 外点（accept 前段/observer 分发/tap 广播）靠构造有界、无触发路径，仅登记覆盖图口径（R2-10） |

### 2.5 E 簇（前端）

| 根项 | 级别 | 判定 | 修复提交 | 回归测试 / 证据 |
|---|---|---|---|---|
| E1-01 | P2 | 并入根 R-WS-TOKEN | 见 §2.2 B1-05 行 | 入 §3-1 |
| E1-02 | P2 | 待决策（挂靠 §3-1） | — | 终树无 logout 路由、前端纯本地登出，登记属实（R2-02 核） |
| E1-03 | P3 | 已修 | 839be472（FX 死支）＋0e1dcfe7（site.ts 水管） | 深色防复活门四断言终树直跑 rc=0（R2-12） |
| E1-04 | P3 | 已修 | 162b9c84 | `check-sensitive-keys.mjs` 全量扫裸读终树直跑 rc=0（R2-12） |
| E1-05 | P3 | 已修 | 74be3d78 | 事件＋代次守卫机制（R2-12 核） |
| E1-06 | P3 | 已修 | df5094ea | 与 jwt.ts 逐字对齐＋`PasskeyDataError` 区分（R2-12 核） |
| E2-01 | P2 | 并入根 R-B5-02 | 见 §2.2 B5-02 行 | 三层＋demo 四面齐（R2-12） |
| E2-02 | P2 | 已修 | 6ef5ca7e | `node --test import-format.test.ts` 9/9；R2-14 红证 #13 强红 |
| E2-03 | P3 | 已修（代码面） | d7f065f1 | demo check 新增实跑批量阶段（代码面）；e2e 实跑欠账入 §4（PV-8） |
| E2-04 | P3 | 已修 | 36468b3e | 节点/隧道双面回滚＋回执点名（R2-12 核） |
| E2-05 | P3 | 已修 | 5c8e8ba8 | 全 skipped 分支点名文案（R2-12 核） |

### 2.6 F 簇（横切）

| 根项 | 级别 | 判定 | 修复提交 | 回归测试 / 证据 |
|---|---|---|---|---|
| F1-01 | P2 | 已修 | 74ee693f | exchanger/dtls/socks 三回归（`exchanger_tls_test`、`dialer_tls_test`、`connector_tls_test`）；终树生产 `InsecureSkipVerify:true` 字面量复算 0；LIVE2-AGENT 场景 5 |
| F1-02 | P3 | 已修 | 4c272d15 | http/http2 两包 `probe_decoy_test.go`（5s 超时＋重定向帽 3） |
| F1-03 | P3 | 已修 | e794a3ef | 两份 `sniffer_tls_test.go`；LIVE2-AGENT 场景 5（自签上游即拒） |
| F1-04 | P3 | 并入根 R-MD5 | 见 §2.2 | 入 §3-4 |
| F1-05 | P3 | 并入根 R-GOJA-CACHE | 见 §2.2 B5-04 行 | — |
| F1-06 | P3 | 并入根 R-INSTALL-CMD | 见 §2.2 B3-01 行 | — |
| F2-01 | P1 | 已修 | aa1cf7bd | `TestApplyUserCountersForeignNodeDroppedF201`；LIVE2-PANEL-A 场景 1（丢弃日志逐字吻合）；R2-14 红证 #3 强红 |
| F2-02 | P2 | 已修 | 6b6d9e55 | `TestApplyInboundCounterSamplesForeignNodeDroppedF202`／`TestInboundCounterNodeScopeMigrationF202` |
| F2-03 | P2 | 已修 | c5f67496＋880c05bf（普查登记 follow-up） | `TestLegacyBatchContentDedupeF203`／`TestFlowUploadLegacyVerbatimReplayBookedOnceF203`；LIVE2-PANEL-A 场景 2；观察 OBS-1 入 §4 |
| F2-04 | P2 | 已修 | 50ae9945＋fd724234（GATE-FIX B 钉收敛） | `TestImportNegativeCountersClampedF204`／`TestSubUsedSaturatesF204`／`TestBackupImportTailReevaluatesQuotaF204`；LIVE2-PANEL-A 场景 3（双 MaxInt64 饱和截断含在内） |
| F2-05 | P2 | 已修 | 0601b1f5 | `TestCreateForwardTxNumQuotaF205`（10 并发恰 1）；LIVE2-PANEL-A 场景 4（基线 9/10 建成 → 修复后恰 1） |
| F2-06 | P3 | 登记遗留 | — | 入 §4（§9.13） |
| F2-07 | P3 | 登记遗留 | — | 入 §4（§9.13） |

### 2.7 复审新立案关账（R2 期，共 23 笔复修提交，尖端 a2cb5977 → 8c5d56dd）

| 新立案 | 级别 | 判定 | 修复提交 | 回归测试 / 证据 |
|---|---|---|---|---|
| R2-08-01（B8-01 status 面） | P2 | 已修 | 29cb5c47（FIX-R2-1） | `TestImportAdminGuardRejectsSoleAdminDisabledByBackup`／`TestImportAdminGuardRejectsLegacyPasswordSoleAdmin`／正控 `TestImportAdminGuardAcceptsDisabledSecondAdmin`；VERIFY-R2 红证成立（双 FAIL 文案与原红证逐字同） |
| R2-08-02 | P3 | 已修 | e48cbae7（FIX-R2-1） | `TestImportServiceMonitorsMirrorWriteGates` 警告点名清单补 "ghost-node" |
| A-AN1（R2-13 观察升 P2） | P2 | 已修 | eb2df0d2（FIX-R2-2） | `system_upgrade_a_an1_test.go`（3 plan 级＋3 executor 级）；VERIFY-R2 语义回退红证恰 4 例 FAIL |
| C-A1 | P3 | 已修（文档） | d012c225（FIX-R2-2） | install.md 脱敏口径与 C4-03 实现对齐（混合 ≥8／纯段 ≥16） |
| C-A2 | P3 | 已修（文档） | 4eda6b82（FIX-R2-2） | skills auth.md 两处示例改为后端实返「操作成功」 |
| R2-01-01 | P2 | 已修 | 6e6f23b8＋c33843d8（FIX-R2-3） | 套件 `test_panel_anchor_corroboration_degrades` 改写三相；VERIFY-R2 子集红证文案与原 RED0 逐字同；套件终局 215 例全绿 |
| R2-01-02 | P3 | 已修 | acdb7523（FIX-R2-3） | 套件 `test_panel_caddy_source_fetches_use_timeouts`；两脚本抓取点机械复扫差集 ∅ |
| R2-01-03（A4-03 残面） | P3 | 已修 | 85097d62（FIX-R2-3） | pins 门 workflow `image:` 普查（下限 11→12）＋selftest 变体 8；两处失实注释改实 |
| R2-01-04（A4-04 残面） | P3 | 已修 | 449a2f4d（FIX-R2-3） | 契约门 nullable 集机械推导（实测 4 助手）＋selftest 变体 3 |
| R2-01-05 | P3 | 已修（文档） | 内容在 **2c546c5c** 内（作者路 FIX-R2-4；FIX-R2-3 报告误记 FIX-R2-2，VERIFY-R2 §4 点名更正） | install.md 菜单 5 Caddyfile 日志句改实 |
| R2-02-01 | P3 | 文档关闭（主控预裁定） | 2c546c5c（FIX-R2-4，代码零非注释行改动经 VERIFY-R2 机械过滤证实） | 消费点全集枚举：唯一消费者为管理员节点安装命令的 scheme 推断，无鉴权/限流/Cookie/联邦信任消费；两处失实注释改实＋install.md 例外注 |
| R2-02-02（C4-02 gist 孪生） | P3 | 已修 | 6aec34b3（FIX-R2-4） | `gist_r20202_test.go` 四形态（降级剥/同 scheme 保/http→http 保/跨 host 剥） |
| R2-02-03 | P3 | 已修 | 53e6a5c9（FIX-R2-4） | `gist_r20203_test.go`（userinfo 不得出现＋操作前缀保留） |
| R2-03-01（B2-01 披露子面） | P3 | 已修 | 1526175b（FIX-R2-6） | `import_admin_disclosure_r20301_test.go` ×3（新建点名/提拔点名/集合未扩大静默） |
| R2-03-02 | P3 | 登记遗留 | — | 入 §4（仅 PG 契约覆盖不对称，行为无缺口） |
| R2-03-03 | P3 | 登记遗留 | — | 入 §4（非事务 CreateUser 零生产调用的潜在面） |
| R2-05-01（B4-04 残面） | P3 | 已修 | 36ab05a3（FIX-R2-6） | `replace_forward_ports_r20501_test.go` ×2；TunnelEntryNodeIDs 全 15 消费点普查（写面全覆、非写面逐项定性） |
| R2-07-01（netns 族） | P2 | 已修 | 9427c1b5（FIX-R2-5） | `TestB601FamilyNetnsRejected`／`TestB601FamilyStoredNetnsPaused`／ValidatorKeyShapes 5 拒绝形；VERIFY-R2 红证成立；同族换谓词复扫差集仅此族（另两项见下行） |
| interface（§14 裁定） | P3 | 已修 | 08d1fba4（FIX-R2-7） | `TestB601FamilyInterfaceRejected`／`TestB601FamilyStoredInterfacePaused`／`TestB601FamilyInterfaceLifecycleIntact`（合法物化流不断经真实生命周期 apply 证） |
| so_mark（§14 裁定） | P3 | 已修 | 4a9583ad（FIX-R2-7） | `TestB601FamilySoMarkRejected`（含 0 值对照）／`TestB601FamilyStoredSoMarkPaused`／ValidatorKeyShapes 6 拒绝形＋6 放行形 |
| Go 锚面 1：面板 corroborate（§15） | BC 级 | 已修（待验收确认） | ab22b0b7（FIX-R2-9）＋30257e57（FIX-R2-8 文档对齐） | `TestDownloadReleaseAssetProxyWinnerNoCorroborationAborts`／`TestDownloadReleaseAssetProxyWinnerCorroborated`；入 §3-5 |
| Go 锚面 2：agent corroborate（§15） | BC 级 | 已修（待验收确认） | 90a54d52（FIX-R2-9） | `TestCorroborateUpgradeChecksumWinnerLayersFixR29` 六子例；顺带收口空集静默放行 |
| Go 锚面 3：安装命令内嵌锚（§15） | BC 级 | 已修（待验收确认） | 378aa0de（FIX-R2-9） | `TestBuildNodeInstallCommandAnchorWinnerLayersFixR29` 三子例（中止删档落实） |
| Go 锚面 4：x/kernel corroborateAnchorDigest（§16） | BC 级 | 已修（待验收确认） | 8c5d56dd（FIX-R2-10） | `TestCorroborateAnchorDigestWinnerLayersFixR210` 六子例＋三消费者逐面例；§17 两项（钉迁移强度、geodata 早退前移）经 VERIFY-R2 独立复核通过 |
| R2-10-01 | 信息 | 登记（不立案） | — | socks relayUDP 一处断言操作数随链组合而变，已被分发 recover 收敛为单连接失败；可选 comma-ok 硬化，未做 |
| R2-11-01 | 账目 | 已处置 | — | C1-03/C1-04/C3-07 补登遗留（§13），见 §4 |
| R2-11-02 | 账目 | 已处置 | — | R-MD5 转需人工决策（§13），见 §3-4 |
| R2-11-03 | 账目 | 已处置 | — | 路由口径订正（§13）：实注册展开集两树各 205、差集 ∅；官方 204 分解的子数 182 与实测 183 有 1 条既存口径差；`/kernel/batch-install|batch-remove` 两树均不存在（见 §4 孤儿端点项） |

---

## 3. 需人工决策清单

| # | 事项 | 现状 | 建议 | 影响 |
|---|---|---|---|---|
| 3-1 | **WS 短效票据**（根 R-WS-TOKEN：B1-05＋E1-01，P2） | 7 天期全权 JWT 仍走 WS URL query `?secret=`；本轮已落缓解三面（随版前端 nginx 日志只记 `$uri`、Caddy 模板过滤删 `secret` 参数、install.md 字段级指引含自检法），LIVE-C 分层实证随版面日志 `secret=` 为 0，暴露面收敛到运营自加的默认日志反代层 | **立项建票据**：≤60s、单次、scope 限 system-info 的 WS ticket（新端点＋握手改造＋兼容窗口），根除 token-in-URL | 属协议 BC；不建则须书面接受「自加反代默认日志＝会话接管面」的残余 |
| 3-1a | **登出无服务端撤销**（E1-02，挂靠 3-1） | 后端无 logout 路由（grep 零命中属实）、前端纯本地清 key；复制走的 token 至到期有效，与 3-1 叠加放大泄露窗口 | 随票据一并设计 per-user token epoch／撤销面，或至少缩短用户 JWT 期 | 与 PasswordChangedAt 失效面的叠加范围须产品定 |
| 3-2 | **信封码 vs HTTP 状态**（B9-01＋B9-02） | WriteJSON 恒 HTTP 200＋信封码、未知路径裸文本 404；前端全量消费信封码，功能不受影响 | **保持现状＋文档点名**（改真状态码是全前端错误处理契约的 BC，收益仅形态一致性） | 若将来改，须双轨期与前端同批切换，避免两次改契约 |
| 3-3 | **C2-03 sing-box 远程 rule-set 无完整性字段** | 格式内根本不存在可填的完整性值；URL 为固定 SagerNet 模板＋闭合 tag 集 | **书面豁免接受残余**（本报告即豁免登记）；备选是自托管分发（产品级变更） | 残余＝上游模板仓被攻陷时规则集内容可被替换，无本地锚可验 |
| 3-4 | **R-MD5 sunset**（B1-03＋F1-04，P3，§13） | 存量休眠账户的无盐 MD5 校验仍被接受；缓解已在位（登录成功即转 bcrypt、导入强制重置、时序填充），残余仅静态泄露面、无远程触发 | 下一发版周期做 sunset：剩余 legacy 行强制走重置流、其后删 `security.MD5` | 执行与否由用户拍板；本轮不修、不静默丢弃 |
| 3-5 | **§15/§16 锚分层收口（BC 级，已执行待验收确认）** | 「直连死、仅一代理活、其余线路全灭」网络态下，面板内升级／agent 自升级／生成安装命令／内核与 geodata 安装四面，由「点名降级放行」变为「点名拒绝」；运维退路在（脚本升级运维显式单线、手动替换），回执点名原因、非静默失败 | **确认接受**（安全模型跨面一致优先：单个被劫持代理不得自给自洽对整单伪造最高特权消费点） | 若不接受，须回退四面分层逻辑（ab22b0b7/90a54d52/378aa0de/8c5d56dd）并改以书面接受残余登记 |

关联注记：B7-03 的 PG 升级前快照本体维持此前「切出」裁定不重开（披露半边已修，见 §2.2）；本轮不列为新决策项。

---

## 4. 遗留清单（点名＋理由，均不记已修）

| 项 | 级别 | 理由与现状 |
|---|---|---|
| C2-02 GeoData 半边 | P3 残余 | Xray 二进制半已换 GitHub API 资产 digest 强锚；GeoData 仍同源 `.sha256sum` 单锚（校验值**来源**问题）。做 digest 锚须动 agent geodata 消费面，本轮不动（§12）。注：§16 收口的是跨线路**复核降级分层**、与本残余相邻但不同机制，不得混报 |
| C1-03 | P3 | migrateSchema v0→7 链非原子，但全步幂等、重跑安全、无数据丢失路径；双 DB DDL 事务化包裹非零风险，收益仅可观测性（§13 补登） |
| C1-04 | P3 | migrateSubscriptions 一期不在事务内，但选择器自愈、仅瞬时错误时阻塞启动；修法已明确，须配故障注入测试单独成批（§13 补登） |
| C3-07(a) | P3 | 测试辅助 DNS pin 全局无同步：仅测试面潜伏竞态、生产路径不受影响、现行用例串行无假绿（§13 补登） |
| C3-07(b) | P3 | S-05 内核行为已在真机两证（LIVE-B），但仓内未沉淀 root 门控集成测试、无对应 CI 通道，该证明不可由仓内复跑（§13 补登） |
| F2-06 | P3 | 节点 NIC 展示口径：本节点 agent 可靠伪造 boot 变更抬高仅自身节点的展示月总量；仅展示、自范围（§9.13） |
| F2-07 | P3 | 幂等存储为进程本地＋10 分钟 TTL，重启丢在途创建的重放保护；已登记的单实例部署限制（§9.13） |
| A3-09 缺口 4 | P3 | Alpine 套件真跑目前是文件桩＋容器内 OpenRC 形态；建议随 A4 在 CI 加 alpine 容器真跑一步（FIX-S2B 定性：非零风险遗留） |
| demo e2e 未跑 | 证据欠账 | `scripts/demo/check.mjs` 需 playwright chromium-1187，测试机无此环境，未跑（demo 构建本身 RC=0）；E2-03 按代码面判已修，实跑欠账归 CI 形态环境补（PV-8） |
| 孤儿端点 | 登记保留 | 仅 `/user-inbound/list|set` 一对（管理员面、回执完整、删之是 BC，§9.9 保留登记）；**订正**：`/kernel/batch-install|batch-remove` 在基线与终树均不存在，不得列为保留孤儿（§13） |
| R2-13 其余观察项 | 观察 | A-AN2（configfile 键拒宽于注释，fail-closed 仅误拒风险）、A-AN3（C3-02 新支错误原文回出，管理员面）、A-AN4（trustedProxyCIDRs 每请求重解析＋非法条目逐请求打日志，错配置可刷日志）、A-AN5（超 72 字节 FLMX_ADMIN_PASSWORD 播种中止文案未点名 env 键）、B 观察 4（A2-09 零网络臂回退钉、D1-06 预算只穿 speedtest 腿、D1-02 零值 reporter 懒初始化理论竞态、A1-01 预算在路线探测后定格）、D 观察 3（竞态测试断言力依赖 -race、B703 第 3 例为脚本文字在位、fxe_r1_checker 双设 FLVX+FLMX 无害瑕疵）、C 记录 2（自测变体编号序、前端 import 未按字母序）——均不立案 |
| R2-03-02 | P3 注记 | 导入面用户名折叠执法只有 SQLite 侧用例＋同事务代码路径，PG 级契约覆盖不对称；行为无缺口 |
| R2-03-03 | P3 注记 | 非事务版 `CreateUser` 无内建折叠复检、零生产调用、仅测试夹具在用；若将来获得生产调用方，须改走 Tx 变体 |
| R2-06 OBS-1 | 观察 | F2-03 去重表无全局清扫/窗口内行数上限：沉默节点行留存至其下次 claim；>24h 重放靠信封新鲜度＋2027-02-02 日落。与 FIX-B3 披露一致 |
| R2-09 OBS-1 / OBS-2 | 观察 | OBS-1：D1-02 认领集为进程内状态，agent 重启后仅剩时间戳窗兜底。OBS-2：面板 TrimSpace 与 agent 不 trim 的纯空白 secret 接缝，方向 fail-closed、无正常产生路径 |
| R2-04 观察 | 观察 | 备份结构 `NodeSSHConfigBackup` 不含 SSH 指纹字段：恢复后节点回未钉态、下次 Test 重新 TOFU 并回执明示新指纹；失效方向保守 |
| FIX-R2-6 原始长度门注记 | P3 注记 | 「原始长度」型门以未去重列表计长（nft 面 len==1 判定、forwardUpdate 多入口 InIP 门、迁移 plan 的 hasCustomInIP 门）：存量重复行下单节点隧道会被计成多入口，后果方向均为拒绝/跳过（fail-closed 侧），不产生静默错写；改其口径须单独立项并配 nft/迁移面回归，本轮不动 |
| C1-05 PG 证据限度 | 注记 | PG 侧撕裂读定量活体探针（L-12）无执行证据；该条判定立于代码核读＋快照事务回归＋PG 契约测试代证（R2-11/§13 注记）。C1-01 的 L-11 活体定量同此注记 |
| B6-02 真机注记 | 注记 | 修复后双面板到期回收真机景未在 LIVE2 批复跑；判定立于代码＋tick 级回归 |
| B1-04（本报告补登） | P3 | 环境 `JWT_SECRET` 任意非空长度即被接受、无强度门，终树与基线同形。其原利用链的另一腿（seed 管理员固定 PasswordChangedAt）已随 B1-01 修复拆除；残余＝运维自设弱 secret 时 JWT 可被离线爆破，属部署口径面。本轮未修、未被前序台账登记，于此补登，建议随 R-MD5 同批以「启动时弱 secret 点名告警（不拒启）」零风险形态处置或书面接受 |

---

## 5. 门禁终局表（终尖端 `8c5d56dd`，HARVEST 实跑为准）

| 门 | 结果 |
|---|---|
| go-backend 非 handler（17 包） | **rc=0，全 ok，0 FAIL** |
| go-backend handler（加宽 `-timeout 30m`、独占） | **rc=0，ok**（515s） |
| go-gost 全量 | **rc=0，0 FAIL** |
| go-gost/x 全量 | **rc=0，45 包 ok，0 FAIL**（R2-10 确认性分包由此外覆关闭） |
| PG 契约 10 例 · postgres:16（glibc＋ICU en-US） | **10 PASS / 0 FAIL / 0 SKIP**（折叠例真实执行，PV-6 残项闭合） |
| PG 契约 10 例 · postgres:16-alpine | 9 PASS / 0 FAIL / 1 SKIP（musl/C locale 下折叠例按设计自跳） |
| PV-14 定向收割组（T1–T12） | **12 组全绿、106 顶层 PASS、0 FAIL**（initial_admin ×7、OBS-2 隔离复跑 0.04s PASS 定性环境竞速、R2-03/06/10/11 欠账组全数实跑） |
| check-doc-consistency.sh（全工具链） | **RESULT: PASS，FAIL 0，PENDING 0** |
| 脚本套件（proxy） | **215 例全绿**（FIX-R2-3 在 c33843d8 同环境双绿：基线 214 PASS＋尖端 215 PASS；其后脚本面 diff ∅，HARVEST 代证成立） |
| 脚本套件（alpine） | exit 0（缺口 4 的设计形态注记见 §4） |
| 轻门电池（R2-14 终树复跑） | 8 门＋9 自测＋`bash -n` 24/24＋锚点普查测试 **全绿**（含 timeout-budgets 门＋自测两对绿、8 变体全抓，PV-9 销账） |
| 前端（GATE-FE，树自其后零改动） | `pnpm run build` PASS（3248 模块）；tsc 0；eslint 0 errors；check:all 17/17（HARVEST 差分 ∅ 登记延续） |
| VERIFY-R2 红证 | 四根 P2 行为级红证全成立（回退即红、红在修复行为、恢复即绿）；P3 批抽验 diff↔声称逐笔一致；§17 两专项独立复核通过；23 笔复修提交账目闭合、零野提交 |
| 基线红对账 | BL-GO 超时形态、BL-AGENT 单次 flaky（终局未复现）均按基线红账口径对清，无一冒充修复红或放行新红 |

---

## 6. 白话摘要

这一轮把面板从里到外审了一遍又修了一遍：第一轮 24 个审计员分区包干（外加基线、交叉核销和真机定性），把约 970 个后端文件、200 个前端文件和两个安装脚本全过了一遍，查出 143 个问题、合并成 133 个根问题，其中 11 个最危险（P1）。修的时候先把上一轮已验证的修复搬过来，再开了 14 条修复线逐个根治，每个问题一个提交、都带「修之前会红、修之后转绿」的回归测试；修完又在真机上把关键场景全跑了一遍（订阅计数、导入、升级、SSH、agent 数据面、脚本套件）。然后换了一批没参与修复的审计员（14 路）把整个仓库重新审了一遍——不是只看改动——他们又挖出 4 个 P2（包括一个备份导入能把唯一管理员置停用的锁死面、一个联邦配置能把监听塞进别的网络命名空间的漏网面），全部当轮修掉并由第三方回退验证「撤掉修复就复现」。最后在最终代码上把全部测试、PostgreSQL 契约、文档门和脚本套件（215 例）完整跑绿。

三个最危险的问题：**一是新装面板的默认管理员口令是公开的**（`admin@user`），谁先登录谁接管——改成随机生成或环境指定的一次性口令、只在安装时出现一次。**二是订阅流量计数不验节点归属**，一台被攻陷的节点能把任意订阅的用量灌爆、触发配额切断——改成只认该节点实际服务的订阅，外来上报直接丢弃并留日志。**三是节点间联邦共享能携带证书文件路径直达对方 root agent**，等于让对端读你机器上的任意证书私钥——改成单点校验器把所有本机路径类字段（以及复审又挖出的网络命名空间、网卡、socket 标记几族）全部拒收。

复审共两轮全量（一轮审计＋一轮换位复审，中间还有交叉核销与红证复验），最终状态：**零 P0、零 P1，所有测试门禁在最终尖端全绿**；剩下的是一份点名的遗留清单和五件需要你拍板的事（最重要的是 WS 链接里带全权令牌要不要立项做短效票据）。全部分支只在本地，**没有 push、没有打 tag、没有发版**，等你验收。

---

## 7. 全量提交清单（`git log 00762aba..8c5d56dd --oneline` 原样，共 221 笔）

本报告提交（本文件）为其后的第 222 笔，不在下列清单内。

```text
8c5d56dd fix(kernel): layer anchor corroboration by winning route and name the empty set (FIX-R2-10)
90a54d52 fix(agent): fail closed when a mirror wins the upgrade checksum without corroboration (FIX-R2-9 agent)
378aa0de fix(install): fail closed in generated install command when a proxy wins without corroboration (FIX-R2-9 install-command)
ab22b0b7 fix(upgrade): fail closed when a proxy wins and the second anchor is unavailable (FIX-R2-9 panel)
4a9583ad FIX-R2-7 so_mark: reject socket marks in peer-shared resources
08d1fba4 FIX-R2-7 interface: reject interface bindings in peer-shared resources
30257e57 docs(upgrade): state script-side anchor degradation differs from panel side (FIX-R2-8)
36ab05a3 fix(repo): dedupe forward port entries by node in replaceForwardPortsTx (R2-05-01)
1526175b fix(repo): name imported admin creations and promotions in import warnings (R2-03-01)
c33843d8 R2-01-01: feed the second anchor in the routed-failover fixture
9427c1b5 R2-07-01: reject netns references in peer-shared resources
eb2df0d2 fix(upgrade): fail closed when the accident-gate volume probe fails (A-AN1)
acdb7523 R2-01-02: bound the Caddy source fetches with the A1-02 watchdog set
6e6f23b8 R2-01-01: fail closed on an uncorroborated proxy win in panel downloads
e48cbae7 fix(repo): name the missing-node skip in service monitor import (R2-08-02)
29cb5c47 fix(repo): count enabled admins in the backup-import admin invariant (R2-08-01)
2c546c5c docs(trust): mark panelRequestScheme as a deliberate independent trust face (R2-02-01)
53e6a5c9 fix(gist): redact URL userinfo from gist transport errors (R2-02-03)
6aec34b3 fix(gist): strip Authorization on https-to-http redirect downgrade (R2-02-02)
449a2f4d R2-01-04: derive the nullable helper set mechanically in the contract gate
85097d62 R2-01-03: census workflow image: refs in the pins gate
d012c225 docs: sync install log-redaction wording with the C4-03 heuristic (C-A1)
4eda6b82 docs: align auth API response examples with the backend success message (C-A2)
a2cb5977 test(scripts): rebuild alpine harness copies before the A3-08 section (ALP-RED-1)
b76050ad test(scripts): pin FLMX_SUITE_ISOLATED empty in the host-safety gate refusal arms (OBS-1)
ecbb5005 test(scripts): align trusted-proxies env guidance grep with the shipped wording (SCRIPT-RED-B)
ce40aef3 test(scripts): give three install.sh update fixtures a valid VERSION for the A3-02 grammar (SCRIPT-RED-A)
b248be2d Migrate TestR6L8F04RateLimitKeyForRequest trusted-proxy case to the C4-01 explicit list
71b7f6ec Migrate TestR24A_XFFCorrectTwoHopChainUnaffected to the C4-01 explicit trusted-proxy list
9a37efaf Migrate TestC02_AppendedChainStillResolvesAndLimits to the C4-01 explicit trusted-proxy list
905b8421 Migrate TestA001_TrustedProxyHops to the C4-01 explicit trusted-proxy list
54499db8 docs: disclose C4-01 trusted-proxy narrowing in upgrade and install guides (C4-01, PV-13)
5efe19bf Gate-fix G: migrate contract gate C4 extraction anchor from ExportPartial wrapper to the exportPartial dispatch (C1-05); mutation-proven
6d80052a Gate-fix F: update K11-COUNT go-gost/x package claims to go list reality (tested 45, untested 162, total 207)
e338ee83 Gate-fix E (PV-9): register testNodeNftables timeout budget as row T064 (fe 300 literal, be 30 expr); timeout gate and selftest pass two consecutive pairs, all variants caught incl. C113
187009f4 Gate-fix D (R7 anchor census): register install.sh corroborate_latest_tag as the seventh pinned anchor copy; gate verifies every copy value; mutation-proven
cff45040 Gate-fix C (PV-12): register importServiceMonitorLimits IN site as literal 7-key list; re-pin B8 restore-gate tests to carried-legacy rulings
27e96c0c Gate-fix C (B8-05/B8-02): restore gates keep carried legacy values - tunnel type 0 verbatim, http monitors restored with warning
fd724234 Gate-fix B (F2-04/R4B5): converge seeded subscription divergence before baseline in users-only anti-overfix pin
4095cd41 Gate-fix A (B7-05/B3-01): assert dev-channel named rejection and quoted install args in channel/install pins, legal 32-hex fixture secrets
6f39a58b C4-01 (PV-11): pass FLMX_TRUSTED_PROXIES through the shipped compose templates backend environment and pin it in the suite
4d06b554 Merge fix-max-fe: frontend fixes (E1/E2 clusters, C3-02 SSH form, nginx access-log redaction)
99737031 Merge fix-max-ci: CI pins census, gate mutation selftests and contract census (A4 cluster, XC4-B2)
f94bc6e2 Merge fix-max-g2: x protocol-layer fixes (D3-03..09, F1-01..03)
67e5f47d Merge fix-max-b7: backend misc gates (B1-02, B2-02, B3-01/02, B4-01/03/04/05, B7-03/04/05, C1-01/02/05, C3-04/05/06, C4-01/03, XC-4 pin)
51a4d3d7 Merge fix-max-b5: restore-face mirror gates, row budgets and import secret charset gate (B8 cluster, B4-02, B3-01)
d3de9387 Merge fix-max-b3: flow counter node scoping, legacy batch dedupe and forward Num quota (F2 cluster)
f0f95dab Merge fix-max-b2b (includes fix-max-b2): federation validation single point (B6 cluster, B9-03, C4-02, XC4-B3)
2f900dab Merge fix-max-g3: agent command-envelope security and command contract (D1 cluster)
2e9464db Merge fix-max-g1: agent service and kernel panic plus robustness fixes (D2 cluster, D3-01/02)
d2774c6b Merge fix-max-b1: nftables SSH host-key TOFU pinning and target-host validation (C3-01, C3-02)
9690a679 Merge fix-max-b6: goja result/cache bounds and kernel version plus Xray digest anchors (B5-03/04/05, C2-01/02)
d748c69d Merge fix-max-doc: guide and NOTES audit verdicts (B4-06, B5-06)
1dd6105f Merge fix-max-s2: install.sh A3 hardening and suite host-safety gate (T-INC-01)
c8a61479 Merge fix-max-s1: panel_install A1/A2 hardening, Caddy secret log filter, FLMX_TRUSTED_PROXIES .env entry (C4-01)
f9b5a815 B8-14: move the users section to the large row-budget tier so legitimate 30000-user backups stay restorable
baa85721 B4-05: shape the port-occupancy sentinel predicate by boundary and operator so compact port forms are covered, with a mutation test on synthetic sources
8524ca95 C1-05: run ExportAll/ExportPartial segment queries inside one snapshot transaction (PG repeatable-read read-only); verification-stage correction: the mechanism test probes Statement.ConnPool for the tx handle
67c42368 B3-01: gate node secret charset on the import face like the generation face
57147847 B8-14: add per-section row budgets to backup import
9b8bea76 XC-4 pin (1): monitoring list faces return the unified internal-error envelope without leaking storage errors
713fcc78 B4-03: return tunnel cleanup warnings into the update receipt and sweep stale forward services on reconnect against the current tunnel entry set
c0142bc3 B3-01: reject non-hex node secrets at install and shell-quote every interpolant of the node install command
0e680128 B7-05: reject the dev release channel by name at both latest resolvers, the list faces and both upgrade executors
06290f03 B7-04: share parseReleaseChecksum across the download and corroboration legs, tolerating tab separators, uppercase hashes and binary markers
e68da283 B7-03: disclose deferred PostgreSQL snapshot via pgSnapshotDeferred on the upgrade run receipt, status writes and helper terminal state
880c05bf F2-03: register flow_legacy_batch_dedupe as excluded runtime bookkeeping in the backup coverage census
0601b1f5 F2-05: enforce the forward Num quota inside CreateForwardTx under the user row lock
50ae9945 F2-04: clamp restored counters at import, saturate quota sums, and re-evaluate quotas in the import tail
465487ee B6-01 family completion: reject peer-carried kcp config files, SSH key material and decoy file paths at the shared-resource validator
1076f848 C3-02: expose the pinned SSH host key fingerprint in the ListNodes sshConfig DTO so a reopened node form shows the pin
dcba430d C4-01: trust only loopback plus explicit FLMX_TRUSTED_PROXIES CIDRs in clientip, private peers no longer trusted by default
98adfa7c C4-01: carry FLMX_TRUSTED_PROXIES in the generated .env with field-level guidance so fronted installs name their proxy subnet instead of sharing one rate-limit bucket
d5f1682b A1-03 adjustment 2: the explicit-version grammar guards the injection surface, not version semantics - the suite pinned-version sentinel v-test is a standing fixture contract across update and install tests
4e3385f8 B8-13: fail Gist upload when PATCH returns no Gist id instead of wiping gist_id
d5adaeff Show pinned SSH host key fingerprint and reset option in node SSH form (C3-02)
2ff1af99 B4-01: gate stored chain strategies on the tunnel redeploy validation path
7e231e3a A3-02: give the update-download test a legal explicit version fixture
d70bb5b0 B8-12: name dropped config keys in the restore receipt
3935bf9c C3-06: requeue node metrics and traffic when a flush write fails
66e2fb06 C3-05: validate and clamp node metric inputs at ingestion
1cee6b6c B8-11: normalize node RenewalCycle/ForwardMode on restore like the write face
0e680ebb A1-03 adjustment: allow one optional v prefix in the explicit-version grammar (panel_version_cmp already treats it as cosmetic and the suite pinned-version fixture contract uses v-prefixed values); traversal and injection shapes stay rejected
880a687b gofmt: format the B6-01/B6-06 regression test files
7b23b8f9 C3-04: share one deadline across all IPs in local TCP health checks
b952e083 gofmt: align imports in D3-04/D3-05 test files
38f7937c B8-10: normalize PeerShare AllowedIPs on restore like the write face
3708a289 D3-03: propagate half-close in xnet.Transport and wait for both directions instead of truncating in-flight reverse data
2b06140d XC4-B3: add tick-level test pinning the expired-share reclaim wiring in the federation cleanup loop
4a96a867 D3-04: run priority hop candidates through failure filtering and exclude failed nodes on Router retries
264d75fa C4-03: mask pure-alpha, pure-numeric and short mixed path tokens in log redaction
51ed9d0e B8-09: mirror template kind/builder/CheckTemplate gates on the restore face
673be779 XC4-B2: extract the legacy dual-push cutoff decision into a self-tested script instead of workflow inline bash
eeaddda5 A2-04 follow-up: pin the CRLF shape of get_env_var in the suite (values, quoted values and empty values read CR-free)
c5f67496 F2-03: deduplicate legacy no-seq flow batches by content hash in the accounting transaction
fad8e427 D1-01: make the lifecycle fake panel send encrypted command envelopes
ad9b4d36 A4-08: lock the e2e Python dependencies (requirements.lock plus exact pins) instead of unbounded ranges
6edbb6bf A4-07: run the CI PostgreSQL contract job on postgres:16-alpine, the major the shipped compose deploys
48d70e3a Section 9.6 mitigation: the menu-5 generated panel site block now carries a Caddy log filter that deletes the secret query parameter from the access log
a1a8da92 A4-06: declare top-level read-only permissions in ci-build.yml
707da602 A4-03: pin all container base images by digest, cover docker in Dependabot, and census image refs in the pins gate
0c329e54 B3-02: add deterministic scan fallback to inbound port picking
5d7376b2 B6-05: merge remote-usage sync into stored RemoteConfig instead of overwriting relay credentials
eb4e319a B8-08: mirror source name/interval/role gates on the restore face
e1b62495 D3-07: manage Router dial/bind attempt contexts explicitly so a successful attempt's context lives as long as its conn
bdc8aa7b A4-02: add a census floor to the workflow pins gate and mutation selftests for the five gates that had none
3828aef5 D1-04: add a mechanical panel/agent command-set contract test
e6bf9b34 A2-08: hard-fail invalid FLMX_INSTANCE (env or deployment .env) at the install, update, uninstall and migrate entry points via one shared guard, keeping the read-path fallback for pure reads
4c272d15 F1-02: fetch probe-resistance decoy with a timeout-bounded, redirect-capped client instead of http.Get
5608bb4c B8-06: clamp negative MaxConn/IPMaxConn on forward restore
a822f031 D1-06: give read-path commands a per-command budget context and refuse spent budgets
eba99b3a B1-02: reject passwords over the 72-byte bcrypt limit at set and verify faces
2c4649c2 A2-07: add an flock run lock at the panel_install.sh entry and PID-suffixed Caddy backup names so same-second reruns cannot overwrite the previous rollback copy
621ecf1b D2-05: check stopCh before first launch and keep signalling until the supervisor exits
4e020b23 B4-02: make restore port rechecks order-independent and cover chain/inbound faces
edba19ac C4-02: strip federation credentials on https-to-http redirect downgrade, not just cross-host
9fbfa1ce D2-04: bound detectVersion with CommandContext and WaitDelay instead of racing on cmd.Process
9a3ba1da A1-06: stage the daemon.json rewrite through mktemp in /etc/docker with a JSON re-parse before the atomic rename, dropping the predictable /tmp path
ef27d2c6 A3-08: make the OpenRC pidfile a single source for service template and health check
4d34138b B9-03: cap federation client success-body decoding at 1 MiB on all seven methods
b537101e D1-05: hard-reject panel-supplied checksumUrl in UpgradeAgent, mirroring downloadUrl
ce52277d A1-05: strip all trailing slashes in normalize_proxy_prefix, not just one
ea462a95 B6-06: run the full write-time validation set in stored peer resource revalidation and on resume
c859c3b6 B4-04: dedupe entry nodes in CreateForwardTx before locking
6b6d9e55 F2-02: scope inbound counter samples and baselines to the reporting node
fca51ba6 A3-07: accept the node secret via FLMX_NODE_SECRET instead of argv
e794a3ef F1-03: verify upstream TLS against the dialled target when the client sent no SNI, instead of disabling verification
fa846582 A1-04: share one channel tag grammar (release_tag_pattern) between probe_route and resolve_latest_release_tag so route probing and version parsing cannot drift
eb1a7e5f R-WS-TOKEN mitigation (audit 9.6): log only method + path + protocol in the shipped frontend nginx access log so no secret= query string can land on disk
ab7d68e3 D1-03: cap inbound command frames with a 10MB read limit on the agent WebSocket
5c8e8ba8 E2-05: stop reporting vacuous probe success when every inbound probe result is skipped
b7894d9d A1-03: validate explicit version env values against the union version grammar in resolve_version and the update_panel version chain before any URL or image-tag splicing
df5094ea E1-06: restore base64url padding in passkey decode (same as jwt.ts) and surface decode failures as a distinct data error instead of user cancellation
519e8c5c B8-05: add tunnel Type/Status enum gates to the restore face
74be3d78 E1-05: clear config revalidation state and the gist restore receipt on logout so a different account in the same tab never sees the previous session's cache
b4887bcb B2-02: propagate flow-reset DB errors instead of always reporting success
fe079282 A2-06: no-Docker uninstall now lists and deletes the same residual set as the Docker branch instead of only docker-compose.yml and .env
162b9c84 E1-04: route config page initial cache reads through the guarded cache layer and drop the sensitive key from its read list
616926e9 D2-03: cap report response body reads at 4096 bytes in both v1/config post paths
57a698ef Validate nftables target hosts at the render/apply single point (C3-01)
2705fa0e A3-06: serialize install.sh runs with a process lock
03c2d323 A3-05: only self-delete when the target is verifiably the script itself, and not on menu exit
2a130033 A2-05: menu-5 preserved rewrite strips only the target address from a shared Caddy site block and keeps the remaining addresses' block intact with a named notice
36468b3e E2-04: honour per-item failure receipts in node/tunnel reorder, roll back local order and resync from server on failure
d7ecd79f D2-02: refuse service hook execution at the executor in the managed agent build
dd4a4232 Verify nftables SSH host keys against a TOFU-pinned fingerprint (C3-02)
6abebf7e B6-01: reject node-local TLS/mitm cert file paths in peer resource configs
d7f065f1 E2-03: synthesize BatchOperationResult receipts for batch endpoints in demo mode and cover an executed batch action in the demo check
74ee693f F1-01: default to verifying TLS configs in DoT/DoH exchanger, DTLS dialer and SOCKS5-TLS connector
a61335fe T-INC-01: add host safety gate and stub-layer leak sentinel to the install script suites
b6291069 D2-01: make needWrap an atomic.Bool like its three protocol-block siblings
7b114c99 B8-02: mirror the full service-monitor write gate set on the restore face
b3b3b504 A1-02: put the Docker installer-script download under the same curl watchdog set as download_verified so a stalled get.docker.com reaches the package-manager fallback
076e92ca D1-02: reject replayed command envelopes with a bounded requestId claim set
f26b76bc A3-04: give install-over-existing the update path's downgrade, backup, smoke and rollback protections
88e16343 B2-02: propagate flow-reset DB errors instead of always reporting success
6c563b1a A1-01: give routed downloads a shared 240s total budget (FLMX_DOWNLOAD_BUDGET_SECONDS) with per-leg caps tightened to the remaining budget
47b72383 A4-04: derive the contract gate C1 census mechanically from all List* exits instead of a hand-kept registry
aa1cf7bd F2-01: scope subscription counter samples to the reporting node's served subscriptions
f6418976 C2-02: anchor Xray downloads to the GitHub API asset digest instead of the co-located checksum file
56f514e1 D3-05: fail node parsing closed on an invalid matcher rule instead of keeping an unfiltered node
89397b64 A3-03: make the second binary anchor fail closed for proxy-served downloads
944f77d8 A2-03: default-scope pinning writes volume keys only from live reads, dropping the sqlite fallback write and the postgres existence claim
d5935453 Correct route-count comment to the 204 register per audit-max ledger 9.8 (was frozen 211)
889c4e79 A3-01: require cross-route corroboration for latest version resolution
3e589010 D1-01: reject plaintext-envelope commands when a node secret is configured
6ef5ca7e E2-02: count non-JSON leftover lines as parse failures in ny forward import instead of silently discarding them
09948303 D3-02: recover per-connection handler panics in service dispatch and fix the two named panic triggers
86eeb382 A2-09 residual: pin no network key and warn on multi-network backends instead of silently pinning the legacy default
0e1dcfe7 E1-03: remove dark wallpaper key from the public brand config pipeline in site.ts and pin it in the dark-mode gate
b0e66563 A2-02: add the postgres face of the post-update volume self-check so a rebuilt PG container on an empty volume fails and rolls back
417ee8db C2-01: validate kernel versions inside the kernel package as the single point
c3dabe60 A3-02: validate explicit VERSION values before building download URLs
a7ed1eb3 A2-01 residual: tri-state legacy-volume probe so an unreadable mountpoint aborts pinning instead of reading as empty
c99a3759 D3-09: IsIPv4 only matches literal IPv4 hosts, hostnames keep dual-stack networks
bcdb7956 B5-04/F1-05: bound the goja compiled-program cache at 128 entries with clear-on-overflow
846775e5 A4-01: census flow-form and block-scalar uses: in the workflow pins gate, with a premise count check
125a848e C1-01: report pending v3/v4 migrations and name them in PendingMigrations
27d92428 D3-06: route FailFilter hot-path logging through the structured logger instead of fmt.Printf to stdout
53fbb5ef B5-03: bound goja script result count and adopted name length, pin timeout and confinement guarantees (B5-05)
6d864417 D3-01: default non-positive UDP conn pool TTL to 30s so NewTicker never panics
760b8b11 D3-08: return pooled UDP buffer on listener queue-full drop path
ca3e881f C1-02: set adminBootstrapRequired when seed admin Create fails
9d4ae2dd B4-06: document gost single-forward offline delete as accepted fail-closed manual-retry design
2db0282b NOTES: mark kernel/Xray checksum, S-05, S-06, SSRF and R-001/R-002 seed lines with audit verdicts
a614ab1b B5-06: state imported Snell v6 and negative versions are skipped for Stash/Shadowrocket in guides
e2dc29da Add pendingDelete to the recorded mine-subscription demo fixtures for the new DTO field
73f3c022 Align the gist restore repush fixture port with the node port spec gate
fe347483 Dedupe the WebAuthn origin line left doubled by the FX-D merge resolution
47ba9f5e Cover the initial admin password lifecycle and declare seeded fixture passwords via the env seam
8fd699f4 Surface the one-time initial admin password at install time and document the first-login flow
d5f5fccd Seed admin with a generated or env-provided initial password instead of the public literal
82f1bac8 Add FX-B regression tests and align restore fixtures with node port spec gate
844e6721 Rewrite port occupancy sentinel with AST predicate and handler coverage
0e780c14 Name unknown fields in backup import rejection and gist restore receipts
fa8ff802 Split provider share denial reasons and reclaim expired shares on the maintenance tick
a701ce59 Mirror write-face gates on backup restore and add expired share census
bb258c08 Gate chain strategy on tunnel write faces and stored role runtime apply
e5e6ddb8 Add kernel single-point chain strategy legal sets
c939d964 Correct rename pinning wording in the upgrade guide
eb6b9a66 Complete script-face accident gate, align env parsing with compose semantics, fix multi-network pin
f3d536b3 Complete accident-state gate matrix and converge PG bind handling in upgrade executor
935c6736 Add single-point DB_TYPE normalization shared by startup and mode checks
8785a7b3 Update config access tests to the jwt_secret rejection contract
2e8fd778 Align R2B8 full-restore fixture with the import admin invariant
2ff86f1d Run gofmt on FX-E changes
d2fa5126 Honor the GitHub prerelease flag in release channel classification
8aa9f9c8 Reject jwt_secret writes through config update with deployment guidance
3c64ff78 Fix monitoring freshness races and unify admin error responses
aa9994d0 Surface pending-delete state in my-subscriptions view
5d658bb0 Accept stored legacy subscription tokens of any length on the fetch path
ac35206d Reject user imports that would leave the panel without an administrator
9b3776e7 Extend doc consistency gate with skill method and config key checks
839be472 Remove dead dark wallpaper branch and correct data-compat comment
42d5339a Correct Snell delivery rules and theme package dark-set guidance in guides
0a477757 Align skill references and examples with current API payloads
fcfd0ff0 Rewrite skill backup and config references to match current API
7f70cf8f Drop legacy-name clearing twins in gist tests instead of renaming them onto the FLMX key
38e78e58 Document FLVX_* env retirement and the pre-upgrade rename checklist
1bec92fe Retire FLVX_* legacy env fallback reads across backend, agent, scripts and compose templates
c80c3294 Gate legacy backend repo dual-push behind a mechanical 1.9.10 cutoff
```

---

## 8. 分支回收状态

- 本报告提交后，按任务书在测试机对 `00762aba..HEAD`（含本报告笔）执行 `git bundle create`；
- bundle 经 tssh 通道拉回沙箱：`~/workspace/audit-max/flmx-max-fix.bundle`；
- 沙箱 git 可写性已探（/tmp 临时仓 init＋commit 成功），遂在 `~/workspace/flvx-max` 内 `git fetch` 该 bundle 新增本地分支 `fix/full-audit-20261105` 并 rev-parse 对账（不 force 任何既有分支；沙箱仓原分支 feat-latency-hint 未动）。
- 精确哈希与对账结果见提交后主控回收清单（本节为方法登记，避免报告内自引未定哈希）。
