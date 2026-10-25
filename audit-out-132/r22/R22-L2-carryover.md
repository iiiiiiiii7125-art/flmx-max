# R22-L2 结转复核报告 — R21 结转复核（只读审计）

**窗口：2026-10-25T14:03:30Z → 2026-10-25T14:28:48Z（UTC）｜ lane：R22-L2｜测试机锁：`R22-L2`，acquire 14:03:30Z（当时 free），release 14:28:48Z**

- 基线（pin 死）：main @ `b823421`（`b82342137cb3157197ad43906c41ccbc6145b64d`，R21 修复 fix-r21 已合入并推 origin；`docs(r21): R21 fix integration report (F-01..F-03)`）。只读读数树 `~/workspace/.r22-tree` @ `b823421`（开工验证干净，全程未修改；探针在快照副本 `~/workspace/.r22-l2-snap` 跑，探针测试文件 `r22_l2_probe_test.go` 跑后即删——已删；快照与 TMPDIR `~/workspace/.r22-l2-tmp` 随报告收尾删除）。
- 依据（只读）：`R22-00-baseline.md`（new4 部署：paneld md5 `1ca4572b…`、served index `57ab941e…`、计数 pristine 1/0/0/1/1）；`R21-X-ledger.md`（§四去重账、§六观察项、§八未验证清单）、`R21-X-verdicts.md`（V-01、V-08、V-09）、`R21-L2-carryover.md`（27,342 字节）；`R21-FIX-F01/F02/F03/INT-REPORT.md`；`R21-L1-close-r20.md`（15,303 字节）；任务书 `flmx-audit-catalog-v132.md`（正本）；护栏 N-01…N-37 全程有效（N-36：本轮以 R22-00-baseline.md 为准）。
- 本路只读审计：未改产品代码、未打 tag、未发版。测试机（新机 ip-172-26-4-158，tailnet 100.123.93.34）已完全恢复对账（见 §10）。
- **结论速览：新立案 0（产品 P0/P1/P2/P3 均为 0）；F-01 §6 七项残余全部复核（5 项维持关闭、1 项环境侧无对象、1 项维持登记）；F-02 伴生第二道闸真机腿首次补跑通过；F-03 相位腿按直连拓扑重跑通过；R19 测试文件单独重跑 3/3；R21-L1 未做五项在本轮基线上全部维持且余下三项首次闭合；观察项 8 项逐项复核（OBS-01 与 OBS-R21-L1-01 的 3.1s/60s 现象在 xray 26.10.21 栈上均未复现，其余维持登记），无一升级立案。**
- 判据说明：每项结论均有本轮独立新证据（沙箱探针、真机腿或 fresh 代码枚举），不得继承上一轮结论；N-35 对账以探针对象本身为准，N-37 只认落盘原始日志 + 名单存档。

## 1. F-01 §6 残余风险七项 — 逐项复核

原始清单见 `R20-FIX-F01-REPORT.md` §6（1 多节点/多端口、2 离线跳过、3 切隧道+去限流、4 realm/nft、5 force-delete、6 存量孤儿 2/12、7 DeleteCLimiters 瞬时失败无重试）。

### 1.1 多节点/多端口去重 — 维持关闭（沙箱双 mock + 真机单节点归一腿）

**沙箱探针（`R22-L2-probe-handler.log`，本轮新写双 mock 节点 harness，4/4 PASS 中的 2 项）**
- `TestR22L2MultiNodeMultiPortStaleCleanup`：端口集 {(1,22000),(1,22001),(2,22002)}、带旧快照同步 → 每节点 UpdateService ≥1 次、DeleteCLimiters 每节点恰 1 次（共 2 次，同名 `rule_conn_limit_<id>`），且均在其节点 UpdateService 之后。
- `TestR22L2OfflineNodeSkipBranch`：节点 2 从不拨号 → warnings 含“节点 relay2 不在线，已跳过下发”，在线节点删恰 1 次、离线节点 UpdateService 0 次、DeleteCLimiters 0 次（另见 journal 级 `sent-failed reason=update-fallback … 节点不在线` 佐证）。

**真机腿 Leg H（`R22-L2-legH-mp-setup.log` / `R22-L2-legH-mp.log`，转发 16）**
- 夹具第二端口行 (16, node 1, 22001) + 面板口 63107 → 更新 `{maxConn:0, ipMaxConn:0}` code 0 → `forward_port` 只剩 [(1,22001)]、gost limiter 消失、journal `DeleteCLimiters rule_conn_limit_16` 恰 1 条命令、UpdateService 只推 `[::]:22001`、监听面仅 22001 tcp+udp（63107 已停）。
- **机制 pin 死（与 R21 互补）**：`mutations.go:3492` `port` 无请求值时取 `GetMinForwardPort(id)`（最小值胜出）；`buildForwardPortEntriesWithPreservedInIP`（`:7035`）每入口节点只建一个条目。R21 腿剩面板口 11119、本轮剩夹具口 22001，皆因最小值胜出——**单节点双端口经任何产品更新必归一为单端口**，多端口线上形态只存在于多节点（R21-L2 §9 口径复证）。
- **未验证（测试床限制，非产品缺口）**：多节点真机腿仍不可跑（测试机仅 1 节点），覆盖来自沙箱双 mock（真实 WS 传输、按节点归因）。

### 1.2 离线跳过分支 — 维持关闭（R21-X-02 的缺陷在本基线上已修复，Leg A 反转）

**真机腿 Leg A（`R22-L2-legA-*.log`，转发 12，隧道 3，maxConn=5）**
- 建转发 → gost 服务 2、`rule_conn_limit_12` 在且被引用（md5 `857a0c21`）。
- 停 agent（节点 status 0）→ 离线更新 `{maxConn:0, ipMaxConn:0}` code 0，回执 `warnings: ['节点 w10-test 不在线，已跳过下发']`；gost md5 更新前后一致 `857a0c21`，limiter 仍在且被引用——离线跳过本身正确。
- `reset-failed` 后重启 agent → 节点上线；gost 服务 2、refs 空、limiter 消失；**重启后窗口内 journal `DeleteCLimiters {"limiter":"rule_conn_limit_12"}` 恰 1 条命令**（requestId `1_1792937092407661201`，14:04:52）——与 R21 立案 Leg B 的“计数 0”逐项反转。
- **口径核对**：`control_plane.go:507-510` 注释现为“留待下次成功同步——重连重推 redeployLocalNodeRuntime 在重推成功后按 ID 补删（R21-X-02）”，与代码（`upgrade.go:891` 重推成功后调 `deleteRedeployStaleRuleConnLimiter`）一致，R21-X-02 的口径偏差已不存在。

### 1.3 切隧道 + 去限流组合 — 维持关闭

**沙箱**：`TestR22L2TunnelSwitchAndDelimitComposition` PASS——batch 删 + 带旧快照同步，顺序恰为 DeleteService→DeleteCLimiters→UpdateService→DeleteCLimiters，DeleteCLimiters 共 2 次同名。

**真机腿 Leg B（`R22-L2-legB.log` / `R22-L2-legB-journal.log`，转发 13，隧道 4→5）**
- 更新 `{tunnelId:5, maxConn:0, ipMaxConn:0}` code 0 → 转发已切隧道 5、limiter 消失、服务 2（新隧道在位）；journal 同秒（14:05:16）顺序逐字为 DeleteService（`13_1_0_tcp/udp`）→ DeleteCLimiters（`rule_conn_limit_13`）→ UpdateService（`[::]:2219`）→ DeleteCLimiters（同名），共 4 条命令。

### 1.4 realm / nft 分支 — 维持关闭（设计主张真机复核通过）

**Fresh 枚举（`R22-L2-enumeration.log`，基线 b823421 全仓 grep）**
- `realm.go` 零 climiter 引用；`RenderRealm` 的 `RealmEndpoint` 无任何 limit/conn 字段（仅一处 `Interface binds outgoing connections` 注释）；`nftables_*` 零 `"limit"` 引用。
- 面板侧 Add/Update/DeleteCLimiters 发送点仅 `control_plane.go:2227/2232/2301` 与联邦 peer-share（`federation_resources.go:112/329`，`peer-share-` 前缀）；`cleanNodeConfigs` 仍无 CLimiters 字段（面板无 conn limiter 清扫器，R21-L2 §5 口径复证）。

**真机腿 Leg H-realm（`R22-L2-legH-realm*.log`，隧道 8 realm、转发 17）**
- 建带限流 realm 转发 → 面板警告“realm 引擎不支持限速和连接数限制，这些设置不生效”；gost 0 服务/0 limiter（md5 `23ca5d88` 未动）、realm 配置 1 endpoint（forward 17，`[::]:2005`）。
- 更新去限流 → endpoint 仍在、gost 仍 0/0；删除转发 → endpoint 归 0、gost 仍 0/0；窗口内 journal `DeleteCLimiters` 0 次、`rule_conn_limit_17` 0 次——realm 面确无 climiter 残留向量。
- 注记（探针误报，非产品）：`mkfwd` 驱动的 gost-limiter 断言在 realm 腿报 2 FAIL——该断言只适用于 gost 隧道，realm 腿的实际断言（gost 为空 + endpoint 在位）全部通过；见 §11。

### 1.5 forwardForceDelete gost 路径 — 维持关闭（R21-X-03 已修复，Leg C 反转）

**真机腿 Leg C（`R22-L2-legC*.log`，转发 14，隧道 6，inPort 59355）**
- 建带限流转发 → gost 服务 2、limiter 在且被引用；删前 `*:59355 LISTEN`。
- `force-delete` code 0 → gost 服务 0、climiters []、refs []；删后 `NO_LISTENER_59355`；journal `DeleteCLimiters {"limiter":"rule_conn_limit_14"}` 恰 1 条命令（14:05:50）——与 R21 立案 Leg D（gost 仍 14 服务、journal 0 次清理命令）逐项反转。
- 另：`mutations.go:3779` 的 F-03 修复注释在位（gost 分支逐节点 best-effort 调 `deleteForwardServicesOnNodeBatch`）。

### 1.6 存量孤儿 rule_conn_limit_2 / rule_conn_limit_12 — 环境侧无对象，维持关闭

- 新机 pristine gost 为 `{"services": null}`（md5 `23ca5d88`），climiters 为空——R20 旧机的存量孤儿 2/12 在本机不存在，无清理对象。
- 兜底面仍在：`go-gost` 全量套件 `R22-L2-probe-gost.log` exit 0（lifecycle ok，10.380s）；F-04 自愈代码在 7d93792→b823421 区间零改动（产品 diff 仅 7 个文件，见 §4），本轮全部腿亦未产生任何存量孤儿（每次删转发后 gost 回 `23ca5d88`）。

### 1.7 DeleteCLimiters 瞬时失败无重试 — 维持登记（设计内 best-effort，非立案）

- 代码 pin 死（b823421）：`deleteForwardConnLimiterOnNode`（`control_plane.go:2293-2304`）单次 `sendNodeCommand`（末参为 tolerateNotFound，非重试开关；`sendNodeCommandWithTimeout` `:844` 单次发送、6s 超时、无重试循环），失败只记日志、不挡调用方；注释明言“离线时调用方本就走 S-04/重连清理分支、会经同一漏斗重试”。
- 本轮全部腿的 DeleteCLimiters 均首发即达（Leg A/B/C/D 各恰 1 条，无重试、无丢失），无瞬时失败实例可供立案；残余风险与 R20/R21 同口径登记，不升级。

## 2. F-02 companion 第二道闸（syncCompanion dirty-check）— 真机腿首次补跑，通过

R21 未单独跑的真机腿，本轮补跑（沙箱 `TestConnIdleCompanionSyncMirrorsOrigin` 同在 §4 的 23/23 内 PASS）。

**真机腿 Leg E（`R22-L2-legE-*.log`，SS 2022-blake3 源入站 6，port 59960，idle=10，meteringEnabled=1）**
- 需求面：存量 share 订阅 1（`source_all=1`、enabled）已提供 demand；建源入站 code 0（`meteringActive: true`）→ 伴生行经产品路径物化：id 7、port 47813、`connIdleSeconds=10`、updated_time `1792937215053`；xray 渲染 level 2 `connIdle: 10`。
- 漂移（夹具，复刻沙箱漂移法）：DB `UPDATE node_inbound SET conn_idle_seconds=5 WHERE id=7` → compcheck 确认伴生 5、updated_time 未动。
- 触发（产品路径）：源入站全量重保存（idle 仍为 10；save 路径无条件调 `reconcileSSMetering()`，`kernel.go:1166`）→ 伴生回 10、updated_time 前进至 `1792937238082`；xray 渲染 level 2 仍为 10。**dirty-check 含 ConnIdleSeconds 的断言在真机成立**。
- No-op 侧：无漂移再次重保存 → 伴生 updated_time 仍为 `1792937238082`（未盲重写）。
- 收尾：删源入站 6 → 伴生 7 级联消失（compcheck []、DB 查无 companion_of>0 的行），见 §10。

## 3. F-03 相位腿按直连拓扑重跑 — 通过

R21-L1 未按直连拓扑重跑的相位腿，本轮用裸 VLESS 直连客户端（R20 方法）重跑；入站：VLESS 59944（id 8）、VMess 59945（id 9）、VLESS0 59946（id 10）；节点与本地客户端均为 Xray 26.10.21；dummy 目标为本机公网 IP（见 §11 探针事故 1）。

| 腿 | 方法 | 结果 | 对照 |
|---|---|---|---|
| F1 VLESS N=5 带数据 1MB | 直连单 burst | **关断 10.01s**（idle 自末数据起；dummy 全量 1,048,576 B、EOF 10.01s） | R20 10.20/10.21s、R21-L1 10.32s，一致 |
| F2 VLESS N=5 静默 | 直连无数据 | **关断 5.20s**（dummy EOF 5.00s、0 字节） | R20 5.20s、R21-L1 5.21s，一致 |
| F3 VLESS N=10 带数据 1MB | 直连单 burst（idle 改 10 后渲染 level 3 connIdle=10 已验） | **关断 20.01s** ∈(10,20] | R20 20.24/20.22s、R21-L1 20.21s，一致 |
| F4 相位早 burst2=5.5s | 直连双 burst（2,097,152 B 全量） | **idle 自 burst2 起 9.50s**（总 15.01s） | — |
| F5 相位晚 burst2=9.5s | 直连双 burst（2,097,152 B 全量） | **idle 自 burst2 起 5.50s**（总 15.02s） | — |
| F6 VLESS N=0 静默 | 直连、60s 观察 | **STILL_OPEN @60s**（客户端无 EOF；dummy EOF 65.86s 系客户端退出后的连带断开） | R20/R21 STILL_OPEN @20s，一致 |

- 相位区分成立：早→9.50s、晚→5.50s，**间距 4.00s**，与 R20 的 3.98s、R21-L1 的 3.99s 一致——窗口模型（相位决定，非恒 2N）在直连拓扑上复证。
- 原始日志：`tm/client-f{1,2,3,4,5,6b}.log` 与 `tm/dummy-f{1,2,3,4,5,6b}.log`（SUMMARY 行 6 条全在位）。
- 探针事故（均已处理，非产品问题，见 §11）：首版裸客户端等响应头才发数据（Xray 响应头懒发，见 F2 的 `resp-hdr=0000 elapsed=5.20`）、F6 首跑静默分支 25s 自断、127.0.0.1 目标被 freedom 黑洞。

## 4. R19 测试文件单独重跑 + shipped 修复测试 — 全部通过

沙箱快照（TMPDIR 指大盘，`R22-L2-probe-handler.log` 全量原始日志在位）：

| 组 | 结果 |
|---|---|
| 本轮新探针 `TestR22L2*`（双 mock 节点 harness） | **4/4 PASS**（多节点清理、离线跳过、切隧道组合、helper 设计锁定） |
| R21-F01 3 / F-02 5 / F-03 3（shipped，b823421 基线） | **11/11 PASS** |
| R20-F01 4（shipped） | **4/4 PASS** |
| `TestConnIdleCompanionSyncMirrorsOrigin`（F-02 第二道闸沙箱） | **1/1 PASS** |
| R19 文件单独重跑 `TestR19F03*`（`R22-L2-probe-r19.log`） | **3/3 PASS**（batch/control/无限制兜底各一） |
| **合计** | **23/23 PASS，0 FAIL**（`ok … 12.751s`） |

- 基线 diff 核对：`88f10db..b823421` 产品改动仅 7 个文件（`realm.go`、`control_plane.go`（注释 4 行）、`upgrade.go`、`mutations.go` + 3 个 `r21_fix_f0*_test.go`）；`7d93792..b823421` 产品 diff 另含 connidle-unlimited 面（与本路对象无交集）；`go-gost`、`add_service_guard.go` 零改动。

## 5. F-01 修复的 realm→gost 反向切换同型残留核查 — 无残留

**真机腿 Leg D（`R22-L2-legD*.log`，隧道 6、转发 15，maxConn=5）**
- 去程 gost→realm：engine 持久化 realm；gost 服务 0、climiters []；realm 配置 1 endpoint（forward 15，`[::]:23390`）；journal 恰 1 条 `DeleteCLimiters {"limiter":"rule_conn_limit_15"}`（14:06:00），DeleteService 在先（requestId 序）。
- 反向 realm→gost：engine 持久化回 `''`；gost 服务回 2、`rule_conn_limit_15` 重建且被引用；realm 配置收敛为 0 endpoint；journal 恰 1 条 `AddCLimiters {"limits":["$ 5"],"name":"rule_conn_limit_15"}`（原值，14:06:18）、`DeleteCLimiters` **0 条**——切回过程无多发、无同型残留。
- 沙箱同证：`TestR21F01EngineSwitchRealmToGostRecreatesLimiter` 在 §4 的 11/11 内 PASS。

## 6. R21-L1 未做项中 L2 覆盖过的五项 — 本轮基线上仍维持

V-01 末段所列 L2 已覆盖五项（F-01 §6 的多节点/多端口、离线跳过、切隧道+去限流、force-delete、realm/nft），本轮在 b823421 基线上逐项重验（§1.1–1.5），**全部维持**（其中离线跳过与 force-delete 两项的立案缺陷已由 F-02/F-03 修复反转，见 §1.2/§1.5；其余三项维持关闭）。
- 另：R21-L1 §6 余下三项（F-02 伴生第二道闸真机、F-03 相位腿直连拓扑、R19 测试文件单独重跑）本轮首次闭合（§2/§3/§4）——R21-L1 未做项至此在 R22 基线上**全部有新证据覆盖，无遗留**。

## 7. R21 观察项逐项复核

### 7.1 OBS-R21-L1-01（SS 2022-blake3-aes-128-gcm，本地 xray 客户端 60s 内不透传 EOF）— 未复现，维持登记，不立案

**真机腿 Leg G3（`R22-L2-legG-ssblake3.log` + `tm/client-g3.log` / `tm/dummy-g3.log`，源入站 6，idle=10）**
- 经本地 xray 客户端（SOCKS 59948 → SS 59960）发 1MB：dummy 全量 1,048,576 B、EOF 20.02s；**驱动侧 21.03s 收到 EOF**（仅 ~1s 透传滞后）——60s STILL_OPEN 现象未出现。
- 对照腿 G4（aes-256-gcm，idle=5，`R22-L2-legG-ssaes.log`）：驱动侧 10.01s、dummy EOF 10.00s，全量到齐。
- 判定：现象与 xray 版本栈强相关（R21 观测用本地客户端 26.3.27；本轮本地客户端与节点均为 26.10.21），面板代码路径未动；失效点仍在上游客户端行为，非面板代码路径。维持登记、不立案；若后续栈回退到 26.3.27 需重验。

### 7.2 R20-OBS-01（VMess/SS 约 3.1s 与 connIdle 无关）— 未复现，维持登记，不立案

**真机腿 Leg G1/G2（`R22-L2-legG-vmess5.log` / `R22-L2-legG-vmess10.log`，VMess 59945，经本地 xray 客户端 SOCKS 59947）**
- idle=5 → **关断 10.02s**（dummy EOF 10.00s，全量 1MB）；idle=10（list 显 10、渲染 level 4 connIdle=10 已验）→ **关断 20.02s**（dummy EOF 20.02s）——均落 (N,2N] 窗口，随配置走。
- SS 同型：aes-256-gcm idle=5 → 10.01s（§7.1 对照腿）；blake3 idle=10 → 20.02s。3.1s 快关族在本栈上均未复现。
- 判定：与 OBS-R21-L1-01 同因（xray 26.10.21 栈行为），不是面板回归——渲染随值变、计时随窗口变，面板侧行为符合文档模型。维持登记、不立案。

### 7.3 R20-OBS-02（O-A7-1 列表刷新交错竞态、O-A7-2 Webhook 示例时区）— 维持

- O-A7-1：`7d93792..b823421` 前端 diff 仅 `nodes.md`、`types.ts`、`inbound-form-modal.tsx`、`presets.ts` 4 个文件——`node.tsx` 的 `mergeNodeRealtimeState` / `latestProbe` 路径（`:425` / `:1385`）未动，竞态路径与 R20 登记时一致。
- O-A7-2：`doc/guide/notify.md` 在本区间 diff=0，示例仍写 `时间：2026-10-16 10:00:00 UTC`（`:42`），与 R20 登记一致。
- 两项均维持观察、不立案。

### 7.4 R20-OBS-03（L7-1…L7-4 发送未读/未具名声明）— 维持

Fresh 代码核对（b823421）：
- L7-1：`NodeApiItem`（`types.ts:1-30`）仍未具名声明 `latency` / `latencyTargets` / `panelVersion` / `versionWarning`，`[key: string]: unknown` 索引签名在位。
- L7-2：`node_latency.go:618-626` 仍仅显式自选时附 `latencyTargets` 键（`if raw, ok := targets[id]; ok` + `parseNodeTargetChoice` 非空才附），与前端“键存在 = 自定义”判定对齐。
- L7-3：`panelVersion` / `versionWarning` 全前端 `grep` 仍为 0 读。
- L7-4：`panel-sharing.tsx` 内 `relayPassword` / `versionWarning` / `relayUsername` 仍为 0 读（命中的 `nodeId` / `expiryTime` 均为表单字段，非列表响应读取）。
- 四项均为可读性/冗余口径，无断链，维持观察、不立案。

### 7.5 R20-OBS-04（rule_traffic_limit_* / user_conn_limit_* 同型残留可能）— 维持

- `rule_traffic_limit_*`：`cleanOrphanedLimiters`（`flow_policy.go:643–656`）+ `lookupSpeedLimiter` 前缀特判仍在位（代码未动）。
- `user_conn_limit_*`：本轮全部真机腿的 journal  grep 中 `user_conn_limit` 出现 0 次；gost 从未出现该名前缀；沙箱 `TestR22L2*` 与 shipped 测试中断言其永不删（§4 的 23/23 内含此断言）。
- 维持观察、不立案。

### 7.6 R20-OBS-05（VM-1 首跑 2.16s 冷启动、VM-5a 3.11s 为无判定带记录值）— 维持

- 本轮新计时（§3、§7.2）与 R20 记录值一致的只有 VLESS 面（10.01/20.01/5.20s）；VMess 面新值（10.02/20.02s）随窗口走，与 R20 的 3.11s 不一致——该差异即 §7.2 的栈行为变化，非面板漂移。OBS-05 本身是“无判定带的记录值”，维持登记、不立案。

### 7.7 OPEN-01 微观机理余项（sent-ok 瞬间冻结未达成）— 维持登记，不重开

- R21-L5 已用新方法（SIGSTOP 编排）使 K2 PASS：`send` 08:52:27.824 → `sent-ok` 08:52:27.836 → SIGKILL → 冻结快照 14 服务零重复、与冻结前 md5 逐字节相同（`R21-L5-unverified.md` §1）。
- 本轮 fresh 核对：`add_service_guard.go` 在 `7d93792..b823421` 区间零改动（产品 diff 见 §4），guard/fallback 路径（`control_plane.go:479` update-fallback、`server.go` 6s 超时不关闭 WS）与 R21-L5 登记一致；K2 的触发条件与结论未受本轮修复影响。
- 维持登记（R19 V-06 口径：SOLVED，不重开）；K2 重跑属 L5 面，本路不重复执行。

## 8. 新立案

**0 项。产品 P0=0、P1=0、P2=0、P3=0。** 无 CAND 编号发出。

## 9. 未验证清单

1. **多节点真机腿**：测试机仅 1 节点，多节点/多端口线上形态未在真机跑；覆盖来自沙箱双 mock（真实 WS 传输、按节点归因，§1.1）。非产品缺口，是测试床限制。
2. **K2 重跑**：本路未重跑 kill-freeze K2（属 L5 面；机制面零改动，见 §7.7）。
3. **远端节点与非 Debian 内核**（单机 Debian 12，无靶机）；**浏览器 DOM 鼠标实点**（能力不可验；served index md5 = dist md5 已由 R22-00 基线核对）；**真实 Gist 端到端**（无真实令牌，不得索取）；**单族 sock_diag 真机形态**（触发条件在本机内核上不可产生，承 R21）。
4. **F-01 §6-7 的瞬时失败实例**：本轮无一次 DeleteCLimiters 瞬时失败可供观测（全部首发即达），该残余风险的“失败分支”本身未被真机触发过——登记在案（§1.7）。

## 10. 恢复对账（`R22-L2-recovery.log`、`R22-L2-cleanup-del.log`、`R22-L2-machine-cleanup.log`）

- 测试入站全删（6 SS 源 + 伴生 7 级联消失、8 VLESS、9 VMess、10 VLESS0、11 SS aes），隧道全删（3/4/5/6/7/8），转发全删；xray 客户端支架（xc-vmess/xc-ss-blake3/xc-ss-aes）按 PID 文件精确杀停（禁用 pkill 模式匹配），dummy 进程无残留。
- 一个同步周期（~75s）后复核：counts **1/0/0/1/1 PASS**、gost md5 **`23ca5d88`**（0 服务/0 limiter，与腿前一致）、agent md5 **`27787cbf`**、节点 w10-test status 1、三容器 healthy；全部腿端口（59944/59945/59946/59947/59948/59949/59950/59951/59960/59961/22001/63107）归零、无残留进程。
- 机上已清：`/root/r22-l2/`（驱动、脚本、token、凭据、xc 配置）、`/root/panel-cred.txt`；机上其余 lane 目录（r22-l1、r22-l5、r22-stage）未动。锁已释放（`released by R22-L2 (was R22-L2 2026-10-25T14:03:30Z)`，check 显示 free）。
- 本地快照 `~/workspace/.r22-l2-snap` 与 `~/workspace/.r22-l2-tmp` 已删；探针测试文件已删；只读树 `~/workspace/.r22-tree` `git status` 干净。

## 11. 过程偏差与探针事故登记（均已处理，不影响结论）

1. **隧道 id 误用**（`R22-L2-legA-setup.log`，4 FAIL）：腿脚本硬编码隧道 1，新机 DB 自增 id 已到 3，建转发报 `code=-1 隧道不存在`。修正：以 `mktunnel` 返回的实际 id（3）建转发；无残留、无产品问题。
2. **journal 窗口起错三次**（Leg B、Leg D 去程/反向）：首版 grep 的 since 晚于命令时刻致 0 命中；均以驱动打印的 `journal-since` 为准重查，命令级证据逐条补齐（Leg B 4 条、Leg D 去程 1 条 DeleteCLimiters + DeleteService 在先、反向 1 条 AddCLimiters + 0 条 DeleteCLimiters）。日志在位，不影响结论。
3. **127.0.0.1 dummy 被黑洞**（F1 首跑，`R22-L2-legF-vless5-recon.log`）：裸 VLESS 客户端 60s 收不到响应头、dummy 未接。根因：节点 Xray 26.10.21 freedom 出站内置 `defaultBlockPrivateRule`（D-GEO-01 已决议保持拦截的同一机理）黑洞私网目标。处置：dummy 目标改本机公网 IP 16.146.199.68 后立即接通。**这是 D-GEO-01 机理在本轮的独立复现，不是 F-03 产品问题**；R20/R21 时代的直连腿（127.0.0.1 目标）在本栈上已不可复现，须以公网目标重跑（本轮全部计时腿均如此）。
4. **裸 VLESS 客户端等响应头才发数据**（F1 首跑）：Xray 的 2 字节 VLESS 响应头为懒发（F2 静默腿 `resp-hdr=0000 elapsed=5.20` 证实：响应头随会话关闭才到），先等头再发数据会让会话空闲至 N 秒关断、burst 发不出去。处置：请求后立即流水线发 burst1、不等响应头（响应头在读循环首 2 字节剥离）；F1 重跑干净通过（10.01s、全量到齐）。
5. **F6 首跑静默分支自断**（`R22-L2-legF-vless0.log`）：静默分支在响应头 recv 处以观察超时（25s）抛错退出，连带 dummy 侧 EOF，无法区分“节点未关”与“客户端自断”。处置：响应头改 5s 短超时跳过、读循环用 60s 长超时；重跑 F6b 正式 STILL_OPEN @60s。
6. **SSH 断管一次**（F1 首跑）：前台长腿（60s）超会话致断管、客户端随会话死亡、dummy 空等 60s。处置：改机上 nohup 脱离跑 + 轮询日志（R21-L1 同型教训复发，记入）。
7. **realm 腿 mkfwd 断言误报**（§1.4）：驱动的 gost-limiter 断言不适用 realm 隧道，2 FAIL 为探针误报；realm 腿实际断言（gost 为空 + endpoint 在位/归零 + journal 0 次）全部通过。
8. **凭据脱敏**：腿日志原样含两枚测试用 SS 密码（已删入站专用）与一枚存量订阅 token——本地落盘前已全部替换为 `[REDACTED-TEST-KEY]` / `[REDACTED-SUB-TOKEN]`（grep 复核残留 0 处）；本报告未复现任何密钥或 token。机上 xc 配置与 token 文件已随 `/root/r22-l2/` 删除。

## 12. 证据索引（探针原始日志同目录落盘）

| 文件 | 内容 |
|---|---|
| `R22-L2-probe-handler.log`（26,840 B） | 沙箱探针 23/23 PASS 全量原始日志 |
| `R22-L2-probe-r19.log`（3,536 B） | R19 测试文件单独重跑 3/3 |
| `R22-L2-probe-gost.log`（218 B） | go-gost 全量套件 exit 0 |
| `R22-L2-enumeration.log`（1,122 B） | realm/nft 全仓枚举 |
| `R22-L2-legs.py` / `R22-L2-dummy.py` / `R22-L2-vless.py` / `R22-L2-socks.py` / `R22-L2-runleg.sh` | 真机腿驱动与计时脚本（vless.py/runleg.sh 为修正后终版） |
| `R22-L2-tm-pristine.log` / `R22-L2-tm-pristine2.log` / `R22-L2-tm-backup.log` | 测试机 pristine 基线 + DB 备份（md5 `92c0b4ed…`） |
| `R22-L2-legA-*.log` | Leg A 离线更新→重连重推（转发 12） |
| `R22-L2-legB*.log` | Leg B 切隧道+去限流（转发 13） |
| `R22-L2-legC*.log` | Leg C force-delete（转发 14，inPort 59355） |
| `R22-L2-legD*.log` | Leg D 引擎切换往返（转发 15） |
| `R22-L2-legE*.log` | Leg E 伴生第二道闸（源 6 / 伴生 7） |
| `R22-L2-legF-*.log` | Leg F 直连相位腿（F1–F6，含探针事故过程日志） |
| `R22-L2-legG-*.log` | Leg G VMess/SS 计时（OBS-01 / OBS-R21-L1-01） |
| `R22-L2-legH-*.log` | Leg H 多端口归一 + realm 设计面（转发 16/17） |
| `R22-L2-cleanup-*.log` / `R22-L2-recovery.log` / `R22-L2-machine-cleanup.log` | 收尾删除、同步周期后复核、机上清场 |
| `tm/client-*.log` / `tm/dummy-*.log`（20 个） | 计时腿原始 client/dummy 日志（SUMMARY 10 条） |

END
— R22-L2-carryover END · 自证：本行之前正文字节数 26627 · END
