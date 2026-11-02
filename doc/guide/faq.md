# 常见问题

## 面板与安装

### 安装脚本提示 "Docker command not found"？
使用一键安装脚本时，Docker 缺失会由脚本自动安装，正常不会看到这条提示；它多见于手动用 docker compose 部署的场景。这种情况请确保您的系统已安装 Docker 和 Docker Compose。
- Ubuntu/Debian 安装 Docker: `curl -fsSL https://get.docker.com | bash`

### 面板无法访问 (Connection Refused)？
1. 检查防火墙是否放行了前端端口（默认 `6366`）。
2. 检查容器是否正常运行: `docker ps`。
3. 查看容器日志: `docker logs flux-panel-backend` 或 `docker logs vite-frontend`。

### 节点显示离线？
1. 检查节点服务器与面板服务器之间的网络连通性。
2. 确认在节点端安装时输入的 **面板地址** 和 **密钥** 是否正确。
3. 检查节点端服务状态：systemd 节点执行 `systemctl status flux_agent`；Alpine / OpenRC 节点没有 systemd，执行 `rc-service flux_agent status`。
4. 查看节点端日志：systemd 节点执行 `journalctl -u flux_agent -f`；Alpine / OpenRC 节点没有 journald，日志在文件 `/var/log/flux_agent.log`（错误日志 `/var/log/flux_agent.err.log`），用 `tail -f /var/log/flux_agent.log` 查看。
5. 如果是升级到 1.3.4 后才离线、且面板没有 TLS：多半是节点 `config.json` 的 `addr` 为裸地址（无 `ws://` 前缀）——新版本不再自动降级到明文连接，把 `addr` 改为显式 `ws://<面板地址>` 后重启 agent 即可，详见 [节点连不上：地址写法与时钟](./install.md#节点连不上-地址写法与时钟-1-3-4-起)。同时检查节点与面板的时钟偏差是否超过 5 分钟（先校时）。

### 只有 TCP 能通，UDP 不通？
请检查服务器防火墙和安全组（AWS/阿里云/腾讯云等）是否同时放行了对应端口的 **TCP 和 UDP** 协议。

### IPv6 无法使用？
面板安装脚本会自动尝试配置 Docker 的 IPv6。如果失败，请手动检查 `/etc/docker/daemon.json` 配置，确保 `ipv6: true` 且分配了正确的 `fixed-cidr-v6` 子网。

### 如何切换到 PostgreSQL？
在 `.env` 文件中设置 `DB_TYPE=postgres`，并让 `DATABASE_URL` 与 `POSTGRES_*` 保持一致，然后执行 `docker compose up -d` 重启服务即可。使用安装脚本部署时，`POSTGRES_PASSWORD` 会自动随机生成并写入 `.env`。详见 [PostgreSQL](./postgresql.md)。

### 从 SQLite 迁移到 PostgreSQL 后数据丢失？
1. 确认迁移前已备份 SQLite 文件（`gost.db.bak`）。
2. 确认 `pgloader` 命令执行成功，检查其输出是否有报错。
3. 确认 `.env` 中 `DATABASE_URL` 的密码与 `POSTGRES_PASSWORD` 一致。
4. 详细迁移步骤参考 [PostgreSQL](./postgresql.md)。

### PostgreSQL 容器启动失败？
1. 检查 `POSTGRES_PASSWORD` 是否已设置：不能为空，**也不能仍是默认值 `flux_panel_change_me` 或 `postgres`**——1.2.0 起后端遇到空/默认口令会直接拒绝启动。
2. 查看容器日志：`docker logs flux-panel-postgres`。
3. 如果是首次启动后修改了密码，需要删除旧的数据卷重新初始化：`docker volume rm postgres_data`。

## 节点与订阅

### 内核状态显示“异常”

在“内核”标签里点击该内核 → “日志”查看原因，常见的有端口被占用、证书路径不存在、IPv6 被禁用（见 [搭建节点 - 监听地址](./nodes.md#监听地址)）。

### 流量一直是 0

检查节点是否安装了 nftables；流量每分钟左右同步一次。

### ACME 一直申请不到证书

确认域名已解析到节点；HTTP 验证时 80 端口不能被其他程序占用，并且要能从公网访问；也可以改用 DNS 验证。

### Snell 用哪个版本

入站的 Snell 版本不能高于节点安装的 Snell 主版本，v5 入站需要节点安装 Snell 5.x，v6 入站需要节点安装 Snell 6.0.0（beta，仅 v6 客户端可用）。

### Reality 用 Clash / Mihomo 连不上

Xray 26.7.11 起，Reality 未设置“最低客户端版本”时默认只接受 26.3.27 及以上的客户端，而 Mihomo（Clash Verge、Stash、FlClash 等）上报的版本是 1.8.2，会被拒绝。面板默认把最低客户端版本设为 `1.0.0`，所有客户端都能连接；如需调整，在 Reality 设置里修改“最低客户端版本”。也可以在安装内核时选择旧版本的 Xray。

### 面板自升级提示“部署docker-compose.yml不可用”

见 [升级](./upgrade.md#修复面板内升级)。

### 用 IP 访问时复制失败

浏览器只在 HTTPS 页面里完全开放复制功能。面板会自动改用兼容方式复制，仍失败时会提示；建议通过 HTTPS 域名访问面板，或长按文字手动复制。

### 经节点代理访问内网地址会被拦截（已知限制）

经面板入站（VLESS / VMess / Trojan / Hysteria / SS / Snell 等）访问私网目标——回环 `127.0.0.1`、RFC1918（`10/8`、`172.16/12`、`192.168/16`）、链路本地 `169.254/16`（含云元数据 `169.254.169.254`）等——时，连接会被拦截，三个内核均如此。这是与 geo 数据无关（面板不靠 geoip.dat 做此判定）的有意为之的安全策略：防止任一持订阅用户借节点探查内网与云元数据。三个内核的拦截机制不同：

- **Xray 内核**：由自由出站（freedom）的内置默认规则拦截私网目标；被拦连接会静默挂起 30–90 秒后断开，客户端看不到错误信息（静默挂起是 Xray 独有的表现）。
- **sing-box 内核**：由面板生成的节点配置拦截——路由规则中置顶写入 `ip_is_private`（非公网地址）→ `reject` 的私网拒绝规则，先于链式、分流与按用户规则生效，用户自设分流指向私网目标同样被拒绝；被拦连接会被直接拒绝，不会像 Xray 那样静默挂起。拦截对两种目标形态都成立：IP 字面量直接命中置顶规则；域名目标在经本地出站（直连与按用户直连出站、含出站 IP 策略的直连克隆）发出前会被先解析再按同一规则判定，解析结果中只要含任一私网地址即整连接拒绝（含 A/AAAA 混合结果）。经链式或分流代理转发到远端落地的域名不在此限：它与 Xray 相同，由落地端解析、按落地端网络判定（Xray 的 freedom 规则同样只管本地出站）。
- **Snell 内核**：snell-server 本身没有目标过滤能力，由节点上的 agent 在系统层拦截——snell-server 进程以专用系统用户运行，agent 用 nftables 按该用户的出站数据包目的 IP 丢弃一切非公网目标（IP 字面量与域名解析结果一视同仁）；被拦连接表现为连接超时。若节点缺少 nftables 或无法建立该专用用户，Snell 入站会明确启动失败，不会在无拦截的状态下运行。

合规客户端不受影响：面板下发的订阅配置已把私网与 LAN 流量路由为直连，根本不会送进节点。受影响的只是手动客户端与运维用法（例如经代理访问面板自身、LAN 服务）。私网目标请直连，不要走代理。
