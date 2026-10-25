# R22 裁定（X-verdicts）

基线：main @ b823421。裁定只认落盘原始日志 + 名单存档；摘要计数不得作裁定依据。

## V-01：R21-X-01 / X-02 / X-03 全部 CLOSE（3/3）

L1 三项复核均有独立新证据（沙箱先红后绿 + 真机腿），无 FAIL。
- X-01：沙箱 10 项先红后绿（红 6 FAIL/4 PASS，X-01 两项 FAIL 文案 `expected exactly 1 DeleteCLimiters, got 0` 与立案机理逐字一致；绿 10/10）；真机隧道 2/转发 8（maxConn=5）切 realm → gost 0 服务/0 limiter/0 引用，journal 恰 1 条 `DeleteCLimiters {"limiter":"rule_conn_limit_8"}`（13:52:09，requestId `1_1792936329009086664`），`user_conn_limit` 0 触碰；切回 gost → limiter 重建（`AddCLimiters "$ 5"`），`DeleteCLimiters` 0 条；离线切换守卫亦验（0 次 DeleteService、0 次 DeleteCLimiters）。
- X-02：F-02 选方案 ①（重连重推按“当前无限流 ⇒ 失势”补删，非字面传旧快照）；代码 pin 死（`upgrade.go:885–893/937–951`、隧道/转发重试补删 `:985–993/:1005–1013`；`control_plane.go:2074` climiter 只由当前记录赋值）；口径逐字核对（`control_plane.go:505–509` 的“留待下次成功同步”现为真；R20 报告 §6.2 同句为历史记录未被误改）；真机按 R21 Leg B 同序列：转发 9 离线更新去限流 code 0 带“不在线”警告、gost md5 不变，重连后 2 秒（13:53:08）即重推，`UpdateService`（转发 9 新配置已无 climiter）后恰 1 条 `DeleteCLimiters {"limiter":"rule_conn_limit_9"}`，gost 只剩 limiter 8，自愈 0 提及。
- X-03：沙箱先红后绿（红相主回归 `expected DeleteService from force-delete (gost path), got 0`）；真机转发 10（maxConn=5，端口 32584 在监听）force-delete code 0 → gost 4 服务、只剩 `rule_conn_limit_8`、端口停监听；journal 同秒先 `DeleteService ["10_1_0_tcp","10_1_0_udp","10_1_0"]` 后 `DeleteCLimiters {"limiter":"rule_conn_limit_10"}`，各恰 1 条；普通删转发回归（转发 11）PASS。
- L1 全量回归：`go test ./...` 产品失败 0（唯二 FAIL 均归因：health 仅已知 fake-IP 环境红一格；kernel 首跑 FAIL 是快照缺 `vite-frontend/` 与 `doc/` 的探针缺件，补软链后重跑 ok）；R21 修复测试 11/11 PASS；vet/build exit 0；恢复对账通过（counts 1/0/0/1/1，gost md5 回 pristine `23ca5d88…`，节点在线，锁已放）。

## V-02：R22-X-01 立案，产品 P3

L3-CAND-01 采纳，典则取 L3 口径。依据：对象 `vite-frontend/src/api/index.ts:382`（`generateCaptcha: () => Network.post('/captcha/generate')`）+ 后端无此路由（`handler.go` 仅注册 `POST /captcha/check`（:292）与 `POST /captcha/verify`（:293）；`grep -rn captcha/generate go-backend` 0 命中）+ 零调用点（`grep -rn generateCaptcha vite-frontend/src scripts/` 仅定义本身）+ 历史口径（自 v1.0.0（`04f333c`）即已存在导出，后端从未有对应路由）+ 全集核对（190 个 API 调用点中唯一未映射者）。定 P3：死代码、无调用方，当前影响为零，调用时得 404；修复为纯前端死代码清理（删一行导出），无 BC。

## V-03：R22-X-02 立案，文档 P3

L3-CAND-02 采纳，典则取 L3 口径。依据：对象 `doc/guide/forward.md:67`（尾部 nftables 删除 pending-delete 小节，称「管理员可通过节点页的 Pending Delete 管理接口查看待清理项」）+ 前端无任何 pending-delete 界面（`grep -rn pending-delete vite-frontend/src` 0 命中；`node.tsx` 无此 UI）+ 前端无 api 封装（`api/index.ts` 无 `pending-deletes` 调用）+ 唯一真实接口是原始 API `POST /api/v1/node/nftables/pending-deletes`（`handler.go:324`）+ 正确文档在位（`nodes.md:231-246` 明言 pending 行“只能通过上面的 pending-deletes 接口查看”，与 `forward.md:67` 直接矛盾）+ 批量删除后的瞬态结果弹窗（`forward/batch-actions.ts:122`）只在删除当次展示 pending 数量，无持久管理视图。定 P3（文档）：管理员按文档在节点页找管理入口会找不到（须改用原始 API 调用）；无数据面风险（`ListForwards` 恒过滤 status=2，pending 行不进列表）。

## V-04：哨兵、常设全集、A9、R13/FINAL-P2-1 结论采纳

L3/L4 结论采纳。哨兵 53 行 51 PASS + 2 承继（S-16、S-34；S-41 的 PG 子格同口径承继，行级不降格），0 回退；全量套件 0 FAIL（handler 1355 PASS 含 4 SKIP、kernel 436、repo 267、netguard 8、PG 契约 243 PASS / 9 SKIP；gost 与 gost-x 全包 ok；安装套件 exit 0；前端构建 exit 0，dist index md5 = served）；常设全集六项差集全部为 ∅（附则 A 60 格、A-2 11 行、版本门 13 行（VG-09 未验证沿袭）、ENC 文法 99 格、路由 202 / 表 50、消费面 22 CELL）；列表/详情 24 接口对账差集 ∅；轮换域 A9 新立案 2（R22-X-01/02）；R13 关闭 7 项与 FINAL-P2-1 七段谓词维持关闭。L3 首跑中断（handler/repo 套件 13:43/13:39 先后中断，疑似五 lane 并行 OOM/误杀，未证实；中断时 364 PASS / 0 FAIL）改用 runtime 追踪的后台 exec 顺序重跑后全绿，无遗留问题，不计产品失败。L4 R5 首跑 FAIL（探针未排除 SQLite 内部表 `sqlite_sequence` 致表数 51，修正查询后 2/2 PASS，首跑 FAIL 日志 `R22-L4-r5-probe-first-run.log` 保留在案）已处理留档，不计产品失败。S-52 行号注记：实测 ENC 门调用 `:2008/:2033/:2119`、文案 `:2009/:2036/:2122`，门体与「25.9.5 及以上」文案逐字在位；基线 S-52/S-53 行号已按 b823421 重钉。

## V-05：L2 结转复核结论采纳

L2 结论采纳。F-01 §6 七项残余逐项复核：①多节点/多端口维持关闭（沙箱双 mock 每节点 DeleteCLimiters 恰 1 次；真机更新后端口归一为最小值 22001，机制 pin 死 `GetMinForwardPort`）；②离线跳过维持关闭（Leg A 反转 R21 立案：重连重推 DeleteCLimiters 恰 1 条；“留待下次成功同步”口径现与代码一致）；③切隧道+去限流维持关闭（真机顺序 DeleteService→DeleteCLimiters→UpdateService→DeleteCLimiters 逐字吻合）；④realm/nft 维持关闭（枚举零引用 + realm 真机腿：gost 0/0、endpoint 生命周期正常、journal 0 次）；⑤force-delete 维持关闭（Leg C 反转：服务+limiter 已清、端口停听、journal 恰 1 条）；⑥存量孤儿 2/12 环境侧无对象（新机 pristine gost 为空）；⑦DeleteCLimiters 瞬时失败无重试维持登记（单次发送+记日志，设计内 best-effort；本轮全部首发即达）。F-02 伴生第二道闸真机腿通过（伴生经产品路径物化 id 7、idle 10 → DB 漂移至 5 → 源入站重保存触发 reconcile → 伴生回 10、updated_time 前进；无漂移重保存 updated_time 不动；删源入站后伴生级联消失）。F-03 相位腿直连拓扑重跑通过（VLESS N=5 带数据 10.01s、静默 5.20s、N=10 带数据 20.01s、相位早 burst2 idle 9.50s / 晚 5.50s，间距 4.00s，与 R20 3.98s / R21-L1 3.99s 一致；N=0 STILL_OPEN @60s）。R19 测试文件单独重跑 3/3 PASS；shipped 修复测试 11/11、R20-F01 4/4、伴生沙箱 1/1、本轮新探针 4/4，合计 23/23 PASS。realm→gost 反向切换无同型残留（去程 DeleteCLimiters 恰 1 条；回程 AddCLimiters 恰 1 条原值 `$ 5`、DeleteCLimiters 0 条）。R21-L1 未做五项本轮基线上全部维持，余下三项（伴生真机、相位直连、R19 单跑）首次闭合，L1 未做项至此全部有新证据覆盖。观察项 8 项维持登记（见总账 §六）。

## V-06：L5 未验证清单与 K2/Trojan 结论采纳

L5 结论采纳：kill-freeze K2 **PASS 维持**（新方法 SIGSTOP 编排首跑即成：SEND-SEEN 触发后 6.11s → SENT-OK（50ms 后）→ SIGKILL；冻结快照 2 服务零重复、md5 与冻结前逐字节相同；sent-ok 0→1）；Trojan 腿 **PASS 维持**（TLS 直连 5.06s / 重复 5.05s / 最小对照 30s 未断；另以公网目标补探证实中继端到端，dummy 接受 1 次）；仍未验证：远端/非 Debian 内核、浏览器 DOM 鼠标实点（served-chunk 基线已重定至 b823421，served md5 = dist md5）、真实 Gist 端到端、单族 sock_diag 真机形态（新内核 6.1.0-53 上直接 netlink 重验：AF_INET6 在 v6 禁用时仍 DONE-success，7 条）、F-01 多节点真机腿（仅 1 节点；沙箱双 mock 在 b823421 上重验 2/2 PASS）。L5 §12 第 11 项（handler 全量重跑）已由 L3 闭合（handler 1355 PASS），不再结转。L5 事故（Trojan 重跑日志明文打印 scratch 口令 1 处，已脱敏为 `<password-redacted>`，残留 0 处，口令所属入站已删；补探 dummy 绑公网 IP 失败改绑 0.0.0.0 后通过）均已处理，不影响结论。L5 观察项 OBS-R22-L5-01（`inbound/list` 对已认证管理员原样返回 `settings.password`）登记不立案。

## V-07：L3 O-K-5 密码回显事故登记（须跟进）

L3 计数腿调试命令因 shell 引号断裂，把测试机面板密码原值回显进一次 exec 调用的 stderr（仅 lane 会话的工具输出可见）。已核查该值未写入任何日志或报告文件（`R22-L3-counts.log`、`R22-L3-served-chunk.log` 均已 `grep -c` 核查为 0 命中；L3 报告亦不含该值、JWT 与订阅 token；主控复核 L3 落盘文件未见该值）。**建议轮换测试机面板密码**（凭据在 `~/workspace/w10-run/panel-cred-new.txt`，600）——主控在 R22 结束后安排，用户侧执行。本轮登录失败的前两次尝试系引号问题，非凭据错误。

## V-08：L2 计时腿方法变更登记（D-GEO-01 机理独立复现）

L2 计时腿方法变更记入报告 §11：127.0.0.1 dummy 目标在本栈上被节点 Xray 26.10.21 freedom 内置拦私网规则黑洞（D-GEO-01 同一机理的独立复现），直连腿须以本机公网 IP 为目标；另裸 VLESS 客户端须流水线发数据（响应头懒发）。两处探针修正已记入 L2 报告 §11，不计产品失败。geo 拨内网挂住一事另立案（D-GEO-01 待用户定夺），不在本轮范围；本轮仅记录该机理在 R22 基线上的独立复现，不另立案。

## V-09：R22 基线部署事故登记

R22 审前重部署（b823421 构建 new4）中首版 new4 前端 GET / → 403——本沙箱 umask 007 使本地 dist 文件权限为 660/770，nginx worker（`nginx` 用户）读不到 `/usr/share/nginx/html/index.html`（`open() failed (13: Permission denied)`）。处置：本地 `chmod -R a+rX dist` 后重新上传、重建 `flmx-frontend:new4`、仅重起 frontend，GET / → 200、served md5 与本地 dist 逐字一致。部署事故已记入 `R22-00-baseline.md`；教训：今后 stage 上传前须 chmod a+rX。部署本身不影响审计结论（五路均在修复后的 new4 面板上跑，真机腿复核 paneld `1ca4572b…`、served `57ab941e…` 一致）。

## V-10：收敛判定未达成

用户口径（一轮零新增产品问题）：本轮新增产品 P3=1（R22-X-01）。旧靶向轮口径对照：新增产品 P0/P1=0 且哨兵零回退（满足旧口径条件；R20、R21、R22 连续三轮满足），但用户最新口径优先，不以旧口径报收敛。下一轮 R23 在修复（R22-FIX-PLAN F-01/F-02）后重审，不发版。
— R22-X-verdicts END · 自证：本行之前正文字节数 11339 · END
