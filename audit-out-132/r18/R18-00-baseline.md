# R18 基线

- 审计对象：main @ a0c18a0（R17 修复 fix-r17-a/b/c 已合入并推 origin）
- 提交：a0c18a0dc90b2f9c7a67e8a2a016cc1d97505fb5（short a0c18a0），2026-10-25 01:33:23 +0000，「docs(r17-d): integration report + acceptance evidence (R17-X-01)」
- origin/main 与本地 main 均为 a0c18a0（实测一致，`git log -1 main` 同值）
- 树哈希：a0c18a0^{tree} = f11ac9e88f5f684078be63d166af72dfbca96e33
- 最近发布 tag 仍为 1.4.8（bd9f99d）；本轮不发版、不打 tag、不改产品代码（只读审计）
- 产品改动（相对 R17 基线 5473377）：仅 fix-r17-a/b/c 的产品/测试文件——`go-backend/internal/http/handler/add_service_guard.go`（新增 279 行，panel 侧 AddService 幂等守卫 + dispatch record）、`add_service_guard_test.go`（新增）、`control_plane.go`（26 行改动，A4 域邻接）、`handler.go`（17 行）、`go-gost/main.go`/`program.go`（agent 启动诊断）、`go-gost/x/config/dedupe.go`（新增，去重服务）、`dedupe_test.go`、`loader.go`、`loader/duplicate_service_test.go`、`r17_fix_a_test.go`、`persist.go`、`testdata/r17-poisoned-gost.json`、`x/socket/service.go`、`r17_fix_a_test.go`、`install.sh`（16 行）；其余为审计归档文件。A4 域直接相关改动：`control_plane.go`（AddService 守卫接线）与 `handler.go`（dispatch record），转发/计费核心文件（mutations.go、flow_upload_batch.go、nftables_traffic.go、realm.go、flow_policy.go、user_quota.go）本轮 diff = 0 行
- FINAL-P2-1 七段谓词已在 main：在位（`backup_import_repush.go:38-47` 七段或，含 `SpeedLimitsImported` 与 `UserTunnelsImported`；`backup_final_p2_1_test.go` 4 项断言在位）
- 路由：钉死值 202、数据表 49 张，待各域复核（双向差集）
- 哨兵表：R18-00-sentinel.md，承 R17 53 行 S-01…S-53 原样沿用，基线重钉为本轮提交；开工前落盘
- 轮换全量抽审域：A4（转发与计费）——R15 轮换 A5、R16 轮换 A2、R17 轮换 A3，本轮轮换 A4
- 只读 worktree：~/workspace/.r18-tree @ a0c18a0（detached HEAD，`git status` 空），供各域读数、永不修改；共享工作树现处 feat-latency-hint @ a99d68d 且有未提交审计文件残留，不得用作读数树；测试在 `git archive a0c18a0` 快照 `~/workspace/.r18-tmp/r18snap` 上跑，探针文件跑后即删
- 本轮为靶向复审轮：哨兵回归（S-01…S-53）+ 轮换域 A4 全量抽审 + R13 关闭 7 项与 FINAL-P2-1 七段谓词维持关闭复核

R18-00-baseline END
