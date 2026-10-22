# FLMX 1.3.5 发布报告（RELEASE-135）

- 任务：发布 FLMX 1.3.5（用户已预授权：R1 修复按推荐口径发版）
- 来源：集成树 `~/workspace/integrate-r1` @ `219e3ac`（R1 修复 54/54，R1-FIX-REPORT.md）；发版门真机复验 9/9 PASS（R1-RELEASE-GATE.md）
- 结论：**发布成功。Release Published，12/12 资产 sha 核对一致，无真回归、无未定案异常。**

## 一、合 main 与版本同步

| 步 | 结果 |
|---|---|
| 合并 | `git merge --no-ff integrate-r1` → main `23a5fd8`「merge integrate-r1: FLMX 1.3.5 R1 审计修复版」，零冲突、无丢提交（ff2e0ef 本为 integrate-r1 祖先，82 提交全量并入，102 文件 +7472/−745） |
| 版本字面量同步 | `a0a76a4`：skills/flmx-api/package.json、SKILL.md 的 version 与 doc/ai-skill.md 发布示例 tag 三处 1.3.4 → 1.3.5；`scripts/check-doc-consistency.sh` PASS（三处一致：1.3.5）。仓库内 install.sh/panel_install.sh 的 `PINNED_VERSION=""` 保持不动（流水线改写发布副本） |
| 发版说明 | `4c819e9` 定稿 `audit-out-132/next/RELEASE-NOTES-1.3.5.md`（按批次分组 + BC-01…BC-14 逐条变更与可转发文案 + 升级注意事项）；凭据节发版后回填（见 §五异常定案） |
| 推送 | main `ff2e0ef..4c819e9` 已推 origin |

## 二、tag 与 CI

- tag：`1.3.5`（纯数字，指向 `4c819e9`），已推送。

| 流水线 | run | 结论 | 备注 |
|---|---|---|---|
| Build and Push Images（主发版流，tag 触发） | 39745016488 | ✅ success（8m43s） | 五 job 全绿：Check Version / Build & Push Go Backend / Build & Compress GOST Binary / Build & Push Vite Frontend / Create Release (Tag Only)；Release 于 2026-10-22T08:12:40Z Published |
| CI Build Check（main 推送） | 39745003138 | ✅ success（1m55s） | 六 job 全绿：Build Frontend / Build Agent / Build Go Backend / **Go Backend PostgreSQL Contract** / **Test Installer Scripts (systemd and Alpine OpenRC)** / Go Full Tests——**首跑全绿，无契约 flake 定案事项**（与 1.3.4 首跑一红不同） |
| Publish Skill to npm（tag 触发） | 39745016982 | ❌ failure（惯例红，不阻塞） | 仅 `Publish to npm` 步失败，日志实证 `npm error code ENEEDAUTH`（NODE_AUTH_TOKEN 为空）；`Sync package.json version with tag` 步已按 1.3.5 成功执行 |
| Deploy Docs（main 推送） | 39745003315 | ✅ success | pages build and deployment 39745057135 success |

## 三、流水线内专项核对（发版门遗留清单）

| 项 | 结论 | 证据 |
|---|---|---|
| X-P3-33 Release 唯一来源 | ✅ 核实 | publish-skill 本轮实跑全程无 Create GitHub Release 步（job 步骤清单已核），Release 仅由 docker-build 的 Create Release job 创建，无双 Release 冲突 |
| PG 契约 | ✅ 通过 | CI Build Check 的 Go Backend PostgreSQL Contract job（postgres:17 + FLMX_POSTGRES_TEST_DSN）success |
| X-P2-12 Alpine/OpenRC 真实环境 | ✅ 通过 | CI Build Check 的 Test Installer Scripts (systemd and Alpine OpenRC) success；主发版流 Create Release job 内的「Verify release installer on Alpine OpenRC」步随 job success 一并通过 |
| agent 版本注入（发版门观察项） | ✅ 正式版本 | 发布资产 gost-amd64 实跑 `-V` → `gost 1.3.5 (go1.25.14 linux/amd64)`，非 dev |

## 四、Release 与资产核对

- Release：**Published**（draft=false、prerelease=false），https://github.com/iiiiiiiii7125-art/flmx-max/releases/tag/1.3.5 ，published_at 2026-10-22T08:12:40Z。
- 资产 12 个逐个下载核 sha（发布 .sha256 与本地实算比对）：

| 产物 | sha256（实算=发布） | 结果 |
|---|---|---|
| gost-amd64 | cbb50198cc0d388dfddf15d2bd45a5c0f2920401015957722bddbca199891122 | ✅ |
| gost-arm64 | 5f619ce7c22a28a3cfe8a34e93053827bb2f47cbd713080c6246dd64fbef3aa6 | ✅ |
| install.sh | 571ceed867af1c795c8f3ca95b9bcc045567a27810facd630027b5946a894918 | ✅ |
| panel_install.sh | 9086511fa7ee8de3c49744e1ce562578d71b4ed5c8430f93e9aa56937f887cd3 | ✅ |
| docker-compose-v4.yml | 502e8a2873c639d73bb4d6a74ec7d6ce9b049de646f5ab8daa05fe0195b0dec9 | ✅ |
| docker-compose-v6.yml | 739d43eed7bbdb1be8a71f660d07964b25c82c97c59bfaf66e49887d0815efa4 | ✅ |

- 发布副本口径：install.sh 与 panel_install.sh 均 `PINNED_VERSION="1.3.5"`、`REPO="iiiiiiiii7125-art/flmx-max"`；docker-compose-v4.yml 镜像标签 `flmx-flux-panel-backend:1.3.5`、`flmx-vite-frontend:1.3.5`。
- GHCR 镜像标签：当前 token 无 read:packages 权限，API 核对返回 403，**未做 API 核对**（与 1.3.3/1.3.4 先例同）；以主发版流 Build & Push Go Backend / Vite Frontend 两 job success 及 compose 资产标签为据登记。

## 五、异常与定案

1. **发版说明文件写入截断（已修复）**：定稿提交 `4c819e9` 中的 RELEASE-NOTES-1.3.5.md 在「升级注意事项」第一节中途被截断（尾部混入字面截断标记、凭据节缺失）——写入通道截断所致，提交前未检出。定案：GitHub Release 正文由流水线模板生成、不引用该文件，**发布本体不受影响**；已在发版后把缺失部分（升级注意事项后半 + 凭据节实填）补齐，随本报告一并提交 main（与 1.3.4 的 ff2e0ef 回填提交同为 tag 后落 main 的口径）。教训：长文件写入后必须以字节数/关键探针复核落盘完整性再提交。
2. publish-skill ENEEDAUTH 红：惯例项（每版皆红），定案不阻塞；本轮其版本同步步已实际生效（1.3.5），npm 凭据补齐后可直接转绿，且转绿后无 Release 冲突面（见 §三）。

## 六、遗留登记（转下一轮 R2）

- FIX-REPORT §六残余：federation_transport.go 的 NAT64 内嵌 IPv4 判定待核；ws 会话上限检查/登记非原子（观察，未立案）；基建文本残余 3 项（catalog I 域「最多 10 个」、checklist A-01「191 条路由」、catalog「33 条」计数）；X-P3-17 台账勘误建议。
- FIX-PLAN §三.5 的 R2 带入项不变：A5-06 客户端版本域实测、IsSafeRemoteAddr 链路本地专项、D6-09 goja 压测、D7-06…09 补全、A7-04 日报台账 backlog、L-S1 真实配置形态。
- GHCR `:1.1.0` 残留镜像仍待用户网页端手动删除（沿袭项，与本版无关）。
