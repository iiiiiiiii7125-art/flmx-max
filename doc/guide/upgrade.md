# 升级

## 面板内升级

**推荐升级顺序：先备份 → 先停 agent（节点侧）→ 换面板 → 再启 agent。** 该顺序同样适用于下面的脚本升级：升级前先备份（网站配置 → 数据备份与恢复 → 下载备份；脚本升级会在更新前自动备份到部署目录的 `backups/`），再到每个节点上停掉 agent（`systemctl stop flux_agent`），换完面板后重新启动 agent（`systemctl start flux_agent`）。不停 agent 直接换面板时，在线 agent 会在面板重建期间重试/重连（有重连对账兜底），但面板与 agent 版本会短暂不一致。

**网站配置** → **面板自升级** → **检查更新** → **立即升级**。

升级会拉取新镜像并重建前后端容器，约 1 分钟后刷新页面即可。页面没有变化时，通常是浏览器或 CDN 缓存，刷新或清理缓存即可。

## 脚本升级

在面板部署目录（存放 `docker-compose.yml` 和 `.env` 的目录）重新运行安装脚本，选 `2. 更新面板`：

```bash
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh -o panel_install.sh && curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh.sha256 -o panel_install.sh.sha256 && sha256sum -c panel_install.sh.sha256 && chmod +x panel_install.sh && ./panel_install.sh
```

数据会保留，更新前会自动备份到部署目录的 `backups/`。

> 升级本身不会更换 `JWT_SECRET`；若升级时手工轮换了 `.env` 的 `JWT_SECRET`，所有已登录会话都会失效需重新登录，全部带 `?user=&sig=` 的个人订阅链接也会一并失效（签名用的是同一密钥），用户需在面板里重新复制链接。

> 升级不会改变在用的容器网络：脚本更新与面板内升级都会保留存量 `docker-compose.yml` 的子网与网络名，模板里的新子网（`172.30.0.0/16`）只对全新安装生效，见 [安装部署的子网提示](./install.md)。

脚本更新按版本感知执行：默认更新到最新正式版（测试版不参与默认更新）；需要定版时用 `VERSION=<版本号> ./panel_install.sh` 运行并选 `2`，会按指定版本更新。想跟进测试版时，用 `FLMX_RELEASE_CHANNEL=dev`（或 `beta`）再跑脚本，或显式 `VERSION=<测试版号>`。目标版本低于当前版本时会被**拒绝**（防降级，与面板内升级口径一致），同版本则按重装继续。当前版本以**后端容器实际运行的镜像版本**为准交叉核对：`.env` 记录与实际运行版本不一致时脚本会打印告警并按实际版本判定（取不到实际版本时才回退 `.env` 口径），防止 `.env` 被手改或停留在失败升级的目标版本时把降级误放行。

## 指定版本

到 [Releases](https://github.com/iiiiiiiii7125-art/flmx-max/releases) 复制对应版本的命令，例如：

```bash
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/download/1.0.1/panel_install.sh -o panel_install.sh && curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/download/1.0.1/panel_install.sh.sha256 -o panel_install.sh.sha256 && sha256sum -c panel_install.sh.sha256 && chmod +x panel_install.sh && ./panel_install.sh
```

## 降级到旧版本（手动）

脚本与面板内升级都拒绝降级；确需手动回退到旧版本时注意：如果用过 Shadowsocks「按订阅计量」（1.4.2 起），**回退前必须先关闭全部入站的「按订阅计量」开关，并确认计量专用入口已消失（入站列表不再显示「计量中」徽标、对应端口已释放）再回退**。旧版本不认识计量专用入口，会把它当作普通 Shadowsocks 入站对外分发，其凭据可被直接使用且不计量。

## 节点 agent 升级

**节点** 页选择节点 → **升级**，留空版本即使用最新版。也可以在节点上重新运行节点安装脚本。通过脚本更新同样防降级：目标版本低于节点现装版本时脚本会拒绝更新并说明（如确需降级请手动处理），同版本按重装继续，版本不可比时放行。

> 节点升级与面板自升级的同版本口径不同（有意差异）：节点目标版本与节点当前版本相同时，面板直接提示「已是最新版本」、不下发升级、不重启 agent；而面板自升级（含脚本更新）指定同版本会按**重装**继续执行，用于修复面板自身文件损坏等场景。两处都拒绝降级。

1.3.4 起面板内升级与 init 系统无关：systemd 与 OpenRC（Alpine）节点都走各自的服务管理器，不再硬编码 systemd 命令；升级会先给新二进制做 `-V` 冒烟、备份现二进制（备份写失败直接中止、不裸替换），重启后本地健康检查不过会自动回滚到备份，回滚在节点本地闭环、不依赖与面板的连接，结果记在节点上的 `agent-upgrade.log`。

> 无 TLS 面板 + 节点 `addr` 为裸地址的部署，agent 升级重启后可能连不上（不再自动降级明文），升级前先看 [节点连不上：地址写法与时钟](./install.md#节点连不上-地址写法与时钟-1-3-4-起)。联邦双方还要注意 [升级顺序](./relay.md#联邦-面板共享-升级顺序-先消费方-后提供方)。

## 修复面板内升级

面板内升级提示“部署docker-compose.yml不可用”时：3.0.6 及更早版本的面板内升级会把后端容器的部署目录挂载弄丢，之后面板内升级会一直提示这个错误。（注：3.0.x 指本项目上游的旧版本线；现行 1.0.x 版本均晚于该节点，全新安装的 1.0.x 不受影响，本节只针对从旧版本线一路升级上来的存量部署。）数据不受影响，修复一次即可：

1. 在服务器上进入存放 `docker-compose.yml` 和 `.env` 的目录（常见为 `/root`，或当初运行安装脚本的目录），用 `ls` 确认这两个文件都在
2. 执行：

   ```bash
   docker compose up -d --force-recreate backend frontend
   ```

也可以在该目录运行 3.0.7 及以后的安装脚本（现行 1.0.x 的安装脚本同样适用），选“更新面板”，会顺带修复。

## 从旧版本升级（含 R5-B4 并发修复）

### 1. 升级前备份

```bash
docker ps --format '{{.Names}}' | grep -i backend
docker exec <backend容器> cp /app/data/gost.db /app/data/gost.db.bak
docker cp <backend容器>:/app/data/gost.db.bak /tmp/gost.db.bak
sqlite3 /tmp/gost.db.bak "PRAGMA integrity_check;"
```

### 2. 检查重复/孤儿数据

```bash
sqlite3 /tmp/gost.db.bak << 'SQLEOF'
.headers on
.mode column
SELECT node_id, port, COUNT(*) FROM forward_port GROUP BY node_id, port HAVING COUNT(*) > 1;
SELECT fp.node_id, fp.port FROM forward_port fp LEFT JOIN "forward" f ON f.id = fp.forward_id WHERE f.id IS NULL;
SELECT node_id, port, COUNT(*) FROM chain_tunnel WHERE port > 0 GROUP BY node_id, port HAVING COUNT(*) > 1;
SELECT node_id, port, COUNT(*) FROM node_inbound WHERE kernel_id > 0 GROUP BY node_id, port HAVING COUNT(*) > 1;
SELECT "user", COUNT(*) FROM "user" GROUP BY "user" HAVING COUNT(*) > 1;
SQLEOF
```

> 以上 SQL 中的 4 条重复检查与代码启动预检逻辑（`runV1Precheck`）一致；`forward_port` 孤儿检查不在启动预检内，只包含在 `scripts/phase_a_dryrun.sql` 的 dry-run 清单中。

### 3. 清理步骤

| 类型 | 保留 | 依据 |
|------|------|------|
| forward_port 重复 | status=1 的行；都有则保留 forward_id 最小 | 活跃优先 |
| forward_port 孤儿 | 删除 | 转发已不存在 |
| chain_tunnel 重复 | tunnel_id 最小 | 先建优先 |
| node_inbound 重复（仅 kernel_id > 0 的内核入站） | enabled=1；都有则 id 最小 | 启用优先；导入节点（kernel_id=0）同端口不同主机是合法数据，不要清理 |
| 用户名重复 | 最早创建未停用；其余改名加 `_dup` | 不删账号 |

### 4. 触发迁移

```bash
docker restart <backend容器>
docker logs <backend容器> 2>&1 | grep -i migration
```

### 5. 索引未创建时的处理

若日志显示 `PRECHECK FAILED`：面板正常启动，按 §2 重新检查清理后重启即可（迁移幂等，会重试）。

### 6. 回滚

```bash
sqlite3 /app/data/gost.db "DROP INDEX IF EXISTS idx_forward_port_node_port;"
sqlite3 /app/data/gost.db "DROP INDEX IF EXISTS idx_chain_tunnel_node_port;"
sqlite3 /app/data/gost.db "DROP INDEX IF EXISTS idx_node_inbound_node_port;"
sqlite3 /app/data/gost.db "DROP INDEX IF EXISTS idx_user_username_unique;"
```

### 7. 停用用户名释放

`POST /api/v1/user/update` 将停用用户改名后，新用户可注册原用户名（`user_id` 不变）。

## GitHub 镜像代理

面板内置了一个 GitHub 文件代理，地址是 `/api/v1/open_api/gh/`（面板域名后拼接，例如 `https://你的面板域名/api/v1/open_api/gh/...`），用于加速从 GitHub 下载内核、规则集等文件。节点安装内核时可以选择走这个代理，尤其适合 GitHub 直连慢的机器。

只代理白名单内的仓库：

| 仓库 | 用途 |
|------|------|
| 本面板仓库 | 面板/节点安装脚本、Release 资产 |
| `xtls/xray-core` | Xray 内核 |
| `sagernet/sing-box` | sing-box 内核 |
| `ihciah/shadow-tls` | ShadowTLS |
| `v2fly/geoip`、`v2fly/domain-list-community` | 路由规则数据 |
| `sagernet/sing-geosite`、`sagernet/sing-geoip` | sing-box 规则数据 |
| `metacubex/meta-rules-dat` | Mihomo 规则数据 |
| `blackmatrix7/ios_rule_script` | 分流规则脚本 |

- 单个文件上限 300MB，总缓存上限 2GB，缓存目录随数据库路径自动推导
- release tag 的文件永久缓存；`latest`、raw 文件和 API 响应按 TTL 刷新
- GitHub 不可达时返回已缓存的旧副本，不会直接报错

### 版本发现的出站安全策略

内核版本列表、节点升级版本列表与面板自升级版本发现（即系统设置里「开启 GitHub 加速」走的版本查询）与订阅源共用同一套出站安全策略：重定向逐跳复检，云元数据地址（`169.254.169.254`）在任何配置下都被拒绝，私网/回环/链路本地目标默认拒绝。

- 用内网代理或本地代理（如 `http://127.0.0.1:8080` 的 gh-proxy）做加速地址时，须显式放行，否则版本列表会报错：
  - `FLMX_FETCH_PRIVATE_ALLOWLIST=127.0.0.1/32`：仅放行指定 CIDR（推荐）
  - `FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true`：放行所有私网地址（不推荐）
- 公网加速地址（如默认的 `https://gcode.hostcentral.cc`）不受影响，无需配置。
