# R19 总账（X-ledger）

基线：main @ d66042c（`d66042cc0b7ac011ddfad09f3f2709b90dafda2e`，R18 修复 fix-r18 已合入并推 origin）。审计轮：靶向复审轮。五路：L1 R18 四项关闭验证、L2 未决缺口裁定、L3 哨兵 + 轮换域 A6、L4 常设全集、L5 未验证清单复核。

## 一、R18 关闭验证：4/4 关闭

| 项 | 结论 | 关键新证据 |
|---|---|---|
| R18-X-01（P3，删隧道回执已删数披露） | **CLOSE** | 沙箱 9/9 绿 + 7/2 红重构；真机成功删（t1，code 0，服务 16→12）+ 离线删/重连对账（t2：停机时 DB 行删、孤儿服务残留，重启后面板下发 DeleteService，监听消失） |
| R18-X-02（P3，x/api 两处 services 盲 append） | **CLOSE** | 17 处 upsert 在位；三启用门 0 行 diff；`&ps.config` 别名已修；沙箱三形 + helper 绿 |
| R18-X-03（P3，x/socket 三处盲 append + 自愈扩展） | **CLOSE** | socket 3 处去重 + dedupe.go 五类 Heal + program.go 五路接线在位；沙箱 23/23；真机毒化 4 类一次起成（NRestarts=0，4 条 Warn 逐类点名）、WS 三次 AddCLimiters 落盘一份、SIGHUP 后 PID 不变 |
| R18-X-04（P3，x/api 其余 15 类 create 盲 append） | **CLOSE** | 15 类 create 各 1 处 upsert 在位；沙箱 15/15 子测绿 |

## 二、本轮新立案：产品 P2 = 1、P3 = 2，P0/P1 = 0

| 典则编号 | 来源 | 内容 | 级别/面别 |
|---|---|---|---|
| R19-X-01 | L2-CAND-01（+ L1-CAND-01/02 同物并入） | 删隧道披露残留：nft 分支删库失败在 `tunnel_delete_resolution.go:789` 返裸 err；`deleteTunnelByID` 失败在 `:832` 与 `mutations.go:2609` 同样裸返，注释自认「转发已全删」却只补刷白名单、不披露 | 产品 P3 |
| R19-X-02 | L2-CAND-02 | `update*` nil panic 全族 + `OnUpdate` 锁泄漏：沙箱实测 5/5 panic（updateChain/limiter/connLimiter/updateServices/deleteChain）；同族约 49 处；loader 对 17 类中 16 类 nil 即 panic，落盘一个 JSON null 即可致启动崩溃循环；`OnUpdate` 非 defer 解锁，panic 被 recover 后 globalMux 写锁永不释放，后续 `config.Set` 实测死锁 | 产品 P2 |
| R19-X-03 | L1-CAND-03 | 原始 `gost.json` 即含孤儿 `rule_conn_limit_12`（对应 forward 12 已不存在），疑似删转发未清 climiter；机理未证实（R20 须先核删转发是否清 climiter 再定修法） | 产品 P3 |

## 三、流程项：1 项

| 编号 | 内容 | 级别 |
|---|---|---|
| R19-PROC-01 | 测试机互斥排期复发（R18-PROC-01）：L5 在窗口 1 内覆盖 L1 的锁文件（02:56:52Z）并于 03:18–03:20 部署、三次重启 agent；L1 亦在 03:11–03:24Z 持续操作。根因是锁文件非原子（可静默覆盖）+ 无操作前归属复核。R20 起锁抢占原子化（`mkdir` 式）+ 每次重启/部署前复核锁归属 | 流程 P1（不计产品收敛判据） |

## 四、去重账

五路原始候选：产品 5 件（L1-CAND-01/02/03、L2-CAND-01/02）+ 流程 1 件（L1-CAND-04）+ 观察 6+ 条。典则编号：L1-CAND-01 与 L1-CAND-02 与 L2-CAND-01 为同物（nft 分支 + deleteTunnelByID 两处裸返），并入 R19-X-01，典则取 L2 口径；L2-CAND-02 → R19-X-02；L1-CAND-03 → R19-X-03；L1-CAND-04 → R19-PROC-01。无撞号、无遗漏。

## 五、观察项（不立案）

- R19-OBS-01（L2，高优先级）：`TestR1B3TunnelDeleteOnlineRealFailureAborts` 撒谎——提交态 PASS 但伪装节点命中 0 次（SSRF 拦截代行了 `success=false` 分支）；补 `FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true` 后仍 PASS 且命中 1 次，一行修复已验证。行为本身由 R18-F01 的 9 个测试真实覆盖；契约测试注释明言该行为由本测试「钉死」，故 FIX-PLAN 以测试基建项 T-01 顺手补 env 加命中/文案断言，不占产品编号。
- R19-OBS-02（L5）：VLESS 1MB 批量 10.21s，约为 VMess/SS 的 3 倍；单点观察值，建议 R20 单独立项核查。
- R19-OBS-03（L4）：R18 §7.1 算术误记订正——`ListNodes()` 恒发实为 27 键非 26、`NodeApiItem` 具名实为 28 字段非 27，节点列表总可能键为 32 非 31；结论不变。
- R19-OBS-04（L1/L2/L5 交叉）：F-01 报告所称红态 `6 FAIL / 1 PASS` 在树内无红日志、按字面不可复现（纯 a0c18a0 产品文件 + 新测试无法编译，`undefined: formatDeletedForwardsSummary`）；R18-X-01 关闭不依赖该数字（依据为本轮绿测 + 7/2 红重构 + 真机腿）。证据级注记，不立案、不阻塞关闭（同 R18 V-06 口径）。
- O-L1-2…O-L1-6、O-L3-1（A6-观察-01，SIP023 多段残余按节点来源拆分登记，承 R14）、L4 三条（NodeApiItem 未具名 latency 系字段；panelVersion/versionWarning 全前端 0 读；latencyTargets 无自选键缺失语义与 1.4.8 口径一致）：均维持观察，不立案。
- R18-OBS-01 / OBS-02 已解决（L5）：第二重启 burst 周期 1 = L1 毒化重启 + systemd on-failure 循环、周期 2 = L5 自身 reset-failed+restart 跨 lane 干扰，11 条 Started 算术闭合；VM-1 1.13s/1.10s 复现 R17 的 1.14s 重测值（冷启动 ~2.1s / 热启动 ~1.1s 双态）。
- OPEN-01 微观机理维持 SOLVED（R18 V-01）：L5 B2「重复可持 42s 且非总为 6_1_0」抵的是 INT 早已作废的「49ms 瞬态」全称假设，不抵 restore 成功/失败分支机理（R18 已以 3_5_3 全周期复现定位）；不重开。

## 六、哨兵与常设全集

- 哨兵 53 行：**51 PASS / 0 FAIL / 2 承继（S-16、S-34）/ 0 回退**。S-41 PG 子格同口径承继，行级不降格。
- 常设全集六项差集全部为 ∅：附则 A 60 格、A-2 11 行、版本门 13 行（VG-09 沿袭未验证）、ENC 文法 99 格、路由 202 / 表 49（方法口径，见 §九）、消费面 22 CELL；列表/详情字段对账差集 ∅。
- 轮换域 A6：新增 0；相对 R18 基线 A6 文件差集 0 行；B-01…B-24、C-12…C-16 全项复验通过。
- R13 关闭 7 项与 FINAL-P2-1 七段谓词维持关闭。

## 七、未验证清单（L5）

已验证 PASS：item 2（VLESS u1/u4 腿）、item 4（Trojan 最小 A/B）、item 5（供数/1MB 批量腿）、item 9（订阅流量计数器响应性）；kill-freeze 部分达成（K1 首次抓到 sent-ok 并当毫秒 SIGKILL，冻结文件 12 服务无重复；K2 超时，sent-ok 只在 fallback 路径触发）。仍未验证：item 3（SS 直连腿——方法失效，自写 AEAD 客户端明文长度前缀致会话不建立；SS connIdle 行为由供数腿 SS-2 15.03s 交叉验证 PASS，R20 须换正确方法重跑）、item 7（远端/非 Debian 内核）、item 8（浏览器 DOM 鼠标实点；served-chunk 基线已重定至 d66042c）、OPEN-01 微观机理余项（B2 之后仍未闭合，见 §五）、item 6 沿用 R18 结论（2/2 PASS，未重跑）。

## 八、计数汇总

- 产品面新增：P0=0、P1=0、P2=1、P3=2。
- 文档面新增：0。测试基建新增：0（R1B3 以 T-01 跟进，不占编号）。流程基建新增：1（R19-PROC-01）。
- R18 四项关闭：4/4。测试机离场对账：node 1 / tunnel 4 / forward 6 / inbound 578 / subscription 18；agent `aff338e3…`、unit `a12afd9c…`、gost `d48f09e1…`（328 行，12 服务）、面板 1.3.5 healthy；磁盘 3.1G 空闲（L5）/ 3.2G（L1 时点，差异为构建缓存与备份目录）。

## 九、基线口径订正（非产品变化）

- 数据表数：任务书「49 表」滞后。L1/L5 两次普查均为 50（不含 `sqlite_sequence`）；多出的一张是 `node_traffic`，为 d66042c 产品迁移表 + 历史回填（11 小时桶），非测试残留（L5 已核；`repository.go` 在位）。L4 原料表 49 行是其普查方法未含 `node_traffic`（与 R18 原料表同方法、双向差集 ∅，回归有效性不受影响）。** standing 值自本轮起改为 50（不含 sqlite_sequence）；R20 起 L4 普查方法须纳入 `node_traffic`。**
- 节点列表键数：见 R19-OBS-03（27/28/32）。

## 十、三方对账

总账 ↔ R18 FIX-PLAN（F-01…F-04 + P-01）↔ 五路原始候选：差集 ∅。R18 FIX-PLAN 四项产品修复已全部关闭；流程项 P-01（互斥排期）执行复发，转 R19-PROC-01；五路候选 5 件产品立案全数入账（3 件典则，2 件并入），无遗漏、无重复编号；观察项登记在案不立案。
— R19-X-ledger END · 自证：本行之前正文字节数 8326 · END
