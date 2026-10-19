# O12 联邦与监控功能审计报告

**审计时间**：2026-10-20
**审计员**：联邦与监控审计员
**工作目录**：~/workspace/flvx-max

## 审计结论

**发现问题：P0 0 / P1 1 / P2 2 / P3 3**

联邦的鉴权模型、命令白名单、资源命名空间隔离经过本轮代码级验证，整体是扎实的。监控的 SSRF 双层防护（创建时校验 + 拨号时重校验防 TOCTOU）到位。发现的问题集中在可观测性、内存泄漏和信息泄露等边缘情况。

---

## P1 严重

### O12-001：`authPeer` 向未鉴权调用方泄露数据库错误详情
- **文件**：`go-backend/internal/http/handler/federation.go:816-819`
- **代码**：
  ```go
  share, err := h.repo.GetPeerShareByToken(token)
  if err != nil {
      response.WriteJSON(w, response.Err(-2, err.Error()))  // 直接返回 err.Error()
      return
  }
  ```
- **问题**：`GetPeerShareByToken` 的数据库错误（如连接失败、SQL 语法错误）被原样返回给未鉴权的调用方。错误信息可能包含数据库路径、表名、SQL 片段等内部细节。
- **复现**：无需复现，代码路径确认。任意未鉴权请求携带任意 token 访问 `/api/v1/federation/connect`，若此时数据库异常，响应中即包含内部错误详情。
- **影响**：信息泄露（低危，但属于纵深防御缺失）。正常情况下 DB 不会出错，但攻击者可结合其他手段（如资源耗尽）触发。
- **同类位置**：`federation.go:908` (`federationConnect`)、`federation.go:942` (`federationTunnelCreate`) 有相同模式。
- **建议修复**：记录日志，返回通用 500：
  ```go
  if err != nil {
      log.Printf("federation authPeer: GetPeerShareByToken failed: %v", err)
      response.WriteJSON(w, response.Err(-2, "internal error"))
      return
  }
  ```

---

## P2 一般

### O12-002：删除节点后 `notifier.offlineAlerted` 残留，内存泄漏
- **文件**：`go-backend/internal/http/handler/notify.go:69-70, 323-360`
- **问题**：`notifier` 的 `offlineTimers` 和 `offlineAlerted` 两个 map 以 nodeID 为键，但节点删除时没有任何清理逻辑。
- **复现步骤**：
  1. 节点 A 掉线 → 90 秒后触发告警 → `offlineAlerted[A] = true`
  2. 删除节点 A（管理后台删除节点）
  3. `offlineAlerted[A]` 永久残留，`offlineTimers[A]` 的 timer 虽已触发并自删，但 map 条目在 `notifyNodeOnline` 未被调用时不会清理
- **影响**：长期运行的面板删除大量节点后，map 持续增长。单条目很小，实际影响有限，但属于资源泄漏。
- **建议修复**：在节点删除逻辑中调用 `h.notifier.clearNode(nodeID)` 清理两个 map。

### O12-003：`syncRemoteNodeStatuses` 无并发上限
- **文件**：`go-backend/internal/http/handler/federation.go:1902-1968`
- **问题**：同步远端节点状态时，为每个远端节点启动一个 goroutine，无 semaphore 限制。
  ```go
  for i, entry := range remotes {
      wg.Add(1)
      go func(idx int, e remoteEntry) { ... }(i, entry)
  }
  wg.Wait()
  ```
- **影响**：远端节点数通常很少（个位数），实际风险低。但如果用户添加了上百个远端节点，一次列表刷新即产生上百个并发 HTTP 请求（每个 5 秒超时），可能导致连接耗尽或目标面板被打挂。
- **建议修复**：加 semaphore 限制并发数为 10，或复用 L-001 的修复模式。

---

## P3 备注

### O12-004：`isFederationCleanupRequest` 的 body 读取上限 1MB
- **文件**：`go-backend/internal/http/handler/federation.go:876-898`
- **说明**：为判断是否为清理请求（delete 命令），需要预读 body。用 `io.LimitReader(original, 1<<20)` 限制 1MB。`CommandType` 字段位于 JSON 开头，1MB 足够。但如果攻击者发送超大 body，前 1MB 不含完整 `CommandType` 时，`json.Decode` 会失败 → 返回 false → 走正常鉴权路径（更严格），属于 fail-closed，无安全问题。仅记录。

### O12-005：联邦客户端 `remoteURL` 未做 SSRF 校验
- **文件**：`go-backend/internal/http/client/federation.go:109-130`
- **说明**：`Connect(url, ...)` 直接拼接 `url + "/api/v1/federation/connect"` 发起 HTTP 请求，未校验 URL 是否为内网地址。
- **风险评估**：`remoteURL` 来自数据库 `node.RemoteURL` 字段，由**管理员**在添加远端节点时填写。管理员是可信角色，且填写内网地址是合法用例（如内网联邦）。因此不视为漏洞，仅记录。如需纵深防御，可在节点创建/更新时对 `RemoteURL` 做格式校验（必须 http/https）。

### O12-006：监控 `WorkerLimit` 上限 50 但默认 20
- **文件**：`go-backend/internal/monitoring/limits.go:66-68`
- **说明**：`normalizeServiceMonitorLimits` 将 `WorkerLimit` 上限钳制为 50。默认值 20，管理员可在配置中调高但不超过 50。属于合理的资源保护，无问题。仅记录。

---

## 已验证通过（代码级确认）

### 联邦鉴权
1. **Peer Token**：`randomToken(32)` 使用 `crypto/rand`，256 位熵，不可猜测（`mutations.go:5746`）
2. **Token 比对**：`GetPeerShareByToken` 走数据库精确匹配，无时序问题（DB 查询本身恒定）
3. **IP 白名单**：`federationClientIP` 默认只信任 `RemoteAddr`；XFF 仅在显式配置 `FLVX_FEDERATION_TRUSTED_PROXIES` 且 RemoteAddr 落在其中时才解析，且 fail-closed 回退到 RemoteAddr（`federation.go:1767-1791`）。旧 FGH-007 缺陷已修复，本轮确认无回归。
4. **Domain 校验**：`X-Panel-Domain` 头精确匹配（非子串），未提供时 403（`federation.go:839-856`）
5. **过期/停用/超流**：三态检查在 `authPeer` 和 `federationRuntimeCommand` 内双重执行，且 `federationRuntimeCommand` 在拿锁后重新读取 share（防 TOCTOU）（`federation.go:1373-1391`）

### 联邦命令执行
6. **命令白名单**：`isFederationRuntimeCommandAllowed` 仅允许 15 个 GOST 资源命令（`federation.go:1604-1612`），不在白名单的一律拒绝
7. **Shell Hook 防护**：`validatePeerResourceReferences` 明确拒绝 `preup/predown/postup/postdown`（S-03 修复，本轮确认仍在）（`federation_resources.go:663-678`）
8. **资源命名空间隔离**：`peerShareResourceName` 生成 `peer-share-{shareID}-{kind}-{base64(name)}`，跨 share 的同名资源不会冲突；`preparePeerResourceCommand` 中 `GetPeerShareResource(share.ID, kind, name)` 按 shareID 查询，peer 无法操作其他 share 的资源（`federation_resources.go:179`）
9. **端口范围**：`validateFederationCommandPorts` 确保 service 的端口在 share 允许范围内；`federationTunnelCreate` 三重检查端口占用（`federation.go:1620-1646, 960-987`）
10. **清理权限**：已撤销/过期的 share 仍允许 delete 类命令（`isFederationCleanupRequest`），确保资源可回收；非 delete 命令被拒绝（`federation.go:1390-1394`）

### 监控 SSRF
11. **双层校验**：创建/修改时 `ValidateMonitorTCPTarget`（`monitoring.go:453, 612`）+ 拨号时 `resolveValidatedMonitorIPs` 重解析重校验（`checker.go:404`），消除 DNS TOCTOU（R5R2-001 修复，本轮确认仍在）
12. **拨号直连 IP**：校验后直接拨 `ip.String()` 字面量，不再二次 DNS 解析（`checker.go:409-414`）
13. **内网段拒绝**：10/8、172.16/12、192.168/16、169.254/16、100.64/10、fc00::/7、fe80::/10 全拒绝（`monitor_ssrf.go:19-28`）

### 监控告警
14. **掉线告警**：90 秒 grace 期（`notify.go:44`），timer 触发时二次确认 `IsNodeOnline`（防竞态），同一问题只告警一次（`offlineAlerted`），恢复后发送恢复通知（`notify.go:323-360`）
15. **入站探测**：连续 2 次失败才告警（`notifyInboundDownStreak = 2`），`track()` 中 disabled/skipped 的入站清理状态（`kernel_probe.go:62-91`）
16. **监控权限**：`ensureMonitoringAccess` 区分 admin（直接放行）和普通用户（需 `HasMonitorPermission`）（`monitoring.go:872-889`）

### 测试
17. `go test ./internal/http/handler/ -run "TestFederation|TestMonitor|TestNotify"` 全部通过

---

## 未能验证
- 联邦跨机器实际联调（需双面板实例）：token 握手、端口预留、命令下发、状态同步的端到端流程
- 节点掉线 90 秒的真实告警触发（需实际断开节点网络）
- 大量远端节点（100+）时的 `syncRemoteNodeStatuses` 并发压力

---

## 建议修复顺序
1. **O12-001**（P1）：`authPeer` 错误信息脱敏 — 简单，影响面小
2. **O12-002**（P2）：节点删除时清理 notifier 状态 — 需找到节点删除入口
3. **O12-003**（P2）：`syncRemoteNodeStatuses` 加并发限制 — 简单

**只审计未修复**：源代码未修改。
