# R21-FIX-F01 修复报告 — switchTunnelEngine gost→realm 遗留 rule climiter 孤儿

**窗口：2026-10-25T09:08:21Z → 2026-10-25T09:52:00Z（UTC）｜ lane：R21-F01｜测试机锁：`R21-F01`，acquire 09:22:29Z（当时 free，mkdir 原子抢占）｜分支：`fix-r21-f01`**

- 基线（pin 死）：main @ `7d93792`（R20 修复 fix-r20 已合入并推 origin）。独立 worktree `~/workspace/.fixr21-f01` @ `7d93792`，分支 `fix-r21-f01`。只修 F-01，不合 main、不打 tag、不发版。
- 依据：`R21-FIX-PLAN.md`（F-01）、`R21-X-ledger.md` §三 R21-X-01、`R21-X-verdicts.md` V-02、`R21-L2-carryover.md`（§5/§6 CAND-01，沙箱 helper 探针 `TestR21L2DeleteForwardServicesOnNodeKeepsLimiter`）。
- ⚠️ **恢复未完（见 §7）：测试机 tailnet 通路自 09:35:40Z 起中断，锁仍由本 lane 持有、未释放；后端切回 r1gate-1.3.5 与节点上线复核待通路恢复后补跑。修复本身已验证（沙箱 + 真机腿全绿），DB 与 agent 已还原并验证，机器停在一致态。**

## 1. 结论

- **F-01 已修复。** `switchTunnelEngine` gost→realm 分支在 `DeleteService` 成功后补发按 ID 的 `DeleteCLimiters`（复用 `deleteForwardConnLimiterOnNode` 的按 ID 兜底，与删转发漏斗同口径）；realm→gost 反向切换核查无同型残留、无需改码（limiter 由 `syncForwardServices` 经 `ensureConnLimiterOnNode` 重建）。
- 沙箱先红后绿 3/3，邻接回归 9/9；`go vet` / `go build` / 前端 `tsc --noEmit` 全绿；`go test ./...` 产品失败 0（handler 包默认 10m 超时一格见 §4.3，30m 重跑 ok）。
- 真机引擎切换腿（R21 立案时未跑）已补跑：建带限流 gost 转发 48 → 切 realm → agent gost.json 无 `rule_conn_limit_48` 孤儿；切回 gost → limiter 重建且被引用；更新去限流、批量换隧道、删转发三腿不受影响。
- 无 BC。无断言更新（只新增测试）。未触碰任何有意设计护栏。

## 2. 修复内容

### 2.1 改动（唯一产品改动，+19/−1）

`go-backend/internal/http/handler/realm.go` `switchTunnelEngine` gost→realm 分支（原 `:363`）：

```go
// R21-X-01（F-01）：gost→realm 切换时旧引擎服务删后须清掉
// 该转发的 rule_conn_limit_<id>。……（注释全文见 diff）
if err := h.deleteForwardServicesOnNode(&forwards[i], nodeID); err != nil {
    collect("旧引擎服务删除", err)
    continue
}
h.deleteForwardConnLimiterOnNode(&forwards[i], nodeID)
```

- 只在 `DeleteService` 成功后发 `DeleteCLimiters`（先删服务、后清 limiter；与 `deleteForwardServicesOnNodeBatch` / `controlForwardServices` 的 DeleteService 分支同口径）。离线节点的删除失败仍由既有 `collect` 记 deferred，不清 limiter，与服务删除的命运一致（离线时服务未删、limiter 仍被引用，不成孤儿）。
- 按 ID 兜底：从未带过限流的转发亦发一次 `DeleteCLimiters`（R20-X-01 ② 同款；agent 侧删不存在者为 noop）。`user_conn_limit_*` 共享、永不删（helper 内已保证）。

### 2.2 为何清理不进 `deleteForwardServicesOnNode` 本身（设计锁定）

该 helper 另有两个调用方是“删旧服务后立即重建同一服务”的重建路径——`syncForwardServicesWithWarnings` 的 UpdateService 回退（`control_plane.go:469`）、默认监听回退（`:535`）。重建服务仍引用原 limiter；helper 内清会让重建服务挂上已删的 limiter。清理只落在切换点（服务永久迁出 gost），不在共享 helper 内。锁定测试 `TestR21F01DeleteForwardServicesOnNodeAloneKeepsLimiter` 钉住此语义（helper 单独调用仍 0 次 `DeleteCLimiters`）。

### 2.3 反向 realm→gost 核查（无同型残留，无需改码）

- realm 渲染无任何 limit/conn 字段（`RenderRealm` 的 `RealmEndpoint` 无 limit 字段；`forwardHasIneffectiveLimits` 只作警告谓词），realm 侧从不产生 `rule_conn_limit_*`，拆除时无 limiter 可清。
- 切回 gost 时 `syncForwardServices` 重建服务并经 `ensureConnLimiterOnNode` 重建同名 rule limiter（`AddCLimiters`），不是孤儿。沙箱反向测试断言：切回后 `AddCLimiters` 含同名 limiter、且 `DeleteCLimiters` 总数仍为去程的 2 次（无多发）。
- 真机切回腿同证（§6）。

## 3. 新增测试

`go-backend/internal/http/handler/r21_fix_f01_test.go`（新文件，3 测试，均用真实 WS 传输的 `cleanupAgent` + mock 节点）：

| 测试 | 断言 |
|---|---|
| `TestR21F01EngineSwitchGostToRealmDeletesRuleLimiter` | 建两条同隧道转发（带限流 maxConn=7 / 从未带限流）→ 切 realm → 恰 2 次 `DeleteCLimiters`（每转发 1 次，名按 ID），且均在 `DeleteService` 之后 |
| `TestR21F01EngineSwitchRealmToGostRecreatesLimiter` | 去程 2 次 `DeleteCLimiters` 后切回 gost → 有 `UpdateService`、有 `AddCLimiters`（同名 rule limiter）、`DeleteCLimiters` 总数仍为 2 |
| `TestR21F01DeleteForwardServicesOnNodeAloneKeepsLimiter` | helper 单独调用只发 `DeleteService`、0 次 `DeleteCLimiters`（§2.2 设计锁定） |

## 4. 沙箱回归

### 4.1 先红后绿（`R21-FIX-F01-red.log` / `R21-FIX-F01-green.log`）

- 红（修复暂存前，基线态）：主回归与反向回归 FAIL——`expected exactly 2 DeleteCLimiters commands (one per forward), got 0: []`；设计锁定腿 PASS。2 FAIL / 1 PASS。
- 绿（修复恢复后）：3/3 PASS（0.45s / 0.57s / 0.70s）。

### 4.2 邻接回归（`R21-FIX-F01-neighbour.log`，9/9 PASS）

- R19 删转发漏斗 ×3：`TestR19F03DeleteForwardBatchRemovesConnLimiter`、`TestR19F03DeleteForwardControlRemovesConnLimiter`、`TestR19F03DeleteForwardWithoutLimitsStillDeletesRuleLimiterByID`。
- R20 更新/删除 ×4：`TestR20F01DeleteAfterLimitsClearedSendsDeleteCLimiters`、`TestR20F01UpdateDropsLimitsDeletesStaleLimiter`、`TestR20F01UpdateKeepsLimitsSendsNoDeleteCLimiters`、`TestR20F01UpdateThenDeleteLeavesNoOrphan`。
- realm 切换 ×2：`TestEngineSwitchRealmAccountingSettlesAtSwitch`、`TestRealmEngineSwitchConverges`。

### 4.3 全量与静态检查

- `go vet ./...` exit 0（`R21-FIX-F01-vet.log`）；`go build ./...` exit 0（`R21-FIX-F01-build.log`）；前端 `tsc --noEmit` exit 0（`R21-FIX-F01-tsc.log`；前端零改动，node_modules 借主 checkout 的同 lockfile 依赖临时软链跑 tsc，跑后已删软链、工作区无残留）。
- `go test ./...`（默认 10m 超时，`R21-FIX-F01-gotest-full-10m.log`）：除下述两格外全部包 ok；**产品失败 0**——
  - `internal/health` FAIL：仅 `TestValidateMonitorTCPTarget/公网域名放行` 一格，为已知沙箱环境红（本机 DNS 是假的），非产品失败；
  - `internal/http/handler` 10m0s 超时：**零个测试失败**（`panic: test timed out after 10m0s`，报警时正在跑的 `TestR2B10MonitorAccessAdminField` 仅跑了 2s；该测试单跑 0.84s PASS）。2 CPU 沙箱上 handler 全包（1342+ 测试）跑满 10m 属墙钟不足，非产品失败。
- handler 包 30m 重跑（`R21-FIX-F01-gotest-handler-30m.log`）：**ok，753.869s，0 FAIL**。
- `go-gost` 模块零改动，未重跑（本修复只涉 `go-backend`）。

## 5. 真机腿（测试机 Lightsail，公网 16.146.199.68 / tailnet 100.80.182.11）

驱动：`R21-FIX-F01-legs.py`（复用 R21-L2 驱动模式，新增 `tunget` / `tunengine` 腿；token 缓存 `/root/r21-f01/.token` 600）。部署：`R21-FIX-F01-deploy.sh`（`FROM flmx-backend:r1gate-1.3.5` + 修复版 paneld 覆盖）。

### 5.1 Pristine 基线（09:29:53Z，`tm-pristine.log`）

- counts 1/4/6/578/18 PASS；gost md5 `d48f09e1`，services 12，climiters `['rule_conn_limit_12', 'rule_conn_limit_2']`，refs []；节点 `w10-test` status 1、`dev (debian/amd64)`、forwardMode agent。
- 隧道 1 `W10-T1`：type 1、engine `''`、入口节点 1（tls）、无出节点——可切 realm。
- DB 备份（09:30:01Z，破坏性操作前，`tm-db-backup.log`）：`sqlite3 .backup` → `/root/r21-f01/gost.db.pristine`，md5 `fb90bcc092221cf110cbc6c9eddb86fa`；表 50；realm 内核行 1（切换前已存在，非本腿产生）。

### 5.2 部署修复版后端（09:30:37Z，`tm-deploy.log`）

- 修复版 paneld md5 `de45573f95f1cb9254bb74999ef3cc8d`（本地 `go build`，CGO_ENABLED=0）；镜像 `flmx-backend:r21-f01`（`598e916feccc`）；容器内 `/app/paneld` md5 一致；healthy；GET / 6366 → 200；gost md5 未动 `d48f09e1`；agent active。agent 未动（pristine `aff338e3`，无 F-04 自愈干扰——孤儿若留必现形）。

### 5.3 主腿：gost→realm（09:30:59Z，`tm-leg-switch-realm.log`）

- 建转发 48 `r21-f01-sw`（隧道 1，maxConn=5，`203.0.113.9:9001`，入口端口 13810）：code 0；gost services 14，新增 `rule_conn_limit_48` 且被引用（`tm-leg-mkfwd.log`）。
- `POST /tunnel/update` 全量回发（`/tunnel/get` 取现值、只改 `engine`）：code 0，engine 持久化为 `realm`。
- **切后 gost：services 14→8，climiters 回 pristine `['rule_conn_limit_12', 'rule_conn_limit_2']`，refs []——`rule_conn_limit_48` 已删，无孤儿。**（隧道 1 名下转发 1、6、48 共 6 个服务一并迁出 gost，数量吻合。）
- realm 数据面在位：agent `/etc/flux_agent/kernels/realm/run/config.toml` 含 3 个 endpoint（forward 1: 31080、6: 14601、48: 13810，目标与 DB 一致）。
- journal 佐证缺口（见 §8.1）：agent 单元 `StandardOutput=null`/`StandardError=null`（unit md5 `a12afd9c`，与 L2 pristine 一致；09:01 重写、09:02:01 重启后 PID 838368 再无 journal 输出），切换窗口内 `DeleteCLimiters` 的 journal 计数不可观测。决定性证据是状态面（gost.json 孤儿消失 + realm 配置在位）与沙箱命令级记录（每转发 `DeleteService`→`DeleteCLimiters`）；同窗口内无其他可删该 limiter 的机制（pristine agent 无 F-04 自愈；面板 `cleanNodeConfigs` 不清 CLimiters，L2 §5 已 pin 死）。

### 5.4 反向腿：realm→gost（09:32:45Z，`tm-leg-switch-back.log`）

- code 0，engine 持久化回 `''`；gost services 回 14，`rule_conn_limit_48` 重建且被引用；realm 配置收敛为 0 endpoint（仅 `[log]`/`[network]`）；端口 13810/31080/14601 均由 `flux_agent`（PID 838368）监听。

### 5.5 既有腿回归（均 code 0、failures=0）

- 更新去限流（09:32:58Z，`tm-leg-update.log`）：转发 48 `{maxConn: 0, ipMaxConn: 0}` → limiter 48 消失、services 仍 14。
- 批量换隧道（09:33:06Z，`tm-leg-batchtunnel.log`）：新建转发 49（隧道 1，maxConn=5）→ batch-change 至隧道 2（successCount 1，新端口 12957）→ limiter 49 仍在且被引用、services 16，无孤儿、无丢失。
- 删转发（09:33:17Z，`tm-leg-delete.log`）：删 49 → services 14、limiter 49 消失；删 48 → services 12、climiters 回 `[12, 2]`；counts 1/4/6/578/18 PASS。

## 6. 恢复对账

### 6.1 已完成并验证（09:33:52Z–09:34:19Z）

- DB 还原（`tm-db-restore.log`）：停后端容器 → 以备份覆盖 `gost.db`（md5 `fb90bcc0` 与备份一致）→ 删 `-wal`/`-shm` → 起后端（仍为修复版）→ counts 1/4/6/578/18 PASS、隧道 1 engine `''`、表 50、realm 内核行 1（`tm-restore-backend.log`）。
- agent 还原（`tm-agent-restore.log`）：gost.json 以 `/root/r21-l2/gost.json.pristine` 覆盖 → `reset-failed` → `restart flux_agent` → agent active；gost md5 `d48f09e1`、agent md5 `aff338e3`、services 12、climiters `[12, 2]`、refs []。
- 说明：删腿后 gost md5 曾为 `52a345e4`（`tm-gost-diff.log`）——仅服务数组顺序不同（切换往返删后重建的服务追加在尾），集合与字节长度（6237）完全一致；已按 L2 恢复口径以 pristine 文件覆盖 + 重启 agent 根治，md5 已回 `d48f09e1`。

### 6.2 待完成（测试机通路中断，见 §7）

1. 节点重连复核：agent 重启后节点 status 回 1、gost md5 过一个同步周期（~65s）仍为 `d48f09e1`。
2. 后端切回：compose tag `flmx-backend:r21-f01` → `r1gate-1.3.5`，`up -d backend`、healthy、运行中 paneld md5 `6c67f9f7670b69bd1d78061688f2da17`、GET / 200。
3. 删镜像 `flmx-backend:r21-f01`；清机上密钥类临时文件（`/root/r21-f01/.token`）。
4. 释放测试机锁（`testmachine-lock.sh release R21-F01`）。
5. 锁在 §6.2 完成前由本 lane 继续持有，他人不得操作测试机。

## 7. 测试机通路中断（09:35:40Z 起，未恢复）

- 现象：自 09:35:40Z 起，全部 SSH（`tsconnect.py` 经运行时代理 3130 端口 CONNECT 至 tailnet 100.80.182.11:22）均报 `proxy closed during CONNECT`；09:40、09:42、09:45 三次重试同错。最后一次成功连接 09:34:19Z（agent 还原验证）。
- 排查：公网出口代理（3128）正常（example.com:443 CONNECT 200）；`tailscale status` 显示本机已入 tailnet、目标设备 `ip-172-26-9-119 100.80.182.11` 在册；经 3130 隧道代理 curl 目标 :6366/:22 均为 http=000；公网 IP 16.146.199.68 的 :22/:80/:6365/:6366 经 3128 亦为 000（平台出口本就阻断公网 SSH，且面板端口只绑回环/容器网段，该面无结论）。无待审批项（`permissions.list_pending` 为空）。
- 定性：中断始于 agent 重启后约 1 分钟，但 agent 重启与 tailscaled/网络无关；更可能为尾端 tailscaled 或隧道代理路由侧波动/许可过期。按 tailscale 文档口径，不再高频重试，等通路恢复或用户侧确认后补跑 §6.2。
- 末态（最后一次验证 09:34:19Z）：DB pristine、agent pristine、gost pristine；唯一待归位的是后端镜像 tag（仍为修复版 `r21-f01`——与 pristine DB/agent 完全兼容，功能面等价于 r1gate-1.3.5 + 本修复）。

## 8. 未验证清单

1. **§6.2 四项**：节点上线复核、后端切回 r1gate-1.3.5、删 r21-f01 镜像、放锁——通路中断所致，待补跑（非产品缺口）。
2. **F-01 多节点真机腿**：测试机仅 1 节点，引擎切换的多节点形态未在真机跑；沙箱为单节点 mock（按节点归因的 DeleteCLimiters 计数只在删转发/更新路径由双 mock 覆盖，R21-L2）。切换循环本身按 `uniqueInt64s(nodes)` 逐节点发清理，与删转发漏斗同口径。
3. **journal `DeleteCLimiters` 计数**：见 §5.3——agent 单元自 09:02:01 重启后无 journal 输出（StandardOutput/StandardError=null），该佐证面不可观测；不影响结论（状态面 + 沙箱命令级证据已闭合）。
4. 远端节点/非 Debian 内核、浏览器 DOM 腿、真实 Gist 端到端：与本修复无关，沿用 R21 未验证清单。

## 9. 证据索引（同目录落盘）

| 文件 | 内容 |
|---|---|
| `R21-FIX-F01-red.log` | 沙箱红：2 FAIL（主回归/反向回归，`got 0 DeleteCLimiters`）+ 1 PASS |
| `R21-FIX-F01-green.log` | 沙箱绿：3/3 PASS |
| `R21-FIX-F01-neighbour.log` | 邻接回归 9/9 PASS（R19 ×3、R20 ×4、realm 切换 ×2） |
| `R21-FIX-F01-gotest-full-10m.log` | `go test ./...` 默认 10m：仅 health 环境红 + handler 超时（零测试失败） |
| `R21-FIX-F01-gotest-handler-30m.log` | handler 包 30m 重跑：ok 753.869s，0 FAIL |
| `R21-FIX-F01-vet.log` / `R21-FIX-F01-build.log` / `R21-FIX-F01-tsc.log` | vet/build/tsc 均 exit 0 |
| `R21-FIX-F01-deploy.sh` / `R21-FIX-F01-legs.py` | 真机部署脚本 / 驱动 |
| `tm-pristine.log` / `tm-db-backup.log` | pristine 对账 / DB 备份 |
| `tm-deploy.log` | 修复版后端部署 |
| `tm-leg-mkfwd.log` / `tm-leg-switch-realm.log` / `tm-leg-switch-realm-evidence*.log` | 建转发 / 切 realm / 证据（realm 配置、journal、panel 日志） |
| `tm-leg-switch-back.log` / `tm-leg-update.log` / `tm-leg-batchtunnel.log` / `tm-leg-delete.log` | 切回 / 更新 / 换隧道 / 删除 |
| `tm-gost-diff.log` | 删腿后 gost md5 顺序差（集合一致） |
| `tm-db-restore.log` / `tm-restore-backend.log` / `tm-agent-restore.log` | DB 还原 / 后端重启验证 / agent 还原 |
| `lock-check-1.log` | 锁复核记录（部署/DB 还原/agent 重启前各一次，均 check ok） |

## 10. 锁说明

测试机锁 `R21-F01 2026-10-25T09:22:29Z` 仍持有、未释放（§6.2 待补跑）。注：任务书称锁脚本在仓库内 `scripts/testmachine-lock.sh`，但本基线 checkout 的 `scripts/` 内无此文件；使用的是与之同一内容的正本 `~/workspace/.fixr20/scripts/testmachine-lock.sh`（R19-PROC-01，mkdir 原子抢占；锁本体 `~/workspace/.testmachine-lock.d/`，owner 单行 `R21-F01 2026-10-25T09:22:29Z`）。`check` 在每次部署/重启前复核，均通过（见 `lock-check-1.log`）。
— R21-FIX-F01-REPORT END · 自证：本行之前正文字节数 16665 · END
