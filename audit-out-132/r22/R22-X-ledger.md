# R22 总账（X-ledger）

基线：main @ b823421（R21 修复 fix-r21 已合入并推 origin）。只读审计，未改产品代码、未打 tag、未发版，main 仍在 b823421。

## 一、五路交付（均验盘在位）

| 路 | 报告 | 字节数 | 结论 |
|---|---|---|---|
| L1 R21 关闭验证 | R22-L1-close-r21.md | 21,215 | R21 三项全部 CLOSE；新立案 0 |
| L2 R21 结转复核 | R22-L2-carryover.md | 26,705 | 维持关闭/登记；新立案 0 |
| L3 哨兵 + A9 | R22-L3-sentinel-k.md | 60,947 | 哨兵 51 PASS + 2 承继，0 回退；A9 新立案 2（产品 P3×1、文档 P3×1） |
| L4 常设全集 | R22-L4-matrices.md | 52,993 | 六项差集全部 ∅；新立案 0 |
| L5 未验证清单 | R22-L5-unverified.md | 27,039 | K2/Trojan 维持 PASS；新立案 0 |

五路报告合计 188,899 字节，尾行 END 均在位；探针原始日志同目录落盘。

## 二、R21 关闭验证：3/3 关闭

| 项 | 级别 | 结论 | 关键新证据 |
|---|---|---|---|
| R21-X-01 引擎切换孤儿 limiter | P3 | CLOSE | 沙箱 10 项先红后绿（红 6 FAIL/4 PASS，X-01 两项 FAIL 文案 `expected exactly 1 DeleteCLimiters, got 0` 与立案机理逐字一致；绿 10/10）；真机切 realm → gost 0 服务/0 limiter/0 引用，journal 恰 1 条 `DeleteCLimiters {"limiter":"rule_conn_limit_8"}`（13:52:09）；切回 gost → limiter 重建；离线切换守卫 0 次 DeleteService/DeleteCLimiters |
| R21-X-02 离线更新孤儿重连清理 | P3 | CLOSE | F-02 选方案 ①（重连重推按“当前无限流 ⇒ 失势”补删，非字面传旧快照）；代码 pin 死（`upgrade.go:885–893/937–951`、隧道/转发重试补删 `:985–993/:1005–1013`；`control_plane.go:2074` climiter 只由当前记录赋值）；口径逐字核对（`control_plane.go:505–509` 的“留待下次成功同步”现为真；R20 报告 §6.2 同句为历史记录未被误改）；真机按 R21 Leg B 同序列：转发 9 离线更新去限流 code 0 带“不在线”警告、gost md5 不变，重连后 2 秒即重推，恰 1 条 `DeleteCLimiters {"limiter":"rule_conn_limit_9"}`，自愈 0 提及 |
| R21-X-03 force-delete gost 路径清理 | P3 | CLOSE | 沙箱先红后绿（红相主回归 `expected DeleteService from force-delete (gost path), got 0`）；真机转发 10（maxConn=5，端口 32584 在监听）force-delete code 0 → gost 4 服务、只剩 `rule_conn_limit_8`、端口停监听；journal 同秒先 `DeleteService ["10_1_0_tcp","10_1_0_udp","10_1_0"]` 后 `DeleteCLimiters {"limiter":"rule_conn_limit_10"}`，各恰 1 条；普通删转发回归（转发 11）PASS |

## 三、本轮新立案：产品 P3=1、文档 P3=1，P0/P1/P2=0

| 编号 | 内容 | 级别 | 对象 |
|---|---|---|---|
| R22-X-01 | `generateCaptcha` 死导出指向未注册端点 `/captcha/generate` | 产品 P3 | `vite-frontend/src/api/index.ts:382` |
| R22-X-02 | `forward.md:67` 宣称的「节点页的 Pending Delete 管理接口」不存在 | 文档 P3 | `doc/guide/forward.md:67` |

证据等级：X-01 代码 pin 死 + 全集核对（190 个 API 调用点中唯一未映射者；后端 `grep -rn captcha/generate go-backend` 0 命中，`handler.go` 仅注册 `POST /captcha/check`（:292）与 `POST /captcha/verify`（:293）；`grep -rn generateCaptcha vite-frontend/src scripts/` 仅定义本身，零调用点；自 v1.0.0（`04f333c`）即已存在导出，后端从未有对应路由）。X-02 前端 `grep -rn pending-delete vite-frontend/src` 0 命中、`node.tsx` 无此 UI、`api/index.ts` 无 `pending-deletes` 封装；唯一真实接口是原始 API `POST /api/v1/node/nftables/pending-deletes`（`handler.go:324`）；`nodes.md:231-246` 明言 pending 行“只能通过上面的 pending-deletes 接口查看”，与 `forward.md:67` 的“节点页的…管理接口”直接矛盾。

## 四、去重账

五路原始候选 2 件 → 典则 2 项，无撞号、无遗漏、无并入：
- L1 候选 0；L2 候选 0；L3 候选 2（CAND-01/02）→ 典则 R22-X-01/02（按 CAND 号直取）；L4 候选 0；L5 候选 0。
- L3 观察项 O-K-1…O-K-5、L1 OBS-F03-1、L2 观察项 8 项、L5 `inbound/list` password 回显观察均登记不立案（见 §六），与两项典则无重叠。

## 五、三方对账：差集 ∅

总账（§二 3 关闭 + §三 2 新立）↔ R21 FIX-PLAN（F-01/F-02/F-03）↔ 五路原始候选：R21 FIX-PLAN 三项产品修复已全部关闭；五路候选 2 件立案全数入账，无遗漏、无重复编号；观察项登记在案不立案。

## 六、观察项（登记在案，不立案）

- OBS-R22-L1-01（承继 OBS-F03-1）：force-delete 审计日志 `residue_note` 静态文案仍称“可能残留”，清理已实际下发。
- O-K-1：`verifyCaptcha`（`api/index.ts:383`）亦为死导出（零调用点），但其端点 `/captcha/verify` 已注册（`handler.go:293`），无 404 风险；与 `generateCaptcha` 同批清理即可。
- O-K-2：`forward.tsx` `getStatusDisplay` case 2（「删除中」）经 `forwardList` 不可达——`ListForwards` 恒过滤 status=2（`repository.go` R5-B3 注释在位），pending 行在列表中永不出现；纯防御分支（CAND-02 关联）。
- O-K-3：`limit.tsx` 停用 chip 仍为 danger 色（`:193`），纯观感。
- O-K-4：L3 首版 handler/repo 套件在 13:43 与 13:39 先后中断（疑似五 lane 并行下的 OOM/误杀，未证实）；改用 runtime 追踪的后台 exec 顺序重跑后全绿，无遗留问题。
- O-K-5（安全·须跟进）：L3 计数腿调试命令因 shell 引号断裂，把面板密码原值回显进一次 exec 调用的 stderr（仅 lane 会话的工具输出可见）；已核查该值未写入任何日志或报告文件（`R22-L3-counts.log`、`R22-L3-served-chunk.log` 均已 `grep -c` 核查为 0 命中；本报告亦不含该值、JWT 与订阅 token）。**建议轮换测试机面板密码**（凭据在 `~/workspace/w10-run/panel-cred-new.txt`，600）。——主控跟进，不属修复轮。
- OBS-R22-L5-01：`inbound/list` 对已认证管理员原样返回 `settings.password`（L5 登记，不立案；是否收紧回显策略由主控定）。
- L2 观察项 8 项：OBS-R21-L1-01 与 R20-OBS-01 的 3.1s/60s 现象在 xray 26.10.21 栈上均未复现（VMess 5→10.02s、10→20.02s；SS blake3 驱动侧 21.03s、dummy 20.02s；aes 对照 10.01s——全部落 (N,2N] 窗口），维持登记、不立案；R20-OBS-02/03/04/05 与 OPEN-01 逐项 fresh 核对后维持登记、不立案（OPEN-01：guard 文件在 7d93792→b823421 零改动，R21-L5 K2 PASS 口径不变）。
- L2 ⑦：DeleteCLimiters 瞬时失败无重试——维持登记（单次发送+记日志，设计内 best-effort；本轮全部首发即达）。
- L4 指针漂移（均有因、句文逐字一致）：VG-10 `model.go:211→216`、前端 `:915/:917→:930/:932`；VG-11 `nodes.md:215→217`；ENC 门 `:2007/:2032/:2118→:2008/:2033/:2119`、函数定义 `:1997/:2031/:2083→:1998/:2032/:2084`。
- L3 O-A8-2 沿袭：Alpine 容器级套件本地未跑，CI 代偿，承 R9。

## 七、哨兵与常设全集

- 哨兵 53 行：51 PASS + 2 本地不可判定·承继（S-16、S-34；S-41 的 PG 子格同口径承继，行级不降格），0 回退。全量套件 0 FAIL（handler 1355 PASS、kernel 436、repo 267、netguard 8、PG 契约 243；gost 与 gost-x 全包 ok；安装套件 exit 0；前端构建 exit 0，dist index md5 = served `57ab941e…`）。handler 算术 1342+13=1355 闭合（fix-r21 11 项 + connIdle-unlimited 2 项，0 删减）；kernel 433+3=436（connIdle-unlimited 3 项）。
- 常设全集六项差集全部为 ∅（附则 A 60 格、A-2 11 行、版本门 13 行（VG-09 未验证沿袭）、ENC 文法 99 格、路由 202 / 表 50、消费面 22 CELL）；列表/详情 24 接口对账差集 ∅；轮换域 A9 新立案 2（R22-X-01/02）；R13 关闭 7 项与 FINAL-P2-1 七段谓词维持关闭。
- 口径：数据表 50 张 = 49 张 AutoMigrate（含 node_traffic）+ 迁移创建的 schema_migration（R20 V-06 口径，本轮复证）。

## 八、未验证清单（结转 R23）

1. 远端节点与非 Debian 内核——需第二台节点机 / 非 Debian 内核靶机。
2. 浏览器 DOM 腿鼠标实点——需浏览器任务能力；served-chunk 已重定至 b823421。
3. 真实 Gist 端到端 + 真机恢复后节点终态——需真实令牌（不得向用户索取）/ 真机恢复腿。
4. 单族 sock_diag 真机警告形态——需无 IPv6 inet_diag 支持的内核靶机；本机内核（6.1.0-53）已用直接 netlink 证据证明不可触发（AF_INET6 在 v6 禁用时仍 DONE-success，7 条）。
5. F-01 多节点真机腿——需第二台节点机；沙箱双 mock 已在 b823421 上重验 2/2 PASS。
6. VG-09 Snell 服务端二进制实测（沿袭 R14–R21）——出现可达的官方 snell-server 二进制时再逐格销账。
7. 真 PG 方言面——PG 契约归发版门。
8. SIP023 四面（Surge/Loon/QX/URI）与 144 格——出现可达的官方可验客户端/二进制时再逐格销账。
9. `withSpeed=true` 测速腿——需 live browser / 远端节点。
10. socket 包 4 SKIP 本地不可判定（观察级）——沙箱网络/权限前提不在场。
11. Alpine/OpenRC 容器套件——本轮未跑，与 R21 同口径；CI 代偿。
12. demo 形状漂移 / fixtures 内容闸——快照内实跑 exit 0，未落盘日志文件（命令可重跑：`node scripts/demo/check-shape-drift.mjs`、`node scripts/demo/check-fixtures-content.mjs`）。
- 已闭合、不再结转：L5 §12 第 11 项（handler 全量重跑）已由 L3 闭合（handler 1355 PASS）；R21 结转的 L1 余项（R21-X-01 真机引擎切换腿、F-02 companion 第二道闸真机、F-03 相位腿直连拓扑、R19 测试文件单独重跑）本轮全部有新证据覆盖，不再结转。
- R21 已闭合项维持：kill-freeze K2（L5 新方法 SIGSTOP 编排首跑即成）、Trojan 腿（L5 修正后重跑 + 公网补探 PASS）。

## 九、收敛判定：未达成

用户口径（一轮零新增产品问题）：本轮新增产品 P3=1（R22-X-01）。旧靶向轮口径对照：新增产品 P0/P1=0 且哨兵零回退（满足旧口径条件；R20、R21、R22 连续三轮满足），但用户最新口径优先，不以旧口径报收敛。下一轮 R23 在修复（R22-FIX-PLAN F-01/F-02）后重审，不发版。
— R22-X-ledger END · 自证：本行之前正文字节数 10405 · END
