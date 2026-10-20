# P1 复审报告：stash 损伤区 + 手工合并复审

**时间**：2026-10-20 ~11:40 CST
**基线**：main @ 152ac29，工作区未提交改动（4 路修复 + 父代理手工合并）
**性质**：只读复审 + 构建/vet/测试验证，未改任何代码
**结论**：**6 项全部通过，无 P0/P1/P2 问题；2 个 P3 观察项。本区可安全合入。**

---

## 1. kernel_relay.go — K-002 验证点（通过）

- **token 来源**：`openSubscription` 内 `token := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/open_api/sub/"), "/")`（:512），同一作用域内 :561 调用 `verifySubURLSigForToken(token, userID, sig)`。token 在验证前已做 `len==32` 检查 + `GetSubscriptionByToken` + 常量时间比对（:513-518）。✓
- **v2/v1 双验完整**：user_inbound.go 有 `subURLSigForToken`（:45，输入 `flvx-subsig-v2:{token}:{userID}`，hex 前 32）、`verifySubURLSigForToken`（:67，先验 v2 再回退 v1）、v1 弃用日志经 `sync.Once`（:22-28）。v1 也是 32 hex 字符，`len(sig)!=32` 的前置检查不影响回退。✓
- **其他逻辑未破坏**：O3-002 IPv6 回退（:160）、M-002/M-003 日志（:420/:438/:446）都在位；diff 显示本文件改动仅 +10/-2 行（注释 + 验证点）。✓

## 2. passkey.go — K-005 限流 + O11-P3-1 吊销（通过）

- **插入位置**：begin（:288）、finish（:322）均在方法校验后、`passkeyConfig()`/挑战生成（`BeginDiscoverableLogin`）或挑战消费前。挑战池 DoS 堵口正确。✓
- **语义匹配**：`checkLoginRateLimit(w,r) bool`（login_rate_limit.go:352），返回 false 时内部已写 429 JSON（:367-368）；passkey 调用模式 `if !check { return }` 与密码登录（handler.go:399）完全一致。✓
- **O11-P3-1**：`RevokeUserSessions` 在 `DeletePasskey` 成功（deleted==true）后调用（:459-463），失败仅 log、仍返回删除成功——降级可接受（主目标凭据删除已达成，会话吊销为附加动作）。✓

## 3. node_latency.go — 四处合并（通过）

- **常量**：`maxProbeAllNodes = 50`（:30-31）在 const 块内，与 `maxPinnedLatencyTargets` 等并列。✓
- **L-002**：`done := make(chan struct{}); go func(){ wg.Wait(); close(done) }()` + `select { case <-done: case <-ctx.Done(): return nil, ctx.Err() }`（:284-292）。goroutine 只写预分配 slice slot，取消路径不再读 `points`，无数据竞争。调用方：
  - 后台任务（:367）：`err != nil && ctx.Err() == nil` —— 取消时条件为假，错误被吞，正确；
  - `nodeLatencyTargetsSet`（:530，显式 IDs 立刻探测）：仅 log 错误，无影响；
  - `nodeLatencyProbe`（:584）：取消时返回 500 context canceled——客户端已取消，无实际危害。✓
- **J-004/O1-005**：200 上限 + `ListMonitorNodes` 存在性检查（插入于 targetsSet decode 之后）。误伤风险低：同一 DB，刚创建节点即刻可见；admin-only 接口。✓
- **O1-004 truncated**：空 IDs 收敛为前 50 在线节点（:562-582），响应 `response.R{Code:0, Msg:提示, TS:..., Data:out}`（:590-598）。`response.R` 结构为 {code,msg,ts,data}，前端 `probeNodeLatency(ids=[])` 默认空数组触发此路径，data 形状不变，仅 msg 携带提示——前端兼容。✓
- **O1-006 注释**（工作树保留）未被破坏。✓

## 4. node_route.go — L-003 缓存上限（通过）

- `maxHopASNCacheSize = 10000` 在 var 块（:58）；`hopOriginASN` 写路径持 `hopASNMu` 做「超限清空 + 写入」原子操作（:90-97）；读路径同样持锁（:70-72）。✓
- `warmHopASNCache`（O1-008，并发信号量 4）经 `hopOriginASN` 访问缓存，无直接 map 操作——与 L-003 无冲突。✓
- 清空时并发 goroutine 可能在新 map 重查 DNS——无损坏，仅少量重复查询，可接受。✓

## 5. gh_mirror.go — J-002 IP 限流（通过，1 个 P3）

- 固定窗口 60 次/分钟/IP，全部变更在 `ghMirrorRateMu` 下；map 上限 10000 host，超限对新 IP fail-closed。✓
- `net.SplitHostPort(r.RemoteAddr)` 处理 IPv6（含括号）正确；TCP 下 RemoteAddr 恒为 ip:port，err 分支基本不可达。✓
- **P3 观察**：同一 NAT 出口后多节点集中升级可能撞 60/min（每节点升级约 2-5 请求；>10 节点同分钟升级有风险）；429 无 `Retry-After` 头。当前阈值对常规运营够用，大规模机群升级前建议评估或调高。

## 6. mutations.go — tunnelBatchDelete O3-003（通过，1 个 P3）

- 收集逻辑与单删 O3-001 一致（`ListForwardsByTunnel` × `ListNodeInboundRelays`，`rel.ForwardID == fwd.ID` 匹配，map 去重），收集在所有 `deleteTunnelByID` 之前（级联删之前 relay 行仍存在）。✓
- 删除失败的隧道其入站也被收集并刷新——刷新幂等，无害。✓
- **P3 观察**：`ListNodeInboundRelays()` 全量查询在批次循环内（每 tunnel 一次），与 O3-001 单删同开销；典型批次为个位数，可接受；超大批次可提为循环外一次。

---

## 验证结果

| 验证 | 结果 |
|---|---|
| `go build ./...` | ✅ 通过（BUILD_EXIT=0） |
| `go vet ./internal/http/handler/` | ✅ 无输出 |
| `go test ./internal/http/handler/ -run "TestPasskey\|TestSub\|TestLatency\|TestRoute\|TestMirror\|TestTunnel\|TestSig\|TestNotify"` | ✅ ok 4.617s |
| `go test ./internal/kernel/ -run "TestLoon\|TestQX"`（A 的 H-002） | ✅ ok |
| 修复标记核查（12 个标记 × 文件） | ✅ 全部在位：K-002（kernel_relay.go+user_inbound.go）、K-005/O11-P3-1（passkey.go）、L-002/J-004/O1-004/J-003/O1-005/L-008（node_latency.go）、L-003/O1-008（node_route.go）、J-002（gh_mirror.go）、O3-003/O3-001（mutations.go/kernel_relay.go） |

---

## 总结

**本区（stash 损伤区 + 手工合并 + 父代理补修 J-002/O3-003）可安全合入。** 6 项复审全部通过，无 P0/P1/P2 问题。2 个 P3 为纯观察项（gh_mirror 429 无 Retry-After、tunnelBatchDelete 全量查询可提循环外），不影响本次发版，可留待后续优化。
