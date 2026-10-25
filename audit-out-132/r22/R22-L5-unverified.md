# R22-L5 未验证清单复核 + 真机腿

**窗口：2026-10-25T13:38:12Z → 2026-10-25T13:56:10Z（17m 58s）｜Lane L5｜锁：`R22-L5 2026-10-25T13:38:12Z`，每次真机腿前 check 均通过，13:50:01Z 已放；日志写明发起方 R22-L5**

**基线（pin 死）**：main @ `b823421`（完整 `b82342137cb3157197ad43906c41ccbc6145b64d`，R21 修复 fix-r21 已合入并推 origin）。只读读数树 `~/workspace/.r22-tree` @ b823421（全程 `git status` 干净，未修改）；构建与探针在快照副本 `~/workspace/.r22-l5snap`（探针文件 `r22_l5_probe_test.go`，跑后即删；快照已整体删除）。测试机：新机 ip-172-26-4-158，tailnet 100.123.93.34，Debian 12，内核 6.1.0-53-cloud-amd64，root，用户授权任意折腾、非生产。测试机面板为 R22 基线部署 new4（b823421 构建，见 `R22-00-baseline.md`）。

**依据**：`audit-out-132/r21/R21-L5-unverified.md`（25,595 字节）、`R21-X-ledger.md` §八（未验证清单结转项）、`R21-X-verdicts.md` V-07/V-09、任务书 `~/workspace/user/files/flmx-audit-catalog-v132.md`（正本）；护栏 N-01…N-37 全程有效。geo 拨内网挂住一事不在本轮范围（D-GEO-01 用户已于 2026-10-25 拍板选 A 保持拦截，文档分支待 R22 结束后合 main），本报告只在 Trojan 腿 §2.4 以旁证形式记录其机理的一次独立复现，不并入、不立案。

**方法来源说明**：R21 旧脚本文件（`~/workspace/.r21-l5scripts/`）在读取时触发安全检查，本轮**未逐字执行旧脚本**；按 R21 报告正文记载的方法学重新实现为 R22 新脚本（`~/workspace/.r22-l5scripts/R22-L5-*.py|sh`，机上副本 `/root/r22-l5/`），并含 V-09 修正：删除一律显式 ID（永不用列表 `[0]`）、删前先备份 DB、token/口令永不进日志。全部腿均以新脚本跑出新证据。

**结论总览**

| # | 项 | 结论 |
|---|---|---|
| 1 | kill-freeze K2（R21 闭合项复核） | **PASS 维持**（新证据：SIGSTOP 编排首跑即成；见 §1） |
| 2 | Trojan 腿（R21 闭合项复核） | **PASS 维持**（新证据：TLS 直连 5.06s / 重复 5.05s / 最小对照 30s 未断 + 公网目标补探中继成立；见 §2） |
| 3 | 远端节点与非 Debian 内核 | **仍未验证**（方法不可验；见 §3） |
| 4 | 浏览器 DOM 腿（鼠标实点） | **仍未验证**（无鼠标实点能力）；served-chunk 基线已重定至 b823421（served md5 = dist md5，见 §4） |
| 5 | 真实 Gist 端到端 | **仍未验证**（无真实令牌，不得向用户索取；见 §5） |
| 6 | 单族 sock_diag 真机形态 | **仍未验证**（触发条件在本机内核上不可产生；新内核 6.1.0-53 上直接 netlink 证据重验通过，见 §6） |
| 7 | F-01 多节点真机腿 | **仍未验证**（新机仅 1 节点）；沙箱双 mock 覆盖已在 b823421 上重验 2/2 PASS（见 §7） |
| 8 | 其他四路未验证项汇总 | L4 已汇总（§8）；**L1/L2/L3 报告在本轮报告落笔时尚未落盘，未能汇总**，见 §8 缺口登记 |
| — | 新立案 | **0**（本轮无新产品缺陷立案；见 §9） |

---

## §1 kill-freeze K2 — PASS 维持（新方法重跑，首跑即成）

### 1.1 代码 pin 死（基线 b823421，相对 7d93792 的 diff 核查）

- `go-gost/` 在 7d93792..b823421 间**零 diff**：agent 侧 `updateServices`（`go-gost/x/socket/service.go:122`）仍为 upsert 语义（存在则更新，不存在则创建，从不返回 not-found）；`createServices`（同文件 `:24`）第一阶段遇已注册名仍报 `service %s already exists`。
- `control_plane.go` 在本区间仅改 1 处注释（R21-X-02 说明）+ R21 修复：guard/fallback 路径（`add_service_guard.go:272` sent-ok 唯一出口、`control_plane.go:479` update-fallback 分支、`:465` 起 UpdateService 先失败才走、`sendNodeCommand` `:838`、默认超时 6s `:144`、写 deadline 5s `ws/server.go:77`）**全部未变**；`mutations.go` 的改动仅为 R21-X-03 force-delete gost 清理（`:3757` 起），forward-update 同步调用点（`:3605`）未变。
- 结论：K2 触发机理与 R21 完全相同，复跑条件具备。

### 1.2 正对照 PASS（真实运行前门禁）

watcher 对 scratch 日志 + dummy `sleep` pid + scratch gost 文件跑：`send` 行 13:46:13.152 → SIGCONTED 同毫秒（State T→S，证明 SIGCONT 腿）；`sent-ok` 行 13:46:15.215 → KILLED 同毫秒；SNAPSHOT 同毫秒，md5 `342938901ecd911d7c193813381697cf` = scratch 文件，services=2 dup=none；watcher exit 0。门禁通过。日志 `R22-L5-ctl-watch.log`（259 B）。

### 1.3 真实运行（2026-10-25 13:46:11Z–13:46:33Z，首跑即成）

- 门前：后端 sent-ok 基线计数 0；agent PID 135305，NRestarts=0；DB 已备份（`/root/r22-l5/gost.db.pre-k2.bak`，md5 `ce4e156a5f364d2d1990fce6ba8f60ba`）。
- R22 差异：新机 tunnel=0，故先建 scratch 隧道 `r22-l5-k2-tun`（id=1，`inNodeId:[{nodeId:1, protocol:tls, strategy:round}]`，与 R21 新机腿同 payload），再建 scratch 转发 `r22-l5-k2-test`（id=7，tunnel 1，`127.0.0.1:18081`，inPort 8699，无限流）；5s 内服务 `7_1_0_tcp/udp` 落盘；冻结前 gost md5 `20cdf70acdff5ee8e33f817ba4f92e39`。
- `SEND-SEEN` 13:46:28.680（触发后 **6.11s** = 6s UpdateService 超时，符合设计）→ SIGCONTED 同毫秒。
- `SENT-OK` 13:46:28.730（SIGCONT 后 **50ms**）→ SIGKILL 同毫秒 → SNAPSHOT 同毫秒，md5 `20cdf70acdff5ee8e33f817ba4f92e39`，services=**2，dup=none**，与冻结前 md5 逐字节相同。
- 备份 SIGCONT 于 13:46:31.577 发现旧 PID 已不存在（“backup-CONT: pid gone”——watcher 已先行完成，不需要备份）。
- 后端 sent-ok 窗口内计数 **0 → 1**（仅本次 K2 事件）；原始日志行时间戳 `send... 13:46:28.662679173Z`、`sent-ok... 13:46:28.688785189Z`。
- agent 被 systemd 重启：active，新 PID 139774，NRestarts=1；重启后 live gost md5 与冻结快照完全相同（`20cdf70a…`）。
- API 回执 `{"code":0,"msg":"操作成功","ts":1792935988688}`（ts ≈ 13:46:28.688Z，即 sent-ok 时刻）。
- 清理：节点回在线后（`nodeonline` 0s 即在线）按**显式 ID**删转发 7（code 0）→ 删隧道 1（code 0）→ gost 回 pristine md5 `23ca5d88ba3bca475f24ee48c47e0fc9`（services=0），计数回 1/0/0/1/1。无 R21 式 S-04 瞬态（删前等了节点在线）。

**结论**：刺激已送达、投递成功、冻结快照 2 服务零重复且与冻结前 md5 逐字节相同——**K2 断言 PASS 维持**。

证据：`R22-L5-killfreeze-K2.log`（6,056 B）、`R22-L5-killwatch-K2.log`（411 B）、`R22-L5-gost-frozen-k2.json`（996 B）、`R22-L5-ctl-watch.log`（259 B）、`R22-L5-k2-backupcont.log`（35 B）。

---

## §2 Trojan 腿复测 — PASS 维持（新实现有效重跑 + 公网目标补探）

### 2.1 第一次跑无效（探针脚本假设错误，非产品问题）

`R22-L5-trojan.log`（440 B），13:46:42Z。脚手架正常（dummy 59909 已监听、DB 已备份）；Trojan 入站创建报 `{"code":-1,"msg":"TLS 需要填写域名 (SNI)"}`。

根因（代码 pin 死，基线 b823421）：`Normalize`（`model.go:301-302`）在 Security.Type 为空时默认取该协议+传输首个合法安全层；Xray Trojan+TCP 的合法集为 `[tls, reality]`（`capabilities.go:184`，无 `none`），故空 security 默认走 TLS；TLS + 自签模式（`CertModeSelf` 为默认，`model.go:209`）要求 SNI（`model.go:676`）。R21 的旧 payload（`/root/r17/create-trojan.json`）随旧机下线已不可考，但其腿能通明文 trojan 客户端必有 TLS 层——本轮据此修正：创建时显式带 `"security": {"type": "tls", "sni": "r22min.test"}`，客户端先 TLS（校验证书关闭）再发 trojan 握手。**面板不提供明文 Trojan 入站是产品既定口径，不是缺陷。**

### 2.2 第二次跑有效（修正后，TLS 直连）

`R22-L5-trojan-rerun.log`（2,700 B，已脱敏，见 §9 事故 1），13:47:42Z–13:48:26Z。创建成功（id 4，port 59906，idle=5；inbound 1→2）；xray 配置 5s 内含 59906；policy `{"levels": {"1": {"connIdle": 5}}}`；clients=2，pwlen=[20, 32]（第 2 个为订阅用户客户端，非本腿口令）。

- **R22-TR-1 Trojan 直连 main：CLOSED 5.06s**（header_sent 0.02s），判定带 3.5–6.5s，**PASS**；
- **R22-TR-1r 直连重复：CLOSED 5.05s**（header_sent 0.00s），**PASS**；
- **R22-TR-1m Trojan 最小（minxray 59914，同口令、TLS、无 policy 段）：STILL_OPEN after 30.0s**，**PASS**（最小 A/B 对照成立）。

删除用**显式 id=4**（code 0）；inbound 回 1，xray 配置端口只剩 [59940]（pristine VLESS），计数回 1/0/0/1/1。

### 2.3 补探：dummy 0 接受的机理确认（R22-TR-PUB）

主腿三腿的 dummy（127.0.0.1:59909）接受数均为 **0**，而会话仍在 5.05–5.06s 准时关闭。解释：agent Xray 26.10.21 的 freedom 出站内置 `defaultBlockPrivateRule`（D-GEO-01 已查清机理）把到回环目标的中继拨号黑洞了——中继未抵达 dummy，但入站会话的 idle 计时照常触发，故关闭时间仍为准值。R21 的腿未记录 dummy 接受数，同一缺口当时未被发现。

补探 `R22-L5-trojan-pub.log`（1,231 B），13:49:25Z：同型入站（id 5，port 59907，idle=5，TLS），目标改机**公网 IP** 16.146.199.68:59909（dummy 绑 0.0.0.0:59909 监听；公网 IP 为 NAT 地址、不能直接 bind，见 §9 事故 2）：

- **R22-TR-PUB：CLOSED 5.05s，dummy accepts=1**，两判定（3.5–6.5s 且接受 ≥1）**PASS**。

中继端到端成立，黑洞解释得证。删除用显式 id=5（code 0），计数回 1/0/0/1/1，xray 端口 [59940]。本补探同时是 D-GEO-01 机理的一次独立旁证（freedom 拦私网、不拦公网），但 geo 本身不在本轮范围、不立案。

**结论：Trojan 腿复测完成，R21 5.24s/5.07s 直连实测被本轮 5.06s/5.05s 直连实测取代，PASS 维持；中继链路经公网目标补探完整证实。**

---

## §3 远端节点与非 Debian 内核 — 仍未验证

方法不可验，阻塞原因：

- 仅一台 Debian 12 测试机（新机 ip-172-26-4-158，内核 6.1.0-53-cloud-amd64）；节点列表仅 1 节点（w10-test，id=1，status=1，`dev (debian/amd64)`）；无远端节点可用（无第二台节点机、无可注册的远端 agent 靶机）；无非 Debian 内核靶机（无 Alpine/Arch/Ubuntu 内核、无定制无 IPv6 内核等）。
- 本地沙箱无 Docker、DNS 是假的，无法起第二节点或换内核。

如实登记未验证，不得冒充。本项无需上机。证据：`R22-L5-pristine.log`（节点列表 + `uname -a`）。

---

## §4 浏览器 DOM 腿 — 仍未验证（鼠标实点不可验）；served-chunk 基线已重定至 b823421

- 鼠标实点能力：本路无浏览器任务能力，DOM 真实点击不可验，如实登记未验证。L4 §11 U-1（前端渲染、弹窗交互、demo 页实际展示）与 R13X-003（编辑表单省略域外 ratio/flow 的 DOM 腿）同口径并入此项，不另立。
- served-chunk 基线已重定：本轮两次独立实测 `GET / 6366 → 200 size=8776`，served index.html md5 `57ab941e7fc6025b52700b2bf36f2241` = R22-00-baseline.md 记载的本地 dist md5（b823421 前端构建；与 1.4.9/88f10db 前端同源，fix-r21 无前端改动）。证据：`R22-L5-recon.log`（13:38:29Z）与 `R22-L5-pristine.log`（13:45:53Z）两次测量一致。

---

## §5 真实 Gist 端到端 — 仍未验证

无真实令牌、不得向用户索取——如实登记未验证。代码 pin 死（基线 b823421）：`gist_backup.go:60` 配置键 `gist_token`；`:337` 上传取 `cfg[gistConfigToken]`；`:402` token 为空即不启用。真实令牌端到端（建 Gist → 恢复，含 L4 U-3 的“真机恢复后节点终态”）无令牌无法跑。Gist 功能的模拟/门层面覆盖见 L4（`TestGist` 同批 20 项 PASS，`R22-L4-handler-gate.log`），与本项不重叠。

## §6 单族 sock_diag 真机形态 — 仍未验证（触发条件在本机内核上不可产生；新内核重验）

### 6.1 代码 pin 死（基线 b823421）

- `go-gost/` 零 diff：分族 dump（`go-gost/x/socket/tuning_probe_linux.go`）——每族一次 dump（AF_INET then AF_INET6，绝不用 AF_UNSPEC）；单族失败跳过记入 `FamilyErrors`，仅双族失败才报 RTT 不可用。`buildDiagRequest`（`:85`）：72 字节请求，nlmsghdr len=72、type=20（SOCK_DIAG_BY_FAMILY）、flags NLM_F_REQUEST|NLM_F_DUMP、seq=1，family、IPPROTO_TCP、INET_DIAG_INFO 扩展位、states 0xfff。tcp_info 的 `tcpi_rtt` 在偏移 68。
- 警告形态：`tuning_probe.go:210-225`——单族失败时 agent 记日志 `tune probe: %s sock_diag dump failed, RTT median covers %s only: %v`，并向结果追加警告 `"%s sock_diag 取数失败，中位数仅基于 %s 连接"`。
- 调用入口：面板 `POST /api/v1/node/tune`（`handler.go:314`，`node_tune.go:57`），`{"id":1,"action":"probe","withSpeed":false}`，面板侧 probe 超时 90s；探针只读（不写盘、不 apply）。本腿未重启 agent。

### 6.2 真机探针结果（警告从未出现）

`R22-L5-sockdiag.log`（2,164 B），2026-10-25 13:45:58Z–13:46:03Z，新机内核 6.1.0-53-cloud-amd64（R21 为旧机 6.1.0-54）：

| 探针 | ts | code | rttMedianMs | rttSamples | warnings | 备注 |
|---|---|---|---|---|---|---|
| 基线（IPv6 开） | 1792935958663 | 0 | 10 | 9 | 无（回执无 warnings 键） | — |
| IPv6 禁用后 | 1792935960794 | 0 | 14 | 9 | 无 | `disable_ipv6=1` 已生效 |
| 恢复探针 | 1792935962919 | 0 | 11 | 9 | 无 | `disable_ipv6=0`，节点在线 |

### 6.3 决定性机制证据：AF_INET6 dump 在 v6 禁用时仍成功（本轮重验）

直接 netlink 探针（与 agent 的 `buildDiagRequest` 同型请求，72 字节、states 0xfff）：

| 状态 | AF_INET | AF_INET6 |
|---|---|---|
| v6 开 | DONE-success，24 条 | DONE-success，8 条 |
| v6 **禁用**（disable_ipv6=1） | DONE-success，30 条 | DONE-success，**7 条** |
| 恢复 | DONE-success，34 条 | DONE-success，8 条 |

结论：本机内核（Debian 6.1.0-53-cloud-amd64）对 AF_INET6 sock_diag 始终回 NLMSG_DONE，sysctl 禁用 IPv6 只影响地址分配与新连接（v6 条数 8→7，一条 v6 连接消失），**不产生 dump 错误**。警告形态要求的是单族 dump **ERROR**（`FamilyErrors[i]`），该触发条件在本机上不可产生；要产生它需要无 IPv6 inet_diag 支持的内核或 netlink 故障——本测试机无法提供。**因此单族警告形态仍未验证，阻塞原因已精确到内核行为 + 直接 netlink 证据，不是“没试”。** 代码层面的警告字符串与日志格式已在 §6.1 pin 死；沙箱单元测试覆盖该分支（本轮重验：`TestR14F06F04SingleFamilyFailureWarns` 等 6/6 PASS，`R22-L5-tuneprobe-sandbox.log`），真机形态待有无 IPv6 内核的靶机再验。

sysctl 已恢复（disable_ipv6=0），节点在线（`nodeonline` 0s 即在线）。

---

## §7 F-01 多节点真机腿 — 仍未验证（单机仅 1 节点）；沙箱双 mock 覆盖已在 b823421 上重验

- 真机腿：新机仅 1 节点（w10-test），多节点/多端口真机形态无条件跑，维持未验证。R21 结转的关联项（R21-X-01 真机引擎切换腿、F-02 companion 第二道闸真机、F-03 相位腿直连拓扑、R19 测试文件单独重跑）属 L1 车道：L1 日志已落盘（`R22-L1-tm-x01-switch-realm.log` 等），但 L1 报告尚未落盘，本轮不代结论，见 §8。
- 沙箱双 mock 覆盖重验（本轮新探针，`R22-L5-dualmock.log`，2,553 B）：探针文件 `r22_l5_probe_test.go` 在快照副本 `~/workspace/.r22-l5snap/go-backend` 内跑后即删。双 mock 节点经真实 WS 传输、按节点归因记录命令（R21-L2 同口径，探针文件已重写——R21-L2 探针文件按纪律已删）：
  - `TestR22L5MultiNodeStaleCleanup`：端口 {(1,22000),(1,22001),(2,22002)}、旧 maxConn=5 → 新 maxConn=0：UpdateService 恰 3 次（节点 1 ×2、节点 2 ×1，payload 覆盖三端口）→ DeleteCLimiters 恰 2 次（每节点恰 1 次，limiter 名均为 `rule_conn_limit_<id>`，且均在其节点 UpdateService 之后）。**PASS**（0.46s）。
  - `TestR22L5OfflineNodeSkipBranch`：节点 2 断 WS → 回执 warnings 含“节点 relay2 不在线，已跳过下发”，UpdateService 仅节点 1 ×2、DeleteCLimiters 仅节点 1 ×1。**PASS**（0.48s）。注：离线节点的 UpdateService 失败后走了 update-fallback（日志 `decision=send reason=update-fallback` → `sent-failed ... err=节点不在线`），守卫未死循环、回执正常。
- 同口径附带重验：R21 F-01 常驻测试 3/3 PASS（`R22-L5-f01-sandbox.log`：引擎切换 gost→realm 删 limiter、切回 gost 重建 limiter、deleteForwardServicesOnNode 单独调用不删 limiter）。

**结论**：多节点真机腿仍未验证；多节点同步的“每节点恰 1 次失势清理、后于该节点推送、离线节点跳过”三断言已在 b823421 沙箱双 mock 上重验通过。

---

## §8 其他四路新发现的未验证项汇总（去重后）

### 8.1 L4 已汇总（报告 `R22-L4-matrices.md`，52,993 字节，END 在位，新立案 0）

| # | 内容 | 来源 | 与 L5 关系 |
|---|---|---|---|
| U-1 | 浏览器 DOM 腿（前端渲染、弹窗交互、demo 页实际展示） | L4 §11 U-1 | 并入本轮第 2 项（§4），去重 |
| U-2 | 真 PG 方言面（本轮 SQLite 进程内验证；PG 契约归发版门） | L4 §11 U-2 | **新增结转项**（本轮第 7 项，§12） |
| U-3 | 真实 Gist 端到端、真机恢复后节点终态 | L4 §11 U-3 | 并入本轮第 3 项（§5），去重 |
| U-4 | handler 全量重跑（本轮任务书未要求；L3 车道另行交付） | L4 §11 U-4 | 跨车道待办，待 L3 报告；不作产品未验证项 |
| U-5 | VG-09 Snell 服务端二进制实测（在盘全根搜索 `snell-server` 0 命中） | L4 §11 U-5 / O-L4-1 | **新增结转项**（本轮第 8 项，§12；沿袭 R14–R21） |
| O-L4-5a | SIP023 四面（Surge/Loon/QX/URI 原样下发多段）与 144 格仍未验证（无新官方二进制） | L4 §10 O-L4-5 | **新增结转项**（本轮第 9 项，§12；目录 §67 同口径） |
| O-L4-5b | `withSpeed=true` 测速腿未验证（无 live browser / 远端节点） | L4 §10 O-L4-5 | **新增结转项**（本轮第 10 项，§12；沿袭 R21 §9 O-L4-5） |
| O-L4-4 | socket 包 4 SKIP（`TestR6B6HandleUdpPingPrivateListenerSucceeds` 等，沙箱网络/权限前提不在场） | L4 §10 O-L4-4 | 观察级登记（本轮第 11 项，§12）；本地不可判定，不计 PASS、不计失败 |

L4 观察项 L7-1…L7-4 与 O-L4-2/O-L4-3/O-L4-6/O-L4-7 均为观察、不立案，不入未验证清单。

### 8.2 L1/L2/L3 汇总缺口（明文登记）

**L1、L2、L3 三路的报告在本轮报告落笔时（2026-10-25 13:56Z）尚未落盘**（目录内仅有各路日志与脚本，无 `R22-L1-*.md` / `R22-L2-*.md` / `R22-L3-*.md`），其登记的未验证/能力边界项**未能汇总**。已知的待 L1/L2/L3 报告确认的结转候选（引自 R21-X-ledger §八，不代结论）：R21-X-01 真机引擎切换腿、F-02 companion 第二道闸真机未单独跑、F-03 相位腿未按直连拓扑重跑、R19 测试文件未单独重跑。**请主控在 L1/L2/L3 报告落盘后补汇**，本轮 L5 清单以 §12 为准，不冒充已汇总。

---

## §9 事故登记（探针脚本/操作问题，非产品缺陷）与新立案

**事故 1（凭据脱敏）**：Trojan 重跑日志的 `inbound listed with idle` 行把整条入站记录打印，致 scratch Trojan 口令（20 位随机串）明文进了 `R22-L5-trojan-rerun.log`。已用 python 将该口令替换为 `<password-redacted>`（1 处），复核 grep：残留 0 处；32-hex token 扫描：各日志中仅有 docker 容器 ID 与 md5 校验和（非认证 token），`Authorization`/`Bearer` 0 命中。口令所属入站（id 4）已删、xray 配置已无该口令，凭据已失效。教训：探针打印 API 记录时只取 id/idle 等字段，不打印整条记录；补探脚本已按此口径写。

**事故 2（公网 IP 绑定）**：补探 dummy 首次绑 `16.146.199.68:59909` 报 `Cannot assign requested address`——Lightsail 公网 IP 为 NAT 地址、不在本地接口上。改绑 `0.0.0.0:59909`、握手目标仍写公网 IP 后通过。非产品问题。

**事故 3（K2 日志 cosmetic）**：`tunid`/`fwdid` 腿用 `| head -1` 取 id 时 python 报 `BrokenPipeError`（两处），id 仍正常输出、不影响腿。探针脚本小毛病，不修产品。

**事故 4（双 mock 探针 harness）**：首版探针的 httptest handler 对两个连接各 `close(serverDone)` 一次，致 `panic: close of closed channel`（net/http 逐连接恢复，测试仍 PASS）。已改 `sync.Once` 后重跑干净（2/2 PASS，日志 `R22-L5-dualmock.log` 为重跑后版本）。探针问题，不修产品。

**观察项（登记，不立案）**：`inbound/list` 对已认证管理员原样返回 `settings.password`（Trojan 口令在列表回执内可见）。前端展示/分享入站需该口令，属既定口径；本轮不立案，由主控定夺是否追踪。

**新立案：0**。本轮无新产品缺陷。K2 冻结快照零重复（PASS 维持）；Trojan 直连、重复、最小对照、公网目标补探均 PASS；sock_diag 警告未触发是触发条件不可产生，非代码缺陷；双 mock 探针 2/2 PASS。

---

## §10 离场恢复对账（完全量，恢复前后计数）

恢复沉淀复核 `R22-L5-restore-settle.log`（1,074 B），2026-10-25 13:49:54Z；锁已于 13:50:01Z 释放。

| 项 | 基线 pristine（部署后，R22-00-baseline） | 本轮腿期间 | 离场后 | 结论 |
|---|---|---|---|---|
| agent 二进制 | 27787cbf52e9390685e75399ff9da821 | 同值（未换） | **27787cbf52e9390685e75399ff9da821** | 未动 |
| agent unit | active，PID 135305，NRestarts=0 | K2 SIGKILL 后重启 1 次 | **active，PID 139774，NRestarts=1** | 正常（K2 方法设计的单次重启） |
| gost.json | 23ca5d88ba3bca475f24ee48c47e0fc9（`{"services": null}`） | K2 期间 20cdf70a（2 服务） | **23ca5d88ba3bca475f24ee48c47e0fc9**（services=0） | 回位 |
| 计数 node/tunnel/forward/inbound/subscription | 1 / 0 / 0 / 1 / 1 | K2 期间 1/1/1/1/1；Trojan 期间 inbound 2 | **1 / 0 / 0 / 1 / 1** | 回位 |
| 节点 | 1 在线（w10-test） | 在线（K2 冻结瞬间除外） | **1 在线**（`dev (debian/amd64)`） | 正常 |
| xray inbounds | [(59940, vless)]，policy None | Trojan 期间 +59906/59907/59914 | **[(59940, vless)]，policy None** | 回位 |
| 面板 | new4（backend 1ca4572b…/frontend 57ab941e…） | 同值（未重部署） | **new4**（三容器 healthy） | 未动 |
| served index.html | 57ab941e7fc6025b52700b2bf36f2241 | 同值 | **57ab941e7fc6025b52700b2bf36f2241** | 未动 |
| scratch 端口 | 全部空闲 | 8699/59906/59907/59909/59914 占用 | **全部空闲** | 回收 |
| 残留进程 | — | dummy/minxray（腿内） | **无残留脚手架进程** | 回收 |
| sysctl | disable_ipv6=0 | 腿内曾置 1 | **disable_ipv6=0** | 回位 |
| DB 备份（机上） | — | `/root/r22-l5/gost.db.pre-k2.bak`、`pre-trojan.bak`、`pre-trojan-pub.bak`（md5 均为 `ce4e156a…`，删前备份） | **3 份在盘** | 保留备查 |
| 锁 | free | R22-L5 持有 13:38:12Z–13:50:01Z | **free** | 已放 |

---

## §11 证据清单（同目录落盘，原始日志）

| 文件 | 字节 | 内容 |
|---|---|---|
| `R22-L5-recon.log` | 1,375 | 新机侦察（内核/节点/计数/served md5/容器） |
| `R22-L5-pristine.log` | 998 | pristine 复核（计数 1/0/0/1/1、gost、节点、served md5） |
| `R22-L5-killfreeze-K2.log` | 6,056 | K2 真实运行（正对照 + SEND-SEEN/SENT-OK/SIGKILL/SNAPSHOT 时间线 + 清理） |
| `R22-L5-killwatch-K2.log` | 411 | K2 watcher 日志（机上原始文件拉回） |
| `R22-L5-gost-frozen-k2.json` | 996 | K2 冻结快照（2 服务，dup=none，md5 20cdf70a…） |
| `R22-L5-ctl-watch.log` | 259 | K2 正对照（dummy T→S、KILLED、快照 md5 一致） |
| `R22-L5-k2-backupcont.log` | 35 | K2 备份 SIGCONT（pid gone） |
| `R22-L5-trojan.log` | 440 | Trojan 首跑（无效：TLS 需 SNI，探针假设错） |
| `R22-L5-trojan-rerun.log` | 2,700 | Trojan 有效重跑（5.06s / 5.05s / 30s 未断；已脱敏 1 处） |
| `R22-L5-trojan-pub.log` | 1,231 | Trojan 公网目标补探（CLOSED 5.05s，dummy 接受 1 次） |
| `R22-L5-sockdiag.log` | 2,164 | sock_diag 三探针（基线 / v6 禁用 / 恢复）+ 直接 netlink 六格 |
| `R22-L5-dualmock.log` | 2,553 | 沙箱双 mock 探针 2/2 PASS（重跑后干净版） |
| `R22-L5-f01-sandbox.log` | 3,620 | R21 F-01 常驻测试 3/3 PASS（b823421） |
| `R22-L5-tuneprobe-sandbox.log` | 835 | tuning probe 单元测试 6/6 PASS（含单族失败警告分支） |
| `R22-L5-restore-settle.log` | 1,074 | 离场恢复沉淀复核 |
| **合计** | **24,747** | 15 个文件 |

另：机上 `/root/r22-l5/`（脚本 + DB 备份 3 份 + 冻结快照 + watcher 日志，0600/0700）；本地探针脚本 `~/workspace/.r22-l5scripts/`（legs/killfreeze/killwatch/sockdiag/trojan/trojan-pub；R22 新实现，未逐字执行 R21 旧脚本）。

---

## §12 未验证项结转（留 R23，去重后）

1. 远端节点与非 Debian 内核（§3）——需第二台节点机 / 非 Debian 内核靶机。
2. 浏览器 DOM 腿鼠标实点（§4）——需浏览器任务能力；served-chunk 已重定至 b823421。
3. 真实 Gist 端到端 + 真机恢复后节点终态（§5）——需真实令牌（不得向用户索取）/ 真机恢复腿。
4. 单族 sock_diag 真机警告形态（§6）——需无 IPv6 inet_diag 支持的内核靶机；本机内核（6.1.0-53）已用直接 netlink 证据证明不可触发。
5. F-01 多节点真机腿（§7）——需第二台节点机；沙箱双 mock 已在 b823421 上重验 2/2 PASS。
6. VG-09 Snell 服务端二进制实测（L4 U-5/O-L4-1，沿袭 R14–R21）——出现可达的官方 snell-server 二进制时再逐格销账。
7. 真 PG 方言面（L4 U-2）——PG 契约归发版门。
8. SIP023 四面（Surge/Loon/QX/URI）与 144 格（L4 O-L4-5；目录 §67）——出现可达的官方可验客户端/二进制时再逐格销账。
9. `withSpeed=true` 测速腿（L4 O-L4-5）——需 live browser / 远端节点。
10. socket 包 4 SKIP 本地不可判定（L4 O-L4-4）——观察级，沙箱网络/权限前提不在场。
11. handler 全量重跑（L4 U-4）——L3 车道产物，待 L3 报告落盘后由主控补汇（非产品未验证项）。

另：L1/L2/L3 报告未落盘（§8.2），其未验证项待主控补汇；R21 结转的 L1 余项（R21-X-01 真机引擎切换腿、F-02 companion 第二道闸、F-03 相位腿直连拓扑、R19 测试文件单独重跑）以 L1 报告为准。

END 27039
