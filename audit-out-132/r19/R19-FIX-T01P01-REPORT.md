# R19 修复报告：T-01（测试基建）+ P-01（流程）

基线：main @ d66042c（`d66042cc0b7ac011ddfad09f3f2709b90dafda2e`）。
分支：`fix-r19-t01p01`（独立 worktree `~/workspace/.fixr19-t01p01`）。
依据：`audit-out-132/r19/R19-FIX-PLAN.md`（T-01、P-01 节）、`R19-X-ledger.md`
（R19-OBS-01、R19-PROC-01）、`R19-X-verdicts.md`（V-05、V-07、V-08）。
范围：只做 T-01 与 P-01 两项。不合 main、不打 tag、不发版、不改版本字面量、
不改产品代码（P-01 只改测试机锁机制与纪律文字；T-01 只改测试）。
`git add` 仅明确文件，禁用 `git add -A`。

## 一、T-01：R1B3 测试补 env 加命中/文案断言（R19-OBS-01 / V-07）

### 问题

`TestR1B3TunnelDeleteOnlineRealFailureAborts` 提交态 PASS 但伪装节点命中 0 次：
伪装节点是 `httptest` 本地地址，SSRF 内网拦截代行了 `success=false` 分支，
测试走的是拦截错误路径而非远程明确失败路径——空心 PASS。L2 探针实测
（`R19-L2-r1b3-probe.log`）：无 env 时 hits=0 仍 PASS；补 env 后 hits=1 仍 PASS。

### 改动（文件 1）

`go-backend/internal/http/handler/r1_b3_tunnel_delete_offline_test.go`
（`go test` 目标包 `go-backend/internal/http/handler`）：

- `:6`：import 增 `strings`、`sync/atomic`。
- `:128–140`：测试注释改写，写明 R19-OBS-01 空心 PASS 机理与三点钉死口径。
- `:141`：`t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")`——放行本地伪装节点。
- `:142`、`:144`：`var hits atomic.Int32`，伪装节点 handler 首行 `hits.Add(1)`。
- `:176–178`：命中断言——`hits.Load() < 1` 即 `FAIL`（“the SSRF guard likely
intercepted the request and the test would pass vacuously”）。
- `:179`：`t.Logf("fake remote node hits=%d", …)`，留命中数证据。
- `:180–189`：失败文案断言——回执 `msg` 须含伪装节点失败文本
`simulated real failure` / `permission denied`，且含 `数据面清理失败`
披露，证明走的是在线真实失败分支而非离线/拦截分支。

失败文案链路已核对（非凭空断言）：伪装节点 `success:false` →
`sendRemoteNodeCommandWithTimeout`（`control_plane.go:887–895`）返回
`errors.New("simulated real failure: permission denied")` →
`cleanupForwardsDataPlane`（`tunnel_delete_resolution.go:800–802`）包成
`转发 … 数据面清理失败，隧道未删除：…；已删除 0 条转发` →
`tunnelDelete` 回执 `msg`。产品代码零改动。

### 证据

- 补 env 后实测（本机沙箱，go1.25.1）：
`TestR1B3TunnelDeleteOnlineRealFailureAborts` PASS，`fake remote node hits=1`
（`go test./internal/http/handler/ -run TestR1B3TunnelDeleteOnlineRealFailureAborts -v -count=1`）。
- 红态验证（钉子会咬人）：临时删 `:141` 的 `Setenv` 行重跑，测试 FAIL——
`fake remote node was never hit (hits=0)`；随后原样恢复，`git diff` 确认
恢复后改动与提交内容一致（25 行增、1 行删）。旧版无此断言时同条件 PASS，
即旧测试确为空心 PASS。
- 同文件其余 R1B3 测试（`TestR1B3*` 全批）同步绿，见 §四。

### 钉子（V-08，文件 2）

`go-gost/x/api/r19_fix_t01_batch_dup_test.go`（新建，package `api`，
复用同包 `r18_fix_f02f04_test.go` 的 `r18TestRouter` / `r18SeedConfig` /
`r18Post` / `r18CountNamed`）：

- `:19`：`TestR19T01CreateServicesBatchIntraDupFailsAtStage2`——批内同名
`[同名, 同名]` 调 `POST /config/services/batch`。
- 断言三件：HTTP 400；内存配置中该名 0 项；注册表空。
- 机理（已核 `config_service.go:96–176`）：第 1 阶段两项均过 `IsRegistered`
守卫；第 2 阶段第二项 `Register` 报 `ErrDup`、回滚第一项并 400；
永不到第 3 阶段（启动）与第 4 阶段（配置落盘）。与 V-08 口径一致，
源自 R19 L2 scratch 探针（`R19-L2-batch-dup_test.go.txt`）。
- 实测：`--- PASS: TestR19T01CreateServicesBatchIntraDupFailsAtStage2 (0.01s)`，
日志 `batch [dup,dup] -> 400, config 0 entries, registry empty`。

## 二、P-01：测试机互斥锁原子化（R19-PROC-01 / V-05）

### 复发事实与锁的实际位置

- R19 复发：L1 02:56:37Z 建锁，15 秒后锁文件被覆盖为 `L5 02:56:52Z`；
L5 于 03:18–03:20 部署 `/root/r19/` 并三次重启 agent；L1 亦在
03:11–03:24Z 持续操作（`R19-X-verdicts.md` V-05、`R19-L1-product-closure.md` §5）。
根因定为机制缺陷：非原子锁 + 无操作前归属复核。
- **位置核查（纠正任务书口径）**：锁文件实际位于**本机共享工作区**
`~/workspace/.r19-testmachine-lock`（即 `/home/hatch/workspace/`），
不在测试机磁盘上；各 lane 在本机创建/覆盖它，测试机上并无锁文件。
仓库内无任何锁脚本（`grep testmachine-lock` 全仓库仅 R19 报告命中），
旧机制是各 lane 手工 `echo "Lx …" > 锁文件` 的文字约定。
故按任务书要求：原子化锁脚本作为新文件落到仓库内，不只写文字纪律。

### 改动（文件 3）

`scripts/testmachine-lock.sh`（新建，可执行）：

- `:9–10`：锁本体改为目录 `$HOME/workspace/.testmachine-lock.d/`，内含
`owner` 文件一行 `LANE ISO8601-UTC`；旧轮次锁文件
`~/workspace/.r19-testmachine-lock` 废弃。
- `:49–80` `acquire <LANE> [--wait SECONDS]`：`mkdir` 单次原子抢占
（`:65`）；成功写 owner，失败打印当前持有者并退出 1，`--wait` 则每 5 秒
轮询直至超时；绝不覆盖他人锁。父目录预建在 `:64`（非原子步骤，不影响
抢占原子性——该缺口在自测中暴露并已修复，见 §证据）。
- `:82–92` `check <LANE>`：复核归属——锁须存在且 owner lane 名 == 本 lane，
否则退出 1。每次重启 / 部署 / agent 操作前必跑。
- `:94–104` `release <LANE>`：仅 owner 本人可放；他人持有时拒绝。
- `:106–112` `status`：打印持有者或 `free`。
- `:115–128` `break --reason "…"`：仅协调者可用，移除疑似僵死锁；
`--reason` 强制，原因与原持有者一并打印（须抄入本轮报告）。
不得用于抢占在用窗口。
- 锁目录可用 `FLMX_TESTMACHINE_LOCK_DIR` 覆盖（仅供测试；默认
`$HOME/workspace/.testmachine-lock.d`）。`bash -n` 语法检查通过。

### 证据（沙箱实测，锁目录指向 `/tmp/tml-test/`）

14 项全过：`status` 空闲报 `free`；`acquire L1` 成功；`acquire L5`
DENIED 退出 1 且 owner 仍为 L1；`check L5` 失败、`check L1` 通过；
`release L5` 拒绝；`status` 仍为 L1；`acquire L2 --wait 6` 轮询两次后
超时 DENIED（实测 10.17s，5 秒轮询粒度）；`break` 无 `--reason` 拒绝、
有 `--reason` 成功并打印原持有者；`break` 后 `status` 回 `free`；
双进程并发抢锁恰一人胜出（R1 胜、R2 DENIED）；owner 的 `acquire` +
`release` 全程通过；空闲时 `check` 失败。测试后临时锁目录已删除。

## 三、§流程：P-01 纪律全文（R20 起生效）

1. 抢锁一律走 `scripts/testmachine-lock.sh acquire <LANE>`（`mkdir` 式原子
抢占）；建锁失败即等待（`--wait` 轮询）或向协调者报备，**不得覆盖他人锁**，
不得手工 `echo … > 锁文件` 建锁。旧轮次锁文件
`~/workspace/.r19-testmachine-lock` 作废，唯一有效锁为
`~/workspace/.testmachine-lock.d/`。
2. 每次重启 / 部署 / agent 操作前必跑
`scripts/testmachine-lock.sh check <LANE>` 复核锁归属；锁内 lane 名须为
本 lane，不通过不得操作测试机。
3. 窗口起止与 lane 归属写入各路报告首行：报告首行须写明本路窗口起止
（UTC）、持有 lane、锁 `acquire` / `check` 的输出行。
4. 工作结束即 `release <LANE>` 放锁；放锁失败（他人持有）不得强行删除，
报协调者处理。
5. 疑似僵死锁只由协调者用 `break --reason "…"` 移除，原因与原持有者抄入
本轮报告；`break` 不得用于抢占在用窗口。
6. 违反者该窗口证据作废重跑：未持锁操作、覆盖他人锁、或未做操作前复核的
lane，其窗口内测试机证据一律作废，在干净窗口重跑（R19 V-05 污染边界
口径：L1 仅 t3 删腿一格排除、L5 Phase A A1–A3 作废重跑）。

## 四、回归

- 相关测试包：
- `go-backend/internal/http/handler`：`TestR1B3*` 全批绿；
`TestR1B3TunnelDeleteOnlineRealFailureAborts` 单测 hits=1 PASS。
- `go-gost/x/api`：`ok github.com/go-gost/x/api 0.696s`（含新增钉子测试）。
- 全量（`go test ./... -count=1`，go1.25.1）：
  - `go-backend`（重跑，全日志 `/home/hatch/tmp-r19t01p01/backend-full-rerun.log`）：
    16 包 ok、1 包 FAIL。`handler` 包 `ok 589.745s`（含 T-01 改动）、
    `store/repo` `ok 199.175s`、`tests/contract` `ok 94.130s`。
    唯一 FAIL 为 `internal/health` 的 `TestValidateMonitorTCPTarget/公网域名放行`
    （`monitor_ssrf_test.go:39`）——沙箱 fake-IP DNS 环境红，见下。
  - `go-gost/x`：全绿，退出码 0（0 FAIL；`api` 包 `ok 0.696s`，含新增钉子）。
  - `go-gost`（根模块）：`ok github.com/go-gost/gost/tests/lifecycle 25.455s`。
- 唯一红的签名与基线对照（已知沙箱 fake-IP DNS 环境红）：
  `ValidateMonitorTCPTarget("example.com:443")` 返回
  `目标地址不允许（内网/保留地址）: example.com`，wantErr=false。
  沙箱 DNS 把 `example.com` 解析为 `198.18.160.111`（CGNAT 保留段），SSRF
  守卫据此拒绝。基线对照：pristine d66042c 临时 worktree 上同测试同子项
  同文案 FAIL（`monitor_ssrf_test.go:39`），与本分支完全一致——红与本次
  改动无关（本改动未碰该包及任何产品代码），为环境红。
- 另记一次偶发红（已闭合，非产品失败）：首次 `go-backend` 全量中
  `internal/store/repo` FAIL（289.487s）；随后该包单独重跑两次均 PASS
  （235.045s、exit=0；第二次日志 `/tmp/repo-rerun.log`），且全量重跑中该包
  `ok 199.175s`——判定为并行 lane + 资源争用下的偶发失败，非产品回归。
- 另记一次环境红（已闭合，非产品失败）：`go-gost` 首次全量
  `tests/lifecycle` 链接失败 `no space left on device`——`/tmp` 为 512M
  tmpfs，三路全量并行时 `go-build*` 临时目录占满；改 `TMPDIR` 到大盘
  （`/home/hatch/tmp-r19t01p01`）重跑后 `ok 25.455s`。

## 五、未验证项

- 本轮不做真机验证：T-01 与 P-01 均为测试基建/流程项，无产品行为变化，
无需上测试机；R20 靶向轮复核时以本报告为准。
- R19 其余未验证项（SS 直连腿方法失效、远端/非 Debian 内核、浏览器 DOM 腿、
kill-freeze K2、VLESS 1MB 10.21s 观察）按 R19-FIX-PLAN 继续结转 R20，
不属本修复范围。

## 六、提交文件清单

| # | 文件 | 改动 |
|---|---|---|
| 1 | `go-backend/internal/http/handler/r1_b3_tunnel_delete_offline_test.go` | T-01：补 env、命中计数与断言、失败文案断言（25 行增、1 行删） |
| 2 | `go-gost/x/api/r19_fix_t01_batch_dup_test.go` | 新建：V-08 第 2 阶段 fail-closed 回归钉子 |
| 3 | `scripts/testmachine-lock.sh` | 新建：P-01 原子化互斥锁脚本（acquire/check/release/status/break） |
| 4 | `audit-out-132/r19/R19-FIX-T01P01-REPORT.md` | 本报告 |

产品代码 0 改动；版本字面量 0 改动；未合 main、未打 tag、未发版。
— R19-FIX-T01P01-REPORT END · 自证：本行之前正文字节数 11458 · END
