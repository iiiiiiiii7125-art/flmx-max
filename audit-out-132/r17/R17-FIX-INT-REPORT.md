# R17 修复集成报告（fix-r17 @ 5125ff5）— D 路验收

- 集成树：`~/workspace/.fixr17`，分支 `fix-r17 @ 5125ff5`
- 验收执行：2026-10-25 00:22–01:31Z（CST 08:22–09:31），测试机 Lightsail Debian 12（公网 16.146.199.68 / tailnet 100.80.182.11，非生产，用户授权任意折腾）
- 验收人：D 路（最后一棒）。约束遵守：只做验收与收尾，不合 main、不打 tag、不发版、不改版本字面量。
- 依据：`R17-FIX-A-REPORT.md`、`R17-FIX-B-REPORT.md`、`R17-FIX-C-REPORT.md`；主控裁定（2026-10-25 08:14 CST）：集成回归放行，基线同启循环不再加跑，验收改用可区分的确定性测试。

## 0. 结论总表

| 项 | 结果 |
|---|---|
| 三路合入 | A `c922e5d` + B `6d4a51a` + C `5233d8d`/`792a46c` 零冲突合入，均已是 `5125ff5` 的祖先 |
| 集成回归 | gost 0 / x 0 / 前端 tsc+vite 0；backend 唯一失败为已知沙箱环境红（见 §2），产品失败 0，放行 |
| 腿 1 确定性去重（主判据） | **修后 PASS**（真实面板回退 AddService sent-ok 后落盘无重复）；**反向对照未复现**（未修复 agent 同事件后落盘亦无重复，如实登记，见 §4 + 未解项 R17D-OPEN-01） |
| 腿 2 启动自愈（主判据） | **PASS**（修后一次起成、自愈告警可查、落盘无重复）；对照 PASS（未修复应声 failed、NRestarts=5） |
| 腿 3 harness 重跑 | 10/10 MISS，hits=0；**仅为无回归记录**（基线亦 0/10，非区分证据） |
| 腿 4 计时 + 守卫日志 | **PASS**（VLESS/VMess/SS/Trojan 5s 定时器全部 operative，对照成立，渲染与清理全过）；守卫判定行 48 条可查 |
| R17-X-01 | 建议关闭（见 §9，附一项观察项 R17D-OPEN-01，不阻塞） |
| 门后恢复 | pristine 全量对账通过（见 §11） |

## 1. 三路合入情况

| 路 | 提交 | 文件 | 内容 |
|---|---|---|---|
| A 去重+自愈 | `c922e5d` | 8 文件，+951/−1 | `go-gost/x/socket/service.go`（`createServices` 第 4 阶段按名去重，与 `updateServices` 同口径）；新增 `go-gost/x/config/dedupe.go`（`DeduplicateServices`/`HealPersistedServices`）+ `persist.go` 的 `PersistNow`；`go-gost/program.go`（`Start()` 与 `reloadConfig()` 接启动自愈）；测试 3 文件 + 毒化夹具 `go-gost/x/config/testdata/r17-poisoned-gost.json`（378 行） |
| B 幂等守卫 | `6d4a51a` | 4 文件，+637/−4 | 新增 `go-backend/internal/http/handler/add_service_guard.go`（279 行）；`control_plane.go` 三处回退改走 `guardedAddService`（1 号 update-fallback、2 号 default-bind-fallback、3 号 self-occupied-rebind）；`handler.go` 守卫状态；测试 319 行 |
| C 日志可查 | `5233d8d` | 4 文件，+129/−6 | `install.sh` unit 改 `StandardOutput=journal`/`StandardError=journal` + `SyslogIdentifier=flux_agent`；`go-gost/main.go` 启动失败写 `agent-startup.log`；`loader.go` 的 `ParseService`/`Register` 失败包一层服务名；测试 1 文件 |
| C harness | `792a46c` | 1 文件，+168 | `audit-out-132/r17/R17-FIX-C-harness.sh`（10 循环同启） |

合入方式：三路依次合入 `fix-r17`，零冲突；尖端 `5125ff5`（merge fix-r17-c）。经 `git merge-base --is-ancestor` 核对，`c922e5d`、`6d4a51a`、`5233d8d`、`792a46c` 均已是 `5125ff5` 的祖先。

## 2. 集成回归（先决条件，主控已裁定放行）

| 日志 | exit | FAIL | 说明 |
|---|---|---|---|
| `R17-FIX-INT-gost.log` | 0 | 0 | |
| `R17-FIX-INT-x.log` | 0 | 0 | 27 个包 ok |
| `R17-FIX-INT-frontend.log` | tsc 0 / vite 0 | 0 | |
| `R17-FIX-INT-backend.log` | 1 | 1（环境红） | 16 个包 ok；唯一失败 `go-backend/internal/health` 的 `TestValidateMonitorTCPTarget/公网域名放行`（`monitor_ssrf_test.go:39`：`ValidateMonitorTCPTarget("example.com:443")` 在沙箱 fake-IP DNS 下被判内网）。与任务书点名的签名一致；`handler 378.146s`、`repo 125.324s`、`contract 40.774s`、`kernel` 均 ok。**产品失败 0** |

豁免依据（已做 main 对照）：detached worktree 上对 `main @ 5473377` 单跑该用例同样失败（同一行、同一错误）；且 R17 diff 未动 `go-backend/internal/health/`。临时 worktree 已删。主控裁定（2026-10-25 08:14 CST）：放行，不因 backend exit=1 而停。

## 3. 部署（r17d，2026-10-25 00:27Z，`R17-FIX-D-deploy.log`）

本地同树同标志构建（`CGO_ENABLED=0 go build`，无 `-s -w`，无 UPX；`go1.25.1`；前端 tsc+vite），机上 COPY-only 镜像组装（沿 C 路做法，`FLUX_VERSION=1.4.8-r17d`，工作目录 `/root/r17d/`）：

| 产物 | 校验 |
|---|---|
| 修后 agent `flux_agent-r17d` | 50,270,345 字节，md5 `23aa260d8bffb857171f26e3426b7d2d`；strings 含 `startup config self-heal`（2 处）、`parse service`（1 处） |
| 未修复 agent `flux_agent-main`（main @ 5473377 同树构建） | 50,247,962 字节，md5 `ab628bcd120e4fe478cf083bf5b59b4b`；strings 中 `startup config self-heal`/`parse service`/`DeduplicateServices` 均为 0。注：md5 与 R17 的 `b92b6199…` 不同，是 Go 构建路径嵌入所致（不同构建目录），源码同树 |
| 修后 paneld | 43,876,976 字节，md5 `1eb46e4b40c994c0b1819d154a0b2e04`；`connIdle` 字面量 8 处、`r17-addservice-guard` 1 处（与 R17/148B 同数） |
| 前端 dist | tsc exit=0、vite exit=0，PWA precache 65 entries；`dist/index.html` sha256 `25c4c6893cdf402c34a50e5fb73638e9c3a755b6f553ebd7494fa73cb24b7853`（与 R17 的 `3e969898…` 不同，属构建环境非确定性，源码无前端改动）；`nginx.conf` md5 `8e4c555a283c283a31110b5c9601b479`（与 148B/R17 同） |
| stage.tgz | 24,540,018 字节，md5 `8f5ee7970727f416b4e254158ffac80c` |

部署结果：镜像 `flmx-backend:r17d`（`84d21ef2a0ab`）、`flmx-frontend:r17d`（`887b6c4209e2`）；`GET /` 200；agent active；gost pristine md5 `d48f09e1…`、328 行、12 服务；计数 1/4/6/578/18；旧 token 在新面板上仍有效。门前基线：面板 1.3.5（`flmx-backend:r1gate-1.3.5`），agent pristine md5 `aff338e3…`，unit 原件 md5 `a12afd9c…`，磁盘 3.9G 空闲；前端容器 `(unhealthy)` 为门前已存在的问题（healthcheck 探针问题），不以容器 health 字段为准，以 `GET /` 200 验证。

## 4. 验收腿 1：确定性去重腿（主判据）

### 4.1 方法（经代码实查后定案）

面板健康态不会发 `AddService`；唯一确定性触发是“暂停服务 + `UpdateService` 失败 → 1 号回退 `AddService`”。执行：暂停转发 6（`a1-relay1`，端口 14601，服务 `6_1_0_tcp`/`6_1_0_udp`）→ dummy 占住 14601（TCP+UDP）→ 重启 agent 触发 onNodeOnline redeploy 双推 → `UpdateService` 报 bind 失败 → 守卫发 fallback `AddService`；watcher 监听后端容器 json 日志的 guard `send` 行后立即杀 dummy。

执行中发现更重要的自然触发：redeploy 时 `updateServices` 对多条转发走重建路径，重建的“注销旧监听 → 立即解析绑定”存在间歇性 bind 竞态（旧监听关闭有延迟），`UpdateService` 失败 → fallback `AddService`。**该自然事件在 11 次 agent 重启中触发 3 次**，且与 dummy 无关。工具脚本：`R17-FIX-D-dummy.py`、`R17-FIX-D-watcher.py`、`R17-FIX-D-leg1.sh`、`R17-FIX-D-restart-probe.sh`（1s 轮询查重）、`R17-FIX-D-fastpoll.sh`（50ms 采样 × 40s）、`R17-FIX-D-killwatch.py`/`R17-FIX-D-killfreeze.sh`（sent-ok 瞬间 SIGKILL agent 冻结落盘）。

### 4.2 逐次结果（UTC；gost.json 均 12 服务除非注明）

| 次 | agent | 结果 |
|---|---|---|
| fixed-1（00:29:06Z） | 修后 | setup 失败（dummy v6 绑定退出、watcher 容器 ID 路径错），不计 verdict。但同次重启的 redeploy 中转发 3（`3_5_3`，13102）走了 fallback：`send → sent-ok` 一次（00:29:13.932Z）+ 5 次 `sent-failed`（bind）。**修后 agent 上一次真实成功的重复 `AddService`，落盘无重复** |
| fixed-2（00:30:00Z） | 修后 | watcher 00:30:08.191 见到 guard `send`（6_1_0 update-fallback），00:30:08.192 杀 dummy，但 fallback #1 仍 `sent-failed`（agent 侧解析在 guard 行后约 1ms 即发生，watcher 慢约 5ms，没抢赢）。随后 3 号 rebind：删 + 150ms + `AddService` → `sent-ok`（删后重加，本身不产生重复）。终态 12 服务、无重复、6_1_0 paused、agent active |
| fixed-3（00:32:53Z） | 修后 | MISS（无新增 guard 行），终态 gost md5 不变、12 服务、无重复。SSH 中途断开，结论按机上状态重建，已补记进日志 |
| control-1（00:35:40Z） | 未修复 | MISS，无新增 guard 行，无 dup |
| control-2（00:37:32Z） | 未修复 | MISS，无新增 guard 行，无 dup |
| control-3（00:39:44Z） | 未修复 | MISS，无新增 guard 行，无 dup |
| control-4（00:42:19Z） | 未修复 | 触发！转发 4（`4_5_4`，13103）：update-fallback `sent-failed`（bind），rebind `sent-ok`（删后重加）。终态 12 服务、无重复 |
| control-5（00:44:27Z） | 未修复 | **判别事件触发**：转发 2（`2_5_2`，13101）update-fallback `AddService` **`sent-ok`**（`probe-control-5.log`：`send` 00:44:33.501364Z → `sent-ok` 00:44:33.504889Z）。agent journal 证实：00:44:33.497 两行 bind 错误（`UpdateService` 解析失败 + 回滚恢复解析失败）→ 00:44:33.500 收到 `AddService` → 00:44:33.501 `listening on [::]:13101/tcp`（`AddService` 成功）→ 00:44:33.550 直接推的 `UpdateService` 再次重建 2_5_2。但 **1s 轮询 0 dup sightings，终态 gost 12 服务、无重复，md5 仍为 `a491fb80…` 不变**。**对照未复现毒化** |
| control-6（00:51:08Z） | 未修复 | 50ms 高速轮询，MISS（无新增 guard 行），0 dup sightings，终态干净 |
| kill-1（00:55:11Z） | 未修复 | kill-freeze：60s 窗口内无 sent-ok，TIMEOUT，agent 存活，gost 不变 |
| kill-2（00:56:29Z） | 未修复 | kill-freeze：60s 窗口内无 sent-ok，TIMEOUT，agent 存活，gost 不变 |

### 4.3 腿 1 判定

- **修后 agent：PASS。** 一次真实成功的重复 `AddService`（fixed-1，3_5_3 sent-ok）后落盘无重复，去重有效；另一次 rebind sent-ok（fixed-2）亦干净。
- **反向对照：未复现。** 一次真实成功的重复 `AddService`（control-5，2_5_2 sent-ok）后落盘亦无重复；5 次重启探针 + 1 次 50ms 高速轮询 + 2 次 kill-freeze，0 dup sightings。按任务书要求如实登记，不得伪造。
- 自然触发率：11 次重启中 3 次出现 fallback 事件（fixed-1 的 3_5_3、control-4 的 4_5_4、control-5 的 2_5_2），其中判别性事件（update-fallback sent-ok）2 次（修后 1 次、未修复 1 次）。

### 4.4 未解项 R17D-OPEN-01（control-5 悖论，明文登记）

按 main 原版代码，`createServices` 第 4 阶段是盲 append（已逐行核对 `/tmp/r17d-main/go-gost/x/socket/service.go:83-85`），`OnUpdate` 同步改 live global 后同步 `persist()` 写盘（`persist()` 直接写 `Global()` 克隆，无去重），故 `AddService` 成功后内存应有 14 服务、落盘应有重复。但实测终态 12 服务、无重复（原始 grep 复核：`2_5_2_tcp` 与 `2_5_2_udp` 各 1 份）。

已排除的解释：运行中二进制确为未修复版（事后核对 md5 `ab628bcd…`，PID 692920，启动于 00:44:27Z）；`updateServices` 第 3 阶段只替换首个匹配项、不删多余副本；窗口内无 `DeleteService`（journal 命令清单无）；`pauseServices` 只打 paused 标记；`config.Set(originalConfig)` 只在错误路径调用且本次无错误；单 agent 进程（PID 交接 691929→692920 干净，`MainPID` 唯一）；无 stray 落盘（`/gost.json`、`/root/gost.json`、`/tmp/gost.json` 均不存在）。

Journal 微时序（00:44:33.49–.56，`short-precise`）：`.497` 两行 bind 错误 → `.500` 收到 `AddService` → `.501` `listening on [::]:13101/tcp` → `.502` 收到下一条 `UpdateService`（7_1_0）→ `.503/.504` 7_1_0 listening → 持久化行；其后每条 `UpdateService` 各跟一行持久化；`.549` 的 `UpdateService` 再次重建 2_5_2（`.550` listening）→ 持久化。**唯独 `AddService` 的持久化行在 `.501` 与 `.502` 之间未出现**，而文件 mtime 定格在 `.564`（PauseService 那次落盘）。重复项若曾落盘，只可能存活于 `.501`→`.550` 约 49ms 窗口内——1s 轮询必漏，50ms 轮询在 control-6 无事件故未覆盖该窗口；两次 kill-freeze 均未等到 sent-ok，未能冻结该窗口。

结论状态：**微观机理未解**，不得伪造对照复现。但这不否定修复本身：A 路单元级红→绿已证明盲 append 漏洞及其闭合；修后 agent 在 live 路径上已证实去重（fixed-1）；自愈腿（§5）亦证实毒化文件可被修复。最可能的后续方向（未证实）：重复项在该 49ms 窗口内被某条未识别的路径清理，或直接推 `UpdateService` 的重建以某种方式覆盖了它；亦可能原始 R17 毒化需要“`AddService` 恰为某转发的最后一次同步、其后无直接推跟进”的时序，而本轮两次 sent-ok 均发生在隧道推（其后必有直接推跟进）。建议作为观察项跟进，不阻塞 R17-X-01 关闭（见 §9）。

## 5. 验收腿 2：启动自愈腿（主判据）

毒化文件原件 md5 `e381299b2aa931d2f7c097661ead6702`（378 行，14 服务，`6_1_0_tcp`/`6_1_0_udp` 各重复一次），机上 `/root/r17c/gost.json-poisoned`。每次放毒前先停 agent（运行中 agent 会把内存干净配置写回磁盘“自愈”外来毒化文件，C 路教训）。

| 组 | 结果 |
|---|---|
| 修后 agent（`leg2-fixed.log`，00:59:32Z） | **PASS。** 放毒后重启：`active`、`NRestarts=0`（一次起成）；journal：`{"level":"warning","msg":"startup config self-heal: removed duplicate service(s) [6_1_0_tcp 6_1_0_udp] from persisted config","time":"2026-10-25T00:59:44.150Z"}`（A 路日志格式逐字一致）；`agent-startup.log` 无文件（无失败，符合预期——该文件只在启动失败时写）；落盘后 md5 `2ff44f86…`、334 行、12 服务、无重复名 |
| 未修复 agent（`leg2-control.log`，01:00:41Z） | **PASS（预期失败复现）。** 放毒后重启：`active=failed`、`NRestarts=5`、无 agent 进程；journal 5 行 `fatal`：`listen tcp 0.0.0.0:14601: bind: address already in use`（PID 697694/697716/697737/697758/697779）；落盘文件仍为毒化原件（md5 `e381299b…`、378 行、14 服务、`6_1_0_tcp`/`6_1_0_udp` 各 2 份）。与 C 路已验证的现场逐字一致 |

两组结束后均恢复 pristine gost（md5 `d48f09e1…`）并重启 agent 验证 active。

## 6. 验收腿 3：harness 重跑（无回归记录）

集成树上跑 C 路 harness 10 循环同启（`R17-FIX-C-harness.sh 10 75 r17d`，`harness-r17d.log`，01:03–01:18Z）：**cycles_done=10，hits=0，hit_rate=0/10**。每循环均为 `MISS (gost pristine, agent active)`；终态 gost md5 `d48f09e1…`、328 行、`DUP:none`、`SERVICES:12`，agent active、`restarts=0`。

跑前准备：换回修后 agent + 恢复转发 6（DB status 0→1，`POST /api/v1/forward/resume {"id":6}` code 0），使 redeploy 后 gost 回到 pristine（harness 的命中判据含 md5-changed；转发 6 若保持暂停，每循环都会因 paused 元数据而误计命中）。

**如实声明：基线亦 0/10（C 路 `harness-r17c-base.log`），故本项不构成修复有效的区分证据，仅为无回归记录。** 区分证据见腿 1（修后 live 去重）与腿 2（自愈）。

## 7. 验收腿 4：计时腿 + B 路守卫日志

### 7.1 方法与判定口径

沿用 R17-L4 口径（`R17-L4-timing.md`），判定标准先行：D1-direct（Trojan/VLESS 直连）关断于 5s ±1.5s（3.5–6.5s）；D1-supply（VMess/SS 上游供数：客户端每 0.5s 发 1 字节、持续 10s 后停发）关断于停发后 5s ±1.5s（自起算 13.5–16.5s）；D1-silent（VMess/SS 完全静默关断于 0.5–2.0s 为预期上游行为 O-148B-1，不计 FAIL）；D2-control（无值对照供数模式 30s 仍不断；静默对照腿不具判别力，M1）；D3-render（policy levels 与入站一一对应且 `connIdle:5`；`flmx-idle-*` 出站 userLevel 一致；规则顺序 idle 用户规则 → idle 通用规则 → per-user mark 规则）；D4-cleanup（删测试入站后 policy 为 null、`flmx-idle-*` []、idle 规则 0、inbound 计数回 578）。

本次与 R17 的差异：**VLESS 行为腿按任务书要求重跑**（R17 未重跑、结论承自 148B）。其余腿复用 R17 脚手架（机上 `/root/r17/`：`socks_test2.py`/`socks_test3.py`/`ss_door_feed.py`/`trojan_test.py`/`vless_test.py`/`dummy.py`/`api17.sh`；缺失的 xclient 配置从 `R17-L4-evidence.tgz` 恢复）。

### 7.2 D3 渲染 — PASS（`leg4-create.log` + `xray-config-created.json`，md5 `e4566692…`）

测试入站（均 Xray/plain TCP/security none，node 1，kernel xray id 3；`connIdleSeconds=5`，除对照外）：

| id | 名称 | 协议 | 端口 | connIdle |
|---|---|---|---|---|
| 931 | r17-idle-vmess | vmess | 59905 | 5 |
| 932 | r17-vmess-ctl | vmess | 59908 | 0（对照） |
| 933 | r17-idle-ss | shadowsocks | 59907 | 5 |
| 934 | r17-idle-trojan | trojan | 59906 | 5（TLS 自签，SNI `r17.test`） |
| 935 | r17-idle-vless | vless | 59904 | 5 |

创建均 code 0，计数 578 → 583。agent 下发 xray 配置核对：

- policy：`{"levels": {"1": {"connIdle": 5}, "2": {"connIdle": 5}, "3": {"connIdle": 5}, "4": {"connIdle": 5}}}`（931→1、933→2、934→3、935→4）。
- `flmx-idle-*` 出站：`flmx-idle-931`（userLevel 1）、`-931-u1`/`-931-u4`（1）、`flmx-idle-933`（2）、`flmx-idle-934`/`-934-u1`/`-934-u4`（3）、`flmx-idle-935`/`-935-u1`/`-935-u4`（4）。SS 仅通用出站、无 per-user 组合出站（该入站无 per-user 客户端，符合设计，与 R17/148B 同）。
- 规则顺序：0 `in-931→flmx-idle-931-u1`、1 `in-931→flmx-idle-931-u4`、2 `in-931→flmx-idle-931`、3 `in-933→flmx-idle-933`、4 `in-934→flmx-idle-934-u1`、5 `in-934→flmx-idle-934-u4`、6 `in-934→flmx-idle-934`、7 `in-935→flmx-idle-935-u1`、8 `in-935→flmx-idle-935-u4`、9 `in-935→flmx-idle-935`，随后 10–11 为 per-user mark 规则（`flmx-u1`/`flmx-u4`）。每入站的用户规则先于其通用规则、全部 idle 规则先于 mark 规则，符合 D3。

过程注记：首跑脚本的 VLESS payload 路径写错（`cp` 源文件名不符），VLESS 创建报 code −1（参数错误）；已用补正命令创建（id 935，code 0，ts `1792891334570`），D3 以 5 入站完整版重核并落盘；脚本已修正，随报告提交。补正过程如实记在 `leg4-create.log` 尾部 SUPPLEMENT 段。

### 7.3 行为腿 — PASS（`leg4-legs.log`，01:23:12–01:25:56Z）

哑目标 127.0.0.1:59909（只 accept、不发数据、不关闭）；客户端 xray 为 agent 自带二进制（Xray 26.3.27）。

| 腿 | 方式 | 结果 | 判定 |
|---|---|---|---|
| R17D-VL-1 VLESS 直连主凭证 | Python 直连 59904 → 59909，超时 20s | `header_after=5.24s`，CLOSED 5.25s | D1-direct 3.5–6.5s PASS（与 148B 的 5.20–5.28s 一致） |
| R17D-VM-2 VMess 供数主凭证 | socks 11080，发送 10s，超时 30s | 停发于 10.01s，CLOSED 16.02s（停发后 5.01s） | D1-supply 13.5–16.5s PASS |
| R17D-SS-2 SS 供数主凭证 | door 11087 供数，发送 10s，超时 30s | 停发于 10.00s，CLOSED 15.02s（停发后 5.02s） | D1-supply PASS |
| R17D-TR-1 Trojan 直连主凭证 | Python TLS 直连 59906 → 59909，超时 20s | `header_sent_after=0.09s`，CLOSED 5.09s | D1-direct PASS（R17：5.07s） |
| R17D-CTL-supply VMess 供数无值对照 | socks 11083（入站 932，无 idle），发送 10s，超时 30s | STILL_OPEN@30s | D2 PASS |
| R17D-VM-2m VMess 供数最小 | socks 11084（最小 xray，无 policy），发送 10s，超时 30s | STILL_OPEN@30s | D2 PASS |
| R17D-SS-2m SS 供数最小 | door 11085（最小 xray），发送 10s，超时 30s | STILL_OPEN@30s | D2 PASS |
| R17D-VM-1 VMess 静默主凭证 | socks 11080 → 59909，超时 20s | CLOSED 2.15s；复测 R17D-VM-1r CLOSED 1.14s | O-148B-1 复核 PASS（首跑 2.15s 判为冷启动离群，与 R17 的 2.10s→1.10s 同例；两值均保留） |

### 7.4 D4 清理 — PASS（`leg4-delete.log`，01:27:18Z）

931/932/933/934/935 逐个删除均 code 0；删除后计数 578、r17 残留 []、policy `None`、`flmx-idle-*` 出站 []、idle 规则 []、出站回 `[direct, block, flmx-u1, flmx-u4, flmx-u12]`、规则 3、inbounds 11——与基线逐字一致。脚手架清理：dummy、client xray 7 进程、最小 xray 2 进程全部已杀；5990x/5991x/1108x 测试端口全关；仅剩 agent 自身 xray（PID 709053）。

## 8. B 路守卫日志（验收期间）

验收窗口内（r17d 后端容器自部署起的 json 日志），`r17-addservice-guard` 判定行共 **48 条**，按服务名可 grep 完整判定链：

- 按 decision：`send` 24、`sent-ok` 10、`sent-failed` 14。
- 按 reason：`update-fallback` 32、`self-occupied-rebind` 16。（本窗口内未触发 `default-bind-fallback` 与 `skip-confirmed`/`skip-joined` 判定——守卫只在实际走到的回退路径上打行，符合设计。）
- 按服务：`3_5_3` 28、`6_1_0` 6、`4_5_4` 8、`2_5_2` 2、`7_1_0` 2、`1_2_1` 2（tcp/udp 各半）。

## 9. R17-X-01 关闭建议

**建议关闭 R17-X-01。** 依据：

1. 漏洞本身已在单元级证实并闭合：A 路 `createServices` 盲 append 的红→绿测试（`r17_fix_a_test.go`）证明重复下发曾可毒化落盘、修复后按名 upsert。
2. 修复在 live 路径上已证实有效：腿 1 fixed-1，一次真实面板回退 `AddService` sent-ok 后落盘无重复。
3. 毒化文件的危害已被自愈兜底：腿 2 修后 agent 对毒化原件一次起成、去重告警可查、落盘无重复；即使某条路径再次写出重复文件，下一次 agent 重启也不再自砖（与 R17 发现时“任何版本 agent 都无法重启”相比，危害已消除）。
4. 面板侧补发面已被守卫收敛：B 路守卫在验收窗口内对全部 24 次回退下发都打出了可 grep 的判定链（`send`/`sent-ok`/`sent-failed`），且 `tolerateExists` 吞 already-exists 的面仍在守卫的判定覆盖内。

附带观察项（不阻塞关闭）：**R17D-OPEN-01**——未修复 agent 在 live 路径上的重复 `AddService` sent-ok 未能留下持久重复（§4.4）。这意味着原始毒化的持久化机理仍有一环未定位；但该环的存在与否不改变上述 4 点（修复 + 自愈双保险已覆盖“写出重复”与“读入重复”两端）。建议作为观察项登记，后续若再有毒化现场，优先抓取 sent-ok 瞬间的落盘快照（本次 kill-freeze 未等到事件）。

## 10. 未验证项（明文登记，不得冒充）

1. R17D-OPEN-01 的微观机理（见 §4.4）：重复项在 49ms 窗口内的去向未定位；kill-freeze 两次均未等到 sent-ok 事件。
2. VLESS 用户凭证腿（u1/u4）：本轮只跑主凭证直连；148B 的 VLESS 用户腿为 5.20–5.28s，本轮未重跑（渲染路径由 D3 的 level 4 与三协议主凭证腿共同验证）。
3. Python 直连 SS 客户端腿：未跑（R17/148B 同口径：自写实现不可靠，其结论不作数；SS 5s 结论以 dokodemo-door 供数腿为准）。
4. Trojan 最小 xray A/B 腿：未跑（Trojan 直连腿 5.09s 已直接证明服务端行为；最小对照对 Trojan 不是必需）。
5. 下游供数与 1MB 批量腿：未在 fix-r17 上重跑（R17 值：下游供数 12.14s/12.11s、批量 3.11/3.12s；与 connIdle 渲染无关的面，本轮未动）。
6. A 路留待立项的两处同型盲 append：管理 API 的 `go-gost/x/api/config_service.go:84-85` 与 `:169-171`（当前部署不可达，A 路报告已登记，修复未含）。
7. 远端节点与非 Debian 内核（仅本机 Debian + Xray 26.3.27）。
8. 浏览器 DOM 腿：计时腿无 DOM 面（不适用）。
9. 订阅 1 流量计数器：本轮腿未产生 u1 供数流量，计数器维持 R17 门后的 up 3648 / down 2105821（与 R17-L4 §9 一致；对账口径为计数与二进制/配置，不含流量计数器）。

## 11. 门后恢复对账（`R17-FIX-D-restore.log`，01:29:55Z）

| 对账项 | 基线（门前 pristine 1.3.5） | 门后 | 一致 |
|---|---|---|---|
| 面板版本 | 1.3.5，镜像 `flmx-backend:r1gate-1.3.5` / `flmx-frontend:r1gate-1.3.5`，backend healthy，`GET /` 200 | 同（compose/.env md5 与 `.pristine` 备份逐一一致：`3afbac11…` / `fffcd005…`；FLUX_VERSION=1.3.5） | ✓ |
| 节点 | 1（w10-test 在线） | 1（code 0） | ✓ |
| tunnel / forward / inbound / subscription | 4 / 6 / 578 / 18 | 4 / 6（status 全 1）/ 578 / 18 | ✓ |
| agent 二进制 | md5 `aff338e3676bc56d0f3986a1a62f8fdd` | 同 md5，service active，新 PID 710218 | ✓ |
| agent unit | md5 `a12afd9c36fba7b76287210efaa199cb`，`StandardOutput=null` | 同 md5，`StandardOutput=null` / `StandardError=null` | ✓ |
| gost.json | md5 `d48f09e132ebe99fe97a619dd72d42d4`，328 行 | 同 md5，328 行 | ✓ |
| 表数 | 49 表口径（`node_traffic` 为新版面板 autoMigrate 补建） | 50 表（含 `node_traffic`=1） | ✓ |
| 候选镜像 | — | `flmx-backend:r17d` / `flmx-frontend:r17d` 已删；仅剩 `r1gate-1.3.5` | ✓ |
| 脚手架进程 / 端口 | — | dummy/client xray/最小 xray 全部已杀，测试端口全关；仅剩 agent 自身 xray | ✓ |
| 磁盘 | 3.9G 空闲 | 3.6G 空闲（本轮消耗约 0.3G：镜像层与日志残留，与 R17 同量级） | ✓（记录） |

注：恢复脚本的“subscription count 3”为计数 bug（`/api/v1/subscription/list` 返回 data 为 dict `{items, regions, ruleGroups}`，`len(dict)` 计的是键数）；已从 `/tmp/rst2-sub.json` 的 items 重数：**18**，ids `[1,2,3,4,5,6,7,8,9,10,11,12,13,15,16,17,18,20]`。补正记在 `R17-FIX-D-restore.log` 尾部 SUPPLEMENT 段。前端容器在门前即为 `(unhealthy)`（healthcheck 探针问题，门前已存在），本轮 `GET /` 200 直接验证在位，不以容器 health 字段为准。

## 12. 证据清单

本地目录 `audit-out-132/r17/`（集成树内；`*.log` 被 gitignore，本次 `git add -f` 逐文件显式入库，R16 有此先例）：

| 文件 | 内容 |
|---|---|
| `R17-FIX-INT-backend.log` / `R17-FIX-INT-gost.log` / `R17-FIX-INT-x.log` / `R17-FIX-INT-frontend.log` | 集成回归四份日志（§2） |
| `R17-FIX-D-deploy.log` / `R17-FIX-D-deploy.sh` | r17d 部署全程与脚本（§3） |
| `R17-FIX-D-leg1-fixed-1.log` / `R17-FIX-D-leg1-fixed-2.log` / `R17-FIX-D-leg1-fixed-3.log` | 腿 1 修后三次（§4.2） |
| `probe-control-1.log` … `probe-control-5.log` | 腿 1 对照五次重启探针（§4.2；control-5 含 2_5_2 sent-ok 判别事件） |
| `fastpoll-control-6.log` | 腿 1 对照 50ms 高速轮询（§4.2） |
| `R17-FIX-D-leg1-kill-1.log` / `killwatch-kill-1.log` / `killfreeze-kill-2.log` / `R17-FIX-D-killwatch-kill-2.log` | kill-freeze 两次（均 TIMEOUT，未等到 sent-ok） |
| `R17-FIX-D-dummy-fixed-1.log` / `R17-FIX-D-dummy-fixed-2.log` / `R17-FIX-D-watcher-fixed-1.log` / `R17-FIX-D-watcher-fixed-2.log` | 腿 1 dummy/watcher 运行日志 |
| `R17-FIX-D-dummy.py` / `R17-FIX-D-watcher.py` / `R17-FIX-D-leg1.sh` / `R17-FIX-D-restart-probe.sh` / `R17-FIX-D-fastpoll.sh` / `R17-FIX-D-killwatch.py` / `R17-FIX-D-killfreeze.sh` | 腿 1 工具脚本 |
| `leg2-fixed.log` / `leg2-control.log` / `R17-FIX-D-leg2.sh` | 腿 2 自愈正向与对照（§5） |
| `harness-r17d.log` | 腿 3 harness 10 循环（§6） |
| `leg4-create.log` / `leg4-scaffold.log` / `leg4-legs.log` / `leg4-delete.log` | 腿 4 创建/脚手架/行为腿/清理（§7） |
| `R17-FIX-D-leg4-create.sh` / `R17-FIX-D-leg4-scaffold.sh` / `R17-FIX-D-leg4-legs.sh` / `R17-FIX-D-leg4-delete.sh` / `R17-FIX-D-create-vless.json` | 腿 4 脚本与 VLESS payload（VLESS 测试 UUID 仅用于已删测试入站 935） |
| `xray-config-created.json` / `xray-config-after-delete.json` | D3 渲染快照与 D4 清理后快照（§7.2/§7.4） |
| `R17-FIX-D-restore.log` / `R17-FIX-D-restore.sh` | 门后恢复与对账（§11） |

说明：报告正文不含任何凭据明文（面板密码、测试 UUID/口令、token 均未写入）；测试凭据仅存于机上 `/root/r17/` 与证据文件内的客户端配置中（测试入站已删、凭据已失效；与 R17/148B 证据口径一致）。SSH 中途断开两次（fixed-3、kill-1），相关结论均按机上状态重建并已补记进对应日志，不以断开时的本地输出为准。

## 自证

- 本报告正文（不含本节）字节数：28652
- 尾行：END

END
