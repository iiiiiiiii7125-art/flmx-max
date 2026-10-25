# R21-FIX-F01 修复报告 — switchTunnelEngine gost→realm 遗留 rule climiter 孤儿

**窗口：2026-10-25T09:08:21Z → 09:52:00Z（首轮）＋ 2026-10-25T12:04:27Z → 12:30:13Z（续做，§11）｜ lane：R21-F01｜分支：`fix-r21-f01` @ `01336d4`**

- 基线（pin 死）：main @ `88f10db`（`feat: inbound idle timeout unlimited (-1 renders as 365d)`；本分支变基后 merge-base = `88f10db`，父提交即 main 尖端；首轮开工时基线为 `7d93792`，后由主控指令变基至 `88f10db`，提交内容不变、见 §11.1）。独立 worktree `~/workspace/.fixr21-f01`，分支 `fix-r21-f01`。只修 F-01，不合 main、不打 tag、不发版。
- 依据：`R21-FIX-PLAN.md`（F-01）、`R21-X-ledger.md` §三 R21-X-01、`R21-X-verdicts.md` V-02、`R21-L2-carryover.md`（§5/§6 CAND-01，沙箱 helper 探针 `TestR21L2DeleteForwardServicesOnNodeKeepsLimiter`）。
- ⚠️ **阅读口径（两台测试机）**：§5–§7 描述的是**旧测试机** ip-172-26-9-119（tailnet 100.80.182.11），该机已于 2026-10-25 下线；§11 续做在**新测试机** ip-172-26-4-158（tailnet 100.123.93.34）上重跑最小腿并闭合旧报告 §8.3 的 journal 证据缺口。两台机器的 pristine 基线不同（旧机 1/4/6/578/18，新机 1/0/0/1/1），不得混用。

## 1. 结论

- **F-01 已修复。** `switchTunnelEngine` gost→realm 分支在 `DeleteService` 成功后补发按 ID 的 `DeleteCLimiters`（复用 `deleteForwardConnLimiterOnNode` 的按 ID 兜底，与删转发漏斗同口径）；realm→gost 反向切换核查无同型残留、无需改码（limiter 由 `syncForwardServices` 经 `ensureConnLimiterOnNode` 重建）。
- 沙箱先红后绿 3/3，邻接回归 9/9；`go vet` / `go build` / 前端 `tsc --noEmit` 全绿；`go test ./...` 产品失败 0（handler 包默认 10m 超时一格见 §4.3，30m 重跑 ok；续做重跑见 §11.2）。
- 真机引擎切换腿（R21 立案时未跑）已补跑：旧机建带限流 gost 转发 48 → 切 realm → agent gost.json 无 `rule_conn_limit_48` 孤儿；切回 gost → limiter 重建且被引用；更新去限流、批量换隧道、删转发三腿不受影响。新机最小腿（§11.5）复证同一结论，并补到旧机缺失的 journal 命令级证据：切换窗口内 agent journal 有且仅有一条 `DeleteCLimiters {"limiter":"rule_conn_limit_1"}`。
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
- 真机切回腿同证（§6，旧机；§11.5，新机）。

## 3. 新增测试

`go-backend/internal/http/handler/r21_fix_f01_test.go`（新文件，3 测试，均用真实 WS 传输的 `cleanupAgent` + mock 节点）：

| 测试 | 断言 |
|---|---|
| `TestR21F01EngineSwitchGostToRealmDeletesRuleLimiter` | 建两条同隧道转发（带限流 maxConn=7 / 从未带限流）→ 切 realm → 恰 2 次 `DeleteCLimiters`（每转发 1 次，名按 ID），且均在 `DeleteService` 之后 |
| `TestR21F01EngineSwitchRealmToGostRecreatesLimiter` | 去程 2 次 `DeleteCLimiters` 后切回 gost → 有 `UpdateService`、有 `AddCLimiters`（同名 rule limiter）、`DeleteCLimiters` 总数仍为 2 |
| `TestR21F01DeleteForwardServicesOnNodeAloneKeepsLimiter` | helper 单独调用只发 `DeleteService`、0 次 `DeleteCLimiters`（§2.2 设计锁定） |

## 4. 沙箱回归（首轮）

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

## 5. 真机腿（旧测试机 Lightsail，公网 16.146.199.68 / tailnet 100.80.182.11）

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
- journal 佐证缺口（见 §8.1）：agent 单元 `StandardOutput=null`/`StandardError=null`（unit md5 `a12afd9c`，与 L2 pristine 一致；09:01 重写、09:02:01 重启后 PID 838368 再无 journal 输出），切换窗口内 `DeleteCLimiters` 的 journal 计数不可观测。决定性证据是状态面（gost.json 孤儿消失 + realm 配置在位）与沙箱命令级记录（每转发 `DeleteService`→`DeleteCLimiters`）；同窗口内无其他可删该 limiter 的机制（pristine agent 无 F-04 自愈；面板 `cleanNodeConfigs` 不清 CLimiters，L2 §5 已 pin 死）。**该缺口已由 §11.5 新机腿闭合。**

### 5.4 反向腿：realm→gost（09:32:45Z，`tm-leg-switch-back.log`）

- code 0，engine 持久化回 `''`；gost services 回 14，`rule_conn_limit_48` 重建且被引用；realm 配置收敛为 0 endpoint（仅 `[log]`/`[network]`）；端口 13810/31080/14601 均由 `flux_agent`（PID 838368）监听。

### 5.5 既有腿回归（均 code 0、failures=0）

- 更新去限流（09:32:58Z，`tm-leg-update.log`）：转发 48 `{maxConn: 0, ipMaxConn: 0}` → limiter 48 消失、services 仍 14。
- 批量换隧道（09:33:06Z，`tm-leg-batchtunnel.log`）：新建转发 49（隧道 1，maxConn=5）→ batch-change 至隧道 2（successCount 1，新端口 12957）→ limiter 49 仍在且被引用、services 16，无孤儿、无丢失。
- 删转发（09:33:17Z，`tm-leg-delete.log`）：删 49 → services 14、limiter 49 消失；删 48 → services 12、climiters 回 `[12, 2]`；counts 1/4/6/578/18 PASS。

## 6. 恢复对账（旧机）

### 6.1 已完成并验证（09:33:52Z–09:34:19Z）

- DB 还原（`tm-db-restore.log`）：停后端容器 → 以备份覆盖 `gost.db`（md5 `fb90bcc0` 与备份一致）→ 删 `-wal`/`-shm` → 起后端（仍为修复版）→ counts 1/4/6/578/18 PASS、隧道 1 engine `''`、表 50、realm 内核行 1（`tm-restore-backend.log`）。
- agent 还原（`tm-agent-restore.log`）：gost.json 以 `/root/r21-l2/gost.json.pristine` 覆盖 → `reset-failed` → `restart flux_agent` → agent active；gost md5 `d48f09e1`、agent md5 `aff338e3`、services 12、climiters `[12, 2]`、refs []。
- 说明：删腿后 gost md5 曾为 `52a345e4`（`tm-gost-diff.log`）——仅服务数组顺序不同（切换往返删后重建的服务追加在尾），集合与字节长度（6237）完全一致；已按 L2 恢复口径以 pristine 文件覆盖 + 重启 agent 根治，md5 已回 `d48f09e1`。

### 6.2 未完成（测试机通路中断，见 §7；旧机已下线，下列项已无执行对象，见 §11.1）

1. 节点重连复核：agent 重启后节点 status 回 1、gost md5 过一个同步周期（~65s）仍为 `d48f09e1`。
2. 后端切回：compose tag `flmx-backend:r21-f01` → `r1gate-1.3.5`，`up -d backend`、healthy、运行中 paneld md5 `6c67f9f7670b69bd1d78061688f2da17`、GET / 200。
3. 删镜像 `flmx-backend:r21-f01`；清机上密钥类临时文件（`/root/r21-f01/.token`）。
4. 释放测试机锁（`testmachine-lock.sh release R21-F01`）。
5. 锁在 §6.2 完成前由本 lane 继续持有，他人不得操作测试机。

## 7. 测试机通路中断（09:35:40Z 起，未恢复；旧机）

- 现象：自 09:35:40Z 起，全部 SSH（`tsconnect.py` 经运行时代理 3130 端口 CONNECT 至 tailnet 100.80.182.11:22）均报 `proxy closed during CONNECT`；09:40、09:42、09:45 三次重试同错。最后一次成功连接 09:34:19Z（agent 还原验证）。
- 排查：公网出口代理（3128）正常（example.com:443 CONNECT 200）；`tailscale status` 显示本机已入 tailnet、目标设备 `ip-172-26-9-119 100.80.182.11` 在册；经 3130 隧道代理 curl 目标 :6366/:22 均为 http=000；公网 IP 16.146.199.68 的 :22/:80/:6365/:6366 经 3128 亦为 000（平台出口本就阻断公网 SSH，且面板端口只绑回环/容器网段，该面无结论）。无待审批项（`permissions.list_pending` 为空）。
- 定性：中断始于 agent 重启后约 1 分钟，但 agent 重启与 tailscaled/网络无关；更可能为尾端 tailscaled 或隧道代理路由侧波动/许可过期。按 tailscale 文档口径，不再高频重试，等通路恢复或用户侧确认后补跑 §6.2。
- 末态（最后一次验证 09:34:19Z）：DB pristine、agent pristine、gost pristine；唯一待归位的是后端镜像 tag（仍为修复版 `r21-f01`——与 pristine DB/agent 完全兼容，功能面等价于 r1gate-1.3.5 + 本修复）。
- 后续：旧机已下线（2026-10-25），§6.2 余项不再执行；新机接替为测试机，续做见 §11。

## 8. 未验证清单（首轮）

1. **§6.2 四项**：节点上线复核、后端切回 r1gate-1.3.5、删 r21-f01 镜像、放锁——通路中断所致，待补跑（非产品缺口）。**旧机已下线，此四项无执行对象；新机等价项已在 §11.5 完成（节点上线复核、后端切回 new1 基线、删 r21-f01-v2 镜像、放锁）。**
2. **F-01 多节点真机腿**：测试机仅 1 节点，引擎切换的多节点形态未在真机跑；沙箱为单节点 mock（按节点归因的 DeleteCLimiters 计数只在删转发/更新路径由双 mock 覆盖，R21-L2）。切换循环本身按 `uniqueInt64s(nodes)` 逐节点发清理，与删转发漏斗同口径。
3. **journal `DeleteCLimiters` 计数**：见 §5.3——旧机 agent 单元自 09:02:01 重启后无 journal 输出（StandardOutput/StandardError=null），该佐证面不可观测；不影响结论（状态面 + 沙箱命令级证据已闭合）。**已由 §11.5 新机腿闭合：切换窗口内 journal 有且仅有一条 `DeleteCLimiters {"limiter":"rule_conn_limit_1"}`。**
4. 远端节点/非 Debian 内核、浏览器 DOM 腿、真实 Gist 端到端：与本修复无关，沿用 R21 未验证清单。

## 9. 证据索引（首轮，同目录落盘）

| 文件 | 内容 |
|---|---|
| `R21-FIX-F01-red.log` | 沙箱红：2 FAIL（主回归/反向回归，`got 0 DeleteCLimiters`）+ 1 PASS |
| `R21-FIX-F01-green.log` | 沙箱绿：3/3 PASS |
| `R21-FIX-F01-neighbour.log` | 邻接回归 9/9 PASS（R19 ×3、R20 ×4、realm 切换 ×2） |
| `R21-FIX-F01-gotest-full-10m.log` | `go test ./...` 默认 10m：仅 health 环境红 + handler 超时（零测试失败） |
| `R21-FIX-F01-gotest-handler-30m.log` | handler 包 30m 重跑：ok 753.869s，0 FAIL |
| `R21-FIX-F01-vet.log` / `R21-FIX-F01-build.log` / `R21-FIX-F01-tsc.log` | vet/build/tsc 均 exit 0 |
| `R21-FIX-F01-deploy.sh` / `R21-FIX-F01-legs.py` | 真机部署脚本 / 驱动（旧机） |
| `tm-pristine.log` / `tm-db-backup.log` | pristine 对账 / DB 备份（旧机） |
| `tm-deploy.log` | 修复版后端部署（旧机） |
| `tm-leg-mkfwd.log` / `tm-leg-switch-realm.log` / `tm-leg-switch-realm-evidence*.log` | 建转发 / 切 realm / 证据（realm 配置、journal、panel 日志）（旧机） |
| `tm-leg-switch-back.log` / `tm-leg-update.log` / `tm-leg-batchtunnel.log` / `tm-leg-delete.log` | 切回 / 更新 / 换隧道 / 删除（旧机） |
| `tm-gost-diff.log` | 删腿后 gost md5 顺序差（集合一致） |
| `tm-db-restore.log` / `tm-restore-backend.log` / `tm-agent-restore.log` | DB 还原 / 后端重启验证 / agent 还原（旧机） |
| `lock-check-1.log` | 锁复核记录（部署/DB 还原/agent 重启前各一次，均 check ok） |

## 10. 锁说明（首轮）

测试机锁 `R21-F01 2026-10-25T09:22:29Z` 曾持有。注：任务书称锁脚本在仓库内 `scripts/testmachine-lock.sh`，但本基线 checkout 的 `scripts/` 内无此文件；使用的是与之同一内容的正本 `~/workspace/.fixr20/scripts/testmachine-lock.sh`（R19-PROC-01，mkdir 原子抢占；锁本体 `~/workspace/.testmachine-lock.d/`，owner 单行 `R21-F01 2026-10-25T09:22:29Z`）。`check` 在每次部署/重启前复核，均通过（见 `lock-check-1.log`）。

**订正（§11.1）**：该锁已于 2026-10-25T09:53:14Z 由本 lane 释放（持有自 09:22:29Z）；放锁后 3 秒由 `R21-F03` 持有（acquire 09:53:17Z）。本节首轮正文“仍持有、未释放”已过期，以 §11.1 暂停附记为准。

---

## 11. 续做（2026-10-25T12:04:27Z → 12:30:13Z）

任务书：R21 修复 F-01 续做（只修、不合 main、不打 tag、不发版）。自验现状 → 完成修复再验 → 真机腿核对（旧机日志完整则不重跑、证据不全则在新机重跑）→ 逐项报告 → 提交。

### 11.1 暂停附记与现状自验

- **暂停（2026-10-25T09:52:46Z 用户指令，09:53:14Z 执行）**：本 lane 已暂停：无在跑测试（本 lane 全部已完成；ps 中一个 `go test ./...` 属他 lane `~/tmp-r21unl`，未动）；不再起新测试、不再用测试机；分支与 worktree 保持原样（未合 main、未删 worktree、未再提交）。
- **锁已放**：`R21-F01` 于 2026-10-25T09:53:14Z 释放（持有自 09:22:29Z）；放锁后状态 free。
- **锁复核附记（2026-10-25T09:53:47Z）**：父控提示锁仍由本 lane 持有——实查：锁已于 09:53:14Z 由本 lane 释放生效；**现由 `R21-F03` 持有（acquire 2026-10-25T09:53:17Z，即本 lane 放锁后 3 秒）**，并非本 lane 持有，无需、亦不得再次释放（非 owner 会被脚本拒绝）。本 lane 自暂停指令后未再做任何测试机操作。
- **测试机末态（最后一次验证 09:34:19Z，tailnet 中断前）**：DB pristine、gost.json `d48f09e1`、agent `aff338e3`；待补跑项（节点上线复核、后端切回 r1gate-1.3.5、删 r21-f01 镜像、清 `.token`）因通路中断未完成，机器上后端仍为修复版 `flmx-backend:r21-f01`。**旧机随后下线，§6.2 四项无执行对象。**
- **现状自验（12:04Z）**：工作区 `~/workspace/.fixr21-f01`，分支 `fix-r21-f01`，HEAD `01336d4 fix(r21-f01): drop orphaned rule conn limiter on gost->realm engine switch`，merge-base(main) = `88f10db`（变基已完成，提交内容与首轮一致：`realm.go` +19/−1、`r21_fix_f01_test.go` 新文件、报告与日志）；未提交改动仅 `M audit-out-132/r21/R21-FIX-F01-REPORT.md`（即本文件首轮正文后的两则附记，现已并入 §11.1 正文）。`git status` 无其他改动；无被 kill 的本 lane 测试（暂停时无在跑测试）。

### 11.2 沙箱再验（变基后 checkout，全部重跑，不继承首轮结论）

- `go vet ./...` exit 0（`R21-FIX-F01-verify2-vet.log`；仅一条 GOPATH/GOROOT 同目录 warning，与产品无关）；`go build ./...` exit 0（`R21-FIX-F01-verify2-build.log`）。
- F-01 三测试重跑（`R21-FIX-F01-verify2-green.log`）：**3/3 PASS**——主回归 1.45s、反向回归 3.05s、设计锁定 1.87s；包耗时 6.473s。
- 邻接回归重跑（`R21-FIX-F01-verify2-neighbour.log`）：**9/9 PASS**（R19 ×3、R20 ×4、realm 切换 ×2；包耗时 15.476s）。
- 前端 `tsc --noEmit` exit 0（`R21-FIX-F01-verify2-tsc.log`，0 字节；前端零改动，node_modules 借主 checkout 同 lockfile 依赖临时软链，跑后已删）。
- `go test ./... -p 1 -timeout 30m`（`R21-FIX-F01-verify2-gotest-full2.log`，耗时约 17 分钟，`FULL2 exit=1`）：**产品失败 0**——18 个有测试的包全部 ok（handler `ok 713.859s`、store/repo `ok 140.230s`、tests/contract `ok 89.429s`、其余 15 包 ok；http 与 store/model 无测试文件）；唯一 FAIL 包 `internal/health` 仅一格 `TestValidateMonitorTCPTarget/公网域名放行`（`example.com:443` 被判内网/保留地址），与首轮同一格、同一机理——已知沙箱 fake-DNS 环境红，非产品失败。`go-gost` 模块零改动，未重跑。
- **首跑中断事故**：第一次全量（默认并行，`R21-FIX-F01-verify2-gotest-full.log`，仅跑到 `http/client` 即中断）与一次修复版 paneld 构建（`aborted`）在沙箱多 lane 并行、内存吃紧时被打断（8G 沙箱当时已用 6.1G，另有 R21-F02/F03 在跑全量/tsc）；无产品失败、无测试失败，日志保留为中断证据；改 `-p 1` 重跑后全程通过。tsc 首跑会话亦同因失联，重跑 exit 0。

### 11.3 旧机 tm-leg 日志核对（结论：完整，无需因“腿中断”重跑）

逐文件复核提交内旧机日志（`tm-leg-mkfwd.log`、`tm-leg-switch-realm.log`、`tm-leg-switch-realm-evidence*.log` ×4、`tm-leg-switch-back.log`、`tm-leg-update.log`、`tm-leg-batchtunnel.log`、`tm-leg-delete.log`、`tm-pristine.log`、`tm-deploy.log`、`tm-gost-diff.log`、`tm-db-restore.log`、`tm-restore-backend.log`、`tm-agent-restore.log`、`tm-db-backup.log`、`lock-check-1.log`）：

- 五条腿（建转发、切 realm、切回、更新去限流、批量换隧道、删转发）**全部 `failures=0`**，每腿起止时间、code 0、gost 摘要（services/climiters/refs）、realm 配置均在位；部署与两次还原日志完整；无任何一腿中途中断。
- 唯一证据缺口是 §8.3 的 journal `DeleteCLimiters` 计数（旧机 agent 单元 StandardOutput=null，切换窗口内无 journal 输出）。该缺口在首轮已被裁定为“不影响结论”，但属于“证据不全”——按任务书条件，**在新机重跑最小腿**（建带限流转发 → 切 gost→realm → 验旧 limiter 已清、服务正常），并利用新机 agent journal 可观测的特性补齐命令级证据。§6.2 旧机余项因机器下线而无执行对象，不重跑。

### 11.4 新机状态核对（与任务书前提不符，如实记录）

- 新机 ip-172-26-4-158（tailnet 100.123.93.34），公网仍为同一静态 IP 16.146.199.68；SSH（Tailscale + `tsconnect.py`，显式密钥 + `/home/hatch/.ssh/known_hosts`）通。
- **任务书称“面板 main @ 88f10db 已恢复”——实测不符**：运行中 `/app/paneld` md5 `69d166cc2d0923ef72f974912767bfa8`（43,851,762 字节）即新机搭建报告所记 main @ `7d93792` 构建；served `index.html` md5 `edd0e1db53e640c7d3da7bf5db646baa` 亦为 `7d93792` dist。为核对，我方在 `.r14-merge`（@ `88f10db`，只读构建、产物落 `/tmp`）以同口径（CGO_ENABLED=0、`-trimpath`）构建 main @ `88f10db` paneld：md5 `153bc896519f3a047d5d1faa914953c4`（43,852,010 字节），与运行中二进制不同。**结论：新机面板实际为 main @ `7d93792`（new1 搭建态），不是 `88f10db`。** 本续做不擅自升级面板基线（任务书只授权 F-01 修复验证）；新机腿部署的修复版以前述 `7d93792` 镜像为底座（见 §11.5），修复 hunk 与旧机腿同一提交。
- 新机 pristine 基线（`tm-newmachine-pristine.log`，12:25:53Z）：counts **1/0/0/1/1 PASS**（node/tunnel/forward/inbound/subscription；新机为全新 DB，无隧道、无转发，只有一个入站 `new1-vless` VLESS/59940）；节点 `w10-test` status 1、`dev (debian/amd64)`、forwardMode agent；gost.json md5 `8a80554c`（0 services、0 climiters、空）；agent md5 `27787cbf`；三容器 healthy（`flmx-backend:new1`、`flmx-frontend:new1`、postgres）；DB 备份（python `sqlite3.backup` API，因新机无 `sqlite3` CLI）md5 `81ae1e4bbf1b87e1a8a6528a16b15e09`，**表 51**（含 `sqlite_sequence` 与 `schema_version`；与旧机 50 的差为计数口径，非产品差异；表名全集已逐一核对无异常）；gost.json pristine 备份已存机上 `/root/r21-f01/gost.json.pristine`。
- **agent journal 可观测**：新机 agent 单元 `StandardOutput=journal`、`StandardError=journal`，实测 journal 每 60s 有 `🔔 收到命令: type=KernelStatus` + 脱敏详情行——WS 命令级证据在本机可观测，§8.3 可闭合。

### 11.5 新机最小腿（12:22:46Z → 12:30:13Z，持锁窗口）

- **锁**：`~/workspace/.r14-merge/scripts/testmachine-lock.sh`（与 `.fixr20` 正本同一内容）。`acquire R21-F01 --wait 3600`：GEO-INV 自 12:03:51Z 持有，等 9 轮（每 5s）后于 **12:22:46Z 抢到**（`R21-FIX-F01-verify2-lock-wait.log`）；部署与恢复前各 `check` 一次，均通过（`R21-FIX-F01-verify2-lock-check.log`，另含放锁行）；**12:30:13Z 释放，放锁后状态 free**。
- **部署**（`R21-FIX-F01-deploy-newmachine.sh`，`tm-newmachine-deploy.log`，12:26:04Z–12:26:16Z）：修复版 paneld 由本 worktree（`01336d4` 内容）同口径构建（CGO_ENABLED=0、`-trimpath`），md5 **`3b208eba31d132285f5383a4e69fb261`**（43,851,466 字节）；`FROM flmx-backend:new1` 构建镜像 `flmx-backend:r21-f01-v2`（`37e891d162ae`），容器内 `/app/paneld` md5 一致；compose 仅 backend tag 切到 `r21-f01-v2`，`up -d backend` 后 2 次探查即 healthy；运行中 paneld md5 复验一致；GET / 6366 → 200；gost md5 未动 `8a80554c`；agent active。agent 未动（pristine `27787cbf`，无 F-04 自愈干扰）。
- **建隧道**（`tm-newmachine-leg-mktun.log`）：`mktun r21-f01-t1` code 0，隧道 **id 1**，engine `''`，入口节点 1（tls）。
- **建带限流转发**（`tm-newmachine-leg-mkfwd.log`）：`mkfwd r21-f01-sw 1 5 0 203.0.113.9:9001` code 0，转发 **id 1**；gost services=2，climiters `['rule_conn_limit_1']`，refs `['rule_conn_limit_1']`。
- **主腿 gost→realm**（`tm-newmachine-leg-switch-realm.log`，12:26:47Z）：`tunengine 1 realm` code 0，engine 持久化为 `realm`；**切后 gost 为 `{"services": null}`——services 0、climiters []、refs []，`rule_conn_limit_1` 已删，无孤儿**；realm 配置含 1 个 endpoint（forward 1：`[::]:19671` → `203.0.113.9:9001`）。驱动首跑在此处因 gost.json 空数组被 agent 写成 JSON `null` 而 `TypeError` 崩溃（`data.get("services", [])` 兜不住显式 null）——纯探针缺陷，产品状态已由原始文件 `{"services": null}` 证实；驱动已修（`or []` 兜底，见 §11.6），重跑 `gost` 腿得正式 PASS 行。
- **journal 命令级证据**（`tm-newmachine-leg-switch-realm-journal.log`，**闭合 §8.3**）：切换窗口（since 12:26:40）内 `DeleteCLimiters` 匹配 **2 行 = 恰 1 条命令**——`12:26:45 🔔 收到命令: type=DeleteCLimiters requestId=1_1792931205518646325` 与 `🔔 命令详情(已脱敏): {"data":{"limiter":"rule_conn_limit_1"},"requestId":"1_1792931205518646325","type":"DeleteCLimiters"}`；按 `rule_conn_limit_1`  grep 亦为 1 行（详情行）。命令时间（12:26:45）在切换响应（12:26:47）之前 2 秒，与“先删服务、后清 limiter、再回执”的时序一致；窗口内无其他可发该命令的操作。
- **反向腿 realm→gost**（`tm-newmachine-leg-switch-back.log`，12:27:37Z）：code 0，engine 持久化回 `''`；gost services 回 2，`rule_conn_limit_1` 重建且被引用；realm 配置收敛为 0 endpoint（仅 `[log]`/`[network]`）；journal 切换窗口内 `AddCLimiters` 恰 1 条命令（`{"limits":["$ 5"],"name":"rule_conn_limit_1"}`，maxConn=5 原值），`DeleteCLimiters` **0 条**——切回过程无多发、无同型残留。
- **清理腿**（`tm-newmachine-leg-cleanup.log`）：删转发 1 code 0 → gost services 0、climiters []；删隧道 1 code 0 → 隧道从列表消失；counts 回 **1/0/0/1/1 PASS**；gost md5 `23ca5d88`（`{"services": null}` 形态，与 pristine 的 `{}` 形态语义等价、字节不同）。
- **后端恢复**（`tm-newmachine-restore-backend.log`，12:28:01Z–12:28:09Z）：compose backend tag 切回 `new1`，`up -d backend` 后 healthy；运行中 paneld md5 回 `69d166cc`；GET / 200；镜像 `flmx-backend:r21-f01-v2` 已删（机上另有他 lane 的 `flmx-backend:new2`，未动）；`.token` 与 `/root/panel-cred.txt` 已清。附带说明：恢复脚本末尾的 inline python 摘要行因同一 null 问题崩溃（`TypeError: object of type 'NoneType' has no len()`）——仅摘要打印失败，恢复本身逐项验证通过；该脚本不复用，agent 恢复改用已修正的独立脚本。
- **agent 恢复**（`tm-newmachine-agent-restore.log`，12:28:27Z–12:28:32Z）：gost.json 以 pristine 备份覆盖 → `reset-failed` → `restart flux_agent` → active；gost md5 回 **`8a80554c`**、agent md5 `27787cbf`、services 0、climiters []、refs []。
- **最终对账**（`tm-newmachine-final.log`，12:29:55Z–12:30:07Z）：一个同步周期（~85s）后 gost md5 仍为 `8a80554c`、agent active、三容器 healthy（backend `new1`）；counts 1/0/0/1/1 PASS；节点 `w10-test` status 1；served `index.html` md5 回 `edd0e1db`（`7d93792` dist，基线已还原）。**机器停在与腿前一致的 pristine 态，锁已放。**

### 11.6 续做探针事故登记（均已处理，不影响结论）

1. 驱动 null 崩溃（§11.5 主腿）：agent 空数组写 JSON `null`，旧驱动 `.get(..., [])` 兜不住；已修为 `or []`（`R21-FIX-F01-legs-newmachine.py` 内注释说明），重传后重跑得正式 PASS。**产品无问题**，`{"services": null}` 即空态的合法序列化。
2. 恢复脚本 inline 摘要 null 崩溃：同机理，仅摘要行失败，恢复逐项已验证；agent 恢复改用已修正的独立脚本（`R21-FIX-F01-newmachine-agent-restore.sh`），全程 PASS。
3. 新机无 `sqlite3` CLI：DB 备份改用 python `sqlite3.backup` API（在线备份），md5 在位。
4. `/tmp` 探针脚本被沙箱清掉：重建后跑最终对账（`r21f01-probe2.py`，内容见 `tm-newmachine-final.log` 摘要；token 只放内存、不落盘）。
5. 沙箱多 lane 并行致首跑全量与一次构建被打断（§11.2），改 `-p 1` 重跑通过；tsc 首跑会话失联，重跑 exit 0。
6. 锁等待：GEO-INV 持有 12:03:51Z→12:22:46Z，本 lane `--wait` 等 9 轮后抢到，未抢占、未 break。

### 11.7 未验证清单更新（续做后）

1. **§8.3 journal 计数——已闭合**（§11.5 新机腿：`DeleteCLimiters` 恰 1 条命令、limiter 名逐字一致）。
2. **§8.2 多节点真机腿——仍未验证**（新机亦仅 1 节点；切换循环按 `uniqueInt64s(nodes)` 逐节点发清理，与删转发漏斗同口径，沙箱为单节点 mock）。
3. §6.2 旧机四项——无执行对象（旧机下线）；新机等价项（节点上线复核、后端切回基线、删修复镜像、清 token、放锁）已在 §11.5 全部完成并验证。
4. 远端节点/非 Debian 内核、浏览器 DOM 腿、真实 Gist 端到端：与本修复无关，沿用 R21 未验证清单。
5. 新机面板实际为 `7d93792` 而非任务书所称 `88f10db`（§11.4）——已如实记录，未擅自升级；R22 审前请主控确认新机面板基线口径。

## 12. 证据索引（续做新增，同目录落盘）

| 文件 | 内容 |
|---|---|
| `R21-FIX-F01-verify2-vet.log` / `R21-FIX-F01-verify2-build.log` | vet/build 重跑，均 exit 0 |
| `R21-FIX-F01-verify2-green.log` | F-01 三测试重跑 3/3 PASS（1.45s / 3.05s / 1.87s） |
| `R21-FIX-F01-verify2-neighbour.log` | 邻接回归重跑 9/9 PASS |
| `R21-FIX-F01-verify2-tsc.log` | tsc 重跑 exit 0（0 字节） |
| `R21-FIX-F01-verify2-gotest-full.log` | 全量首跑：跑到 `http/client` 即被沙箱并行打断（中断证据） |
| `R21-FIX-F01-verify2-gotest-full2.log` | 全量 `-p 1` 重跑：18 包 ok，handler 713.859s；唯一 FAIL 为 health 环境红一格；`FULL2 exit=1` |
| `R21-FIX-F01-verify2-lock-wait.log` | 锁 `--wait` 记录：GEO-INV 持有，等 9 轮，12:22:46Z 抢到 |
| `R21-FIX-F01-verify2-lock-check.log` | 锁复核 ×3 + 释放记录 |
| `R21-FIX-F01-deploy-newmachine.sh` / `R21-FIX-F01-restore-newmachine.sh` | 新机部署 / 后端恢复脚本 |
| `R21-FIX-F01-legs-newmachine.py` | 新机腿驱动（基线 1/0/0/1/1、mktun/deltun 腿、null 兜底修正） |
| `R21-FIX-F01-newmachine-pristine.sh` / `R21-FIX-F01-newmachine-agent-restore.sh` | 新机 pristine+备份 / agent 恢复脚本 |
| `tm-newmachine-pristine.log` | 新机 pristine 基线 + DB 备份（md5 `81ae1e4b`、表 51） |
| `tm-newmachine-deploy.log` | 修复版后端部署（paneld `3b208eba`，镜像 `r21-f01-v2`） |
| `tm-newmachine-leg-mktun.log` / `tm-newmachine-leg-mkfwd.log` | 建隧道 1 / 建带限流转发 1 |
| `tm-newmachine-leg-switch-realm.log` | 切 realm：gost `{"services": null}`、limiter 已清、realm endpoint 在位 |
| `tm-newmachine-leg-switch-realm-journal.log` | journal：`DeleteCLimiters {"limiter":"rule_conn_limit_1"}` 恰 1 条命令 |
| `tm-newmachine-leg-switch-back.log` | 切回 gost：limiter 重建（`AddCLimiters "$ 5"`）、`DeleteCLimiters` 0 条 |
| `tm-newmachine-leg-cleanup.log` | 删转发/删隧道、counts 回 1/0/0/1/1 |
| `tm-newmachine-restore-backend.log` | 后端切回 new1、删修复镜像、清 token/凭据 |
| `tm-newmachine-agent-restore.log` | agent 恢复：gost md5 回 `8a80554c` |
| `tm-newmachine-final.log` | 最终对账：同步周期后 gost 稳定、节点在线、counts PASS |
— R21-FIX-F01-REPORT END · 自证：本行之前正文字节数 34650 · END
