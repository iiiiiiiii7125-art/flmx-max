# R22 修复报告 F-01：删除 `generateCaptcha` / `verifyCaptcha` 死导出

- 基线：main @ 9908cbb（= b823421 的 R21 修复 + D-GEO-01 文档提交，`docs(faq): record Xray freedom private-target block as known limitation`）。
- 分支：`fix-r22-f01`，独立 worktree `~/workspace/.fixr22-f01`（`git worktree add ~/workspace/.fixr22-f01 -b fix-r22-f01 9908cbb`）。
- 依据：`R22-FIX-PLAN.md` F-01、`R22-X-ledger.md` R22-X-01、`R22-X-verdicts.md` V-02。
- 窗口：2026-10-25 22:35–22:45 CST（UTC 14:35–14:44）。无需测试机（纯前端死代码删除，零调用点）。

## 一、改了什么

只改 1 个文件，删 3 行，零新增行：

| 文件 | 行（修复前） | 改动 |
|---|---|---|
| `vite-frontend/src/api/index.ts` | `:382` | 删 `export const generateCaptcha = () => Network.post(`/captcha/generate`);`（R22-X-01：指向未注册端点 `/captcha/generate` 的死导出） |
| `vite-frontend/src/api/index.ts` | `:383–384` | 删 `export const verifyCaptcha = (data: { captchaId: string; trackData: string }) => Network.post("/captcha/verify", data);`（同批清理的死导出；其端点 `/captcha/verify` 已注册，不另编号，见 R22-X-ledger §六 O-K-1） |

`diff --stat`：`vite-frontend/src/api/index.ts | 3 ---`，1 file changed, 3 deletions。`checkCaptcha`（`:381`，`/captcha/check`）与「验证码相关接口」注释保留；其余文件零改动。

后端注册表复核（修复前，`go-backend/internal/http/handler/handler.go`）：仅注册 `POST /api/v1/captcha/check`（`:292`）与 `POST /api/v1/captcha/verify`（`:293`）；`grep -rn "captcha/generate" go-backend/` 0 命中（exit 1）。

## 二、先红后绿证据

探针：`/tmp/r22-f01-count-probe.py`（方法复现 R22-L3 §3.1：`vite-frontend/src/api/index.ts` 内全部 `Network.post/get` 首参路径字面量 + `exportBackup` 直调 axios 的 `/backup/export`，逐条对 `handler.go` 的 `mux.HandleFunc("/api/v1/...")` 注册表核对；模板字面量按 `${` 截断后以前缀路由匹配，如 `/monitor/nodes/${nodeId}/metrics` → `/monitor/nodes/` 前缀路由）。

探针修正记录：首版正则 `<[^>]*>` 被嵌套泛型截断（`Record<string, [number, number]>>` 等 7 处多行调用漏抓，初版只得 182/189）；改用 `[^()]*` 跨泛型匹配后重跑得精确全集。另核查：全文件无变量路径 `Network.*` 调用（`grep -nP 'Network\.(get|post)\b[^()]*\(\s*[a-zA-Z_$]'` 0 命中）。

### 红（修复前）

- `audit-out-132/r22/R22-FIX-F01-counts-red.log`（探针 exit 1）：
  - `TOTAL_CALL_POINTS=190`（189 个 `Network.*` + 1 个 `exportBackup` 直调 axios），`MAPPED=189`，`MISSING=1`；
  - `MISSING line=382 path=/captcha/generate`——与 R22-L3 §3.1 的「190 个调用点中唯一未映射者」逐字一致。
- `audit-out-132/r22/R22-FIX-F01-grep-red.log`：
  - `grep -rn generateCaptcha vite-frontend/src scripts/` 1 命中，仅定义本身（`:382`）；排除定义后调用点 0 个；
  - `grep -rn verifyCaptcha vite-frontend/src scripts/` 1 命中，仅定义本身（`:383`）；排除定义后调用点 0 个；
  - `grep -rn "captcha/generate" go-backend/` 0 命中（exit 1）。

### 绿（修复后）

- `audit-out-132/r22/R22-FIX-F01-counts-green.log`（探针 exit 0）：
  - `TOTAL_CALL_POINTS=188`，`MAPPED=188`，`MISSING=0`——全部映射到后端注册表，缺失 0 条。
- `audit-out-132/r22/R22-FIX-F01-grep-green.log`：
  - `grep -rn generateCaptcha vite-frontend/src scripts/` 0 命中（exit 1）；
  - `grep -rn verifyCaptcha vite-frontend/src scripts/` 0 命中（exit 1）。
- `audit-out-132/r22/R22-FIX-F01-pathdiff.log`（HEAD 版 vs 工作区版路径全集逐项差分）：
  - `RED_HEAD_total=190 GREEN_working_total=188`；仅移除 `/captcha/generate`、`/captcha/verify` 两条，新增 0 条；重复路径字面量 5 处红绿一致（未变）。

### 口径偏差说明（任务书算术问题，非产品问题）

任务书与 `R22-FIX-PLAN.md` F-01 写「调用点全集重核 190 → 189」，但本次同批删除的是**两个**调用点（`generateCaptcha` 与 `verifyCaptcha` 各占 1 个 `Network.*` 调用），正确算术为 **190 → 188**。实测 188、缺失 0 条，差分日志证明只移除了上述两条路径、未多删未少删。建议主控把计划中的「190 → 189」订正为「190 → 188」；本修复本身无误，不因此返工。

## 三、构建 / 测试结论

- 前端 `audit-out-132/r22/R22-FIX-F01-frontend-build.log`：`tsc_exit=0`，`vite_exit=0`（`✓ built in 2.54s`，PWA precache 65 entries）。执行方式说明：沙箱无 `pnpm` 二进制，故直接跑 `package.json` 的 `build` 脚本本体 `tsc && vite build`（`node_modules/.bin/tsc` + `node_modules/.bin/vite`）；`node_modules` 以软链复用主仓同 lockfile 的已装依赖（`vite-frontend/package.json` 与 `pnpm-lock.yaml` 在 9908cbb 与主仓 HEAD 间 `git diff` 为空，版本一致），软链与 `dist/` 均被 git 忽略、未入提交。
- 后端 `audit-out-132/r22/R22-FIX-F01-go-build.log`：`go_build_exit=0`（`go build ./...`，go1.25.1，`~/go/bin/go`）。
- 后端 `audit-out-132/r22/R22-FIX-F01-go-vet.log`：`go_vet_exit=0`（`go vet ./...`）。
- 后端 `audit-out-132/r22/R22-FIX-F01-go-test.log`：`go test ./...`（go-backend 模块）`go_test_exit=1`，16 个包 `ok`，唯一 FAIL 包为 `go-backend/internal/health`，唯一 FAIL 用例为 `TestValidateMonitorTCPTarget/公网域名放行`。该红为**已知 health 沙箱 fake-IP 环境红**，签名与 R22-L1 基线日志逐字核对一致：
  - 本轮：`monitor_ssrf_test.go:39: ValidateMonitorTCPTarget("example.com:443") err=目标地址不允许（内网/保留地址）: example.com, wantErr=false`（另有 `service monitor 71: node target skipped: 目标地址不允许（元数据/硬拒绝地址）: 169.254.169.254:80`）；
  - R22-L1 `R22-L1-gotest-full.log` 同行逐字相同（仅耗时不同：本轮 0.03s/2.816s，L1 为 0.00s/2.845s）。
  - 机理：沙箱 DNS 把 `example.com` 解析到保留地址，SSRF 守卫拒绝，与产品代码无关；本修复未碰后端任何文件。**产品失败 0**。其余关键包均 `ok`：`handler`（287.129s）、`repo`（50.602s）、`tests/contract`（31.274s）、`kernel`、`netguard`、`monitoring`、`ws` 等。
- go-gost 未跑：本次改动为纯前端删除，未触 agent 代码，不在任务书回归范围内。

## 四、断言更新登记

**0 条。** 本项无测试断言改动：未删测试、未放宽断言、未吞错误。唯一剩余文本提及是后端注释 `go-backend/internal/http/handler/handler.go:1192`（R10X-008 历史说明中的「verifyCaptcha 无现行调用点」一句，属注释而非代码引用；陈述仍为真——该导出本就零调用点、现已不存在），按范围未动，未计入改动文件。

## 五、工作区与交付状态

- 提交纪律：禁用 `git add -A`；本次只显式 add 2 个文件：`vite-frontend/src/api/index.ts` 与本报告 `audit-out-132/r22/R22-FIX-F01-REPORT.md`（提交 hash 见分支尖端，主控核验）。
- 证据日志（`*.log`）说明：仓库 `.gitignore:122` 的 `*.log` 规则使全部 `*.log` 不可入 git，与 R22 审计日志同口径——日志落盘在 worktree 内 `audit-out-132/r22/R22-FIX-F01-*.log`（共 9 个：counts-red/green、grep-red/green、pathdiff、frontend-build、go-build、go-vet、go-test），随 worktree `~/workspace/.fixr22-f01` 保留，不随提交入库；报告正文已摘录全部关键行。
- 工作区干净：除上述 2 个已提交文件外，`git status --porcelain` 无其他改动；`node_modules` 软链与 `vite-frontend/dist/` 均被忽略。
- 未合 main、未打 tag、未发版。分支 `fix-r22-f01` 基线 main @ 9908cbb 未动；main 仓 HEAD（a99d68d，含其他工作）未碰。
- 工具链注记：Go `go version go1.25.1 linux/amd64`；每次 go 调用均带 `warning: both GOPATH and GOROOT are the same directory (/home/hatch/go)`，为沙箱环境变量所致的良性警告，不影响构建与测试结果。
— R22-FIX-F01-REPORT END · 自证：本行之前正文字节数 8130 · END
