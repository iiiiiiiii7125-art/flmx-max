# R5 修复 · 批 4（计费口径）报告

- 条目：**R5X-013（P3）关闭**——转发「已用流量」三数据面统一落 scaled 与 forward.md「如实统计」及 R3 raw/scaled 分离裁定不一致。
- 裁定正本：`R5-X-verdicts.md` §5.1 采 F 案 (a) 代码向文档对齐；`R5-FIX-PLAN.md` 批 4 节；探针数值正本 `R5-F-report.md` §R5-F-01 / `R5-L-report.md` §3 [L]-01。
- 基线：`integrate-r5` @ 112a911（tag 1.4.1 发布态）；分支 `fix-r5/b4`，worktree `~/workspace/r5-wt/b4`。
- 行为变更：BC-R5-02（forward 行口径切换，历史已落 scaled 值不回溯）。

## 1. 修复内容（delta 拆 raw/scaled 两组）

| 文件 | 改动 |
|---|---|
| `go-backend/internal/store/repo/repository.go:78-95` | `FlowUploadCounterDelta` 新增 raw 组 `RawInFlow/RawOutFlow` + `HasRaw` 标记；`InFlow/OutFlow` 语义明确为 scaled 组（user/user_tunnel 行与 quota 口径） |
| `go-backend/internal/store/repo/repository.go:139-147`（`applyFlowUploadDeltasTx`） | forward 行聚合改用 raw 组；user/user_tunnel 行聚合保持 scaled 组不变。`HasRaw=false` 的存量直接构造（旧测试/外部调用以单一 InFlow/OutFlow 构造 delta）回退旧口径（forward 落 scaled），行为不变——repo 全包既有断言（含 480/660 直构用例）全绿实证 |
| `go-backend/internal/http/handler/flow_upload_batch.go:178-190`（gost 路径） | delta 填 raw 组 = `pl.rawIn/pl.rawOut`（与 `forwardTraffic` 指标桶同源同值）、置 `HasRaw=true`；scaled 组与 quotaUsage 不变 |
| `go-backend/internal/http/handler/nftables_traffic.go:554-582`（nft/realm 共用 `buildNftFlowUploadBatch`） | delta 填 raw 组 = `delta.BytesIn/BytesOut` 全量原始字节（不受 TunnelFlow 单向计费门控，与指标桶 raw 累加同口径）；合并分支同步累加 raw 组并补 raw 溢出守卫；scaled 组与 quotaDelta 不变 |

三数据面覆盖：gost 独立构建器 + nft/realm 共用构建器，两处均已拆组；落账函数共用一处，无第三构建器（全集 grep `FlowUploadCounterDelta{` 非测试构造点恰 2 处，已逐处核）。

## 2. 三组数值验证证据（逐字按裁定，先红后绿）

验证载体：更名后整链用例 `TestR3B3ZeroRatioWholeChainQuotaZeroForwardRowRawPreserved`（见 §3 登记），种子隧道 flow=2、推送入 80 / 出 110（gost 线格式 U=110、D=80；nft BytesIn=80、BytesOut=110），经 `ApplyFlowUploadDeltasBatch` 真实落账后断言 DB 三行。

**先红**（测试先行、生产未改时）：

```
r3_b3_billing_nft_warning_test.go:114: gost ratio=0: forward in_flow must be raw 80, got 0
--- FAIL: TestR3B3ZeroRatioWholeChainQuotaZeroForwardRowRawPreserved
```

（旧构造 forward 落 scaled，ratio=0 时为 0，与 F 域探针「ratio=0 forward 计数恒 0」同签名。）

**后绿**（修复后同命令）：`--- PASS: TestR3B3ZeroRatioWholeChainQuotaZeroForwardRowRawPreserved (1.26s)`。三组数值逐组断言结果：

| 组 | forward 行（raw） | user / user_tunnel 行（scaled） | 配额增量 quotaUsage |
|---|---|---|---|
| ratio=2 / flow=2 推 80/110 | **80 / 110** ✅ | 160 / 220 | **380** ✅ |
| ratio=0 推 80/110 | **增长 80 / 110** ✅ | 0 / 0 | **0** ✅ |
| ratio=1 推 80/110 | 80 / 110（raw=scaled，三面同值不变）✅ | 80 / 110 | 190 |

gost 与 nft/realm 两路径每组各落账一次、断言值相同（三面口径一致）。另有 delta 级断言：ratio=2 时两路径 delta 的 raw 组 = 80/110 且 `HasRaw=true`。

## 3. 断言更新登记（裁定驱动，单独登记）

| 项 | 内容 |
|---|---|
| 更名 | `TestR3B3ZeroRatioWholeChainQuotaZeroRawPreserved` → `TestR3B3ZeroRatioWholeChainQuotaZeroForwardRowRawPreserved` |
| 依据 | verdicts §5.1 执行注记 + N-35 名实对齐：原名含 RawPreserved，但其实测对象仅为 `forwardTraffic` 指标桶与 quotaUsage，forward 行从未在其断言内（R5-L-report §3 同指） |
| 新增断言 | ① 落账后 forward 行 = raw、user/user_tunnel 行 = scaled（三组数值，§2）；② delta raw 组字段断言；③ ratio=1 三面对照组；④ 落账夹具补建 user_tunnel 行（种子夹具原无 UT 行、meta.UserTunnelID=0 时落账函数按 ID>0 跳过 UT 面，补建后重取 metas，属夹具完备化非断言放宽） |
| 既有断言 | quotaUsage（0 / 380）、scaled delta 0/0、forwardTraffic raw 保全、O6-005 零计费警告日志——逐字保留、未放宽、未删除 |
| 其他测试断言 | 本批未改任何其他测试断言（连带冲突 1 条见 §5，登记待裁、未擅改） |

## 4. forwardResetFlow / statistics_flow 来源口径核对结论

- **forwardResetFlow**（handler `mutations.go` → repo `ResetForwardFlow`，`repository_mutations.go`）：仅将目标 forward 行 `in_flow/out_flow` 清零，不触 user/user_tunnel 行、quota 与 statistics_flow。其唯一作用对象即 forward 行本身，口径切换后清零对象自动随之为 raw 口径，**与新口径一致、无需改动**；重置语义（只清该转发显示计数）与切换前结构相同。既有 `forward_reset_flow_test.go` / `repository_forward_flow_reset_test.go` 在本批回归中全绿。
- **statistics_flow**（`jobs.go:runStatisticsFlowJob`）：来源为 `ListAllUserFlowSnapshots` = **user 行** in_flow+out_flow 快照差分，属 scaled（计费）口径，与 user 行/quota 同源；**不读 forward 行**，故 forward 行切 raw 不影响其口径，切换前后一致、**无需改动**。同源消费面 `GetStatisticsFlows`（用户页统计）、`SumStatisticsFlowSince`（notify 汇总）均为该 scaled 快照的只读消费，口径自洽。既有 `jobs_test.go` 统计用例全绿。
- 变体搜索（修 A 漏 B）：forward/user/UT 三表同值累加的另一写面 `Repository.AddFlow`（repository.go）全仓**零调用方**（死代码/历史遗留），无生产路径可达；其单值签名无从拆 raw/scaled，本批未动，登记备查——若未来复活须按本批同口径拆组。peer-share 计量走 `peer_share.current_flow` 独立面，不写三表，不在本条口径内。

## 5. 连带断言冲突登记（未擅改，待主控裁定）

`TestFlowOwnershipLocalForwardRequiresReportingNode`（flow_ownership_test.go:181）：基线 PASS、本批后 FAIL（`node 2 local traffic=200 want=80`）。定性：该用例缺隧道行、meta 默认 ratio=1 / TunnelFlow=1，其期望 80 系**旧 scaled-forward 口径**值（仅入方向计费值）；R5X-013 裁定后 forward 行落全量 raw（与指标桶同源、不受计费门控），同推送（U=120/D=80）forward 总量 = 200。用例本体（属主节点才入账：node1=0、node2 有值）判别力不变。按本批任务纪律「除裁定点名的断言更新外不得改其他断言」，**本批未改此断言**；建议主控按裁定驱动补登记：期望 80 → 200 并同步其注释口径（N-35 同型处置）。

## 6. 测试结论

- 定向整链用例：先红（§2 签名）→ 后绿 PASS。
- `go vet ./internal/store/repo/ ./internal/http/handler/`：0。
- repo 包全量：`ok go-backend/internal/store/repo 294.791s`（含 HasRaw 回退口径的既有直构断言全绿）。
- handler 包流量定向组（`-run 'Flow|Nft|Quota|Statistics|Reset'`）：除 §5 登记的 1 条连带冲突外全绿。
- handler 包全量：本机多批并行负载下 600s 默认超时未跑完（超时点 `TestR2B7_012_SetUserInboundsPhantomRejected` 单独复跑 0.89s PASS，属负载超时非挂死）；全量中另 2 条红经基线（integrate-r5 裸树）同命令对照为**同点同红**、与本批无关：`TestInboundProbeEmptyIDsDoesNotClearDown`（live 探针需真实网络连接，本环境不可达）、`TestR2B10InboundRecoveryFailureResendsOnce`（webhook 计数，R5X-006/023 测试面领地）。本批新引入的红仅 §5 的 1 条口径连带，已登记待裁。
- PG 路径未本地实跑（本机无 PG 实例，沿袭登记；本批改动为 DB 无关的聚合口径拆分，SQLite/PG 共用同一 `applyFlowUploadDeltasTx`）。

## 7. 关闭条目

- R5X-013（P3）：**关闭**。forward 行落 raw、user/user_tunnel 行与 quota 落 scaled，三数据面同批落地；历史已落 scaled 值不回溯（BC-R5-02，发版说明注记切换点）。
- 遗留登记 2 项（不占关闭数）：§5 连带断言更新待主控裁定；§4 `AddFlow` 死代码备查。
