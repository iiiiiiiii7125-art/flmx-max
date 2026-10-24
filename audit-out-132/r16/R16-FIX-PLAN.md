# R16 FIX-PLAN（修复计划，不发版）

基线：main @ b54b44f。本计划覆盖本轮新立 3 项全部（`R16-X-ledger.md` §2），三方对账差集 ∅。修复由主控按自主修复授权直接执行，修完再审下一轮（R17），不发版。无 BC 项（三项均为健壮性/演示面/死代码修正）。

## F-01：userList 分页乘法溢出（R16-X-02，产品 P3）

- 对象：`go-backend/internal/http/handler/handler.go:740`，`start := (req.Current - 1) * req.Size` 无上溢防护；溢出后 `users[start:end]` 负下标 panic（Recover 兜底回 code -2）。
- 改动：解码后钳制 Current/Size 上限，或乘前判溢出（二选一，主控定；仅此一处，同型搜索确认无第二处分页乘法）。
- 测试：先红后绿——溢出形 Current/Size（L3 探针 `R16-L3-a2-probe.log` 两溢出形）修复前 panic、修复后 code 0；两对照仍 code 0；正常分页行为不变。
- 核对：全仓同型搜索（`(Current-1)*Size` 及同类分页乘法）确认仅此一处。

## F-02：入站页 sheetProbe 空指针（R16-X-01，产品·演示面 P3）

- 对象：`vite-frontend/src/pages/inbound.tsx:1020-1024`，`sheetProbe` 与修复前旧式逐字同构；probe=null 时 `sheetProbe !== undefined && !sheetProbe.ok` 读 null.ok，详情页崩溃。
- 改动：与 renderInbound 修复同形——null 与 undefined 同义为“暂无探测结果”（`sheetItem?.probe && !sheetItem.probe.skipped ? sheetItem.probe : undefined`）。
- 测试：先红后绿——点击探针 Run D（L1 `R16-L1-sheet-click.log`：点击外部入站卡片 `机场 · 台湾 01`，夹具 probe 为 null）修复前页面文本归 0 + TypeError，修复后正常渲染；走查须覆盖 sheet 打开一步（README §30 盲区：不点击的控件不在覆盖内——本次须补点击腿或明文登记仍盲区，不得默认已覆盖）。
- 核对：修复后全仓同型再搜索，确认 renderInbound 与 sheetProbe 两处均已守卫，无第三处。

## F-03：inboundProbeView 死代码（R16-X-03，产品 P3）

- 对象：`go-backend/internal/http/handler/node_latency.go:460-484`，生产调用方为 0，仅 `node_latency_test.go` 引用；注释“is what the inbound list shows”失实（实际列表走 `kernel.go:716-718`）。
- 改动：删除或接线并订正注释（二选一，主控定；删除时同步调整测试引用，接线时验证实际列表路径确实走它）。
- 测试：修后 go build/vet/test 全绿；如删除，相关测试同步更新且断言不动其余部分。

## 执行顺序

1. F-01（生产 API 健壮性）优先；2. F-02（演示面崩溃）；3. F-03（死代码）。
2. 每修复带先红后绿回归测试；修后 go build/vet/test + 前端 tsc 全绿；修完按本轮同一口径重审（R17），不发版。
3. R17 关闭验证须同时复核 R15-FIX-01 两处位点（renderInbound + sheetProbe）与 R16-X-02/X-03；VMess/SS/Trojan 计时腿重跑另由发版门或专项验证安排，不属本计划执行项。

R16-FIX-PLAN END
