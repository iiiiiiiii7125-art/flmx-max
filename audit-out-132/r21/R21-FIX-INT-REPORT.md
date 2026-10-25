# R21 修复集成报告（R21-FIX-INT-REPORT）

## §1 基线与变基决定

- 审计基线：main @ `7d93792`（R20 修复后 main）。
- 集成基线：main @ `88f10db`（= 7d93792 + 空闲超时「不限」功能，`feat-connidle-unlimited`）。
- 决定：**三路变基到 88f10db 上继续**，而非在 7d93792 上做完再合。
- 依据（变基前实查）：「不限」提交 88f10db 的 12 个产品文件（`handler/kernel.go`、`kernel/model.go`、`kernel/render_xray.go`、`store/model/node_kernel.go`、`store/repo/backup_connidle_test.go`、`api/types.ts`、`inbound-form-modal.tsx`、`presets.ts`、`doc/guide/nodes.md` + 3 个测试文件）与三路改动面（F-01 `handler/realm.go`、F-02 `handler/control_plane.go`/`handler/upgrade.go`、F-03 `handler/mutations.go`）**零文件重叠**。三路逐一 stash→rebase→pop，零冲突，工作全部保留（F-01 提交 `ed6150d` → `01336d4`）。集成回归只需在最终基线上全量跑一次。

## §2 三路合入（零冲突）

集成工作区 `~/workspace/.fixr21`，集成枝 `fix-r21`（先 ff 到 88f10db，再依次合入）：

| 合入 | 提交 | 内容 |
|---|---|---|
| F-01 | `65bcf3b` | merge(fix-r21-f01)：gost→realm 引擎切换补清 limiter（R21-X-01） |
| F-02 | `8ac9138` | merge(fix-r21-f02)：重连重推补清失势 limiter（R21-X-02） |
| F-03 | `44143e9` | merge(fix-r21-f03)：force-delete 补发 DeleteService + DeleteCLimiters（R21-X-03） |

- HEAD：`fix-r21` @ `44143e9`，工作区干净，未合 main、未打 tag、未发版。
- 集成 diff vs `88f10db`：97 文件 +23572/−3。产品文件 7 个：`handler/realm.go`、`handler/control_plane.go`、`handler/upgrade.go`、`handler/mutations.go` + 新建 `r21_fix_f01_test.go`、`r21_fix_f02_test.go`、`r21_fix_f03_test.go`；其余为逐项报告与日志。
- 断言更新登记：**0 条**（三路均无断言更新；F-02 另补隧道重试缺口与一处过期行号引用，属同路径修补，已记其报告）。

## §3 集成回归（最终基线 44143e9，全量重跑）

驱动 `/tmp/r21-int-regress.sh`，日志 `audit-out-132/r21/R21-FIX-INT-regress.log`（19,627 字节）：

| 步骤 | 结果 |
|---|---|
| go-backend build + vet | exit 0 |
| go-backend `go test ./... -count=1` | 16 包 ok（handler 469.301s、repo 159.666s、contract 61.191s）；唯一 FAIL 是已知 health 沙箱 fake-IP 环境红 `TestValidateMonitorTCPTarget/公网域名放行`；**产品失败 0** |
| go-gost build | exit 0 |
| go-gost/x `go test ./...` | 27 包 ok，exit 0 |
| 前端 `pnpm run build`（tsc + vite） | 首跑 exit 1：全新 worktree 缺 `node_modules` 致 `tsc: not found`——R20 同型陷阱复发；`pnpm install` exit 0 后重跑 exit 0（built 3.69s，PWA precache 65 entries），日志 `R21-FIX-INT-frontend-install.log` / `R21-FIX-INT-frontend.log` |
| 一致性脚本 `check-doc-consistency.sh` | PASS |
| R21 新测试逐字重跑（集成树内 `-run TestR21 -v`） | **11/11 PASS**（F-01 3 个、F-02 5 个、F-03 3 个，5.965s） |

## §4 真机腿（各路，新机 ip-172-26-4-158 / 100.123.93.34）

| 路 | 持锁 | 腿结论 |
|---|---|---|
| F-01 | 12:22:46Z–12:30:13Z | 建隧道 1 + 带限流转发 1 → 切 gost→realm → gost 变 `{"services": null}`，limiter 已清；journal 有且仅有一条 `DeleteCLimiters {"limiter":"rule_conn_limit_1"}`（12:26:45），§8.3 缺口闭合；切回后 limiter 重建；恢复 pristine，agent 回 pristine md5 `8a80554c`，节点在线 |
| F-02 | 12:30:16Z 起（排队等 F-01，未抢占） | 离线序列 3 遍有效腿（转发 2/3/4），重连后 journal 每次恰 1 条面板下发的 `DeleteCLimiters`，gost 孤儿清零；恢复 pristine（backend 回 new1、节点 1 在线、隧道 0、转发 0、三容器 healthy） |
| F-03 | 12:43:47Z–13:03:01Z | 自建 gost 隧道 1 → 建带限流转发 5 → force-delete code 0 → gost 0 服务/0 limiter/0 引用、端口已停；journal 13:00:34 先后收到 `DeleteService` 与 `DeleteCLimiters`；回归腿（转发 6 普通删除后 md5 回到部署前值）PASS；恢复对账 PASS（counts 回到发现态 1/0/0/1/1，compose 回 `flmx-backend:new1`，镜像 sha 与部署前一致，`r21-f03` 镜像 0 残留） |

- 三路锁交接干净（F-01 → F-02 → F-03），无覆盖、无抢占；腿后锁 free。
- 集成清理：机上 `/root/r21-f01/`、`/root/r21-f02/`、`/root/r21-f03/` 残留目录已删（证据已入库），现无 r21 残留；三容器 healthy。

## §5 前提订正（N-36）：新机面板基线口径

- 任务书前提“新机面板 main @ 88f10db 已恢复”与实测不符：运行中 paneld md5 `69d166cc`（43,851,762 字节）、served index `edd0e1db` 均为 **7d93792** 构建；88f10db 的 paneld 为 `153bc896`（43,852,010 字节）。三路真机腿均以 7d93792 镜像（new1）为底座部署修复版，未擅自升级基线。
- 影响评估：修复面与「不限」面零文件重叠，腿结论对修复行为本身有效，不阻塞集成与合 main。
- **R22 审前须确认新机面板基线口径**（升到 88f10db 还是继续以 7d93792 为准），本轮不替 R22 定。

## §6 遗留与未验证

- F-01 多节点真机腿未验证（新机仅 1 节点），沿袭登记。
- geo 更新后 freedom 出站拨 127.0.0.1/RFC1918 挂住一事另立案排查中，不在本轮范围。
- R21 审计的三项 P3 均已修完；是否收敛（用户口径：一轮零新增产品问题）由 R22 重审判定，本轮不报收敛。

## §7 交付

- 集成分支 `fix-r21` @ `44143e9`（工作区干净，未合 main、未打 tag、未发版）。
- 逐项报告：`R21-FIX-F01-REPORT.md`（34,730 字节，END 在位）、`R21-FIX-F02-REPORT.md`（14,347 字节，END 在位）、`R21-FIX-F03-REPORT.md`（13,413 字节，END 在位），均已验盘。
- 本集成报告 `R21-FIX-INT-REPORT.md`（见尾行自证）+ 回归日志 `R21-FIX-INT-regress.log`（19,627 字节）、前端补验日志两份。

— R21-FIX-INT-REPORT END · 自证：本行之前正文字节数 6013 · END
