# R18 总账（X-ledger）

基线：main @ a0c18a0（`a0c18a0dc90b2f9c7a67e8a2a016cc1d97505fb5`，R17 修复 fix-r17 已合入并推 origin）。审计轮：靶向复审轮。五路：L1 R17-X-01 关闭验证、L2 管理 API 盲 append 核查、L3 哨兵 + 轮换域 A4、L4 常设全集、L5 未验证清单复核。

## 一、R17 关闭验证：1/1 关闭

| 项 | 结论 | 关键新证据 |
|---|---|---|
| R17-X-01（P1，gost 重复写致 agent 重启自砖） | **CLOSE** | 机理 PASS（第 13 服务 ParseService `ln.Init` eager 绑定报错，沙箱重跑 `TestLoadPoisonedConfigFailsWithDuplicate` 实测 `bind: address already in use`）；验收四项：①确定性去重 PASS（单元重跑 12/12）②启动自愈 PASS（毒化原件一次起成、NRestarts=0，journal 警告行在位；对照组 failed、NRestarts=5）③harness 10/10 MISS PASS（本轮重跑 cycles_done=10 hits=0）④计时 8 腿全过（VLESS 5.26s、VMess 16.06s、SS 15.04s、Trojan 5.09s、三对照 STILL_OPEN@30s）；R17D-OPEN-01 已解（见 V-01） |

## 二、本轮新立案：产品 P3 = 4，P0/P1/P2 = 0

| 典则编号 | 来源 | 内容 | 级别/面别 |
|---|---|---|---|
| R18-X-01 | L3/A4-008 携入 | `cleanupForwardsDataPlane` gost 分支（`tunnel_delete_resolution.go:766-780`）仍逐条先清数据面后删库，中途硬失败时已删转发数不披露（回执只称「隧道未删除」） | 产品 P3 |
| R18-X-02 | L2 | `x/api/config_service.go:85`（`createService`）与 `:171`（`createServices` 第 4 阶段）盲 append，管理 API 三启用门全关故不可达；已在基线二进制实测复现（暂停→重启→同名重建→`svc-a` 计数 2，R17-A 自愈随即去重回 1 并告警） | 产品 P3 |
| R18-X-03 | L2 | x/socket 3 处盲 append（`chain.go:35`、`limiter.go:29`、`:118`），经面板 WebSocket 常规下发可达（`AddChains`/`AddLimiters`/`AddCLimiters`）；触发机理未立（变更命令单队列串行、无 paused 式漂移）；启动自愈现仅覆盖 Services | 产品 P3 |
| R18-X-04 | L2 | x/api 其余 15 类 create 盲 append（admission/auther/bypass/chain/conn_limiter/hop/hosts/ingress/limiter/observer/rate_limiter/recorder/resolver/router/sd），同一不可达 API | 产品 P3 |

去重账：五路原始候选 4 件产品立案 + 1 件流程建议 + 6 条观察，典则编号无撞号（X-01 由 L3/A4 占用，L2 建议号 X-02/X-03/X-04 直接采纳；L5-CAND-01 转流程项 R18-PROC-01，不占产品编号）。

## 三、流程项：1 项

| 编号 | 内容 | 级别 |
|---|---|---|
| R18-PROC-01 | 测试机多 lane 并行互相污染（L5 Phase A 窗口内他 lane 上线 r17d 面板、agent 二进制三次切换、3 次非本路重启；本路 6 次重启亦可能污染他 lane 证据）。R19 须对测试机实行 lane 间互斥排期；L5 items 2–5 与 kill-freeze 改期到独占窗口重跑 | 流程 P1（不计产品收敛判据） |

## 四、观察项（不立案）

- R18-OBS-01（L1）：leg2 对照组第二重启 burst，触发源未定位，不影响结论。
- R18-OBS-02（L1）：VM-1 静默腿 2.13/2.15s 略高于 O-148B-1 带宽 2.0s，INT 的 1.14s 未重现，不计 FAIL。
- O-L3-1：dedupe first-wins 仅启动去重面。
- O-L3-2：`guardedAddService` skip-confirmed 分支生产不可达。
- L4 三条：`NodeApiItem` 未具名声明 latency 系字段（靠索引签名兜底）；`panelVersion`/`versionWarning` 由 `/node/list` 发送但全前端 0 读（冗余发送）；`latencyTargets` 无自选则键缺失语义与 1.4.8 口径一致、渲染已对齐。
- R18-L5-CAND-02：A1 瞬态重复是 OPEN-01 窗口的首次直接捕获，支持「随后 persist 覆盖清理」假说方向，OPEN-01 本身仍按 L1 结论已解（见 V-01，不重复立案）。

## 五、哨兵与常设全集

- 哨兵 53 行：**51 PASS / 0 FAIL / 2 承继（S-16、S-34）/ 0 回退**。S-41 PG 子格同口径承继，行级不降格。
- 常设全集六项差集全部为 ∅：附则 A 60 格、A-2 11 行、版本门 13 行（VG-09 沿袭未验证）、ENC 文法 99 格、路由 202 / 表 49、消费面 22 CELL；列表/详情字段对账差集 ∅。
- 轮换域 A4：除 R18-X-01 外新增 0；A4-001…A4-007 已闭合，A4-009/A4-010 疑似项结构性消除。
- R13 关闭 7 项与 FINAL-P2-1 七段谓词维持关闭。

## 六、未验证清单（L5，§10 九项）

已验证 PASS 1 项（item 6 管理 API 两处行为腿 2/2）；仍未验证 4 项系排期阻塞（items 2–5：VLESS 用户凭证腿、SS 直连腿、Trojan 最小对照、下游供数/1MB 批量腿，改期独占窗口重跑）；item 1（OPEN-01 微观机理）部分进展（瞬态重复首次直接捕获，14 服务、`6_1_0` 对各 2 份，存活约 160ms，md5 与 R17 毒化原件逐字节一致）但 sent-ok 瞬间冻结未达成；items 7/8/9 仍未验证（远端/非 Debian 内核、浏览器 DOM 腿、订阅 1 流量计数器响应性）。

## 七、计数汇总

- 产品面新增：P0=0、P1=0、P2=0、P3=4。
- 文档面新增：0。测试基建新增：0。流程基建新增：1（R18-PROC-01）。
- R17-X-01 关闭：1/1。测试机离场对账：1/4/6/578/18、表 50、agent `aff338e3…`、gost `d48f09e1…`、unit `a12afd9c…`（null）、镜像仅剩 1.3.5、磁盘 3.6G（以 L1 最终对账为准）。

## 八、三方对账

总账 ↔ R17 FIX-PLAN（F-01 = R17-X-01）↔ 五路原始候选：差集 ∅。R17 FIX-PLAN 唯一修复项已关闭；五路候选 4 件产品立案全数入账，无遗漏、无重复编号；流程建议 1 件转 R18-PROC-01；观察 6 条登记在案不立案。
— R18-X-ledger END · 自证：本行之前正文字节数 5626 · END
