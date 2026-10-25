# R18 未验证清单复核（L5）— main @ a0c18a0

- **审计对象**：`main @ a0c18a0`（完整提交 `a0c18a0dc90b2f9c7a67e8a2a016cc1d97505fb5`）
- **依据**：`R17-FIX-INT-REPORT.md` §10（9 项未验证清单）。**来源订正**：任务书所指路径 `audit-out-132/r17/R17-FIX-INT-REPORT.md` 在本机工作树（`feat-latency-hint @ a99d68d`）上**不存在**；该文件只存在于 `main @ a0c18a0` 的提交树内，本轮以 `git show a0c18a0:audit-out-132/r17/R17-FIX-INT-REPORT.md` 直读（239 行，28,736 字节），§10 原文逐项抄录于下表。A/B/C 分报告（同提交树内 `R17-FIX-A/B/C-REPORT.md`）仅作交叉引用，未发现与 INT-REPORT §10 相抵触之处。
- **基线核对**：`git merge-base --is-ancestor 5125ff5 a0c18a0` 为真；`5125ff5..a0c18a0` 仅含 `a0c18a0` 一个 docs 提交（集成报告 + 验收证据），产品代码与 fix-r17 树逐字一致。`5473377..a0c18a0` 产品 diff 共 15 文件：`add_service_guard.go`（新增）+ `add_service_guard_test.go`（新增）、`control_plane.go`、`handler.go`、`go-gost/main.go`、`program.go`、`x/config/dedupe.go`（新增）+ `dedupe_test.go`（新增）、`x/config/loader/duplicate_service_test.go`（新增）+ `loader.go`、`persist.go`、`x/config/testdata/r17-poisoned-gost.json`（新增）、`x/socket/r17_fix_a_test.go`（新增）、`service.go`、`install.sh`（仅 C 路 unit 日志行，见 §7）。
- **复核日期**：2026-10-25（测试日志时戳 UTC 2026-10-25 01:36–01:49，即 CST 09:36–09:49）
- **性质**：只读审计，不改产品代码、不打 tag、不发版。隔离探针在 `~/workspace/r18-l5-scratch/` 内跑（`gost/` 为 a0c18a0 `go-gost` 复制件 + 临时探针测试文件，永不入库）；测试机操作只做只读查询与 agent 重启探针（Phase A），未部署候选件（原因见 R18-L5-CAND-01）。
- **护栏**：N-01…N-37 全程有效；裁定只认落盘原始日志 + 名单存档（N-37）。
- **分工声明**：第 6 项管理 API 两处按任务书分工——L2 定可达性，本路定行为腿；本路不重复立案，交叉引用见 §6。写稿时 `audit-out-132/r18/` 内未见 R18-L2 报告落盘（只有 L1/L3/L4 他路文件），L2 结论待协调者补引。

## 结论总表（§10 原文逐项抄录后逐项复核，不得漏项）

| # | §10 原文（逐字） | R18-L5 结论 | 证据 |
|---|---|---|---|
| 1 | R17D-OPEN-01 的微观机理（见 §4.4）：重复项在 49ms 窗口内的去向未定位；kill-freeze 两次均未等到 sent-ok 事件 | **部分进展，仍未验证关闭** | §1：瞬态重复首次直接捕获（A1，3 采样、存活约 160ms、文件与 R17 毒化件逐字节一致）；sent-ok 瞬间冻结仍未达成；归因受并行 lane 干扰（见 CAND-01） |
| 2 | VLESS 用户凭证腿（u1/u4）：本轮只跑主凭证直连；148B 的 VLESS 用户腿为 5.20–5.28s，本轮未重跑（渲染路径由 D3 的 level 4 与三协议主凭证腿共同验证） | **仍未验证** | §2：未跑。候选件未部署（CAND-01 串行阻塞）；R17/148B 值不得冒充本基线值 |
| 3 | Python 直连 SS 客户端腿：未跑（R17/148B 同口径：自写实现不可靠，其结论不作数；SS 5s 结论以 dokodemo-door 供数腿为准） | **仍未验证** | §3：未跑（同上）。SS 5s 结论仍以供数腿为准的口径不变 |
| 4 | Trojan 最小 xray A/B 腿：未跑（Trojan 直连腿 5.09s 已直接证明服务端行为；最小对照对 Trojan 不是必需） | **仍未验证** | §4：未跑（同上） |
| 5 | 下游供数与 1MB 批量腿：未在 fix-r17 上重跑（R17 值：下游供数 12.14s/12.11s、批量 3.11/3.12s；与 connIdle 渲染无关的面，本轮未动） | **仍未验证** | §5：未跑（同上）。R17 值仍为上一轮值；O-R17-1 批量大小依赖亦未探 |
| 6 | A 路留待立项的两处同型盲 append：管理 API 的 `go-gost/x/api/config_service.go:84-85` 与 `:169-171`（当前部署不可达，A 路报告已登记，修复未含） | **行为腿已验证 PASS**（2/2 探针）；可达性待 L2；本路不立案 | §6：隔离 handler 探针 2/2 PASS（200 + 落盘 2 份同名）；分歧前提已在产品代码中定位；部署态三无支撑“当前部署不可达” |
| 7 | 远端节点与非 Debian 内核（仅本机 Debian + Xray 26.3.27） | **仍未验证** | §7：`5473377..a0c18a0` 零多节点/多发行版手段新增；沙箱无 Docker；单机 Debian |
| 8 | 浏览器 DOM 腿：计时腿无 DOM 面（不适用） | **仍未验证** | §8：本路无浏览器任务能力；vite-frontend 零改动，R17-L5 的 5473377 served-chunk 基线仍有效，无需重定 |
| 9 | 订阅 1 流量计数器：本轮腿未产生 u1 供数流量，计数器维持 R17 门后的 up 3648 / down 2105821（与 R17-L4 §9 一致；对账口径为计数与二进制/配置，不含流量计数器） | **仍未验证** | §9：计数器基线两次读数一致（up 3648 / down 2105821）；u1 流量未产生，响应性未验证 |

## 0. 门前基线（测试机，2026-10-25 01:36–01:38Z，只读）

面板 1.3.5（`flmx-backend:r1gate-1.3.5` / `flmx-frontend:r1gate-1.3.5`，backend healthy，前端 `(unhealthy)` 为门前已存在）；agent `aff338e3676bc56d0f3986a1a62f8fdd`（main @ 5473377 未修复版）、active、MainPID 710218、NRestarts=0；`gost.json` md5 `d48f09e132ebe99fe97a619dd72d42d4`、328 行、12 服务；计数 node 1 / tunnel 4 / forward 6 / inbound 578 / subscription 18（POST list，GET 误用会得 code −1“请求失败”，因 list 接口为 POST-only——本轮初查曾踩此坑，已订正）；订阅 1 计数器 up 3648 / down 2105821（`POST /api/v1/subscription/traffic {"id":1}`）；磁盘 3.6G 空闲。token 由面板凭据现取（code 0，248 字节，存机上 `/root/r17/token` 600；凭据本身绝不入报告）。

## 1. R17D-OPEN-01 微观机理 — 部分进展，仍未验证关闭

### 1.1 Phase A：50ms 快采重启探针（未修复 agent，6 循环）

方法：`R18-L5-fastpoll-r18.sh`（INT-REPORT `R17-FIX-D-fastpoll.sh` 的改编版：工作区 `/root/r18/`；1.3.5 面板无 guard 日志行，改读 agent journal）——重启 agent 后以 50ms 采样 `gost.json` 45s，凡同名重复即存全文件。6 循环（A1–A6，01:40:04–01:45:21Z），日志 `R18-L5-phaseA-fastpoll.log`（95,374 字节）。

| 循环 | 起始 agent md5 | 结果 |
|---|---|---|
| A1（01:40:04） | `aff338e3…`（未修复 main@5473377） | **瞬态重复捕获**：pre 12 服务无重复；01:40:38.043（重启后约 30s）采到 14 服务、`6_1_0_tcp`/`6_1_0_udp` 各 2 份，连续 3 采样（38.043/38.099/38.152）后 38.202 回到 12 服务无重复——**存活约 160ms**；终态 md5 `d48f09e1…` pristine |
| A2–A5 | `23aa260d…`（修后 r17d agent） | 0 sightings。注：二进制非本路更换（见 §1.3），此 4 循环测的是修后 agent，不得计入未修复对照 |
| A6（01:44:33） | `ab628bcd…`（未修复 main@5473377，另一构建目录） | pre 即为毒化态 14 服务、`6_1_0` 各 2 份（A5 终态干净后 6s 内写成，见 §1.3）；45s 窗口内 445 采样全毒化；终态 12 服务无重复、agent active。终态清理非本路所致（见 §1.3） |

### 1.2 A1 捕获物的同一性（硬证据）

- `/root/r18/fastdup-A1-1.json` md5 **`e381299b2aa931d2f7c097661ead6702`**、378 行——与 R17 毒化原件（`R17-L4-evidence/gost.json-candidate-written.json`，INT-REPORT §5 同 md5）**逐字节一致**；`fastdup-A6-1.json` 同 md5、两文件 `python3` 比对 identical。本地存档：`R18-L5-fastdup-A1-1.json`（7,220 字节，md5 同上）。
- 判定：A1 采到的不是采样撕裂（撕裂读会 JSON 失败，不会得到格式完好、恰为 `6_1_0` 对逐字加倍的 378 行文件），而是一次真实的 R17-X-01 形态瞬态毒化；其约 160ms 存活与 INT-REPORT §4.4 的“49ms 窗口内被随后 persist 覆盖”假说方向一致（存活量级同为百毫秒内），但**清理路径本身仍未定位**（哪一次 persist、是否确为直接推 `UpdateService` 的重建覆盖，日志中无对应落盘时序——A1 窗口的 agent journal 在本路 grep 口径下无 AddService/UpdateService 行留存）。

### 1.3 归因污染（诚实登记，结论降级的原因）

Phase A 窗口内测试机被他 lane 并发使用，本路观测到的状态变化不能全归因于本路重启：

- agent 二进制在本路未触碰的情况下切换：A1 起 `aff338e3…` → A2 起 `23aa260d…`（修后 r17d）→ A6 起 `ab628bcd…`（未修复另一构建）。
- 面板在窗口内由 1.3.5 切为 `flmx-backend:r17d` / `flmx-frontend:r17d`（01:47:29Z 查得 Up 5 分钟，即约 01:42Z 上线，正处 A3 窗口内）。
- 非本路重启：A4 窗口内出现新 PID 714880（01:43:28），A5 窗口内出现新 PID 715269（01:44:13）与 715443（01:44:23）；A4 末 agent 为 inactive（pid 0）。
- A6 pre 的毒化文件写于 A5 终态干净（01:44:27.894）至 A6 起（01:44:33.430）的 6s 间隙内，同一间隙内二进制被换为 `ab628bcd…`——他 lane 在该间隙内操作是更可能的写成方；A6 终态的清理（毒化 + 未修复 agent 组合照 R17 严重性探针应 `failed`，实测却 active + 文件 pristine）亦只能解释为他 lane 在窗口内干预（换修后 agent 自愈或恢复 pristine 文件），非本路所致。
- 因此：A1 瞬态重复的时序（重启后约 30s，与 A3/A4/A5 的 redeploy 命令簇 20–45s 到达时延同量级）与本路重启的 redeploy 一致，**倾向于是本路重启触发**，但因他 lane 同窗口活跃，不得排除他 lane 触发——归因记为“倾向本路、未可确证”。kill-freeze（sent-ok 瞬间 SIGKILL 冻结落盘）本轮未执行：INT-REPORT 两次 kill-freeze 均未等到 sent-ok，且本窗口的 sent-ok 时刻无 guard 行可盯（1.3.5 面板），冻结判据不成立。

### 1.4 结论

**部分进展，仍未验证关闭。** 新证据：瞬态重复落盘已被 50ms 快采直接捕获（A1，文件与 R17 毒化件逐字节一致），49ms 窗口假说的“写出后很快被覆盖”一环得到实测支撑。仍缺：sent-ok 瞬间冻结取证、清理 persist 的逐行定位、无干扰下的干净归因。关闭条件：串行窗口内重跑快采 + kill-freeze，抓到 sent-ok 时刻的落盘快照与随后清理 persist 的对应日志行。

## 2. VLESS 用户凭证腿（u1/u4）— 仍未验证

未跑。本轮未部署候选件（见 CAND-01：01:42Z 起测试机由他 lane 的 r17d 部署占用，agent 处于其对照配置；本路部署会冲掉他 lane 的运行态与证据）。已备好的执行口径（留给串行重跑）：以 a0c18a0 树构建候选件部署；建 VLESS 入站（Xray/plain TCP/security none，`connIdleSeconds=5`，端口 59904）；以机上 `/root/r17/vless_test.py`（Python 直连，取 UUID、端口、哑目标端口、超时）跑 u1/u4 UUID（从 agent 下发 xray 配置的 per-user 客户端条目中程序化提取，不明文抄录）；判定口径沿 INT-REPORT §7.1 D1-direct（3.5–6.5s）+ D2 无值对照。148B 的 5.20–5.28s 与 INT-REPORT 的主凭证 5.25s 均为上一基线值，不得冒充 a0c18a0 的用户腿值。

## 3. Python 直连 SS 客户端腿 — 仍未验证

未跑（同 §2 的串行阻塞）。R17/148B 口径不变：自写 SS 客户端实现不可靠，其结论不作数；SS 5s 结论仍以 dokodemo-door 供数腿为准。串行重跑时须先实现 SS AEAD TCP 客户端（机上 `ss_test.py` 在位，可作起点），并以供数腿交叉核对。

## 4. Trojan 最小 xray A/B 腿 — 仍未验证

未跑（同 §2 的串行阻塞）。串行重跑口径：最小 xray（无 policy）配 Trojan 入站，Python TLS 直连（机上 `/root/r17/trojan_test.py`）超时 30s 应 STILL_OPEN，与面板 Trojan 直连腿（INT-REPORT：5.09s）成 A/B 对照。

## 5. 下游供数与 1MB 批量腿 — 仍未验证

未跑（同 §2 的串行阻塞）。R17 值（下游供数 12.14s/12.11s、批量 3.11/3.12s）仍为上一轮值，不得冒充 fix-r17/a0c18a0 值。串行重跑口径：沿 R17-L4 D 组（`socks_test2.py` 带目标端口参数，M2 教训：勿用目标写死的 `socks_test.py`）重跑 VMess 下游供数、VMess 1MB、SS 1MB；O-R17-1 的批量大小依赖（只测 1MB 一档）建议补 100KB/10MB 两档；VLESS 批量值（R17 §8 记未重测）建议一并补。

## 6. 管理 API 两处同型盲 append — 行为腿已验证 PASS（2/2）；可达性待 L2；本路不立案

### 6.1 行为腿：隔离 handler 探针 2/2 PASS（本轮新证据）

在 `~/workspace/r18-l5-scratch/gost/`（a0c18a0 `go-gost` 复制件）内以临时测试文件 `x/api/r18_l5_api_dup_test.go`（仅隔离副本存在，永不入库）直接调用两 handler（gin `httptest`，body 直绑 `ServiceConfig`——`ShouldBindJSON(&req.Data)` 要求体本身即 ServiceConfig，首跑误用 `{"data":…}` 包裹得 400，已订正）：

- 前提（与 A 路 socket 探针同构）：配置内预置同名陈旧条目（`{name, addr: 127.0.0.1:0}`），注册表内无该名；`config.SetPersistPath(<tmp>/gost.json)` + `EnablePersist()`。
- `TestR18L5ApiCreateServiceBlindAppend`：**PASS**——`createService` 返回 200 `{"msg":"OK"}`，内存 2 份、**落盘 2 份**同名条目。
- `TestR18L5ApiCreateServicesBlindAppend`：**PASS**——`createServices`（批量）返回 200，内存 2 份、**落盘 2 份**同名条目。
- 日志：`R18-L5-api-dup.log`（928 字节，`ok github.com/go-gost/x/api 0.260s`，2/2 PASS）。运行命令：`cd gost/x && TMPDIR=<scratch>/tmp go test ./api/ -run 'TestR18L5ApiCreateService' -v -count=1`（`TMPDIR` 须指离 `/tmp`——沙箱 `/tmp` 为 512M tmpfs、已用 95%，首跑即因 link 写盘报 `no space left on device`；改 `TMPDIR` 后通过。后续 lane 注意）。
- 结论：两处盲 append 在 a0c18a0 均** live**——`IsRegistered` 门 + 注册成功后无去重 append 的形状与修前 socket `createServices` 第 4 阶段逐字同构（`config_service.go:84-85` 单个、`:169-171` 批量），`config.OnUpdate` 随后同步 `persist()` 写盘（`x/config/config.go:134-144`、`persist.go`）。

### 6.2 分歧前提已在产品代码中定位（非合成假设）

- `x/config/loader/loader.go:257`：暂停服务在启动加载时被跳过注册（`if paused … { continue }`），但仍留在 `cfg.Services` 中——配置有其名、注册表无其名，正是探针所用的分歧态，面板暂停/恢复为其生产来源。
- `x/api/config_service.go` `updateService`：`old.Close()` + `Unregister(name)` 后 `ParseService` 失败即 return（约 :230–:245），配置内旧条目保留、注册表已失其名——同一 API 内即可制造分歧。
- 缓解面（a0c18a0 已有，非本项修复）：`program.Start()` 接 `healPersistedConfig`（`program.go:77`），毒化文件在下次启动时去重告警、不再自砖——该向量的最坏后果已由“下次重启自砖”降为“落盘毒化 + 启动告警自愈”；但**写出重复本身未修复**，重复仍可经由管理 API 写出。

### 6.3 可达性（L2 定；本路只列支撑证据，不代判）

- 运行态（2026-10-25 01:47Z 直查）：agent 进程无 `-api` 参数、无 `GOST_API` 环境变量、`/etc/flux_agent/gost.json` 顶层键仅 `climiters/limiters/services`、无 `api` 段。
- 全仓检索（a0c18a0）：`install.sh` / `panel_install.sh` / `go-backend/` 内无 `GOST_API` / `-api` 启用点；唯一启用点为 `x/config/parsing/parser/parser.go:77`（读 `GOST_API` 环境变量）。与 A-road §6 的“当前部署不可达”结论一致。
- **处置**：本路不立案（任务书禁令）；待 R18-L2 可达性结论补引后由父级立项——修复款式与 A 路同（upsert 替换盲 append），另建议同查 A-road 点名的链/限流器同型残留（`x/socket/chain.go:34`、`limiter.go` 各处、`x/api/config_chain.go`/`config_limiter.go` 等，本轮未验）。

## 7. 远端节点与非 Debian 内核 — 仍未验证

- `git diff 5473377..a0c18a0 --stat -- vite-frontend scripts panel_install.sh` 为空；`install.sh` 唯一改动为 C 路 unit 日志行（`StandardOutput/Error: null → journal` + `SyslogIdentifier=flux_agent`，OpenRC 输出改 `/var/log/flux_agent.log`）——**零多节点/多发行版验证手段新增**。
- `test-install-scripts-alpine.sh` 仍只验安装脚本（一次性 Alpine 容器内跑 `install.sh`、验 OpenRC 服务），不验 agent 在非 Debian 内核上的 sock_diag 分族、调优、内核下发行为；且本沙箱无 Docker（`docker: command not found`，本轮实测），本路无法执行。
- 测试机仍为单机 Debian（Lightsail Debian 12）；第二节点与非 Debian 内核均未获。结论：**仍未验证**。

## 8. 浏览器 DOM 腿 — 仍未验证

- 本路为通用子代理，无浏览器任务能力，不得声称已验。
- served-chunk 基线复核：`git diff 5473377..a0c18a0 -- vite-frontend` 为空——R17-L5 §4.2 的 5473377 构建基线（58 JS + 1 CSS，`R17-L5-chunks.sha256`，`dist/index.html` sha256 `3e969898…`）对 a0c18a0 仍有效，**无需重定**（R17-L5 §4.3 的重定条件是前端文件改动，本轮未触发）。served 比对与实点（入站创建/编辑/分流设置、调优弹窗“填入建议值”、延迟提示药丸、节点页两视图延迟区）仍待父代理侧浏览器任务。

## 9. 订阅 1 流量计数器 — 仍未验证

- 计数器读数方法已验证可用：`POST /api/v1/subscription/traffic {"id":1}` → `data.up/down` 总计。门前（01:38Z）与门后（01:48Z）两次读数均为 **up 3648 / down 2105821**，与 R17 门后值逐字一致——期间无流量产生。
- 本轮腿未产生 u1 供数流量（§2 腿未跑），计数器对 u1 流量的响应性**未验证**。串行重跑时以 item 2 的 VLESS u1 供数腿产生流量后复读（R17 的 u1 供数曾使计数器 +1388/+2652，同口径可复核）。

## 10. 新发现

### R18-L5-CAND-01（流程·建议 P1）—— 测试机多 lane 并行，审计窗口互相污染

- **对象**：Lightsail 测试机（100.80.182.11）在本轮 Phase A 窗口（01:40–01:45Z）内的共享使用。
- **现象**：本路 Phase A 期间，他 lane 部署 `flmx-backend:r17d` / `flmx-frontend:r17d`（约 01:42Z 上线）、agent 二进制在 `aff338e3…`（未修复）→ `23aa260d…`（修后 r17d）→ `ab628bcd…`（未修复另一构建）间切换、非本路重启 3 次（新 PID 714880/715269/715443）、A4 末 agent inactive、A6 窗口内毒化文件被他 lane 清理。`audit-out-132/r18/` 内同时存在 L1/L3/L4 他路文件（时戳 01:35–01:48Z），佐证多 lane 并行。
- **影响**：本路 Phase A 归因污染（§1.3）、Phase B（items 2–5 的候选件部署与计时腿）无法执行；本路 6 次重启亦可能污染他 lane 的证据（其 leg2 对照/修后腿正在同一窗口内跑）。单机审计的“一次一 lane”是 R17 各轮的隐含前提，本轮被打破。
- **建议处置**：主控对测试机实行 lane 间互斥排期（部署/重启类操作串行，串行窗口内他 lane 只读）；本路 Phase B（items 2–5）与 item 1 的 kill-freeze 改期到独占窗口重跑；R18 总账须注明本轮 L5 的 4 项“仍未验证”系**排期阻塞**而非技术不可验。

### R18-L5-CAND-02（观察项，不立案）—— A1 瞬态重复是 OPEN-01 窗口的首次直接捕获

- **对象**：`R18-L5-fastdup-A1-1.json`（md5 `e381299b…`，与 R17 毒化件逐字节一致）+ `R18-L5-phaseA-fastpoll.log` A1 段（01:40:38.043–38.202Z，3 采样）。
- **内容**：见 §1.2/§1.4。支持“重复项在随后 persist 中被覆盖清理”的假说方向，但清理 persist 的逐行定位仍缺；关闭条件见 §1.4。
- **建议处置**：记入 R18 总账观察项；不另立产品项；R17D-OPEN-01 本身仍为未验证，不关闭。

## 11. 门后状态（本路未部署候选件，无“恢复”动作；以下为离场时只读核对）

| 对账项 | 门前（01:36–01:38Z） | 离场（01:48Z） | 说明 |
|---|---|---|---|
| 计数 node/tunnel/forward/inbound/subscription | 1 / 4 / 6 / 578 / 18 | 1 / 4 / 6 / 578 / 18 | 一致；本路未建测试入站 |
| 订阅 1 计数器 up/down | 3648 / 2105821 | 3648 / 2105821 | 一致；无流量产生 |
| gost.json | md5 `d48f09e1…`、328 行 | 同 md5、328 行 | 一致 |
| 面板镜像 | `r1gate-1.3.5` | `r17d`（Up 5 分钟） | **他 lane 部署，非本路所为；本路未动** |
| agent 二进制 | `aff338e3…`（未修复） | `ab628bcd…`（未修复另一构建）、active、MainPID 716353、NRestarts=0 | **他 lane 更换，非本路所为**；本路未将其恢复为 1.3.5 pristine——当前运行态属他 lane 的对照配置，擅自恢复会冲掉其证据 |
| 本路机上残留 | — | `/root/r18/` 仅 4 文件（`fastpoll-r18.sh`、`phaseA-fastpoll.log`、2 个毒化样本；445 个 A6 重复采样已删，仅留 1 份样本） | 已清理；无残留进程 |

## 12. 未做事项（明文）

未部署候选件（items 2–5 未跑，见 CAND-01）；未执行 kill-freeze（§1.3）；未向用户索取任何令牌；未登录生产面板；未做浏览器实点；未查 CI 运行结果；未改产品代码、未合 main、未打 tag、未发版。候选件本地构建曾启动（`~/workspace/r18-l5-scratch/build-r18.sh`，paneld 起步）后被沙箱 VM 重置中断（`build-out/` 为空、后台会话丢失），与任何结论无关——构建产物不是本轮任一判定的依据。

## 13. 证据清单

| 文件 | 内容 | 字节数 |
|---|---|---|
| `audit-out-132/r18/R18-L5-phaseA-fastpoll.log` | Phase A 6 循环原始日志（A1 瞬态重复 3 采样 + A6 445 采样 + 各循环 journal 节录） | 95,374 |
| `audit-out-132/r18/R18-L5-fastdup-A1-1.json` | A1 捕获的瞬态重复文件原件（378 行，md5 `e381299b2aa931d2f7c097661ead6702`，与 R17 毒化件逐字节一致） | 7,220 |
| `audit-out-132/r18/R18-L5-api-dup.log` | 管理 API 盲 append 隔离探针原始日志（2/2 PASS，`ok … 0.260s`） | 928 |
| `audit-out-132/r18/R18-L5-fastpoll-r18.sh` | Phase A 快采脚本（INT-REPORT fastpoll 改编版） | 2,655 |
| 机上 `/root/r18/` | `fastpoll-r18.sh`、`phaseA-fastpoll.log`、`fastdup-A1-1.json`、`fastdup-A6-1.json`（A6 样本，md5 同 `e381299b…`） | 见机上 |
| 本报告 | `audit-out-132/r18/R18-L5-unverified.md` | 见自证 |

— R18-L5-unverified END · 自证：本行之前正文字节数 22668 · END
