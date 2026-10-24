# FINAL-P2-1 修复报告（修后发路径）

- 基线：main @ 249edee（R13 已合 main，终局候选版审计基线）
- 分支：fix-final-p2-1（停在分支完成点：未合 main、未打 tag、未发版）
- 任务书口径：只修 FINAL-P2-1 一条；FINAL-P3-1 / FINAL-P3-2 不占号、未顺手修
- 断言更新：无既有断言改动；新增 4 项断言（见下），均为本任务书点名驱动

## 问题（审计亲核证实）

`backupImportNeedsForwardRepush` 的重推谓词只认五段（forwards /
tunnels / nodes / inbounds / inboundRelays），`model.go:959-960` 已有的
`SpeedLimitsImported`、`UserTunnelsImported` 不在其中。单独导入限速段时
DB 已变、节点 limiter 仍为旧值且无其他尾钩子补推，窗口无限期。

## 与 R13X-001 的关系（同触发链、不同谓词缺口，非同物）

- 同链：同一触发函数 `backupImportNeedsForwardRepush` → 同一重推执行
  体 `repushForwardRuntimeAfterImport`（在线节点整节点
  `redeployLocalNodeRuntime`，sync 时按当前 DB 口径现查现算 limiter）。
- 不同缺口：R13X-001 是「整个重推触发不存在」（在线节点转发数据面
  无任何重推触发）；FINAL-P2-1 是 R13X-001 修复已落地的谓词本身漏
  两段——限速段单独导入时谓词判 false，重推执行体根本不跑。
  故本修复只补谓词两段，不动 R13X-001 的执行体、回执字段与离线
  节点语义。

## 改动（产品代码 1 文件，+7/−1）

`go-backend/internal/http/handler/backup_import_repush.go`

- 谓词函数 `backupImportNeedsForwardRepush`（:38）：
  - 前：:43-44 五段或（Forwards / Tunnels / Nodes / Inbounds /
    InboundRelays）
  - 后：:43-45 七段或，新增
    `result.SpeedLimitsImported > 0 || result.UserTunnelsImported > 0`
- 注释（:33-37）：写明两段触发的理由（转发 speed_id/ip_speed_id
  直引与 user_tunnel.speed_id 间接引均在 sync 时现算，故限速-only
  导入无重推即无限期旧值服役）。
- 未改动：谓词其他五段语义、重推执行体、回执字段、agent 协议、
  DB schema、handler 签名。无前端改动。

## 先红后绿

新增断言文件
`go-backend/internal/http/handler/backup_final_p2_1_test.go`（4 项）：

1. `TestFinalP21RepushTrigger`（谓词表）：speedLimits-only → true、
   userTunnels-only → true；kernels-only / subscriptions-only → false；
   forwards 锚点 → true；nil → false。
   - 红：speedLimits-only / userTunnels-only 均为 false（修复前实测）。
2. `TestFinalP21SpeedLimitsOnlyImportRepushesLimiter`（真导入 e2e）：
   转发直引规则 R=10 Mbps → 仅导 speedLimits 段（R 改为 1 Mbps）→
   回执 `speedLimitsImported` = 导出段行数、repushNodeIds 含在线节点、
   失败清单为空、节点收到 R 的新限速载荷（1 Mbps 线）。
   - 红：修复前回执 repushNodeIds 为空（谓词判 false）。
   - 注：初版断言写死 `speedLimitsImported:1`，实测为 2（导出段含
     R 与回退规则两行同导）；已修正为与导出段行数比对，非产品问题。
3. `TestFinalP21UserTunnelsOnlyImportRepushesLimiter`（真导入 e2e）：
   转发无直引、UT 引 R=10 → 仅导 userTunnels 段（UT speed_id 改为
   回退规则 F=5）→ 回执 `userTunnelsImported:1`、repushNodeIds 含在线
   节点、节点转发服务 limiter 回退为 F。
   - 红：修复前回执 repushNodeIds 为空。
4. `TestFinalP21SubscriptionOrKernelOnlyImportDoesNotRepush`（对照）：
   在线节点上直接调用重推体，subscriptions-only / kernels-only
   均 attempted 与 failed 为空。

绿后复跑：4 项全部 PASS（handler 包内单测约 3s）。

## 全量回归

- `go build ./...`：exit 0
- `go vet ./...`：exit 0
- `go test ./...`：16 包 ok，产品失败 0；唯一 FAIL 为
  `internal/health` 的 `TestValidateMonitorTCPTarget/公网域名放行`
  一格（沙箱 fake-IP DNS 使 example.com 落 198.18/15 保留段，与
  R13 基线同签名、CI 真实 DNS 下绿），按环境红单列。
- 前端 `tsc --noEmit`：exit 0，无诊断。
- 回归日志：[FINAL-P2-1-test.log](sandbox://workspace/flvx-max/audit-out-132/final/FINAL-P2-1-test.log)

## 未验证 / 移交

- 真机终态（测试机在线节点 limiter 实测）未在本分支内做，移交父
  代理的集成与门复验（门须点名：限速段单独导入后在线节点 limiter
  终态与 DB 一致）。
- FINAL-P3-1 / FINAL-P3-2 未动，仍按终局候选版审计立案留待后续处置。

— FINAL-P2-1-FIX-REPORT END · 分支 fix-final-p2-1 · 产品失败 0 · 未合 main、未打 tag、未发版 · END
