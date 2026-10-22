# FLMX 1.3.6 发布报告

- 版本：**1.3.6**（R2 审计修复版；R2 总账 49 条 49/49 关闭，发版门真机复验 11/11 + 补充 3/3 PASS 后按用户预授权链发版）
- Release：https://github.com/iiiiiiiii7125-art/flmx-max/releases/tag/1.3.6
- 状态：**Published**（非 draft / 非 prerelease），发布时间 `2026-10-22T12:52:55Z`，target `60e940e`
- 发版说明：`audit-out-132/r2/RELEASE-NOTES-1.3.6.md`（凭据节已回填）

## 1. 提交链

| 提交 | 内容 |
|---|---|
| `8c1a510` | merge integrate-r2（`integrate-r2 @ 3c53ced` 合入 main；`1672dc4` 为祖先，114 文件 +9222/−663，零丢提交） |
| `7f99cf0` | 版本字面量同步 1.3.5 → 1.3.6（`skills/flmx-api/package.json`、`SKILL.md`、`doc/ai-skill.md` 示例 tag 两处，共四处）；`scripts/check-doc-consistency.sh` PASS（三处一致） |
| `60e940e` | 发版说明定稿（tag `1.3.6` 指向点；定稿落盘后已做字节数 + 尾 20 行探针复核，16559 字节完整无截断） |
| `1bacbef` | 发布后凭据回填（发版说明凭据节写入 CI run 号与资产核对结论；tag 不动） |

仓库内 `PINNED_VERSION` 维持流水线口径（仓库副本为空、发布副本由 release job 改写），未手工编辑，与 AGENTS.md 反模式条一致。

## 2. CI 结论（如实记录）

| 工作流 | run | 结论 |
|---|---|---|
| Build and Push Images（tag 触发，主发版流） | `39780809259` | **success**（Check Version / Build & Push Vite Frontend / Build & Push Go Backend / Build & Compress GOST Binary / Create Release 五 job 全绿） |
| CI Build Check（main @ `60e940e`） | `39780800141` | **failure（单点）**，见 §3 定案 |
| CI Build Check（main @ `1bacbef`，回填后复跑） | `39782565937` | **failure（同一单点）**，其余 5 job 全 success |
| Publish Skill to npm（tag 触发） | `39780809198` | failure：Publish to npm 步 `ENEEDAUTH`（历版惯例红，不阻塞发布） |
| Deploy Docs（main 推送） | `39780800028` | **success** |

main CI 两次运行的 job 明细一致：Go Full Tests (backend + agent) **success**、Go Backend PostgreSQL Contract **success**（真 PG 实跑通过；1.3.5 时该 job 曾以时序 flake 红一次，本版两次均绿）、Build Go Backend **success**、Build Agent **success**、Test Installer Scripts (systemd and Alpine OpenRC) **success**、Build Frontend **failure**。

关于 check-contract-fields：CI 中无此 job；其 C7×8 红是本地脚本在 1.3.5 未改动基线同命令复跑同样出现的口径问题（脚本与已发布代码类型口径不一致），非 1.3.6 引入。本报告按实际记录，不改判；处置见 §5 遗留登记。

## 3. 异常及定案：main CI 单点红（demo 录制缺口）

- 现象：Build Frontend 的 `Build` 步（tsc + vite build）**success**，其后 `Check online demo` 步失败，日志原文：`FAIL admin #/monitor: [demo] not recorded: monitor/access`、`1 problem(s). Recording missing for new pages? See scripts/demo/README.md.`
- 根因：R2 批 11 新增监控访问日志端点 `monitor/access`（A7 域修复），前端监控页在 demo 模式下会请求该端点，但 `scripts/demo` 的录制 fixtures 未补该端点的录制，演示站完整性检查因此报缺。两次 main CI 运行（`60e940e`、`1bacbef`）同点同因失败，属确定性缺口、非 flake。
- 影响面：仅在线 demo（演示站）的录制完整性；不影响发布镜像（前端镜像由主发版流独立构建并 success）、不影响面板真实功能（发版门已真机验过监控访问日志本身）。
- 定案：**不阻塞 1.3.6 发布**——主发版流 success、Release 已 Published 且资产全量核对通过（§4）；该缺口登记为遗留、转 R3 必修（§5 第 4 项）。本发布任务不擅自改判、不为过检临时改代码。

## 4. Release 资产核对（12/12）

12 个资产逐个下载、以发布 `.sha256` 逐个比对：**12/12 一致**。

| 资产 | 字节 | sha256（实测 = 发布值） |
|---|---|---|
| gost-amd64 | 9193400 | `222679025085cb000d42786da648e86044e9235c08b7d9dc2e17041beff0a205` |
| gost-arm64 | 7383352 | `3ec49e20d3a3d00e1299efc24231b1d42da76018e1c7c8d2707445ca1031760b` |
| install.sh | 30708 | `04de1472e9984ca48367be79190240f06b144fbb5b1959bcbdf295fdbd1a1ff3` |
| panel_install.sh | 62244 | `cd9b4aed973daedf98f988ffe90471467c4736bcea5299953e0c6545db2aafd5` |
| docker-compose-v4.yml | 3509 | `6ae38bbd77366e1d315242c2529f20a7ce37b624637698da4d85a26cb931fcf2` |
| docker-compose-v6.yml | 3569 | `0500db248ee63321df4bb83c5838f23511e04da94d80e9bd8755bceaa108e73d` |

发布副本内容核对：

- `install.sh` / `panel_install.sh`：`PINNED_VERSION="1.3.6"`、`REPO="iiiiiiiii7125-art/flmx-max"` ✓
- `docker-compose-v4.yml` / `docker-compose-v6.yml`：`flmx-flux-panel-backend:1.3.6`、`flmx-vite-frontend:1.3.6` ✓
- 发布 `gost-amd64 -V` 实跑：`gost 1.3.6 (go1.25.14 linux/amd64)`——正式版本注入，非 dev 占位 ✓
- GHCR 标签：当前 token 无 `read:packages`，API 与 ghcr.io tags/list 均返回 403，**未做 API 侧核对**（与历版同）；镜像推送以主发版流 Build & Push Frontend/Backend 两 job success 为据，不假报已核。

## 5. 遗留登记（转 R3）

1. **O-1（发版门观察项）**：基线夹具节点 SS2022 明文口令致整份 sing-box 配置 check 不过，与 R2 改动无关；R3 评估导入侧 SS2022 密钥格式校验。
2. **总账↔计划↔批次三方对账常设项**：R2 修复中 FIX-PLAN 曾漏分配 R2X-028/049、由补批 13 补关；后续每轮审计将「总账 ↔ 修复计划 ↔ 批次落地」三方对账列为常设检查项。
3. **C7 脚本口径对账**：check-contract-fields 的 C7×8 红属脚本与已发布代码的口径不一致（基线既存）；R3 做「脚本更新 vs 类型回迁」二选一裁定后落地。
4. **demo 录制缺口（本版新增，main CI 单点红根因）**：`scripts/demo` 未录制 `monitor/access`；R3 必修——按 `scripts/demo/README.md` 流程补录，并普查 R2 其余新增端点/页面是否同样缺录（修 A 漏 B 同族排查）。
5. **R2 已接受口径的残余**（发版说明已向用户披露，不重复立项）：日报/台账 pending 为进程内存态，部分送达后重启可能重算重发、已送达通道或多收一份；R2X-049 提交段复检在 PG Read Committed 下仍有理论窗口（裁定即以复检为止）；tunnelCreate 在联邦 CreateTunnel 后失败时远端预留无释放（既有同形态、非 R2 引入）；system_upgrade 版本 API 失败日志直印面板构造 URL 一处（无 token，观察级）。

## 6. 结论

FLMX 1.3.6 已正式发布：主发版流 success、Release Published、12/12 资产 sha 一致、发布副本版本注入正确。唯一异常为 main CI 的 demo 录制单点红（§3 已定案、不阻塞发布）与 publish-skill 的惯例 ENEEDAUTH 红，均如实记录并转 R3 跟进。
