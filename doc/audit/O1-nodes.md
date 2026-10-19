# O1 节点管理功能全量审计报告

**审计员**：节点管理专项
**日期**：2026-10-20
**参考手册**：`doc/guide/nodes.md`
**范围**：节点 CRUD、分组、状态、延迟、调优、回程路由、升级、导入节点

## 审计方法

- 对照手册逐项核对实现
- 边界条件：空值、超长、非法输入、并发
- 运行相关单元测试建立基线

## 新发现问题

### P1 严重

**O1-001 — 调优失败删除原始值备份，无法恢复**（`go-gost/x/socket/tuning.go:276`）
- **复现**：节点上某 sysctl 参数内核不支持（如 `net.ipv4.tcp_fastopen` 在旧内核返回 EINVAL）→ `tuneWrite` 失败 → 代码执行 `delete(backup, k)`
- **后果**：`backup` 存的是**调优前原值**。删除后，"恢复默认" 时该参数无原值可写回，永久丢失
- **代码**：
  ```go
  if err := tuneWrite(k, value); err != nil {
      if os.IsNotExist(err) || errors.Is(err, syscall.EINVAL) {
          warnings = append(warnings, ...)
      }
      if tuned {
          failed[k] = true
          delete(backup, k) // ← 错误：删除的是原值备份
      }
      continue
  }
  ```
- **注释声称** "unchanged, nothing to restore" 是错误的——backup 里是原值，不是新值
- **注**：此问题在 C 域审计中已报告（C-001/L-006），至今未修复

### P2 一般

**O1-002 — `nodeCreate` 并发创建同名同 IP 节点时可能配置到错误的节点**（`mutations.go:339-407, 495-510`）
- **复现**：两个管理员同时创建 name="node1"、serverIP="1.2.3.4" 的节点
- **流程**：`CreateNode()` → `findCreatedNodeID(name, serverIP)` 从后往前找最后一个匹配
- **后果**：可能返回另一个请求的节点 ID，`persistNodeSSHConfig(nodeID, ...)` 把 SSH 配置写到错误的节点
- **修复建议**：`CreateNode` 应返回插入的 ID，而非事后按 name+IP 搜索

**O1-003 — `hopASNCache` 无界增长，内存泄漏**（`node_route.go:55-58`）
```go
var (
    hopASNMu    sync.Mutex
    hopASNCache = make(map[string]string)  // ← 无上限、无过期
)
```
- 每次回程路由追踪遇到新的 CMI hop IP 段就加一条，面板长期运行持续增长
- **注**：L 域审计已报告（L-003），未修复

**O1-004 — `nodeLatencyProbe` 空 IDs 探测全部在线节点，无二次确认**（`node_latency.go:512-538`）
- 前端"全部检测"按钮 → `ids: []` → 探测所有 `Status=1` 的节点 × 最多 10 个测试点
- 50 个节点 = 500 次 TCP 出站连接，无确认、无限流
- **注**：J 域审计已报告（J-003），手册未说明此行为

### P3 备注

**O1-005 — `nodeLatencyTargetsSet` 不验证节点 ID 存在性**（`node_latency.go:436-510`）
- `SetNodesLatencyTargets` 的 WHERE 子句静默忽略不存在的 ID，API 返回成功
- 用户勾选已删除的节点时无反馈
- **注**：L 域审计已报告（L-009）

**O1-006 — `latencyProbe` 取最大延迟而非平均值**（`node_latency.go:300-312`）
```go
// latencyProbe summarizes a node's points: OK when any point answered,
// with the worst answering point's latency.
```
- 节点卡片显示的是 10 个测试点中最差的那个延迟，而非平均值
- 语义可疑但已在注释中明确，不算 bug，记录备查
- **注**：L 域审计已报告（L-007）

**O1-007 — `nodeUpgrade` 允许降级到旧版本**（`upgrade.go:172-216`）
- 未比较当前版本与目标版本，管理员可误操作降级 agent
- **注**：L 域审计已报告（L-005）

**O1-008 — 路由追踪的 CMI ASN 查询串行执行**（`node_route.go` 相关）
- 10 个 hop 最长 20 秒（每个 DNS 查询 2 秒超时）
- **注**：L 域审计已报告（L-004）

## 手册一致性核对（全部通过 ✅）

| 手册声明 | 代码位置 | 结论 |
|---------|---------|------|
| 测试点最多 10 个 | `node_latency.go:24` `maxLatencyTargets = 10` | ✅ 一致 |
| 卡片最多固定 3 个 | `node_latency.go:25` `maxPinnedLatencyTargets = 3` | ✅ 一致 |
| 测试点改名自动跟随 | `syncNodeTargetChoices` `node_latency.go:393` | ✅ 有实现 |
| 删除测试点回退全局默认 | `targetsForNode` 空时用 pool | ✅ 有实现 |
| 调优写入 `/etc/sysctl.d/99-flvx-tuning.conf` | `tuning.go` | ✅ 一致 |
| 恢复默认写回原值并删配置文件 | `tuning.go` restore 逻辑 | ✅ 有实现（但 O1-001 导致原值可能丢失） |
| 不支持的参数 warning 跳过 | `tuning.go:268-270` | ✅ 有实现 |
| 旧 agent 提示升级 | `node_tune.go:51-53` "节点 agent 版本过旧" | ✅ 有实现 |
| 路由 24h 刷新 / 6h 重试 | `node_route.go:17,19` | ✅ 一致 |
| 无 raw socket 权限优雅报错 | `traceroute.go` 错误处理 | ✅ 有实现 |
| 线路识别：CN2/163/4837/9929/CMI | `node_route.go` 线路映射表 | ✅ 有实现 |
| 仅 agent 模式显示调优入口 | 前端条件渲染 | ✅（P1 已验证挂载） |
| nftables 模式无调优入口 | 前端条件渲染 | ✅（P1 已验证） |

## 已验证通过

1. **节点级联删除完整**：`DeleteNodeCascade` 清理 16 类关联数据（ForwardPort、ChainTunnel、内核、SSH 配置、nft 绑定/计数器/pending、监控、指标、peer share、订阅计数器），事务保护
2. **导入节点三重校验**：端口/主机/凭据校验 + 去重（`externalKey`）+ 端口分配锁（`AcquirePortAllocLock`）
3. **导入数量上限**：`maxImportNodes` 限制
4. **延迟探测并发控制**：信号量在 goroutine 启动前获取（无 L-001 类问题）
5. **路由追踪并发控制**：同上，正确
6. **调优 groups 白名单**：agent 侧返回"未知调优项"错误，非静默忽略
7. **升级版本号校验**：`isValidAgentVersion` 正则 + S-02 路径穿越防护
8. **升级不发送 downloadUrl**：S-02 修复后 agent 侧本地拼 URL
9. **节点安装密钥安全**：`randomToken` 返回 hex 编码，无 shell 注入风险
10. **相关单元测试通过**：`go test -run "TestNode|TestLatency|TestTune|TestRoute|TestUpgrade"` 全过

## 未能验证（需人工/真机）

- 节点 agent 实际安装流程（需落地机）
- 内核安装/启停/日志（需落地机）
- 调优参数实际生效（需 root 落地机）
- 回程路由 traceroute（需 raw socket 权限）
- 大批量节点（50+）的延迟探测性能
- 前端节点卡片/列表的延迟显示（无浏览器环境）

## 修复优先级建议

1. **O1-001（P1）**：调优备份删除 bug — 数据丢失风险，立即修复
2. **O1-002（P2）**：nodeCreate TOCTOU — 低频但后果严重（配错节点）
3. **O1-003（P2）**：hopASNCache 加 LRU 上限或 TTL
4. O1-004~O1-008：已有报告，按原计划排期
