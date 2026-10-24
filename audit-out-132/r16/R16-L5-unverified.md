# R16 未验证清单复核（L5）— main @ b54b44f

- **审计对象**：`main @ b54b44f`（完整提交 `b54b44f772d8b4efdd6d08b1fbbbc40c8091a143`，R15 修复 fix-r15 已合入并推 origin；本地只读 worktree `~/workspace/.r16-tree @ b54b44f`，detached HEAD，工作树干净——本轮结束时复核 `git status --porcelain` 为 0 行）
- **依据**：`audit-out-132/r15/R15-X-ledger.md` §6（仍未验证项）、`audit-out-132/r15/R15-L6-unverified.md`（§1.1/§1.3/§3/§5/§6/§8 U-1~U-4）、`audit-out-132/PORT-148B-GATE.md`（148B 门计时证据，门对象 `integrate-148b @ 7244fbb`）、`audit-out-132/r16/R16-00-baseline.md`（R15-FIX-REPORT.md 在工作树与全部历史中均不存在，协调者已查；本路以 ledger §6 清单为准，不引用该文件）
- **复核日期**：2026-10-25（测试日志时戳 UTC 2026-10-24 22:0x，即 CST 2026-10-25 06:0x）
- **性质**：只读审计，不改产品代码、不打 tag、不发版。测试一律在 `/tmp` 隔离副本跑（`/tmp/r16-l5-sock`、`/tmp/r16-l5-gist`），只读树永不修改；Go 用 `/home/hatch/go/bin/go`（`go1.25.1`）
- **护栏**：N-01…N-37 全程有效；裁定只认落盘原始日志 + 名单存档（N-37），摘要计数不得作裁定依据。本报告每项判定均以落盘日志原文或 `git show`/`git diff` 直读代码为准

## 结论总表

| # | 未验证项（R15-X-ledger §6） | R16-L5 结论 | 证据 / 登记原因 |
|---|---|---|---|
| 1 | 真实 Gist 成功上传/下载往返 | **仍未验证** | §1：无真实令牌（未索取）；不依赖令牌的可验路径经核查不存在——`FLMX_GIST_API_BASE` mock 路径只验客户端对假服务，不验真实 GitHub；mock 链本轮隔离复跑 22/22 PASS（§1.3，仅为机制证据，不得冒充端到端） |
| 2 | 单族 sock_diag 失败警告形态 | **部分已验证 / 失败形态仍未验证** | §2：单元测试覆盖仍在位且本轮隔离复跑 PASS（新证据）；真机单族失败仍无法触发，无新增故障注入手段（生产代码零 env hook） |
| 3 | 远端节点与非 Debian 内核 | **仍未验证** | §3：`5bd5a90..b54b44f` 零基建改动，无新增可验手段；既存 Alpine 脚本只验安装脚本、且本机无 Docker 无法跑 |
| 4 | 生产面板 DOM 鼠标实点 | **仍未验证** | §4：本路为通用子代理、无浏览器任务能力，不得声称已验；须由父代理侧浏览器任务实点；另 R15 静态 chunk 证据因 `inbound.tsx` 改动已过期（L5-CAND-01） |
| 5 | VMess/SS/Trojan 计时腿未在 5bd5a90 重跑 | **仍未验证（登记须重跑）** | §5：`7244fbb..b54b44f` 触及内核渲染路径 6 文件（F-03 等），须点名改动并重跑，不得以旧证据顶替；`5bd5a90..b54b44f` 渲染路径零改动，新增干涉为零，但不构成承继 |

## 1. 真实 Gist 成功上传/下载往返 — 仍未验证

### 1.1 不依赖令牌的可验路径：经核查不存在（对“真实”而言）

`git show b54b44f:go-backend/internal/http/handler/gist_backup.go` 直读：

- 上传与下载均硬性要求令牌。`gistUpload`（`:337-340`）：`token == ""` → `errText("请先填写 GitHub 令牌")`；版本列表 handler（`:599-602`）：`token == ""` → `"请先填写 GitHub 令牌"`；`gistVersionContent`（`:639`）同样先判空令牌。`gistRequest`（`:251`）恒发 `Authorization: Bearer <token>`——即使读公开 Gist，面板路径也不走无令牌分支。
- API 基址可改，但改的不是“真实”。`gistAPIBase()`（`:85-94`）：读 `FLMX_GIST_API_BASE`（兼容回落 `FLVX_GIST_API_BASE`），默认 `https://api.github.com`；文件头注释明文：“API 基址环境变量为 FLMX_GIST_API_BASE……默认 https://api.github.com”，函数注释：“测试用环境变量指向 mock 服务”。该变量是测试钩子，不是真实 GitHub 的免令牌通道。
- 结论：不依赖令牌的可验路径只有一条——把基址指向本地 mock 服务、用哑令牌跑上传/下载（见 §1.3）。它验证的是客户端对假服务的请求/响应构造，**不是**真实 GitHub 的成功上传/下载往返。真实往返仍缺真实令牌；无令牌、未向用户索取（按任务书禁令）。

### 1.2 仍未验证的具体内容（与 R15-L6 §3 同口径）

真实令牌下的上传成功（新建/更新 Gist）、下载恢复往返、加密内容解密还原。R15-L6 §4 的定时实测（哑令牌真实打到 GitHub 得 401 落库）只证明上传尝试真实发出且失败落库，不得冒充成功路径。

### 1.3 Mock 机制本轮新证据（仅机制，不得冒充端到端）

`gist_backup_test.go`（b54b44f）13 个 `TestGist*` 用例 + 同包其余 `TestGistRestore*` 用例，全部用 `httptest` mock GitHub API（`newMockGistServer`，`t.Setenv("FLMX_GIST_API_BASE", m.server.URL)`，`:144-169`）。本轮在 `/tmp/r16-l5-gist`（b54b44f 隔离副本）复跑：

```
go test ./internal/http/handler/ -run 'TestGist' -v -count=1
--- PASS 22 项、--- FAIL 0 项，ok go-backend/internal/http/handler 36.087s
```

22 项含上传建/补/跳过不变（`TestGistUploadCreatesThenPatchesAndSkipsUnchanged`）、加密上传、删后重建、失败记 `lastError`、无令牌拒绝、明文拒传、版本列出近 10、恢复复用（`TestGistVersionContentForRestoreReuse`）、恢复加密往返。日志：`audit-out-132/r16/R16-L5-gist-mock.log`（24,233 字节）。该结果只证明 mock 路径在 b54b44f 仍通；真实 Gist 往返结论仍为**仍未验证**。

## 2. 单族 sock_diag 失败警告形态 — 单元覆盖本轮已验证；真机形态仍未验证

### 2.1 单元测试覆盖仍在位（本轮新证据）

- 测试文件 `go-gost/x/socket/r14_f06f04_test.go` 在 b54b44f 在位（2,990 字节），`TestR14F06F04SingleFamilyFailureWarns` 四腿：IPv6 失败→警告含“IPv6/仅基于 IPv4”+日志；IPv4 失败镜像腿；双族正常无警告；双族皆败走整腿回落警告（`sock_diag 取数失败`）。
- 生产接线仍在位：`tuning_probe.go:219-225`（单族失败时警告 `"%s sock_diag 取数失败，中位数仅基于 %s 连接"`）、`tuning_probe_linux.go:64-77`（分族 `FamilyErrors` 填充；双族皆败返回整腿错误）。
- 本轮在 `/tmp/r16-l5-sock/x`（b54b44f 隔离副本）复跑：`go test ./socket/ -run 'TestR14F06F04SingleFamilyFailureWarns' -v -count=1` → **PASS（0.00s）**，日志含 `tune probe: IPv4 sock_diag dump failed, RTT median covers IPv6 only: v4 dump failed`。日志：`audit-out-132/r16/R16-L5-sockdiag-unit.log`（373 字节）。`5bd5a90..b54b44f` 未动 `go-gost/x/socket/` 任一文件（diff 为空），覆盖无衰减。

### 2.2 真机单族失败形态仍未验证：无新增故障注入手段

- 生产代码中无任何故障注入钩子：`grep -rn "os.Getenv|Getenv" go-gost/x/socket/*.go`（非测试文件）**零命中**；`fault/inject/FORCE_/FAIL_/SIMULATE` 检索亦零命中（仅 `ParseChain`、`kernel.Default` 等无关命中）。唯一可换实现处是包级变量 `tuneProbeTCPRTTs`（`tuning_probe.go:88`，默认 `sockDiagTCPRTTs`，`:211` 调用）——进程内测试缝，只能由测试代码替换，无环境变量、无 API、无命令行可从运行时触发单族失败。
- 本机（R15-L6 结论沿用，未获新手段推翻）双族 dump 均成功，无法自然触发；`5bd5a90..b54b44f` 的 9 文件 diff 中无任何 sock_diag/调优相关改动，无新增注入路径。
- 结论：单族失败警告的**真机形态仍未验证**；单元覆盖的本轮已验证（§2.1）不得冒充真机形态。

## 3. 远端节点与非 Debian 内核 — 仍未验证

- `git diff 5bd5a90..b54b44f --stat` 共 9 文件：审计归档 3（`PORT-3342-REA-UDIT.md`、`r14/R14-00-baseline.md`、`r14/R14-00-routes-raw.csv`）、`skills/flmx-api/references/errors.md`、`vite-frontend/scripts/demo/` 3（`build_fixtures.py`、`check-fixtures-content.mjs`、`record_items.mjs`）、`src/demo/fixtures.json`、`src/pages/inbound.tsx`。**零测试基建、零多节点/多发行版验证手段新增**。`scripts/`、`install.sh`、`panel_install.sh`、`go-gost/`、`go-backend/` 在该区间 diff 全为空。
- 既存手段复核（均非新增，且均不覆盖本项）：
  - `test-install-scripts-alpine.sh`（自 `v1.0.0` 即在，非新增）：在一次性 Alpine 容器（`docker run --rm … alpine:3.22`）内以 root 跑 `install.sh`、验 OpenRC 服务——只验安装脚本，不验 agent 在非 Debian 内核上的 sock_diag 分族、调优、内核下发行为；且本沙箱无 Docker，本路无法执行。
  - 测试机仍为单机 Debian（R15-L6：Lightsail Debian 12，内核 `6.1.0-54-cloud-amd64`）；本路为通用子代理、无测试机 SSH 通路，未获第二节点与非 Debian 内核。
- 结论：**仍未验证**。联邦路径、其他发行版/内核的 agent 行为均未验证。

## 4. 生产面板 DOM 鼠标实点 — 仍未验证

- 本路为通用子代理，无浏览器任务能力（不能开 live 浏览器、不能点击/填表/实点）。按任务书：**不得声称已验**。
- 结论：**仍未验证**，须由父代理侧浏览器任务实点。待点项与 R15-L6 §6 同：入站创建/编辑/分流设置三处交互、调优弹窗“填入建议值”按钮、延迟提示药丸点击跳转、节点页两视图延迟区渲染。
- 补充（见 L5-CAND-01）：R15-L6 的静态上限证据（候选面板实 served 58/58 chunk 逐字节一致）是在 5bd5a90 构建上测得；fix-r15 改了 `inbound.tsx`（见 §5.3），b54b44f 的前端构建与该证据已不是同一构建。浏览器任务执行前须先重定 served chunk 基线，不得直接沿用 R15 的 58/58。

## 5. VMess/SS/Trojan 计时腿未在 5bd5a90 重跑 — 仍未验证（登记须重跑）

### 5.1 148B 门计时证据原文（门对象 `integrate-148b @ 7244fbb`，`PORT-148B-GATE.md` §3）

| 腿 | 证据 |
|---|---|
| VLESS 915 主/u1/u4 直连 | 5.28s / 5.20s / 5.21s 关断 |
| VLESS 918 链式主/u1 | 5.20s / 5.20s 关断 |
| VLESS 916 对照（无 connIdle） | 20s 仍不断 |
| Trojan 920 直连 | Python TLS 直连客户端 5.06s 关断；经 client xray 6.13s/6.15s（+~1.1s 为 client 传播 artifact） |
| VMess 919 上游供数模式 | 停发后 6.01s 关断（=5s+1s client 延迟）；对照最小 VMess 同模式 STILL_OPEN 至 25s，A/B 证明 5s 关断由面板配置引起 |
| VMess 919 / SS 921 完全静默 | ~1.10–1.11s 关断；最小隔离 xray（无 policy、默认 300s）同复现，系上游 Xray 26.3.27 自身行为，先于面板 5s 定时器，方向安全 |

注意：VMess/SS 的 148B 证据本身是有条件的——静默腿由上游机制先行收尾，operative 证明依赖上游供数模式的 A/B 对照。R15-L6 只重跑了 VLESS 四腿（§1.1：服务端关断 5.5–5.9s 直连/分流、6.5–6.7s 链式经由、对照 20s 不断），VMess/SS/Trojan 在 5bd5a90 未重跑，登记为 U-1 未验证。

### 5.2 渲染文件清单逐一核对（13 文件 × 2 区间）

渲染路径口径：从入站定义产出内核配置的发射/路由/模型/下发校验文件（`go-backend/internal/kernel/` 渲染系 + `handler/kernel*.go` 下发/校验邻接）。

| 文件 | `7244fbb..b54b44f` | `5bd5a90..b54b44f` |
|---|---|---|
| `kernel/render.go`（分发） | ZERO | ZERO |
| `kernel/render_xray.go` | **改动 +56/−17**（§5.3） | ZERO |
| `kernel/render_singbox.go` | ZERO | ZERO |
| `kernel/render_snell.go` | ZERO | ZERO |
| `kernel/chain.go` | **改动 +5/−2**（§5.3） | ZERO |
| `kernel/split.go` | **改动 +34/−3**（§5.3） | ZERO |
| `kernel/model.go` | **改动 +10/−8**（§5.3） | ZERO |
| `kernel/egress.go`、`egress_xray.go` | ZERO | ZERO |
| `kernel/realm.go`、`proxy.go` | ZERO | ZERO |
| `handler/kernel.go` | **改动 +46**（§5.3） | ZERO |
| `handler/kernel_chain.go` | **改动 +14/−3**（§5.3） | ZERO |

另：`go-gost/` 在 `5bd5a90..b54b44f` diff 为空；在 `7244fbb..b54b44f` 仅动调优探针（`tuning_probe*.go` + `r14_f06f04_test.go`），与 connIdle 渲染无关。

### 5.3 点名改动（`7244fbb..b54b44f` 触及渲染路径的 6 文件；与 `7244fbb..5bd5a90` 同内容，fix-r15 未再动其中任一文件）

1. **`kernel/render_xray.go`（F-03，核心）**：新增 `idleOutboundSet()`——每渲染一次算出需下发 idle 出站的入站集（直连 idle 入站 + 带分流直连规则的链式 idle 入站）；`xrayConnIdleRoutes()` 改为按该集合下发、链式入站不再下发 idle 规则（仅出站供分流直连引用，因链式默认规则先匹配、idle 规则必死）；`renderXray()` 与 `xrayChainRoutes()` 透传该集合。改动正落在 connIdle 下发函数本身。
2. **`kernel/chain.go`**：`xrayChainRoutes()` 签名增加 `idleEmitted` 参数并透传给 `xraySplitRoutes()`，防分流直连规则引用未下发的 idle 出站。
3. **`kernel/split.go`**：新增 `hasSplitDirect()`；`xraySplitRoutes()` 以 `idleReady = idleLevel > 0 && idleEmitted[in.ID]` 决定分流直连是否走 idle 出站，否则回落共享直连/mark 出站并记日志（“分流直连回落到共享直连出站（该路流量超时不生效）”）——分流直连腿的超时生效条件被改写。
4. **`kernel/model.go`**：`ConnIdleSeconds` 校验由 `1..MaxConnIdleSeconds` 改为 `0..MaxConnIdleSeconds`（0=清除，Normalize 接受；渲染语义不变——0 仍渲染为无 segment），报错文案同步改。改的是同一字段的保存口径。
5. **`handler/kernel.go`**：新增 `healSnellPoisonedRows()` 并在 `applyKernelWith()` 头部调用（Snell 毒化行下发前清路由）。仅 Snell 内核触发（`k.KernelType != kernel.TypeSnell` 即返回），对 Xray 计时腿无直接影响，但属于下发入口改动。
6. **`handler/kernel_chain.go`**：Snell 组合守卫前移至目标校验之前（保存期拒绝 Snell+Via/分流，与渲染文案逐字对齐）。仅 Snell 分支，对 Xray 计时腿无直接影响。

### 5.4 为什么不承继——非干涉子分析写明，但不构成承继依据

- 逐 hunk 核对后：对**直连（`Via==nil`）Xray 入站**，F-03 的下发物与 7244fbb 行为等价——`idleEmitted` 对直连 idle 入站恒真；每用户 idle 出站（level+mark）与 catch-all 规则的发射条件不变；分流直连的 `idleReady` 在直连 idle 入站上恒真。改动实际改变行为的只是链式 idle 入站（新增分流直连出站、去掉死规则）与分流直连回落路径。
- 但“行为等价”是代码读数结论，不是实测结论。任务书的承继条件是“渲染路径零改动”——`7244fbb..b54b44f` 在渲染路径上有 6 文件改动，**不满足**。按任务书规则（“如有改动，须点名改动并登记须重跑，不得以旧证据顶替”）与审计纪律（计时证据不得跨渲染改动继承），148B 门的 VMess/SS/Trojan 计时证据在 b54b44f 不得顶替为已验。
- 增量说明：`5bd5a90..b54b44f` 在 13 文件渲染清单上全部 ZERO——fix-r15 未新增任何渲染干涉；须重跑的范围与 R15 登记的 U-1 相同，未扩大、也未缩小。
- 结论：**仍未验证**。须在 b54b44f（或后续候选版）上重跑 VMess/SS/Trojan 三协议 5s 计时腿（含 148B 口径的上游供数 A/B 对照与静默腿定性），重跑通过前不得引用 148B 证据作本基线结论。

### 5.5 增量区间的唯一前端改动（非渲染路径，但与 DOM 腿相关）

`5bd5a90..b54b44f` 唯一产品代码改动是 `vite-frontend/src/pages/inbound.tsx`（+8/−2）：外部入站首轮探测未完成（`probe=null`）时，旧式 `!item.probe?.skipped` 为真、会把 `null` 原样赋给 `probe`、随后读 `null.ok` 抛错致整页空白；改为 `item.probe && !item.probe.skipped` 才取值。纯展示层空值守卫，不触 connIdle、不触内核渲染、不触任何计时路径。见 L5-CAND-01（该改动使 R15 的 served chunk 证据过期）。

## 6. 新发现（临时编号，由协调者统一编号；不得自编 R16-X-##）

### L5-CAND-01（观察项，非产品缺陷，不建议单独立案）—— R15 served-chunk 静态证据在 b54b44f 已过期

- **对象**：`vite-frontend/src/pages/inbound.tsx`（fix-r15，`5bd5a90..b54b44f` 唯一产品代码改动，§5.5）与 R15-L6 §6 的 DOM 静态证据（候选面板实 served 58/58 chunk 逐字节一致，5bd5a90 构建）。
- **现象**：b54b44f 的前端源码已不是 5bd5a90 的构建输入；R15 的 58/58 逐字节一致结论描述的是旧构建，不能直接沿用为 b54b44f 的 DOM 静态证据。
- **影响**：仅证据时效。DOM 腿本就仍未验证（§4）；父代理侧浏览器任务执行前，须先以 b54b44f 构建重定 served chunk 基线（重比 58/58 + 关键字符串在位），再实点。
- **建议处置**：记入 R16 总账观察项，由执行浏览器任务的一路顺带重定基线；不另立产品项。

（本轮其余核对未发现新产品缺陷：Gist mock 链 22/22、sock_diag 单元 1/1 均在本轮隔离复跑通过；渲染清单核对未见清单外改动。）

## 7. 证据清单

| 文件 | 内容 | 字节数 |
|---|---|---|
| `audit-out-132/r16/R16-L5-gist-mock.log` | b54b44f 隔离副本 Gist mock 链复跑原始日志（`TestGist*` 22 PASS / 0 FAIL，`ok … 36.087s`） | 24,233 |
| `audit-out-132/r16/R16-L5-sockdiag-unit.log` | b54b44f 隔离副本单族 sock_diag 单元复跑原始日志（PASS，`ok … 0.032s`） | 373 |
| 本报告 | `audit-out-132/r16/R16-L5-unverified.md` | 见自证 |

- 只读树状态：`~/workspace/.r16-tree @ b54b44f772d8b4efdd6d08b1fbbbc40c8091a143`，本轮结束 `git status --porcelain` 0 行（未修改）。
- 隔离副本：`/tmp/r16-l5-sock`（go-gost）、`/tmp/r16-l5-gist`（go-backend），均为 b54b44f 复制件；测试写盘只发生在副本与 Go 构建缓存内，未动只读树与共享工作树。
- 未做事项（明文）：未向用户索取任何令牌；未登录任何面板；未操作测试机（本路无 SSH 通路）；未做浏览器实点（本路无浏览器任务能力）。

— R16-L5-unverified END · 自证：本行之前正文字节数 18211 · END
