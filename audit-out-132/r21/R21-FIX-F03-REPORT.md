# R21-F03 修复报告（F-03：forwardForceDelete gost 路径补发节点清理）

- Lane：R21-F03（单 lane 续做；上一 lane 被 kill，全部测试与真机腿由本 lane 重跑，不认领半截结果）
- 分支：`fix-r21-f03`，基线 main @ 88f10db（`feat: inbound idle timeout unlimited (-1 renders as 365d)`）
- 对象：R21-X-03（产品 P3）——`forwardForceDelete` gost 路径不发任何节点清理命令，服务仍监听、limiter 仍被引用。依据：`R21-FIX-PLAN.md` F-03、`R21-X-ledger.md` R21-X-03、`R21-X-verdicts.md` V-04
- 测试机窗口：2026-10-25T12:43:47Z（锁 R21-F03）— 13:03:01Z（放锁），CST 20:43:47 — 21:03:01
- 结论：**修复完成并验证通过**。沙箱先红后绿 + 全量套件（除已知 health 沙箱 fake-IP 红外）全绿；真机腿建带限流转发 → force-delete → 服务已删、limiter 已清、端口已停，journal 逐条命令在证；普通删转发回归通过；测试机已恢复并对账一致。**未合 main、未打 tag、未发版。**

## §1 修复内容

文件：`go-backend/internal/http/handler/mutations.go`（`forwardForceDelete`，+19/−1 行）；回归测试：`go-backend/internal/http/handler/r21_fix_f03_test.go`（新建，3 个测试）。

改法（与 FIX-PLAN 一致）：force-delete 在删库后按隧道类型分流——realm 仍走 `syncRealmNodes`、nft 仍走 `reconcile`，**两者皆否（gost）时逐节点 best-effort 调用 `deleteForwardServicesOnNodeBatch`**，与普通删转发同漏斗：先 `DeleteService`（`5_1_0_tcp/_udp/5_1_0` 三名），后按 ID 兜底 `DeleteCLimiters`（`rule_conn_limit_<id>`，经 `deleteForwardConnLimiterOnNode`）。节点离线时只记 `force_delete_forward_cleanup_skipped` 日志、不硬失败（force-delete 的语义即“强制”；残留待重连后面板清扫 / agent 自愈收敛）。realm/nft 分支行为不变。

自验：`deleteForwardServicesOnNodeBatch`（`mutations.go:2241`）本身已含 nft/realm 内部分流，但 force-delete 的 gost 分支只在 `!isRealm && !nftMode` 时进入，故不会重复触发；其 DeleteService 失败时返回错误（force-delete 记日志继续）、成功后才清 limiter（顺序由测试断言锁定：DeleteCLimiters 不得先于 DeleteService）。

断言更新：无。三个新测试均为新增断言，无既有断言改动、无夹具 setup 值改动。

## §2 沙箱回归（本 lane 全部重跑）

工具链 `~/go/bin/go` 1.25.1；`TMPDIR=$HOME/tmp-r21f03`（大盘；/tmp tmpfs 仅 512M）。

### §2.1 先红（未修复代码）

`git stash` 仅 mutations.go 后跑新测试：`R21-F03-red-rerun.log`，exit=1。

- FAIL `TestR21F03ForceDeleteGostCleansServiceAndLimiter`（`r21_fix_f03_test.go:60`：`expected DeleteService to be sent by force-delete (gost path), got 0`）——与立案机理逐字一致。
- 另两个新测试在未修复代码上即 PASS（离线仍删库、普通删转发），属回归守卫、非红点，诚实登记。

### §2.2 后绿（修复代码）

`git stash pop` 恢复后跑新测试 + 同族（R19-F03、R20-F01、force-delete、白名单刷新）：`R21-F03-green-rerun.log`，exit=0，**15/15 PASS**：

- `TestR21F03ForceDeleteGostCleansServiceAndLimiter` / `TestR21F03ForceDeleteOfflineNodeStillDeletesDB` / `TestR21F03NormalDeleteStillCleansServiceAndLimiter`
- `TestForceDeleteAdminOnly` / `TestForwardForceDeleteRemovesNftablesBindingAndReconciles`
- `TestR19F03DeleteForwardBatchRemovesConnLimiter` / `TestR19F03DeleteForwardControlRemovesConnLimiter` / `TestR19F03DeleteForwardWithoutLimitsStillDeletesRuleLimiterByID`
- `TestR1B3ForwardDeleteRefreshesWhitelist` / `TestR1B3ForwardForceDeleteRefreshesWhitelist` / `TestR1B3ForwardBatchDeleteRefreshesWhitelist`
- `TestR20F01DeleteAfterLimitsClearedSendsDeleteCLimiters` / `TestR20F01UpdateDropsLimitsDeletesStaleLimiter` / `TestR20F01UpdateKeepsLimitsSendsNoDeleteCLimiters` / `TestR20F01UpdateThenDeleteLeavesNoOrphan`

### §2.3 构建与全量套件

- `go build ./...` + `go vet ./...`（go-backend）：干净，`BUILD_VET_OK`。
- 前端 `tsc --noEmit`：exit 0，无输出（`R21-F03-tsc-rerun.log` 0 字节；无前端改动）。
- 全量 `go test ./... -count=1 -timeout 30m`（`R21-F03-gotest-full-rerun.log`，`BACKEND_FULL_EXIT=1`）：
  - **16 个包 ok**，含 handler（600.036s）、repo（201.485s）、contract（78.546s）、ws、kernel、netguard 等；
  - **唯一失败：`internal/health` 的 `TestValidateMonitorTCPTarget/公网域名放行`**（`example.com:443` 在沙箱 DNS 下解析到 fake IP 而被拒）——已知沙箱环境红，与 F-03 无关（上一 lane 全量跑亦同项红；R21 审计承继口径）。
  - 产品失败 0（health 红除外）。
- `go-gost`：`GOST_EXIT=0`（lifecycle ok）。`go-gost/x`：`GOSTX_EXIT=0`（27 个包 ok）。
- 事故登记：首次全量后台跑在 12:14—12:28 间丢失（exec 会话消失、进程无存、日志冻结在 health 行），按“被 kill 的测试一律重跑”改用 nohup 脱离重跑至完成；本节全部数字均出自重跑日志，不继承丢失那次的任何结论。

## §3 真机腿（新机 ip-172-26-4-158，tailnet 100.123.93.34）

锁：`testmachine-lock.sh`，R21-F03 持有 12:43:47Z—13:03:01Z；每次部署/恢复前均 `check` 通过。SSH 走 `tsconnect.py` + 显式密钥；凭据仅读 `w10-run/panel-cred-new.txt`（600），已 scp 至机上 `/root/panel-cred.txt`（600），**不贴报告**。

### §3.0 发现态与基线重定（重要）

到机时实测与任务书所述有出入，**以实测为准**：

- 面板 served `currentVersion: 1.4.9-new`（`/api/v1/system/version`），三容器 healthy，`GET / 6366` 200；后端镜像为 `flmx-backend:new1`（非 1.4.9 release tag）。
- **DB 近空**：counts `node/tunnel/forward/inbound/subscription = 1/0/0/1/1`，**不是 R21 pristine 的 1/4/6/578/18**；gost.json 0 服务、0 climiters（md5 `23ca5d88ba3bca475f24ee48c47e0fc9`）。agent 二进制 `27787cbf52e9390685e75399ff9da821`，active。
- 处理：不恢复他人备份、不碰存量；腿脚本 `counts` 判定与 `PRISTINE_CLIMITERS` 已重定为发现态基线 1/0/0/1/1、空集；腿自建隧道+转发、用完删净，回归发现态。R22 须注意测试机 DB 已不再是 R21 pristine。

### §3.1 部署（两次尝试，见 §3.5 事故）

- DB 备份（python3 sqlite3 在线备份，面板运行中）：`/root/r21-f03/gost.db.pre` 765,952 字节。
- 部署运行 2（nohup）：12:57:17—12:57:27Z。镜像 `flmx-backend:r21-f03`（`8ef980147d3a`），compose 仅后端 tag 切换，backend healthy（第 2 次探针），`GET / 6366` 200，运行中 paneld md5 `6ea6d4f3ad1a24abe044bc15c735e4c2`，marker 核验 `grep -a -o force_delete_forward_cleanup_skipped /app/paneld | wc -l` = **1**。
- gost.json 在部署前后 md5 不变（`23ca5d88…`），agent 未重启。

### §3.2 主腿：建带限流转发 → force-delete（Leg A，F-03 本体）

| 步骤 | 结果 |
|---|---|
| `mktunnel r21-f03-tun` | code 0，tunnel id **1**，engine `""`（gost），入口节点 1 |
| `mkfwd r21-f03-fwd 1 5 0 203.0.113.9:9001` | code 0，forward id **5**；gost services=2，climiters=`[rule_conn_limit_5]`，refs=`[rule_conn_limit_5]`；inPort **60774** |
| 监听核查（删前） | `*:60774 LISTEN`（`flux_agent` pid 116455） |
| `forcefwd 5` | code 0；gost services=**0**，climiters=`[]`，refs=`[]` |
| 监听核查（删后） | `NO_LISTENER_60774` |
| journal（agent，since 12:59:48） | 13:00:34 `DeleteService`（`services: ["5_1_0_tcp","5_1_0_udp","5_1_0"]`，requestId `…227619620`）→ 同秒 `DeleteCLimiters`（`limiter: "rule_conn_limit_5"`，requestId `…229011129`）；顺序 DeleteService 在先；建侧 13:00:22 有对应的 `AddCLimiters` + `AddService`，生命周期闭环 |
| 面板审计日志 | `op=force_delete_forward operator_id=1 forward_id=5 name="r21-f03-fwd" user_id=1 residue_port=60774` |

**主腿结论：PASS。** 与 R21 立案时的 Leg D（建 43 → force-delete 后 gost 仍 14 服务、limiter 43 在且被引用、journal 0 次清理命令）逐项反转。

### §3.3 回归腿：普通删转发（Leg B）

`mkfwd r21-f03-fwd2 1 5 0 203.0.113.9:9002` → forward id **6**（inPort 11854，gost 2 服务、`rule_conn_limit_6` 在且被引用）；`delfwd 6` code 0 → gost services=0、climiters=[]、md5 回到 `23ca5d88ba3bca475f24ee48c47e0fc9`。**回归 PASS。**

### §3.4 恢复与对账

- `deltunnel 1` code 0；counts 回到 **1/0/0/1/1 PASS**；gost md5 `23ca5d88…`、agent 二进制 `27787cbf…` 均与发现态一致。
- 恢复（13:01:14—13:01:26Z）：compose 由部署前备份恢复为 `flmx-backend:new1`；运行容器镜像 `sha256:61e1531f2770…` 与部署前一致；backend healthy，`GET / 6366` 200；sysver 仍 `1.4.9-new`；`r21-f03` 镜像已删（`no r21-f03 images left`）。
- 对账：counts 1/0/0/1/1、gost 0 服务/0 climiters/md5 `23ca5d88…`、agent active 且未重启、DB 与发现态一致（API counts 证明；DB 备份 `gost.db.pre` 留存机上未动用）。锁已释放（13:03:01Z，free 已确认）。

### §3.5 部署事故与脚本修正（均已处理，不掩盖）

1. **首次部署 SSH 断管**：`docker build` 进行中 SSH `Broken pipe`，部署脚本中途死亡；但 build 进程在机上存活并完成（孤儿镜像 `fa58f4b9c552`）。未造成损害（compose 未切换、后端仍 new1 healthy）。改用 nohup 在机上跑部署、轮询日志，运行 2 成功；孤儿镜像随恢复时的 `docker rmi` 一并清理（恢复后 `r21-f03` 镜像 0 残留）。
2. **机上无 `sqlite3` CLI**：部署脚本原 DB 备份步失败（`sqlite3: command not found`）。改为 python3 `sqlite3` 在线备份 API；备份失败时 `exit 1` 中止部署。
3. **旧 md5 期望值无法复现**：脚本原 `PANELD_MD5_EXPECT=7e8313e4…`，本地 CGO_ENABLED=0 / `-trimpath` / CGO=1 三种构建均不命中（上一 lane 构建环境不可考）。改用本 lane 实测构建值 `73c1c1a5e29893844cee247bb80e9522`；并因本地构建含绝对路径、docker 构建在 `/src`，镜像内 paneld 不再要求 md5 相等，改为 marker 串核验。
4. **镜像内无 `strings`**：debian slim 运行时镜像无 binutils，`strings` 核验恒为 0（假阴性，非真阴性）。改用容器内 `grep -a -o` 核验，计数 1。
5. **44M paneld 二进制传机极慢**：tsconnect 代理约 14KB/s，scp 49M stage 预估 ~60 分钟。改为 source-only stage（7.2M）+ 机上 docker build；已传部分用文件清单 diff 补传缺失的 7 个非测试 `.go` 文件与 `Dockerfile.backend`（其余缺失均为 `_test.go`/contract 测试，不影响构建）。
6. **腿脚本两个发现态 bug**：空 gost.json 的 `services/climiters` 为 `null` 而非 `[]`，`gost()` 判空已修；`counts` 判定已重定为发现态基线（见 §3.0）。

## §4 交付文件（本提交）

分支 `fix-r21-f03`（提交号见提交后补记——本报告写于提交前；父代理以 `git log` 为准）。

- 产品：`go-backend/internal/http/handler/mutations.go`、`go-backend/internal/http/handler/r21_fix_f03_test.go`
- 脚本：`audit-out-132/r21/R21-F03-deploy.sh`、`R21-F03-legs.py`、`R21-F03-restore.sh`
- 沙箱日志（本 lane 重跑）：`R21-F03-red-rerun.log`、`R21-F03-green-rerun.log`、`R21-F03-gotest-full-rerun.log`、`R21-F03-gotest-gost-rerun.log`、`R21-F03-gotest-gostx-rerun.log`、`R21-F03-tsc-rerun.log`（0 字节，tsc 干净）
- 真机日志：`R21-F03-tm-pre.log`、`R21-F03-tm-counts-pre.log`、`R21-F03-tm-sysver.log`、`R21-F03-tm-deploy.log`（首次断管尝试）、`R21-F03-tm-deploy-run2.log`、`R21-F03-tm-legs.log`、`R21-F03-tm-post-restore.log`、`R21-F03-tm-restore.log`
- 上一 lane 遗留（随本次归档）：`R21-F03-PAUSE.log`、`R21-F03-red.log`、`R21-F03-green.log`、`R21-F03-gotest-full.log`、`R21-F03-gotest-gost.log`、`R21-F03-gotest-gostx.log`、`R21-F03-gotest-handler-30m.log`、`R21-F03-frontend-install.log`、`R21-F03-tsc.log`、`R21-F03-tm-pristine.log`

## §5 观察项（登记，不立案；由主控定）

- OBS-F03-1：force-delete 成功后审计日志的 `residue_note` 仍为静态文案“节点规则可能残留，需人工确认清理”（`buildForwardResidueHint` 在删库前生成、无条件记录）。清理已实际下发，该提示现为误导性残留文案；是否随文案收敛处理由主控定，不属 F-03 修复域。
- OBS-F03-2：测试机 DB 发现态近空（1/0/0/1/1），R21 pristine（1/4/6/578/18）已不存在；R22 轮次须以发现态为准重定基线，或由主控决定是否恢复 R21 DB 备份。
- 未验证（结转）：多节点 force-delete（单机仅 1 节点；沙箱为单 mock 节点）；realm/nft 路径真机 force-delete（本轮只动 gost 腿；沙箱既有测试覆盖两分支行为不变）。

## §6 护栏自查

- 未触碰有意设计护栏；无 nolint、无放宽断言、无删测试、无吞错误、无重试掩盖、无调用点计数宣称清零。
- 断言更新 0 项；夹具 setup 值改动 0 项。
- `git add -A` 未用；提交只 add 明确文件，`*.log` 逐文件 `git add -f`。
- 未合 main、未打 tag、未发版；测试机已恢复 pristine（发现态）并对账；锁已释放。
— R21-FIX-F03-REPORT END · 自证：本行之前正文字节数 13333 · END
