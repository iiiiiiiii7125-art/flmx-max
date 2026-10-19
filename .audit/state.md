# state.md — 审计循环状态

- 任务：flvx-max 自主审计-修复-复核 永续循环（Master Loop v6，2026-10-19 17:20 起取代 v5）
- 仓库：https://github.com/iiiiiiiii7125-art/flvx-max
- 基线分支：main @ 9a2810b（2026-10-19）
- 当前轮次：R2（完成，已合入 audit/integration）；下一轮：R10（主视角=并发与资源），分支 audit/loop-10
- 分支策略（v6）：每轮独立分支 `audit/loop-<轮次>`；通过复核的轮次合入 `audit/integration`（已建，基线 main@9a2810b）；**绝不直接动 main**；推送 audit 分支到远端只用非 force；禁止发布 release / 打 tag / force-push。
- 主视角轮换：轮次 mod 7 → 0 脚本正确性 / 1 功能契约 / 2 安全 / 3 并发与资源 / 4 数据一致性 / 5 格式与兼容 / 6 可观测性与性能
- 决策记录：`decisions.md`（v6 规范名；v5 的 DECISIONS_NEEDED.md 已删除）
- 需人类操作（发布/合并 main 等）→ `BLOCKED_FOR_HUMAN.md`，继续下一项
- 收敛判据：C1–C9（见任务书 §9）；轮次上限 30
- 汇报节奏（v6 §10）：每轮写 rounds/R<N>.md；每 5 轮简报；P0/P1 修复完成立即简短说明；收敛/30 轮未收敛输出最终报告
- P0/P1 开放数：P0=0，P1=0（TST-001/002/003 已修复提交）；P2 开放 0（SCR-001/002/003 已修复）；P3 开放 4（SCR-006/010/012/016 留后轮，其中 012/016 拟转产品决策/接受风险）
- 连续零 material 缺陷轮次：0（R0 起算；R1 有新增发现，计数未启动）

## 轮次历史

| 轮次 | 分支 | 主视角 | 新增发现 (P0/P1/P2/P3) | 修复 | 验证 | 状态 |
|---|---|---|---|---|---|---|
| R0 | audit/loop-1 | 基线（全量测试+脚本静态检查） | 0/3/4/14 | — | build/vet 绿；go test：gost 全绿，backend 18 失败（3 P1 真缺陷+7 测试债务+2 环境+2 已知+4 同根因）；前端构建绿 | 完成 |
| R1 | audit/loop-1 | 功能契约（F14 数据面+S4 脚本动态） | 0/0/0/3（SCR-018/019/R5R1-001，P3 可达；SCR-009 由潜在升级可达） | TST-001/002/003（P1）、TST-004、SCR-001/002/003（P2）、SCR-004/005/007/008/009/011/013/014/015/017/018/019、R5R1-001（P3） | R4 全量回归绿（known-baseline 外零失败）；R5 四问全过 | 完成，已合入 audit/integration |
| R2 | audit/loop-2 | 安全（F1 节点与内核） | 0/1/0/1（F1X-001 P1 可达；R5R2-001 P3 加固） | F1X-001（P1，监控 TCP SSRF）、R5R2-001（P3，DNS TOCTOU） | R4 全量回归绿（known-baseline 外零失败）；R5 四问全过 | 完成，已合入 audit/integration |
| R3 | audit/loop-3 | 并发与资源（F2 入站与模板） | 0/0/1/1（F2X-001 P2 可达；R5R3-001 P3） | F2X-001（P2，端口分配 TOCTOU）、R5R3-001（P3，锁范围过大） | R4 全量回归（known-baseline 外零失败）；R5 首次退回后 spot-check 通过 | 完成，已合入 audit/integration |
| R4 | audit/loop-4 | 数据一致性（F3 证书与 ACME） | 0/0/0/0（零新增） | 无 | R4 build/vet 绿（零代码改动）；R5 四问全过 | 完成，已合入 audit/integration |
| R5 | audit/loop-5 | 格式与兼容（F4 分流与出站） | 0/0/0/0（零新增；R5R5-001 为 P3 观察） | 无（R5R5-001 观察项不阻塞） | R4 build/vet 绿（零代码改动）；R5 四问全过 | 完成，已合入 audit/integration |
| R6 | audit/loop-6 | 可观测性与性能（F5 中转与隧道） | 0/0/0/0（零新增） | 无 | R4 build/vet 绿（零代码改动）；R5 四问全过 | 完成，已合入 audit/integration |
| R7 | audit/loop-7 | 脚本正确性（F6 转发业务）+ 防假收敛 L1-L6 | 0/1/0/0（R5R7-001，P2 可达） | R5R7-001（P2，限速 fail-open） | R4 全量回归（known-baseline 外零失败）；L6 spot-check 通过 | 完成，已合入 audit/integration |
| R8 | audit/loop-8 | 功能契约（F7 订阅） | 0/2/0/0（F7X-001、R5R8-001，P2 可达） | F7X-001（P2，凭据下发失败无重试）、R5R8-001（P2，reset 无重试） | R4 全量回归（known-baseline 外零失败）；R5 spot-check 通过 | 完成，已合入 audit/integration |
| R9 | audit/loop-9 | 安全（F8 导入） | 0/0/0/0 | — | build/vet 通过；R5 复核通过 | 完成，已合入 audit/integration |
