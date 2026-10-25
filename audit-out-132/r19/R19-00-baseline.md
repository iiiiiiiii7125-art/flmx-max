# R19 基线（开工前落盘）

- 审计对象：main @ d66042c（`d66042c`，R18 修复 fix-r18 已合入并推 origin；origin/main 实测一致待核）。
- 审计轮：靶向复审轮。五路：L1 R18 四项关闭验证、L2 未决缺口裁定、L3 哨兵 + 轮换域 A6、L4 常设全集、L5 未验证清单复核。
- 护栏：N-01…N-37 全程有效；R18-PROC-01 测试机 lane 间互斥排期（协调者排期、独占窗口、锁文件 `~/workspace/.r19-testmachine-lock`）。
- 只读审计：不改产品代码、不打 tag、不发版。
— R19-00-baseline END
