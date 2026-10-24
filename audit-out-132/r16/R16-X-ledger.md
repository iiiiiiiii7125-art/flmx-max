# R16 总账（X-ledger）

基线：main @ b54b44f（完整提交 b54b44f772d8b4efdd6d08b1fbbbc40c8091a143；R15 修复 fix-r15 已合入并推 origin）。五路报告全部在位，字节数与尾行 END 已逐一验盘（见 §6）。

## 1. R15 关闭验证：2/3 关闭，1 未关闭，0 未验证

| 项 | 内容 | 结论 | 关闭证据（本轮新证据） |
|---|---|---|---|
| R15-X-01 | errors.md:15 的 500 行失实（文档 P3） | **CLOSE** | L1：500 行已删、历史注记在 :16；全仓 envelope `code: 500` 发射点 = 0（含测试文件；唯一命中为 `doc/audit/J-api.md:66` 历史审计散文）；三处 raw `http.Error`（kernel_relay.go:846、gh_mirror.go:321,327）逐一核对排除；其余八码逐码有发射点；check-doc-consistency.sh 重跑 PASS exit 0。L3 独立复核：九码均有发射点、`500` 零发射点、码表与发射点一致，维持关闭。 |
| R15-X-02 | demo 夹具缺 `backup/gist/status` 录制（产品·演示面 P3） | **CLOSE** | L1：record_items.mjs:24 直接调用在位；两内容闸含 gist-status 形状键；两静态闸本轮 exit 0；重录取证 80 键变、159/159 条目 ts 全变、唯一非监控新增键即 `backup/gist/status`；本轮走查 Run A exit 0（`demo ok: 14 admin + 5 user pages`，chromium-1187）；修复前复现 Run C exit 1、唯一 FAIL 逐字相同。 |
| R15-FIX-01 | 入站页空指针 probe=null 读 null.ok（修复中发现） | **OPEN（未关闭，结转 R17）** | L1：renderInbound 腿已闭合（修复在位；敏感性探针 Run B：旧代码+新夹具复现 20 problems，首因 `null.ok`）。但关闭条件“确认全仓同型仅此一处”不成立——同型搜索发现第二处未修复位点 inbound.tsx:1020-1024 `sheetProbe`（见 R16-X-01）。R15-FIX-01 本身结转未关闭；第二处位点单独立案 R16-X-01，不与 R15-FIX-01 合并计数。 |

- V-06 三处归档在位核对（L2）：REA-UDIT 机理订正、R14-00-routes-raw.csv（idx 60/109 均为 GET,HEAD）、R14-00-baseline.md（:38 分布行已改）——均以 `git show b54b44f:` 直读提交树核对在位；diff 5bd5a90..b54b44f 恰 9 文件、504 行新增 / 3 行删除，无多余、无缺失。V-06 关闭。
- R15-FIX-REPORT.md 去向（L1 修正协调者基线注记）：该文件以**未入库**形态存在于修复 worktree `~/workspace/.fixr15/audit-out-132/r15/`（9,164 字节，sha256 `fda006d9d0e18fb5ae6a96d36902c90aba7f1d368f7aeddf2df0b2982fb761da`，b54b44f 提交后 3 秒落盘），不在共享工作树、不在任何提交历史中。本轮已将其原样归档至 `audit-out-132/r15/R15-FIX-REPORT.md`（同字节数、同哈希，内容未改）。其 §3“全仓同型仅此一处”结论与本轮探针不一致，以本轮探针为准（见 R16-X-verdicts V-04）。

## 2. 本轮新立案：3 项（去重后 3 项，无撞号）

| 典则编号 | 内容 | 级别/面别 | 立案来源 |
|---|---|---|---|
| R16-X-01 | 入站页 `sheetProbe` 与修复前旧式逐字同构，probe=null 时读 null.ok 致详情页崩溃（inbound.tsx:1020-1024；点击探针 Run D 实测复现，页面文本归 0、`TypeError: Cannot read properties of null (reading 'ok')`） | 产品·演示面 P3 | L1（L1-CAND-01） |
| R16-X-02 | `userList` 分页乘法溢出可致 slice panic（handler.go:740，`(req.Current-1)*req.Size` 无上溢防护；两溢出形实测均 panic，两对照均 code 0） | 产品 P3 | L3（L3-CAND-01） |
| R16-X-03 | `inboundProbeView` 为死代码，注释失实（node_latency.go:460-484；生产调用方为 0，仅测试引用；实际列表走 kernel.go:716-718） | 产品 P3 | L1（L1-CAND-02） |

去重账：五路原始候选 4 件（L1-CAND-01、L1-CAND-02、L3-CAND-01、L5-CAND-01）→ 立案 3 件。L5-CAND-01（served-chunk 静态证据过期，浏览器任务前须以 b54b44f 重定基线）为观察项、不单独立案（见 X-verdicts V-03）。面别计数：产品面新增 3 项（P3=3）、文档 0、测试基建 0、流程基建 0；**产品面新增 P0/P1/P2 = 0、P3 = 3**。

## 3. 三方对账

- 总账（R15 2 关闭 + 1 未关闭 + 本轮新立 3 项）↔ R15 FIX-PLAN（F-01/F-02）：F-01、F-02 均已在 583cec0 执行，关闭证据逐项对上修复条目，差集 ∅；R15-FIX-01 在同一提交内修了 renderInbound 一腿，sheetProbe 一腿未修，差集为 R16-X-01（已立案）。
- 总账 ↔ 五路报告原始立案：原始候选 4 件去重为 3 项立案 + 1 项观察（L5-CAND-01），每件去向均有对象级依据，差集 ∅（无一立案被无故丢弃）。
- 总账 ↔ R16 FIX-PLAN（F-01…F-03）：3 项新立案逐项对上修复条目，差集 ∅。
- 级别列与面别列：总账每行均含级别与面别。

## 4. 哨兵与常设全集

- 哨兵 53 行：**51 PASS + 2 本地不可判定·承继（S-16、S-34；S-41 的 PG 子格同口径承继），0 回退**（L3）。handler 全量 1306 PASS / 0 FAIL / 4 SKIP（ok 458.383s，与 R15 逐数一致）；kernel 264 PASS；repo 234 顶层 PASS；netguard 8/0；PG 契约靶向 3 SKIP（无 DSN）。
- 常设全集（L4）：附则 A 60 格、附则 A-2 11 行、版本门 13 行（VG-09 沿袭未验证）、ENC 文法 99 格、路由 202 / 表 49、消费面抽核 22 CELL——**六项差集全部为 ∅**，新立案 0。R4×C8 snell shadow-tls 伴生本轮已单列补格，R4 至此无未单列伴生。TableName 48 之谜解开：第 49 个模型为 repo 包 `NodeSeq`（node_seq.go:28 自带 TableName()），AutoMigrate 49 项无一缺定义，非漂移。
- 轮换域 A2 全量抽审（L3）：路由 202 / 表 49 双向差集 ∅；鉴权分布与方法守卫分布与 R15 逐字一致；批量语义与分页抽核通过；R1 深审 A2 遗留 5 项按对象复验已修在位（A2-04 本轮未复核，结转）。A2 域内新立案 1 项（R16-X-02 userList 分页溢出）。
- R13 关闭 7 项维持关闭 7/7（按对象本身复核）；FINAL-P2-1 七段谓词维持关闭（行为测试 4/4 PASS）。

## 5. 未验证清单复核（L5）

- 仍未验证：真实 Gist 成功上传/下载往返（无真实令牌，未索取；mock 链 22/22 仅为机制证据）；单族 sock_diag 失败警告形态（单元覆盖在位，生产代码零 env hook，唯一缝为进程内测试变量）；远端节点与非 Debian 内核；生产面板 DOM 鼠标实点（须父代理侧浏览器任务）。
- VMess/SS/Trojan 计时腿：**登记须重跑**。`7244fbb..b54b44f` 触及 render_xray.go、chain.go、split.go、model.go、handler/kernel.go、handler/kernel_chain.go 共 6 个渲染文件，不满足“零改动”承继条件，148B 旧证据不承继；`5bd5a90..b54b44f` 渲染路径零改动。已写明非干涉子分析（直连 Xray 入站下发物逐 hunk 核对行为等价）——代码读数结论，不得顶替重跑。

## 6. 五路报告验盘

| 报告 | 字节数 | 尾行 END |
|---|---|---|
| R16-L1-product-closure.md | 20,489（正文 20,409 + END 行） | 在位 |
| R16-L2-v06-docs-closure.md | 8,906 | 在位 |
| R16-L3-sentinel-a2.md | 31,313 | 在位 |
| R16-L4-matrices.md | 30,010 | 在位 |
| R16-L5-unverified.md | 18,290（正文 18,211 + END 行） | 在位 |
| R16-00-baseline.md | 1,394 | 在位 |
| R16-00-sentinel.md | 26,287 + R16 前言（承 R15 53 行逐字沿用） | —（表文件） |

## 7. 观察项（不立案，结转）

- L5-CAND-01：R15 served-chunk 静态证据（58/58，5bd5a90 构建）因 fix-r15 唯一产品改动 inbound.tsx 而过期；浏览器任务执行前须先以 b54b44f 构建重定 served chunk 基线。
- L3 O-L3-1：FIX-PLAN-132.md 的 A2-01/02/03/05/06 行过期（代码已修，计划仍标「待修」）。
- L3 O-L3-2：S-51 徽标 b54b44f 实测 `:672/:843/:1403`（5bd5a90 为 `:666/:837/:1397`，+6 恰为 R15 探针空值修复 hunk 所致）；R15-L3 所引行号与 5bd5a90 实测差 2 行，属该报告引用偏差。
- L3 O-L3-3：S-39(c) 注释行 +1 漂移，内容未变。
- R15 观察项结转：L1 OBS-1（Xray 25.6.8 对 `listen: ""` 入站 `-test` panic，生产不可达）、L1 OBS-2（Xray 25.9.5 沙箱内不可得）、L3 O-R15-A5-1（VG-11 R14 考古计数不复现，属引证问题）、O-R15-A5-2（nodes.md:272 括注歧义）；L4 保留：VG-09 Snell 服务端仍未验证、VG-10 指针漂移 `model.go:209→:211`（值未变）。R4×C8 本轮已闭合，不再结转。
- L4 注记：ghfast.top 现对 XTLS 返回 403 `Forbidden by black list`（Xray 25.8.29 本轮直连 GitHub 重下，构建哈希与 R15 一致）。

## 8. 收敛判定

本轮新增产品 P0/P1/P2 = 0、P3 = 3，哨兵零回退。按用户口径“一轮零新增产品问题”才收敛：**本轮未达成收敛**。R15-X-01、R15-X-02 已关闭，R15-FIX-01 未关闭结转；下一轮 R17 在修复（R16 FIX-PLAN F-01…F-03）后重审，不发版。

R16-X-ledger END
