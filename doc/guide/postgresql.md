# PostgreSQL 数据库指南

FLMX 默认使用 SQLite 作为数据库，同时支持 PostgreSQL：表结构与 R5-B4 端口/用户名唯一索引（含 node_inbound 部分唯一索引）在启动时对两种数据库同等建立，迁移状态接口的报告对 PostgreSQL 同样有效。本文档介绍如何使用 PostgreSQL 部署面板、从 SQLite 迁移以及日常维护。

已知限制（与 SQLite 部署相同或特有）：

- 面板当前按**单实例**部署设计：端口分配等进程内锁不跨实例协调，PostgreSQL 部署同样不要多实例并行运行面板。
- PostgreSQL 侧的自动化回归覆盖小于 SQLite（CI 的 PostgreSQL 契约测试为 `TestPostgres*` 一组，共 6 个，见 `.github/workflows/ci-build.yml`），升级前建议先在测试环境验证备份恢复与订阅下发。

## 一、SQLite 与 PostgreSQL 对比

| 特性 | SQLite | PostgreSQL |
|------|--------|------------|
| **部署复杂度** | 零配置，开箱即用 | 需要额外的数据库服务 |
| **并发性能** | 适合小规模单机使用 | 支持高并发读写 |
| **数据规模** | 适合中小规模数据 | 适合大规模数据 |
| **备份与恢复** | 直接复制文件 | 支持逻辑备份与物理备份 |
| **高可用** | 不支持 | 支持主从复制、流复制 |

**建议**：如果你只是个人使用或小团队使用，SQLite 完全够用。如果节点多，推荐使用 PostgreSQL。

---

## 二、环境变量说明

以下环境变量用于配置数据库连接，在 `.env` 文件或 Docker Compose `environment` 中设置。

### 后端服务 (backend) 使用

| 变量名 | 说明 | 默认值 | 示例 |
|--------|------|--------|------|
| `DB_TYPE` | 数据库类型，`sqlite` 或 `postgres` | `sqlite` | `postgres` |
| `DATABASE_URL` | PostgreSQL 连接字符串（仅 `DB_TYPE=postgres` 时必填） | 空 | `postgres://flux_panel:密码@postgres:5432/flux_panel?sslmode=disable` |
| `DB_PATH` | SQLite 数据库文件路径（仅 `DB_TYPE=sqlite` 时使用） | `/app/data/gost.db` | `/app/data/gost.db` |

### PostgreSQL 容器使用

| 变量名 | 说明 | 默认值 |
|--------|------|--------|
| `POSTGRES_DB` | 数据库名称 | `flux_panel` |
| `POSTGRES_USER` | 数据库用户名 | `flux_panel` |
| `POSTGRES_PASSWORD` | 数据库密码 | `flux_panel_change_me` |

> ⚠️ **安全提示**：生产环境中请务必修改 `POSTGRES_PASSWORD` 为强密码，不要使用默认值！

> ⛔ **拒绝启动（1.2.0 起）**：后端启动时会校验口令——`POSTGRES_PASSWORD` 为空、等于默认值 `flux_panel_change_me` 或等于 `postgres` 时，后端**直接拒绝启动**并在日志里报错。上表「默认值」只是 compose 模板里的占位值，不能原样用于实际部署。另外，后端只校验 `DATABASE_URL` 里的口令、不会替你比对两者是否一致，`DATABASE_URL` 与 `POSTGRES_PASSWORD` 不一致时表现为数据库连接认证失败（28P01）。脚本一键迁移时若发现 `.env` 里的口令为空或仍是默认值，会自动生成随机口令并写回 `.env`。

---

## 三、全新部署（Docker Compose + PostgreSQL）

安装脚本会根据环境自动下载对应的 Compose 配置并保存为 `docker-compose.yml`。默认使用 SQLite，只需配置环境变量即可切换到 PostgreSQL。

### 1. 创建 `.env` 文件

在 `docker-compose` 同目录创建 `.env` 文件：

```bash
# 基础配置
JWT_SECRET=替换为你的密钥
BACKEND_PORT=6365
FRONTEND_PORT=6366

# PostgreSQL 配置
DB_TYPE=postgres
DATABASE_URL=postgres://flux_panel:替换为强密码@postgres:5432/flux_panel?sslmode=disable

POSTGRES_DB=flux_panel
POSTGRES_USER=flux_panel
POSTGRES_PASSWORD=替换为强密码
```

> ⚠️ `DATABASE_URL` 中的密码必须与 `POSTGRES_PASSWORD` 保持一致。

### 2. 启动服务

```bash
docker compose up -d
```

### 3. 验证

```bash
# 检查所有容器是否正常运行
docker ps

# 查看后端日志，确认连接 PostgreSQL 成功
docker logs flux-panel-backend

# 查看 PostgreSQL 容器状态
docker logs flux-panel-postgres
```

---

## 四、从 SQLite 迁移到 PostgreSQL

如果你已经在使用 SQLite 并且希望迁移到 PostgreSQL，请按照以下步骤操作。

### 快速方式：脚本菜单一键迁移（推荐）

如果你是通过安装脚本部署面板，可直接执行：

```bash
./panel_install.sh
# 选择 4. 迁移到 PostgreSQL
```

脚本会自动完成以下操作：
- 备份 SQLite 数据到当前目录（`gost.db.bak`）
- 迁移前把 `.env` 快照到 `.env.pre-postgres-migration`
- 口令为空或仍是默认值时，生成随机口令并写入 `.env`（后端对空/默认口令拒绝启动）
- 启动并等待 PostgreSQL 健康检查通过
- 使用 `pgloader` 导入 SQLite 数据（导入前自动处理下方 `release_legacy_family` 兼容问题）
- 自动写入 `.env` 的 `DB_TYPE=postgres` 与 `DATABASE_URL`
- 重启服务并等待后端健康检查；**若启动失败，自动回滚 `.env` 并恢复迁移前的 SQLite 服务**，不会把面板留在坏配置上

### 手动方式：按步骤迁移

### 1. 备份 SQLite 数据

```bash
# 停止所有服务
docker compose down

# 备份 SQLite 数据文件到当前目录
docker run --rm -v sqlite_data:/data -v "$(pwd)":/backup alpine sh -c "cp /data/gost.db /backup/gost.db.bak"
```

### 2. 配置 PostgreSQL 环境变量

在 `.env` 文件中添加 PostgreSQL 配置（参考上方"环境变量说明"）。

### 3. 仅启动 PostgreSQL

```bash
docker compose up -d postgres
```

等待 PostgreSQL 完全就绪：

```bash
# 检查 PostgreSQL 健康状态
docker inspect --format='{{.State.Health.Status}}' flux-panel-postgres
# 输出 "healthy" 表示就绪
```

### 4. 使用 pgloader 迁移数据

```bash
source .env
docker run --rm \
  --network gost-network \
  -v sqlite_data:/sqlite \
  dimitri/pgloader:latest \
  pgloader /sqlite/gost.db "postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}"
```

> 📌 建议直接从 `.env` 读取 `POSTGRES_USER`、`POSTGRES_PASSWORD`、`POSTGRES_DB`，避免手填密码导致认证失败。

#### 迁移被 `release_legacy_family` 堵住时（1.3.2 及更早版本的库）

1.3.2 及更早版本的 SQLite 库里，`peer_share_resource` 表的 `release_legacy_family` 列是 `numeric` 类型、默认值 `false`。pgloader 按 numeric 翻译该默认值时生成 `default 'false'`，PostgreSQL 拒绝建表并报：

```
ERROR Database error 22P02: invalid input syntax for type numeric: "false"
FATAL Failed to create the schema
```

整条迁移会在建表阶段失败、一行数据都搬不过去（全库仅此一列有此问题）。脚本菜单的一键迁移已自动处理，手动迁移时先把库 dump 出来、只改这一列的默认值再重建一份副本，用副本迁移：

```bash
docker run --rm -v sqlite_data:/sqlite alpine sh -c '
  apk add --no-cache sqlite >/dev/null 2>&1
  sqlite3 /sqlite/gost.db .dump > /tmp/dump.sql
  sed "/CREATE TABLE.*peer_share_resource/,/);/ s/DEFAULT false/DEFAULT 0/g" /tmp/dump.sql > /tmp/dump-fixed.sql
  rm -f /sqlite/gost-migrate.db
  sqlite3 /sqlite/gost-migrate.db < /tmp/dump-fixed.sql
'
# 然后把 pgloader 的源换成副本：
# pgloader /sqlite/gost-migrate.db "postgresql://..."
```

适用范围：1.3.2 及更早版本创建的 SQLite 库。后续版本模型侧修复后该列默认值不再是 `false`，上述预处理替换不命中、可跳过；对已修复的库执行也无害（生成一份等价副本而已）。（在 pgloader 的 load 文件里给该列写 CAST 规则是等价的另一种变通，二选一即可。）

### 5. 启动全部服务

```bash
source .env
export DB_TYPE=postgres
export DATABASE_URL="postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}?sslmode=disable"
docker compose up -d
```

### 6. 验证迁移

登录面板后，检查以下数据是否完整：
- 用户列表和权限
- 节点信息和状态
- 隧道配置
- 转发规则
- 流量统计数据

---

## 五、独立 PostgreSQL（非 Docker）

如果你不想使用 Docker Compose 中自带的 PostgreSQL 容器，也可以连接外部的 PostgreSQL 实例。

### 1. 准备 PostgreSQL

在目标 PostgreSQL 服务器上创建数据库和用户：

```sql
CREATE USER flux_panel WITH PASSWORD '你的强密码';
CREATE DATABASE flux_panel OWNER flux_panel;
```

### 2. 配置连接

修改 `.env` 文件，将 `DATABASE_URL` 指向外部 PostgreSQL：

```bash
DB_TYPE=postgres
DATABASE_URL=postgres://flux_panel:你的强密码@数据库地址:5432/flux_panel?sslmode=disable
```

> 📌 如果 PostgreSQL 在远程服务器且启用了 SSL，请将 `sslmode=disable` 改为 `sslmode=require` 或 `sslmode=verify-full`。

### 3. 停用内置 PostgreSQL 容器（可选）

如果使用外部 PostgreSQL，可以在启动时不启动内置的 postgres 服务：

```bash
docker compose up -d backend frontend
```

---

## 六、数据备份与恢复

### 逻辑备份（pg_dump）

```bash
# 备份（在 Docker 环境下）
docker exec flux-panel-postgres pg_dump -U flux_panel flux_panel > backup_$(date +%Y%m%d_%H%M%S).sql

# 恢复
docker exec -i flux-panel-postgres psql -U flux_panel flux_panel < backup_20260101_120000.sql
```

### 定时备份（cron）

创建备份脚本 `/opt/flmx/backup.sh`：

```bash
#!/bin/bash
BACKUP_DIR="/opt/flmx/backups"
mkdir -p "$BACKUP_DIR"
docker exec flux-panel-postgres pg_dump -U flux_panel flux_panel | gzip > "$BACKUP_DIR/flmx_$(date +%Y%m%d_%H%M%S).sql.gz"
# 清理 30 天前的备份
find "$BACKUP_DIR" -name "flmx_*.sql.gz" -mtime +30 -delete
```

添加 cron 任务（每天凌晨 3 点执行）：

```bash
chmod +x /opt/flmx/backup.sh
echo "0 3 * * * /opt/flmx/backup.sh" | crontab -
```

---

## 七、常见问题

### Q: 切换到 PostgreSQL 后启动失败，提示连接被拒绝？

**A**: 
1. 确认 PostgreSQL 容器已启动并处于 `healthy` 状态：`docker ps`。
2. 确认 `DATABASE_URL` 中的主机名、端口、用户名、密码正确。
3. 在 Docker Compose 环境下，主机名应为 `postgres`（服务名），而非 `localhost`。

### Q: pgloader 迁移时报错？

**A**: 
1. 确认 PostgreSQL 容器已完全就绪（状态为 `healthy`）。
2. 确认 `--network gost-network` 参数正确，使 pgloader 容器与 PostgreSQL 在同一网络中。
3. 如果数据库已有表结构，pgloader 可能会报冲突。可以先清空目标数据库后重试。
4. 若报 `invalid input syntax for type numeric: "false"`，是 1.3.2 及更早版本库的已知兼容问题，见上方「迁移被 `release_legacy_family` 堵住时」。

### Q: 如何查看当前使用的数据库类型？

**A**: 查看后端容器的 `DB_TYPE` 环境变量：

```bash
docker exec flux-panel-backend printenv DB_TYPE
```

### Q: 可以同时使用 SQLite 和 PostgreSQL 吗？

**A**: 不可以。`DB_TYPE` 只能设置为 `sqlite` 或 `postgres` 之一。后端启动时根据此配置连接对应的数据库。

### Q: PostgreSQL 数据存储在哪里？

**A**: 在 Docker Compose 部署中，PostgreSQL 数据存储在名为 `postgres_data` 的 Docker Volume 中。可以通过以下命令查看：

```bash
docker volume inspect postgres_data
```
