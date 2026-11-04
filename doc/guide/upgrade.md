# 升级

## 面板内升级

**推荐升级顺序：先备份 → 先停 agent（节点侧）→ 换面板 → 再启 agent。** 该顺序同样适用于下面的脚本升级：升级前先备份（网站配置 → 数据备份与恢复 → 下载备份；脚本升级会在更新前自动备份到部署目录的 `backups/`），再到每个节点上停掉 agent（systemd 节点：`systemctl stop flmx_agent`；Alpine / OpenRC 节点：`rc-service flmx_agent stop`；改名前安装、尚未迁移布局的节点服务名仍为 `flux_agent`（布局迁移只在节点机上重跑安装命令或脚本更新时发生，面板内的节点升级只换二进制、不迁布局）），换完面板后重新启动 agent（`systemctl start flmx_agent` / `rc-service flmx_agent start`）。不停 agent 直接换面板时，在线 agent 会在面板重建期间重试/重连（有重连对账兜底），但面板与 agent 版本会短暂不一致。

**网站配置** → **面板自升级** → **检查更新** → **立即升级**。

升级会拉取新镜像并重建前后端容器，约 1 分钟后刷新页面即可。页面没有变化时，通常是浏览器或 CDN 缓存，刷新或清理缓存即可。

### 面板自升级的下载校验（跨线路第二锚）

面板自升级经面板下载的资产（compose 文件）要过两道校验；前后端镜像不经面板下载、由 `docker compose pull` 从镜像仓库拉取，靠仓库的摘要（digest）机制保证完整性，不在双锚覆盖内。脚本升级同口径（见本节末尾）：

- **第一锚（同源校验）**：资产与它的 `.sha256` 校验文件从**同一条线路、同一次下载**取得并比对，哈希对不上直接拒绝该线路。只靠这一锚有个缺口：某条线路若被污染，资产与校验文件会被一起替换、自洽地通过校验。
- **第二锚（跨线路复核）**：某条线路胜出定版后，面板再从候选线路里**另一条不同的线路**重新取一次同一资产的 `.sha256`，与第一锚采信的校验值比对。两条线路独立给出同一个校验值，才算复核通过。
- **compose 落盘前内容校验**：下载到的 `docker-compose.yml` 在覆盖现有文件之前还要过一道内容校验——必须是合法 YAML、根为映射、含 `services` 且包含预期的 `backend` 与 `frontend` 两个服务；任一不合即中止升级，**旧 compose 文件保持不动**，不会把坏文件落盘后拿宿主 Docker 权限执行。

撞见下面两种回执时的含义与处置：

- **「校验文件经两条不同线路获取的结果不一致，已中止升级（…第二锚）」**：两条线路给出的校验值互相矛盾，说明至少一条线路上的内容不可信（镜像被篡改、缓存污染或同步错位都有可能）。此时升级被**硬中止、不换路重试**，面板保持原版本运行、不会进入拉镜像 / 重建步骤。处置：不要反复立即重试、更不要想办法绕过校验；先等一段时间再试（线路缓存同步后可能自行恢复），若多次重试仍在同一资产上中止，把回执里的两个哈希值留存下来排查线路，不要继续升级。
- **回执点名「跨线路锚复核已降级」**：其他线路都没能给出合法校验值（线路不可达、校验文件取不到等），没有第二条线路可复核。此时**不中止**，按第一锚（同源校验）的结果继续升级，回执里会以「跨线路锚复核已降级：升级校验锚复核不可用（其他线路均未给出校验值），按单锚继续」明确点名。含义是这次升级只经过单锚校验、不是升级失败；可以正常完成，介意时可在网络恢复后留意后续升级是否仍点名降级——长期每次都降级说明候选线路整体可达性有问题，需要排查出站网络。

**脚本侧同口径**：`panel_install.sh` 的首装、菜单更新与自更新三条链下载资产时同样执行跨线路复核（脚本内函数 `corroborate_download_anchor`）：异路给出不同哈希时在 stderr 报「跨线路锚复核失败：校验文件经两条不同线路获取的结果不一致，已中止（L8-F-02 第二锚）」，**删除已下载文件并整单中止**；无异路可复核时在 stderr 点名「跨线路锚复核已降级：其他线路均未给出校验值，按单锚继续（L8-F-02）」后继续。整个复核一轮共用 20 秒总界，不会显著拉长升级时间。唯一例外是运维显式设置 `FLMX_VERIFY_DOWNLOADS=0` 时校验被整体关闭、第二锚随第一锚一并跳过——这是显式逃生口，不是静默降级，不要在日常升级里设置它。

> 节点 agent 升级侧有同一套第二锚口径，其期望哈希由面板预热下发，见下文「GitHub 镜像代理（面板源分发）」的「升级完整性」一条。

## 脚本升级

在面板部署目录（存放 `docker-compose.yml` 和 `.env` 的目录）重新运行安装脚本，选 `2. 更新面板`：

```bash
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh -o panel_install.sh && curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh.sha256 -o panel_install.sh.sha256 && sha256sum -c panel_install.sh.sha256 && chmod +x panel_install.sh && ./panel_install.sh
```

> 校验口径提示：上面这条（及下文「指定版本」一节的）手抄命令里，脚本与校验文件经**同一条线路**取得，只过第一锚（同源校验、单锚）；脚本运行起来之后的更新下载则按上文口径执行第二锚（跨线路复核、双锚）。手抄的第一跳只有单锚，介意这一跳线路可信度时，可参照 README 的跨线路取法、让脚本与校验文件分走两条线路取得后再比对。

数据会保留，更新前会自动备份到部署目录的 `backups/`。

**备份保留口径（轮换）**：脚本更新备份按家族各保留**最近 3 份**、更旧的自动删除，不要把 `backups/` 当长期归档——需要留超过 3 代的备份，请在被轮换掉之前自行复制到别处。三大家族同口径：① 每次脚本更新整包备份的目录 `backups/panel-update-<时间戳>`（含数据库与 `.env` 等部署文件）；② 强制重装时旧 `.env` 的副本 `.env.bak.<时间戳>`；③ `docker-compose.yml.bak.<时间戳>`。备份内容补充：SQLite 形态的整包备份**不含** `github-cache/`（那是可重新下载的发行文件缓存，不是数据，恢复时会自动重建）；PostgreSQL 形态备份含 `pg_dump` 导出的 SQL。人工清理：这三类备份只是升级失败时人工恢复与排障的依据，面板日常运行不依赖它们，手工删掉旧份（例如只留最近 1 份）不影响运行；自检：在部署目录 `ls backups/` 与 `ls .env.bak.* docker-compose.yml.bak.*`，每族应至多 3 份。

> 升级本身不会更换 `JWT_SECRET`；若升级时手工轮换了 `.env` 的 `JWT_SECRET`，所有已登录会话都会失效需重新登录，全部带 `?user=&sig=` 的个人订阅链接也会一并失效（签名用的是同一密钥），用户需在面板里重新复制链接。

> 升级不会改变在用的容器网络：脚本更新与面板内升级都会保留存量 `docker-compose.yml` 的子网与网络名，模板里的新子网（`172.30.0.0/16`）只对全新安装生效，见 [安装部署的子网提示](./install.md)。

脚本更新按版本感知执行：默认更新到最新正式版（测试版不参与默认更新）；需要定版时用 `VERSION=<版本号> ./panel_install.sh` 运行并选 `2`，会按指定版本更新。想跟进测试版时，用 `FLMX_RELEASE_CHANNEL=dev`（或 `beta`）再跑脚本，或显式 `VERSION=<测试版号>`。目标版本低于当前版本时会被**拒绝**（防降级，与面板内升级口径一致），同版本则按重装继续。当前版本以**后端容器实际运行的镜像版本**为准交叉核对：`.env` 记录与实际运行版本不一致时脚本会打印告警并按实际版本判定（取不到实际版本时才回退 `.env` 口径），防止 `.env` 被手改或停留在失败升级的目标版本时把降级误放行。

**存量部署的容器改名（脚本升级流内默认执行）**：改名前部署的面板，容器名仍是旧名（后端 `flux-panel-backend`、PostgreSQL `flux-panel-postgres`、前端 `vite-frontend`），或部署 `.env` 里缺 `PANEL_BACKEND_CONTAINER` / `PANEL_FRONTEND_CONTAINER` / `PANEL_POSTGRES_CONTAINER` 任一身份键时，脚本升级会**默认**把容器改名为现行名（`flmx-panel-backend` / `flmx-panel-postgres` / `flmx-panel-frontend`，多实例部署带实例后缀）：停删将被改名的旧容器 → 把身份键写为现行名 → 按新名重建并做健康检查；健康检查不过时按既有升级回滚恢复旧 `.env` 与旧容器名。改名**只动容器名**：数据卷、网络与 Compose 项目名只有在 `.env` 缺键时才按实际在用值补写，已有值逐字不动，数据不迁移。容器名是手工自定义名（非旧缺省名、非现行名）的部署不触发改名，现行名部署零动作。不想改名时，运行脚本前设置环境变量 `PANEL_IDENTITY_RENAME=0`（如 `PANEL_IDENTITY_RENAME=0 ./panel_install.sh`）再走升级，该次升级保留旧容器名。**自检**：升级后在部署目录执行 `docker ps --format '{{.Names}}'` 应看到三个现行名容器在运行；`grep -E '^PANEL_(BACKEND|FRONTEND|POSTGRES)_CONTAINER=' .env` 三键应均为现行名。一点例外：SQLite 形态部署触发改名时，PostgreSQL 容器同样会被停删，但**不会随本次升级重建**（只有 PostgreSQL 形态下升级才会拉起它），自检时只看到后端与前端两个容器属预期。没有任何流程依赖该容器先存：日后经脚本菜单 4 迁移到 PostgreSQL 时，迁移流程会按 `.env` 现行名与钉住的卷名自行拉起它；卷数据不动。若你在该 compose 里另行跑着面板不用的 PostgreSQL 容器，改名升级会把它一并停删，升级前请先自行处理。

## 指定版本

到 [Releases](https://github.com/iiiiiiiii7125-art/flmx-max/releases) 找到目标版本，把下面命令里的 `<目标版本号>` 换成该版本号再执行（不要带着占位符原样执行）：

```bash
VERSION=<目标版本号>
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/download/$VERSION/panel_install.sh -o panel_install.sh && curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/download/$VERSION/panel_install.sh.sha256 -o panel_install.sh.sha256 && sha256sum -c panel_install.sh.sha256 && chmod +x panel_install.sh && ./panel_install.sh
```

## 降级到旧版本（手动）

脚本与面板内升级都拒绝降级；确需手动回退到旧版本时注意：如果用过 Shadowsocks「按订阅计量」（1.4.2 起），**回退前必须先关闭全部入站的「按订阅计量」开关，并确认计量专用入口已消失（入站列表不再显示「计量中」徽标、对应端口已释放）再回退**。旧版本不认识计量专用入口，会把它当作普通 Shadowsocks 入站对外分发，其凭据可被直接使用且不计量。

**备份跨版本恢复的保真边界**：备份文件的版本号新旧恒为 `1.0`、不随格式加段升版。用**新版本面板导出的备份恢复进旧版本面板**时，旧代码不认识的新增段/字段会被忽略、恢复回执仍报全成功——受影响项按旧面板版本分：旧面板为 **1.9.5** 时只丢一项，即 `inbounds` 段内的 `shields` 字段（1.9.6 起导入节点的删除屏蔽登记随该字段往返；节点本身照常恢复，只是恢复后重新同步同一来源时，被手工删过的导入节点可能再次出现，重新删除即可）；旧面板为 **1.9.4 及更旧** 时再加 `templates`（订阅模板）一段，恢复后订阅按无模板渲染（节点与凭据本身不受影响）。备份文件本体无损，没有数据被写坏：把面板重新升级到导出时的版本（或更高）后**重新导入同一份备份**即可完整恢复。反向（旧备份恢复进新面板）无此问题，缺失的段按空集处理。

## 节点 agent 升级

**节点** 页选择节点 → **升级**，留空版本即使用最新版。也可以在节点上重新运行节点安装脚本。通过脚本更新同样防降级：目标版本低于节点现装版本时脚本会拒绝更新并说明（如确需降级请手动处理），同版本按重装继续，版本不可比时放行。

> **容器形态节点例外**：用 Docker / Docker Compose 安装命令部署的节点不在上述面板内升级的适用范围内——容器内没有脚本安装的二进制布局与服务管理器，面板下发升级/回退会直接失败并在回执中说明原因（不会损坏节点）。这类节点的升级路径是**重新获取安装命令并重跑**：在面板为该节点重新生成 Docker / Docker Compose 安装命令，复制到节点上执行，即以新镜像重建容器完成升级（安装命令的重跑本身就是容器形态的升级/重装路径）；降级同理，改用目标版本的安装命令重跑。

> 节点升级与面板自升级的同版本口径不同（有意差异）：节点目标版本与节点当前版本相同时，面板直接提示「已是最新版本」、不下发升级、不重启 agent；而面板自升级（含脚本更新）指定同版本会按**重装**继续执行，用于修复面板自身文件损坏等场景。两处都拒绝降级。

### 节点回退（手动回退到上一版本）

升级被防降级拒绝时，回执会把你指引到这里的**节点回滚功能**：**节点**页 → 目标节点行操作 **回退** → 弹窗确认「回退到上一个版本吗？」→ **确认回退**。回退会把节点上的 agent 换回备份版本并自动重启，期间节点短暂离线。

可用条件（缺一不可）：

- **脚本形态安装的节点**。容器形态节点不支持面板回退，执行时会被明确拒绝并提示改走容器路径——在节点安装弹窗重新获取**目标版本**的 Docker / Docker Compose 安装命令、在节点上重跑（见上文容器形态节点例外）。
- **该节点至少经面板成功升级过一次**。回退备份由面板升级在替换二进制之前于节点本地生成（节点上 `/etc/flmx_agent/flmx_agent.old`；改名前生成的备份在旧路径 `/etc/flux_agent/flux_agent.old`，仍被识别），从未面板升级过的节点没有这份备份，回退会回执「回退失败：没有可用的备份文件，无法回退」——这是正常拒绝，先正常升级一次后才有可回退的上一版本。
- **节点在线**（回退命令由面板下发给 agent 执行）。另外回退与升级在同一节点上互斥：该节点已有升级 / 回退操作在途时，回退会被拒绝并点名在途操作，等它完成后再试。

落点语义（单代）：

- 备份**只保存一代**——被最近一次面板升级换下来的那个版本。回退就是把这份备份换回并重启 agent；**回退完成后，回执会点名实际落到的版本**，以回执为准核对落点。
- 回退本身**不产生新的备份**：回退之后想再回到较新版本，只能重新走升级；回退完成后现行二进制已与备份相同，再次执行回退会被拒绝并提示「已在备份版本，无需回退」，不会再往前退一代。
- **与脚本更新的备份关系**：在节点上重跑安装脚本更新用的是另一份临时备份（`flmx_agent.bak`，只服务于脚本更新自身的失败自动回滚，更新成功即删除；改名前生成的旧名 `flux_agent.bak` 仍被识别），与面板回退的 `.old` 备份是两回事。面板回退在换入备份前会先校验这份备份的代际：面板升级生成 `.old` 时会同写一份配对记录，证明它正是被那次升级换下的**紧邻上一代**。若最近一次版本变更走的是脚本更新（现行版本已不是配对记录里那次面板升级装入的版本），这份 `.old` 就不再是紧邻上一代——此时回退会被**拒绝**，而不是静默跨代退到更早的版本；回执会点名配对记录的装入版本、当前版本与备份版本，如需回到备份版本，请用面板的节点安装命令重新安装该版本。存量节点上在配对机制之前生成的 `.old` 没有配对记录，这类备份仍可回退，回执会点名实际落到的版本，并注明该备份未经紧邻上一代校验。
- **以上校验与点名以新版 agent 为前提**：代际配对校验、跨代回退拒绝与落点版本点名，都是带配对记录机制的新版 agent 才有的行为。**旧版 agent**（配对机制之前的版本）没有配对记录与代际校验：回退会直接把 `.old` 备份换回执行，脚本更新居中造成的跨代落点不会被拒绝，回执也不点名落点版本；这类回退的面板回执会补注与存量备份同口径的未校验提示（该备份无代际配对记录，未经紧邻上一代校验）。在面板先升、节点后升的混跑窗口内对旧 agent 节点执行回退时，见到该提示请结合节点实际版本自行核对落点；要获得完整的代际校验与落点点名，先把该节点 agent 升级到当前版本。
- 与升级当场的**自动回滚**区分：新版本重启后健康检查不过时节点本地自动换回备份，那是升级流程内部的闭环（见下一段）；本节的手动回退是事后由管理员主动发起的操作，两者用的是同一份 `.old` 备份。

**自检**：回退完成后，节点重新在线，在节点页核对该节点的版本已变为回执点名的上一版本，即落点正确。

1.3.4 起面板内升级与 init 系统无关：systemd 与 OpenRC（Alpine）节点都走各自的服务管理器，不再硬编码 systemd 命令；升级会先给新二进制做 `-V` 冒烟、备份现二进制（备份写失败直接中止、不裸替换），重启后本地健康检查不过会自动回滚到备份，回滚在节点本地闭环、不依赖与面板的连接，结果记在节点上的 `agent-upgrade.log`。

> 无 TLS 面板 + 节点 `addr` 为裸地址的部署，agent 升级重启后可能连不上（不再自动降级明文），升级前先看 [节点连不上：地址写法与时钟](./install.md#节点连不上-地址写法与时钟-1-3-4-起)。联邦双方还要注意 [升级顺序](./relay.md#联邦-面板共享-升级顺序-先消费方-后提供方)。

## 修复面板内升级

面板内升级提示“部署docker-compose.yml不可用”时：3.0.6 及更早版本的面板内升级会把后端容器的部署目录挂载弄丢，之后面板内升级会一直提示这个错误。（注：3.0.x 指本项目上游的旧版本线；现行版本均晚于该节点，全新安装不受影响，本节只针对从旧版本线一路升级上来的存量部署。）数据不受影响，修复一次即可：

1. 在服务器上进入存放 `docker-compose.yml` 和 `.env` 的目录（常见为 `/root`，或当初运行安装脚本的目录），用 `ls` 确认这两个文件都在
2. 执行：

   ```bash
   docker compose up -d --force-recreate backend frontend
   ```

也可以在该目录运行 3.0.7 及以后的安装脚本（现行版本的安装脚本同样适用），选“更新面板”，会顺带修复。

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
SELECT LOWER("user"), COUNT(*) FROM "user" GROUP BY LOWER("user") HAVING COUNT(*) > 1;
SQLEOF
```

> 以上 SQL 中的前 4 条重复检查与代码启动预检逻辑（`runV1Precheck`）一致；`forward_port` 孤儿检查不在启动预检内，只包含在 `scripts/phase_a_dryrun.sql` 的 dry-run 清单中。最后一条是用户名**大小写折叠**重复检查，与 v5 迁移（`k10_v5_username_casefold_unique`，1.7.5 起）的预检同形，即 dry-run 清单的第 6b 节：1.7.5 起用户名判重不区分大小写，仅大小写不同的同名用户（如 `Admin` 与 `admin`）同样算重复，不清理会使折叠唯一索引降级缺席（见 §5）。

> 注意：上面六条都是**计数定位**查询，只输出分组键与计数；§3 保留判据需要的明细列不在其中，清理前先用下面的明细查询把重复组逐行拉出来再对照 §3 判定。特别注意 `status` 是父表 `forward` 的列（经 `forward_id` 关联），`forward_port` 表本身**没有** status 列——在 forward_port 上按 status 过滤会直接报错。

```bash
sqlite3 /tmp/gost.db.bak << 'SQLEOF'
.headers on
.mode column
SELECT fp.id, fp.node_id, fp.port, fp.forward_id, f.status FROM forward_port fp JOIN "forward" f ON f.id = fp.forward_id WHERE (fp.node_id, fp.port) IN (SELECT node_id, port FROM forward_port GROUP BY node_id, port HAVING COUNT(*) > 1) ORDER BY fp.node_id, fp.port, fp.forward_id;
SELECT node_id, port, tunnel_id FROM chain_tunnel WHERE port > 0 AND (node_id, port) IN (SELECT node_id, port FROM chain_tunnel WHERE port > 0 GROUP BY node_id, port HAVING COUNT(*) > 1) ORDER BY node_id, port, tunnel_id;
SELECT id, node_id, port, kernel_id, enabled FROM node_inbound WHERE kernel_id > 0 AND (node_id, port) IN (SELECT node_id, port FROM node_inbound WHERE kernel_id > 0 GROUP BY node_id, port HAVING COUNT(*) > 1) ORDER BY node_id, port, id;
SELECT id, "user", status, created_time FROM "user" WHERE "user" IN (SELECT "user" FROM "user" GROUP BY "user" HAVING COUNT(*) > 1) OR LOWER("user") IN (SELECT LOWER("user") FROM "user" GROUP BY LOWER("user") HAVING COUNT(*) > 1) ORDER BY LOWER("user"), created_time;
SQLEOF
```

### 3. 清理步骤

| 类型 | 保留 | 依据 |
|------|------|------|
| forward_port 重复 | forward.status=1 的行；都有则保留 forward_id 最小 | 活跃优先（status 是父表 forward 的列，forward_port 本身没有 status 列） |
| forward_port 孤儿 | 删除 | 转发已不存在 |
| chain_tunnel 重复 | tunnel_id 最小 | 先建优先 |
| node_inbound 重复（仅 kernel_id > 0 的内核入站） | enabled=1；都有则 id 最小 | 启用优先；导入节点（kernel_id=0）同端口不同主机是合法数据，不要清理 |
| 用户名重复 | 最早创建未停用；其余改名加 `_dup` | 不删账号 |
| 用户名大小写折叠重复（仅大小写不同） | 保留其一；其余按上一行口径改名 | 1.7.5 起判重不区分大小写；系统只在启动日志中逐组点名，不自动合并/改名/删账号，保留哪个由管理员决定 |

### 4. 触发迁移

```bash
docker restart <backend容器>
docker logs <backend容器> 2>&1 | grep -i migration
```

### 5. 索引未创建时的处理

若日志显示 `PRECHECK FAILED`：面板正常启动，按 §2 重新检查清理后重启即可（迁移幂等，会重试）。

大小写折叠唯一索引（`idx_user_username_fold_unique`，v5 迁移 `k10_v5_username_casefold_unique`，1.7.5 起）同理：存量库存在折叠重复组时迁移**降级跳过**——不建索引、不记迁移版本，启动日志逐组点名（折叠名、成员 id、实际存储形态）；面板正常启动，应用层判重照常按大小写不敏感执法，只是少了数据库层对并发写入的最终兜底。按 §3 清理折叠双生子后**重启面板即自动补建**该索引（迁移未记版本、下次启动重试），无需手工建索引；不清理则每次启动都会重复点名、索引持续缺席。

### 6. 回滚

在**宿主机**上执行。注意 `/app/data/gost.db` 是后端**容器内**的路径，宿主机上没有这个文件——直接在宿主机跑 `sqlite3 /app/data/gost.db …` 只会在错误位置新建一个空库、回滚实际不发生。先停后端，把数据库复制出来、改完再放回去（`<backend容器>` 的查法见 §1）：

```bash
docker stop <backend容器>
docker cp <backend容器>:/app/data/gost.db /tmp/gost.db
sqlite3 /tmp/gost.db "DROP INDEX IF EXISTS idx_forward_port_node_port;"
sqlite3 /tmp/gost.db "DROP INDEX IF EXISTS idx_chain_tunnel_node_port;"
sqlite3 /tmp/gost.db "DROP INDEX IF EXISTS idx_node_inbound_node_port;"
sqlite3 /tmp/gost.db "DROP INDEX IF EXISTS idx_user_username_unique;"
sqlite3 /tmp/gost.db "DROP INDEX IF EXISTS idx_user_username_fold_unique;"
docker cp /tmp/gost.db <backend容器>:/app/data/gost.db
docker start <backend容器>
```

### 7. 停用用户名释放

`POST /api/v1/user/update` 将停用用户改名后，新用户可注册原用户名（`user_id` 不变）。

## GitHub 镜像代理（面板源分发）

面板内置一个 GitHub 文件代理端点，地址是 `/api/v1/open_api/gh/`（面板域名后拼接，例如 `https://你的面板域名/api/v1/open_api/gh/...`）。**面板源分发**起，它是节点下载的主力通道之一，专治国内节点机连不上/连不快 GitHub 时的安装、升级与内核下载：

- **节点自升级、内核安装、geodata 下载**：agent 从自己配置里的面板地址（安装时的 `-a` 参数）派生出面板源线路，自动加入内置候选的并行测速排名——面板线路延迟低时胜出，健康机器的直连短路行为不变。地址只由 agent 从自身配置派生，面板无法在运行时给 agent 下发下载地址（安全边界）。
- **新节点安装**：面板生成的安装命令把面板源排在候选表第一位；`install.sh` 本身也先单探面板源、通则独占使用。装机时唯一已证可达的目的地就是面板，国内机不再先撞 GitHub 直连。
- **升级完整性**：面板下发升级命令前会先把目标版本双架构资产预热进镜像缓存，并随命令附上该版本的期望 SHA-256（取自面板镜像缓存的官方校验文件，经格式校验）；agent 下载后以它为校验基准，哈希不符直接拒绝升级并在回执中写明。期望哈希缺失时（旧面板、预热未命中）回退现行校验口径，不影响升级可用性。
- **无需任何设置**：面板侧没有开关，节点侧也不用改配置；面板源线路与内置 9 代理＋直连受同一套测速、续切管辖（升级与内核下载的总预算是 240 秒，geodata 下载单独为 300 秒），面板镜像不可用时自动回落既有路线，不劣化现状。
- **存量节点的自举边界（1.9.0 起）**：面板源线路是 **1.9.0 起的 agent 原生能力**——节点升级自 1.9.0 起原生支持走面板源。**1.8.x 及更早版本的存量 agent 尚无这条线路，无法用它给自己升级**（它认识的候选仍只有直连＋9 代理）。被墙的存量节点先这样到达新版：在面板里生成该节点的**安装命令**（现行 `install.sh` 已把面板源排在第一候选，装机全程可走面板源），在节点机上执行一次，装到 1.9.0 或更新版本（安装命令恒装所选渠道的最新版）；此后该节点的升级、内核与 geodata 下载全程可走面板源，不再依赖 GitHub 可达性。

该端点同时保留手动拼接用法：外部工具、脚本可以经面板中转拉取下方白名单仓库的 GitHub 文件；早期版本节点的本地代理配置若仍指向这个端点，也继续可用。注意它与面板自身的版本发现路线无关：版本发现、内核版本列表走面板侧直选（见下一节），不经过这个端点。

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
- 按来源 IP 限流 60 次/分钟，超限返回 429
- release tag 的文件永久缓存；`latest`、raw 文件和 API 响应按 TTL 刷新
- GitHub 不可达时的兜底分内容类型：release tag 的文件永久缓存、直接用缓存副本；`latest`、raw 文件与 API 响应这类可变内容在 TTL 过期后最多用 24 小时内的旧副本兜底，超过 24 小时仍不可达时会返回下载错误（502），不会无限期发旧内容

### 版本发现与下载路线（1.7.7 起）

系统设置里的「开启 GitHub 加速」已整体移除。内核版本列表、节点升级版本列表、面板自升级版本发现，以及节点安装/升级与内核下载，都改走**内置候选自动测速直选**：候选为 9 个内置代理前缀加直连，先单独探测直连（以真实目标可解析为准），直连不通才并行探测代理、按延迟选最快，选中路线中途失败按排名自动续切。其中「直连通就直连」有一条 agent 侧边界：节点升级、内核与 geodata 下载在 agent 侧执行时，直连虽然判活、但探测延迟超过 800ms 的**不算短路**，会与 9 个代理同场按延迟排名、可能改走更快的代理；面板侧的版本发现没有这条阈值，判活即直连。无需、也无法在面板里再配置加速地址；已保存的旧加速配置不再被读取。

版本发现与订阅源共用同一套出站安全策略：重定向逐跳复检，云元数据地址（`169.254.169.254`）在任何配置下都被拒绝，私网/回环/链路本地目标默认拒绝。内置候选均为公网地址、正常情况下不受影响；若部署环境的 DNS 把候选域名解析到内网地址，版本列表会报错，须显式放行：

- `FLMX_FETCH_PRIVATE_ALLOWLIST=127.0.0.1/32`：仅放行指定 CIDR（推荐）
- `FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true`：放行所有私网地址（不推荐）

**设置位置与生效方法**：这两个变量是**后端进程的环境变量**，随版 `docker-compose.yml` 的 `backend` 服务默认**没有**它们的传递行——只把变量写进部署目录的 `.env` 文件不会进入后端容器、不会生效。需要在部署目录的 `docker-compose.yml` 里给 `backend` 服务的 `environment:` 段补上对应行（直接写字面量，或写 `- FLMX_FETCH_PRIVATE_ALLOWLIST=${FLMX_FETCH_PRIVATE_ALLOWLIST}` 再把值放进同目录 `.env`），然后执行 `docker compose up -d backend` 重建后端容器才生效。自检：`docker exec <后端容器> printenv FLMX_FETCH_PRIVATE_ALLOWLIST` 能打印出所设值，即已进入后端进程；打印为空就是传递行没补对。同一口径适用于全部 `FLMX_FETCH_*` 与 `FLMX_GIST_API_BASE` 环境变量（见 [导入节点](./import.md#订阅源地址安全策略) 与 [Gist 备份](./gist-backup.md#高级-api-基地址)）。
