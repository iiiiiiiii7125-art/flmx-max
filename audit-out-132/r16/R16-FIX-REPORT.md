# R16 修复报告（FIX-REPORT）

基线：main @ b54b44f。分支：`fix-r16`（独立 worktree `~/workspace/.fixr16`，自 main 切出）。**未合 main、未打 tag、未发版、未改版本字面量。** 修复按 R16-FIX-PLAN（F-01…F-03）顺序执行，三项全部完成；R15-FIX-01 的第二位点随 F-02 一并闭合（见 §F-02 末）。

## F-01：userList 分页乘法溢出（R16-X-02，产品 P3）

- 对象：`go-backend/internal/http/handler/handler.go:740`，`start := (req.Current - 1) * req.Size` 与 `end := start + req.Size` 均无上溢防护。
- 改动（乘前判溢出，正常分页语义不变）：`math` 导入 +1；`start` 先判 `req.Current-1 <= math.MaxInt64/req.Size`，溢出即空页（`start = len(users)`）；`end` 改为 `req.Size <= len(users)-start` 时才取 `start + req.Size`，否则取 `len(users)`。未传分页参数（current/size 为 0）仍走旧全量行为。
- 同型核对：全仓 `(Current-1)*Size` 及同类分页乘法搜索仅此一处（`handler.go:740`）；无第二处。
- 先红后绿（新测试 `go-backend/internal/http/handler/userlist_pagination_test.go`，`TestR16X02UserListPaginationOverflow`；形状逐字取自 R16-L3-sentinel-a2.md:196-198）：
  - 修复前：溢出形 1 `{"current":4611686018427387904,"size":3}` panic（`slice bounds out of range [:-4611686018427387904]`），测试 FAIL。
  - 修复后：两溢出形（另 `{"current":2,"size":9223372036854775807}`）均无 panic、code 0、空页；两对照（`{"current":1,"size":10}` 得 1 条、`{"current":2147483648,"size":2147483648}` 空页）仍 code 0；正常分页（第二页为空）不变。测试 PASS（0.30s）。
- `gofmt` 干净（初版注释空行已按 gofmt 修过）。

## F-02：入站页 sheetProbe 空指针（R16-X-01，产品·演示面 P3）

- 对象：`vite-frontend/src/pages/inbound.tsx:1020-1024`，`sheetProbe` 与修复前 renderInbound 旧式逐字同构；probe=null 时 `!sheetItem.probe?.skipped` 为真，把 null 原样赋给 sheetProbe，随后 `sheetProbe !== undefined && !sheetProbe.ok` 读 null.ok，详情页崩溃。
- 改动（与 renderInbound :566-568 同形）：`sheetItem?.external && sheetItem.probe && !sheetItem.probe.skipped ? sheetItem.probe : undefined`，null 与 undefined 同义为“暂无探测结果”；附注释说明旧式失效机理。
- 先红后绿（点击腿，复用 R16 探针 `/tmp/r16l1/sheet-click.mjs`，chromium-1187，`VITE_DEMO=true` 构建 `dist-demo`）：
  - 红（修复前构建）：点击 `机场 · 台湾 01` 卡片后页面文本 1163 → 0，1 条异常 `TypeError: Cannot read properties of null (reading 'ok')`，`RESULT: SHEET-CRASH-REPRODUCED`，exit 1（日志 `audit-out-132/r16/R16-FIX-F02-red.log`，284 字节，与 R16 Run D 逐字一致）。
  - 绿（修复后重建）：点击后页面文本 1292（sheet 正常渲染），0 异常，`RESULT: NO-CRASH`，exit 0（日志 `audit-out-132/r16/R16-FIX-F02-green.log`，615 字节）。
  - 前端 `tsc --noEmit` exit 0；`vite build` exit 0（2.42s）。
- 同型再搜索：全仓 `probe?.skipped` 命中只剩守卫式（`item.probe && !...`、`sheetItem.probe && !...`、`i.probe && !...`、`!probe || probe.skipped`）与注释；`!x.probe?.skipped` 旧式代码残留 0；两处 `.ok` 消费点（renderInbound :570、sheetProbe :1028）均只收 undefined 或真实 probe。**全仓同型仅此两处，均已守卫。R15-FIX-01 关闭条件至此成立**（renderInbound 腿 R15 已修 + sheetProbe 腿本轮修；正式关闭由 R17 关闭验证裁定）。
- 注：AGENTS.md 规定不加前端测试框架；本次以 CDP 点击探针（红→绿）作为点击腿证据，非单元测试。

## F-03：inboundProbeView 死代码（R16-X-03，产品 P3）

- 对象：`go-backend/internal/http/handler/node_latency.go:460-484`，生产调用方为 0，仅测试引用；注释“is what the inbound list shows”失实（实际列表走 `kernel.go:716-718`，直接读 `externalProbes`）。
- 改动（主控定：删除）：删除函数及注释（28 行）；同步删除仅测该函数的 `TestInboundProbeViewUsesNodeLatency`（`node_latency_test.go`，33 行；其余断言一字未动）；删除后 `model` 导入闲置，一并清理。`latencyProbe` 仍被 `node_latency.go:422` 与 `relay_latency.go:225` 使用，`isExternalInbound` 仍被 `kernel.go` 等多处使用，`withRoutes` 仍被三处使用——均未动。
- 验证：全仓 `inboundProbeView` 引用 0；`go build ./...`、`go vet`（handler/kernel/store）、`gofmt`（4 个改动文件）全净；`TestLatency*`、`TestR16X02*`、`TestNodeListAttachesLatency` 全过。

## 回归（修复后最终态，产品失败 0）

| 项 | 结果 |
|---|---|
| `go build ./...`（go-backend） | exit 0 |
| `go vet`（kernel + store + handler） | exit 0 |
| `go test ./internal/kernel/` | ok 0.154s |
| `go test ./internal/store/repo/` | ok 106.809s |
| `go test ./internal/http/handler/ -count=1` | **ok 365.348s**（982 个顶层测试列出，FAIL 0） |
| 前端 `tsc --noEmit` | exit 0 |
| 前端 `vite build`（demo） | exit 0 |
| `gofmt -l`（4 个改动文件） | 干净 |

注：`gofmt -l` 全仓另有 14 个既有文件未对齐（control_plane.go、kernel.go、monitoring.go、nftables_runtime.go 等），均为 b54b44f 既存、本次未动文件，不属本次改动范围，未重排。

## 归档（随本次提交进库）

- `audit-out-132/r15/R15-FIX-REPORT.md`：9,164 字节，sha256 `fda006d9d0e18fb5ae6a96d36902c90aba7f1d368f7aeddf2df0b2982fb761da`，与 R16-X-ledger §1 逐字一致，内容未改写。**其 §3“全仓同型仅此一处”结论已作废**（R16-X-verdicts V-04），作废声明以 R16-X-verdicts 为准；文件本身原文留存。
- `audit-out-132/r16/`：52 个文件，原样复制自共享工作树，`diff -r` 逐文件一致；五路主报告字节数与 R16-X-ledger §6 一一对应（L1 20,489 / L2 8,906 / L3 31,313 / L4 30,010 / L5 18,290；R16-00-sentinel.md 26,623 = 26,287 + R16 前言）；三份尾行 END 在位（X-ledger、X-verdicts、FIX-PLAN）。其中 35 个 `*.log` 探针日志受 `.gitignore:122` 的 `*.log` 通配规则影响，以 `git add -f` 逐文件显式入库（非 `git add -A`），否则归档将缺失 N-37 裁定依据的原始日志。
- 本次新增证据：`audit-out-132/r16/R16-FIX-F02-red.log`（284 字节）、`R16-FIX-F02-green.log`（615 字节）、本报告。
- 未夹带无关改动：提交文件集 = 4 个产品/测试改动文件 + 1 个新测试文件 + 上述归档文件；`dist-demo/` 与 `node_modules/` 均在 `.gitignore` 内，未入库。

## 未验证项（不冒充）

- 生产面板 DOM 鼠标实点：本次只做到 demo 构建 + CDP 点击探针（点击腿已覆盖 sheet 打开一步）；生产环境鼠标实点仍须父代理侧浏览器任务。
- PG 契约测试：本地无 DSN（与 R16 同口径，3 SKIP 沿袭）；CI 侧覆盖。
- 远端节点与非 Debian 内核：未覆盖。
- VMess/SS/Trojan 计时腿重跑：不属本计划执行项（R16-X-ledger §5 登记，留待发版门或专项验证）。

R16-FIX-REPORT END
