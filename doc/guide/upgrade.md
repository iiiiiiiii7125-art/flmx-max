# 升级

## 面板内升级

**推荐升级顺序：先备份 → 先停 agent（节点侧）→ 换面板 → 再启 agent。** 该顺序同样适用于下面的脚本升级：升级前先备份（网站配置 → 数据备份与恢复 → 下载备份；脚本升级会在更新前自动备份到部署目录的 `backups/`），再到每个节点上停掉 agent（systemd 节点：`systemctl stop flmx_agent`；Alpine / OpenRC 节点：`rc-service flmx_agent stop`；改名前安装、尚未迁移布局的节点服务名仍为 `flux_agent`（布局迁移只在节点机上重跑安装命令或脚本更新时发生，面板内的节点升级只换二进制、不迁布局）），换完面板后重新启动 agent（`systemctl start flmx_agent` / `rc-service flmx_agent start`）。不停 agent 直接换面板时，在线 agent 会在面板重建期间重试/重连（有重连对账兜底），但面板与 agent 版本会短暂不一致。

**网站配置** → **面板自升级** → **检查更新** → **立即升级**。

升级会拉取新镜像并重建前后端容器，约 1 分钟后刷新页面即可。页面没有变化时，通常是浏览器或 CDN 缓存，刷新或清理缓存即可。

### 面板自升级的下载校验（跨线路第二锚）

面板自升级经面板下载的资产（compose 文件）要过两道校验；前后端镜像不经面板下载、由 `docker compose pull` 从镜像仓库拉取，靠仓库的摘要（digest）机制保证完整性，不在双锚覆盖内。脚本升级、节点 agent 升级与面板生成的节点安装命令同为双锚模型，降级收口同律（按获胜线路分层，见下）：

- **第一锚（同源校验）**：资产与它的 `.sha256` 校验文件从**同一条线路、同一次下载**取得并比对，哈希对不上直接拒绝该线路。只靠这一锚有个缺口：某条线路若被污染，资产与校验文件会被一起替换、自洽地通过校验。
- **第二锚（跨线路复核）**：某条线路胜出定版后，面板再从候选线路里**另一条不同的线路**重新取一次同一资产的 `.sha256`，与第一锚采信的校验值比对。两条线路独立给出同一个校验值，才算复核通过。
- **compose 落盘前内容校验**：下载到的 `docker-compose.yml` 在覆盖现有文件之前还要过一道内容校验——必须是合法 YAML、根为映射、含 `services` 且包含预期的 `backend` 与 `frontend` 两个服务；任一不合即中止升级，**旧 compose 文件保持不动**，不会把坏文件落盘后拿宿主 Docker 权限执行。

撞见下面两种回执时的含义与处置：

- **「校验文件经两条不同线路获取的结果不一致，已中止升级（…第二锚）」**：两条线路给出的校验值互相矛盾，说明至少一条线路上的内容不可信（镜像被篡改、缓存污染或同步错位都有可能）。此时升级被**硬中止、不换路重试**，面板保持原版本运行、不会进入拉镜像 / 重建步骤。处置：不要反复立即重试、更不要想办法绕过校验；先等一段时间再试（线路缓存同步后可能自行恢复），若多次重试仍在同一资产上中止，把回执里的两个哈希值留存下来排查线路，不要继续升级。
- **回执点名「跨线路锚复核已降级」**：其他线路都没能给出合法校验值（线路不可达、校验文件取不到等），没有第二条线路可复核，**且本次获胜定版的是直连**。此时**不中止**，按第一锚（同源校验）的结果继续升级，回执里会以「跨线路锚复核已降级：升级校验锚复核不可用（其他线路均未给出校验值），按单锚继续」明确点名。含义是这次升级只经过单锚校验、不是升级失败；可以正常完成，介意时可在网络恢复后留意后续升级是否仍点名降级——长期每次都降级说明候选线路整体可达性有问题，需要排查出站网络。
- **回执报「跨线路锚复核失败：获胜线路为代理……已中止升级」**：获胜定版的是第三方代理、且其他线路都没能给出合法校验值。此时定版依据只剩获胜代理自己给出的资产与校验文件这一自洽对，单个代理即可整单伪造、不构成验证，升级被**硬中止**（与脚本侧同一口径），面板保持原版本运行。处置：等直连或其他线路恢复后再试；若部署环境长期只有单条代理可达，可改走脚本升级并由运维显式指定单线（`PROXY_URL`，自担风险、点名降级），或人工下载并核对哈希后替换部署目录的 compose 文件。

**脚本侧**：`panel_install.sh` 的首装、菜单更新与自更新三条链下载资产时同样执行跨线路复核（脚本内函数 `corroborate_download_anchor`）：异路给出不同哈希时在 stderr 报「跨线路锚复核失败：校验文件经两条不同线路获取的结果不一致，已中止（L8-F-02 第二锚）」，**删除已下载文件并整单中止**；其他线路都给不出合法校验值时按获胜线路分层收口——获胜的是第三方代理时报「跨线路锚复核失败：获胜线路为第三方代理、其他线路均未给出合法校验值，拒绝下载（R2-01-01：第二锚 fail-closed，不降级单锚）」并同样删档中止（单个代理同时喂资产与校验文件的自洽对不构成验证）；只有获胜线路是直连、或路线表只有运维显式指定的一条线路（如 `PROXY_URL` 覆盖）时，才在 stderr 点名「跨线路锚复核已降级」后按单锚继续。整个复核一轮共用 20 秒总界，不会显著拉长升级时间。唯一例外是运维显式设置 `FLMX_VERIFY_DOWNLOADS=0` 时校验被整体关闭、第二锚随第一锚一并跳过——这是显式逃生口，不是静默降级，不要在日常升级里设置它。

> 节点 agent 升级侧有同一套第二锚口径，其期望哈希由面板预热下发，见下文「GitHub 镜像代理（面板源分发）」的「升级完整性」一条。

### 升级前数据库快照：SQLite 有，PostgreSQL 没有（面板内升级）

面板内升级在重建容器前会不会留一份数据库快照，按数据库形态分：

- **SQLite 形态：有。** 升级发起回执与升级状态里的字段 `snapshotPath` 会点名快照的相对路径（形态为 `backups/panel-update-<目标版本>-<时间戳>`，位于面板部署目录下），helper 在停旧后端后、重建前把数据卷整目录复制到该路径，按最近 3 份轮换。快照只备不自动恢复：升级出问题需要回退数据时，按回执点名的路径人工恢复。
- **PostgreSQL 形态：没有。** 面板内升级对 PostgreSQL **不做升级前数据库快照**（文件级备份与失败时按旧定义重建容器的回滚照常，但那不是数据库快照，数据卷本身不被复制）。发起回执与升级状态里会显式带字段 `pgSnapshotDeferred: true` 点名这一事实，同时 `snapshotPath` 为空——**空不代表快照在别处，只是没有**。需要升级前数据库备份的 PostgreSQL 部署，请改走下面的脚本升级（脚本更新的整包备份含 `pg_dump` 导出），或升级前自行在面板里下载一份数据备份（网站配置 → 数据备份与恢复 → 下载备份）。

**自检**：升级完成后在部署目录看状态文件 `.flmx_upgrade_status.json`：SQLite 形态应有非空的 `snapshotPath` 且该目录真实存在（`ls backups/` 可见同名目录）；PostgreSQL 形态应有 `"pgSnapshotDeferred": true` 且无 `snapshotPath`。两个字段都缺失时，说明这次升级不是经面板内升级发起的（或状态文件已被后续升级覆盖）。

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

**存量部署的容器改名（脚本升级流内默认执行）**：改名前部署的面板，容器名仍是旧名（后端 `flux-panel-backend`、PostgreSQL `flux-panel-postgres`、前端 `vite-frontend`），或部署 `.env` 里缺 `PANEL_BACKEND_CONTAINER` / `PANEL_FRONTEND_CONTAINER` / `PANEL_POSTGRES_CONTAINER` 任一身份键时，脚本升级会**默认**把容器改名为现行名（`flmx-panel-backend` / `flmx-panel-postgres` / `flmx-panel-frontend`，多实例部署带实例后缀）：停删将被改名的旧容器 → 把身份键写为现行名 → 按新名重建并做健康检查；健康检查不过时按既有升级回滚恢复旧 `.env` 与旧容器名。改名**只动容器名**：数据卷、网络与 Compose 项目名在 `.env` 缺键时才按实际在用值补写；但两个数据卷键（`FLMX_SQLITE_VOLUME` / `FLMX_POSTGRES_VOLUME`）是例外——已有值与在跑容器的实际挂载冲突时，升级会点名（键名、旧值、实读值）并**以实读为准改写**，因为卷键写错时挂载事实才是数据所在，逐字保留错键会让升级挂着空卷还报成功；网络与 Compose 项目名仍只补缺、已有值逐字不动。数据不迁移。容器名是手工自定义名（非旧缺省名、非现行名）的部署不触发改名，现行名部署零动作。不想改名时，运行脚本前设置环境变量 `PANEL_IDENTITY_RENAME=0`（如 `PANEL_IDENTITY_RENAME=0 ./panel_install.sh`）再走升级，该次升级保留旧容器名；也可以直接在部署 `.env` 里写一行 `PANEL_IDENTITY_RENAME=0`——开关双通道读取、`.env` 优先（`.env` 里出现该键即以其值为准，缺键才看进程环境），脚本升级与面板内升级同口径。**自检**：升级后在部署目录执行 <span v-pre>`docker ps --format '{{.Names}}'`</span> 应看到三个现行名容器在运行；`grep -E '^PANEL_(BACKEND|FRONTEND|POSTGRES)_CONTAINER=' .env` 三键应均为现行名。一点例外：SQLite 形态部署触发改名时，PostgreSQL 容器同样会被停删，但**不会随本次升级重建**（只有 PostgreSQL 形态下升级才会拉起它），自检时只看到后端与前端两个容器属预期。没有任何流程依赖该容器先存：日后经脚本菜单 4 迁移到 PostgreSQL 时，迁移流程会按 `.env` 现行名与钉住的卷名自行拉起它；卷数据不动。若你在该 compose 里另行跑着面板不用的 PostgreSQL 容器，改名升级会把它一并停删，升级前请先自行处理。

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

- 备份**只保存一代**——被最近一次面板升级换下来的那个版本。回退就是把这份备份换回并重启 agent；**回退回执是下发回执、不是落点证明**：回执（`status` 为 `dispatched`）只点名本次回退的**目标版本**（`expectedVersion`），表示命令已下发、节点正在重启；落点是否成功，以节点重连后在节点列表里上报的实际版本为准核对——回退若在节点侧静默失败，重连后版本不会变，那才是落点没到的信号，不要只凭回执成功就认定已经回退。
- 回退本身**不产生新的备份**：回退之后想再回到较新版本，只能重新走升级；回退完成后现行二进制已与备份相同，再次执行回退会被拒绝并提示「已在备份版本，无需回退」，不会再往前退一代。
- **与脚本更新的备份关系**：在节点上重跑安装脚本更新用的是另一份临时备份（`flmx_agent.bak`，只服务于脚本更新自身的失败自动回滚，更新成功即删除；改名前生成的旧名 `flux_agent.bak` 仍被识别），与面板回退的 `.old` 备份是两回事。面板回退在换入备份前会先校验这份备份的代际：面板升级生成 `.old` 时会同写一份配对记录，证明它正是被那次升级换下的**紧邻上一代**。若最近一次版本变更走的是脚本更新（现行版本已不是配对记录里那次面板升级装入的版本），这份 `.old` 就不再是紧邻上一代——此时回退会被**拒绝**，而不是静默跨代退到更早的版本；回执会点名配对记录的装入版本、当前版本与备份版本，如需回到备份版本，请用面板的节点安装命令重新安装该版本。存量节点上在配对机制之前生成的 `.old` 没有配对记录，这类备份仍可回退，回执会点名目标版本，并注明该备份未经紧邻上一代校验；落点同样以重连后节点列表上报的版本为准。
- **以上校验与点名以新版 agent 为前提**：代际配对校验、跨代回退拒绝与目标版本点名（回执的 `expectedVersion`），都是带配对记录机制的新版 agent 才有的行为。**旧版 agent**（配对机制之前的版本）没有配对记录与代际校验：回退会直接把 `.old` 备份换回执行，脚本更新居中造成的跨代落点不会被拒绝，回执也不点名目标版本；这类回退的面板回执会补注与存量备份同口径的未校验提示（该备份无代际配对记录，未经紧邻上一代校验）。在面板先升、节点后升的混跑窗口内对旧 agent 节点执行回退时，见到该提示请结合节点实际版本自行核对落点；要获得完整的代际校验与目标版本点名，先把该节点 agent 升级到当前版本。
- 与升级当场的**自动回滚**区分：新版本重启后健康检查不过时节点本地自动换回备份，那是升级流程内部的闭环（见下一段）；本节的手动回退是事后由管理员主动发起的操作，两者用的是同一份 `.old` 备份。

**自检**：回退完成后，节点重新在线，在节点页核对该节点的版本已变为回执点名的目标版本（`expectedVersion`），即落点正确；若重连后版本未变，说明回退没有落点，按未落点处理，不要凭回执的下发成功认定完成。

1.3.4 起面板内升级与 init 系统无关：systemd 与 OpenRC（Alpine）节点都走各自的服务管理器，不再硬编码 systemd 命令；升级会先给新二进制做 `-V` 冒烟、备份现二进制（备份写失败直接中止、不裸替换），重启后本地健康检查不过会自动回滚到备份，回滚在节点本地闭环、不依赖与面板的连接，结果记在节点上的 `agent-upgrade.log`。

> 无 TLS 面板 + 节点 `addr` 为裸地址的部署，agent 升级重启后可能连不上（不再自动降级明文），升级前先看 [节点连不上：地址写法与时钟](./install.md#节点连不上-地址写法与时钟-1-3-4-起)。

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

**设置位置与生效方法**：这两个变量是**后端进程的环境变量**，随版 `docker-compose.yml` 的 `backend` 服务默认**没有**它们的传递行——只把变量写进部署目录的 `.env` 文件不会进入后端容器、不会生效。需要在部署目录的 `docker-compose.yml` 里给 `backend` 服务的 `environment:` 段补上对应行（直接写字面量，或写 `- FLMX_FETCH_PRIVATE_ALLOWLIST=${FLMX_FETCH_PRIVATE_ALLOWLIST}` 再把值放进同目录 `.env`），然后执行 `docker compose up -d backend` 重建后端容器才生效。自检：`docker exec <后端容器> printenv FLMX_FETCH_PRIVATE_ALLOWLIST` 能打印出所设值，即已进入后端进程；打印为空就是传递行没补对。同一口径适用于全部 `FLMX_FETCH_*` 环境变量（见 [导入节点](./import.md#订阅源地址安全策略)）。

## 可信代理名单收窄：升级后核对限流的客户端 IP 口径（C4-01）

后端判断「是否采信反代传来的 `X-Forwarded-For`（XFF）」的口径在本轮收窄，**升级后请按本节核对一次配置**，否则限流粒度会与升级前不同：

- **旧口径**：直接对端是回环**或 RFC1918 私网地址**（容器子网、`192.168.*` 等）时，即按可信跳数解析 XFF、按真实客户端 IP 限流。
- **新口径**：回环**恒可信**；其余对端——包括模板容器子网 `172.30.0.0/16` 在内的全部私网——**只有落在 `.env` 的 `FLMX_TRUSTED_PROXIES` 显式登记的 CIDR 名单内才可信**。未登记者一律不解析它的 XFF，限流键与登录/审计记录里的来源 IP 都取**直接对端 IP 本身**。CGNAT `100.64.0.0/10` 此前本就不在默认信任内，口径不变。

**对存量部署的影响（升级当下就会发生，安全侧收紧、不中断服务）**：最常见的形态是请求经前端容器转发到后端——直接对端是前端容器的容器网络地址。升级前靠「私网默认可信」按访客真实 IP 分桶限流；升级后若还没登记名单，限流键会**回退为前端容器 IP**：全体访客共用一个 10 次/分钟的登录限流桶（一人连续输错密码即可连带其他人吃 429），登录记录与审计里的来源 IP 也全是同一个容器地址。登录与业务本身不受影响，失效方向是限流更严、不是放行伪造——但请尽快按下法登记，恢复升级前的粒度。直接对端是回环的形态（本机进程反代直达后端端口等）不受影响、仍恒可信。

**配置法（字段级）**：

1. 在面板部署目录的 `.env` 里写一行：`FLMX_TRUSTED_PROXIES=172.30.0.0/16`——值是**转发者所在子网的 CIDR**。存量 `.env` 里可能根本没有这个键（升级不重写 `.env`、也就看不到新装模板里这段字段级注释），没有就新增一行。随版模板的缺省子网就是 `172.30.0.0/16`，照缺省部署即填此值；改过子网的按实际填：先 `docker network ls` 找到面板网络（名以后缀 `gost-network` 结尾），再 `docker network inspect <网络名>` 看 `IPAM.Config` 中的 `Subnet`。链上还隔着其他反代时，把每一跳所在子网一并加进来、**逗号分隔**（如 `FLMX_TRUSTED_PROXIES=172.30.0.0/16,192.168.1.0/24`）。
2. 传递行不用操心：随版 compose 模板（`docker-compose-v4.yml` / `docker-compose-v6.yml`）的 backend `environment:` 已自带 `FLMX_TRUSTED_PROXIES: ${FLMX_TRUSTED_PROXIES:-}`，脚本更新与面板内升级换入新 compose 时自动带入；**手工自备 compose 的部署**须照此在 backend 的 `environment:` 段补上这一行——只写 `.env`、没有传递行，变量进不了后端容器、不会生效。
3. 生效：升级本身会重建后端、随即生效；单独改这个值时，改完 `.env` 执行 `docker compose up -d backend` 重建后端。
4. **防错警告**：绝不要填 `0.0.0.0/0`——名单内任意机器都能伪造 XFF 冒充任意 IP，限流与 IP 审计全部失效；只登记实际转发者所在的子网，范围越宽风险越大；填了无关子网等于没填（仍是共享桶）且不报错，只能靠下面的自检发现。

**自检**：① `docker exec <后端容器> printenv FLMX_TRUSTED_PROXIES` 能打印出所填名单，即变量已进入后端进程；打印为空就是传递行或 `.env` 没写对。② 换一个网络（如手机流量）登录一次面板，看登录记录或后端日志中这条登录的客户端 IP：是新网络的公网 IP 即已恢复真实 IP 口径；仍是 `172.30.x` 的容器地址，就是子网没填对。反向验证：从未登记的内网地址直连后端并轮换 XFF 时，限流计数应按其真实对端 IP 累计、不随 XFF 变化重置（会重置说明该对端被错误信任）。

## FLVX_* 环境变量双读退场（2.0.0 起）

1.9.8 改名过渡期引入的 `FLVX_*` 旧名环境变量双读（新名 `FLMX_*` 优先、旧名兜底）自 **2.0.0 起退场**：后端、节点 agent 与安装脚本只认新名。仍在用旧名键的部署，升级到 2.0.0 后旧名键会被**静默忽略**、对应功能回落缺省行为，不报错——请在升级前按下表逐个改名（只换键名、值不变）。`FLUX_VERSION` 版本键不在此列：它仍是新旧双写、读取仍旧名回退，长期并存，不用改。

| 旧名（2.0.0 起不再读取） | 改名为 | 消费面与不改名的后果 |
|---|---|---|
| `FLVX_TRUSTED_PROXY_HOPS` | `FLMX_TRUSTED_PROXY_HOPS` | 后端：可信代理跳数（登录限流与客户端 IP 解析）。不改名 → 回落默认 1 跳 |
| `FLVX_FETCH_ALLOW_PRIVATE_NETWORKS` | `FLMX_FETCH_ALLOW_PRIVATE_NETWORKS` | 后端：出站抓取（订阅源、版本发现、监控探测）放行全部私网。不改名 → 私网目标恢复默认拒绝 |
| `FLVX_FETCH_PRIVATE_ALLOWLIST` | `FLMX_FETCH_PRIVATE_ALLOWLIST` | 后端：出站抓取按 CIDR 放行私网。不改名 → 放行名单失效 |
| `FLVX_WEBAUTHN_ORIGIN` | `FLMX_WEBAUTHN_ORIGIN` | 后端：Passkey Origin。不改名 → 通行证密钥按未配置处理、入口整体消失；随版 docker-compose 模板里旧名的透传行同步移除 |
| `FLVX_LOG_REDACT_EXEMPT_PREFIXES` | `FLMX_LOG_REDACT_EXEMPT_PREFIXES` | 后端：日志 URL 路径段脱敏的豁免前缀。不改名 → 豁免失效、恢复全量掩码 |
| `FLVX_KERNEL_DIR` | `FLMX_KERNEL_DIR` | 节点 agent：内核状态根目录覆盖（写在 agent 的 systemd unit 或 drop-in 里）。不改名 → agent 回落缺省根目录、内核状态换根（旧目录文件不删） |
| `FLVX_VERIFY_DOWNLOADS` | `FLMX_VERIFY_DOWNLOADS` | 面板安装脚本 `panel_install.sh`：下载校验开关。不改名 → 回落默认开启校验——曾用旧名关过校验的部署，升级后校验会重新打开 |

**改名方法与生效口径**（与各键原有口径一致）：

- 后端键（表中前七行）：它们是**后端进程的环境变量**，口径与上一节相同——部署目录 `.env` 里把键名改掉的同时，须保证 `docker-compose.yml` 的 `backend` 服务 `environment:` 段有**新名**的传递行（模板自带的只有 `FLMX_WEBAUTHN_ORIGIN` 一行，其余键手工补 `- FLMX_<键名>=${FLMX_<键名>}`），再执行 `docker compose up -d backend` 重建后端。自检：`docker exec <后端容器> printenv FLMX_<键名>` 能打印出所设值即已生效。
- agent 键 `FLMX_KERNEL_DIR`：改写 agent 的 systemd unit 文件或 drop-in（`<单元名>.d/*.conf`）里的 `Environment=` 行，执行 `systemctl daemon-reload` 后重启 agent 生效。
- 脚本键 `FLMX_VERIFY_DOWNLOADS`：它是运行 `panel_install.sh` 时的进程环境变量，在命令前设置（如 `FLMX_VERIFY_DOWNLOADS=0 ./panel_install.sh`）或先 `export` 再运行脚本。

**升级前自检**：面板部署目录执行 `grep -E '^FLVX_' .env`，有输出即逐行按下表改名为同名 `FLMX_` 键；节点机上另查 `grep -rs 'FLVX_KERNEL_DIR' /etc/systemd/system/flmx_agent.service /etc/systemd/system/flmx_agent.service.d/ 2>/dev/null`（改名前装的节点单元名仍为 `flux_agent` 时，把路径换成对应的旧单元名）。

## 2.1.0 移除项（升级须知）

- **GitHub Gist 备份已移除**：2.1.0 起面板不再提供 Gist 备份与恢复；升级前请先在 **网站配置 → 数据备份与恢复** 下载一份本地备份（本地备份与自动快照不受影响；已存在于 GitHub 的 Gist 本体不会被删除）。
- **跨面板共享（联邦）已移除**：已建的分享在升级后失效；消费侧已生成的隧道行会保留，等待管理员处理。
- **自建 Snell 入站已移除**：存量自建 Snell 入站以禁用形态保留；经导入 / 订阅源进来的 Snell 节点不受影响、照常下发。
- **多主题与深色模式已移除**：界面只保留默认主题；旧版本保存在浏览器本地的主题选择升级后不再生效，无需手动处理。
