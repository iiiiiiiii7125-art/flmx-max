# FLMX 1.4.4 发布报告（RELEASE-144）

- **Release**：[Release 1.4.4](https://github.com/iiiiiiiii7125-art/flmx-max/releases/tag/1.4.4)（Published，2026-10-23T12:41:00Z，非 Draft / 非 Prerelease）
- **tag**：纯数字 `1.4.4` → `d03c2ab96d1cdb7e5bf34d9fa14575292e8a43a3`
- **内容**：R7 审计修复单线版（R7 总账 8 条 P0=0/P1=0/P2=2/P3=6，8/8 关闭；发版门 8/8 PASS 放行，报告 R7-RELEASE-GATE.md——门代理的完成回执在运行时重启中丢失，以落盘报告为准，报告本体完整、逐项 PASS 在案）
- **发版说明**：[RELEASE-NOTES-1.4.4.md](sandbox://workspace/flvx-max/audit-out-132/r7/RELEASE-NOTES-1.4.4.md)（按批分组 + BC-R7-01/02 定稿全文，凭据已回填）

## 1. 合并链与基线对账

- 合并前对账：`git merge-base main integrate-r7` = main HEAD `1e74e6f`（1.4.3 发布态），integrate-r7 领先 11 提交、落后 0——integrate-r7 为 main 的直系后代，无分叉。11 提交 = 5 批提交 + 5 合并提交 + 门前 fixtures 整册重录补做 `d44c043`。
- 合入：`1d1d127` merge(release) 提交（--no-ff 保线型），**零冲突、零丢提交**（合后 `main...integrate-r7` 左/右计数 1/0，右 0 即无丢提交）；20 文件、+1050/−47，与 R7-FIX-REPORT 的集成 diff 对账逐字一致。
- 字面量同步：`2903826`——沿三落点先例（`skills/flmx-api/package.json`、`skills/flmx-api/SKILL.md` 的 version 与 `doc/ai-skill.md` 示例 tag 两处），3 文件 4 增 4 删；全仓 `1.4.3` 命中自查余一处为 `doc/guide/upgrade.md` 的 SS 按订阅计量首发注记「（1.4.2 起）」，系历史事实注记、**不改**。仓库内 `install.sh`/`panel_install.sh` 的 PINNED_VERSION 按仓库规矩不手改（发布副本由流水线改写，见 §4 核对）。
- 发版说明定稿：`d03c2ab`（10,446 字节，落盘后字节数 + 尾行「— RELEASE-NOTES-1.4.4 END —」探针复核后提交，tag 钉于此提交）；凭据回填 `86cb790`（回填后 11,512 字节、尾行复核在位）。

## 2. 合并树全量回归（本地实跑，结论只认落盘日志）

- go-backend：`go build./...` / `go vet./...` exit 0；`go test./...` **产品失败 0**（handler 346.8s 等其余包全 ok）。唯一 FAIL 为环境红一格并附隔离证据：`internal/health` `TestValidateMonitorTCPTarget/公网域名放行` —— `ValidateMonitorTCPTarget("example.com:443")` 报「内网/保留地址」，系本沙箱 fake-IP DNS 把 example.com 解析落保留段所致；失败集合恰一格一子格、与基线（tag 1.4.3）及历轮集成日志逐字同签名。本轮日志：`RELEASE-144-backend-test.log`（同目录）。
- agent：go-gost 与 go-gost/x 两模块 build exit 0、测试全 ok。
- 前端：`tsc --noEmit` exit 0；`scripts/check-contract-fields.mjs` 全部通过；`scripts/check-doc-consistency.sh` RESULT: PASS；fixtures 内容闸 `check-fixtures-content.mjs` exit 0（关键键内容非空全过）；形状漂移脚本 `check-shape-drift.mjs` exit 0（252 recorded objects 全覆盖）；demo 走查 `demo ok: 13 admin + 5 user pages`（本地以 Playwright Chromium 经 `CHROME` 环境变量运行，与历轮口径一致，CI 已自证）。
- PG 契约：本地无 PG，未跑；以发版门真 PG 实跑（门项 3，6/6）与 CI 实跑（§3，6/6 逐条）双处为据。

## 3. CI 结论（六 job 全绿、无红项）

- **主发版流 Build and Push Images**（run 39954337135 @ tag 1.4.4）：五 job 全 success（Check Version / Build & Push Vite Frontend / Build & Push Go Backend / Build & Compress GOST Binary / Create Release）。
- **CI Build Check 第一轮**（run 39954334634 @ 发布提交 `d03c2ab`）：六 job 全 success——Build Frontend（1m39s）、Go Full Tests（4m4s）、PostgreSQL Contract（49s）、Build Go Backend、Build Agent、Test Installer。**PG 契约 CI 实跑全集 6 条逐条 PASS**（job 日志逐条在列：`TestPostgresCompanionPartialUniqueIndex`、`TestPostgresApplySubscriptionCounterSamplesConcurrentDoubleApplyContract`、`TestPostgresApplyInboundCounterSamplesConcurrentDoubleApplyContract`、`TestPostgresLegacySeqContract`、`TestPostgresNodeCreateRepairsMissingIDDefaultContract`、`TestPostgresSeqConcurrentDoubleApplyContract`）。
- **CI Build Check 第二轮**（run 39955352367 @ 凭据回填提交 `86cb790`）：六 job 全 success，与第一轮 job 级一致。
- **Publish Skill to npm**（run 39954337160）：failure，日志实证 `npm error code ENEEDAUTH`——历版惯例红，不阻塞发布。
- **Deploy Docs**（run 39954334823）：success。
- 本版判读口径执行情况：预期六 job 全绿已达成，**无任何红项需要定案，未使用「在案定案」放行任何一项**；本地唯一环境红（health 一格）的定案与隔离证据见 §2（CI 环境真实 DNS 下 Go Full Tests 全绿，反证其为沙箱环境红）。

## 4. Release 与 12 资产核对（12/12 通过）

- Release 状态：Published、非 Draft / 非 Prerelease；资产恰 12 个（6 产物 + 6 `.sha256`）。
- 6 产物逐个下载与各自 `.sha256` 比对**全部一致**：

| 产物 | sha256 |
|---|---|
| docker-compose-v4.yml | `b5294f31ceaa421182ad8d2b273257fb5fce9be5ee71745068a3ed2924983d45` |
| docker-compose-v6.yml | `ba30347fb3d29a00725a5b602a6158ab961ad299307f5c837bbbe53cafd1c176` |
| gost-amd64 | `a2973d8bc48bd9719b40344861112e5c8e73bac0cfa39fd051c1baec1d0c433e` |
| gost-arm64 | `143118af9923e3ba937cd850f86a54d7fa11f029b2f03013a82c597d569af0a7` |
| install.sh | `d5be923c812b8ee482607925d4bd421cd035b5217879f5e36ee936e5fce9443a` |
| panel_install.sh | `90c0db9100044649285a05c760706d1329c6d7cf7b321cac24baae81e7e278c1` |

- 发布副本注记：`install.sh`、`panel_install.sh` 均 `PINNED_VERSION="1.4.4"`、`REPO="iiiiiiiii7125-art/flmx-max"`；compose v4/v6 的后端（flmx-flux-panel-backend）与前端（flmx-vite-frontend）镜像标签均 `:1.4.4`；发布 `gost-amd64 -V` = `gost 1.4.4 (go1.25.14 linux/amd64)`，agent 正式版本注入在位。
- **GHCR 未核**：标签 API 与 Packages API 以当前 token 请求均返回 403（无 read:packages，与历版同），如实登记；以主发版流 Build & Push 两 job（Frontend/Backend）success 为镜像已推送之据。

## 5. 异常定案

1. **本地 health 环境红一格**：见 §2——沙箱 fake-IP DNS 隔离证据齐（单格单子格、与基线同签名、CI 真实 DNS 下同包全绿），定案环境红、非产品失败。
2. **Publish Skill ENEEDAUTH**：日志实证的惯例红（npm 未登录），不阻塞，与历版同。
3. 除上述外**无异常**：合并零冲突、CI 零红项（除惯例红）、资产零不一致。发版门代理完成回执的运行时丢失已在头部登记，门报告落盘完整、不影响放行依据。

## 6. R8 遗留登记

1. **收敛决胜轮**：R7 为收敛候选第一轮（产品面新增 P0/P1 = 0、R6 关闭零回退）；R8 以 tag 1.4.4 为基线再验一轮产品面 P0/P1 = 0 方正式达成收敛判据（连续两轮）。发布前不宣称已收敛。
2. **四无二进制面 124 格未验证**：Surge/Loon/QX/URI 四面 SS 格守卫已设门、面板渲染 EMIT/SKIP 集已锁，无可达官方二进制/客户端可实测，恒标未验证；R8 沿袭登记，有可验客户端证据时再逐格销账。
3. **墓碑触发条件复核**：差集撤销墓碑表三触发条件（真实复活事件 / grant 相关 schema 迁移窗口 / 恢复到时间点语义）逐轮对账仍未满足、维持缓做；触发条件 2 的激活处置已在 R7 复核预登记，R8 按例复核。
4. **K-S01 回执消费面 API 存废口径待主控裁定**：回执三段完整性中 K-S01 缺消费段一条维持疑似结转（第四轮），其定性取决于相关 API 存废的产品口径——复核已判归主控在 R8 基线时裁定，不占号、不阻断收敛计数；R8 基线员首项即登记该裁定结果。
5. **任务书头部代际刷新**：三正本头部现为 1.4.3 实测值、1.4.4 代际以待回填登记形态在位（R7X-008 口径，未冒充已刷新至未发布版本）；R8 基线员首项以 1.4.4 实测值完成代际回填。
6. **哨兵增行**：R8 哨兵表预登记新增 ENC 三面行（R7X-005 处置规格升格的随附项），基线员落表时执行。

— RELEASE-144 END —
