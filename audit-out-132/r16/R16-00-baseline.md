# R16 基线

- 审计对象：main @ b54b44f（R15 修复 fix-r15 已合入并推 origin）
- 提交：b54b44f772d8b4efdd6d08b1fbbbc40c8091a143（short b54b44f），2026-10-24 21:55:16 +0000，「docs(r15): archive V-06 verdict corrections (were working-tree-only)」
- origin/main 与本地 main 均为 b54b44f（实测一致）
- 路由：handler.go HandleFunc 计数 201（字面量 200 条 + ghMirrorPath 常量 1 条）+ router.go /system-info 1 条 = 202，与钉死值一致，待各域复核
- 数据表：store/repo repository.go:327 AutoMigrate 模型集 49 项，与 5bd5a90 同值（TableName() 定义计数 48 为另一口径，非漂移，见 L4 注记）
- 哨兵表：R16-00-sentinel.md，承 R15 53 行 S-01…S-53 原样沿用，基线重钉为本轮提交；开工前落盘
- 轮换全量抽审域：A2（API）——R15 轮换 A5，本轮轮换另一域；A2 与信封码关闭项 R15-X-01 邻接
- 只读 worktree：~/workspace/.r16-tree @ b54b44f（detached HEAD），供各域读数、永不修改；共享工作树现处 feat-latency-hint @ a99d68d 且有未提交审计文件残留，不得用作读数树
- 本轮不发版、不打 tag、不改产品代码（只读审计）；R15-FIX-REPORT.md 在工作树与全部历史中均不存在（协调者已查），关闭验证不得引用该文件，须以修复提交本身 + 新探针证据为准

R16-00-baseline END
