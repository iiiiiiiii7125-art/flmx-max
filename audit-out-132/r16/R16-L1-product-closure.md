# R16-L1 产品项关闭验证报告（只读审计）

- **基线（pin 死）**：`main @ b54b44f`，完整提交 `b54b44f772d8b4efdd6d08b1fbbbc40c8091a143`（`docs(r15): archive V-06 verdict corrections (were working-tree-only)`，2026-10-24 21:55:16 +0000）。
- **审计对象**：只读读数树 `~/workspace/.r16-tree @ b54b44f`（detached HEAD，全程 0 脏文件、永不修改）；构建与走查在隔离 worktree `~/workspace/.r16-l1 @ b54b44f` 与 `~/workspace/.r16-l1-pre @ 583cec0^`（= `5bd5a90`）内跑，跑后恢复干净。共享工作树（`feat-latency-hint @ a99d68d`）全程未碰。
- **修复提交**：`583cec0`（`fix(r15): errors.md stale 500 row; demo fixture missing backup/gist/status`，6 文件，`+79/−3`）。本路关闭验证以该提交本身 + 本轮新探针证据为准。
- **方法**：三项逐项对探针对象本身重验，全部取本轮新证据，不继承 R15 结论、不引用旧摘要计数。裁定只认落盘原始日志 + 名单存档（N-37）；销账对账探针对象本身，名称/对象/断言三对齐（N-35）。
- **结论**：**CLOSE 2 / OPEN 1 / 未验证 0**。R15-X-01 与 R15-X-02 关闭；R15-FIX-01 因同型搜索发现第二处未修复位点（L1-CAND-01）而 OPEN——其 `renderInbound` 腿已闭合，`sheet` 腿未修且已实测复现崩溃。

## 0. 前提前修正：R15-FIX-REPORT.md 的存在性（任务前提不完整）

- 任务书称该文件“在工作树与全部历史中均不存在（协调者已查）”。本轮核查：**该表述对共享工作树与全部历史成立，但不完整**——文件以**未入库**形态存在于修复 worktree `~/workspace/.fixr15/audit-out-132/r15/R15-FIX-REPORT.md`（9,164 字节，mtime 2026-10-24 21:55:19 UTC，即 `b54b44f` 提交后 3 秒；`git status` 为 `??`，`git ls-files` 无记录）。
- 核对：`git show b54b44f:audit-out-132/r15/R15-FIX-REPORT.md` → `fatal: path ... does not exist in 'b54b44f'`；`git log --all --diff-filter=A -- "*R15-FIX-REPORT*"` 为空（从未入库）；共享工作树 `audit-out-132/r15/` 内无此文件；只读树 `.r16-tree` 内无此文件。
- **处置**：按任务书要求，本路三项裁定均不以该文件为准（本报告所有结论的证据链均为 `583cec0` 提交内容 + 本轮新探针落盘日志）。另记一笔：该文件 §3 的“全仓同型仅此一处”结论与本轮探针结果不一致（见 L1-CAND-01），以本轮探针为准；该文件本身的去留（入库归档或删除）请协调者裁定。

## 1. 逐项关闭表

| 项 | 级别 | 结论 | 新证据（文件行号 + 实测，行号均为 b54b44f 树内） |
|---|---|---|---|
| R15-X-01 | 文档 P3 | **CLOSE** | §2.1：`errors.md:15` 500 行已删、历史注记在 `:16`；全仓 envelope `code: 500` 发射点 = 0（含测试文件；唯一命中为历史审计散文，非发射点）；其余八码逐码有发射点（`0/-1/-2/-3/401/403/409/429`，计数见 §2.1）；`check-doc-consistency.sh` 重跑 `RESULT: PASS` exit 0 |
| R15-X-02 | 产品·演示面 P3 | **CLOSE** | §2.2：`record_items.mjs:24` 含直接调用；两内容闸含 `gist-status` 形状键；夹具重录取证（80 键变、159/159 条目 ts 全变、唯一非监控新增键即 `backup/gist/status`）；本轮走查 Run A exit 0（`demo ok: 14 admin + 5 user pages`），修复前复现 Run C exit 1、唯一 FAIL 逐字相同 |
| R15-FIX-01 | 修复中发现的新问题（入站页空指针） | **OPEN** | §2.3：`renderInbound` 腿已闭合（修复在位 + 走查敏感性探针 Run B 复现 20 problems）；但同型搜索发现第二处未修复位点 `inbound.tsx:1020-1024`（`sheetProbe`），点击探针 Run D 已实测复现同签名崩溃（L1-CAND-01）。关闭条件“确认仅此一处”不成立，故整项 OPEN |

## 2. 分项新证据

### 2.1 R15-X-01（文档 P3）— errors.md 500 行失实：CLOSE

- **删行与注记**（`skills/flmx-api/references/errors.md`，b54b44f 树直读）：修复前（`583cec0^`）`:15` 为 `` | `500` | Parameter error (legacy; same class as `-1` elsewhere) | Check request body format | ``；现 `:15` 为空行，`:16` 为历史码注记，全文如下：`> Historical code: \`500\` was once the parameter-error code. Since F-08, envelope parameter errors are unified under \`-1\` and \`500\` has no emission point.` 活码表其余八行（`0/-1/-2/-3/401/403/409/429`）逐字在位；文件内 `500` 唯一命中即该注记行。
- **全仓 envelope `code: 500` 发射点 = 0**（`R16-L1-demo-b54b44f.log` 的同批探针与 `p03-code500-fullscan.log`）：模式 `Err(500|Code: 500|"code": 500|code: 500` 扫 `*.go/*.ts/*.tsx/*.mjs/*.js/*.md`（含测试文件，排除 `node_modules`/`dist`）仅 1 命中——`doc/audit/J-api.md:66`，J-008 历史审计散文（“参数错误返回 `Err(500)`，业务失败返回 `Err(-2)`，同一接口混用两种错误码体系”，末触 `a9a3116`），描述的是已被 F-08 统一的历史现象，非发射点，排除有据。`Err(http.StatusInternalServerError` 作 envelope 码：0 命中；`Err(500|Code: 500` 含测试文件：0 命中。`response.Err`（`go-backend/internal/http/response/r.go:33-35`）为唯一 envelope 码构造器（`ErrDefault` 即 `Err(-1, …)`，`:41-43`），故该扫查覆盖全部发射面。
- **FIX-PLAN 注明的三处 raw `http.Error` 逐一核对排除**：`go-backend/internal/http/handler/kernel_relay.go:846`（`http.Error(w, "internal error", http.StatusInternalServerError)`）、`gh_mirror.go:321` 与 `:327`（两处均为 `http.Error(w, "cache read failed", http.StatusInternalServerError)`）。三处只带 HTTP 状态码、不写 JSON envelope、不带 `code` 字段；全仓 `StatusInternalServerError` 非测试命中仅此 3 处。其余 `500` 字面量均为注释、端口、超时、截断长度等（`p03` 日志 §C/D），无一为 envelope 码。
- **其余八码逐码有发射点**（非测试 Go 代码计数，`Err(c,` + `R{Code: c,` 双口径）：`0`——`R{Code: 0` ×24（另有 `response.OK/OKEmpty` 构造器，`r.go:17-30`）；`-1`——`Err(-1` ×11、`R{Code: -1` ×3，另 `ErrDefault` ×704（均归一为 `-1`）；`-2`——`Err(-2` ×337、`R{Code: -2` ×1；`-3`——`Err(-3` ×4（`monitoring.go:490/:501/:660` 起）；`401`——×42（首见 `federation.go:1161/:1167/:1181`）；`403`——×45（首见 `config_access.go:31`）；`409`——×1（`idempotency.go:238`“相同请求正在处理中，请稍后重试”）；`429`——×3（`handler.go:897/:1200`、`login_rate_limit.go:367`）。八码全部 ≥1，九码表与实现一致。
- **`scripts/check-doc-consistency.sh` 重跑全绿**（只读脚本，`.r16-tree` 内实跑，`p02-doc-consistency.log`）：7 行 OK（XHTTP 两行、技能包覆盖 197 条 `/api/v1` 路由、版本三处一致 `1.4.8`、安装菜单 6 项、Caddy 反代 `127.0.0.1`、默认账号一致）+ `RESULT: PASS（known-pending 项不计失败）`，exit 0。

### 2.2 R15-X-02（产品·演示面 P3）— demo 夹具缺 backup/gist/status 录制：CLOSE

- **录制调用在位**（`vite-frontend/scripts/demo/record_items.mjs:19-24`）：`// R15-X-02: the config page mounts <GistBackup/> unconditionally for admins and calls backup/gist/status on mount; …` 后接 `await call(tok, "POST", "backup/gist/status", {});`——admin 册直接调用，与其余 item 级端点同列。
- **两内容闸含 gist-status 形状键**：`build_fixtures.py:293`（`("admin", "backup/gist/status", "gist-status")`，`:296-317` 的 `GIST_STATUS_FIELDS` 八字段 `configured/tokenHint/encrypted/intervalHours/gistUrl/lastAt/lastUpload/lastError` + kind 处理分支，查 code 0 + 逐字段类型，bool/int 以 `isinstance(x, bool) != (t is bool)` 区分）；`check-fixtures-content.mjs:76`（`{ book: "admin", key: "backup/gist/status", kind: "gist-status" }`，`:175-205` 同款处理分支，口径与 Python 闸逐项一致：code 0 + 八字段类型 + `configured=false` 时空值不判错）。
- **两静态闸本轮重跑全绿**（`.r16-tree` 内实跑）：`check-fixtures-content.mjs` exit 0（`p06` 日志）：code 闸 13/13 白名单 ok，`ok admin "backup/gist/status" recording #0: code 0, GistBackupStatus shape ok (configured=false)`；`check-shape-drift.mjs` exit 0（`p07` 日志）：`shape ok: 261 recorded object(s) cover every required field … (42 fixture key(s) out of scope)`。
- **夹具内容**（`vite-frontend/src/demo/fixtures.json`，sha256 `31279cc60906529d723b5839cff332ffefa5c7c9631483f57dfc24e726f1e8d2`）：`admin["backup/gist/status"]` 1 条，`{"res": {"code": 0, "msg": "操作成功", "ts": 1792877699789, "data": {"configured": false, "tokenHint": "", "encrypted": false, "intervalHours": 0, "gistUrl": "", "lastAt": 0, "lastUpload": 0, "lastError": ""}}, "key": "{}"}`——种子面板的诚实作答（未配 token），与两闸的“查形状不查非空”口径一致。
- **重录取证（非只信提交信息）**（`R16-L1-fixtures-diff.log`，`583cec0^` 与 `583cec0` 两版夹具逐键比对）：`recordedAt` `1792751378205` → `1792877739132`；键 113 → 114；80 键内容变化；159/159 条目 `res.ts` 全变、0 条相同；新增 34 键中唯一非监控键即 `backup/gist/status`，其余均为随录制时间后移的 monitor 时间窗口键（README 既定行为），删除 33 键全为旧窗口键；gist-status 的 `res.ts` 距 `recordedAt` 仅 39,343 ms，属同一录制会话；外部入站 probe 由修复前的全已完成（`{ok:true, latencyMs:53-72}`）变为全 null——新鲜种子首轮探测未完成态，手工补值不会产生该变化。以上与 README 纪律（种子面板全量重录、不得手工补值）一致。**诚实注记**：录制会话本身未被本轮目击（仓库内无 `raw.json` 与录制日志留存）；本项“重录”结论以该差分取证 + 内容一致性为准，不以提交信息为准。
- **全轮走查先红后绿（本轮三跑，锁定版 chromium-1187 `Chromium 140.0.7339.16`，README 标准命令 `VITE_DEMO=true VITE_BASE=/flmx-max/demo/ vite build --outDir dist-demo` + `node scripts/demo/check.mjs dist-demo`）**：
  - Run A（`b54b44f`，`R16-L1-demo-b54b44f.log`）：`tsc --noEmit` exit 0；构建 exit 0（7.46s，夹具块含 `backup/gist/status`）；走查 **exit 0**，`demo ok: 14 admin + 5 user pages`。
  - Run C（修复前 `583cec0^` = `5bd5a90`，`R16-L1-demo-prefixed.log`）：构建 exit 0；走查 **exit 1**，唯一 FAIL 逐字为 `FAIL admin #/config: [demo] not recorded: backup/gist/status`，`1 problem(s)`——与任务书所述修复前状态逐字一致。
- **残余**：其余四个 Gist 端点（`save/upload/versions/restore`）为动作触发，不在走查覆盖内（R15-L5 范围注记沿用）；本项关闭口径仅为 `gist/status` 一端点，不得以走查已绿冒充五端点已录。

### 2.3 R15-FIX-01（修复中发现的新问题，入站页空指针）：OPEN

- **`renderInbound` 腿：修复在位，已闭合。** `vite-frontend/src/pages/inbound.tsx:562-570`（b54b44f）：
  ```tsx
  // R15-X-02 修复中发现：外部入站首轮探测未完成时 probe 为 null；……
  const probe =
    item.external && item.probe && !item.probe.skipped
      ? item.probe
      : undefined;
  const unreachable = item.enabled && probe !== undefined && !probe.ok;
  ```
  与任务书给定形态逐字一致（null 与 undefined 同义，随后 `probe !== undefined && !probe.ok` 不再读 `null.ok`）。`tsc --noEmit` exit 0（Run A 日志内）。
- **空 probe 态在夹具中真实存在且走查实际经过该态（非冒充，敏感性探针为证）。** 夹具 `admin["inbound/list"]` 12 行 probe 全为 null，含外部入站 id 10/11/12（`机场 · 台湾 01 / 韩国 01 / 英国 01`，`external=true`）。null 的来源已查明：`build_fixtures.py:138-141` 对 `inbound/list` 逐行执行 `i["probe"] = fix_probe(i.get("probe"), …)`，而 `fix_probe`（`:91-92`）对非 dict 原样返回——录制时无 probe 即被显式写成 null（修复前夹具的非外部行早已如此；重录后外部行亦然）。
  - Run A 走查访问 `admin #/inbound` 无问题、exit 0。
  - Run B 敏感性探针（`R16-L1-demo-revert.log`）：同一 `b54b44f` 树仅把 `inbound.tsx` 还原为 `583cec0^` 旧式（`:563` 的 `item.external && !item.probe?.skipped ? item.probe : undefined`），重建走查 **exit 1、20 problems**，首因逐字为 `FAIL admin #/inbound: page error: TypeError: Cannot read properties of null (reading 'ok')`（栈顶 `at Array.map`，即 `renderInbound` 的 map 内抛错），其后 SPA 崩溃连带全部后续页面 blank——与修复提交所述“20 problems”逐字一致。走查确经过该态且对该缺陷敏感；修复后文件已还原，`.r16-l1` 恢复 0 脏文件。
- **但关闭条件“全仓同型搜索确认仅此一处”不成立——第二处未修复位点在位，整项 OPEN。** 同型扫查结果（`p09-sametype-sweep.log`、`p10-probe-surface.log`）：`?.skipped` 全仓仅 2 命中（注释 `:563` 与 `sheetProbe :1020`）；`!== undefined && !*.ok` 全仓仅 2 命中（已修 `:570` 与残余 `:1024`）；其余同型点均已守卫（`dashboard/admin-overview.tsx:311` 的 `i.probe && !i.probe.skipped && !i.probe.ok`、`components/latency-cell.tsx:49` 的 `if (!probe || probe.skipped)`、`:538` 的 `runProbe` 过滤——后端 `inbound/probe` 返回 `map[string]externalProbe` 值类型，JSON 值不可能是 null，且夹具无该端点录制；`inbound/*.tsx` 子组件无 probe 读取；其余 `probe?.` 均为可选链）。唯一残余见 L1-CAND-01。
- **生产可达性界定（诚实口径，不夸大）**：实时 `inbound/list`（`go-backend/internal/http/handler/kernel.go:584`，`:716-718`）仅在 `externalProbes` 命中时才置 `v.Probe = &p`，字段为 `Probe *externalProbe \`json:"probe,omitempty"\``（`:99`）——无结果时键省略、永不发 null；`inboundProbeView`（`node_latency.go:460-484`）无生产调用方（仅测试引用，见 L1-CAND-02），其 nil 返回值到不了响应。故**观测到的 null 态是演示夹具产物**（`build_fixtures.py` 显式写 null），生产实时 API 不产生 null；FIX-01 的崩溃在观测面上属演示面，生产入站页经由已知实时路径不可达。修复本身仍正确且必需（演示是已发布的产品面；代码须兑现文件内“null 与 undefined 同义”的不变量，见 L1-CAND-01）。

## 3. 新发现（临时编号，由协调者统一编号）

### L1-CAND-01（P3 建议，演示面）：入站详情 sheet 的 `sheetProbe` 同型空指针未修，点击即整页空白

- **位置**：`vite-frontend/src/pages/inbound.tsx:1019-1024`（b54b44f；修复前 `583cec0^` 同位置 `:1013-1018` 逐字相同，修复提交未触及）：
  ```tsx
  const sheetProbe =
    sheetItem?.external && !sheetItem.probe?.skipped
      ? sheetItem.probe
      : undefined;
  const sheetUnreachable =
    !!sheetItem?.enabled && sheetProbe !== undefined && !sheetProbe.ok;
  ```
  与修复前的 `renderInbound` 旧式逐字同构：`probe=null` 时 `!null?.skipped` 为真，`sheetProbe` 得 null；`null !== undefined` 为真，随后读 `null.ok` 抛错。`:1027-1033` 的 `sheetProbe?.error` / `sheetProbe ? …` 均在抛错点之后，救不了。
- **触发路径**：`sheetItem = inbounds.find((i) => i.id === sheetFor) ?? null`（`:1015`）；`setSheetFor(item.id)` 由卡片点击触发（`:588` 的 `activate`、`:775` 的 `onPress`）。该求值在组件每次渲染时无条件执行（非仅 sheet 打开时），只要 `sheetFor` 指向一个 probe 为 null 的外部入站即抛错。`loadData` 的合并（`:296` 的 `latestProbe(i.probe, …)`）对 null 输入原样返回 null，故 null 态可达该派生。
- **实测复现**（`R16-L1-sheet-click.log`，固定构建 Run A 的 `dist-demo` + chromium-1187，CDP 点击探针 `/tmp/r16l1/sheet-click.mjs`）：访问 `admin #/inbound` 正常（`hasTW: true`，页面文本 1,163 字符）；点击 `机场 · 台湾 01` 卡片后页面文本长度归 0，捕获 1 条异常——`PAGE-ERROR: TypeError: Cannot read properties of null (reading 'ok')`，与 FIX-01 原崩溃同签名。探针 exit 1（`RESULT: SHEET-CRASH-REPRODUCED`）。
- **为何走查没拦住**：`check.mjs` 的 `INTERACTIONS` 仅两步（user monitor、user my-subscription），无入站卡片点击；README §30 已明文登记“走查不打开的对话框与不点击的控件……不在本闸覆盖内”。sheet 面正在该已登记的盲区内。
- **可达性**：演示面已实测可达；生产实时路径经由已知 API 形态不可达（见 §2.3 末段界定）——故定 P3（与 FIX-01 同级，演示面；定级请协调者裁定）。
- **修法建议**：与 `:566-569` 同款守卫——`sheetItem?.external && sheetItem.probe && !sheetItem.probe.skipped ? sheetItem.probe : undefined`。加固建议：`api/types.ts:920` 的 `probe?: InboundProbe` 应改为 `probe?: InboundProbe | null`，使 `tsc` 对此类空值可见（本次 `tsc --noEmit` exit 0，类型层面完全没拦住该 bug 类）；并在 `check.mjs` 的 `INTERACTIONS` 补一步入站卡片点击（或等价覆盖），否则同类盲区仍会漏网。

### L1-CAND-02（P3 建议，无运行时影响）：`inboundProbeView` 死代码 + 失实注释

- **位置**：`go-backend/internal/http/handler/node_latency.go:460-484`。注释 `:460-461` 称 “`inboundProbeView` is what the inbound list shows”，但全仓生产调用方为 0——仅测试引用（`node_latency_test.go:136/:143/:149/:156`）。实际的 `inbound/list`（`handler.go:315` 注册 → `kernel.go:584` 的 `inboundList`）走 `kernel.go:716-718` 的 `externalProbes.get` 直取，从不调用该函数；其 `:482-484` 的 `if !hasReach { return nil }` 分支在生产响应中永不到达。
- **影响**：无运行时影响；但注释失实，且该函数曾被用作 null 态的机理依据，与实际接线不符。建议删除，或接线并订正注释（由协调者定夺，不属本次关闭验证的修复范围，本路未改）。

## 4. 观察（不立案）

- **OBS-1**：`tsc` 对本 bug 类失明——`InboundApiItem.probe` 的类型（`types.ts:920`）不含 `| null`，而演示数据层真实携带 null；Run A 的 `tsc --noEmit` exit 0 与运行时崩溃并存。已并入 L1-CAND-01 加固建议，不另立案。
- **OBS-2**：`check-doc-consistency.sh` 第 2 项的技能包覆盖数为 197 条路由（R16 基线钉死的路由总数为 202：`handler.go` 201 + `router.go` 1）；差值 5 条为非 `/api/v1/` 前缀路由，不在该检查口径内，非不一致。
- **OBS-3**：本轮三跑的构建产物指纹：Run A 夹具块 `fixtures-CRObjqk3.js 1,725.47 kB`；Run C（`5bd5a90`）夹具块 `fixtures-DHP6byBA.js 1,791.22 kB`（与 R15-L5 的同树构建逐字一致，交叉印证 Run C 复现的是同一基线产物）。

## 5. 交付

- 本报告：`~/workspace/flvx-max/audit-out-132/r16/R16-L1-product-closure.md`
- 证据附件（`~/workspace/flvx-max/audit-out-132/r16/`）：
  - `R16-L1-demo-b54b44f.log`（Run A：`b54b44f` 构建 + 走查 exit 0）
  - `R16-L1-demo-revert.log`（Run B：旧 `inbound.tsx` + 新夹具，走查 exit 1、20 problems）
  - `R16-L1-demo-prefixed.log`（Run C：修复前 `5bd5a90`，走查 exit 1、唯一 FAIL）
  - `R16-L1-sheet-click.log`（Run D：点击探针，sheet 崩溃复现）
  - `R16-L1-fixtures-diff.log`（`583cec0^` vs `583cec0` 夹具逐键比对）
  - `p01-errors-basics.log`、`p02-doc-consistency.log`、`p03-code500-fullscan.log`、`p04-x02-x03-basics.log`、`p05-fixtures-inspect.log`、`p06-check-fixtures-content.log`、`p07-check-shape-drift.log`、`p08-fix-report-absence.log`、`p09-sametype-sweep.log`、`p10-probe-surface.log`（静态探针原始输出）
- 工作树：`~/workspace/.r16-tree`（detached HEAD `b54b44f`，终态 0 脏文件，未修改）；`~/workspace/.r16-l1`（`b54b44f`，构建目录已清、终态 0 脏文件）；`~/workspace/.r16-l1-pre`（`5bd5a90`，构建目录已清、终态 0 脏文件）。未改产品代码、未打 tag、未发版。
- 探针脚本：`/tmp/r16l1/run-a.sh`、`run-b.sh`、`run-c.sh`、`sheet-click.mjs`（临时探针，非交付物；构建产物与日志已落盘如上）。
R16-L1-product-closure END · 自证：本行之前正文字节数 20409 · END
