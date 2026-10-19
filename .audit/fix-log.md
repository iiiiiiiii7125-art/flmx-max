# fix-log.md — 修复日志

每修复独立 commit：`AUDIT-R<N>: <ID> <一句话>`，必须满足 §6.2 DoD。

## Round 1

### AUDIT-R1: TST-001（commit d046507）
- 根因：R5-B7（8163487）对无 `tunnel_id` 列的三个模型（NodeInboundRelay 仅 forward_id、NftCounterState 仅 node_id/forward_id、GroupPermissionGrant 仅 tunnel_group_id/user_tunnel_id）用 `tunnel_id = ?` 删除 → 隧道删除恒回滚。另 B7 块的 `forward_id IN (子查询)` 在 Forward 行已删后求值为空，清理为 no-op。
- 变体扫描：同函数其余 `tunnel_id` 删除逐模型核对列存在性（TunnelGroupTunnel/SpeedLimit/TunnelMetric ✓）；同 commit 的 DeleteNodeCascade 8 个 `node_id` 删除全 ✓；DeleteUserCascade 的 MonitorPermission `user_id` ✓。
- pre-fix 红：R0 基线 4 个测试失败（federation_share_test.go:596 等）；post-fix 绿：5 个相关测试全过。
- 双后端：SQLite 实测；PG 无环境，标注未验证（语句为标准 SQL，无方言特性）。
- 回归测试：新增 `TestAuditR1_TunnelCascadeCleansOrphansByCorrectColumns`（覆盖三条 latent 路径 + 对照无过度删除），已入永久测试集。
- 红线自查：端口复用/冲突域未动；设计特性未变。

### AUDIT-R1: TST-002（commit 09589b4）
- 根因：R5-B7 误加 `tx.Where("kernel_id = ?", id).Delete(&model.UserInbound{})`；UserInbound 无 kernel_id 列 → 内核删除恒回滚。NodeInbound 的 kernel_id 已由上一行清理，无悬空可清，故直接删除该行（非注释掉）。
- pre-fix 红：TestKernelInboundLifecycleContract（:261）；post-fix 绿。
- 变体扫描：同 commit 其余新增见 TST-001。

### AUDIT-R1: TST-003（commit 47d3ff1）
- 根因：R5-B4C 的 `ensureUsernameAvailable` 用 `.Table("\"user\"")`，GORM quoter 二次转义 → `FROM """user"""` → `no such table: "user"`（报错带引号为 smoking gun）；备份 Import 含 users 必失败。
- 变体扫描：全仓仅此一处 `.Table()` 双引号；repository.go:510 的 `.Where("\"user\" = ?", …)` 是 raw SQL，`"user"` 即合法标识符，不受影响（seedData 测试全过）。
- pre-fix 红：TestImportUsersDoesNotStoreLegacyMD5Passwords、TestTrafficLimitMiBSurvivesBackupRestore；post-fix 绿。
- 备注：该函数注释声称被 CreateUser 等复用，实际全仓仅 importUsers 一处调用。

### AUDIT-R1: TST-004（commit e67d115 + 补提交）
- 根因：批次 4 迁移 `r5b4_v1_port_user_unique` 建 `(node_id,port)` 唯一索引（符合 §1.1，设计正确），7 个旧测试 fixture 在同一 `(node_id,port)` 插两行而变非法。产品代码无问题，纯测试债务。
- 变体扫描：全仓 `seedForwardForNftables` 调用点逐个核对（共 25 处，全部加 port 参数）；`insertChain`/`seedRelayTargetTunnel` 同理；issue313 发现测试自身重复插入完全相同的 forward_port 行（脏数据一行即可），删除重复。
- pre-fix 红：R0 基线 7 个测试失败；post-fix 绿：7 个全过（-run 隔离复现确认非测试污染）。
- 回归测试：fixture 修正本身即回归覆盖；断言未变更。

### AUDIT-R1: SCR-001（commit e94b000）
- 根因：`publish-skill.yml` 触发器 `v*` 与仓库纯数字 tag 惯例互斥（docker-build.yml 用 `[0-9]*`），workflow 永远不运行；且 `${GITHUB_REF#refs/tags/v}` 对纯数字 tag 剥不掉前缀得完整 ref；算出的 version 从不写回 package.json。
- 变体扫描：docker-build.yml 的分支推送/update-release-gost 不可达、:313-314 sed 死代码属同类"死亡流水线"问题，记入 SCR-001 变体但本轮未改（改 CI workflow 需记理由，风险收益比低，留后轮评估）。
- pre/post：静态验证；YAML 解析通过。动态验证=下次纯数字 tag 推送（CI 见证）。

### AUDIT-R1: SCR-002（commit e3055bb）
- 根因：`vite-frontend/nginx.conf` 无 `client_max_body_size`，>1MB 备份 JSON 经 `/api/v1/backup/import` 被 nginx 默认 1m 以 413 拦截，面板 UI 静默失败。
- 变体扫描：其他上传接口（头像/壁纸等）经同一 nginx，均受益；后端 Go 无 body 上限（流式解码），无需改。
- 修复：`client_max_body_size 50m;`（面板为自托管服务）。

### AUDIT-R1: SCR-003（commit 6130e9f）
- 根因：菜单 5 `setup_domain_proxy` 是独立流程，不经过 `get_config_params`，`FRONTEND_PORT` 恒未设置 → Caddy 恒反代 6366；用户装非默认端口后域名访问 502。
- 变体扫描：同函数其他端口引用无；更新流程的 `resolve_panel_deployment` 已证明 .env 可读。
- 修复：从 `PANEL_DEPLOY_DIR/.env`、`./.env`、`/opt/flvx-panel/.env` 依次读取并校验端口，找不到回退 6366。桩测试验证：8080 读到/缺失回退/非法回退。

### AUDIT-R1: SCR-004（commit 0968192）
- 根因：postinstall/link 的 `if(fs.existsSync(target)||fs.lstatSync(target).isSymbolicLink())` ——全新安装时 target 不存在，`lstatSync` 抛 ENOENT，symlink 从未自动建。
- 修复：无条件 `try{fs.unlinkSync(target)}catch(_){}`（覆盖文件/正常 symlink/断裂 symlink/不存在）。三 case 已验证；package.json 仍为合法 JSON。

### AUDIT-R1: SCR-005（commit 3dbaf3a）
- 根因：`.env.production` 的 `VITE_GITHUB_REPO` 残留上游 kure29/flvx。docker-build.yml:181 发版时重写，官方镜像不受影响；本地/第三方构建指向错误仓库。
- 修复：默认值改 fork 地址。`VITE_APP_VERSION=beta` 发版时同样重写，保留。

### AUDIT-R1: SCR-007（commit 49ae76a）
- 根因：`get_architecture` 未知架构静默回退 amd64；releases 只有 amd64/arm64 资产，回退致下载的二进制无法执行、失败定位极晚。
- 修复：未知架构报错退出；`build_download_url` 分开声明与赋值（SC2155，local 吞退出码）；`ensure_download_url_initialized` 分步检查（无 set -e，内层 $() 失败不自动中断外层）。riscv64 桩测试验证失败正确向上传播。

### AUDIT-R1: SCR-008（commit 12dc28c）
- 根因：`get_config_params` 读入端口后直接写 .env，手误输入非数字端口时错误在 `docker compose up` 端口映射解析时才暴露。
- 修复：安装时即校验 1-65535 数字（与更新流程 `validate_panel_update_environment` 一致）。

### AUDIT-R1: SCR-009（commit 7100655）
- 根因：见 findings（R1 S4 T6 动态复现，由"潜在"升级"可达"：单行 JSON + 无 jq → sed 零匹配、文件不变，脚本仍打印"重启 Docker"并 exit 0）。
- 修复：sed 后校验 `"ipv6": true` 是否写入，失败明确告警。单行/多行 JSON 双路径桩测试验证。

### AUDIT-R1: SCR-011（commit 186f2c0）
- 根因：`get_public_ip` 兜底用 `grep -oP`，busybox/Alpine 无 -P 支持恒失败（`|| true` 吞错）。
- 修复：`awk '$1=="inet"{split($2,a,"/"); print a[1]; exit}'`，POSIX 兼容。提取逻辑桩测试验证。

### AUDIT-R1: SCR-013（commit 4d7eb9b）
- 根因：go-backend 缺 `.dockerignore`（vite-frontend 有），`COPY . .` 把 .git 等送进 builder 层。设计权衡：最终镜像只取 /out/paneld，无密钥泄漏，纯减体积/提速。

### AUDIT-R1: SCR-014（commit dda2a73）
- 根因：`vite-frontend/liquid-glass.js`（70KB，含 66KB base64 位移图）全仓无引用（src/index.html/package.json 均无）。删除（git 可恢复）。

### AUDIT-R1: SCR-015（commit 779d5c5）
- 根因：SKILL.md front-matter version 2.1.5 vs package.json 2.1.9-rc4 漂移。对齐到 2.1.9-rc4（发版时 publish-skill.yml 从 tag 同步 package.json）。

### AUDIT-R1: SCR-017（commit 106d802）
- 根因：ci-build.yml setup-go 1.23 vs go.mod 1.25.0（AGENTS.md 记为已知 quirk，靠 GOTOOLCHAIN 能工作但漂移）。对齐到 1.25。node 20 vs Dockerfile node:22 属构建镜像选择，暂不碰（记入 SCR-017 变体）。

### AUDIT-R1: SCR-018（commit 85ae3c1）
- 根因：见 findings（R1 S4 T5 动态复现：caddy 已安装但 /etc/caddy 不存在时 `cat >` 报 No such file or directory）。
- 修复：写文件前 `mkdir -p /etc/caddy`。变体：同函数备份行同样假设目录存在，一并覆盖。

### AUDIT-R1: SCR-019（commit 734dacf）
- 根因：见 findings（R1 S4 T5：动态证伪命令注入——heredoc 单遍展开；证实非法输入写坏 Caddyfile 致 caddy 重启失败）。
- 修复：写文件前校验 `^[A-Za-z0-9.*-]+$`，非法直接拒绝。8 组输入桩测试验证（正常/通配符通过，$()/反引号/空格/斜杠/花括号拒绝）。

## Round 2

### AUDIT-R2: F1X-001（commit 见 git log）
- 根因：`monitorServiceCreate/Update` 对 `target` 仅判空即入库；面板本机执行的 TCP 探测（nodeId<=0）由 `checker.checkTCP` 经 `net.DialTimeout` 直接拨号，无任何 SSRF 拦截。低权限监控用户可指定任意内网 host:port，面板成为探测跳板（F1 审计员端到端 PoC：monuser 建 127.0.0.1:33571 监控→面板拨号→桩收到 HIT→run 返回 success）。
- 变体扫描：`monitorServiceUpdate` 同源同 sink（同修）；ICMP 类型强制要求 nodeId（节点侧执行，信任边界不同，不限制）；隧道质量探测目标为管理员配置的隧道端点（非用户任意输入）；H-15（远程源/外部导入）为另一入口，已有 SecureFetch，本处为遗漏的同类入口。
- 修复（双层）：① `health.ValidateMonitorTCPTarget`（新文件 `internal/health/monitor_ssrf.go`）：`SplitHostPort` 解析，拒绝环回/链路本地/组播/未指定/私网段/元数据/CGNAT，域名逐 IP 判定（fail-closed），支持 `FLVX_FETCH_PRIVATE_ALLOWLIST` 放行通道（与 H-15 同源）；② `monitorServiceCreate`（nodeId<=0 的 tcp）和 `Update`（终态 tcp+本机且目标/类型/位置被改动时）在入库前校验；③ `checker.checkTCP` 拨号前重验（纵深防御，拦截存量坏数据 + 防 DNS rebinding）。
- pre-fix 红：PoC（审计员动态）；post-fix 绿：`TestMonitorServiceCreateRejectsPrivateTargetForNonAdmin`（127.0.0.1/192.168 拒绝、8.8.8.8 放行、nodeId>0 不受限）+ `TestValidateMonitorTCPTarget`（18 用例 + allowlist）。
- 双后端：SQLite 实测；PG 无环境，标注未验证（纯 Go 逻辑，无 SQL 方言）。
- 回归测试：已入永久测试集（health 单元测试 + contract API 测试）。
- 红线自查：端口复用未动；监控权限模型未变（仅加目标校验）；节点侧执行不受影响。
- 附带：旧权限测试 `monitoring_contract_test.go` 的 createBody fixture `127.0.0.1:1` → `8.8.8.8:53`（该测试测权限授予非 SSRF，意图保留）。

### AUDIT-R3: F2X-001（commit 见 git log）
- 根因：R5-B4 的 per-node 端口锁放在 `CreateNodeInbound` 内部，而冲突检查（`inboundPortConflict`）与随机选端口（`pickFreeInboundPort`）在 handler 锁外。两并发 `inboundSave(port=0)` 可挑中同一端口，一个撞唯一索引 `idx_node_inbound_node_port` 报裸 DB 错误。F2 审计员 20 协程动态复现（1 成功 19 失败）。
- 变体扫描：`SaveNodeInbound` 更新路径同源（本次同修，锁覆盖整个 inboundSave）；隧道（mutations.go:836）与转发（repository_mutations.go:1443）锁位置正确；`remote_source.go:142` 与 `kernel_external.go:179` 两个外部 `CreateNodeInbound` 调用点补锁。
- 修复：锁上移到调用方——`inboundSave` 端口处理前 `AcquirePortAllocLock(row.NodeID)`（defer 释放）；`CreateNodeInbound` 去内部锁（sync.Mutex 不可重入，防死锁）。
- pre-fix 红：审计员 PoC；post-fix 绿：`TestConcurrentInboundCreateAutoPortNoConflict`（10 并发 HTTP 创建 port=0，-race 下全成功且端口互异）。
- 双后端：SQLite 实测；PG 无环境，标注未验证（锁为进程内 sync.Mutex，与 DB 无关）。
- 回归测试：已入永久测试集。
- 红线自查：端口复用规则未动；仅加锁范围，无行为变更。

### AUDIT-R3: R5R3-001（commit 见 git log）
- 根因：F2X-001 修复用 `defer ReleasePortAllocLock`，锁从临界区开始一直持有到 `inboundSave` 返回，穿越下游 `applyKernel`（`sendNodeCommandWithTimeout`，超时 20s/5min）等网络调用。违反三处"锁内禁止网络调用"声明（修复注释、port_alloc_lock.go:26、AcquirePortAllocLock 注释），且把并发入站保存串行化在网络 I/O 上。
- 修复：defer 改显式释放——临界区内 8 个提前返回前 `releasePortAlloc()`，DB 写入（Save/CreateNodeInbound）后立即释放，下游下发无锁执行。Release 即 Unlock，9 个释放点互斥，已逐路径核对无双释放、无 defer 残留。
- 回归：`TestConcurrentInboundCreateAutoPortNoConflict` -race -count=2 全绿；handler/contract 包 -race 失败全在 known-baseline。
- 红线自查：锁范围收窄为纯本地临界区，符合设计契约；无行为变更（除消除串行化）。

### AUDIT-R7: R5R7-001（commit 见 git log）
- 根因：`syncForwardServicesWithWarnings` 中 `ensureLimiterOnNode`/`ensureTrafficLimiterOnNode` 失败时，若为节点离线/timeout 错误，仅告警"已跳过下发"并 `continue`。转发创建成功，但限速器永不下发（`ensureLimiterOnNode` 唯一调用点在创建/更新时，无重连/定时补下发机制）。L6 审查员深挖 F6.4 发现。
- 变体扫描：两处同模式（用户/隧道限速 + IP 限速）均修；连接数限制走服务定义，不受影响。
- 修复（方案 A，fail-closed）：离线/timeout 时直接 `return nil, fmt.Errorf("节点 %s 不在线，限速无法下发，请稍后重试")`，不再告警继续。用户明确知道限速未生效，可稍后重试。
- 回归测试：`TestLimiterOfflineFailClosed`（离线错误识别）+ `TestLimiterOfflineErrorMessage`（文案）。完整端到端需真实离线节点，标注未验证。
- 红线自查：fail-closed 语义；错误文案明确；无静默绕过。
