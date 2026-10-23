# FLMX 1.4.3 发布报告（RELEASE-143）

- **Release**：[Release 1.4.3](https://github.com/iiiiiiiii7125-art/flmx-max/releases/tag/1.4.3)（Published，2026-10-23T08:19:28Z，非 Draft / 非 Prerelease）
- **tag**：纯数字 `1.4.3` → `37a15d764cd96e3baae118a364de94f54f383f0a`
- **内容**：R6 审计修复单线版（R6 总账 25 条 P0=0/P1=1/P2=6/P3=18，25/25 关闭；发版门 9/9 PASS 放行，报告 R6-RELEASE-GATE.md）
- **发版说明**：[RELEASE-NOTES-1.4.3.md](sandbox://workspace/flvx-max/audit-out-132/r6/RELEASE-NOTES-1.4.3.md)（按批分组 + BC-R6-01…06 与可转发文案，凭据已回填）

## 1. 合并链与基线对账

- 合并前对账：`git merge-base main integrate-r6` = main HEAD `c2aad9e`（1.4.2 发布态），integrate-r6 领先 20 提交、落后 0——integrate-r6 为 main 的直系后代，无分叉。
- 合入：`e35d92f` merge(release) 提交（--no-ff 保线型），**零冲突、零丢提交**（合后 `main..integrate-r6` 计数为 0）；相对 1.4.2 基线 38 文件、+2796/−83。
- 字面量同步：`66a46c0`——沿三落点先例（`skills/flmx-api/package.json`、`skills/flmx-api/SKILL.md` 的 version 与 `doc/ai-skill.md` 示例 tag 两处）；全仓 `1.4.2` 命中自查余一处为 `doc/guide/upgrade.md` 的 SS 按订阅计量首发注记「（1.4.2 起）」，系历史事实注记、**不改**。仓库内 `install.sh`/`panel_install.sh` 的 PINNED_VERSION 按仓库规矩不手改（发布副本由流水线改写，见 §4 核对）。
- 发版说明定稿：`37a15d7`（12,989 字节，落盘后字节数 + 尾行「— RELEASE-NOTES-1.4.3 END —」探针复核后提交）；凭据回填 `8aedd6b`（回填后 15,117 字节、尾行复核在位）。

## 2. 合并树全量回归（本地实跑，结论只认落盘日志）

- go-backend：`go build ./...` / `go vet ./...` exit 0；`go test ./...` **产品失败 0**（16 包 ok，含 handler 322s、repo、contract 包）。唯一 FAIL 为环境红一格并附隔离证据：`internal/health` `TestValidateMonitorTCPTarget/公网域名放行` —— `ValidateMonitorTCPTarget("example.com:443")` 报「内网/保留地址」，系本沙箱 fake-IP DNS 把 example.com 解析落保留段所致；失败集合恰一格一子格、与基线（tag 1.4.2）及 R6 集成日志（`R6-integrate-backend-test.log`）逐字同签名。本轮日志：`RELEASE-143-backend-test.log`（同目录）。
- agent：go-gost 与 go-gost/x 两模块 build exit 0、测试全 ok（x/socket 含 R6B6 UdpPing 用例）。
- 前端：`tsc --noEmit` exit 0；`scripts/check-contract-fields.mjs` 全部通过；`scripts/check-doc-consistency.sh` RESULT: PASS；demo 走查 `demo ok: 13 admin + 5 user pages`（本地须以 Playwright Chromium 经 `CHROME` 环境变量运行，与 R6 门口径一致；缺省 `google-chrome` 本机不存在为环境事项、非产物问题，CI 已自证）；形状漂移脚本 `check-shape-drift.mjs` exit 0（205 recorded objects 全覆盖）。
- PG 契约：本地无 PG，未跑；以发版门真 PG 实跑（门项 4，6/6）与 CI 实跑（§3，6/6）双处为据。

## 3. CI 结论（两轮 job 级一致、六 job 全绿、无红项）

- **主发版流 Build and Push Images**（run 39922530625 @ tag 1.4.3）：五 job 全 success（Check Version / Build & Push Vite Frontend / Build & Push Go Backend / Build & Compress GOST Binary / Create Release）。
- **CI Build Check 第一轮**（run 39922522015 @ 发布提交 `37a15d7`）：六 job 全 success——Build Frontend（1m50s）、Go Full Tests（4m8s）、PostgreSQL Contract（50s）、Build Go Backend、Build Agent、Test Installer。**PG 契约 CI 实跑全集 6 条逐条 PASS**（job 日志逐条在列：`TestPostgresCompanionPartialUniqueIndex`、`TestPostgresApplySubscriptionCounterSamplesConcurrentDoubleApplyContract`、`TestPostgresApplyInboundCounterSamplesConcurrentDoubleApplyContract`、`TestPostgresLegacySeqContract`、`TestPostgresNodeCreateRepairsMissingIDDefaultContract`、`TestPostgresSeqConcurrentDoubleApplyContract`）。
- **CI Build Check 第二轮**（run 39923984385 @ 凭据回填提交 `8aedd6b`）：六 job 全 success，与第一轮 job 级一致。
- **Publish Skill to npm**（run 39922530441）：failure，日志实证 `npm error code ENEEDAUTH`——历版惯例红，不阻塞发布。
- **Deploy Docs**（run 39922522082）：success；pages build and deployment 随动 success。
- 本版判读口径执行情况：预期六 job 全绿已达成，**无任何红项需要定案，未使用「在案定案」放行任何一项**；本地唯一环境红（health 一格）的定案与隔离证据见 §2（CI 环境真实 DNS 下 Go Full Tests 全绿，反证其为沙箱环境红）。

## 4. Release 与 12 资产核对（12/12 通过）

- Release 状态：Published、非 Draft / 非 Prerelease；资产恰 12 个（6 产物 + 6 `.sha256`）。
- 6 产物逐个下载与各自 `.sha256` 比对**全部一致**：

| 产物 | sha256 |
|---|---|
| docker-compose-v4.yml | `1a1295b0c98c49ef9e91ea355699a9300b9ddca16223385a9d9aa9496bbb4ae1` |
| docker-compose-v6.yml | `5e772088280d0505463dcaaeb66202f8e920e1d39209621ade3375bd6b91dc69` |
| gost-amd64 | `c0f47bd11a83872bdc0976f3680c01d2b73d49ac901dc9a3e05ab6c39f095ec1` |
| gost-arm64 | `ca79d135d24eda98fecd497d507cb9b2abef1bbc7151dbae109cc2cbc5141a0f` |
| install.sh | `d94a2e7da48d2adc59e48c4bf8a0937a6ee8d827bdbbb736c394be90c297c6e0` |
| panel_install.sh | `1c0194610fd4cf5825fe6dc8f19180c36bd7386a77c872db58b00baaf5f32503` |

- 发布副本注记：`install.sh`、`panel_install.sh` 均 `PINNED_VERSION="1.4.3"`、`REPO="iiiiiiiii7125-art/flmx-max"`；compose v4/v6 的后端（flmx-flux-panel-backend）与前端（flmx-vite-frontend）镜像标签均 `:1.4.3`；发布 `gost-amd64 -V` = `gost 1.4.3 (go1.25.14 linux/amd64)`，agent 正式版本注入在位。
- **GHCR 未核**：标签 API 以当前 token 请求返回 403（无 read:packages，与历版同），如实登记；以主发版流 Build & Push 两 job（Frontend/Backend）success 为镜像已推送之据。

## 5. 异常定案

1. **本地 health 环境红一格**：见 §2——沙箱 fake-IP DNS 隔离证据齐（单格单子格、与基线同签名、CI 真实 DNS 下同包全绿），定案环境红、非产品失败。
2. **本地 demo check 浏览器缺省**：本机无 `google-chrome`，以 Playwright Chromium 经 `CHROME` 变量复跑 PASS（与 1.4.2 发布注记同型）；CI Build Frontend 内 demo check 正常走查并 success，非产物问题。
3. **Publish Skill ENEEDAUTH**：日志实证的惯例红（npm 未登录），不阻塞，与历版同。
4. 除上述外**无异常**：合并零冲突、CI 两轮零红项、资产零不一致。

## 6. R7 遗留登记

1. **Xray 本体 SS 子集门待 R7 裁定**：Xray 本体 SS 方法集窄于面板共同集（18 值中 10 值被 Xray exit 23）——R6 两条关闭断言已逐字达标、该观察不在断言字面内；是否给 Xray 链式面加子集门（或在选目标时提示）由 R7 交叉复核裁定。
2. **SubTraffic.since fixtures 漂移一格**：demo fixtures 与现行形状的一格漂移（发版门 §9 登记），R7 随 demo 基建面处理。
3. **catalog §1 指针刷新**：审计任务书 catalog §1 指针刷新留 R7（R6 基建批已完成三正本基线标签同步与 §0 对账、§1.7 新设；§1 指针代际刷新未做）。
4. **Surge/Loon/QX 多段残余**：SIP023 多段在 Surge/Loon/QX 三面的残余口径维持未验证（无官方二进制），R7 复核其性质是否变化。
5. **墓碑触发条件复核**：差集撤销墓碑表三触发条件（真实复活事件 / grant 相关 schema 迁移窗口 / 恢复到时间点语义）本轮逐条对账均未满足、维持缓做；R7 按例复核触发条件。
6. **四无二进制面 124 格**：Surge/Loon/QX/URI 四面的 SS 格守卫已设门、代码断言在案，无官方二进制/客户端可实测，恒标未验证，R7 沿袭登记（有可验客户端证据时再逐格销账）。

— RELEASE-143 END —
