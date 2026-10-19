# 审查域 L 报告：节点管理功能深度审计

## 概要
- 审查范围：节点 CRUD 级联、延迟探测、调优、路由追踪、节点升级、导入解析
- 发现问题数：P0 0 个 / P1 1 个 / P2 5 个 / P3 4 个
- 实际执行的测试：`go test ./internal/kernel/ -run TestParse|TestImport` 通过；`go build ./...` 通过

## P1 严重

### L-001：`nodeBatchUpgrade` 的 goroutine 泄漏风险
- **文件**：`go-backend/internal/http/handler/upgrade.go:285-300`
- **现象**：`for i, id := range req.IDs` 循环中先 `wg.Add(1)` 再 `go func()`，goroutine 内部 `sem <- struct{}{}` 阻塞等待信号量。如果 `req.IDs` 很大（如 1000 个节点），会瞬间启动 1000 个 goroutine，大部分阻塞在 sem 等待，浪费内存。
- **复现**：POST `/api/v1/node/upgrade/batch` with `{"ids": [1,2,...,1000]}`。
- **期望**：使用 worker pool 模式，限制 goroutine 总数。
- **实际**：goroutine 数 = IDs 数，无上限。
- **影响范围**：批量升级大量节点时内存 spike。
- **修复方向**：将 `sem <- struct{}{}` 移到 `go func()` 之前（主循环中限流），或改用固定 worker 池。

## P2 一般

### L-002：`probeNodeLatency` / `traceNodeRoutes` 的 ctx 取消后仍等待
- **文件**：
  - `go-backend/internal/http/handler/node_latency.go:274-290`
  - `go-backend/internal/http/handler/node_route.go:243-265`
- **现象**：`if ctx.Err() != nil { break }` 只跳出任务分发循环，但 `wg.Wait()` 会等待已启动的 goroutine 全部完成。每个 ping 最长约 14 秒（4 次 × 2.2 秒 + 5 秒），ctx 取消后请求仍可能 hang 住。
- **期望**：ctx 取消后快速返回。
- **实际**：等待所有已启动探测完成。
- **修复方向**：`nodeLatencyPing` / `nodeTraceroute` 内部检查 ctx，或使用 `errgroup.WithContext`。

### L-003：`hopASNCache` 无上限，内存泄漏风险
- **文件**：`go-backend/internal/http/handler/node_route.go:52-53`
- **现象**：`hopASNCache = make(map[string]string)` 是全局 map，只增不减。每次路由追踪遇到新的 CMI IP 都会缓存。长期运行的面板会无限增长。
- **期望**：LRU 上限或定期清理。
- **实际**：无上限。
- **修复方向**：加 `maxHopASNCacheSize`（如 10000），超限时清空或 LRU 淘汰。

### L-004：CMI hop 的 DNS 查询串行执行
- **文件**：`go-backend/internal/http/handler/node_route.go:175-186`（`routePointFrom` 调用 `refineHopNet`）
- **现象**：`routePointFrom` 为每个 hop 调用 `refineHopNet(ip, ...)`，CMI 的 hop 会触发 `hopOriginASN` 的 DNS 查询（2 秒超时）。10 个 CMI hop 串行 = 最长 20 秒，拖慢整个追踪响应。
- **期望**：并发查询或批量查询。
- **实际**：串行。
- **修复方向**：在 `traceNodeRoutes` 的 goroutine 内对 hops 并发做 ASN 查询。

### L-005：`nodeUpgrade` 允许降级到旧版本
- **文件**：`go-backend/internal/http/handler/upgrade.go:177-230`
- **现象**：只校验版本号格式（`isValidAgentVersion`），不比较目标版本与当前版本。用户可误操作将节点从 1.0.5 降级到 1.0.0。
- **期望**：目标版本 < 当前版本时警告或拒绝。
- **实际**：无检查。
- **修复方向**：查询节点当前版本，与目标版本做语义比较，降级时要求二次确认。

### L-006：调优写入失败时删除历史备份（引用 C 报告）
- **文件**：`go-gost/x/socket/tuning.go:276-281`
- **现象**：二次调优时若内核拒绝写入（EINVAL），`delete(backup, k)` 会删除第一次调优时保存的原始值备份，导致以后无法恢复到真正的原始值。
- **报告**：C-node.md 已详细记录，此处引用。

## P3 体验/建议

### L-007：`latencyProbe` 取最大延迟而非平均值
- **文件**：`go-backend/internal/http/handler/node_latency.go:306-312`
- **现象**：`if pt.LatencyMs > p.LatencyMs { p.LatencyMs = pt.LatencyMs }` 取所有测试点中的最大值作为节点延迟。语义上通常应显示平均值或最小值，最大值会让节点看起来比实际慢。
- **备注**：可能是有意为之（保守估计），如为有意请在注释中说明。

### L-008：`nodeTune` 的 Groups 参数无白名单校验
- **文件**：`go-backend/internal/http/handler/node_tune.go:33-37`
- **现象**：`req.Groups` 直接透传给 agent（`map[string]interface{}{"groups": req.Groups}`），无白名单校验。非法组名依赖 agent 侧处理。
- **修复方向**：在 handler 层加白名单（bbr/buffer/tcp/backlog/forward/files）。

### L-009：`nodeLatencyTargetsSet` 未验证节点 ID 存在性
- **文件**：`go-backend/internal/http/handler/node_latency.go:476`
- **现象**：直接调用 `SetNodesLatencyTargets(req.IDs, value)`，IDs 包含不存在的节点时静默成功。
- **修复方向**：校验 IDs 对应的节点存在，或返回实际更新的数量。

### L-010：`classifyRoute` 多骨干网优先级
- **文件**：`go-backend/internal/http/handler/node_route.go:106-123`
- **现象**：同时检测到 9929 和 4837 时只返回"联通 9929"。实际中几乎不会同时出现，低优先级。

## 已验证通过

1. **节点级联删除完整**：`DeleteNodeCascade`（`repository_mutations.go:392`）经 `deleteNodeKernelsTx`（`repository_kernel.go:112`）覆盖 NodeInbound/NodeInboundRelay/NodeInboundCounter，无孤儿数据。
2. **导入解析健壮**：`finishProxy`（`proxy_parse.go:89`）有端口范围（1-65535）、主机、凭据三重校验；`parseVMessURI` 的 JSON 解析失败有明确错误。
3. **升级 S-02 防护到位**：版本号正则 `^v?[0-9]+\.[0-9]+\.[0-9]+$` 防路径穿越；面板不再发送 downloadUrl，由 agent 本地拼接。
4. **延迟配置校验完整**：`parseLatencyTargets` 有数量（10）、名称（20字）、重名、地址有效性、pinned 上限（3）五重校验。
5. **路由线路识别**：9 个骨干网前缀 + CMI 的 ASN 二次细分（58807→CMIN2），逻辑正确。

## 未能验证的项

- 真实节点延迟探测的耗时和准确性（需真实节点）
- 真实路由追踪的线路识别准确率（需真实线路）
- 节点升级的端到端流程（需真实节点 agent）
- 大批量（100+）节点并发探测时的面板负载
