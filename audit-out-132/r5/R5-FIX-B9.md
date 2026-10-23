# R5 修复 · 批 9 报告（demo / CI / 测试基建）

- 分支：`fix-r5/b9`（worktree `~/workspace/r5-wt/b9`，基线 `integrate-r5` @ `112a911` = tag 1.4.1 发布态）。
- 认领：R5X-006（P2，测试基建）、R5X-007（P2）、R5X-008（P2，测试基建）、R5X-019（P3）、R5X-023（P3，测试基建）。
- 纪律：未改任何产品语义（尤其未动产品轮次语义）；提交只 `git add` 明确文件；断言变更全部裁定驱动并逐条登记（§6）。
- 结论速览：**007 / 008 / 019 关闭（demo 门禁实跑全绿）；023 关闭（-count=10 全绿 + handler 全量零新增失败）；006 关闭（迁移案 (b) 落地；掉线/静默腿在有 RFC1918 地址环境实跑 PASS，恢复腿在本沙箱因 connect() EPERM 自检 Skip 明示、随发版门 CI 复验——与 B10 存量恢复用例在本沙箱同命运，见 §1.3）**。

---

## §1 R5X-006（P2，测试基建）— 契约测试与 DB-4 轮次语义脱节

### 1.1 选案：(b) 拆层（采 FIX-PLAN 推荐案）

由：(a) 案需给 contract 包加仅测试可用的轮次注入点或以极短间隔启真实定时 loop——前者在产品代码里留测试后门、后者把用例时长与调度抖动绑死，两者都比 (b) 脆弱；(b) 案的状态机断言落在 handler 包内，可直接复用 B10 已验证的定时轮直调模式（`probeExternalInbounds(ctx, nil, probeRoundScheduled)` + `fixdWebhook` 桩），且 contract 包保留一层真正属于「契约」的断言（HTTP 端点回执形态）。产品轮次语义零改动。

### 1.2 落地

| 层 | 文件 | 内容 |
|---|---|---|
| handler（新） | `go-backend/internal/http/handler/notify_r5_b9_test.go` | `TestR5B9ImportedNodeDownScheduledRoundAlerts`：定时轮 1 轮失败不告警 → 2 轮恰一条「导入节点连不上」（断言 event=`inbound_down`、正文含节点名）→ 3 轮不重复 → 改选 `notify_events=node_offline` 后以**第二条独立入站行**打满 streak 仍静默（断言对象是事件选择门本身，非已 down 行的空断言）。`TestR5B9ImportedNodeRecoveryScheduledRoundAlerts`：掉线 → 同端口重新监听 → 定时轮恰一条「导入节点已恢复」+ streak/down 状态清除断言 |
| contract（改写） | `go-backend/tests/contract/notify_contract_test.go` `TestNotifyImportedNodeDownContract` | DB-4 正向断言：手动端点连打 3 次（超 streak），每次回执含该入站 `ok=false` 结果且 webhook 恒 0；恢复段单列子用例 `manualProbeAfterRecovery`（手动探测回 `ok=true` 且不产生恢复告警） |

### 1.3 实跑证据（Skip/Pass 区分，逐字）

本机先给 `lo` 加 RFC1918 别名 `192.168.99.10/24` 使夹具生效（验毕已删除，别名与 `host0` 上的 `172.16.99.10/24` 均已还原）：

| 用例 | 有 RFC1918 地址 | 无地址 |
|---|---|---|
| handler `TestR5B9ImportedNodeDownScheduledRoundAlerts` | **PASS（实跑，2.14s）** | SKIP（夹具明示） |
| handler `TestR5B9ImportedNodeRecoveryScheduledRoundAlerts` | **SKIP（自检明示，见下）** | SKIP（夹具明示） |
| contract `TestNotifyImportedNodeDownContract`（父） | **PASS（实跑，2.83s）** | SKIP（夹具明示） |
| contract 子用例 `manualProbeAfterRecovery` | **SKIP（自检明示）** | —（父已 Skip） |

恢复腿 Skip 的原因不是无地址，而是本沙箱对「拨号到已监听的 RFC1918 地址」施加 `connect() EPERM`（python 原生 socket 实测 `Errno 1 Operation not permitted`，与 R5-I 报告 E3 逐字同型；lo 与 host0 两接口同结果）。同命运实锤：存量 B10 用例 `TestR2B10InboundRecoveryFailureResendsOnce` 在 pristine 主仓树本沙箱同跑 **FAIL 于 :450**（恢复 webhook 不达）——即恢复腿在本沙箱对任何实现都不可表达，属环境面而非本批实现面。处置：两处恢复腿均先做「自检拨号」环境门，自检不过 `Skipf` 写明原因与待验环境（CI/无过滤），**不以 Skip 冒充 Pass**；恢复断言本体已落地、随发版门清单第 6 项在 CI 实跑复验（CI runner 无此过滤，R5-I §1.3 E3 口径）。

先红对照：旧契约用例在本类环境（夹具生效）的 FAIL 签名由 R5-I E1 在案（`:153 expected 1 webhook calls, got 0`）；改写后父用例同环境 PASS。

### 1.4 回归

- `go vet ./tests/contract/ ./internal/http/handler/` 无输出；`go build ./...` OK。
- contract 全包实跑：唯一 FAIL = `TestExternalProbeContract`，pristine 主仓同跑同签名 FAIL（`reachable node: ... ok:false`，同为 live 拨号 EPERM 面，R5-I §8 已登记其本地失败属夹具连通性面）——零新增。
- handler 全包实跑见 §5。

---

## §2 R5X-007（P2）— `node/traffic/month` 技能包 + demo 双缺录、check-doc-consistency 无 CI 接线

| 半边 | 落地 |
|---|---|
| 技能包 | `skills/flmx-api/references/nodes.md` 新增 `POST /api/v1/node/traffic/month` 条目（入参 `tzOffset` 浏览器时区偏移、出参 `{节点ID: [入, 出]}`、明示整机网卡口径与计费/订阅口径不可混称，口径摘自 `api/index.ts:143-146` 与后端 `node_traffic.go` 注释） |
| CI 接线（裁定写死项） | `.github/workflows/ci-build.yml` frontend job 在 `Check contract fields` 后新增 `Check doc consistency` 步（`bash scripts/check-doc-consistency.sh`，workspace 根目录）——与 check-contract-fields 同位，缺口前移到 PR 期 |
| demo fixtures | admin 册补录 `node/traffic/month` 一条（key=`{"tzOffset":-480}`，data 为 6 个录制节点的整机网卡字节；适配器在 key 不匹配时回退首条，任意时区可答）。整册重录环境不可得，此条为手工补录、形态与后端回执逐字同构（`map[string][2]int64`） |
| 录制脚本 | `vite-frontend/scripts/demo/record_items.mjs` 增加该端点直调录制步（注释带 R5X-007 号），下次整册重录自动带出、不再靠手工 |

实跑：`bash scripts/check-doc-consistency.sh` 修复前 exit 1（唯一 FAIL = 该路由）→ 修复后 **exit 0**（「技能包覆盖 handler.go 全部 192 条 /api/v1 路由」，其余 5 项全 OK）。demo 门禁实跑见 §4（与 008/019 同批验收）。

## §3 R5X-008（P2，测试基建）— check.mjs 未随 IA 合并同步

`vite-frontend/scripts/demo/check.mjs` 三处改动（裁定写死）：

1. `ADMIN_PAGES` 删 `monitor`（14 → 13 页）：管理员 /monitor 恒重定向 /node，走查必红点消除。
2. `INTERACTIONS` 的隧道页签步由 `"admin monitor"` 迁移为 `"user monitor"`（步体不变：点文本恰为「隧道」的 button）。
3. MonitorOverview 覆盖以**被授权用户册**补回：`USER_PAGES` 增 `monitor`（4 → 5 页）；fixtures user 册 `monitor/access` 改为 `{allowed: true, admin: false}`（该册自此为监控被授权用户），并从 admin 册复制 MonitorOverview + 隧道页签实际消费的 monitor 录制 23 键（`monitor/nodes`、`monitor/tunnels`、`monitor/tunnels/quality`、各节点/隧道 metrics 与 quality 窗口各留一条——适配器按 bare 路径回退命中、一路径一条足够、`monitor/services*` 三键族；`monitor/permission/list` 为管理员面，不复制）。

先红（基线树 + 基线 check.mjs，Chromium 140 实跑，exit 1，3 条）：
```
FAIL  admin #/monitor: [demo] not recorded: node/traffic/month
FAIL  admin #/monitor: ended up at #/node (signed out?)
FAIL  admin #/monitor (interaction): interaction control not found
```
后绿见 §4。

## §4 R5X-019（P3）— demo fixtures 形状漂移（trafficSince / shareAddressMode）

整册重录环境不可得 → 手工补值，方法与 shiftTimes 平移口径核对如下（裁定要求项）：

- `trafficSince`（admin `subscription/list` 5 条 + user `user-subscription/mine` 3 条，同 id 同值）：按后端 `subTrafficSince` 回退链语义取值——`resetDay=0` 三条取种子创建时刻 `1792680981470`（与录制内首条入站 `createdTime` 同值，与 recordedAt 的差 = 2024920ms）；`resetDay=1`（id 3）取当月重置点 2026-10-01 00:00 +08 = `1790784000000`；`resetDay=15`（id 4）取 2026-10-15 00:00 +08 = `1791993600000`。**shiftTimes 核对**：五值均 > 1.6e12 且与 recordedAt（1792683006390）相差 < 400 天（最大差 ≈ 22 天），全部落入适配器平移窗口，演示时随整体时间轴前移、不会出现过去/未来错位；`dateText(有效毫秒)` 不再产生 NaN。
- `shareAddressMode`（admin `inbound/list` 11 条）：按前端既有兜底口径逐条推导（`shareHost` 非空 → `custom`、空 → `auto`），得 8 auto / 3 custom，与后端存储模式语义一致。
- fixtures 体积：2,322,696 → 3,029,322 字节（增量主体为 §3 user 册 monitor 录制复制，系走查覆盖的必要数据）。

## §5 007/008/019 合并验收（demo 门禁实跑）+ 全量回归

- demo 构建：`VITE_DEMO=true VITE_BASE=/flmx-max/demo/ pnpm exec vite build --outDir dist-demo` 成功；`CHROME=<Playwright Chromium 140> node scripts/demo/check.mjs dist-demo` → **`demo ok: 13 admin + 5 user pages`，exit 0**（含 user monitor 走查与「隧道」交互步、admin node 页 traffic/month 应答、my-subscription 页 trafficSince 渲染路径）。三条同批验收成立，**均标实跑关闭**。
- 浏览器环境注记：本机 `/opt/meta-chromium/chrome`（152）下 check.mjs 对**基线树同样**全页假红（20 条、hash 恒空，SPA 未启动），系该二进制与 check.mjs 的 CDP 用法不兼容、与本批改动无关（基线同签名复现已实测）；CI 用 runner 自带 Chrome、本机验收用 Playwright Chromium 140。
- 前端正式构建：`pnpm run build`（tsc + vite）exit 0。
- handler 全包（584s 实跑）：FAIL 恰 2 条——`TestInboundProbeEmptyIDsDoesNotClearDown`、`TestR2B10InboundRecoveryFailureResendsOnce`，均为存量用例且 **pristine 主仓同跑同 FAIL**（同 §1.3 的 live 拨号 EPERM 环境面）；本批 PendingDelete 用例（§6）在全量内 PASS、R5B9 掉线用例 PASS / 恢复用例 SKIP 明示。**零新增失败**（本沙箱口径；CI 无过滤环境按 R5-I 口径应全绿，随发版门复验）。

## §6 R5X-023（P3，测试基建）— PendingDelete deflake 三项

`go-backend/internal/http/handler/r3_b4_whitelist_funnel_test.go` `TestR3B4PendingDeleteJobRefreshesWhitelistOnlyAtFinalDelete`，三项照裁定写死（禁重试、禁固定等待掩盖）：

1. **:295 改内容型断言**：pending 窗口内由 `len(fresh)!=0 即 FAIL` 改为逐份比对——任何新下发 allow 必须与初始 allow **逐字相等**（与同文件 ①防过修用例同口径；N-04 语义强度不放宽：内容变化的刷新仍即刻 FAIL）。
2. **基线快照前事件驱动静默同步**：拍 baseline 前以 `waitForCondition` 轮询 recorder 计数、直至一个轮询间隔（300ms）内不再增长，确认重连波次尾随 apply 已落定；系静默判定、非 sleep 掩盖竞态。
3. **终删段改 `waitForCondition`**：以「出现不含 `198.51.100.10/32` 的 allow」为条件事件驱动等待，替代单次即时快照取最后一份的写法，消除反向竞态。

验收：同命令 `-count=10` **10/10 PASS**（修复前基线证据：R5-L 报告同命令 8 PASS / 2 FAIL 同签名）；handler 全量连跑结论见 §5（该用例在全量内 PASS）。

## §7 断言变更登记（裁定驱动，逐条）

| # | 用例 | 变更 | 驱动 |
|---|---|---|---|
| 1 | contract `TestNotifyImportedNodeDownContract` | 断言对象由「手动端点驱动掉线/恢复/未选静默」改为「手动端点返回探测结果（ok=false/true）且 webhook 恒 0」的 DB-4 正向断言；原状态机断言全集移 handler 层（下行），未丢失任何一条 | R5X-006 裁定案 (b) |
| 2 | handler 新增 `TestR5B9ImportedNodeDownScheduledRoundAlerts` | 承接原契约断言 1–3、5（一次不报/恰一条/不重复/未选静默），驱动面由手动端点改定时轮直调；未选静默改用第二条独立入站行（原契约该段在 DB-4 下为空断言，此为加强非放宽） | R5X-006 裁定案 (b) |
| 3 | handler 新增 `TestR5B9ImportedNodeRecoveryScheduledRoundAlerts` | 承接原契约断言 4（恢复恰一条 + 标题口径），另加 streak/down 清除断言；加环境自检 Skip 门（沙箱 EPERM 时明示） | R5X-006 裁定案 (b) + R5-I E3 |
| 4 | contract 子用例 `manualProbeAfterRecovery` | 新增环境自检 Skip 门（同上） | R5-I E3 |
| 5 | handler `TestR3B4PendingDelete...` :295 | 计数型断言 → 内容型断言（逐字相等） | R5X-023 裁定三项 (1) |
| 6 | 同上 基线段 | 新增静默同步（事件驱动） | R5X-023 裁定三项 (2) |
| 7 | 同上 终删段 | 单次快照断言 → `waitForCondition` 事件驱动断言 | R5X-023 裁定三项 (3) |

产品代码零改动；`go-backend/tests/contract/`、`handler` 两包之外无测试断言变更。

## §8 逐条状态

| 条目 | 状态 | 依据 |
|---|---|---|
| R5X-006 | **关闭**（恢复腿带 CI 复验注记） | §1：案 (b) 落地；有地址环境掉线腿 PASS、contract 父 PASS、无地址 SKIP 明示；恢复腿断言已落地、本沙箱自检 SKIP 明示，随发版门第 6 项 CI 实跑复验（同 B10 存量恢复腿口径） |
| R5X-007 | **关闭（实跑）** | §2 + §5：技能包补录、fixtures 补录、check-doc 接入 CI 三件齐；脚本 exit 0、demo 门禁 exit 0 |
| R5X-008 | **关闭（实跑）** | §3 + §5：check.mjs 三处改动齐；先红 3 条签名 → 后绿 `demo ok: 13 admin + 5 user pages` |
| R5X-019 | **关闭（实跑）** | §4 + §5：两字段补齐且过 shiftTimes 核对；走查覆盖 my-subscription/subscription 页全绿 |
| R5X-023 | **关闭（实跑）** | §6：deflake 三项齐；`-count=10` 10/10 PASS；handler 全量零新增失败 |

残余风险：① 006 恢复腿与 handler 全包 2 条存量红同为沙箱 EPERM 环境面，须在 CI（发版门第 6 项）确认全绿后才算 006 的最后一腿实证关闭；② fixtures 的 monitor 录制复制使 user 册与 admin 册各持一份（+0.7MB），下次整册重录时应由录制流程直接产出被授权 user 册、消除手工复制；③ Chrome 152 与 check.mjs 的 CDP 不兼容为本机环境注记，CI runner Chrome 不受影响。
