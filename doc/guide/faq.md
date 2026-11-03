# 常见问题

## 面板与安装

### 安装脚本提示 "Docker command not found"？
使用一键安装脚本时，Docker 缺失会由脚本自动安装，正常不会看到这条提示；它多见于手动用 docker compose 部署的场景。这种情况请确保您的系统已安装 Docker 和 Docker Compose。
- Ubuntu/Debian 安装 Docker: `curl -fsSL https://get.docker.com | bash`

### 面板无法访问 (Connection Refused)？
1. 检查防火墙是否放行了前端端口（默认 `6366`）。
2. 检查容器是否正常运行: `docker ps`。
3. 查看容器日志:容器名以部署目录 `.env` 的 `PANEL_BACKEND_CONTAINER` / `PANEL_FRONTEND_CONTAINER` 为准(脚本安装默认 `flmx-panel-backend` / `flmx-panel-frontend`)。在部署目录执行 `docker logs "$(sed -n 's/^PANEL_BACKEND_CONTAINER=//p' .env)"`,前端容器把键名换成 `PANEL_FRONTEND_CONTAINER` 即可。旧模板部署的 `.env` 里这两键的值就是旧名 `flux-panel-backend` / `vite-frontend`,同一条命令照样取得到。

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
1. 检查 `DATABASE_URL` 里内嵌的口令：后端校验的是 `DATABASE_URL` 的口令（它从不读 `POSTGRES_PASSWORD` 本身），该口令为空、**或仍是默认值 `flux_panel_change_me` / `postgres`** 时，1.2.0 起后端会直接拒绝启动。`POSTGRES_PASSWORD` 是数据库容器初始化用的变量，两处口令要保持一致，只改一处同样起不来。
2. 查看容器日志:容器名以部署目录 `.env` 的 `PANEL_POSTGRES_CONTAINER` 为准(脚本安装默认 `flmx-panel-postgres`)。在部署目录执行 `docker logs "$(sed -n 's/^PANEL_POSTGRES_CONTAINER=//p' .env)"`。
3. 如果是首次启动后修改了密码，需要删除旧的数据卷重新初始化:卷名以 `.env` 的 `FLMX_POSTGRES_VOLUME` 为准(脚本安装默认 `flmx-postgres-data`),执行 `docker volume rm "$(sed -n 's/^FLMX_POSTGRES_VOLUME=//p' .env)"`。旧模板部署这两键的值即旧名 `flux-panel-postgres` / `postgres_data`。

## 节点与订阅

### 内核状态显示“异常”

在“内核”标签里点击该内核 → “日志”查看原因，常见的有端口被占用、证书路径不存在、IPv6 被禁用（见 [搭建节点 - 监听地址](./nodes.md#监听地址)）。

### 流量一直是 0

检查节点是否安装了 nftables；流量每分钟左右同步一次。

### ACME 一直申请不到证书

确认域名已解析到节点；HTTP 验证时 80 端口不能被其他程序占用，并且要能从公网访问；也可以改用 DNS 验证。

### Snell 用哪个版本

Snell 的版本匹配不是简单的「主版本必须一致」：**5.x 节点可以服务 v4 与 v5 入站**；只有 **6.x 是特例——装了 6.0.0 的节点只接 v6 入站**，拒绝 v4 / v5。除此之外的拒绝方向只有一个：**入站版本高于节点**（如 v6 入站配 5.x 节点、v5 入站配 4.x 节点）。v6 入站需要节点安装 Snell 6.0.0（beta，仅 v6 客户端可用）。

### Reality 用 Clash / Mihomo 连不上

Xray 26.7.11 起，Reality 未设置“最低客户端版本”时默认只接受 26.3.27 及以上的客户端，而 Mihomo（Clash Verge、Stash、FlClash 等）上报的版本是 1.8.2，会被拒绝。面板默认把最低客户端版本设为 `1.0.0`，客户端不会再被这条版本门拒绝；如需调整，在 Reality 设置里修改“最低客户端版本”。也可以在安装内核时选择旧版本的 Xray。注意：低于 26.5.9 的 Xray 没有内置私网拦截（见下节「经节点代理访问内网地址会被拦截」），为 Reality 降级内核会同时让该节点失去私网拦截，安装这类版本时面板会给出明确警告；优先用上面的「最低客户端版本」设置解决，不要为 Reality 降级内核。（注：本节的 Xray 26.7.11、默认接受下限 26.3.27 与 Mihomo 上报版本 1.8.2 均转述自代码注释、未在仓内逐项实证，实际以所装 Xray 版本的行为为准。）

### 面板自升级提示“部署docker-compose.yml不可用”

见 [升级](./upgrade.md#修复面板内升级)。

### 用 IP 访问时复制失败

浏览器只在 HTTPS 页面里完全开放复制功能。面板会自动改用兼容方式复制，仍失败时会提示；建议通过 HTTPS 域名访问面板，或长按文字手动复制。

### 经节点代理访问内网地址会被拦截（已知限制）

经面板入站（VLESS / VMess / Trojan / Hysteria / SS / TUIC / AnyTLS / NaïveProxy / Snell 等）访问私网目标——回环 `127.0.0.1`、RFC1918（`10/8`、`172.16/12`、`192.168/16`）、链路本地 `169.254/16`（含云元数据 `169.254.169.254`）、运营商 CGNAT `100.64/10`、IPv6 的 ULA/链路本地/组播，以及保留与文档测试段——时，连接会被拦截（Xray 附版本条件且段集以其内置规则为准，见下；sing-box 与 Snell 共用面板下发的同一份段集）。这是与 geo 数据无关（面板不靠 geoip.dat 做此判定）的有意为之的安全策略：防止任一持订阅用户借节点探查内网与云元数据。三个内核的拦截机制不同：

- **Xray 内核**：由自由出站（freedom）的内置默认规则拦截私网目标；**具体拦截段集以所装 Xray 版本的内置规则集为准**（上节列举的段集是 sing-box / Snell 的产品正本，Xray 的内置集与之大体一致，但个别保留/文档段——如 IPv6 文档段——可能有出入，不以面板文档为准）。被拦连接会静默挂起一段时间后断开（约 30–90 秒，该区间为上游经验值、仓内无实证，实际时长随 Xray 版本与网络环境而异），客户端看不到错误信息（静默挂起是 Xray 独有的表现）。该内置规则自 Xray **26.5.9** 起才有：更低的版本对私网目标直通、无任何拦截，安装这类版本时面板会明确警告，请改装 26.5.9 及以上。
- **sing-box 内核**：由面板生成的节点配置拦截——路由规则中写入一份显式段集（`ip_cidr`：RFC1918、回环、链路本地、CGNAT、ULA、组播与保留/文档段，与 Snell 的拦截段集为同一份产品正本）→ `reject` 的私网拒绝规则，它排在嗅探（sniff）规则之后、先于链式、分流与按用户规则生效，用户自设分流指向私网目标同样被拒绝；被拦连接会被直接拒绝，不会像 Xray 那样静默挂起。拦截对两种目标形态都成立：IP 字面量直接命中该规则；域名目标在经本地出站（直连与按用户直连出站、含出站 IP 策略的直连克隆）发出前会被先解析再按同一规则判定，解析结果中只要含任一私网地址即整连接拒绝（含 A/AAAA 混合结果）。经链式或分流代理转发到远端落地的域名不在此限：它与 Xray 相同，由落地端解析、按落地端网络判定（Xray 的 freedom 规则同样只管本地出站）。
- **Snell 内核**：snell-server 本身没有目标过滤能力，由节点上的 agent 在系统层拦截——snell-server 进程以专用系统用户运行，agent 用 nftables 按该用户新发起出站连接的目的 IP 丢弃非公网目标（IP 字面量与域名解析结果一视同仁）；被拦连接表现为连接超时。唯一的放行例外是系统 DNS 解析：节点 `/etc/resolv.conf` 里配置的 nameserver 在 53 / 853 端口被精确放行（否则走局域网解析器的节点域名全灭），此外没有其他放行面。注意这份豁免取的是守卫下发那一刻的 resolv.conf 快照——运行期改了节点 nameserver 后，要重启 Snell 内核或重新下发入站让守卫重应用，新解析器才会被豁免；不重应用时以专用用户新发起的解析会失败（已在跑的 snell-server 进程沿用旧解析器、不受影响）。拦截只针对 snell-server 主动拨出的连接：客户端连入 Snell 入站的会话回包不受影响，用私网地址接入的客户端（同一局域网/VPC、本机回环、经私网中转跳板）可正常使用。若节点缺少 nftables 或无法建立该专用用户，Snell 入站会明确启动失败，不会在无拦截的状态下运行。另注意：Snell 入站若跑在 sing-box 内核上，由上面 sing-box 的配置规则拦截、不走本节的 agent / nftables 机制——两条路径的拦截结论一致。

合规客户端不受影响：面板下发的订阅配置已把私网与 LAN 流量路由为直连，根本不会送进节点。受影响的只是手动客户端与运维用法（例如经代理访问面板自身、LAN 服务）。私网目标请直连，不要走代理。
