# R15 修复报告（fix-r15）

基线：main @ 5bd5a90。分支：`fix-r15`（独立 worktree `~/workspace/.fixr15`）。
修复提交：`583cec0`（R15-X-01 + R15-X-02 + 修复中发现的空指针守卫）。
归档提交：见 §4（V-06 三文件，本报告之后提交）。
未合 main、未打 tag、未发版、未改版本字面量。
依据：`audit-out-132/r15/R15-FIX-PLAN.md`、R15-X-ledger.md、R15-X-verdicts.md（V-01…V-08）。

## 1. R15-X-01：errors.md:15 的 500 行失实（文档 P3）

- 改动：`skills/flmx-api/references/errors.md:15`——从“Response Code Field”活码表删 `500` 行，表下加历史码注记：“`500` was once the parameter-error code. Since F-08, envelope parameter errors are unified under `-1` and `500` has no emission point.”（FIX-PLAN 二选一取“改写为现状”，删行 + 留历史注记，不留误导行。）
- 核对（全仓扫查，5bd5a90 树）：
  - envelope `code: 500` 发射点：0（`Err(500` / `code.*500` / `Code: 500` 全仓 0 命中）。
  - 其余八码均有发射点：`0`（`response.OK`）、`-1`（`ErrDefault` + 11 处显式 `Err(-1`）、`-2`（337 处）、`-3`（4 处）、`401` / `403` / `409`（`idempotency.go:238`）/ `429`（`handler.go:897`）。
  - raw `http.Error` 的 500（`kernel_relay.go:846`、`gh_mirror.go:321,327`）只带 HTTP 状态码、不带 envelope `code` 字段，与本表无关。
- 测试：`scripts/check-doc-consistency.sh` 重跑 `RESULT: PASS`（exit 0）。

## 2. R15-X-02：demo 夹具缺 backup/gist/status 录制（产品·演示面 P3）

- 根因：`config.tsx:2244` 管理员恒挂载 `<GistBackup/>`，挂载即调 `POST /backup/gist/status`；录制枚举（`record_pages.mjs` 走查 + `record_items.mjs` 直接调用）两处均未列该端点，1.4.6 Gist 落地时从未录入。
- 改动：
  - `vite-frontend/scripts/demo/record_items.mjs`：admin 册加直接调用 `POST backup/gist/status {}`（种子面板实录，非手工补值）。
  - `vite-frontend/scripts/demo/build_fixtures.py`：`CONTENT_KEYS` 加 `("admin", "backup/gist/status", "gist-status")`，新 kind 校验 code 0 + `GistBackupStatus` 八字段声明形状（`configured/tokenHint/encrypted/intervalHours/gistUrl/lastAt/lastUpload/lastError`，类型逐字段核对；bool/int 以 `isinstance(x, bool) != (t is bool)` 区分）。新种面板诚实作答 `configured=false`、空 tokenHint/gistUrl——空值即事实，故闸查形状不查非空。
  - `vite-frontend/scripts/demo/check-fixtures-content.mjs`：`CHECKS` 加同款 `gist-status`（两处闸口径逐项一致）。
  - `vite-frontend/src/demo/fixtures.json`：按 README 纪律从种子面板全量重录（空库后端 `DB_PATH=~/workspace/.tmp-demo/demo.db SERVER_ADDR=127.0.0.1:16370` + seed_1/2/3 + vite dev :3114 + `record_pages.mjs` + `record_items.mjs` + `build_fixtures.py`，种子面板与录制器均在本机沙箱，CHROMIUM 为锁定版 chromium-1187）。重录前后结构差分：admin 66→67 路（唯一新增即 `backup/gist/status`）、user 47→47；其余“增减”均为 monitor 时间窗口键随录制时间整体后移（README 既定行为）；共享路径录制条数零变化；无一键被洗空。
- 先红后绿（证据日志落盘本目录）：
  - 红 1（旧夹具）：`R15-demo-red1.log`（138 字节），exit 1，唯一 FAIL 逐字为 `FAIL admin #/config: [demo] not recorded: backup/gist/status`（与 R15 立案逐字一致）。
  - 红 2（新夹具 + 旧前端，见 §3）：`R15-demo-red2.log`（1,139 字节），exit 1，20 problems。
  - 绿（新夹具 + 守卫修复）：`R15-demo-green.log`（33 字节），exit 0，`demo ok: 14 admin + 5 user pages`（14 含 R14 A10-13 补的 change-password；FIX-PLAN 的“13”系旧数，以 check.mjs 枚举为准）。
- 静态闸：`check-shape-drift.mjs` exit 0（261 recorded objects）；`check-fixtures-content.mjs` exit 0（含新行 `ok admin "backup/gist/status" recording #0: code 0, GistBackupStatus shape ok (configured=false)`）；`build_fixtures.py` 写盘时 code gate + content gate 双过。

## 3. 修复中发现的新产品缺陷：外部入站 probe=null 致入站页空白（待主控编号）

- 发现：§2 红 2 的 20 problems 首因是 `admin #/inbound: page error: TypeError: Cannot read properties of null (reading 'ok')`，其后全部页面 blank（SPA 崩溃连带）。与 gist 缺口无关，是重录暴露的真缺陷。
- 机理（文件行号）：
  - 后端 `go-backend/internal/http/handler/node_latency.go:460-484` `inboundProbeView`：外部入站（`isExternalInbound`）在 `h.externalProbes` 无条目时返回 nil；首轮探测（`runExternalProbeLoop` 启动即跑一轮，但 dial + 延迟 + 路由全轮耗时数分钟）未完成前，`inbound/list` 的外部入站 probe 即为 null。实测：重录时走查到 /inbound 约在面板启动后 2–3 分钟，首轮未完成；面板启动 10 分钟后复查，同一批外部入站 probe 已为 `{ok:true, latencyMs:1–2, checkedAt}`。
  - 前端 `vite-frontend/src/pages/inbound.tsx:562-564`（修前）：`item.external && !item.probe?.skipped ? item.probe : undefined`——probe=null 时 `!null?.skipped` 为真，null 原样赋给 probe；随后 `probe !== undefined && !probe.ok`（null !== undefined 为真）读 `null.ok` 抛错。`renderInbound` 在 `Array.map` 内抛错，整页空白。生产面同样可达：任何新鲜面板 / 首轮探测未完成 / 探测轮失败时，有外部入站的管理員打开入站页即空白页。
- 修复（`inbound.tsx:562-570`，一处）：`item.external && item.probe && !item.probe.skipped ? item.probe : undefined`，null 与 undefined 同义为“暂无探测结果”。同文件其余两处（`:594`、`:827`）均以 `probe ? … : …` / `probe?.error` 守卫，无需另改；全仓同型 `probe?.skipped ?` 仅此一处（grep 核对）。`dashboard/admin-overview.tsx:311` 与 `latency-cell.tsx` 的 probePoints 均已守卫。
- 回归：§2 绿日志即回归证据（新夹具的外部入站 probe 仍为 null——新鲜面板诚实态——走查全过，空 probe 路径现由走查覆盖）。本缺陷未在 R15 立案中，**待主控编号入账**；修法已随 `583cec0` 落地。
- 未采纳的替代：等探测轮完成后再重录（会把 null 态藏回夹具、掩盖真缺陷并留下竞态）；在 `build_fixtures.py` 伪造外部 probe（违反 README“种子缺事实就补种子、不要在 fixtures 里手工补值销账”纪律）。

## 4. V-06 归档：三处未入库订正随本分支提交

逐项按 R15-L5-three-items.md:128–130 核对内容与裁定一致（字节数逐字相符，`cmp` 校验与共享工作区原件逐字节一致），随后 `git add` 明确文件归档：

| # | 文件 | 字节 | 内容核对 |
|---|---|---|---|
| 1 | `audit-out-132/port-3342/PORT-3342-REA-UDIT.md` | 7,061 | REA-P3-1 标题已为“可达，每配置间隔一次失败尝试、失败通知至多 1 条/30 分钟”；:34 溯源行在位（原标题及“enqueueNotify 无去重窗口”失实 + R14-A3 §A3-02 探针实证 + `notify.go:227 notifyDupWindow = 30 分钟`）；机理段已改写。 |
| 2 | `audit-out-132/r14/R14-00-routes-raw.csv` | 25,543 | 第 60 行 idx 60 `/api/v1/open_api/gh/` 与第 109 行 idx 109 `/api/v1/open_api/sub/` 的 `method_guard` 均为 `GET,HEAD`；全表 203 行（含表头）。 |
| 3 | `audit-out-132/r14/R14-00-baseline.md` | 31,788 | :38 方法守卫分布行已为 `POST only 131 / ANY 53 / GET only 13 / not(Get,Post) 1 / GET,HEAD 2 / GET,POST 1 / GET (WS upgrade) 1`（合计 202），附 `method_guard` 列统一记允许方法集注记。 |

三文件在 `5bd5a90` 树内均不存在（`git ls-files` 0 命中、`git show 5bd5a90:` fatal，与 V-06 一致），本次以新文件归档，无其它改动夹带。归档提交：`b54b44f`（`docs(r15): archive V-06 verdict corrections`）。

## 5. 回归证据（修后全绿）

- `go build ./...` exit 0；`go vet ./...` exit 0（仅 GOPATH/GOROOT 同目录无害警告）。
- `go test ./... -count=1`（go-backend 全集）：handler 367.335s ok、kernel ok、repo 109.952s ok、contract 45.083s ok；**产品失败 0**。唯一 FAIL 为已知沙箱环境红 `internal/health TestValidateMonitorTCPTarget/公网域名放行`（fake-IP DNS 致 example.com 落 198.18/15，CI 真实 DNS 下绿，与历轮同签名）。
- 前端 `tsc --noEmit` exit 0；`vite build`（demo 构建两次）exit 0。
- `check-doc-consistency.sh` PASS（exit 0）；demo 两静态闸 exit 0；走查 exit 0（§2）。
- 本次改动无后端产品代码（仅文档/演示脚本/夹具/前端一行守卫），无 BC。

## 6. 未验证项（不冒充）

- 浏览器 DOM 鼠标实点未做（走查为程序化页面访问 + 2 交互步，未做人工点击）。
- 真实 Gist 成功上传/下载往返未验证（无真实令牌，未索取；沿袭）。
- 远端节点与非 Debian 内核未验证（沿袭）。
- 种子面板与录制均在本机沙箱完成（README 纪律要求种子面板，测试机未参与本次修复；真机行为留 R16）。

R15-FIX-REPORT END
