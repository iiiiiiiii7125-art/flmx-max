# R22 修复集成报告（FIX-INT-REPORT）

基线：main @ `9908cbb`（= b823421 的 R21 修复 + D-GEO-01 文档提交 `docs(faq)`）。分支 `fix-r22`，worktree `~/workspace/.fixr22`。未合 main、未打 tag、未发版。

## 变基决定（已执行，有据）

直接在 main @ 9908cbb 上做，无需变基。依据：D-GEO-01 提交只改 `doc/guide/faq.md` 一处，与两路改动面（F-01 `vite-frontend/src/api/index.ts`、F-02 `doc/guide/forward.md`）零文件重叠；两路之间亦零重叠（前端代码 vs 文档）。三路合入零冲突；merge-base 与 main = `9908cbb`。

## 两路合入（按完成顺序合入，不等齐）

| 路 | Lane 分支 | 合入提交 | 修复 | 回归 |
|---|---|---|---|---|
| F-02 | `fix-r22-f02` @ `3a70f55` | `616063f` | `forward.md:67` 改指真实原始 API `POST /api/v1/node/nftables/pending-deletes`，与 `nodes.md:231-246` 对齐 | 文档构建 exit 0；逐字对照一致；断言 0 条 |
| F-01 | `fix-r22-f01` @ `6035130` | `a25736f` | 删 `api/index.ts:382` `generateCaptcha` 与 `:383–384` `verifyCaptcha` 两个死导出 | 调用点全集 190→188、缺失 0；两标识 grep 0 命中；tsc+vite 全绿；go 测试产品失败 0；断言 0 条 |

产品 diff vs `9908cbb`：仅 2 文件，+1/−4（`forward.md` 1 行改、`index.ts` 删 3 行）。全分支 diff 208 文件 +29807/−4，其中 204 文件为归档提交 `c47cc85`（见下）。

## 归档提交 `c47cc85`

按 FIX-PLAN「R22 审计产物随本次提交归档进库」：把主仓 `audit-out-132/r22/` 全部审计产物（175 文件，3.0M，含 `tm/` 腿日志）+ F-01 的 9 个证据日志 + 计数探针脚本（`R22-FIX-F01-count-probe.py`，方法留存）以显式 `git add -f` 入库。口径：R21 在 main 有 94 个已跟踪审计文件（含 `.log`），R22 此前为 0 个——本次补齐。F-02 无独立证据日志（证据在报告正文内）。禁用 `git add -A`，全程显式 add。

## 集成回归（最终基线 `c47cc85`，全量重跑）

日志：`R22-FIX-INT-regress.log`（27,307 字节，盘上，`*.log` 不入库）。2026-10-25T14:47:31Z 起，约 8.6 分钟。

| 步骤 | 结论 |
|---|---|
| go-backend build / vet | exit 0（`GOPATH==GOROOT` 警告为沙箱环境变量所致，良性） |
| go-backend `go test ./...` | exit 1，16 包 ok；唯一 FAIL 是已知 health 沙箱 fake-IP 环境红 `TestValidateMonitorTCPTarget/公网域名放行`（`monitor_ssrf_test.go:39`，`err=目标地址不允许（内网/保留地址）: example.com`，与 R22-L1 基线日志逐字相同）；**产品失败 0**（handler 342.5s、repo 91.4s、contract 40.4s 均 ok） |
| go-gost build | exit 0（`CGO_ENABLED=0`） |
| go-gost/x `go test ./...` | exit 0，全部 ok |
| 前端 tsc + vite build | exit 0（PWA precache 65） |
| doc 构建 | exit 0（vitepress v1.6.4） |
| 文档一致性脚本 | PASS（`RESULT: PASS`；技能包覆盖 197 条路由、三处版本 1.4.8 一致） |
| API census 探针重跑 | `TOTAL=188 MAPPED=188 MISSING=0`，exit 0 |
| grep `generateCaptcha`/`verifyCaptcha` | exit 1 = 0 命中 |
| grep `pending-delete` in `vite-frontend/src` | 3 命中，全部为注释（见下） |
| `forward.md:67` vs `nodes.md:231-246` | 逐字对照一致：两处均指向 `POST /api/v1/node/nftables/pending-deletes` |

## 口径订正（两项，非产品问题）

1. **190 → 188，不是 190 → 189**：任务书与 FIX-PLAN 写「190 → 189」，但同批删除的是两个调用点，正确算术为 190 → 188；集成树内探针重跑实测 188、缺失 0，差分日志证明只移除了上述两条。修复本身无误。
2. **pending-delete grep 不是 0 命中**：FIX-PLAN 称「`grep -rn pending-delete vite-frontend/src` 0 命中」，集成重跑实测 3 命中——`types.ts:269`、`forward.tsx:3107`、`batch-actions.ts:39`，全部为注释，无 UI、无 API 封装；R22-X-02 立案的实质前提（无管理接口、无前端封装）仍成立。后续引用应写「3 命中、全部为注释，无 UI/无 API 封装」。

## 断言更新登记

**0 条。** 两路均未删测试、未放宽断言、未吞错误。F-01 唯一剩余文本提及是后端注释 `handler.go:1192`（R10X-008 历史说明，陈述仍为真，按范围未动）。

## BC 注记

无 BC 项。F-01 为前端死代码删除（零调用点）；F-02 为文档措辞修正（与已在位的正确文档口径对齐）。

## 未验证清单结转（R23）

R22 未验证项 12 项继续结转：远端/非 Debian 内核、浏览器 DOM 腿、真实 Gist 端到端、单族 sock_diag 真机形态、F-01 多节点真机腿、VG-09、真 PG 方言面、SIP023 四面与 144 格、`withSpeed=true` 测速腿、socket 4 SKIP、Alpine/OpenRC 套件、demo 内容闸。本轮修复无需测试机（F-01 纯前端死代码、F-02 纯文档），未占测试机锁。

## 交付

- 集成分支 `fix-r22`（集成报告提交即为新 tip），worktree `~/workspace/.fixr22`，工作区干净；merge-base 与 main = `9908cbb`。
- 逐项报告已验盘：F-01 8,209 字节、F-02 9,865 字节，尾行 END 自证在位；集成回归日志 27,307 字节在位。
- 是否收敛由 R23 重审判定，本轮不报收敛。下一步：合 main 后开 R23 重审。
— R22-FIX-INT-REPORT END · 自证：本行之前正文字节数 5269 · END
