# R18 FINAL 总结

基线：main @ a0c18a0（R17 修复 fix-r17 已合入并推 origin）。靶向复审轮，五路并行、每路单独汇报已收齐并验盘。

## 结论

- **R17-X-01（P1）关闭**：机理、去重、自愈、harness、计时四项复核无 FAIL；R17D-OPEN-01 悖论已解（分支机理已定位：UpdateService 失败后回滚 restore 能否重新注册旧服务决定毒化与否；INT「49ms 窗口清理」假说作废）。
- **本轮新立案：产品 P3 = 4**（R18-X-01…X-04），P0/P1/P2 = 0；文档 0；测试基建 0；流程 1（R18-PROC-01，测试机 lane 互斥排期）。
- **哨兵 53 行：51 PASS / 0 回退 / 2 承继**（S-16、S-34）；常设全集六项 + 列表/详情对账差集全部 ∅；轮换域 A4 除 R18-X-01 外新增 0；R13 关闭 7 项与 FINAL-P2-1 七段谓词维持关闭。
- **未收敛**（用户口径：一轮零新增产品问题；本轮新增产品 P3 = 4）。下一步按 R18-FIX-PLAN 修 4 项 + 流程项 P-01 → 开 R19 重审，不发版。

## 各路交付

- L1 R17-X-01 关闭验证：[R18-L1-x01-closure.md](sandbox://workspace/flvx-max/audit-out-132/r18/R18-L1-x01-closure.md)（23,617 字节）
- L2 管理 API 盲 append 核查：[R18-L2-config-service.md](sandbox://workspace/flvx-max/audit-out-132/r18/R18-L2-config-service.md)（27,703 字节）+ R18-L2-empirical.log（4,267 字节）
- L3 哨兵 + A4：[R18-L3-sentinel-a4.md](sandbox://workspace/flvx-max/audit-out-132/r18/R18-L3-sentinel-a4.md)（34,062 字节）
- L4 常设全集：[R18-L4-matrices.md](sandbox://workspace/flvx-max/audit-out-132/r18/R18-L4-matrices.md)（35,968 字节）+ 原料表 R18-L4-routes-raw.csv / R18-L4-tables-raw.txt
- L5 未验证清单：[R18-L5-unverified.md](sandbox://workspace/flvx-max/audit-out-132/r18/R18-L5-unverified.md)（22,747 字节）+ Phase A 日志与毒化样本

## 须知悉的三点

1. INT「守卫日志 48 条」无法从落盘证据重算（落盘仅 26 条、源头容器已删），不作关闭依据；守卫功能以 R18 窗口 14 条新判定链为准（V-06）。
2. 测试机多 lane 并行互相污染已立案流程项 R18-PROC-01；L5 items 2–5「仍未验证」系排期阻塞，R19 独占窗口重跑（V-08/V-09）。
3. 测试机离场对账全过（1/4/6/578/18、表 50、agent `aff338e3…`、gost `d48f09e1…`、unit `a12afd9c…` null、磁盘 3.6G）；`R17-FIX-D-restore.sh` 前提已失真（`/root/r17d/flux_agent.pristine` 部署时被修后 agent 覆盖），L1 已改手动恢复并改用 `/root/r17c/flux_agent.pristine`。
— R18-FINAL-summary END · 自证：本行之前正文字节数 2565 · END
