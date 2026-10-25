# R19 修复报告：F-01——删隧道披露残留（R19-X-01）

- 基线：main @ `d66042c`（`d66042cc0b7ac011ddfad09f3f2709b90dafda2e`）。
- 分支：`fix-r19-f01`（独立 worktree `~/workspace/.fixr19-f01`，自 `d66042c` 切出）。
- 依据：`R19-FIX-PLAN.md`（F-01 节）、`R19-X-ledger.md`（R19-X-01）、`R19-X-verdicts.md`（V-02）、`R19-L2-gaps.md` §1（L2-CAND-01，含 :772 邻接点）。
- 范围：只做 F-01。不合 main、不打 tag、不发版、不改版本字面量。无 BC 项。
- 结论：三处裸返全部补披露；先红后绿 6 FAIL → 17/17 PASS；`go build`/`go vet` exit 0；`go test ./...` 产品失败 0（唯一命名失败为已知沙箱 fake-IP DNS 环境红，见 §4）。

## 1. 缺陷复述（基线行号）

R18-X-01 只修了 gost 分支；同类披露缺口残留两处，外加 L2 点名的同分支邻接点一处：

| 点 | 位置（基线） | 到达时状态 | 原行为 |
|---|---|---|---|
| N-1 | `tunnel_delete_resolution.go:789`，nft 分支删库循环 | reconcile 已成功（全部 nft 规则已下线）、此前转发库行已删 | `return err` 裸返 |
| N-2 | 同文件 `:772`，`MarkForwardPendingDelete` 循环（L2 §1a 邻接点，同候选修复范围） | reconcile 尚未执行、规则未下线；此前转发仅已标记待删除 | `return err` 裸返 |
| D-1 | 同文件 `:832`，`deleteTunnelAndCleanup` 内 `deleteTunnelByID` 失败 | `cleanupTunnelForwardsDataPlane` 已返回 nil，名下转发已删 | `return err` 裸返 |
| D-2 | `mutations.go:2609`，`tunnelDelete` 内 `deleteTunnelByID` 失败 | 同上；注释自认「转发已全删」却只补刷白名单、不披露 | `Err(-2, err.Error())` 裸返 |

回执路径直通 `Err(-2)`，运维只看到原始 DB 错误、不知道已删几条、数据面是否已下线。

## 2. 改法（与 R18-X-01 同款：fail-closed + 回执披露；已删项不回滚）

核心设计：清理函数直接返回已删清单，不事后重读库核对——清单来自执行清理的函数本身的追踪，nft 离线早返形返回 nil（库行仍保留、未删），无伪造已删数的可能；控制流与不回滚语义一律不动，只改失败返回的错误文本。

### 2.1 `go-backend/internal/http/handler/tunnel_delete_resolution.go`

- `:756` 新增 `formatForwardDeleteLabels`（「名(#ID)」标识串，「、」连接）；`:765` `formatDeletedForwardsSummary` 改用它实现，输出不变（「已删除 N 条转发：名(#ID)、…」，0 条时为「已删除 0 条转发」）。
- `:774` 新增 `formatMarkedForwardsSummary`（「已标记 N 条转发待删除：…」，0 条同理）。
- `:785` 新增 `formatTunnelDeleteFailureSummary(tunnelID, deleted)`：已删汇总为准；清理后库中仍残留的转发（如 nft 离线待清理形）另行注明「其余 N 条转发未删除、数据库记录仍保留：…」；残留核对读库失败时只报已删汇总、不虚构残留数。
- `:718-723` `cleanupTunnelForwardsDataPlane` 签名改为 `([]model.ForwardRecord, error)`，透传已删清单；注释补 R19-X-01 口径。
- `:794` `cleanupForwardsDataPlane` 签名改为 `([]model.ForwardRecord, error)`：
  - `:814` N-2 Mark 失败：`转发 %s 标记待删除失败，%s未删除：%w；%s；该转发未标记、规则未下线`（`%s` 为 `formatMarkedForwardsSummary(marked)`）。reconcile 未跑，措辞与「已删」严格区分。
  - `:840` N-1 nft 删库失败：`转发 %s 数据库删除失败，%s未删除：%w；%s；该转发规则已下线、数据库记录仍保留`；其中已删汇总在 >0 时后缀「（规则已下线、库行已删）」，0 条时不加（无已删项可指称）。既有 D6 日志行保留。
  - gost 分支三处 R18 返回点（`:857` 离线删库、`:863` 数据面、`:868` 删库）只改签名适配（`return deleted, …`），文案一字不动。
  - reconcile 失败点（`:825`）与离线早返（`:823`）不动：前者已有计数披露，后者返回 nil（未删）。
- `:875-891` `deleteTunnelAndCleanup`：D-1 改为 `隧道删除失败：%w；%s；隧道记录仍保留`（`%s` 为 `formatTunnelDeleteFailureSummary(tunnelID, deleted)`）。经此函数者（delete-with-forwards `:391/:404`、replace `:465`）同受覆盖，错误文本原样透传。

### 2.2 `go-backend/internal/http/handler/mutations.go`

- `:2590` `tunnelDelete`：取清理返回的 `deletedForwards`；`:2608-2613` D-2 改为 `隧道删除失败：%w；%s；隧道记录仍保留` 后再 `Err(-2, …)`，白名单补刷顺序不变（先补刷、再回执）。
- `:2691/:2704-2708` `tunnelBatchDelete`：同型点一并修复（与单删同病同治；L2 未点名、属「修 A 漏 B」纪律内同型面），失败明细 Reason 改为同款包装。
- `:510` 删用户路径：仅签名适配（`_, err := h.cleanupForwardsDataPlane(…)`），行为与文案不动。

### 2.3 `go-backend/internal/http/handler/r18_f01_tunnel_delete_disclosure_test.go:389`

- R18 测试直接调用点仅签名适配（`_, err = h.cleanupForwardsDataPlane(…)`），断言不动。

### 2.4 新回归测试 `go-backend/internal/http/handler/r19_f01_tunnel_delete_disclosure_residual_test.go`

注入手法：SQLite 触发器精确定位到第 N 条（R18 DROP TABLE 手法的同类注入）——`BEFORE DELETE ON "forward"` 仅拦目标行、`BEFORE INSERT ON "nft_pending_delete"` 仅拦目标标记、`BEFORE DELETE ON "tunnel"` 仅拦隧道行；其余行正常删除。8 个测试：

| 测试 | 覆盖点 | 断言要点 |
|---|---|---|
| `TestR19F01NftDbDeleteFailureDisclosesDeleted` | N-1 中途（2/3） | 「已删除 1 条转发」+ 第 1 条标识 + 「规则已下线」「库行已删」「数据库记录仍保留」；reconcile 恰 1 次；第 1 条已删、后两条与隧道保留 |
| `TestR19F01NftDbDeleteFailureFirstForwardDisclosesZero` | N-1 首条（1/3） | 「已删除 0 条转发」+ 「规则已下线」「数据库记录仍保留」；全部保留 |
| `TestR19F01NftMarkPendingFailureDisclosesMarked` | N-2（2/3） | 「已标记 1 条转发待删除」+ 第 1 条标识 + 「规则未下线」；reconcile 0 次；第 1 条 status=pending-delete |
| `TestR19F01DeleteTunnelByIDFailureDisclosesDeleted` | D-2（HTTP 单删） | 「已删除 1 条转发」+ 标识 + 「隧道记录仍保留」；转发已删、隧道保留 |
| `TestR19F01DeleteTunnelAndCleanupFailureDisclosesDeleted` | D-1（直接调用） | 错误文本本身含已删披露；转发已删、隧道保留 |
| `TestR19F01BatchDeleteTunnelByIDFailureDisclosesDeleted` | 批量同型点 | 失败明细 Reason 含已删披露；转发已删、隧道保留 |
| `TestR19F01NftSuccessPathUnchanged` | nft 成功路径 | 接口成功、隧道与全部转发均删、reconcile 恰 1 次 |
| `TestR19F01GostSuccessPathUnchanged` | gost 成功路径 | 接口成功、隧道与转发均删 |

## 3. 回归：先红后绿

### 3.1 红态（修复前，`r19f01-red.log`，11774 字节）

`go test ./internal/http/handler/ -run 'TestR19F01' -v -count=1`：**6 FAIL / 2 PASS**。6 个失败形回执均为裸触发器错误、无任何已删披露（确为本缺陷，非脚手架问题）：

- nft 中途：`msg="constraint failed: simulated nft db delete failure (1811)"`
- nft 首条：同上（`forward_id=1`）
- Mark：`msg="constraint failed: simulated mark pending failure (1811)"`
- 删隧道行（单删/直接调用/批量）：`msg="constraint failed: simulated tunnel row delete failure (1811)"`
- 2 个成功路径测试红态即 PASS（成功分支本就不动）。

### 3.2 绿态（修复后，`r19f01-green.log`，19270 字节）

同命令集加 R18 回归（`-run 'TestR19F01|TestR18F01'`）：**17/17 PASS**（新 8 项 + R18 9 项），26.698s。gofmt 注释空行修正后重跑定向集（`r19f01-green2.log`，166 字节）：exit 0。

### 3.3 全量

- `go build ./...` exit 0（`r19f01-build.log`，120 字节）；`go vet ./internal/http/handler/` exit 0（`r19f01-vet.log`，120 字节）。
- `go test ./... -count=1`（`r19f01-postfix-gotest.log`，427707 字节，739s）：除下述两项外全部 `ok`——`tests/contract` 由基线 7 FAIL 转为 `ok`（124.493s）；`internal/health` 仅剩已知环境红（见 §4）；`internal/http/handler` 首次跑触默认 10m 超时（见 §3.4）。
- `go test ./internal/http/handler/ -count=1 -timeout 30m`（`r19f01-postfix-handler.log`，167 字节）：**ok，704.442s，exit 0**。

### 3.4 handler 包 10m 超时说明（非产品失败）

首次全量中 handler 包 `panic: test timed out after 10m0s`（600.221s），超时瞬间在跑的测试为 `TestR3B11UserQuotaSustainedCrossingStillSuppressed (0s)`——刚启动、非挂死。基线 handler 包本身即 600.504s（贴线），本轮并行 lane 争用 + 新增 8 测（约 11s）致累计耗时越线。证据：该单测独立重跑 1.60s PASS（`r19f01-r3b11-single.log`，1288 字节）；放宽 `-timeout 30m` 后全包 `ok`（704.442s）。默认 10m 为测试框架报警上限，不是产品行为。

### 3.5 基线对照（`r19f01-baseline-gotest.log`，657084 字节）

修复前在同一 worktree 起的全量（exit 1，701s）：handler 包不含任何 `TestR19F01`（新测试文件在该包编译前尚未落盘，基线干净）。基线失败共 9 项，其中 8 项为 /tmp 磁盘满伪像（见 §4.2），1 项为已知环境红。

## 4. 残余红与未验证项

### 4.1 唯一命名失败：已知沙箱 fake-IP DNS 环境红（非产品）

`internal/health`：`TestValidateMonitorTCPTarget/公网域名放行`——`ValidateMonitorTCPTarget("example.com:443") err=目标地址不允许（内网/保留地址）: example.com`（`monitor_ssrf_test.go:39`）。沙箱 DNS 把公网域名解析到内网/保留地址所致，基线与修后签名一致，与本次改动无关（改动面不含 health/SSRF 判定）。

### 4.2 基线 8 项「database or disk is full」为环境伪像，已消除

基线中 `TestR2B10InboundRecoveryFailureResendsOnce` 与 contract 7 项（`TestExternalImportFromSubscriptionURLContract`、`TestKernelInboundLifecycleContract`、`TestKernelRejectsIneligibleNodesContract`、`TestNodeDeleteRemovesKernelsContract`、`TestInboundShareAndSubscriptionContract`、`TestInboundRelayValidationContract`、`TestInboundProxyProtocolAndWhitelistContract`）均报 `database or disk is full (13)`：当时 /tmp（512M tmpfs）被并发的 go-build 工作目录与测试临时库占满（96%）。修后全量改走 `TMPDIR=/home/hatch/tmp-r19f01 GOTMPDIR=/home/hatch/tmp-r19f01`（64G 空闲盘），contract 7 项全部转绿、R2B10 所在的 handler 包在 30m 超时下全绿。另：首次绿跑曾因同一原因链接失败（`link: mapping output file failed: no space left on device`），改走重定向 TMPDIR 后通过；/tmp 内其他 lane 的 go-build 目录未动。

### 4.3 未验证项（结转 R20 登记，不冒充结论）

- 真机验证未做：nft 分支删库失败与删隧道行失败均未上 Lightsail 测试机（沙箱 SQLite 触发器模拟 + fake manager reconcile，未走真实 SSH 下发）。
- 浏览器 DOM 腿未做：回执文案在前端的渲染未实点核查（后端 `Err(-2, msg)` 直通，前端原样展示既有口径）。
- `doc/guide/relay.md` 未同步：R19-L2 观察 O-01 建议在「删除隧道与节点离线时的语义」节补述在线真实失败时删除中止且回执披露已删转发——本文案属文档面，不属 F-01 产品改动，未动。
- 其余 R19 未验证项（SS 直连腿方法失效、远端/非 Debian 内核、kill-freeze K2、VLESS 1MB 10.21s 观察）与本项无关，继续结转。

## 5. 证据清单（均在 worktree `audit-out-132/r19/` 内落盘；`*.log` 依 `.gitignore:122` 不入 git，与 R18 审计日志同例）

| 文件 | 内容 | 字节 |
|---|---|---|
| `r19f01-red.log` | §3.1 红态：6 FAIL / 2 PASS | 11774 |
| `r19f01-green.log` | §3.2 绿态：17/17 PASS（-v） | 19270 |
| `r19f01-green2.log` | gofmt 后定向集重跑 exit 0 | 166 |
| `r19f01-baseline-gotest.log` | §3.5 修复前全量（exit 1） | 657084 |
| `r19f01-postfix-gotest.log` | §3.3 修后全量（除 health 环境红与 handler 超时外全 ok） | 427707 |
| `r19f01-postfix-handler.log` | §3.3 handler 全包 `-timeout 30m`：ok，704.442s | 167 |
| `r19f01-r3b11-single.log` | §3.4 超时点单测独立重跑 1.60s PASS | 1288 |
| `r19f01-build.log` | `go build ./...` exit 0 | 120 |
| `r19f01-vet.log` | `go vet ./internal/http/handler/` exit 0 | 120 |

## 6. 提交内容

分支 `fix-r19-f01`（自 `d66042c`），提交文件（逐项显式 add，未用 `git add -A`）：

| 文件 | 变化 |
|---|---|
| `go-backend/internal/http/handler/tunnel_delete_resolution.go` | +78/−21：签名改返回已删清单、新增 3 个汇总 helper、nft Mark/删库两点与 deleteTunnelByID 一点补披露 |
| `go-backend/internal/http/handler/mutations.go` | 单删 :2609、批量 :2697 补披露；删用户路径仅签名适配 |
| `go-backend/internal/http/handler/r18_f01_tunnel_delete_disclosure_test.go` | 1 行：直接调用点签名适配 |
| `go-backend/internal/http/handler/r19_f01_tunnel_delete_disclosure_residual_test.go` | 新增：8 个回归测试 |
| `audit-out-132/r19/R19-FIX-F01-REPORT.md` | 本报告 |

`git diff --stat`（报告与日志除外）：3 个产品/测试文件，93 insertions(+)，27 deletions(−)。
— R19-FIX-F01-REPORT END · 自证：本行之前正文字节数 13458 · END
