# state.md — 审计循环状态

- 任务：flvx-max 自主审计-修复-复核 永续循环（Master Loop v6，2026-10-19 17:20 起取代 v5）
- 仓库：https://github.com/iiiiiiiii7125-art/flvx-max
- 基线分支：main @ 9a2810b（2026-10-19）
- 当前轮次：R0（基线审计），分支 audit/loop-1
- 分支策略（v6）：每轮独立分支 `audit/loop-<轮次>`；通过复核的轮次合入 `audit/integration`（已建，基线 main@9a2810b）；**绝不直接动 main**；推送 audit 分支到远端只用非 force；禁止发布 release / 打 tag / force-push。
- 主视角轮换：轮次 mod 7 → 0 脚本正确性 / 1 功能契约 / 2 安全 / 3 并发与资源 / 4 数据一致性 / 5 格式与兼容 / 6 可观测性与性能
- 决策记录：`decisions.md`（v6 规范名；v5 的 DECISIONS_NEEDED.md 已删除）
- 需人类操作（发布/合并 main 等）→ `BLOCKED_FOR_HUMAN.md`，继续下一项
- 收敛判据：C1–C9（见任务书 §9）；轮次上限 30
- 汇报节奏（v6 §10）：每轮写 rounds/R<N>.md；每 5 轮简报；P0/P1 修复完成立即简短说明；收敛/30 轮未收敛输出最终报告
- P0/P1 开放数：P0=0，P1=3（TST-001 隧道删除硬失败、TST-002 内核删除硬失败、TST-003 备份导入用户必失败）
- 连续零 material 缺陷轮次：0（R0 起算）

## 轮次历史

| 轮次 | 分支 | 主视角 | 新增发现 (P0/P1/P2/P3) | 修复 | 验证 | 状态 |
|---|---|---|---|---|---|---|
| R0 | audit/loop-1 | 基线（全量测试+脚本静态检查） | 0/3/4/14 | — | build/vet 绿；go test：gost 全绿，backend 18 失败（3 P1 真缺陷+7 测试债务+2 环境+2 已知+4 同根因）；前端构建绿 | 完成 |
