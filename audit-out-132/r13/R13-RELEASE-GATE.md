# R13 靶向验证门 · 真机验证报告（R13-RELEASE-GATE）

- 门对象：集成树 `integrate-r13 @ dbacf7f`（相对基线 `f49dc0d`，23 文件 +1449/−56；批 R + 批 T2 + 文档批，7/7 关闭）。
- 依据：`R13-FIX-PLAN.md §4` 门 8 项 + `R13-FIX-REPORT.md §5` 预登记（逐字对象以 FIX-PLAN §4 为准）。
- 结论：**8/8 PASS**。未发版、未打 tag、未改产品代码、未合 main（合 main 与终局候选版审计待父代理接续）。
- 部署序列（全程遵守）：先停 agent → 换面板 → 再启 agent（`reset-failed` 后 `start`）。
- 测试机：Lightsail Debian 12（公网 16.146.199.68 / tailnet 100.80.182.11，非生产；SSH 走 Tailscale 隧道）。

## 0. 门前基线与候选部署

- Pristine 基线（门前实测，与 R13 基线报告一致）：49 张表（不含 sqlite_sequence）；node 1（status 1 在线）；
  node_inbound 578；subscription 18；tunnel 4；forward 6；forward_port 6；gost.json 12 服务；
  面板自报 current 1.3.5、latest 1.4.4、hasUpdate=true；容器镜像 `flmx-backend/frontend:r1gate-1.3.5`。
- 候选镜像（门期组装，见过程观察第 2 条）：`flmx-backend:r13gate-1.4.5`（`d1cc052799ac`）、
  `flmx-frontend:r13gate-1.4.5`（`249d567001ab`，第二版）；运行层与两份 Dockerfile 逐字一致，
  仅构建阶段由本地构建替代（同树 `dbacf7f`、同构建标志、GITHUB_REPO 均未设）；
  后端二进制 sha256 前 16 位 `1d9dfe0531514a5a`；前端 dist tarball sha256 前 16 位 `b46fd1605da383`。
  R13 差分无 `go-gost` 改动，agent 二进制未换。
- 部署后核验：面板自报 current 1.4.5、latest 1.4.4、hasUpdate=false；节点 1 在线；agent active；
  后端 healthy。部署前备份：compose、`.env`、sqlite 卷数据、`/etc/flux_agent/gost.json`
  均存 `/root/r13gate/backup/`（恢复用，见门 #7）。

## 门 #1（R-a 真机）PASS

- 在线腿（不重连）：导入含 forwards/tunnels/inbounds 的改参备份——forward 1 监听口 31080→31180、
  forward 6 中转目标改 stale `:29999`。导入期间 agent PID `494155` 未变、节点始终在线。
  回执：code 0；forwardsImported 6、tunnelsImported 4、inboundsImported 576（另跳过 2 条既有坏行，
  见过程观察第 5 条）；`repushNodeIds=[1]`、`repushFailedNodeIds=null`（回执披露重推结果、无失败）。
  终态核对：gost 12 服务无重复；31080 消失、31180 tcp+udp 监听、目标 `127.0.0.1:18080` 不变
  （同 id 改参旧服务被替换，非并存）；forward 6 的 DB 与 gost 转发目标均重算回 `:24501`
  （即入站 39 的端口，与重算口径一致）；其余 5 条转发端口/目标不变；`inet flmx_kernel` nft 表在位。
- 离线腿：停 agent 后节点 status→0、31180 关闭；此时导入 forward 1→31280，回执 code 0、
  `repushNodeIds=null`（离线节点未尝试、未记失败）；`reset-failed` 后启 agent 重连，
  gost 12 服务、31280 实测 LISTEN、31180 消失、节点回在线。
- 证据：机上 `/root/r13gate/evidence/gate1-backup-pristine.json`（导出 414,946 字节，sha 前 16 位
  `2493aba66bf89458`）、`gate1-import-result.json`、`gate1-offline-import-result.json`；gost.json 各阶段快照见门期 SSH 核查记录。

## 门 #2（T2-a / T2-c）PASS

- T2-a（create 守卫，`POST /api/v1/tunnel/create`，均带合法 `inNodeId` 以隔离守卫本身）：
  `type:"abc"`、`status:"abc"`、`type:1.5`、`flow:"abc"`（对照）、`trafficRatio:"abc"` 均 code -1、
  文案逐键点名（`type 参数无效` / `status 参数无效` / `flow 参数无效` / `trafficRatio 参数无效`），
  DB 隧道数不变；合法对照创建成功（id 5，flow 1、ratio 1.0），删除后隧道数回 4、链路各 1 条、gost 12 服务。
  `status` 数值域外枚举门（L4 O-3）按 FIX-PLAN 明确不在本断言内，未测、未扩面。
- T2-c（恢复面域外行跳过点名）：四行域外（flow 0 / flow 99999 / ratio -5 / ratio 200）导入，
  tunnelsImported 0、SkippedRows `tunnels=4`、4 条 warning 逐行点名 id + 名 + 坏值 + 合法范围；
  仅跳过导入后立即核查：隧道 flow/ratio 与链路计数（每隧道各 1 条）均原样——预删未碰跳过行；
  合法重导 tunnelsImported 4、值逐字。
- T2-c 字面复用问题：FIX-REPORT §4 第 1 项已登记（包循环致同合法集本地门镜像，非字面函数复用），
  本门关闭对象是断言本身；字面复用裁量归终局候选版审计（FIX-PLAN §6 第 3 项），本门不代判。

## 门 #3（T2-b）PASS（方法注记见本节末）

- 实 served 候选前端验证：公网 `:6366` 拉取的 `assets/tunnel-BMY_NhoQ.js`（70,065 字节）含编辑态
  `delete re.flow` / `delete re.trafficRatio`（与 `tunnel.tsx` handleSubmit 的省略逻辑逐字对应，
  仅编辑模式、仅存量未改选且存量域外时删键）。
- 存量 flow=0 行（直接 DB 造存量：创建与导入守卫均拦 flow=0，产品路径造不出此行，构造方式如实登记）：
  省略形态改名成功（code 0），DB flow 仍 0、ratio 仍 1.0；显式 flow:0 提交被拒
  （code -1，`计费方向超出范围 {1,2}: 0`，DB 不变）；显式 flow:2 改选成功；测试隧道已删，隧道数回 4。
  后端 presence 口径已核：`tunnelUpdate` 以 `getTunnelRecordRaw`（不归一化）取旧值，缺席字段回填存量。
- 方法注记（不藏）：本子代理无 live browser，未做鼠标实点；表单行为证据 = 实 served JS +
  与 handleSubmit 逐字同形的负载探针（省略 / 显式拒 / 显式改选三态齐全）。如需补一次鼠标点按，
  可由父代理另行安排，不影响本门行为结论。
- 沿袭观察（非失败）：前端容器 health flag 在候选与 pristine 下同为 unhealthy——healthcheck 的
  `localhost` 解析到 `::1` 而 nginx 仅 `listen 80`（IPv4 服务实测正常）；pristine `r1gate-1.3.5`
  容器部署伊始即 unhealthy，属 rig 沿袭、非候选回归。

## 门 #4（R-b / R-c / R-d）PASS

- R-b（入站段计数）：一次入站段导入回执——kernels 4、relays 2（存量 1 + 新行 999001）、
  counters 425（存量 424 + 新三元组，见注记）、subscriptions 19（存量 18 + 新 token 行）、
  subscriptionCounters 17（存量落地 16 + 新基线）、sources 9（存量 8 + 新 file 源）、
  inbounds 576、legacyUsers 0；跳过 inbounds 2、subscriptionCounters 86；DB 逐一核对落库
  （新中转行、新订阅、新订阅计数基线、新源均在位；表计数：relay 1→2、subscription 18→19、
  subscription_counter 102→103、remote_source 8→9）。
  - 注记 1（计数行活表语义）：首探针活三元组（39/tcp/in）回执计入后，DB 值随后为活流量值
    （epoch `715b…` / bytes 2624），因 agent 流量样本持续改写活计数行；改用无活流量三元组
    （39/quic/in/`gate4epoch2`/888）重验，逐字落库、表计数 424→425，证写入路径无误、
    首探针差为活表语义而非导入丢失。
  - 注记 2（86 跳过口径）：订阅计数 102 = 落地 16 + 跳过 86，跳过系引用节点/重映射后订阅不存在的行
    （D4-03 口径），全量对账无静默丢弃。
- R-c（未知类型即报错）：`ExportPartial(["users","bogus_type","inbound"])` code -2，
  文案同时点名 `"bogus_type"` 与 `"inbound"`（`inbound` 非合法类型，合法为 `inbounds`，
  报错口径严于 FIX-PLAN 仅点名 `bogus_type` 的字面要求）；`Import(["bogus_only"])` code -2，
  `导入失败: 未知备份类型："bogus_only"`；前端 `BACKUP_TYPE_VALUES` 16 值与后端
  `validBackupTypes` 16 值逐值一致（顺序亦同）。
- R-d（Protocol 重推导）：id 3（kernelId 0、Extra type `ss`）备份标签改为 `trojan` 后导入，
  DB protocol 回到 `shadowsocks`，Extra sha 与 pristine 导出逐字同值；入站列表展示 id 3 为
  `shadowsocks`，shadowsocks 计 525 与 pristine 一致（展示与筛选一致）。

## 门 #5（F-S01 契约）PASS

- 两条计数管线逐字对象（真 PG，禁 seq/SQLite 顶替）：机上 panel PG（`flux-panel-postgres`）
  内抛弃库 `r13gate_contract`；本地测试二进制经 SSH 端口转发连该真 PG 实跑
  （执行位置在本地不改变“真 PG”对象；机上容器编译过慢，见过程观察第 3 条）：
  `TestPostgresApplySubscriptionCounterSamplesConcurrentDoubleApplyContract` PASS（148.31s）、
  `TestPostgresApplyInboundCounterSamplesConcurrentDoubleApplyContract` PASS（235.27s）。
  首轮两计数测试曾 FAIL，根因为沙箱侧 SSH 转发中断（`connection refused` + `unexpected EOF`，
  见过程观察第 4 条），非断言失败；转发恢复后重跑双双 PASS，首轮 FAIL 不作产品结论。
- Companion 索引：`TestPostgresCompanionPartialUniqueIndex` PASS（89.71s，同库同法）。
- PendingDelete：`TestR3B4PendingDeleteJobRefreshesWhitelistOnlyAtFinalDelete` 本地 `-count=10`：
  10/10 PASS、0 FAIL、rc 0。
- 抛弃库事后处理：`DROP DATABASE r13gate_contract` 已执行，`\l` 验空（无 `r13gate%` 库残留）；
  各测试自带 schema 清理（`DROP SCHEMA … CASCADE`）。
- 证据：本地 `.r13gate/local-logs/gate5-pg.log`（首轮基础设施失败原样保留）、
  `gate5-pg2.log`（重跑双 PASS）、`gate5-pendingdelete.log`（10/10）。

## 门 #6（哨兵抽核）PASS

- S-34（真 PG 并发契约）：即门 #5 两条计数管线真 PG PASS，状态改写为 PASS（本地不可判定项已由门内实跑替代，禁 seq 顶替已遵守）。
- S-16（三类事件“恢复”通知）：导入腿 `TestR2B10InboundRecoveryFailureResendsOnce` 在测试机上实跑
  PASS（4.76s；本地编译的静态测试二进制传机上直接执行，宿主 `172.26.9.119` 满足 RFC1918 夹具；
  机上容器编译过慢，见过程观察第 3 条）；节点腿/延迟腿沿 R13 审计 PASS，无回归信号。
  证据：机上 `/root/r13gate/evidence/gate6-s16-binary.log`。
- S-52（ENC 版本门）：集成树重钉——`handler/kernel.go:1990/:2015/:2101` 均为
  `versionBelow3(version, 25, 9, 5)`，文案 `:1991/:2018/:2104` 含“25.9.5 及以上”；
  `handler/kernel_egress.go:48` 出站 prefer `versionBelowPadded(version, 25, 6, 8)` 未变。行号与 R12 表逐字一致。
- S-53（SS 单面集）：`kernel/credentials.go:627` 六名（chacha20、aes-128-gcm-siv、aes-256-gcm-siv、
  chacha8-ietf-poly1305、aes-128-ccm、aes-256-ccm）与 `:644` 八值
  （2022-blake3-aes-128-gcm、2022-blake3-aes-256-gcm、2022-blake3-chacha20-poly1305、aes-128-gcm、
  aes-256-gcm、chacha20-ietf-poly1305、xchacha20-ietf-poly1305、none）与 R12 表逐字一致。
- 其余 49 行：R13 审计 51 PASS + 2 承继（S-16/S-34 即本门改写项）；R13 产品差分文件集仅
  handler（repush/mutations）、model（ImportResult 视图字段）、repo（导入/隧道门）、
  `tunnel.tsx` + 测试 + 审计文档，未触其他哨兵对象文件；其中导入/恢复相关哨兵面已由
  门 #1/#2/#4 真机复验覆盖。哨兵零回退。

## 门 #7（变更面回归 + pristine 恢复对账）PASS

- 变更面回归（门期本地，集成树未变）：handler 全量 ok（261.694s）、repo 全量 ok（42.650s），rc 均为 0；
  部署前全量 16 包 ok + 前端 `tsc --noEmit` rc 0 + `vite build` 成功（集成回归结论，树未变，沿用）；
  产品失败 0（唯一历轮环境红 health 包单列，不计产品失败）。
- 升级面常规：部署与恢复均按“先停 agent → 换面板 → 再启 agent”执行，无部署序列竞态；
  恢复即升级面的逆操作，同样顺序一次成功。
- Pristine 恢复对账（逐一相等）：面板回 1.3.5（hasUpdate=true）、节点 1 在线、agent active；
  容器回 `r1gate-1.3.5`（后端 healthy）；49 表；node (1,1)；node_inbound 578；subscription 18；
  tunnel 4；forward 6；forward_port 6；forward 1 口回 31080；gost 12 服务、端口集与 pristine 逐一相同
  （31080 在位，31180/31280 消失）；门期构造行清零（中转 999001 计 0、`gate4-token-xyz` 计 0）；
  候选镜像已删（`docker images | grep r13gate` 空）；抛弃 PG 库已删（门 #5）。
  证据：机上 `/root/r13gate/evidence/restore.log`。

## 门 #8（144 格 / 墓碑沿袭）PASS（维持原登记，不新增结论）

- 144 格（Surge/Loon/QX/URI 四面 SS 格）：R13 产品差分不含任何 SS 渲染/格面文件
  （差分文件集见门 #6），无新增可验官方二进制 → 维持未验证登记，不硬判。
- 墓碑三触发逐条对账（均未满足，维持缓做）：① DB schema 变更——模型 diff 仅 ImportResult
  视图结构 JSON 字段（非 GORM 落库模型），`gorm:`/`TableName`/`autoMigrate` 增量 0 行、无迁移文件；
  ② 运营事件——门期仅测试机探针操作，无生产运营事件；③ 备份 PIT 语义——备份版本仍 1.0、
  段结构未动（R13 只加回执字段、计数与校验门）。

## 过程观察（单列，不计门项）

1. Tailscale 隧道曾中断约 20 分钟（CONNECT 立即关闭；公网面板 200 正常；直连公网 SSH 如旧被平台出口重置），
   后自行恢复；其间机上构建进程未受影响（机上持续运行）。
2. 机上源码构建极慢（512MB + swap 颠簸，`go build` 单步逾 40 分钟未完成）→ 改本地组装
   （运行层逐字一致、构建阶段本地替代，见 §0），机上源码构建已取消（backend/frontend 均 `CANCELED`，
   exit 130），tag 未被覆盖。此为门期提效处置，不改产品代码与运行层。
3. 机上容器编译同样受 swap 颠簸拖慢（编译进程长期 D 态等换页）→ PG 三约改本地二进制经转发连真 PG、
   S-16 改本地编译静态测试二进制传机上执行；两者被测对象（真 PG、机上 RFC1918 环境）均未变。
   门中途为给 S-16 编译腾内存，曾临时停面板栈与 agent（门 #1–#5 已完、S-16 不依赖面板），
   最终恢复步骤已重建全部服务并对账通过。
4. 沙箱侧临时目录重置：门中 `/root/.ssh/known_hosts` 丢失致 SSH 报 host key 不符；核对服务器现指纹
   `SHA256:BbrGCDFXpacREkh6aExcaiMgn6+hjQiwbI1TzV8/LOI` 与 `/home/hatch/.ssh/known_hosts` 中
   10 月 22 日记录逐字一致，排除换钥/中间人；此后 SSH 一律显式指定该 known_hosts 文件。
   同一重置致首轮 PG 转发中断（门 #5 首轮 FAIL 的根因），重跑后通过；`/tmp` 门期脚本亦因此丢失，
   已改落持久工作区。另：一次 `pkill` 自匹配误杀本会话（AGENTS.md 旧教训重现），此后改脚本文件方式杀进程。
5. 前端首版组装镜像静态文件权限缺陷（沙箱 660/2770 致 nginx worker 读不到，根 403、前端 unhealthy）→
   `chmod -R a+rX` 后重建第二版，实测根 200、chunk 在位；属门期组装问题，非候选产品缺陷
   （机上源码构建产物的 dist 权限为容器内默认 644，不受此影响）。
6. 导入跳过行 #44（SS2022 坏 key）/#59（坏 UUID）为 pristine 导出中既有的坏行，导入 Normalize
   整行跳过、DB 存量行保留，属既有行为；门期构造的其余行已随恢复清零。
7. 门期曾用直接 DB 写入造存量行（门 #3 flow=0 行）与门期探针行（门 #4 新行），均为测试机夹具构造、
   已在门 #7 恢复中随 DB 文件还原清零，不带入任何产品结论。

— R13-RELEASE-GATE END · 8/8 PASS · 未发版、未打 tag、未合 main · 后续：合 main → 终局候选版审计（含 R12X-002 重定裁定执行、T2-c 字面复用裁量）→ 发布终局版 → 生产最终升级包 · END
