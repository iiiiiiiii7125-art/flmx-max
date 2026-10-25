# R18 F-01 修复报告（R18-X-01）

基线：main @ a0c18a0（`a0c18a0dc90b2f9c7a67e8a2a016cc1d97505fb5`）。
分支：`fix-r18-f01`（独立 worktree `~/workspace/.fixr18-f01`，自 main @ a0c18a0 切出）。
范围：只修 F-01，不合 main、不打 tag、不发版；未改版本字面量；未碰测试机。

## 1. 缺陷（R18-X-01，产品 P3）

`cleanupForwardsDataPlane` gost 分支逐条先清数据面（`controlForwardServices` DeleteService）后删库（`deleteForwardByID`），中途硬失败时回执只称「隧道未删除」，已删转发数与标识不披露。已删项不回滚（现有语义），只缺披露。

## 2. 改动

### 2.1 产品文件（1 个，+36/−3）

`go-backend/internal/http/handler/tunnel_delete_resolution.go`（842 行）：

| 行 | 改动 |
|---|---|
| 731–755 | 新增 `formatForwardDeleteLabel`（:736，`名(#ID)`，无名只标 `#ID`）与 `formatDeletedForwardsSummary`（:745，恒含已删数，>0 附标识；0 条时为 `已删除 0 条转发`） |
| 794 | gost 循环前新增 `deleted []model.ForwardRecord`，只记录已完成「数据面 + 删库」两步的转发 |
| 805 | 离线分支删库失败：裸 `return delErr` 改为披露式错误——`转发 <label> 数据库删除失败，<scope>未删除：<err>；<已删汇总>；该转发数据面未清理（节点离线，待重连对账）` |
| 811 | 数据面硬失败：`转发 %s 数据面清理失败，%s未删除: %w`（仅失败转发名）改为 `转发 <label> 数据面清理失败，<scope>未删除：<err>；<已删汇总>` |
| 816 | 删库失败：裸 `return err` 改为披露式错误——`转发 <label> 数据库删除失败，<scope>未删除：<err>；<已删汇总>；该转发数据面服务已清理、数据库记录仍保留` |

nft 分支未动（见 §7 观察项）。

### 2.2 语义不变点

- 离线/不可达类仍按 X-P1-1 放行删库（`offlineTolerant && isNodeUnreachableError` 分支结构未动；删库成功后 `continue` 不变）。
- 在线真实失败仍中止删隧道/删用户（控制流未动，只改错误文本）。
- 已删项不回滚（与现有不回滚语义一致），只补披露；`deleted` 追加点在两步都成功之后，当前条失败时不计入已删。
- `scope` 参数（"隧道"/"用户"）与 `offlineTolerant` 区分原样保留，两路径共用同一披露构造。

## 3. 调用链追踪：错误到达 HTTP 回执（无调用方改动）

| 调用方 | 回执路径 | 证据 |
|---|---|---|
| `tunnelDelete`（mutations.go:2590） | `response.Err(-2, err.Error())` | 回归测试 HTTP 断言（§4） |
| `tunnelBatchDelete`（mutations.go:2686） | `appendBatchFailure` → `Reason = err.Error()`（mutations.go:2867–2876，原样透传） | 回归测试 HTTP 断言（§4） |
| `tunnelDeleteWithForwards` → `processTunnelDeleteWithForwards`（tunnel_delete_resolution.go:391/404/465） | `response.Err(-2, err.Error())` | 代码追踪；与单删同 `Err(-2)` 口径 |
| `tunnelBatchDeleteWithForwards` | `appendBatchFailure(..., processErr)`，同上 | 代码追踪；与批量单删同函数 |
| `userDelete`（mutations.go:510） | `response.Err(-2, err.Error())` | 回归测试 HTTP 断言（§4） |

调用方无需补披露：错误文本已原样进回执 `msg` / 批量 `failures[].reason`。

实测回执（中途数据面失败，N=2/3）：

```json
{"code":-2,"msg":"转发 r18f01-b(#2) 数据面清理失败，隧道未删除：simulated real failure: permission denied；已删除 1 条转发：r18f01-a(#1)"}
```

## 4. 回归测试（先红后绿）

新文件 `go-backend/internal/http/handler/r18_f01_tunnel_delete_disclosure_test.go`（468 行，9 个测试）。

测试手段：远端 gost 节点以 httptest 伪装——解析 federation 命令体 `services` 名（`<forwardID>_<userID>_<userTunnelID>`）按转发 ID 决定 DeleteService 成功/在线真实失败；删库腿以「处理第 N 条转发的 DeleteService 时删 `nft_counter_state` 表」模拟循环中途数据库硬失败。远端为回环地址，须 `t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")`（federation 传输默认拦截私网目标；与 secure_fetch 测试同口径）。

先红（修复前，`go test ./internal/http/handler/ -run 'TestR18F01' -count=1`）：**6 FAIL / 1 PASS**。失败样本：`msg="转发 r18f01-b 数据面清理失败，隧道未删除: simulated real failure: permission denied"`——恰为 R18-X-01 原缺陷（r18f01-a 已删、零披露）。通过的 1 项为成功路径守卫（行为本就正确）。

修后同命令：**9/9 PASS**：

| 测试 | 覆盖 |
|---|---|
| `TestR18F01DeletedForwardsSummaryFormat`（:169） | 汇总格式：0 条 / 多条 `名(#ID)` / 无名 `#ID` |
| `TestR18F01MidLoopDataPlaneFailureDisclosesDeleted`（:184） | 第 2 条数据面失败（N=2/3）：回执含 `已删除 1 条转发` + 第 1 条标识 + 失败转发名；第 1 条库行已删，第 2/3 条与隧道保留 |
| `TestR18F01LastForwardDataPlaneFailureDisclosesDeleted`（:216） | 最后一条失败（N=3/3）：回执含 `已删除 2 条转发` + 前两条标识；前两条已删 |
| `TestR18F01FirstForwardDataPlaneFailureDisclosesZeroDeleted`（:247） | 第 1 条即失败（N=1，零已删）：回执含 `已删除 0 条转发`；全部保留 |
| `TestR18F01MidLoopDbDeleteFailureDisclosesDeleted`（:277） | 第 2 条数据面已清、删库硬失败：回执含 `已删除 1 条转发` + 第 1 条标识 + `数据库记录仍保留`；第 1 条已删、第 2 条库行保留 |
| `TestR18F01BatchDeleteFailureReasonDisclosesDeleted`（:310） | 批量删隧道同一失败：`failures[0].reason` 含已删披露 |
| `TestR18F01SuccessPathUnchanged`（:352） | 全删成功路径不变：code 0，隧道与全部转发删除 |
| `TestR18F01OfflineBranchDbDeleteFailureDisclosesDeleted`（:376） | 离线分支删库硬失败（本地离线节点 + 删表）：错误含 `已删除 0 条转发` + 失败转发名；转发与隧道保留 |
| `TestR18F01UserDeletePathDisclosesDeleted`（:405） | 删用户路径复用：回执 scope 为 `用户未删除`，含 `已删除 1 条转发` + 第 1 条标识；用户行保留 |

## 5. 全量验证

工作 tree `~/workspace/.fixr18-f01`，Go 1.25.1（`~/go/bin/go`）：

- `go build ./...`（go-backend）：干净。
- `go vet ./...`（go-backend）：干净，0 发现。
- `go test ./... -count=1`（go-backend，约 6 分钟）：`internal/http/handler` **ok（327.5s，含本轮 9 个新测试与全部既有测试）**；其余包全部 ok；唯一 FAIL 为 `internal/health` 的 `TestValidateMonitorTCPTarget/公网域名放行`——**与本次修复无关的既有环境失败**：沙箱假 DNS 把 `example.com` 解析到 `198.18.148.124`（198.18/15 CGNAT 段，判为非公网地址）；在未改动的 main checkout（`~/workspace/flvx-max` @ a99d68d）同命令复现同一失败。本次改动文件只涉及 handler 包，产品失败 0。

## 6. 交付

- 分支 `fix-r18-f01`（worktree `~/workspace/.fixr18-f01`，基于 main @ a0c18a0；未合 main、未打 tag、未发版）。
- 提交：本报告落盘后提交（`git add` 仅以下两个明确文件，禁用 `git add -A`）：
  - `go-backend/internal/http/handler/tunnel_delete_resolution.go`
  - `go-backend/internal/http/handler/r18_f01_tunnel_delete_disclosure_test.go`
- 本报告：`audit-out-132/r18/R18-FIX-F01-REPORT.md`（worktree 内）。

## 7. 未验证项与观察（留 R19）

1. **真机验证未做（按任务约束）**：测试机 gost 节点实删、离线重连对账等腿一律留 R19 独占窗口；本次未上测试机。本地回归均为沙箱 SQLite + httptest 伪装远端。
2. **nft 分支同类缺口未修（作用域外）**：`cleanupForwardsDataPlane` nft 分支删库失败（原 :760–765）仍返回裸 `err`，未披露已删数；reconcile 失败路径本身已有计数披露（`节点不可达，%d 条转发已加入待清理队列`）。是否立项由 R19 裁定。
3. **`deleteTunnelByID` 失败点同类缺口未修（作用域外）**：`deleteTunnelAndCleanup` 末尾删隧道行失败时，全部转发已删、回执未披露「转发已全删」；在 F-01 对象（`cleanupForwardsDataPlane` gost 分支）之外，未改，留 R19 裁定。
4. **附带观察（不立案，仅记录）**：既有测试 `TestR1B3TunnelDeleteOnlineRealFailureAborts` 未设 `FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true`，其远端调用在 federation 传输层即被 SSRF 拦截（`resolves to private/reserved address 127.0.0.1, blocked`），并未真正走到伪装节点的 `success=false` 分支；该测试仅断言中止 + 行保留，故仍通过，但并未实际覆盖「远端明确回失败」路径。R19 可考虑为其补放行 env。
5. R17 修复报告 §10 剩余未验证项（远端/非 Debian 内核、浏览器 DOM 腿、订阅流量计数器响应性）继续结转 R19 登记（按 R18-FIX-PLAN）。

— R18-FIX-F01-REPORT END · 自证：本行之前正文字节数 8970 · END
