# 审查域 C 报告：节点/延迟/调优/路由/GitHub 镜像

## 概要

- 审查范围：`go-backend/internal/http/handler/node_latency.go`（538行）、`node_tune.go`（58行）、`node_route.go`（377行）、`gh_mirror.go`（419行）、`go-gost/x/socket/tuning.go`（316行）
- 发现问题数：P0 0 个 / P1 0 个 / **P2 1 个** / P3 4 个
- 实际执行的测试：
  - 编写并运行 8 个 handler 层单元验证（`parseLatencyTargets` 边界、`targetsForNode` 回退、`renamedTargets` 改名跟随、`classifyRoute` 分类、`routeHopNet` 前缀、`routeFresh` 缓存、`parseGHMirrorPath` SSRF 防护、`ghMirrorCheckRedirect` 重定向限制）——全部通过
  - 编写并运行 3 个 tuning 层验证（`AtLeast` 语义、`applyTuning` 恢复路径、二次写入失败备份）——确认 1 个 P2 bug
  - `go vet` 通过，无新增编译问题
  - 验证测试文件已删除，未污染仓库

## P0 阻断

无。

## P1 严重

无。

## P2 一般

### C-001 二次调优写入失败时丢失原始备份

- 文件：`go-gost/x/socket/tuning.go:240`（`applyTuning` 函数内循环）
- 现象：节点已调优过一次（备份文件中有原始值），第二次应用调优时若 `tuneWrite` 失败（内核不支持该参数，返回 EINVAL），代码执行 `delete(backup, k)`，导致原始备份永久丢失。此后执行 restore 无法恢复到最初值。
- 复现步骤：
  1. 在临时 proc/sys 树中模拟 `net.ipv4.tcp_congestion_control=cubic`
  2. 调用 `applyTuning([]string{"bbr"})` 成功，备份记录 `cubic`
  3. 将 `tuneWrite` 替换为始终返回 `syscall.EINVAL` 的桩
  4. 再次调用 `applyTuning([]string{"bbr"})`
  5. 检查 `loadTuneBackup()`：`net.ipv4.tcp_congestion_control` 的备份已消失（期望仍为 `cubic`）
- 期望：写入失败时备份应保留（值未变，下次仍可恢复）。
- 实际：`delete(backup, k)` 删除了备份。
- 影响范围：仅二次调优且内核拒绝写入的边界场景。首次调优失败时删除备份是正确的（值确实没变）。
- 证据：自编写测试 `TestAuditC_TuneApplyFailKeepsBackup` 失败输出：`BUG 确认：二次写入失败后备份丢失，得到 ""，期望 cubic`
- 修复方向：区分"本次新增的备份"和"历史备份"。只在 `!saved`（本次新备份）且写入失败时删除；历史备份（`saved==true`）写入失败时保留。或在 `want` 构建阶段记录哪些 key 是本次新增的。

## P3 体验/健壮性

### C-002 `inboundProbeView` 强制覆盖延迟探测的失败状态

- 文件：`go-backend/internal/http/handler/node_latency.go:318`
- 现象：`latencyProbe` 在所有测试点都失败时会设置 `OK=false, Error="测试点都连不上"`，但 `inboundProbeView` 拿到结果后强制 `view.OK, view.Error = true, ""`，入站列表始终显示正常。
- 说明：设计意图可能是"延迟点不通 ≠ 节点不可用"，且每个点的详细错误保留在 `view.Points[].Error` 中，前端点开仍可见。因此定为 P3 而非 P2。但全失败时完全抹掉总体错误提示，运维无法从列表页察觉测试点配置问题（如测试点地址失效）。
- 修复方向：全失败时保留 `Error` 提示（如 `view.Error = node.Error`），或仅在部分成功时强制 OK。

### C-003 `applyTuning` 写入 sysctl.d 配置前未创建父目录

- 文件：`go-gost/x/socket/tuning.go:约262行`
- 现象：`os.WriteFile(tuneConfPath, ...)` 前没有 `os.MkdirAll(filepath.Dir(tuneConfPath))`。代码只在写备份文件前创建了备份目录（`os.MkdirAll(filepath.Dir(tuneBackupPath), 0o755)`）。
- 影响：标准系统上 `/etc/sysctl.d/` 必然存在，实际触发概率极低。最小化容器/自定义路径场景下会返回"写入失败"错误。
- 修复方向：在写 conf 文件前加一行 `os.MkdirAll`。

### C-004 调优配置文件注释中的品牌拼写错误

- 文件：`go-gost/x/socket/tuning.go:约260行`
- 现象：`body := "# Written by the FLYX panel (节点调优); remove via the panel to restore.\n"` 中的 "FLYX" 应为 "FLVX"。
- 影响：仅注释文案，不影响功能。

### C-005 GitHub 镜像缓存在 DB_PATH 未设置时使用系统临时目录

- 文件：`go-backend/internal/http/handler/gh_mirror.go:286-293`（`ghMirrorCacheDir`）
- 现象：`DB_PATH` 环境变量为空时，缓存目录为 `os.TempDir()/flvx-github-cache`。容器重启或系统清理 /tmp 后，缓存丢失，"GitHub 不可达时返回旧副本"的降级能力失效。
- 说明：生产部署通常设置 DB_PATH，实际影响有限。建议文档注明 DB_PATH 为必填，或在启动时校验。

## 验证通过的项（无问题）

以下经实际运行测试确认逻辑正确：

1. **C1 延迟配置解析**（`parseLatencyTargets`）：空值/超10个/重名/超3个pinned/非法地址均正确报错；host 规范化正确。
2. **C1 节点自选回退**（`targetsForNode`）：自选点全被删除时回退默认；损坏 JSON 回退默认。
3. **C1 改名跟随**（`renamedTargets`/`syncNodeTargetChoices`）：同地址改名时节点选择跟随；地址变更时不跟随；删除的点被 drop。
4. **C1 离线跳过**（`probeNodeLatency`）：`n.Status != 1` 时跳过；无测试点时跳过。
5. **C1 并发安全**：`probeNodeLatency` 每个 goroutine 写独立 slice 槽位，无数据竞争；信号量+WaitGroup 无死锁。
6. **C3 线路分类**（`classifyRoute`）：CN2/163/9929/4837/CMI/CMIN2 优先级正确；CN2+163 判为 CN2 GT；空 hops 判为"未识别"。
7. **C3 前缀匹配**（`routeHopNet`）：9 个骨干前缀正确；`59.431.x.x` 不误匹配 `59.43.`。
8. **C3 缓存刷新**（`routeFresh`）：24h 成功刷新、6h 失败重试、agent 升级立即重查、测试点改名/地址变更重查，均符合设计。
9. **C3 降级处理**（`routePointFrom`）：旧 agent（"未知命令"）、无 NET_RAW 权限、DNS 失败均有明确中文错误。
10. **C4 SSRF 防护**（`parseGHMirrorPath`）：非白名单仓库拒绝；`..` 路径穿越拒绝；非 github host 拒绝；api.github.com 的 query 非法时拒绝。
11. **C4 重定向限制**（`ghMirrorCheckRedirect`）：仅允许 github.com 和 *.githubusercontent.com；`github.com.evil.com` 后缀欺骗被拒绝（`strings.HasSuffix` 精确匹配）。
12. **C4 300MB 限制**：下载前检查 `ContentLength`，下载中使用 `io.LimitReader(..., 300MB+1)`，下载后校验，时机正确。
13. **C2 AtLeast 语义**（`tuning.go satisfied`）：多值参数取最后一个数比较；已高于推荐值时跳过写入。
14. **C2 恢复路径**（`applyTuning(nil)`）：原值写回、备份文件删除、conf 文件删除，均正确。
15. **C2 旧 agent 错误翻译**（`node_tune.go`）："未知命令类型"转为"节点 agent 版本过旧"，正确。

## 未能验证的项

- **真实节点调优**：需 Linux root 节点实测 BBR 应用、`sysctl -a` 确认、`tuning-backup.json` 并发写。沙箱无节点权限，标为未验证。
- **真实路由追踪**：需节点 raw socket 权限实测 ICMP traceroute 和 Team Cymru DNS 查询。标为未验证。
- **GitHub 镜像真实下载**：沙箱 DNS 被拦截（*.github.com 解析到 198.18.x.x），无法实测下载/缓存/旧副本降级。标为未验证。
- **延迟探测真实耗时**：`probeNodeLatency` 的 16 并发、每点 4 次 ping 在百节点规模下的总耗时，需生产环境实测。代码层面无死锁，标为未验证。
- **前端页面实际验证**：无 live browser，未打开节点页/配置页确认延迟显示、调优弹窗、路由追踪 UI。需人工在浏览器确认。
