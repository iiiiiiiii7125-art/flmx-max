# findings.md — 发现清单

格式（§6.1 必填）：ID、严重度、标签、可达性证据、位置 `文件:行` + ≤10 行摘录、
触发条件、实际/期望行为、影响、变体扫描、置信度。

无可达性证据不得定 P0/P1。

---

## R0 基线审计（脚本静态，2026-10-19）

### SCR-001｜P2｜可达｜publish-skill.yml 的 `v*` 触发器与仓库纯数字 tag 惯例互斥，且 tag 版本与 npm 发布版本脱钩

- 位置：`.github/workflows/publish-skill.yml:5-7`（`tags: - 'v*'`）；`docker-build.yml:9` 触发器为 `'[0-9]*'`
- 触发条件：按仓库既定发版流程打纯数字 tag（如 `1.0.2`）时
- 实际 vs 期望：实际——纯数字 tag 只触发 Docker 构建，publish-skill 永远不运行，skill 自动发布链死亡；若按字面打 `v*` tag 则只发 skill 跳过 Docker 构建，发版语义分裂。另同一文件 :29-33 算出 version 输出，但 :38 `npm publish` 发布的是 package.json 硬编码版本，tag 名从未写回。期望——skill 发布与主发版流水线用同一 tag 惯例，且发布 tag 对应版本。
- 影响：`@flvx/skill-api` npm 自动发布链实际死亡；发版语义分裂风险
- 变体扫描：`docker-build.yml:4` 的 `VERSION: "2.0.7-beta"` 默认值 + :53-71 分支推送逻辑与 :404-407 `update-release-gost` job（要求 `is_tag=='false'`）永远不可达；:313-314 `sed -i "s|2.0.7-beta|…|"` 无操作（目标字符串已不存在，grep 确认）。属同文件死代码变体。
- 置信度：高（纯静态推导，触发器互斥确定）

### SCR-002｜P2｜可达｜nginx.conf 未设 `client_max_body_size`，>1MB 备份经面板恢复被 413 拦截

- 位置：`vite-frontend/nginx.conf`（http 块，全文件无 `client_max_body_size`）
- 触发条件：Docker 部署（前端走 nginx 反代）且某次导出备份 JSON 超过 1MB（nginx 默认值）
- 实际 vs 期望：实际——`vite-frontend/src/api/index.ts:342` `Network.post("/backup/import", data)` 把整个备份 JSON 经 `location ^~ /api/v1/` 代理；nginx 默认 `client_max_body_size 1m` 直接 413，请求到不了后端（`handler.go:1723 backupImport` 本身无大小问题）。期望——F12 承诺的备份恢复不应被反代默认限制截断。
- 影响：节点/中转/订阅多的面板备份超 1MB 后，"备份→恢复"在面板 UI 静默失败（413），只能手动恢复
- 变体扫描：同文件影响所有经 nginx 的大 POST（F8 超大 Clash/sing-box 配置导入同路径）；WS/SSE 路径无大 body 不受影响
- 置信度：高。替代验证：compose 起面板后 `curl -X POST` 2MB JSON 到 `/api/v1/backup/import` 观察 413（需管理员 token）

### SCR-003｜P2｜可达｜菜单 5 `setup_domain_proxy` 无视用户实际安装的前端端口，永远反代 6366

- 位置：`panel_install.sh:391`（`reverse_proxy 127.0.0.1:${FRONTEND_PORT:-6366}`）
- 触发条件：安装时输入非默认前端端口（如 8080），之后用菜单 5 配置域名反代
- 实际 vs 期望：实际——菜单 5 独立流程，不调用 `get_config_params`，不读部署目录 `.env`；`FRONTEND_PORT` 恒未设置，恒为 6366。期望——应读部署目录 `.env` 的 `FRONTEND_PORT`（更新流程 `:572-587` 的 `resolve_panel_deployment`+`validate_panel_update_environment` 已证明可做到）。
- 影响：Caddy 反代到错误端口 → 域名访问 502，误判为证书/网络问题
- 变体扫描：同文件安装流程 :902-903、:963 正确写入用户输入端口；仅菜单 5 路径丢失该信息
- 置信度：高

### SCR-004｜P3｜可达｜skill `postinstall` 全新安装时恒抛异常，symlink 从未自动创建

- 位置：`skills/flvx-api/package.json` → `scripts.postinstall`：`if(fs.existsSync(target)||fs.lstatSync(target).isSymbolicLink()){fs.unlinkSync(target)}`
- 触发条件：首次 `npm i -g @flvx/skill-api`（`~/.agents/skills/flvx-api` 不存在）
- 实际 vs 期望：实际——`existsSync` 为 false 时必求值 `lstatSync`，对不存在路径抛 ENOENT，被外层 catch 吞掉只打印手动命令；symlink 从未创建。期望——新鲜安装自动建好 symlink。
- 影响：安装体验降级（须手动执行打印命令）
- 变体扫描：同文件 `link` 脚本有完全相同表达式，同样 bug
- 置信度：高

### SCR-005｜P3｜潜在｜`vite-frontend/.env.production` 残留上游仓库地址，本地构建 baked 进错误链接

- 位置：`vite-frontend/.env.production:1-2`（`VITE_GITHUB_REPO=https://github.com/kure29/flvx`，`VITE_APP_VERSION=beta`）
- 触发条件：本机 `pnpm run build`（production 模式自动加载）
- 实际 vs 期望：实际——`src/config/site.ts:29-30` 注入 `github_repo`，`version-footer.tsx:60` 渲染仓库链接 → 本地构建前端指向**上游** kure29/flvx，与 AGENTS.md "REPO 保持 fork" 惯例冲突。期望——默认值为 fork 地址。注：`docker-build.yml:181` 发版构建时重写该文件，官方镜像不受影响，仅本地/第三方构建中招。
- 影响：本地构建产物指向错误仓库链接
- 变体扫描：无
- 置信度：高

### SCR-006｜P3｜潜在｜`install.sh` 全程无 `set -e`（无 `-u`/`pipefail`），失败靠显式检查兜底

- 位置：`install.sh`（全文件无 `set -`；对比 `panel_install.sh:2` 有 `set -e`，两测试脚本 `set -eu`/`set -euo pipefail`）
- 触发条件：任一未被显式检查覆盖的命令失败（如 `mkdir -p "$INSTALL_DIR"`、`write_flux_agent_service` 内 heredoc 写失败）
- 实际 vs 期望：实际——关键下载路径有显式检查（`[[ ! -f … || ! -s … ]]`），但非关键路径失败不会立即终止，会带着半成品继续（如写服务文件失败仍会 `enable_and_start` 然后才报"启动失败"）。期望——失败早停，不留半成品。
- 影响：失败定位变晚、可能留半成品
- 变体扫描：shellcheck -S warning 仅报 :201 SC2155（`local ARCH=$(get_architecture)` 掩码返回值，人工确认无害——get_architecture 靠 echo 输出）
- 置信度：中高。替代验证：故障注入（只读目录挂载后跑安装）观察行为

### SCR-007｜P3｜潜在｜`get_architecture` 对未知架构静默回退 amd64，且安装路径不做 `-V` 烟雾测试

- 位置：`install.sh:40-55`（`*) echo "amd64"`）；安装函数 `echo "🔎 flux_agent 版本：$($INSTALL_DIR/flux_agent -V)"` 未判退出码（对比 `update_flux_agent` 有 `if ! … -V …; then rollback`）
- 触发条件：riscv64/armv7 等非 amd64/arm64 机器跑安装
- 实际 vs 期望：实际——下载 amd64 二进制，`-V` 执行失败（无 set -e 照常继续），写配置、注册服务，最后才报"服务启动失败"。期望——未知架构直接报错退出（releases 只有 amd64/arm64 资产）。
- 影响：低频架构安装失败定位晚
- 变体扫描：无
- 置信度：高

### SCR-008｜P3｜潜在｜安装流程无端口校验，与更新流程的 `validate_panel_update_environment` 不一致

- 位置：`panel_install.sh:902-903`（`read FRONTEND_PORT`/`BACKEND_PORT` 后直接 `${VAR:-6366}` 无校验）vs `:572-595` 更新流程校验端口为 1-65535 数字
- 触发条件：安装时手误输入非数字端口（如 `abc`）
- 实际 vs 期望：实际——`.env` 写入非法值，`docker compose up` 端口映射解析失败才暴露，报错远离输入点。期望——输入时即校验（与更新流程一致）。
- 影响：安装失败定位困难；契约不一致类
- 变体扫描：无
- 置信度：高

### SCR-009｜P3｜可达（R1 已修复）｜`configure_docker_ipv6` 的 sed 兜底在单行 JSON daemon.json 上静默无操作

- 位置：`panel_install.sh:305`（`$SUDO_CMD sed -i 's/^{$/{...}/' "$DOCKER_CONFIG"`）
- 触发条件：`/etc/docker/daemon.json` 存在、无 jq、且为压缩单行 JSON（如 `{"data-root": "…"}`）
- 实际 vs 期望：实际——正则 `^{$}` 只匹配"整行仅为 `{`" 的情况，单行文件不匹配 → sed 无替换但退出码 0 → 脚本继续"重启 Docker"，IPv6 实际未启用，全程无告警。期望——无匹配时报错或回退提示。
- 影响：IPv6 未启用但脚本宣称成功
- 变体扫描：无
- 置信度：高。**R1 S4 动态复现确认**（T6：单行压缩 JSON + 无 jq → 备份已建但 sed 零匹配、文件逐字节不变，脚本仍打印"重启 Docker 服务"并 exit 0），由"潜在"升级为"可达"
- 修复：sed 后校验 `"ipv6": true` 是否写入，失败则明确告警（commit `AUDIT-R1: SCR-009`）

### SCR-010｜P3｜潜在｜测试 sourcing `panel_install.sh` 会触发真实网络自更新；PR 场景可能覆盖测试脚本自身

- 位置：`panel_install.sh:18-42`（`self_update_check`）+ `test-install-scripts-proxy.sh` 的 `load_script_without_main`（`source` 触发顶层 `if [ -f "$0" ]`）
- 触发条件：PR 修改 `panel_install.sh` 并递增 `SCRIPT_VERSION`，CI 跑 `test-install-scripts-proxy.sh` 时
- 实际 vs 期望：实际——`self_update_check` 发现 main 版本 ≠ 本地 → `curl … -o "$0"`（此时 `$0` 是**测试脚本自身**）→ `exec bash "$0"` 执行下载来的旧版 `panel_install.sh` 的 `main()` → 非交互 stdin 下 `read` 持续 EOF → 无限"无效选项"循环直到 CI 超时；且被测的是旧脚本而非 PR 版本。期望——测试应 source 本地 PR 版本，不触发自更新。
- 影响：CI 挂起/测错对象（main 常规推送不受影响，版本一致则 no-op）
- 变体扫描：无
- 置信度：中（需 PR+版本递增双条件）。替代验证：本地 source 并观察（注意会写文件，沙箱快照环境做）

### SCR-011｜P3｜潜在｜`get_public_ip` 用 `grep -oP`，busybox/Alpine 下恒失败

- 位置：`panel_install.sh:891`（前两个 curl 源失败后的兜底分支）
- 触发条件：Alpine/busybox 环境且前两个 IP 源失败
- 实际 vs 期望：实际——busybox grep 不支持 `-P` → 管道失败 → `|| true` 吞掉 → 回退显示字面量"服务器IP"。期望——POSIX 兼容写法。
- 影响：仅安装完成提示信息美观，无功能影响
- 变体扫描：无
- 置信度：高

### SCR-012｜P3｜潜在｜`install_docker` 用 `curl … | sh` 拉官方脚本，无校验

- 位置：`panel_install.sh:224`。TLS 传输，官方源。广泛实践但无完整性校验；与基线"内核安装有官方校验"形成反差（那是内核资产，本条是 Docker 安装器）。列为 P3 潜在，不建议硬改（会破坏 DOCKER_MIRROR=aliyun 等价路径），如需处理应走产品决策 → decisions.md。
- 置信度：中

### SCR-013｜P3｜设计权衡｜`go-backend` 无 `.dockerignore`，构建上下文臃肿

- 位置：`go-backend/`（`vite-frontend/.dockerignore` 存在；go-backend 无）。`go-backend/Dockerfile:5-7` 的 `COPY . .` 会把整个构建上下文送进 builder 层。
- 实际：最终镜像只取 `/out/paneld`，无密钥泄漏进镜像问题，纯构建性能/上下文体积问题。
- 标签说明：按 §1.3 判定流程第 3 条（无声明但有注释/测试表达意图→设计权衡，最多 P3）；此处为构建习惯问题，定 P3 设计权衡。
- 置信度：高

### SCR-014｜P3｜体验｜`vite-frontend/liquid-glass.js` 是无人引用的 70KB vendored bundle

- 位置：`vite-frontend/liquid-glass.js`（esbuild 预构建产物，内嵌 66KB base64 位移图）。全仓库 grep 仅其自身匹配，`src/`、`index.html`、`package.json` 均无引用；Dockerfile 的 `COPY . .` 会带进构建上下文但不进 dist。
- 影响：仓库体积/噪音，无运行时影响
- 置信度：高

### SCR-015｜P3｜体验｜skill 版本号三处漂移

- 位置：`skills/flvx-api/package.json: "version": "2.1.9-rc4"` vs `SKILL.md` front-matter `version: "2.1.5"`；`description` 自称支持 OpenCode/OpenClaw/Claude Code，命名残留。
- 影响：纯文档/元数据问题
- 置信度：高

### SCR-016｜P3｜潜在｜compose 文件 `POSTGRES_PASSWORD` 弱默认值

- 位置：`docker-compose-v4.yml:50` / `docker-compose-v6.yml:50`：`${POSTGRES_PASSWORD:-flux_panel_change_me}`
- 实际：`install_panel` 必写随机密码（`generate_random`），默认值只在用户手写 `.env` 漏项时生效；postgres 未 publish 5432 端口，风险低。
- 置信度：高

### SCR-017｜P3｜备注｜CI Go 1.23 vs go.mod 1.25.0；前端 node 20 vs Dockerfile node:22

- 位置：`ci-build.yml` 用 `setup-go 1.23` 构建声明 `go 1.25.0` 的模块（AGENTS.md 已记录为已知 quirk，靠 GOTOOLCHAIN 自动拉取，脆弱但能工作）；前端 CI node `20.19.0` vs 镜像 `node:22-alpine`。
- 影响：版本漂移类，建议对齐
- 置信度：高

### SCR-018｜P3｜可达（R1 已修复）｜`setup_domain_proxy` 写 Caddyfile 前无 `mkdir -p /etc/caddy`

- 位置：`panel_install.sh:393`（`cat > /etc/caddy/Caddyfile <<EOF`）；前置只有 `[ -f … ] && cp` 备份行，无建目录
- 触发条件：caddy 已安装（`command -v caddy` 通过）但 `/etc/caddy/` 不存在（手动二进制安装、非包管理器安装场景）
- 实际 vs 期望：实际——`cat: …: No such file or directory`，函数非零退出（S4 T5 桩环境动态复现，首次运行即命中）。期望——先 `mkdir -p /etc/caddy`
- 影响：菜单 5 在该场景下直接失败；包管理器安装的 caddy 自带该目录，主路径不受影响
- 变体扫描：同函数备份行同样假设目录存在；`configure_docker_ipv6` 有 `$SUDO_CMD mkdir -p /etc/docker`，做法不一致
- 置信度：高（动态复现）
- 修复：写文件前 `mkdir -p /etc/caddy`（commit `AUDIT-R1: SCR-018`）

### F1X-001｜P1｜可达｜SSRF（R2 已修复）｜服务监控 TCP 目标 SSRF——低权限监控用户可驱使面板向任意内网地址发起 TCP 连接

- 位置：
  - `go-backend/internal/http/handler/monitoring.go:417` `monitorServiceCreate`——`target` 仅判空即入库
  - `go-backend/internal/http/handler/monitoring.go:526` `monitorServiceUpdate`——同样无校验
  - `go-backend/internal/health/checker.go:387` `checkTCP`——`net.DialTimeout("tcp", target, timeout)` 直接拨号
  - `go-backend/internal/http/handler/monitoring.go:657` `monitorServiceRun`——立即触发探测并返回 success/latency（oracle）
- 触发条件：被 admin 授予监控权限的非管理员用户，创建 `{"type":"tcp","target":"<任意host:port>"}` 且不指定 nodeId（面板本机执行），或调 `/api/v1/monitor/services/run`
- 实际 vs 期望：实际——面板无条件向指定内网目标发起 TCP 连接，返回连通性/时延（内网端口扫描能力）。期望——面板本机执行的探测目标做 SSRF 拦截
- 影响：低权限用户获得以面板为跳板的内网 TCP 探测能力
- 可达性证据：F1 审计员端到端动态验证（/tmp 真机启动面板，monuser 创建 127.0.0.1:33571 监控→面板拨号→桩收到连接→run 返回 success）
- 变体扫描：update 同源同 sink；ICMP 强制要求 nodeId（节点侧，信任边界不同）；隧道质量探测目标为管理员配置；H-15 为另一入口已有 SecureFetch
- 置信度：高
- 修复：`health.ValidateMonitorTCPTarget`（创建/修改时校验）+ `checkTCP` 拨号前重验（纵深）；节点侧（nodeId>0）不限制；支持 `FLVX_FETCH_PRIVATE_ALLOWLIST` 放行通道。回归测试：health 单元测试 + contract API 测试。

### F2X-001｜P2｜可达｜并发竞态（R3 已修复）｜入站自动端口分配的检查-插入 TOCTOU（R5-B4 锁放错位置）

- 位置：
  - `go-backend/internal/http/handler/kernel.go:695-701` `pickFreeInboundPort`（无锁）
  - `go-backend/internal/http/handler/kernel.go:723-728` `inboundPortConflict`（无锁）
  - `go-backend/internal/store/repo/repository_kernel.go:173-176` `CreateNodeInbound` 内部才 `AcquirePortAllocLock`（锁获取太晚）
- 触发条件：同一节点上两个并发 `inboundSave`（新建，`port=0` 走自动分配），恰好挑中同一随机端口
- 实际 vs 期望：实际——两请求都通过冲突检查，一个撞唯一索引 `idx_node_inbound_node_port`，用户看到 `UNIQUE constraint failed` 裸 DB 错误须手动重试。期望——端口分配在 per-node 锁内原子完成
- 影响：低（无数据损坏，唯一索引兜底；可重试；概率低）。用户可见功能错误
- 可达性证据：F2 审计员动态验证（20 协程并发同一 `(node_id,port)` → 1 成功 19 约束失败，-race 通过）
- 变体扫描：`SaveNodeInbound` 更新路径同源（同修）；隧道路径（mutations.go:836）与转发路径（repository_mutations.go:1443）锁位置正确；仅入站路径错
- 置信度：高
- 修复：锁上移到调用方——`inboundSave` 在端口处理前 `AcquirePortAllocLock(row.NodeID)`（新建/更新全覆盖）；`CreateNodeInbound` 去内部锁（防死锁）；`remote_source.go`/`kernel_external.go` 两个外部调用点补锁。回归测试 `TestConcurrentInboundCreateAutoPortNoConflict`（10 并发 HTTP，-race 全绿）。

### F9X-001｜P2｜潜在｜PG 下用户配额更新的 lost-update 竞态（R10 已修复）

- 位置：`go-backend/internal/store/repo/repository_user_quota.go:222-256` — `AddUserQuotaUsage`；`:279-340` — `addUserQuotaUsageBatchTx`（同模式）
- 代码：read-modify-write（读 `DailyUsedBytes`→内存加→写回），无原子增量
- 触发条件：PG 部署（`DB_TYPE=postgres`）+ 同一用户流量上报并发到达
- 实际 vs 期望：实际——两并发各读 100、各加 50、各写回 150（应 200），配额被低估→用户获超额配额（fail-open 方向）；期望——原子增量
- 影响：P2 潜在。SQLite 因 MaxOpenConns(1) 串行化安全，仅 PG 受影响。PG 环境无，标"潜在"
- 可达性证据：代码级确认；按 §6.1 无动态复现故不定 P0/P1
- 变体扫描：两路径（单条 + 批量）均修；`SaveUserQuotaConfigTx` 为配置写入不受影响
- 修复：稳态累加改用 `gorm.Expr("daily_used_bytes + ?", usedBytes)` 原子增量；窗口滚动（低频）保持原有逻辑
- 发现者：R10 F9 审计员（并发与资源视角）

### F7X-001｜P2｜可达｜订阅凭据下发失败后被记为"已下发"，永不重试（R8 已修复）

- 位置：
  - `go-backend/internal/http/handler/subscription.go:570` — `s.AppliedActive` 在下发前乐观标记
  - `go-backend/internal/http/handler/subscription.go:310-312` — `syncSubscriptions` 下发失败仅记日志，仍无条件 `UpdateSubscriptionApplied`
- 触发条件：停用/删除/到期/流量超限（凭据应被删除的状态变迁）恰好遇到内核下发失败
- 实际 vs 期望：实际——撤销凭据的操作在单次下发失败后静默失效且无恢复机制；期望——下发失败时不标记 AppliedActive（循环重试）
- 影响：P2。已泄露/应回收的订阅凭据在节点离线窗口内被停用后，节点恢复后凭据依然有效
- 可达性证据：F7 审计员代码逐跳链确认
- 变体扫描：`subscriptionDelete` 同样乐观（DB 记录删除后无跟踪，需独立机制，记为残余风险）；`subscriptionResetTraffic` 走同一根因
- 修复：`syncSubscriptions` 仅成功时更新；`subscriptionSave` 移除乐观标记，成功后更新
- 发现者：R8 F7 审计员

### R5R8-001｜P2｜可达｜subscriptionReset 凭据轮换下发失败无重试（R8 已修复）

- 位置：`go-backend/internal/http/handler/subscription.go:625-652` — `subscriptionReset`
- 触发条件：调用"重置链接"（轮换 Token/UUID/Password）时恰好遇到内核下发失败
- 实际 vs 期望：实际——DB 有新凭据、节点只有旧凭据，新链接不可用、旧链接继续有效；期望——下发失败时触发重试
- 影响：P2。违反 F7.14"重置链接后旧链接立即失效"；已泄露链接的应急吊销在离线窗口内失效
- 可达性证据：R5 复核员代码逐跳链确认
- 变体扫描：`subscriptionSave`/`syncSubscriptions` 已由 F7X-001 修复（active 状态路径）；本变体为凭据轮换路径
- 修复（方案 A）：下发失败时 `UpdateSubscriptionApplied(s.ID, -1)` 强制标记需同步，循环重试；成功后恢复正常值
- 发现者：R8 R5 复核员（退回项）

### R5R7-001｜P2｜可达｜限速 fail-open（R7 已修复）｜节点离线时创建的限速转发永不下发限速器

- 位置：
  - `go-backend/internal/http/handler/control_plane.go:369-380` — `ensureLimiterOnNode` 失败时 `isNodeOfflineOrTimeoutError` → 告警 + `continue` 跳过
  - `go-backend/internal/http/handler/control_plane.go:386-397` — `ensureTrafficLimiterOnNode` 同模式
  - `control_plane.go:2118` — `ensureLimiterOnNode` 唯一调用点（仅创建/更新时，无重连补下发）
- 触发条件：节点离线时创建（或更新）带 `SpeedID`/`IPSpeedID` 的转发
- 实际 vs 期望：实际——限速器下发失败仅告警"已跳过下发"→转发创建成功，但无任何机制在节点恢复后补下发，转发永久无速限运行。期望——节点恢复后补下发，或创建时直接失败（fail-closed）
- 影响：P2。用户配置的限速策略被静默绕过（fail-open）；转发可用但不限速。非 P1：有告警（虽不充分）；非 P0：限速≠计费，无直接资金损失
- 可达性证据：L6 审查员代码逐行确认；全仓 grep 确认无补下发机制
- 变体扫描：`ensureTrafficLimiterOnNode`（IP 限速）同模式（同修）；`buildConnLimiterConfigs` 的连接数限制走服务定义下发，节点上线重建时自动带上，不受影响
- 置信度：高
- 修复（方案 A，fail-closed）：离线/timeout 时直接返回错误"节点 X 不在线，限速无法下发，请稍后重试"，不再告警继续。两处同模式均改。
- 发现者：R7 L6 对抗性审查员

### R5R5-001｜P3｜观察（不阻塞）｜geo 数据更新失败无主动告警，仅写 agent 本地日志

- 位置：`go-gost/x/kernel/geodata.go:82`（`logGeo("分流数据更新失败")`）、`:211`（新文件无法启动内核时回滚）、`:221-227`（`logGeo` 仅写 `inst.logs` 本地缓冲）
- 实际 vs 期望：实际——geo 更新失败（下载失败/checksum 不符/新文件无法启动）时 agent 本地记日志，面板无主动感知。期望——至少有失败计数上报或告警
- 影响：P3 观察。fail-closed 完善（失败保留旧文件、无法启动则回滚）；用户可经面板"内核日志"手动看到。非缺陷
- 建议：后轮考虑 agent 上报失败或面板展示 geo 数据版本/更新时间
- 发现者：R5 R5 复核员（视角：可观测性与性能）

### R5R3-001｜P3｜可达｜锁范围过大（R3 修复引入，已修复）｜inboundSave 的 per-node 端口锁被 defer 持有穿越节点下发网络调用

- 位置：
  - `go-backend/internal/http/handler/kernel.go:699-700`（acquire + defer 释放）
  - `go-backend/internal/http/handler/kernel.go:771-777`（锁内下游：applyKernel / applyChainReferrers / applyChangedTargets）
  - `applyKernel` → `sendNodeCommandWithTimeout`（control_plane.go:756，网络调用，超时 20s/5min）
- 实际 vs 期望：实际——锁从端口临界区开始一直持有到函数返回，穿越"下发到节点"的网络调用，同一节点并发入站保存被串行化在网络 I/O 上。期望——DB 写入后即释放锁，下游下发无锁执行；且符合三处"锁内禁止网络调用"的既有声明
- 影响：P3。无数据正确性问题（串行化是保守方向）；低频操作，实际性能影响有限。但注释与实现矛盾，违反锁的设计契约
- 修复：defer 改为显式释放——临界区内 8 个提前返回前释放，DB 写入后立即释放（9 个释放点互斥，无双释放）
- 发现者：R3 R5 复核员（视角：数据一致性+功能契约）；修复后 spot-check 通过

### R5R2-001｜P3｜加固（R2 已修复）｜checkTCP 的 DNS TOCTOU 窗口

- 位置：`go-backend/internal/health/checker.go` `checkTCP`
- 问题：先 `ValidateMonitorTCPTarget(target)`（内部 resolve→校验），再 `net.DialTimeout("tcp", target)`（内部再次 resolve→拨号）。两次 resolve 之间若 DNS 变化（需攻击者控制目标域 DNS + 竞态），拨号可指向未校验 IP。
- 影响：极低（创建时校验已封堵常规路径，本处为纵深加固）
- 修复：拆出 `resolveValidatedMonitorIPs`，checkTCP 解析→校验→直接拨已校验的 IP 字面量（`net.JoinHostPort(ip.String(), port)`），不再二次解析。参照 H-15 `secureFetchDialContext` 模式。
- 发现者：R2 R5 复核员（视角：功能契约+并发）

### R5R1-001｜P3｜体验（R1 已修复）｜SCR-008 端口校验对前导零输入打印 bash 八进制报错但仍接受

- 位置：`panel_install.sh`（`get_config_params`，commit 12dc28c）
- 触发条件：安装时输入 `080` 等前导零端口
- 实际 vs 期望：实际——`(( 080 < 1 ))` 触发 bash "value too great for base" stderr，但 `(( ))` 返回非零使条件判假，端口被接受。期望——静默接受或明确拒绝，不要吓人的报错
- 影响：纯体验；功能与更新流程一致（更新流程用 `-lt` 同样接受 `080` 为 80）
- 置信度：高（R5 复核员桩实测）
- 修复：算术比较加 `10#` 强制十进制（commit `AUDIT-R1: R5R1-001`）

### SCR-019｜P3｜可达（R1 已修复）｜`setup_domain_proxy` 的 DOMAIN 无字符集校验，非法输入写坏 Caddyfile

- 位置：`panel_install.sh:350`（`read DOMAIN` + `tr -d`）→ 写 Caddyfile 的 heredoc
- 触发条件：输入含 `$()`/反引号/空格外特殊字符的域名（复制粘贴污染等）
- 实际 vs 期望：实际——S4 T5 动态证实**不构成命令注入**（bash heredoc 单遍展开，marker 文件未创建），但非法字符串原样写入 Caddyfile（如 `e.com$(touch /tmp/…) {`），caddy 重启失败，脚本返回 1 提示看日志，报错泛化定位绕远。期望——写文件前做字符集校验，非法输入直接拒绝
- 影响：低（操作者自己输入；失败有处理）
- 变体扫描：`getent hosts "$DOMAIN"` 引用安全；`echo "https://$DOMAIN"` 仅显示
- 置信度：高（动态证伪注入、证实写坏路径）
- 修复：校验 `^[A-Za-z0-9.*-]+$`，非法直接拒绝（commit `AUDIT-R1: SCR-019`）

### 明确"非缺陷"（基线一致，已验证）

- `install.sh` / `panel_install.sh` 的 `REPO="iiiiiiiii7125-art/flvx-max"` 硬编码 fork：符合基线与 AGENTS.md；release 流水线确有重写逻辑（docker-build.yml:311-314 sed + grep 校验）
- H-01/D19：`install.sh:verify_downloaded_binary` 与 `panel_install.sh:download_verified` 均为"缺 .sha256 即拒"fail-closed；release 流水线确为 gost 二进制、install 脚本、compose 文件生成 `.sha256`（docker-build.yml build-gost 与 :319-323）
- H-24：`resolve_latest_release_tag` 两脚本均有 `^[0-9][0-9A-Za-z.\-]*$` 守卫，非版本字符串直接走 API 兜底再失败
- `maybe_proxy_url` 默认 `gcode.hostcentral.cc` 前缀代理：用户既定设计（memory 在档），不报
- `docker.sock` 挂载、`JWT_SECRET: ${JWT_SECRET}` 无默认值：基线"后端 root + docker.sock 是自升级硬要求"覆盖
- `test-install-scripts-alpine.sh` 的 `sed '/^# 执行主函数$/,$d'` 锚点在两脚本中唯一（各 1 处），替桩手法成立；POSIX sh 写法经查无 bashism
- shellcheck -S warning（R0 补跑，2026-10-19）：4 脚本仅 SC2155（install.sh:201、panel_install.sh:639-640，人工确认返回值未被依赖）、SC1007（测试脚本）、SC2034/SC1090（测试脚本，source 动态路径预期内）——无新增 material 发现

---

## R0 基线审计（全量测试，2026-10-19）

基线：`go build ./...`（go-backend、go-gost）全绿；`go vet ./...` 双模块全绿；
`go test ./...`：go-gost 全过；go-backend 9 个包 ok，18 个测试失败（见下）。
前端构建：进行中（另起后台任务）。

### TST-001｜P1｜可达｜R5-B7 DeleteTunnelCascade 用不存在的列删 node_inbound_relay，隧道删除硬失败

- 位置：`go-backend/internal/store/repo/repository_mutations.go:687-690`（commit 8163487 "R5-B7: 级联删除补全"，用户 2026-10-19 16:36 推送）
```go
if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.NodeInboundRelay{}).Error; err != nil {
```
- 模型事实：`NodeInboundRelay` 只有 `inbound_id`/`forward_id`（`internal/store/model/node_kernel.go:105-114`），无 `tunnel_id` 列
- 触发条件：任何隧道删除（API `/api/v1/tunnel/delete` → `DeleteTunnelCascade`），确定性复现
- 实际 vs 期望：实际 `SQL logic error: no such column: tunnel_id (1)`，整个事务回滚，隧道删不掉；期望：级联删除成功
- 影响：F5.7/F6.6 承诺的"删除隧道清理关联"反成删除阻塞；联邦双面板、批量删除同路径受影响
- 变体扫描（同 commit 同函数，逐个核对模型列）：
  - `NftCounterState` 无 `tunnel_id`（仅 node_id/forward_id，model.go:168-169）→ 同样会炸，被本行先失败掩盖（latent）
  - `GroupPermissionGrant` 无 `tunnel_id`（仅 tunnel_group_id/user_tunnel_id，model.go:335-336）→ 同样 latent
  - `TunnelGroupTunnel`/`SpeedLimit`/`TunnelMetric` 有 `tunnel_id` ✓
  - `NftRuleBinding` 走 `forward_id IN (?)` ✓（`forwardIDs` 在 :674 预定义）
- 可达性证据（4 个独立测试复现）：`TestDeleteTunnelReleasesFederationForwardRuntimeByPort`（federation_share_test.go:596）、`TestTunnelDeleteWithForwardsDeleteActionRemovesTunnelAndRulesContract`（tunnel_delete_resolution_contract_test.go:66）、`TestTunnelBatchDeleteWithForwardsReturnsTunnelLevelFailuresContract`（:166 successCount=1 got 0）、`TestFederationDualPanelMiddleExitAutoPortContract`（:140 隧道删除返回 code -2）
- 置信度：高

### TST-002｜P1｜可达｜R5-B7 DeleteNodeKernel 用不存在的列删 user_inbound，内核删除硬失败

- 位置：`go-backend/internal/store/repo/repository_kernel.go:105-108`（同 commit 8163487）
```go
// R5-B7: 清理 user_inbound 中的悬空 kernel_id。
if err := tx.Where("kernel_id = ?", id).Delete(&model.UserInbound{}).Error; err != nil {
```
- 模型事实：`UserInbound` 只有 `user_id`/`inbound_id`/`relay_id`（model.go:276-281），无 `kernel_id` 列
- 触发条件：删除节点内核（`DeleteNodeKernel`），确定性复现；事务回滚，内核删不掉（F1.7 卸载/换版本路径）
- 变体扫描（同 commit 其余新增）：`DeleteNodeCascade` 新增 8 个 `node_id = ?` 删除——`NodeSSHConfig`/`NftRuleBinding`/`NftCounterState`/`NodeMetric`/`TunnelMetric`/`PeerShareRuntime`/`SubscriptionCounter` 逐一核对全有 `node_id` ✓（用户自带测试 `TestB7_DeleteNodeCascadeNoOrphans` 通过）；`DeleteUserCascade` 的 `MonitorPermission` 有 `user_id` ✓（model.go:347）
- 可达性证据：`TestKernelInboundLifecycleContract`（kernel_inbound_contract_test.go:261 forced remove → code -2 `no such column: kernel_id`）
- 置信度：高

### TST-003｜P1｜可达｜R5-B4C ensureUsernameAvailable 双重引号表名，备份导入用户必失败

- 位置：`go-backend/internal/store/repo/username_check.go:24`（commit 23c281b "R5-B4C"，在已合并的 17 提交批次内）
```go
query := tx.Model(&struct{ ID int64 }{}).Table("\"user\"").Where("\"user\" = ?", username)
```
- 机制：`.Table()` 的参数已含字面引号，GORM quoter 再转义一次 → SQL `FROM """user"""` → SQLite 解析为字面名为 `"user"` 的表 → `no such table: "user"`。smoking gun：报错信息**带引号**（`no such table: "user"`），而真正的缺表报错不带引号（对比日志中 `no such table: tunnel`）
- 触发条件：备份 `Import()` 含 `users` 的任何导入（F12.1/F12.2），确定性复现
- 实际 vs 期望：实际 `import users failed`，整个导入失败；期望：用户恢复成功
- 影响：F12 备份恢复核心链路断裂（用户维度）
- 变体扫描：`repository.go:510` 的 `.Where("\"user\" = ?", ...)` 是 raw SQL，`"user"` 在 SQL 层即合法标识符，不受影响（seedData 相关测试全过）；全仓仅 username_check.go:24 一处 `.Table()` 双引号。另注意该函数注释声称被 CreateUser/UpdateUser 等复用，实际全仓仅 `importUsers` 一处调用（repository.go:2557）
- 可达性证据：`TestImportUsersDoesNotStoreLegacyMD5Passwords`（repository_backup_test.go:207）、`TestTrafficLimitMiBSurvivesBackupRestore`（repository_traffic_limit_test.go:57）
- 置信度：高

### TST-004｜P2｜可达（测试债务）｜批次 4 唯一索引 (node_id,port) 使 7 个旧测试 fixture 非法

- 背景：迁移 `r5b4_v1_port_user_unique` 建唯一索引 `idx_forward_port_node_port` / `idx_chain_tunnel_node_port` / `idx_node_inbound_node_port`。按 §1.1 判定基线（冲突域=同节点+同端口；唯一索引须含 node_id）**设计正确**，产品行为无误。
- 受影响测试（fixture 在同一 (node_id,port) 插两行，违反新约束；`-run` 隔离复现，排除测试污染）：
  - forward_port 唯一冲突：`TestBatchDeletePartialFailurePerItem`（`seedForwardForNftables` 硬编码 port 20000 被调两次，forward_delete_pending_d1_test.go:283/292）、`TestPendingDeltaDroppedButBaselineAdvances`（:336）、`TestQuotaPauseNftRemovesRules`（quota_nft_d2_test.go:22，`create forward: 端口已被占用`）、`TestIssue313_EntryPortCrossTunnelConflictContract`（issue313…:118 `UNIQUE constraint failed: forward_port.node_id, forward_port.port`）
  - chain_tunnel 唯一冲突：`TestTunnelListAttachesBestExitStateOnlyForEligibleTunnels`（tunnel_best_exit_display_test.go:278）、`TestTunnelGetAttachesBestExitStateToSelectedTunnel`（:246）、`TestRelayTargetTunnelThroughIX`（kernel_relay_target_test.go:66）
- 定性：产品行为符合设计基线，属测试 fixture 过时。修复=更新 fixture 端口（不断言变更，仅让 fixture 遵守已声明约束；不违反"不削弱断言"禁令）
- 置信度：高

### 环境受限（记入 blocked.md，不报缺陷）

- `TestDownloadReleaseAssetUsesGithubProxyWhenEnabled` / `TestDownloadReleaseAssetRejectsOversizedBody`（system_upgrade_test.go:63/103）：`tls: first record does not look like a TLS handshake`——沙箱对 `example.invalid` 的 DNS/透明代理干扰所致；CI 真实 DNS（NXDOMAIN）下走正常错误分支。替代验证：CI 的 ci-build.yml 后端测试 job。
- 已知基线复现（与 `.audit/known-baseline.md` 一致，不新增）：`TestExternalImportFromSubscriptionURLContract`、`TestRemoteSourceContract`（H-15 SSRF 拦截测试用 localhost URL，沙箱环境问题）。

### 与历史记忆的差异（诚实记录）

- 记忆中 fix/r5-audit 时期"除 2 个预先存在失败外全过"，但本次在 main@9a2810b 测得 18 个失败。逐条归因：TST-003 的 `username_check.go` 来自 23c281b（在已合并的 17 提交批次内）；TST-001/TST-002 来自用户今日 16:36 的 R5-B7 两笔提交（合并之后）；TST-004 来自批次 4 唯一索引迁移。均非本次审计引入。

## R18 新增

### DAT18-001（P2，可达）— 已修复
- `DeleteTunnelCascade` 遗漏 `NftPendingDelete`（forward_id），与 `DeleteForwardCascade` 的 R5-B3 D1-001 补充不一致。删除隧道后 nft_pending_delete 残留孤儿行，后台重试循环持续触发不必要 reconcile。
- 位置：`go-backend/internal/store/repo/repository_mutations.go:669-713`
- 修复：补 `tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.NftPendingDelete{})`

### DAT18-002（P3，观察）— 已修复
- `DeleteTunnelCascade` 遗漏 `TunnelQuality`（tunnel_id）。删除隧道后 tunnel_quality 历史探测数据残留（有 `PruneTunnelQualityResults` 自愈，属数据卫生）。
- 位置：同上
- 修复：补 `tx.Where("tunnel_id = ?", tunnelID).Delete(&model.TunnelQuality{})`

### DAT18-003（P2，可达）— 已修复
- `DeleteNodeCascade` 遗漏 `NftPendingDelete`（node_id）、`ServiceMonitor`（node_id）、`ServiceMonitorResult`（node_id）。删除节点后孤儿监控任务持续产生"节点不在线"失败记录。
- 位置：`go-backend/internal/store/repo/repository_mutations.go:392`
- 修复：补三行级联删除

### R5R18-001（P2，可达）— 已修复（R5 复核退回项）
- `DeleteNodeCascade` 遗漏 `PeerShare` 与 `PeerShareResource`（node_id）。已删节点的 share token 在 `GetPeerShareByToken` 中不校验节点存在性，残留即未吊销。
- 位置：`go-backend/internal/store/repo/repository_mutations.go:392`
- 修复：补两行级联删除（`PeerShareResource` 先于 `PeerShare`，子表优先）
- 发现者：R5 复核员（67 模型系统性外键扫描）

## R22 新增（防假收敛阶梯 L1–L5）

### LAD22-001（P3）— 已修复
- `backupExport` 的 `json.NewEncoder(w).Encode` 中途失败时，HTTP 200 已发出，错误信封追加到截断的残缺流后，客户端收到损坏响应。
- 位置：`go-backend/internal/http/handler/handler.go:1708-1712`
- 修复：改 buffer-first（先 Encode 到 `bytes.Buffer`，成功后再发 headers+body）

### LAD22-002（P3，观察，未修）
- 备份 round-trip 可空列 NULL→"" 漂移（`importNodes`/`importTunnels` 无条件 `Valid: true`）。读取侧等价，纯 cosmetic，属产品决策。

### LAD22-003（P3，观察，未修）
- `DisallowUnknownFields` 使含未知字段的备份被整单拒绝。当前 `"1.0"` 自闭环无影响，未来格式演进时的产品决策。

### LAD22-004（P3，观察，未修）
- `Import` nil-safe 分支（`backup.Inbounds != nil`）无测试覆盖（变异 M2 存活）。建议后轮补回归测试。

### LAD22-005（P3，观察，未修）
- 导入前自动备份失败路径无测试覆盖（变异 M3 存活）。建议后轮补回归测试。

### R5R22-001（P2，可达）— 已修复（L6 退回项）
- 导出/导入 10MB 不对称：`decodeJSON` 的 10MB `MaxBytesReader` 上限会误拒合法大备份（Fermi 估算：2 万用户 ≈ 15MB；动态验证：11.44MB 合成备份被拒"http: request body too large"）。
- 位置：`go-backend/internal/http/handler/handler.go:1569`（`decodeJSON`）→ `backupImport`（:1740）
- 修复：新增 `decodeJSONWithLimit(body, out, limit)`；`backupImport` 改用 100MB 上限（与 nginx 50m 对齐，仍有界防 DoS）。动态验证：11.74MB 备份通过大小检查（仅因合成数据字段不匹配被拒，符合预期）。
- 发现者：L6 对抗性审查员
