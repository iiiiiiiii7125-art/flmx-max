# FLMX 1.4.1 发布报告

- 版本：**1.4.1**（1.4.0 的补丁版；R4 审计修复线，基线口径钉死 tag 1.3.7、修复长在 main 线上）。
- Release：https://github.com/iiiiiiiii7125-art/flmx-max/releases/tag/1.4.1 （Published，2026-10-23T02:27:56Z，非 Draft / 非 Prerelease）
- tag `1.4.1`（纯数字）→ `0b18d491cb3057ea5aaf3b876f575cdd119fc57c`。
- 发版说明：[RELEASE-NOTES-1.4.1.md](sandbox://workspace/flvx-max/audit-out-132/r4/RELEASE-NOTES-1.4.1.md)（BC-R4-01…07 逐条 + 可转发文案 + 021 结案文档句补登 + 凭据已回填）。

## §1 发布链

| 步 | 提交 / 证据 |
|---|---|
| 合并 | `bac1025` merge integrate-r4（基线 `a5a418f` 为 main 与 integrate-r4 的 merge-base，集成树领先 39 / 落后 0；`--no-ff` 合入，零冲突、零丢提交，合后 main 领先 a5a418f 共 40 提交） |
| 版本字面量同步 | `45e525e`：`skills/flmx-api/package.json`、`skills/flmx-api/SKILL.md`、`doc/ai-skill.md` 示例 tag 两处 → 1.4.1（沿 1.3.x 同步先例三落点；仓库内 PINNED_VERSION 维持空、由流水线改写发布副本） |
| 发版说明定稿 | `0b18d49`（落盘 12,399 字节，尾部探针复核完整后提交；凭据段发布后回填） |
| CI 配置放宽（发布后、仅 CI 不影响产物） | `5f3ac66`：PG 契约 job 的 `-run` 由单条精确名放宽为 `TestPostgres` 前缀，见 §3 |
| tag | 纯数字 `1.4.1` 已推送，远端指向 0b18d49 |

## §2 CI 结论

### 主发版流 Build and Push Images（run 39888713081，tag 触发）— **success**

五 job 全绿：Check Version and Decide Build / Build & Compress GOST Binary / Build & Push Go Backend / Build & Push Vite Frontend / Create Release (Tag Only)。

### CI Build Check（两轮，job 级结论一致）

| job | run 39888709123 @0b18d49 | run 39889784482 @5f3ac66 |
|---|---|---|
| Build Go Backend | ✅ | ✅ |
| Build Agent | ✅ | ✅ |
| Test Installer Scripts | ✅ | ✅ |
| Go Backend PostgreSQL Contract | ✅（当时仅跑单条，见 §3） | ✅（全 5 条实跑，见 §3） |
| Go Full Tests | ❌ 单点：`TestNotifyImportedNodeDownContract` | ❌ 同一单点、同一签名 |
| Build Frontend | ❌ demo check：`node/traffic/month` 未录 | ❌ 同因 |

### 其余工作流

- Publish Skill to npm（run 39888713004）：failure，日志实证 `npm error code ENEEDAUTH`——惯例红，不阻塞发布（与历版同）。
- Deploy Docs（run 39888709089）：success。

## §3 PG 契约 CI 实跑（含本版处置说明）

任务口径要求本版新增计数并发契约两条（F-S01）在 CI 实跑并确认结果。核查发现：CI 的 PG 契约 job 自 v1.0.0 起 `-run` 只写单条精确名 `TestPostgresNodeCreateRepairsMissingIDDefaultContract`，新增两条（及 Seq 两条）此前只在发版门真机顺跑、从未进 CI。为使「CI 实跑」成为事实而非口头，本版将该 job 的 `-run` 放宽为 `TestPostgres` 前缀并加 `-v`（提交 `5f3ac66`，仅 CI 配置、不触产品代码与发布产物）。run 39889784482 日志逐条实证（postgres:17 服务）：

| 契约 | 结果 |
|---|---|
| `TestPostgresApplySubscriptionCounterSamplesConcurrentDoubleApplyContract`（新，F-S01） | ✅ PASS (0.42s) |
| `TestPostgresApplyInboundCounterSamplesConcurrentDoubleApplyContract`（新，F-S01） | ✅ PASS (0.42s) |
| `TestPostgresLegacySeqContract` | ✅ PASS (0.40s) |
| `TestPostgresNodeCreateRepairsMissingIDDefaultContract` | ✅ PASS (1.02s) |
| `TestPostgresSeqConcurrentDoubleApplyContract` | ✅ PASS (0.43s) |

## §4 资产核对（12/12 通过）

6 产物逐个下载、与各自 `.sha256` 比对全部一致（sha 见发版说明凭据表）。发布副本核对：`install.sh`、`panel_install.sh` 均 `PINNED_VERSION="1.4.1"`、`REPO="iiiiiiiii7125-art/flmx-max"`；docker-compose-v4/v6 的后端（flmx-flux-panel-backend）与前端（flmx-vite-frontend）镜像标签均 `:1.4.1`；发布 `gost-amd64 -V` 输出 `gost 1.4.1 (go1.25.14 linux/amd64)`，正式版本注入成立。

GHCR 标签：API 查询因当前 token 无 `read:packages` 返回 403，未能 API 核对（与历版同，如实登记）；以主发版流 Build & Push Go Backend / Vite Frontend 两 job success 为推送成立依据，未假报。

## §5 异常定案

1. **Go Full Tests 单点红 `TestNotifyImportedNodeDownContract`**：两轮 CI 同点同签名（`expected 1 webhook calls, got 0` 型）。与 1.4.0 发版说明在案定案逐字同型——同一失败在 1.4.0 父提交 CI 已存在、本地隔离跑 PASS，定性 **CI 环境确定性红、既存、非 1.4.1 回归**，不改测试或生产代码凑绿。本版特别核对：任务点名的 TestFederation 时序项与 PendingDelete flake 在两轮 CI 均**未**红（Go Full Tests 的失败集合只有上述一条）。
2. **Build Frontend 的 demo check 红**：本轮 chrome 正常启动，失败为 `[demo] not recorded: node/traffic/month`（#/node 与 #/monitor 两处 + 两条级联，共 4 problems）——1.4.0 新增端点的 demo fixtures 未录，属 1.4.0 线既存登记项（见 §6），前端构建本身 success。不阻塞发布。
3. **check-doc-consistency FAIL（本地，发布前）**：仅既存 1 项「技能包未覆盖 `/api/v1/node/traffic/month`」（1.4.0 移植遗留），版本三处一致检查在同步后为 1.4.1 PASS。本版按任务口径如实记录、不顺手扩面修它，登记 R5（§6）。
4. **publish-skill ENEEDAUTH**：惯例红，不阻塞（见 §2）。

## §6 遗留登记（转 R5）

| # | 项 | 来源 / 口径 |
|---|---|---|
| 1 | 发版门观察 O-1：gost 计量路径下 forward.in_flow/out_flow 落 scaled 值而非原始字节（ratio 0 的转发面板呈零流量；计费本身正确）——R5 立项定性 | R4-RELEASE-GATE.md 项 3，建议 P3 |
| 2 | R3 期 PendingDelete 白名单测试时序 flake deflake 立项（pristine main 同签名复现、负载相关；禁以重试/加等待掩盖） | R4-FIX-REPORT §6-5 |
| 3 | G-S02 硬门：备份导入成员对应强校验（warning 已落地、硬门未做）携带 R5 | R4-FIX-REPORT §6-3 |
| 4 | 差集撤销无痕迹残余：撤销墓碑表方向（schema 变更，本轮口径外） | R4-FIX-REPORT §6-4 |
| 5 | N-35 / N-36 护栏建议待主控批（文档批未擅自入正本） | R4-FIX-REPORT §6-8 |
| 6 | SIP023 多段在 Surge / Loon / QX 面的残余口径（本版仅 sing-box 面解除并实证） | R4-SIP023-R 范围注记 |
| 7 | 入站整对象 presence 八键中缓议 1 键（remark）的后续处置 | R4-X-verdicts §（R4X-018 立 7 / 缓立 1） |
| 8 | `node/traffic/month` 的 demo fixtures 补录（CI demo check 红的根因） | 1.4.0 线登记备查；本版 §5-2 |
| 9 | 技能包覆盖 `/api/v1/node/traffic/month`（check-doc-consistency 既存 FAIL） | 1.4.0 线登记备查；本版 §5-3 |
| 10 | R4X-023 / R4X-024 占位编号的台账正式追认 | R4-FIX-SUPP-REPORT |
| 11 | `TestNotifyImportedNodeDownContract` 的 CI 环境确定性红根因排查（本地 PASS、CI 恒红，两版在案） | 本版 §5-1（承 1.4.0 定案） |

注：PG 契约 CI 实跑面已于本版补齐（§3），后续轮次该项不再属「仅门顺跑」口径。

## §7 发版门衔接

发版门两轮结论（R4-RELEASE-GATE.md / R4-RELEASE-GATE-2.md）：首轮 11 PASS / 1 FAIL（F-S01）+ 加抽两项 PASS；F-S01 定点修复（批 S3，integrate-r4 @ 4167781）后复验项 11 PASS（PG 契约两条 ×2 轮 + 逐字探针并发/顺序各 1 组），放行。R4X-021 rig 产物结案、SIP023 EIH 实证生效，均已写入发版说明。

— RELEASE-141 END —
