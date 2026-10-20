# 升级

## 面板内升级

**网站配置** → **面板自升级** → **检查更新** → **立即升级**。

升级会拉取新镜像并重建前后端容器，约 1 分钟后刷新页面即可。页面没有变化时，通常是浏览器或 CDN 缓存，刷新或清理缓存即可。

## 脚本升级

在面板部署目录（存放 `docker-compose.yml` 和 `.env` 的目录）重新运行安装脚本，选 `2. 更新面板`：

```bash
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh -o panel_install.sh && chmod +x panel_install.sh && ./panel_install.sh
```

数据会保留，更新前会自动备份到部署目录的 `backups/`。

## 指定版本

到 [Releases](https://github.com/iiiiiiiii7125-art/flmx-max/releases) 复制对应版本的命令，例如：

```bash
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/download/1.0.1/panel_install.sh -o panel_install.sh && chmod +x panel_install.sh && ./panel_install.sh
```

## 节点 agent 升级

**节点** 页选择节点 → **升级**，留空版本即使用最新版。也可以在节点上重新运行节点安装脚本。

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
