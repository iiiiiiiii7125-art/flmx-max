# R22 修复 F-02 报告：`forward.md:67` 失实表述修正（R22-X-02，文档 P3）

- 基线：main @ 9908cbb（= b823421 的 R21 修复 + D-GEO-01 文档提交 `docs(faq): record Xray freedom private-target block as known limitation`）
- 分支：`fix-r22-f02`（自 9908cbb 切出，独立 worktree `~/workspace/.fixr22-f02`）
- 依据：`audit-out-132/r22/R22-FIX-PLAN.md` F-02、`R22-X-ledger.md` R22-X-02、`R22-X-verdicts.md`（V-01…V-10）
- 结论：**已修复，回归全绿**。只改一处文档措辞，未碰产品代码，未合 main，未打 tag，未发版。

## 一、改了什么

唯一改动文件：`doc/guide/forward.md:67`（「删除与端口回收」小节，nftables 删除 pending-delete 段落的尾句）。

改前（9908cbb 原文，逐字）：

> - nftables 模式的转发删除走“先标记待删除（pending-delete）→ 后台对账确认节点侧规则已移除 → 删除记录”：节点离线时删除会进入 pending-delete，由后台任务按 1m / 5m / 30m / 2h 退避自动重试，上限 24h 后转人工处理；等待期间该端口仍视为占用，不会分配给新转发。管理员可通过节点页的 Pending Delete 管理接口查看待清理项，见 [节点管理](./nodes.md)。

改后：

> - nftables 模式的转发删除走“先标记待删除（pending-delete）→ 后台对账确认节点侧规则已移除 → 删除记录”：节点离线时删除会进入 pending-delete，由后台任务按 1m / 5m / 30m / 2h 退避自动重试，上限 24h 后转人工处理；等待期间该端口仍视为占用，不会分配给新转发。管理员可通过 `POST /api/v1/node/nftables/pending-deletes` 查看待清理项，见 [节点管理](./nodes.md)。

diff（整行仅尾句一处替换，其余逐字不动）：

```diff
-- nftables 模式的转发删除走“先标记待删除（pending-delete）→ 后台对账确认节点侧规则已移除 → 删除记录”：节点离线时删除会进入 pending-delete，由后台任务按 1m / 5m / 30m / 2h 退避自动重试，上限 24h 后转人工处理；等待期间该端口仍视为占用，不会分配给新转发。管理员可通过节点页的 Pending Delete 管理接口查看待清理项，见 [节点管理](./nodes.md)。
+- nftables 模式的转发删除走“先标记待删除（pending-delete）→ 后台对账确认节点侧规则已移除 → 删除记录”：节点离线时删除会进入 pending-delete，由后台任务按 1m / 5m / 30m / 2h 退避自动重试，上限 24h 后转人工处理；等待期间该端口仍视为占用，不会分配给新转发。管理员可通过 `POST /api/v1/node/nftables/pending-deletes` 查看待清理项，见 [节点管理](./nodes.md)。
```

- 未另立案做页面入口（属产品决策，不在本项范围）；未扩写其他段落；未动 `nodes.md`（其口径本就正确）。

## 二、红证据（修复前，基线 9908cbb 实测）

### 1. `forward.md:67` 原文与 `nodes.md:231-246` 的矛盾点（逐字摘录）

`forward.md:67` 宣称（失实句）：

> 管理员可通过节点页的 Pending Delete 管理接口查看待清理项

`nodes.md:231-246`（正确口径，逐字）：

> ## Pending Delete 管理接口（管理员）
>
> 当节点离线时删除转发，系统会将其标记为 pending-delete（status=2），由后台任务按退避策略（1m/5m/30m/2h，上限 24h）自动重试清理。以下接口用于可观测性：
>
> ### POST /api/v1/node/nftables/pending-deletes
>
> 列出所有待清理的转发。
>
> - **权限**：仅管理员（roleID=0）
> - **请求**：POST，无参数（需 Authorization 头带 JWT）
> - **响应**：`{code, msg, data: [{forward_id, node_id, retry_count, next_retry_at, last_error}], ts}`
> - **用途**：退避耗尽转人工后，管理员在此查看哪些转发需要手动处理
>
> ### 相关行为
>
> - pending 行不会出现在转发列表中（`ListForwards` 恒过滤 status=2），只能通过上面的 pending-deletes 接口查看

矛盾点：`forward.md:67` 说查看途径是「节点页的 … 管理接口」（即前端页面入口），而 `nodes.md:246` 明言 pending 行「只能通过上面的 pending-deletes 接口查看」——即唯一的查看途径是原始 API，不是任何页面入口。

### 2. 前端确无此管理接口（佐证“接口不存在于前端”）

- 真实后端路由在位：`go-backend/internal/http/handler/handler.go:324`：`mux.HandleFunc("/api/v1/node/nftables/pending-deletes", h.nftPendingDeleteList)`。
- 前端无 API 封装：`grep -rn pending-deletes vite-frontend/src` → 0 命中（exit 1）；`grep -rn 'pendingDeletes\|PendingDelete' vite-frontend/src` → 0 命中（exit 1）。
- 前端无该 UI 文案：`grep -rni 'Pending Delete' vite-frontend/src` → 0 命中（exit 1）。
- `grep -rn pending-delete vite-frontend/src` → **3 命中，全部为注释**（见 §五偏差说明；不是 UI，也不是 API 封装）：
  - `vite-frontend/src/api/types.ts:269`：`/** 批量删除中转为 pending-delete 的条数与明细（节点不可达时，后端 mutations.go）（F-144）。 */`
  - `vite-frontend/src/pages/forward.tsx:3107`：`// R5-B5: pending-delete（节点不可达，等待后台清理）。`
  - `vite-frontend/src/pages/forward/batch-actions.ts:39`：`// 待清理（节点不可达转 pending-delete）既非成功也非失败，必须单列`

## 三、绿证据（改后复核）

### 1. `forward.md:67` 与 `nodes.md:231-246` 不再矛盾（逐字对照）

| 核对点 | `forward.md:67`（改后） | `nodes.md:231-246` | 结论 |
|---|---|---|---|
| 查看途径 | 通过 `POST /api/v1/node/nftables/pending-deletes` 查看待清理项 | `### POST /api/v1/node/nftables/pending-deletes`，列出所有待清理的转发；「只能通过上面的 pending-deletes 接口查看」 | 一致：同一原始 API，无页面入口宣称 |
| 权限/用途 | 未重述（指向 nodes.md 链接） | 仅管理员（roleID=0）；退避耗尽转人工后查看 | 无冲突，详述仍由 nodes.md 承载 |
| 失实表述 | 「节点页的 Pending Delete 管理接口」已删除 | — | 失实句已消除 |

改后全仓复核失实表述残留：`grep -rn '节点页的 Pending Delete' doc/ vite-frontend/src` → 0 命中。注：「Pending Delete 管理接口」一语仍在 `nodes.md:231` 的节标题「## Pending Delete 管理接口（管理员）」中出现一次——这是正确口径的节标题本身（指向同一原始 API），非失实残留。

### 2. 文档构建 exit 0

- `cd doc && npm run build`：**exit 0**，vitepress v1.6.4，`build complete in 12.62s`（仅 chunk > 500 kB 的常规 warning，无错误）。
- 前置说明：全新 worktree 内 `doc/node_modules` 不存在，首次 `npm run build` 以 exit 127 报 `vitepress: not found`；按仓库 AGENTS.md 先跑 `npm ci`（`added 127 packages in 4s`，exit 0）后再构建成功。`npm ci` 未改动任何已跟踪文件（`package.json` / `package-lock.json` 无 diff）。
- 构建产物核对：`doc/.vitepress/dist/guide/forward.html` 含新措辞 `nftables/pending-deletes`；旧短语 `Pending Delete 管理接口` 在该 HTML 中出现 0 次。

### 3. 前端 grep 复核（改后不变）

- `grep -rn pending-delete vite-frontend/src` → 仍为上述 3 处注释命中，无新增、无 UI 宣称。
- `grep -rni 'Pending Delete' vite-frontend/src` → 0 命中；`grep -rn 'pending-deletes\|pendingDeletes\|PendingDelete' vite-frontend/src` → 0 命中。

### 4. 无需测试机、无需 go 测试

纯文档措辞修正，未改任何产品代码、配置与测试，故无测试机腿、无 go 测试腿。

## 四、断言更新登记

**0 条**。文档项，无断言/契约/测试需要更新。

## 五、偏差说明（实测与任务书/总账口径不一致处，如实记录）

1. **总账「`grep -rn pending-delete vite-frontend/src` 0 命中」在本基线未复现**：实测在 9908cbb 与 b823421（总账基线）均为 **3 命中**（`git grep -n pending-delete b823421 -- vite-frontend/src` 同样 3 行，逐行一致）。三处均为注释（§二.2 逐字列出），不构成 UI 入口或 API 封装，R22-X-02 立案的实质前提（前端无 Pending Delete 管理接口、无 `pending-deletes` 封装）仍然成立；但「0 命中」的字面表述不准确，改后绿腿亦按实测 3 处注释命中记录，不作 0 命中宣称。后续引用该证据时应写「3 命中、全部为注释，无 UI/无 API 封装」。
2. 任务书称基线为「main @ 9908cbb」；实测主仓当前 main HEAD 为 a99d68d（`feat(node): hint when latency target pool is empty`），9908cbb 为其祖先提交。本 lane 按任务书要求以 9908cbb 本身为 worktree 起点（`git rev-parse 9908cbb` = `9908cbbe9edb67602ceb8d62dcf67cbeeb33bc47`），未受主仓 HEAD 漂移影响。

## 六、工作区与提交

- 工作区：`~/workspace/.fixr22-f02`，分支 `fix-r22-f02`，HEAD 9908cbb。
- 构建前后 `git status --short` 均仅 `M doc/guide/forward.md` 一处改动；`doc/.vitepress/dist/` 由 `.gitignore:282` 覆盖，未进入提交。
- 提交纪律：禁用 `git add -A`，仅显式 add 两个文件——`doc/guide/forward.md` 与本报告 `audit-out-132/r22/R22-FIX-F02-REPORT.md`。
- 未合 main、未打 tag、未发版；未碰其他分支（主仓 main 仍在 a99d68d，`fix-r22` / `fix-r22-f01` 分支未动）。
- 附注：R22 审计源文件（`R22-FIX-PLAN.md`、`R22-X-ledger.md`、`R22-X-verdicts.md` 等）当前在主仓工作区为未跟踪状态（`git status` 显示 `?? audit-out-132/...`），本 lane 未移动、未提交这些文件——归档进库由主控统筹；本报告在 worktree 内新建 `audit-out-132/r22/` 目录落盘。
— R22-FIX-F02-REPORT END · 自证：本行之前正文字节数 9786 · END
