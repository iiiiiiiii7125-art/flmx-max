# PostgreSQL 数据库指南

FLMX 默认使用 SQLite 作为数据库，同时支持 PostgreSQL：表结构与 R5-B4 端口/用户名唯一索引（含 node_inbound 部分唯一索引）在启动时对两种数据库同等建立，迁移状态接口的报告对 PostgreSQL 同样有效。本文档介绍如何使用 PostgreSQL 部署面板、从 SQLite 迁移以及日常维护。

已知限制（与 SQLite 部署相同或特有）：

- 面板当前按**单实例**部署设计：端口分配等进程内锁不跨实例协调，PostgreSQL 部署同样不要多实例并行运行面板。
- PostgreSQL 侧的自动化回归覆盖小于 SQLite（CI 的 PostgreSQL 契约测试为 `TestPostgres*` 一组，共 10 个，见 `.github/workflows/ci-build.yml`；契约测试所用的 PostgreSQL 主版本与随版 compose 部署一致，均为 16），升级前建议先在测试环境验证备份恢复与订阅下发。<!-- [K11-COUNT:testpostgres=10] 本计数由 scripts/check-doc-consistency.sh 第 10 项与代码机械对账，增删 TestPostgres* 用例须同步更新数字与标记 -->

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
| `DB_TYPE` | 数据库类型：`sqlite` 或 `postgres`（另接受别名 `postgresql`，留空等同 `sqlite`；填其他值启动时报错） | `sqlite` | `postgres` |
| `DATABASE_URL` | PostgreSQL 连接字符串（仅 `DB_TYPE=postgres` 时必填） | 空 | `postgres://flmx_panel:密码@postgres:5432/flmx_panel?sslmode=disable` |
| `DB_PATH` | SQLite 数据库文件路径（仅 `DB_TYPE=sqlite` 时使用） | `/app/data/gost.db` | `/app/data/gost.db` |

### PostgreSQL 容器使用

| 变量名 | 说明 | 默认值 |
|--------|------|--------|
| `POSTGRES_DB` | 数据库名称 | `flmx_panel` |
| `POSTGRES_USER` | 数据库用户名 | `flmx_panel` |
| `POSTGRES_PASSWORD` | 数据库密码 | `flmx_panel_change_me` |

> ⚠️ **安全提示**：生产环境中请务必修改 `POSTGRES_PASSWORD` 为强密码，不要使用默认值！

> ⛔ **拒绝启动（1.2.0 起）**：后端启动时校验的是 **`DATABASE_URL` 里内嵌的口令**——该口令为空、等于占位默认值 `flmx_panel_change_me`（改名前的旧占位值 `flux_panel_change_me` 同样被拒）或等于 `postgres` 时，后端**直接拒绝启动**并在日志里报错。后端从不读取 `POSTGRES_PASSWORD` 环境变量本身：只改 `POSTGRES_PASSWORD`、不改 `DATABASE_URL` 里的口令，照样会被拒绝启动，排障时请先核对 `DATABASE_URL`。上表「默认值」只是 compose 模板里的占位值，不能原样用于实际部署。另外，后端不会替你比对两者是否一致，`DATABASE_URL` 与 `POSTGRES_PASSWORD` 不一致时表现为数据库连接认证失败（28P01）。脚本一键迁移时若发现 `.env` 里的口令为空或仍是默认值，会自动生成随机口令并写回 `.env`（同时写入 `DATABASE_URL`）。

> 📌 **改名兼容**：改名前部署的存量 PostgreSQL，库名/用户名在数据卷初始化时已固化为 `flux_panel`，不随本次改名迁移、仍按原名连接；本文各命令中的 `flmx_panel` 对存量部署请按 `.env` 与 `DATABASE_URL` 的实际值（`flux_panel`）替换，新部署用 `flmx_panel`。

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
DATABASE_URL=postgres://flmx_panel:替换为强密码@postgres:5432/flmx_panel?sslmode=disable

POSTGRES_DB=flmx_panel
POSTGRES_USER=flmx_panel
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
docker logs flmx-panel-backend

# 查看 PostgreSQL 容器状态
docker logs flmx-panel-postgres
```

---

## 四、从 SQLite 迁移到 PostgreSQL

如果你已经在使用 SQLite 并且希望迁移到 PostgreSQL，请按照以下步骤操作。

### 快速方式：脚本菜单一键迁移（推荐）

如果你是通过安装脚本部署面板，可直接执行。**菜单 4 不做部署定位、先 `cd` 到部署目录再运行**（菜单 2 更新、菜单 3 卸载会自动定位部署，菜单 4 不会）：部署目录就是同时放着 `.env` 与 `docker-compose.yml` 的那个目录（默认安装路径以你实际部署为准）；站错目录运行时脚本只认当前目录的 `.env`，会报「未找到 .env 文件，请先安装面板」——这不是面板没装，是目录不对。自检：运行前先 `ls .env docker-compose.yml`，两个文件都能列出再继续。

```bash
cd <你的面板部署目录>
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

> 📌 **本节（及 §六、§七）命令里的容器名、卷名、网络名一律以部署目录 `.env` 的实际值为准**，不要照抄模板回退名。涉及的键共五个：`PANEL_BACKEND_CONTAINER`、`PANEL_POSTGRES_CONTAINER`、`FLMX_SQLITE_VOLUME`、`FLMX_POSTGRES_VOLUME`、`FLMX_NETWORK`。安装脚本部署的默认值依次是 `flmx-panel-backend`、`flmx-panel-postgres`、`flmx-sqlite-data`、`flmx-postgres-data`、`flmx-network`（多实例部署各名再带 `-<实例 id>` 后缀）。现行模板的回退名已与脚本默认同为 flmx 名；旧模板回退名（`flux-panel-backend`、`flux-panel-postgres`、`sqlite_data`、`postgres_data`、`gost-network`）只在用旧模板且 `.env` 没有写这些键的部署上成立——即改名前按 §三手工自备 `.env` 且未补这些键的情形；脚本安装的部署 `.env` 必然写有这五键，对这类部署照抄任何回退名只会得到 `No such container/volume/network`。自检：在部署目录执行 `grep -E '^(PANEL_BACKEND_CONTAINER|PANEL_POSTGRES_CONTAINER|FLMX_SQLITE_VOLUME|FLMX_POSTGRES_VOLUME|FLMX_NETWORK)=' .env`，五行齐全即以下各步先 `source .env` 再用变量的写法可直接用。

### 1. 备份 SQLite 数据

```bash
# 在部署目录执行，先加载 .env 取卷名（取值来源见上方注记）
source .env

# 停止所有服务
docker compose down

# 备份 SQLite 数据文件到当前目录
docker run --rm -v "$FLMX_SQLITE_VOLUME":/data -v "$(pwd)":/backup alpine sh -c "cp /data/gost.db /backup/gost.db.bak"
```

### 2. 配置 PostgreSQL 环境变量

在 `.env` 文件中添加 PostgreSQL 配置（参考上方"环境变量说明"）。

### 3. 仅启动 PostgreSQL

```bash
docker compose up -d postgres
```

等待 PostgreSQL 完全就绪：

```bash
# 检查 PostgreSQL 健康状态（容器名取 .env 的 PANEL_POSTGRES_CONTAINER）
source .env
docker inspect --format='{{.State.Health.Status}}' "$PANEL_POSTGRES_CONTAINER"
# 输出 "healthy" 表示就绪
```

### 4. 使用 pgloader 迁移数据

**不要把口令拼在 pgloader 的命令行参数里**：含口令的连接串会以明文出现在宿主机的进程列表（`ps`）和 `docker inspect` 输出里，迁移持续的几分钟内可被本机其他进程读到（脚本一键迁移已因此改走命令文件，手动迁移同口径）。把连接串写进一张权限 0600 的 pgloader 命令文件、只把文件路径作为参数传入，用后立即删除：

```bash
source .env
umask 077
cat > /tmp/flmx-migrate.load <<EOF
LOAD DATABASE
     FROM sqlite:///sqlite/gost.db
     INTO postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB};
EOF
docker run --rm \
  --network "$FLMX_NETWORK" \
  -v "$FLMX_SQLITE_VOLUME":/sqlite \
  -v /tmp/flmx-migrate.load:/tmp/flmx-migrate.load:ro \
  dimitri/pgloader:latest \
  pgloader /tmp/flmx-migrate.load
rm -f /tmp/flmx-migrate.load
```

> 📌 命令文件的内容从 `.env` 的 `POSTGRES_USER`、`POSTGRES_PASSWORD`、`POSTGRES_DB` 生成，避免手填密码导致认证失败；`INTO` 行尾的分号是命令文件的语句终止符，不可省略。

#### 迁移被 `release_legacy_family` 堵住时（1.3.2 及更早版本的库）

1.3.2 及更早版本的 SQLite 库里，`peer_share_resource` 表的 `release_legacy_family` 列是 `numeric` 类型、默认值 `false`。pgloader 按 numeric 翻译该默认值时生成 `default 'false'`，PostgreSQL 拒绝建表并报：

```
ERROR Database error 22P02: invalid input syntax for type numeric: "false"
FATAL Failed to create the schema
```

整条迁移会在建表阶段失败、一行数据都搬不过去（全库仅此一列有此问题）。脚本菜单的一键迁移已自动处理，手动迁移时先把库 dump 出来、只改这一列的默认值再重建一份副本，用副本迁移：

```bash
source .env
docker run --rm -v "$FLMX_SQLITE_VOLUME":/sqlite alpine sh -c '
  apk add --no-cache sqlite >/dev/null 2>&1
  sqlite3 /sqlite/gost.db .dump > /tmp/dump.sql
  sed "/CREATE TABLE.*peer_share_resource/,/);/ s/DEFAULT false/DEFAULT 0/g" /tmp/dump.sql > /tmp/dump-fixed.sql
  rm -f /sqlite/gost-migrate.db
  sqlite3 /sqlite/gost-migrate.db < /tmp/dump-fixed.sql
'
# 然后把上面第 4 步命令文件里的 FROM 源换成副本：
# FROM sqlite:///sqlite/gost-migrate.db
```

适用范围：1.3.2 及更早版本创建的 SQLite 库。后续版本模型侧修复后该列默认值不再是 `false`，上述预处理替换不命中、可跳过；对已修复的库执行也无害（生成一份等价副本而已）。（在 pgloader 的 load 文件里给该列写 CAST 规则是等价的另一种变通，二选一即可。）

同一份副本预处理还处理另一处 pgloader 毒源（与上面的默认值替换无关、所有带该索引的库都适用）：`user` 表的函数唯一索引 `idx_user_username_fold_unique`（表达式 `(LOWER("user"))`，1.7.5 起）会让 pgloader 解析目录时 KABOOM（`TYPE-ERROR: NIL is not of type PGLOADER.CATALOG:COLUMN`）。脚本一键迁移会在副本上删掉该索引，并连带删除 `schema_migration` 中版本为 `k10_v5_username_casefold_unique` 的记录——迁移后首次启动时后端会重跑该迁移、在 PostgreSQL 侧补建同名索引并补记版本；手动迁移时须在副本上执行同样两步（`DROP INDEX IF EXISTS idx_user_username_fold_unique;` 与 `DELETE FROM schema_migration WHERE version='k10_v5_username_casefold_unique';`），只删索引不删记录会让 PostgreSQL 侧永久缺失该索引。

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
CREATE USER flmx_panel WITH PASSWORD '你的强密码';
CREATE DATABASE flmx_panel OWNER flmx_panel;
```

### 2. 配置连接

修改 `.env` 文件，将 `DATABASE_URL` 指向外部 PostgreSQL：

```bash
DB_TYPE=postgres
DATABASE_URL=postgres://flmx_panel:你的强密码@数据库地址:5432/flmx_panel?sslmode=disable
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

以下命令在部署目录执行、先 `source .env`，容器名取 `$PANEL_POSTGRES_CONTAINER`（取值来源见 §四手动迁移开头的注记；脚本安装默认 `flmx-panel-postgres`）：

```bash
source .env
# 备份（在 Docker 环境下）
docker exec "$PANEL_POSTGRES_CONTAINER" pg_dump -U flmx_panel flmx_panel > backup_$(date +%Y%m%d_%H%M%S).sql

# 恢复
docker exec -i "$PANEL_POSTGRES_CONTAINER" psql -U flmx_panel flmx_panel < backup_20260101_120000.sql
```

### 定时备份（cron）

先进入**面板部署目录**——即当初运行 `panel_install.sh` 安装 / 更新面板的目录（自检：在该目录 `ls` 应能看到 `docker-compose.yml` 与 `.env`）。不要凭印象写 `/opt/flmx-panel`：那是容器内的挂载点，宿主机部署目录由安装位置决定，装在别处（如家目录）时照抄会把备份落到真实部署树之外。在部署目录下创建备份脚本 `backup.sh`（脚本按自身所在目录定位备份目录，与面板自身的 `backups/` 约定同根）：

```bash
#!/bin/bash
BACKUP_DIR="$(cd "$(dirname "$0")" && pwd)/backups"
mkdir -p "$BACKUP_DIR"
# 容器名以脚本同目录（部署目录）.env 的 PANEL_POSTGRES_CONTAINER 为准，缺该键时回退模板名
PG_CONTAINER=$(sed -n 's/^PANEL_POSTGRES_CONTAINER=//p' "$(dirname "$0")/.env" | tail -n 1)
docker exec "${PG_CONTAINER:-flmx-panel-postgres}" pg_dump -U flmx_panel flmx_panel | gzip > "$BACKUP_DIR/flmx_$(date +%Y%m%d_%H%M%S).sql.gz"
# 清理 30 天前的备份
find "$BACKUP_DIR" -name "flmx_*.sql.gz" -mtime +30 -delete
```

添加 cron 任务（每天凌晨 3 点执行；把 `<部署目录>` 换成部署目录的实际绝对路径）：

```bash
chmod +x <部署目录>/backup.sh
echo "0 3 * * * <部署目录>/backup.sh" | crontab -
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
2. 确认 `--network` 参数填的是部署 `.env` 里 `FLMX_NETWORK` 的值（脚本安装默认 `flmx-network`；只有 `.env` 未写该键的手工部署才是 `gost-network`），使 pgloader 容器与 PostgreSQL 在同一网络中。
3. 如果数据库已有表结构，pgloader 可能会报冲突。可以先清空目标数据库后重试。
4. 若报 `invalid input syntax for type numeric: "false"`，是 1.3.2 及更早版本库的已知兼容问题，见上方「迁移被 `release_legacy_family` 堵住时」。

### Q: 如何查看当前使用的数据库类型？

**A**: 查看后端容器的 `DB_TYPE` 环境变量（容器名取部署 `.env` 的 `PANEL_BACKEND_CONTAINER`，脚本安装默认 `flmx-panel-backend`）：

```bash
source .env
docker exec "$PANEL_BACKEND_CONTAINER" printenv DB_TYPE
```

### Q: 可以同时使用 SQLite 和 PostgreSQL 吗？

**A**: 不可以。`DB_TYPE` 取 `sqlite` 或 `postgres`（`postgresql` 是 `postgres` 的别名，留空等同 `sqlite`；填其他值后端会报错拒绝启动）。后端启动时根据此配置连接对应的数据库。

### Q: PostgreSQL 数据存储在哪里？

**A**: 在 Docker Compose 部署中，PostgreSQL 数据存储在一个 Docker Volume 中，卷名以部署 `.env` 的 `FLMX_POSTGRES_VOLUME` 为准（脚本安装默认 `flmx-postgres-data`，多实例部署带实例后缀；只有 `.env` 未写该键的手工部署才回退为 `postgres_data`）。可以通过以下命令查看：

```bash
source .env
docker volume inspect "$FLMX_POSTGRES_VOLUME"
```
