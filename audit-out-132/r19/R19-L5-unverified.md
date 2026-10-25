# R19 未验证清单复核（L5）— main @ d66042c

- **审计对象**：`main @ d66042c`（完整提交 `d66042cc0b7ac011ddfad09f3f2709b90dafda2e`）。d66042c = a0c18a0 + R18 修复 F-01…F-04 + 集成 + 文档。
- **依据**：只读读数树 `~/workspace/.r19-tree` @ d66042c 内 `audit-out-132/r18/R18-FIX-INT-REPORT.md` §6、`R18-X-ledger.md` §六（L5 §10 九项）、`R18-L5-unverified.md`、R17 修复报告 §10 剩余项。
- **复核日期**：2026-10-25（测试日志时戳 UTC 2026-10-25 03:20–03:56，即 CST 11:20–11:56）。
- **性质**：只读审计，不改产品代码、不打 tag、不发版。测试机操作：部署候选件（r19d panel + agent，均为 d66042c 构建）做行为腿，完事全量恢复 pristine 并对账。
- **护栏**：N-01…N-37 全程有效；裁定只认落盘原始日志 + 名单存档（N-37）。凭据与订阅 token 绝不明文入报告。
- **窗口**：独占窗口 2 持有者（锁 `~/workspace/.r19-testmachine-lock` 持 `L5 2026-10-25T02:56:52Z`）；L1 先用窗口 1。复核结束时锁文件**已消失**（见 §12），移除时间与操作者不明；本轮窗口内无并行 lane 机器活动的证据（PID 连续、无外来文件/进程、计数对账一致）。

## 结论总表

| # | 复核项 | R19-L5 结论 | 证据 |
|---|---|---|---|
| 1 | R17D-OPEN-01 微观机理（49ms 窗口内重复项去向；kill-freeze 两次未等到 sent-ok） | **部分进展，仍未验证关闭** | §1：B2 首次证实重复可**持续 42s 且非总为 6_1_0**（3_5_3 ×2，808 采样）；B3 毒化文件重启当场自砖；C1/C2（r19d guard）0 采样；kill-freeze K1 首次抓到 sent-ok 并 SIGKILL，冻结文件 12 服务无重复 |
| 2 | VLESS 用户凭证腿（u1/u4） | **PASS** | §2：VL-u1 5.21s、VL-u4 5.21s、VM-u1 供数 15.04s，全部落带；主凭证 VL-1 5.30s、VM-2 16.09s |
| 3 | SS 直连腿 | **未验证（方法失效）**；SS connIdle 行为由供数腿交叉验证 PASS | §3：SS-1（空口令，无效）与 SS-1r（正确 23 字符口令）均 STILL_OPEN@20s；根因在自写客户端（明文长度前缀，见 §3）；供数腿 SS-2 15.03s |
| 4 | Trojan 最小 xray A/B | **PASS** | §4：TR-1m STILL_OPEN@30s（无 connIdle 的最小对照）；TR-1 面板 5.09s（与 R17 逐值一致） |
| 5 | 下游供数 / 1MB 批量腿 | **PASS** | §5：供数 VM-4 12.14s（R17 逐值一致）、VM-4m 12.14s；批量 VM-5 3.11s（逐值一致）、SS-4 3.18s；新增 100KB 2.11s、10MB 3.10s、VLESS 1MB 10.21s（新档，见 §5 观察） |
| 6 | 管理 API 盲 append（R18 已 2/2 PASS） | **沿用 R18 结论，不重跑** | R18-L5 §6；本轮未动该面 |
| 7 | 远端节点 / 非 Debian 内核 | **仍未验证** | §7：单机 Debian 12；沙箱无 Docker；无远端节点 |
| 8 | 浏览器 DOM 腿 | **仍未验证** | §8：本路无浏览器任务能力；鼠标实点未做。served-chunk 基线已重定至 d66042c（见 §8，仅比对、不作 DOM 断言） |
| 9 | 订阅 1 流量计数器响应性 | **PASS** | §9：u1 供数后 +1388/+2652，与 R17 逐值一致；空闲 ~3.5 分钟零漂移；握手流量带刷新滞后（见 §9 订正） |
| OBS-01 | leg2 对照组第二重启 burst 触发源 | **已解决** | §10：周期 1 = L1 毒化重启 + systemd on-failure 循环（进程内重复 bind :14601）；周期 2 = L5 自身 A6 reset-failed+restart（跨 lane 干扰） |
| OBS-02 | VM-1 静默腿 2.13/2.15s | **已解决（向 R17 值收敛）** | §10：VM-1 1.13s、VM-1r 1.10s，复现 R17 的 1.14s 重测值；2.1s 系冷启动侧 |

## 0. 门前基线（测试机，2026-10-25 02:57Z 与 03:25:30Z 两次核对，只读）

面板 1.3.5（`flmx-backend/frontend:r1gate-1.3.5`，backend healthy，前端 `(unhealthy)` 为门前已存在）；agent `aff338e3676bc56d0f3986a1a62f8fdd`（main @ 5473377 未修复版）、active、NRestarts=0；`gost.json` md5 `d48f09e132ebe99fe97a619dd72d42d4`、328 行、12 服务；unit md5 `a12afd9c36fba7b76287210efaa199cb`（StandardOutput/Error=null）；计数 node 1 / tunnel 4 / forward 6 / inbound 578 / subscription 18（`subscription/list` 返回分页 `data.items`，非 `data` 列表——早期计数脚本误读为 0，已订正）；订阅 1 计数器 up 3648 / down 2105821；磁盘门前 3.6G 空闲（L1 工作后 3.2G）。

候选构建（沙箱，go1.25.1，CGO_ENABLED=0，`-trimpath`，`git archive d66042c go-gost go-backend vite-frontend`）：agent `flux_agent-r19d` md5 `053b99fa0d706fb68f19198080de2a77`（50,225,950 字节，`upsertNamed` 符号在位，F-02/F-04）；`paneld` md5 `6f6637e9aa65f4125dab19f8fae88703`（43,839,150 字节，`formatDeletedForwardsSummary`/`formatForwardDeleteLabel` 在位，F-01）；`stage.tgz` md5 `1c66c7169430b9a282eeaf9099354156`（24,530,351 字节；`dist/index.html` md5 `8f115c6c6c26ff728cb3173b04e16389`，59 资产；`nginx.conf` md5 `8e4c555a283c283a31110b5c9601b479`）。前端 dist 复用 `/root/r17d/stage/dist`——`git diff 5473377..d66042c -- vite-frontend` 与 `git diff 5125ff5..d66042c -- vite-frontend` 均为空，沙箱无 pnpm 工具链可重建。构建记录见 `~/workspace/r19-l5-scratch/logs/R19-L5-build.log`。

## 1. R17D-OPEN-01 微观机理 — 部分进展，仍未验证关闭

### 1.1 Phase A（污染，作废）

03:11–03:24Z 污染事件：L1 未查锁即上机部署（agent `flux_agent.r19l1` 与本轮 d66042c 构建逐字节一致；后端 `flmx-backend:r19l1` md5 `a824ee651222acd5985c36ddf6c5a060`，与本轮 r19d paneld 不同）；03:15:34、03:17:54 两次重启；03:17 毒化测试、03:20 SIGHUP 测试。我的 Phase A A1–A3（03:20:19–03:22:46Z）因此跑在**已修复 d66042c agent + r19l1 后端**上，非未修复 + 1.3.5，A1–A3 结果**全部作废**，仅留作事件证据（`R19-L5-phaseA-fastpoll.log`；污染全记见 `~/workspace/r19-l5-scratch/logs/R19-L5-contamination-note.md`）。L1 约 03:24Z 恢复 pristine；03:25:16Z 与 03:29:46Z 两次静默 tick 无 L1 活动（`/root/r19-l1/` 自 03:20 无新文件、agent PID 不变、NRestarts=0、状态 pristine），随后开工。

### 1.2 Phase A-clean B1–B3（未修复 agent `aff338e3` + 1.3.5，独占窗口，03:29:52–03:32:18Z）

日志 `R19-L5-phaseA-fastpoll-clean.log`（1703 行）。

- **B1**：0 采样；事后 pristine `d48f09e1`。
- **B2**：重启后 2.87s（03:30:47.880）出现重复——**`3_5_3_tcp`/`3_5_3_udp` ×2（不是 6_1_0）**，**808 次连续采样至窗口结束（约 42s 持续存在，无自愈）**。事后：14 服务，md5 `54adf6d0e2141c8dfd8aaf8f743dea25`，378 行；agent 保持 active（PID 748305）。样本已存档 `R19-L5-fastdup-B2-1.json`（md5 已核）。**关键发现：重复项不总为 6_1_0，也不总为瞬态——与“49ms 窗口、很快被覆盖”的全称假设直接相抵。**
- **B3**：事前已毒化（14 服务）；对毒化文件重启 → **agent FAILED（pid 0）**——R17-X-01 自砖链的现场复现；864 采样；同 md5 `54adf6d0`。
- B 轮 journal 尾为空（未修复 agent + null-output unit 上无 AddService/UpdateService 行可取）——与 R17-C 设 journal unit 的动机一致。
- 恢复 03:33:28Z：由 `/root/r17d/gost.json.pristine` 恢复 pristine gost，`reset-failed` + start；agent active PID 750801，NRestarts=0，gost `d48f09e1`。清理 1672 个 fastdup 文件时，我的 `rm -f fastdup-B2-*.json` glob **误删机上 keep 副本** `fastdup-B2-1.keep.json`（glob 命中 `*.keep.json`）；本地存档完好，见 §12。

### 1.3 Phase B C1/C2（未修复 agent + r19d panel，journal unit 已装，03:35:38–03:37:15Z）

部署 r19d panel（03:33:42–03:34:18Z，`R19-L5-deploy-panel.log`）：后端 `flmx-backend:r19d`（镜像 `1e85171599ad`）healthy，前端 `flmx-frontend:r19d`（`3b008d0af45c`），`FLUX_VERSION=1.4.8-r19d`，GET / 200；agent 仍 `aff338e3` active PID 751578；journal unit 已装（md5 `05449a73f3d21b037eb082be0a5ff438`）；gost pristine `d48f09e1`；计数 1/578。

C1/C2 快采（`R19-L5-phaseB-fastpoll.log`，99 行）：**两轮均为 0 采样**，事后 pristine `d48f09e1`。journal 现可取 AddService 行（unit 生效，C2 尾有 AddService 接收记录）。03:34:32Z 一次 SSH broken pipe 致首发 C1/C2 盲跑——已按 PID 杀除孤儿进程（PID 751769/751770），改 nohup + 机上记日志重跑（见 §12）。与 B2 对照：同一未修复 agent 下，1.3.5 面板可写出持续重复，r19d 面板（B 路 guard）两轮零重复——guard 抑制重复发送的支撑证据（样本仅两轮，不作证明）。

### 1.4 kill-freeze K1/K2（未修复 agent + r19d panel，03:37:42–03:38:59Z）

日志 `R19-L5-killfreeze.log` + `R19-L5-killwatch-K1.log` / `R19-L5-killwatch-K2.log`。watch 信号已在 d66042c 源码核对：`go-backend/internal/http/handler/add_service_guard.go:272` 记 `decision=sent-ok reason=update-fallback`（`control_plane.go:465` 调用）。

- **K1：sent-ok 首次捕获。**03:37:49.564 后端日志出现 `node=1 decision=sent-ok reason=update-fallback services=[6_1_0_tcp,6_1_0_udp] err=-`，watch 当毫秒 SIGKILL agent PID 752830（killwatch exit=0）。agent 被 systemd 重启（新 PID 752936，active）。冻结读数（kill+2s）：md5 `b448b12d…`、328 行、**12 服务、无重复**。事前为 pristine `d48f09e1`。结论：guard 路径下，sent-ok 瞬间磁盘上**无**重复项（发送与落盘之间存在滞后，或 guard 的单次发送本就无重复可写）。注意：kill+2s 的读数窗口内新 agent 已重启并重同步，冻结读数非严格的 kill 瞬间快照；但若文件在 kill 时已毒化（14 服务），新 agent 启动应如 B3 般失败——它正常 active，故 kill 时文件为 12 服务的推断成立。
  - `b448b12d` 与 pristine `d48f09e1` 同为 328 行/12 服务，字节不同。**该文件副本未保存**，字节差异未验证——仅记假设：服务顺序不同（与早先 `2359c6d2` 重排同类，Go map 迭代顺序所致的 persist 重排）。不得作实。
- **K2：TIMEOUT。**60s 内无 sent-ok（killwatch exit=1）。sent-ok 只在 fallback 路径触发，非每次重启都有；后端日志累计 sent-ok 行共 3 行（K1 占 1 行）。与 R17 两次未等到 sent-ok 一致——该事件本就稀有。
- 小结：kill-freeze 首次完成“sent-ok 捕获 + kill”本身（K1），但冻结文件干净，未能复现“sent-ok 瞬间文件已含重复”的断言；未修复路径的重复落盘由 B2/B3 另行确立。OPEN-01 微观机理**仍未验证关闭**。

## 2. VLESS 用户凭证腿（item 2）— PASS

r19d agent 已装（03:40:29Z，`R19-L5-agent-swap.log`）：`053b99fa…`、active PID 753719、NRestarts=0、gost pristine `d48f09e1`；面板 r19d（backend healthy，前端 `(unhealthy)` 与 1.3.5 同态）。

创建 5 个测试入站（`R19-L5-leg4-create.log`，03:40:43–03:41:08Z）：941 VLESS 59904 idle=5、942 VMess 59905 idle=5、943 VMess-ctl 59908 idle=0、944 SS 59907 idle=5、945 Trojan 59906 idle=5；计数 578→583。D3 渲染核对：policy levels 1–4 均为 `connIdle: 5`；`flmx-idle-*` 出站 userLevel 正确（941→1、942→2、944→3、945→4，-u1/-u4 同级；SS 仅通用出站，无 per-user——与 R17-D 一致）；规则顺序 per-user idle → 通用 idle → mark（`flmx-u1`/`flmx-u4`）；59904/59905/59908/59907/59906 各 3 客户端（SS 0 客户端，见 §3）。xray 配置件 `xray-config-created.json` md5 `05aa40087320b48bab045c18b0212bde`。凭据程序化提取至机上 `/root/r19/creds-extract.json`（0600）。

计时腿（`R19-L5-leg4-legs.log`，3487 行，03:42:59–03:47:46Z；脚手架 `R19-L5-leg4-scaffold.log` 全起：dummies 59909/59910/59911/59915/59916、xclients 11080–11088、minxray 59912/59913/59914）：

| 腿 | 结果 | 判定带 | 结论 |
|---|---|---|---|
| R19-VL-1 VLESS 直连 main | CLOSED 5.30s（header 5.29s） | 3.5–6.5s | PASS（R17D 5.25s、R18-L1 5.26s） |
| R19-VL-u1 VLESS 直连 u1 | CLOSED 5.21s（header 5.21s） | 3.5–6.5s | **PASS（item 2）** |
| R19-VL-u4 VLESS 直连 u4 | CLOSED 5.21s（header 5.21s） | 3.5–6.5s | **PASS（item 2）** |
| R19-VM-2 VMess 供数 main | CLOSED 16.09s | 13.5–16.5s | PASS（R17D 16.02s） |
| R19-VM-u1 VMess 供数 u1 | CLOSED 15.04s | 13.5–16.5s | **PASS（item 2 供数变体；同时喂订阅 1 计数器，见 §9）** |

## 3. SS 直连腿（item 3）— 未验证（方法失效）

| 腿 | 结果 | 结论 |
|---|---|---|
| R19-SS-1 SS 直连 main | STILL_OPEN@20s | 口令为空（提取 bug），**无效** |
| R19-SS-1r SS 直连 main（重跑，正确口令） | STILL_OPEN@20s | **未验证** |
| R19-SS-2 SS 供数 main（door） | CLOSED 15.03s（connected 0.00s） | PASS（R17D 15.02s）——SS connIdle 行为的权威证据 |

SS-1 的提取 bug：凭据提取脚本从 xray 入站 `clients[]` 取 SS 口令，而 SS 入站无 clients（D3 示 `59907 shadowsocks clients= 0`），口令在入站 settings 里，故取到空串（长度 0）。重跑 SS-1r 用 xray 配置中 SS 入站 settings 的正确口令（23 字符，`R19-L5-ss1-rerun.log`）仍 STILL_OPEN@20s，排除口令因素。

根因（对 `/root/r17/ss_test.py` 的代码读数，非产品发现）：客户端发送 `salt + 明文 2 字节长度 + 密文`；SS AEAD 规范要求长度前缀本身也要 AEAD 加密——服务端无法解密长度前缀，会话始终不建立，connIdle 无从应用，连接被挂起。这与 R17 既定口径一致（自写实现不可靠；SS 5s 结论以 dokodemo-door 供数腿为准）。直连腿若要真正验证，需修复该客户端的 AEAD 分块（或改用真实 SS 客户端），留待后续。

## 4. Trojan 最小 xray A/B（item 4）— PASS

| 腿 | 结果 | 结论 |
|---|---|---|
| R19-TR-1 Trojan 直连 main | CLOSED 5.09s（header 0.05s） | PASS（R17D 5.09s 逐值一致） |
| R19-TR-1m Trojan 最小（自签证书，CERT_NONE） | STILL_OPEN@30s（header 0.04s） | **PASS（item 4）**——无 connIdle 的最小对照保持打开，与面板腿形成干净 A/B 对比 |

## 5. 下游供数 / 1MB 批量腿（item 5）— PASS

| 腿 | 结果 | R17 对照 | 结论 |
|---|---|---|---|
| R19-VM-4 下游供数 面板（dummy2 0.5s/10.0s） | CLOSED 12.14s | 12.14s | PASS（逐值一致） |
| R19-VM-4m 下游供数 最小 | CLOSED 12.14s | 12.11s | PASS（±0.03s） |
| R19-VM-5 1MB 批量 | CLOSED 3.11s | 3.11s | PASS（逐值一致） |
| R19-VM-5a 100KB 批量 | CLOSED 2.11s | 无（新档） | 记录值 |
| R19-VM-5b 10MB 批量 | CLOSED 3.10s | 无（新档） | 记录值 |
| R19-SS-4 SS 1MB 批量（door） | CLOSED 3.18s | 3.12s | PASS（±0.06s） |
| R19-VL-5 VLESS 1MB 批量 | CLOSED 10.21s（首包 4096B 于 0.21s） | 无（新档） | 记录值，见观察 |

**观察（非 FAIL，无既定判定带）**：VLESS 1MB 批量 10.21s，约为 VMess/SS 同档（~3.1s）的 3 倍、约为 connIdle=5s 的 2 倍。数据在 0.21s 已到齐，随后空闲至 10.21s 才关断——VLESS 批量腿的关断形态与 VMess/SS 不同（疑似两段 idle 计时串联，或 VLESS 会话的关断路径不同）。仅记录数值与形态，不立结论，建议后续轮次单独立项探 VLESS 批量关断机理。

无值对照（全部 STILL_OPEN@30s，预期内）：R19-CTL-supply（VMess 供数无 idle 入站）、R19-VM-2m（VMess 供数最小）、R19-SS-2m（SS 供数最小）、R19-TR-1m（见 §4）。

## 6. 管理 API 盲 append（item 6）— 沿用 R18 结论

R18-L5 §6 已 2/2 探针 PASS；本轮未动该面，无新证据，结论不变。

## 7. 远端节点 / 非 Debian 内核（item 7）— 仍未验证

单机 Debian 12 测试机；沙箱无 Docker；无远端节点可用。无任何新证据。

## 8. 浏览器 DOM 腿（item 8）— 仍未验证

本路无浏览器任务能力；鼠标实点未做。served-chunk 基线已重定至 d66042c，**仅比对、不作 DOM 断言**：

- d66042c dist：`dist/index.html` md5 `8f115c6c6c26ff728cb3173b04e16389`（8776 字节，59 资产；JS `index-D9z2KQ_9.js`、CSS `index-AKC-1l3F.css`）。
- 现服 1.3.5 index.html（`http://127.0.0.1:6366/`，http=200，8776 字节）：md5 `0f18962440dde5a19507014e63454d5b`；JS `index-DVFTB_h9.js`、CSS `index-C0LAZBTR.css`。
- 两文件 diff **仅 249–250 行**（两处资产引用），其余逐字节一致。差异已解释：`git diff 1.3.5..d66042c -- vite-frontend` = 65 文件、4240 插入——1.3.5 镜像的前端源码与 d66042c 不同，资产哈希不同是预期内。
- 缺口：r19d 前端部署窗口（03:34–03:52Z）内未抓取 served r19d index.html 的 md5（仅部署日志 GET / 200），故无“r19d 容器实际吐出 d66042c dist”的直接散列证据；镜像由含该 dist 的 stage.tgz 构建则有部署日志为证。

## 9. 订阅 1 流量计数器响应性（item 9）— PASS

`POST /api/v1/subscription/traffic {"id":1}`：

| 时点 | up | down | 说明 |
|---|---|---|---|
| 门前基线 | 3648 | 2105821 | R17 门后值一致 |
| 腿结束 03:47:46Z | 5036 | 2108473 | **+1388 / +2652，与 R17 u1 供数值逐值一致**；小时桶 `1792897200000` 记 `up 1388 / down 2652` |
| 恢复后 03:52:26Z | 5200 | 2108749 | +164 / +276（见下） |
| 空闲直读 ~03:56Z | 5200 | 2108749 | **空闲约 3.5 分钟零漂移** |

+1388/+2652 精确复现 R17 的 u1 供数增量，证明计数器对 u1 供数流量响应且计量确定。**订正**：03:50:01 的即时 bracket 读数（VL-u1b 直连腿前后均为 5036/2108473）曾被读作“直连腿贡献为 0”——该读数距腿结束仅约 1s，落在**计数刷新滞后**内，不能作零贡献断言。03:52:26 的 +164/+276 为 SS-1r + VL-u1b 两腿握手流量合并刷新（两腿间不可分），即握手级流量亦被计量、只是刷新滞后。空闲零漂移排除后台漂移。

## 10. R18-OBS-01/02 状态复核

### 10.1 OBS-01（leg2 对照组第二重启 burst 触发源）— 已解决

新证据来自存档日志 `R18-L1-control-journal2.log`（74 行）+ 本轮 `R19-L5-phaseA-fastpoll.log` 第 172 行：

- **周期 1（01:44:32.122–01:44:33.294Z）**：触发源 = L1 自身的毒化重启（Started 01:44:31.972）+ systemd `Restart=on-failure` 循环。每次失败的机内原因：进程内重复 bind `listen tcp 0.0.0.0:14601: bind: address already in use`——毒化文件含 `6_1_0_tcp` ×2 于 `[::]:14601`，第二份的 listen 在同一进程内失败。计数 1–5，01:44:33.294 `Start request repeated too quickly`。
- **周期 2（01:44:33.472–01:44:34.794Z）**：触发源 = **L5 的 A6 快采 `systemctl reset-failed` + `restart`（约 01:44:33.43）**（A6 事前快照 01:44:33.430；journal Started 01:44:33.472，相隔 42ms）。`reset-failed` 清除限速，计数重置 1–5，同循环，01:44:34.794 再次限速。属跨 lane 干扰，收紧 R18-PROC-01。
- 算术闭合：11 条 `Started` = 1 次 sanity start + 1 次毒化重启 + 周期 1 的 4 次重试 + 1 次 A6 重启 + 周期 2 的 4 次重试。

### 10.2 OBS-02（VM-1 静默腿 2.13/2.15s）— 已解决（向 R17 值收敛）

R19-VM-1 静默 main：**CLOSED 1.13s**；R19-VM-1r 重测：**CLOSED 1.10s**。复现 R17 的 1.14s 重测值（R17 首跑 2.15s、重测 1.14s；R18 的 2.13/2.15s 为冷启动侧）。O-148B-1 带 0.5–2.0s：两跑均在带内。静默腿关断时延分布为冷启动 ~2.1s / 热启动 ~1.1s 双态，属上游行为，非 FAIL。

## 11. 恢复与对账（03:50:14–03:52:26Z）

D4 清理（`R19-L5-leg4-delete.log`）：5 入站全删（http=200、code=0），计数回 578，r17 残留 `[]`；agent xray 配置还原（policy null、`flmx-idle` 出站 []、idle 规则 []、出站回 `['direct','block','flmx-u1','flmx-u4','flmx-u12']`、规则 3、入站 11）；脚手架进程与测试端口全清；agent 自有 xray 仍在（PID 756245）。

恢复 pristine（`R19-L5-restore.log`）：agent 回 `aff338e3676bc56d0f3986a1a62f8fdd`，unit 回 `a12afd9c36fba7b76287210efaa199cb`（StandardOutput/Error=null），gost `d48f09e132ebe99fe97a619dd72d42d4`（328 行），compose/env 回 1.3.5（md5 与 pristine 备份一致：compose `3afbac11…`、env `fffcd005…`），面板 1.3.5（backend healthy，GET / 200），r19d 镜像已删（`flmx-backend:r19d` `1e85171599ad`、`flmx-frontend:r19d` `3b008d0af45c`）。计数：node 1、tunnel 4、forward 6（statuses 全 1）、inbound 578、subscription 18（list items 实数；恢复脚本所印“3”为其自身计数 bug——误读分页字段，已用 `data.items` 长度 18 核对）。

**表数 49→50**：面板 sqlite 现 50 表，多出 `node_traffic`。该表在 d66042c 源码中有定义（`node_traffic.go`、`metrics/ingestion.go`、model、repository、contract test），含 11 行小时桶（10-24 15:00–10-25 03:00 UTC，node 1；03:00 桶含本轮腿流量，属产品正常记账）。系 d66042c 系面板迁移建表 + 历史回填，非测试残留数据；由 L1 的 r19l1 面板或本轮 r19d 面板建表，二者皆 d66042c 系，不可分。1.3.5 面板在其上运行正常（healthy、计数与接口均正常）。磁盘：恢复后 3.1G 空闲（门前 3.6G；L1 工作后 3.2G；r19d 镜像已删，余量为运行期日志与 DB 增长）。

## 12. 事件与流程记录

1. **L1 未查锁上机（03:11–03:24Z）**：见 §1.1。A1–A3 作废；我的 3 次重启可能干扰 L1 的 SIGHUP 窗口。R18-PROC-01 的锁检查须由各 lane 在每次上机前执行，本轮 L1 未执行。
2. **锁文件消失**：复核结束时 `~/workspace/.r19-testmachine-lock` 不存在（02:56:52Z 写入时持 `L5 …`）。移除时间与操作者不明；窗口内无并行 lane 机器活动证据。报告如实登记，不重建锁（工作已结束）。
3. **SSH broken pipe 两次**：03:34:32Z（Phase B 首发，致 C1/C2 盲跑）与约 03:55Z（60s 睡眠中的 idle-check）。tsconnect 代理会丢弃长时 SSH 会话；对策为 nohup + 机上记日志 + 短调用轮询，已全程执行。另一次 idle-check 的双重嵌套 bash -c 引用致 `{"id":1}` 体损坏（`code:-1 请求参数错误`，两次皆然）；直接调用则正常——该失败为调用层问题，非面板问题。
4. **glob 误删**：`rm -f fastdup-B2-*.json` 误删机上 keep 副本 `fastdup-B2-1.keep.json`（命中 `*.keep.json`）；本地存档 `R19-L5-fastdup-B2-1.json` 完好。
5. **`b448b12d` 副本未保存**（见 §1.4）：K1 冻结文件的字节差异未验证，仅记假设。
6. **r19d 前端 served md5 未抓取**（见 §8）：部署窗口内只记 GET / 200，未取 served index.html 散列。
7. **机上 `/tmp/gate-*.json` 已删**：含订阅 token 的临时文件（03:16，L1 门前遗留），按既定口径清除；token 本身绝不入报告。L1 的其他 `/tmp` 文件（`create-*-resp.json`、`del-93*.json` 等）未动。
8. **VLESS 1MB 批量 10.21s**（见 §5）：新档数值，形态异常，留待后续单独立项。

## 13. 证据清单

| 文件 | 内容 | 字节数 |
|---|---|---|
| `audit-out-132/r19/R19-L5-phaseA-fastpoll.log` | Phase A 污染轮原始日志（A1–A3，作废，仅作事件证据） | 37,625 |
| `audit-out-132/r19/R19-L5-phaseA-fastpoll-clean.log` | Phase A-clean B1–B3 原始日志（1703 行；B2 808 采样、B3 自砖） | 73,488 |
| `audit-out-132/r19/R19-L5-fastdup-B2-1.json` | B2 捕获的重复文件原件（378 行；机上 keep 副本已误删，见 §12） | 7,212 |
| `audit-out-132/r19/R19-L5-deploy-panel.log` | r19d panel 部署日志 | 3,561 |
| `audit-out-132/r19/R19-L5-phaseB-fastpoll.log` | Phase B C1/C2 原始日志（99 行；两轮 0 采样） | 24,991 |
| `audit-out-132/r19/R19-L5-killfreeze.log` | kill-freeze K1/K2 原始日志 | 1,507 |
| `audit-out-132/r19/R19-L5-killwatch-K1.log` | K1 watch 原始输出（sent-ok + SIGKILL，03:37:49.564） | 175 |
| `audit-out-132/r19/R19-L5-killwatch-K2.log` | K2 watch 原始输出（TIMEOUT） | 37 |
| `audit-out-132/r19/R19-L5-agent-swap.log` | r19d agent 换装日志 | 1,155 |
| `audit-out-132/r19/R19-L5-leg4-create.log` | leg4 创建 + D3 渲染核对 | 2,456 |
| `audit-out-132/r19/R19-L5-leg4-scaffold.log` | leg4 脚手架（dummies/xclients/minxray 全起） | 3,919 |
| `audit-out-132/r19/R19-L5-leg4-legs.log` | 19 条计时腿原始日志（3487 行） | 123,013 |
| `audit-out-132/r19/R19-L5-ss1-rerun.log` | SS-1r 重跑 + VL-u1b bracket 原始日志 | 432 |
| `audit-out-132/r19/R19-L5-leg4-delete.log` | leg4 删除 + D4 清理核对 | 1,111 |
| `audit-out-132/r19/R19-L5-restore.log` | 恢复 pristine + 对账日志 | 3,688 |
| `~/workspace/r19-l5-scratch/logs/R19-L5-contamination-note.md` | 污染事件全记 | 2,891 |
| `~/workspace/r19-l5-scratch/logs/R19-L5-build.log` | 候选构建记录 | 341 |
| 机上 `/root/r19/` | 候选件与脚本（`flux_agent-r19d`、`paneld`、`stage.tgz`、各脚本、pristine 备份） | 见机上 |
| 本报告 | `audit-out-132/r19/R19-L5-unverified.md` | 见自证 |

## 仍未验证清单

- item 1（OPEN-01 微观机理）：重复落盘的 49ms 窗口内去向未定位；K1 证明 guard 路径 sent-ok 瞬间磁盘无重复，但未修复路径的重复写盘时机仍未在毫秒级冻结；`b448b12d` 字节差异未验证。
- item 3（SS 直连腿）：自写 AEAD 客户端长度前缀错误，会话不建立；需修客户端或改用真实 SS 客户端重跑。
- item 7（远端节点 / 非 Debian 内核）：无手段。
- item 8（浏览器 DOM 腿）：无浏览器任务能力；鼠标实点未做；r19d served index.html 散列未抓取。
- 观察项：VLESS 1MB 批量 10.21s 的关断机理未探。


— R19-L5-unverified END · 自证：本行之前正文字节数 25685 · END
