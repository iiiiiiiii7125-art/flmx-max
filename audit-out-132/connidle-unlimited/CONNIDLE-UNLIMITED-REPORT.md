# 空闲超时“不限”功能报告（分支 feat-connidle-unlimited）

- 基线：main @ 7d93792
- 分支：`feat-connidle-unlimited`（worktree `~/workspace/.connidle-unl`）
- 背景：用户常驻自动客服系统空闲常被掐断（3x-ui 设 99999 曾可避免）；要求加「不限」、永不因空闲自动断开
- 状态：**沙箱部分完成，ready-to-merge（沙箱）**；真机腿未做，等新测试机重部署 recipe 与接入信息（见 §7）

## 1. 设计逐项结论

| 项 | 设计 | 结论 |
|---|---|---|
| 哨兵值 | 不加新字段，`ConnIdleSeconds = -1` 表示“不限”，与 `0 = 未设/默认 300 秒` 同套路 | ✅ 已实现；存量值不受影响，备份往返为一个 int，无需迁移 |
| Normalize | -1 仅 Xray 内核放行；<-1 与 >86400 拒绝并给人话提示；非 Xray 内核带值（含 -1）照旧拒绝 | ✅ 已实现（`model.go:321`）；-2 与 86401 拒且报错含“空闲超时”，-1 在 sing-box/snell 拒 |
| 渲染 | -1 写 `31536000` 秒（365 天），新常量 `UnlimitedConnIdleSeconds`；注释写明 (N, 2N] 下 1–2 年才断、等同永不断，以及不用更大数的两点理由（int32 溢出、升级/重启本来就会断会话） | ✅ 已实现；映射只在唯一策略发射点 `render_xray.go:45`（`renderedConnIdleSeconds()`），其余消费面（levels、出站、规则）仍见存量 -1，不改执行链 |
| 前端 | 超时输入框旁加“不限”复选框；勾上数字框禁用、存 -1；取消勾选恢复原值/留空；文案诚实写“不限（渲染为 365 天，实际等同于永不因空闲断开）” | ✅ 已实现；`connIdleBeforeUnlimited` 记勾选前值，取消恢复原值；输入框勾上时显示“不限（渲染为 365 天）”占位 |
| 列表显示 | 入站列表有超时值显示处，-1 显示“不限”而非数字 | ✅ 已核查：**无显示处**。全仓 grep `connIdle`/`idle` 仅命中 modal、presets、types 三文件，列表改动不做 |
| sing-box | 复选框与数字框同样禁用并附已有原因 | ✅ 已实现；非 Xray 存量值警告处 -1 显示“不限” |
| syncCompanion | -1 走 int 比对，无需改动；须核对 -1↔0 切换时伴生行能同步 | ✅ 已核对：代码零改动；`TestConnIdleUnlimitedCompanionSyncSwitches`（10→-1→0→-1 三次切换均同步）绿 |
| 文档 | 入站指南“空闲超时（秒）”一节补“不限”语义与 365 天机制；skills/flmx-api 里凡写 connIdleSeconds 取值范围处同步 -1 含义；一致性脚本须过 | ✅ `doc/guide/nodes.md:74-75` 已补；skills/flmx-api 全仓 grep `idle`/`connIdle`/`空闲` **0 命中**，同步项为空；`scripts/check-doc-consistency.sh` **PASS** |

## 2. 先红后绿

- **红（已确认）**：新测试先于产品代码写入时，`go test ./internal/kernel/ -run TestConnIdleUnlimited` 编译失败：`undefined: UnlimitedConnIdleSentinel` / `undefined: UnlimitedConnIdleSeconds`（常量尚未定义）。
- **绿**：实现后 kernel 新测试 3/3、handler 新测试 2/2、备份 -1 用例 1/1，见 §3。

## 3. 回归计数（产品失败 0）

- `go build ./...` + `go vet ./...`：全绿（`BUILD_VET_OK`）。
- kernel connIdle 定向：**16/16 PASS**（12 个旧测试 + `TestConnIdleUnlimitedNormalize` / `TestConnIdleUnlimitedRendersOneYear` / `TestConnIdleUnlimitedSharesLevels` 3 个新测试 + R14 守卫 `TestR14F06F04ConnIdleZeroWording`），日志 `logs/kernel-connidle.log`。
- handler 新测试：**2/2 PASS**（`TestConnIdleUnlimitedPersistsAndRenders`：建 -1 → 列表显 -1 → 渲染 31536000；编辑 -1→10→-1 往返持久化；`TestConnIdleUnlimitedCompanionSyncSwitches`），日志 `logs/handler-unlimited.log`。
- repo 备份往返：**1/1 PASS**（`TestBackupConnIdleRoundTrip` 补 -1 行：导出 -1、恢复 -1，不塌成 0），日志 `logs/repo-backup.log`。
- 后端全量 `go test ./... -count=1`（日志 `logs/full-test.log`）：16 个包 ok（handler 406.256s、repo 113.697s、contract 59.393s、kernel 0.161s 等），**唯一 FAIL 为已知沙箱环境红**：`internal/health` 的 `TestValidateMonitorTCPTarget/公网域名放行`（沙箱 fake-IP DNS 使 example.com 解析落 198.18/15 保留段；CI 真实 DNS 下绿）。产品失败 **0**。
- 前端：`tsc` exit 0；`vite build` 全绿（PWA precache 65 entries），日志 `logs/frontend-build.log`。构建产物含新文案（“不限”37 处、校验文案 1 处）；`31536000` 在 dist 内 0 命中——正确，该值只由后端渲染，前端只发 -1。
- `pnpm` 说明：沙箱 PATH 无 pnpm/corepack（二进制未装）；`package.json` 与主仓逐字相同，故软链主仓 `node_modules` 跑 `tsc`/`vite`（worktree 内 `node_modules` 为 gitignored 软链，不入库）。

## 4. 断言更新登记（裁定驱动，逐条）

1. `TestConnIdleNormalize`（`go-backend/internal/kernel/connidle_test.go`）：-1 从非法值改为合法值（`valid` 加 `UnlimitedConnIdleSentinel`，`invalid` 改为 `-2` 与 `86401`）。依据：本功能设计（任务书）把 -1 从“非法”重定义为“不限”哨兵。断言其余一字未动。
2. **无测试改动、仅产品文案调整**：首轮全量跑出 `TestR14F06F04ConnIdleZeroWording` 红——我改写的 Normalize 报错文案不含该测试探针要的“填 0”/“或 0”字串。处置：只改我自己的文案为“空闲超时须为 -1（不限）或 0（默认 300 秒）或 1-86400 秒的整数”（含“或 0”，仍说明 0=默认/清除），R14 测试一字未动；重跑 kernel 16/16 绿。

## 5. 改动文件（12 个）

- 后端：`go-backend/internal/kernel/model.go`（字段注释 + `UnlimitedConnIdleSentinel = -1` / `UnlimitedConnIdleSeconds = 31536000` 常量 + Normalize 范围 -1..86400）、`go-backend/internal/kernel/render_xray.go`（`renderedConnIdleSeconds()` 映射）、`go-backend/internal/store/model/node_kernel.go`（注释）、`go-backend/internal/http/handler/kernel.go`（注释）
- 测试：`go-backend/internal/kernel/connidle_unlimited_test.go`（新）、`go-backend/internal/http/handler/connidle_unlimited_test.go`（新）、`go-backend/internal/kernel/connidle_test.go`（断言更新 1 项）、`go-backend/internal/store/repo/backup_connidle_test.go`（-1 往返用例）
- 前端：`vite-frontend/src/pages/inbound/inbound-form-modal.tsx`（“不限”复选框 + 校验 + 文案 + 非 Xray 警告）、`vite-frontend/src/pages/inbound/presets.ts`（toPayload 注释，-1 直通 `Number("-1")`）、`vite-frontend/src/api/types.ts`（注释）
- 文档：`doc/guide/nodes.md`（“不限”一节）
- 无迁移、无新字段、无 BC 项。`skills/flmx-api` 与技能包版本字面量（1.4.8，与本分支基线一致）无需改动。

## 6. 核查结论（全集枚举）

- connIdle 范围校验面：全仓仅两处（后端 Normalize、前台 modal validate），均已更新 -1 口径（其余 86400 命中为 ms 时间戳/uptime/缓存等无关项）。
- 渲染消费面：`connIdle` 只在 `render_xray.go:45` 写出 policy 值，其余消费面只用 level，不用值——-1 映射无遗漏面。
- audit-out 旧哨兵文档（r16/r19）引用的旧校验文案为历史记录，不回改。

## 7. 真机腿：未做（等新机接入信息）

- 旧测试机（100.80.182.11）已下线，未部署、未连接。
- 新测试机已确认：**ip-172-26-4-158，tailnet 100.123.93.34**，公网仍为同一静态 IP 16.146.199.68，Debian 12，2 核/2G 内存/59G 盘、无 Docker、SSH 已通（走 Tailscale + tsconnect.py）。
- 面板搭建已由父 agent 另派（装 Docker + 部署 main @ 7d93792 的面板 + agent）；搭好后按父 agent 给的重部署 recipe 与接入信息，在新机上部署本分支面板再跑真机腿：“不限”入站会话空闲静置 >600 秒仍不断；对照默认入站会话在 300–600 秒内断开。跑完恢复 pristine 并对账。

## 8. 待办

- [ ] 本分支合 main（由父 agent 执行；本分支不自行合 main、不打 tag、不发版）
- [ ] 真机腿（等新机 recipe，见 §7）
- [ ] 随 1.4.9 发版（发版方重做基线）

END
