# FLMX 1.4.2 发布报告

- 版本：**1.4.2**（双线汇合版：R5 审计修复线 + SS 按订阅计量线，均长在 main 线上）。
- Release：https://github.com/iiiiiiiii7125-art/flmx-max/releases/tag/1.4.2 （Published，2026-10-23T05:20:33Z，非 Draft / 非 Prerelease）
- tag `1.4.2`（纯数字）→ `9505467bfa3cee810943ce33314b15c095cda5b2`。
- 发版说明：[RELEASE-NOTES-1.4.2.md](sandbox://workspace/flvx-max/audit-out-132/r5/RELEASE-NOTES-1.4.2.md)（R5 按批分组 + BC-R5-01…07 + BC-R4-01 属实化句 + SS 按订阅计量一节 + 已知限制；凭据已回填）。

## §1 发布链

| 步 | 提交 / 证据 |
|---|---|
| 基线对账 | 两线 merge-base 均为 main HEAD `112a911`（1.4.1 发布态）：integrate-r5 领先 22 提交、ss-metering-142 领先 14 提交，基线关系干净 |
| 合并一 | `b963d91` merge integrate-r5（`--no-ff`，零冲突、零丢提交）。前置处理：工作区 3 份未跟踪的 R5 批报告与分支内跟踪版本逐字一致，删除未跟踪副本后合并（无内容损失） |
| 合并二 | `822a394` merge feature/ss-metering-142（本地 ref `d053047`）。两线交叠 5 文件（handler/kernel.go、kernel_relay.go、kernel/credentials.go、kernel/share.go、store/repo/repository_inbound_backup.go），仅最后一份文本冲突 1 处，处置见 §5-1 |
| U-1 直测合入 | `d0ba567` merge origin/feature/ss-metering-142 tip `c91775e`——见 §3 的口径说明（分支远端 tip 在任务钉死后多出一笔纯测试提交） |
| 版本字面量同步 | `2fb1443`：`skills/flmx-api/package.json`、`skills/flmx-api/SKILL.md`、`doc/ai-skill.md` 示例 tag 两处 → 1.4.2（沿 1.4.1 三落点先例，仓库其余命中点已自查：pnpm-lock 的 merge2@1.4.1 为第三方依赖版本、非本品字面量）；另修正 `doc/guide/upgrade.md` 中 SS 计量的首发版本注记「1.4.1 起」→「1.4.2 起」（该功能原计划随 1.4.1、实际随本版首发，注记随实修正）。仓库内 PINNED_VERSION 维持空、由流水线改写发布副本 |
| 发版说明定稿 | `9505467`（落盘 14,501 字节、尾部探针复核完整后提交；凭据段发布后回填于 `370d347`，回填后 16,720 字节、尾行完整） |
| tag | 纯数字 `1.4.2` 已推送，远端指向 9505467 |

## §2 CI 结论

### 主发版流 Build and Push Images（run 39904392621，tag 触发）— **success**

五 job 全绿：Check Version and Decide Build / Build & Compress GOST Binary / Build & Push Go Backend / Build & Push Vite Frontend / Create Release (Tag Only)。

### CI Build Check（run 39904390177 @9505467，main 推送触发）— **六 job 全 success**

| job | 结果 |
|---|---|
| Build Frontend（pnpm build + contract-fields + check-doc-consistency + demo check） | ✅ |
| Go Backend PostgreSQL Contract | ✅（全 6 条实跑，见 §3） |
| Build Go Backend | ✅ |
| Build Agent | ✅ |
| Test Installer Scripts | ✅ |
| Go Full Tests（backend + agent） | ✅（tests/contract 包 ok 20.4s；internal/health 在 CI 真实 DNS 下亦 ok） |

与 1.4.1 的两处红对比：demo check 与 doc-consistency 已由 R5X-007/008/019 修复并在本版 CI 实证转绿；`TestNotifyImportedNodeDownContract` 经 R5X-006 根治（断言下沉 handler + contract 改 DB-4 正向断言）后，其所在 contract 包在 CI 全量跑中 ok——**本版不再以「在案定案」放行任何红**（本轮 CI Build Check 无红）。凭据回填提交 `370d347` 触发的第二轮 CI Build Check（run 39905426841）为纯文档提交，结论以其完成态为准单独登记（见文末附记）。

### 其余工作流

- Publish Skill to npm（run 39904392569）：failure，日志实证 `npm error code ENEEDAUTH`——惯例红，不阻塞发布（与历版同）。
- Deploy Docs（run 39904390189）：success；GitHub Pages 部署（run 39904438086）success。

## §3 PG 契约与 U-1 关闭

- **CI 实跑全 6 条**（run 39904390177 的 PG Contract job 日志逐条 PASS）：`TestPostgresApplySubscriptionCounterSamplesConcurrentDoubleApplyContract`、`TestPostgresApplyInboundCounterSamplesConcurrentDoubleApplyContract`（R5 计数管线，承 F-S01 对象口径、未以 seq 顶替）、`TestPostgresCompanionPartialUniqueIndex`（SS 线 U-1 直测）、`TestPostgresLegacySeqContract`、`TestPostgresSeqConcurrentDoubleApplyContract`、`TestPostgresNodeCreateRepairsMissingIDDefaultContract`。
- **U-1 关闭口径**：SS 线 R1 审计登记的未验证项「`idx_node_inbound_companion` 部分唯一索引在 PostgreSQL autoMigrate 下的生成形态未验」。关闭证据为按 N-35 护栏补的直测——在真 PG 上经产品迁移路径建表后，先探 `pg_indexes` 中该索引定义（UNIQUE + companion_of + `> 0` 部分谓词），再逐格断言行为（多行 companion_of=0 不互斥、同 origin 第二条伴生被唯一约束以 23505 拒绝、不同 origin 共存）。该直测由 SS 线提交 `c91775e` 落地并已在其分支 CI（run 39902787277）实跑 PASS；本版合入后在发布 CI 再次 PASS，U-1 正式关闭。
- **钉死口径说明（如实登记）**：本任务钉死的 SS 线 tip 为本地 ref `d053047`；执行中发现远端 `origin/feature/ss-metering-142` 在其后多出一笔 `c91775e`（即上述 U-1 直测，纯测试文件、无产品代码）。任务本身要求 U-1 随本版 CI 确认，而该确认在缺此直测时只能间接触达（N-35 不足），故将 `c91775e` 一并合入（`d0ba567`）并在此明示——合入后 SS 线提交全数（14 + 1）可达，零丢弃。
- **本地真 PG 复跑（交叉回归，发布前）**：测试机临时起 postgres:17 容器（与 CI 同大版本，用后即删、隧道已关、未触 pristine 面板栈），经 SSH 隧道以 CI 同 DSN 口径复跑：PG 5 条（计数管线两条 49.6s/50.8s、Seq 两条、NodeCreateRepairs）全 PASS；新库上单独复跑 `TestPostgresCompanionPartialUniqueIndex` PASS（44.7s，该测试自带独立 schema 并在内部逐字断言索引定义，外部 psql 探 public schema 无此索引属口径不同、非缺失）。

## §4 资产核对（12/12 通过）

6 产物逐个下载、与各自 `.sha256` 比对全部一致（sha 见发版说明凭据表）。发布副本核对：`install.sh`、`panel_install.sh` 均 `PINNED_VERSION="1.4.2"`、`REPO="iiiiiiiii7125-art/flmx-max"`；docker-compose-v4/v6 的后端（flmx-flux-panel-backend）与前端（flmx-vite-frontend）镜像标签均 `:1.4.2`；发布 `gost-amd64 -V` 输出 `gost 1.4.2 (go1.25.14 linux/amd64)`，正式版本注入成立。

GHCR 标签：API 查询因当前 token 无 `read:packages` 返回 403，未能 API 核对（与历版同，如实登记）；以主发版流 Build & Push Go Backend / Vite Frontend 两 job success 为推送成立依据，未假报。

## §5 异常定案

1. **合并冲突 1 处（备份导入，两线语义叠加）**：`repository_inbound_backup.go` 入站恢复循环尾——HEAD（R5）侧为 R5X-016 的实落计数 `inboundsImported++` + R5X-024 的循环后汇总 warning；SS 侧为 F6（B-07）的 companion_of 零值回写。按两侧语义合并为：F6 回写（循环内、Create 与 enabled 回写之后）→ 计数自增 → 关循环 → warning 块，两侧逻辑全保留、无一被覆盖。
2. **合并后交叉回归红 4 条（SS 线备份测试）及其处置**：合并树全量首跑，`internal/store/repo` 4 条 SS 线备份测试 FAIL（`TestBackupImportClosesMeteringOnShapeBlockedRows`、`TestBackupImportStripsCompanionChainTargets`、`TestBackupRestoreClearsCollidingCompanionMarker`、`TestBackupExcludesCompanionInbounds`），其余包全过。根因定案：这 4 条的夹具把 SS2022 入站密码写成占位串（`"shared"` / `"ipsk"`），不是恰 16 字节的 base64 密钥——该形态在手动保存面本就被 `kernel.Normalize` 拒绝（`ValidSS2022SingleKey`），在 SS 线单独存在时因导入面无校验而不显形；合并后 R5X-024 的保存面同口径校验（BC-R5-05 已裁定）按规整行跳过，行未落库致断言失败。处置：**只改夹具**——两测试文件的 SS2022 密码换成合法密钥（与 R5 自家夹具同值系），产品代码与全部断言逐字未动；repo 包复跑全绿（40.5s）。定性：两线各自自洽、在叠加面的夹具口径冲突，非任一线产品缺陷；R5X-024 与 SS 的 G1「导入面与保存面同口径」原则在此一致，本处置即该一致性的落地。
3. **其余自动合并文件的语义复核**：交叠的 handler/kernel.go、kernel_relay.go、kernel/credentials.go、kernel/share.go 均为文本自动合并；以合并树全量测试（handler 262.6s ok、kernel ok、repo 复跑 ok、contract 28.7s ok）+ 定向交叉回归（§6）为语义成立证据，未发现第二处叠加冲突。
4. **本地 demo check 的浏览器口径（本地证据注记）**：本地以 /opt 的 Chromium 152 跑 check.mjs 时全页误报 signed-out，换 Playwright Chromium（1187，≈CI/R5 所用 140 系）即 `demo ok: 13 admin + 5 user pages` exit 0。定性为本地浏览器版本与走查脚本的兼容问题、非产品或 fixtures 问题；CI 的 Build Frontend job 已以自有 Chrome 实证 demo check 绿，本注记仅为本地复跑口径留痕。
5. **本地 health 环境红（沿袭口径）**：合并树本地全量唯一产品外 FAIL 为 `internal/health` 的 `TestValidateMonitorTCPTarget/公网域名放行`——沙箱 fake-IP DNS 把 example.com 解析入保留段，与 R5 基线登记的环境红逐字同签名；在 CI 真实 DNS 下该包 ok。产品预存失败 = 0、本版新增失败 = 0。
6. **notify 契约的本地形态**：`TestNotifyImportedNodeDownContract` 本地为明示 SKIP（本机无非 loopback RFC1918 地址，夹具密封性待 CI/真实网络——R5X-006 后的诚实口径，不再冒充 Pass）；其状态机断言本体在 handler 包（`TestR5B9ImportedNodeDownScheduledRoundAlerts` / `…Recovery…`）随全量通过，恢复腿另经 R5 发版门在测试机无过滤环境实跑 PASS，contract 本体在 CI 全量跑中随包 ok。三处证据齐，本项不以「在案定案」结案。

## §6 合并后交叉回归（发布前，合并树）

- SS 计量契约组：repo 包 companion/backup-metering 全组（复跑绿，见 §5-2）；handler 定向 `-run 'Metering|Companion'` 14 条 PASS；kernel 定向 4 条 PASS；PG 侧 companion 直测见 §3。
- R5 计数管线契约组：SQLite 侧随全量 contract 包 ok；PG 侧两条并发契约在真 PG 17 本地复跑 + CI 各一份 PASS（§3）。
- 叠加面结论：计数管线 FOR UPDATE（repo 层基线行锁）与 companion 逻辑（伴生入站的建/删/对账走独立行 + 部分唯一索引）在同树同跑无相互干扰；两线同改的备份导入路径经 §5-2 处置后双侧断言同时成立。
- 前端：tsc exit 0、vite build exit 0、demo check exit 0（13+5 页）、check-doc-consistency PASS、check-contract-fields 全部通过。

## §7 遗留登记（转 R6）

| # | 项 | 来源 / 口径 |
|---|---|---|
| 1 | pristine 存量 id=44 `a4imp-ss` SS2022 坏密钥行：Clash 面原样渲染可使整份 Clash 订阅被拒（sing-box 面既有略过政策不变；R5 批 1 写死 SS2022 key 不入渲染守卫谓词）——存量面口径留 R6 随「ss cipher 未知方法名」立项一并评估 | R5-RELEASE-GATE.md 项 1 注记 1 / R5-FIX-REPORT §7 |
| 2 | SIP023 多段在 Surge / Loon / QX 三面的残余口径：无官方二进制，恒标未验证、不得硬判 | 历轮沿袭；R5 verdicts §5.7⑤（Shadowrocket 已移出残余清单） |
| 3 | G-S02 硬门方向：窄硬门案 A（cb=1 错位 grant 降级写回）已由 R5X-014 落地；**sweep grants>0 豁免面维持疑似结转 R6**（悬空 grant 屏蔽停用对裁定、未实证完整链）；全面硬门口径待 R6 评估 | R5-X-verdicts §5.3 / §6 |
| 4 | 差集撤销墓碑表：定案缓做，过渡 warning 已落地（R5X-015）；启动触发条件三条在案（真实运营事件 / schema 迁移窗口 / 时间点恢复语义立项），启动前置为全集清点表 | R5-FIX-B3 §4 已知限制文本（已入发版说明） |
| 5 | F2 缓议 1 键（remark）：R4 期「立 7 / 缓立 1」的缓立键——R5 verdicts §5.7⑥ 已核实其 presence 落地并**结案**（应有定级 P3 仅存档），本版不重复登记为未决，仅在此存档备查 | R5-X-verdicts §5.7⑥（承 RELEASE-141 §6-7 的更新） |
| 6 | TUIC udp-relay-mode 拨号期行为：check 期已坐实不校验、拨号探针在沙箱不可达，未入校验集——R6 需可拨号环境探针定生死 | R5-FIX-REPORT §7 |
| 7 | ss cipher 未知方法名：批 1 探针证实双 FATAL 但不在 R5 立案范围、合法集口径涉产品裁定；30 个方法名逐值探针在案，R6 立项候选（与 #1 同批评估） | R5-FIX-REPORT §7 |
| 8 | E5-S1 同型补齐（forwardCreate 取行失败补补偿删除，随 R5 批 2 落地、不计关闭数）：后续轮次以故障注入坐实与否决定其结案/升格 | R5-FIX-REPORT §1 附带项 |
| 9 | 全量结论只认落盘日志：批报告「全量 ok」误记已连续两轮复发（后台回读串扰），建议 R6 任务书把「全量结论只认落盘日志文件」写成硬规 | R5-FIX-REPORT §9 |

## §8 发版门衔接

发版门（R5-RELEASE-GATE.md）：9 项全 PASS（项 1 附注记 1 = 本报告 §7-1 的 id=44 夹具存量行；项 6 附口径注记 2 = CI 本体在集成树未推送时以等价复跑判读，本版发布 CI 已以实际 Actions 结果覆盖该注记——六 job 全绿、PG 契约逐条实跑，注记就此关闭）。R5-FIX-REPORT §2 的带门验注记项（R5X-006 恢复腿）已在门内逐字实跑销账。放行对象为 integrate-r5 @ 030b9e7；本版发布树为其与 SS 线的合并树，合并面已经 §5/§6 的交叉回归单独验证，不以门结论直接覆盖合并面。

## 附记 · 第二轮 CI（凭据回填提交触发）

run 39905426841（@370d347，纯文档提交）六 job 全 success（Build Frontend / PostgreSQL Contract / Test Installer / Build Agent / Go Full Tests / Build Go Backend），与首轮 job 级结论一致。两轮 CI 均为全绿，本版无任何以定案放行的红。

— RELEASE-142 END —
