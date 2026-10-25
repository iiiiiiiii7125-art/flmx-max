# 安装部署

面板端管理所有节点、隧道和入站；节点端（agent）装在中转机和落地机上，负责转发和运行内核。

## 面板端

面板端负责管理用户、节点和转发规则。

### 环境要求
- 操作系统：Linux (推荐 Debian 10+ / Ubuntu 20.04+)
- 必须安装 Docker 和 Docker Compose

### 一键安装

使用以下命令即可快速安装面板：

```bash
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh -o panel_install.sh && curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh.sha256 -o panel_install.sh.sha256 && sha256sum -c panel_install.sh.sha256 && chmod +x panel_install.sh && ./panel_install.sh
```

**安装过程中会提示输入以下信息：**
- **前端端口**: 默认为 `6366`
- **后端端口**: 默认为 `6365`

脚本会自动检测系统是否支持 IPv6，并自动配置 Docker 的 IPv6 支持。

### 访问面板

安装完成后，访问：
`http://<服务器IP>:<前端端口>` (默认: `http://<服务器IP>:6366`)

**默认管理员账号：**
- 用户名: `admin@user`
- 密码: `admin@user`

::: warning
首次登录后请立即在“我的 / 个人资料”里修改默认密码。
:::

### 维护菜单

再次运行 `./panel_install.sh` 脚本可以看到管理菜单：
1. 安装面板
2. 更新面板
3. 卸载面板
4. 迁移到 PostgreSQL
5. 配置域名反向代理（自动 HTTPS）
6. 退出


## 节点端

节点端运行在实际进行流量转发的服务器上，需要连接到面板端进行管理。

::: warning 部署注意：同一宿主只部署一个 agent
同一台宿主机上并行运行多个 agent 会共享宿主 nftables 命名空间、互相干扰白名单物化；多节点场景请一台宿主部署一个 agent（不同宿主各一不受此限）。
:::

### 用面板生成的命令安装（推荐）

1. **网站配置** 里填好“面板后端地址”，节点通过它连接面板（通常是 `http://面板IP:6365`，或反向代理后的地址）
2. **节点** 页新建节点，点击 **安装**，安装命令会复制到剪贴板（复制失败时会弹出命令框）
3. 到节点服务器上以 root 执行

### 手动安装

在节点服务器上运行：

```bash
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/install.sh -o install.sh && curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/install.sh.sha256 -o install.sh.sha256 && sha256sum -c install.sh.sha256 && chmod +x install.sh && ./install.sh
```

Alpine Linux 最小化安装若未包含 `curl`，可使用系统自带的 `wget` 下载：

```bash
wget -O install.sh https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/install.sh && chmod +x install.sh && ./install.sh
```

脚本会在 Alpine 上自动安装 Bash、`curl` 和 CA 证书，并使用 OpenRC 注册、启动和管理 `flux_agent` 服务；其他受支持的 Linux 发行版继续使用 systemd。

**安装过程中会提示输入：**
- **服务器地址**: 面板端的通信地址（通常是 `http://<面板IP>:<后端端口>`，例如 `http://1.2.3.4:6365`）。
- **密钥**: 节点页里该节点的密钥。

或者直接使用带参数的命令（适用于自动化部署）：

```bash
# 替换 <面板地址> 和 <密钥>
./install.sh -a "http://1.2.3.4:6365" -s "your_node_secret"
```

### 验证安装
安装完成后，服务会自动启动。
- systemd 查看状态: `systemctl status flux_agent`
- Alpine/OpenRC 查看状态: `rc-service flux_agent status`
- 回到面板 **节点管理** 页面，该节点状态应显示为 **在线**。

### 节点连不上：地址写法与时钟（1.3.4 起）

agent 与面板之间有两条通道，都**不再自动降级到明文**：

- **命令面（WebSocket）**：只按单一 scheme 连接，不再在 wss 失败后自动回退 ws；
- **上报面（HTTP 流量/配置上报）**：候选地址同样收敛为单一 scheme，与命令面同口径。

地址的判定规则两条通道一致：`config.json` 的 `addr` **写了 scheme 就按写的来**（`ws://` / `http://` 即表示你确认使用明文），**裸地址（无前缀）默认按加密连接**（wss / https）。由此产生的典型存量问题：

- **面板只有 http（无 TLS）、节点 `addr` 是裸地址**：这类节点在 agent 升级/重启后会连不上，日志里会明确打印禁止降级的说明。解决办法二选一：把节点 `config.json` 的 `addr` 改为显式 `ws://<面板地址>`（与安装时填的面板地址一致即可，改完重启 agent）——这是「确认明文部署」的显式动作；或者给面板配置 TLS。**新安装的节点不受影响**：1.3.4 起面板生成节点安装命令时，会按面板实际的服务方式自动把 scheme 补成显式写法。
- **时钟偏差**：加密命令信封带时间戳，与面板时钟相差超过 **±5 分钟**（或缺失）会被拒绝执行。节点或面板时钟偏差过大的老部署，请先校时（开启 NTP 同步）再排查连不上问题。


## 反向代理（可选）

如果需要通过域名访问面板并自动获取 HTTPS 证书，可以使用 Caddy 作为反向代理。

### 安装 Caddy

```bash
# Debian / Ubuntu
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudflare.com/content/v1/e2qwFJ2fRP2b2q/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudflare.com/content/v1/e2qwFJ2fRP2b2q/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update
sudo apt install caddy
```

其他系统请参考 [Caddy 官方安装文档](https://caddyserver.com/docs/install)。

### 配置 Caddyfile

编辑 Caddy 配置文件：

```bash
sudo nano /etc/caddy/Caddyfile
```

#### 面板域名配置

将 `panel.example.com` 替换为你自己的域名：

```txt
panel.example.com {
    reverse_proxy 127.0.0.1:6366
}
```

Caddy 会自动为域名申请和续期 HTTPS 证书，无需额外配置。反代目标请写 `127.0.0.1` 不要写 `localhost`：`localhost` 可能先解析到 IPv6 `[::1]`，经 docker-proxy 转发时连接会被重置导致 502（脚本自动生成的 Caddyfile 用的也是 `127.0.0.1`）。

### 重启 Caddy

```bash
sudo systemctl restart caddy
```

### 注意事项

- 确保域名已正确解析到服务器 IP。
- 确保服务器防火墙放行了 **80** 和 **443** 端口（Caddy 自动申请证书需要）。
- 使用 Caddy 反向代理后，可以在 `.env` 中将前端端口改为仅监听本地，避免直接暴露：
  ```
  FRONTEND_PORT=127.0.0.1:6366
  ```

### 登录限流与可信代理跳数

面板的登录限流按客户端真实 IP 生效。当面板前面有反向代理时，后端通过
`X-Forwarded-For` 从右向左按可信跳数解析真实 IP：

| 你的拓扑 | 跳数设置 |
|---|---|
| Caddy → 后端（`panel_install.sh` 默认） | `FLMX_TRUSTED_PROXY_HOPS=1`（默认，可不设） |
| nginx（frontend 容器）→ 后端（docker 默认） | `1`（默认） |
| Caddy → nginx → 后端 | `2` |

跳数配少了，限流键会取到代理自己的 IP（多用户共享一个桶）；配多了，
面板会拒绝信任 XFF、回退到直连对端 IP（同样是代理 IP、共享桶），并
在日志里告警提示检查配置。两种配错都不会取到攻击者伪造的条目。启动
日志会打印生效值，请核对它与你的实际拓扑一致。面板直连公网（无代理）时忽略 XFF。

限流的具体阈值（固定值，不在设置里开放调整）：

| 维度 | 阈值 | 说明 |
|---|---|---|
| 来源 IP | 10 次 / 分钟 | 每个 IP 独立计数，超限返回 429 |
| 来源 IP（被收紧后） | 3 次 / 分钟（收紧状态持续 30 分钟） | 某用户名触发下方信号且未启用登录验证码时，该来源 IP 被收紧 |
| 用户名 | 30 次 / 10 分钟 | 只作信号与渐进延迟依据，**永不直接锁账号**（防用错密码把正常用户锁死） |

登录成功签发的会话（JWT）有效期为 **7 天**。

> 密钥轮换提醒：`.env` 里的 `JWT_SECRET` 同时也是个人订阅链接签名（`?user=&sig=`）的密钥。轮换 `JWT_SECRET` 后，所有已登录会话都会失效需重新登录，全部带 `?user=&sig=` 的个人订阅链接也会一并失效，用户需在面板里重新复制链接。

> 子网提示：旧版本部署的容器网络子网是 `172.80.0.0/16`，它不在 RFC1918 私网段内，后端不把该网段当作可信代理来源，经前端进来的登录请求限流键会坍缩成少数共享桶（有效预算翻倍、且可被单点洪泛耗尽）。新安装的模板已改用 `172.30.0.0/16`，**该模板子网只对全新安装生效**：1.3.4 起两条升级路径（脚本更新与面板内升级）都会把存量 `docker-compose.yml` 里在用的子网与网络名原样保留进新文件，不会因为升级模板而重建容器网络。**存量部署也不要手工改在用子网**（会重建容器网络、容器 IP 全变）；在意限流桶问题的存量部署，可改用宿主机 Caddy / nginx 直接反代后端、或等待后续版本的迁移指引。

### 登录验证码（Cloudflare Turnstile）

在 **网站配置 → 安全** 里可以开启登录验证码：填入 Cloudflare Turnstile 的 Site Key 与 Secret Key 后，登录页会出现人机验证，登录请求必须携带有效验证结果才会被处理。未配置密钥时该功能不启用、登录流程与原来一致。Turnstile 密钥只存面板配置、仅管理员可见；它同时影响上文的限流收紧逻辑（启用验证码后，用户名信号不再触发 IP 收紧）。

升级见 [升级](./upgrade.md)。
