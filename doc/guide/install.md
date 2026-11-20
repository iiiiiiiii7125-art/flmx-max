# 安装部署

面板端管理所有节点、隧道和入站；节点端（agent）装在中转机和落地机上，负责转发和运行内核。

## 面板端

面板端负责管理用户、节点和转发规则。

### 环境要求
- 操作系统：Linux (推荐 Debian 10+ / Ubuntu 20.04+)
- Docker 和 Docker Compose：无需预先安装，未安装时一键脚本会自动安装；仅手动 `docker compose` 部署（不经一键脚本）时需自行安装

### 一键安装

使用以下命令即可快速安装面板：

```bash
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh -o panel_install.sh && curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh.sha256 -o panel_install.sh.sha256 && sha256sum -c panel_install.sh.sha256 && chmod +x panel_install.sh && ./panel_install.sh
```

> 校验口径提示：这条手抄命令里脚本与它的 `.sha256` 校验文件经**同一条线路**取得，只做同源校验（单锚）；脚本运行起来之后，其首装与后续更新的下载会再经**第二条不同线路**复核校验值（双锚，口径见 [升级 - 面板自升级的下载校验](./upgrade.md#面板自升级的下载校验-跨线路第二锚)。也就是说手抄的第一跳只有单锚、脚本接管后是双锚；介意第一跳线路可信度时，可参照 README 的跨线路取法、让脚本与校验文件分走两条线路取得后再比对，或在可信网络完成首次安装。`sha256sum -c` 失败时不要绕过校验强行执行。

> 离线场景：脚本启动时默认先做自更新检查（需联网；断网/慢网时可能等待较久）。只想直接进菜单、跳过这次自更新检查时，用 `SELF_UPDATE_SKIP=1 ./panel_install.sh` 启动；跳过后仍以本地脚本版本运行，不会自动更新到新版。

**安装过程中会提示输入以下信息：**
- **前端端口**: 默认为 `6366`
- **后端端口**: 默认为 `6365`
- **数据库类型**: `1` 为 SQLite（默认）、`2` 为 PostgreSQL；选 PostgreSQL 时脚本会自动生成数据库口令与连接串，不用另外准备

脚本会自动检测系统是否支持 IPv6，并自动配置 Docker 的 IPv6 支持。

**安装后核对：可信代理子网已预填**：新装时脚本按本次部署的 Compose 网络子网（缺省 `172.30.0.0/16`）把 `FLMX_TRUSTED_PROXIES` 预填进部署目录 `.env`，后端据此采信前端容器传来的 `X-Forwarded-For`、按访客真实 IP 限流。装完后核对一行：`docker exec <后端容器> printenv FLMX_TRUSTED_PROXIES` 能打印出子网即生效；打印为空（脚本未能从 compose 读到子网时留空并告警、后端启动日志也会打 `FLMX_TRUSTED_PROXIES is empty` 警告）才需手工在部署目录 `.env` 里写 `FLMX_TRUSTED_PROXIES=<转发者所在子网>`（照缺省部署即 `172.30.0.0/16`；改过子网的用 `docker network inspect` 查在用子网按实际填），再执行 `docker compose up -d backend` 重建后端生效。完整口径（跳数、填法、自检）见下文「登录限流与可信代理跳数」一节。存量部署同样按该节核对，升级不会替你补填这个值。

### 同机多面板共存（FLMX_INSTANCE）

同一台服务器上要并存两套互不干扰的面板（一套正式、一套测试等），给第二套设置一个**实例键**：环境变量 `FLMX_INSTANCE`。带实例键的部署，其面板容器、数据卷、网络、Compose 项目名，以及它给节点生成的容器形态安装命令，都会带上 `-<实例键>` 后缀，与默认实例（不设该变量）完全分开；不设时一切命名与单套部署逐字一致，无感。

- **取值规则**：以小写字母或数字开头，其余字符只能是小写字母、数字、`-`、`_`，全长不超过 32 个字符（如 `test`、`panel2`）。安装时脚本会硬校验，取值不合法直接拒绝安装并提示；部署后若 `.env` 里被改成非法值，读回时按默认实例处理并告警。
- **设置时机**：在**首次运行安装脚本之前**、以环境变量方式传入新部署：换一个空目录，执行 `FLMX_INSTANCE=test ./panel_install.sh`。脚本会自动把实例键落盘到该部署的 `.env`，之后在该目录运行维护菜单（更新 / 卸载等）会自动读回，环境变量与 `.env` 并存时以环境变量为准。不要在首装前手工预置 `.env`：哪怕里面只有 `FLMX_INSTANCE` 一行，也会被当作已有部署触发重装守卫而拒绝。
- **派生命名全集**（以实例键 `test` 为例）：

  | 资源 | 默认实例 | 实例 `test` |
  |------|----------|-------------|
  | 后端容器 | `flmx-panel-backend` | `flmx-panel-backend-test` |
  | 前端容器 | `flmx-panel-frontend` | `flmx-panel-frontend-test` |
  | PostgreSQL 容器 | `flmx-panel-postgres` | `flmx-panel-postgres-test` |
  | SQLite 数据卷 | `flmx-sqlite-data` | `flmx-sqlite-data-test` |
  | PostgreSQL 数据卷 | `flmx-postgres-data` | `flmx-postgres-data-test` |
  | 网络 | `flmx-network` | `flmx-network-test` |
  | Compose 项目名 | `flmx-panel` | `flmx-panel-test` |
  | 节点 agent 容器（容器形态安装时） | `flmx-agent` | `flmx-agent-test` |
  | 节点 agent 宿主机数据目录 | `/etc/flmx_agent` | `/etc/flmx_agent-test` |
  | 节点 agent Compose 落位目录 | `/opt/docker/flmx-agent` | `/opt/docker/flmx-agent-test` |

- **并存注意事项**：
  - 每套面板用**独立的部署目录**：脚本会拒绝在属于另一套面板的部署目录里安装（强制重装也不放行），不要把两套装进同一个目录。
  - 两套的**前端 / 后端宿主机端口不能相同**：安装第二套时把端口改开，例如第一套用默认的前端 `6366` / 后端 `6365`，第二套改用 `7366` / `7365`。
  - 两套面板的**容器网络子网不能重叠**：随版模板缺省子网是 `172.30.0.0/16`（`docker-compose-v4.yml` / `docker-compose-v6.yml` 的 `networks: gost-network: ipam:` 段；v6 模板另有 IPv6 子网 `fd00:dead:beef::/48`），每套面板不论实例键是什么都用同一个缺省子网——实例键只改网络名（`flmx-network` / `flmx-network-<实例键>`），不改子网。装第二套前先查宿主机上已占用的子网：`docker network ls` 列出网络，再逐个查子网：<span v-pre>`docker network inspect <网络名> --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}'`</span>；或一条命令全列：<span v-pre>`docker network ls -q | xargs -r docker network inspect --format '{{.Name}} {{range .IPAM.Config}}{{.Subnet}} {{end}}'`</span>。若 `172.30.0.0/16` 已被第一套占用，第二套的 `docker compose up` 会在建网时失败（典型报错 `Pool overlaps with other one on this address space`），第一套不受影响。
  - **子网冲突的改法**：没有子网环境变量可配，改法是编辑第二套部署目录的 `docker-compose.yml`，把 `networks:` 段里的 `- subnet: 172.30.0.0/16` 改成一个 RFC1918 私网段内、与宿主机上所有在用子网（容器网络、宿主机 LAN、其他 Docker 网络）都不重叠的网段，例如 `172.27.0.0/16`；v6 模板还要把 IPv6 的 `- subnet: fd00:dead:beef::/48` 一并改成不重叠的 ULA 段，例如 `fd00:dead:beec::/48`。改完再 `docker compose up -d`。**只能在首次 `up` 之前改**：已经跑起来的部署不要手工改在用子网（会重建网络、容器 IP 全变；升级路径也会原样保留你改过的子网，见下文「登录限流与可信代理跳数」的子网提示）。改完子网后，`.env` 里的 `FLMX_TRUSTED_PROXIES` 也要同步改成新子网（填法与自检见下文「登录限流与可信代理跳数」），否则登录限流与登录记录的来源 IP 会退回容器地址。**自检**：<span v-pre>`docker network inspect <第二套网络名>`</span> 的 `Subnet` 是新子网，且 `docker network ls` 里两个面板网络的子网互不重叠；登录一次，登录记录里的客户端 IP 是真实公网 IP 即生效。
  - **不要给已部署的实例改实例键**：容器名与数据卷名都由实例键派生，改键等于换了一套新身份——会按新键起新容器、新数据卷，旧卷里的数据不会自动带过来。确需改名按迁移处理：先备份 / 导出旧实例数据，再以新键安装并恢复。
  - 旧版 Compose 模板（资源名写死旧名的版本）不识别实例后缀：装这类版本时脚本会明确告警「实例后缀不生效」，该实例仍用旧缺省名，同机不要再并存第二套该版本部署。
  - **手工拼 Compose 部署必须在 `.env` 写全身份六键**：随版模板里容器/卷/网络名都是 `${PANEL_BACKEND_CONTAINER:-flmx-panel-backend}` 这类带回退值的引用，回退值与脚本安装写入的身份同为 flmx 名；脚本安装时会自动把身份（多实例时含实例后缀）写入部署 `.env`，手工部署漏写则起出来的全是无后缀的回退名资源，两套手工部署同机会直接撞名、不具隔离性。六键清单与各自取值见 postgresql.md §四注记（`PANEL_BACKEND_CONTAINER`、`PANEL_FRONTEND_CONTAINER`、`PANEL_POSTGRES_CONTAINER`、`FLMX_SQLITE_VOLUME`、`FLMX_POSTGRES_VOLUME`、`FLMX_NETWORK`）。自检：在部署目录执行 `grep -cE '^(PANEL_BACKEND_CONTAINER|PANEL_FRONTEND_CONTAINER|PANEL_POSTGRES_CONTAINER|FLMX_SQLITE_VOLUME|FLMX_POSTGRES_VOLUME|FLMX_NETWORK)=' .env`，结果应为 `6`。
- **自检**：安装后在部署目录执行 `grep FLMX_INSTANCE .env` 应能看到实例键；`docker ps` 里该套面板的容器名都应带 `-<实例键>` 后缀；再到该面板的节点页生成一条 Docker 形态安装命令，命令里的容器名应为 `flmx-agent-<实例键>`、数据目录应为 `/etc/flmx_agent-<实例键>`。

**改名前存量部署的容器名**：全新安装的容器名即为现行名（`flmx-panel-backend`、`flmx-panel-postgres`、`flmx-panel-frontend`）。改名前已部署的实例（旧名为 `flux-panel-backend`、`flux-panel-postgres`、`vite-frontend`）会在**脚本升级时被默认改名**为现行名——只改容器名，不动数据卷、网络与数据；不想改名可在升级前设 `PANEL_IDENTITY_RENAME=0` 关闭。触发条件、动作顺序与自检方法见 [升级 - 脚本升级](./upgrade.md#脚本升级)。

### 访问面板

安装完成后，访问：
`http://<服务器IP>:<前端端口>` (默认: `http://<服务器IP>:6366`)

**默认管理员账号：**
- 用户名: `admin@user`
- 密码: 没有固定默认值。新装播种时由后端生成随机初始密码（或采用部署时指定的环境变量 `FLMX_ADMIN_PASSWORD` 的值），只用于首次登录。

**初始密码从哪里看（三条路，任选其一）：**

1. **脚本安装的完成输出**：安装成功块会直接打印一行「🔑 初始管理员密码（随机生成…）」——那就是首登密码。
2. **后端启动日志**：日志只点名文件路径、不记录密码本身。查看命令：`docker logs flmx-panel-backend 2>&1 | grep "initial admin password"`（容器名按实际部署替换），输出会给出一次性文件的完整路径。
3. **数据卷里的一次性文件**：文件名为 `initial-admin-password.txt`，容器内路径 `/app/data/initial-admin-password.txt`（权限 0600）。在宿主机上先查后端容器实际挂载的数据卷名：<span v-pre>`docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Name}}{{end}}{{end}}' flmx-panel-backend`</span>；再查该卷的宿主机挂载点：<span v-pre>`docker volume inspect -f '{{.Mountpoint}}' <上一步的卷名>`</span>；最后读 `<挂载点>/initial-admin-password.txt` 即为首登密码。若数据目录不可写（例如漏挂数据卷），后端会改落到可执行文件同级目录或系统临时目录并照常建号，启动日志会点名实际落点路径——以日志为准。

**用环境变量指定初始密码（可选）**：在部署目录 `.env` 里设置 `FLMX_ADMIN_PASSWORD=<自选值>` 后再安装（随版 Compose 模板已透传该变量），播种即采用该值、不再生成随机密码也不落一次性文件。该值同样只用于首次登录；已完成播种的部署再改这个变量不会改变现有管理员密码，可在首登改密后把该行从 `.env` 删除。

::: warning
用初始密码登录后会被**强制修改密码**：改密完成前账号只能访问改密页。改密成功后一次性文件自动删除、初始密码立即失效。
:::

::: danger 文件丢失且尚未首次登录时
初始密码没有第二份副本（日志只记路径）。若一次性文件在首登前丢失、且不是用 `FLMX_ADMIN_PASSWORD` 指定的密码，则该初始密码无法找回——此时面板刚装完、尚无业务数据，处置办法是停服后清除数据卷重新安装。若已完成首登改密，文件丢失与日常使用无关，无需处理。
:::

### 维护菜单

再次运行 `./panel_install.sh` 脚本可以看到管理菜单：
1. 安装面板
2. 更新面板
3. 卸载面板
4. 迁移到 PostgreSQL
5. 配置域名反向代理（自动 HTTPS）
6. 配置订阅前置域名（自动 HTTPS）
7. 退出


## 节点端

节点端运行在实际进行流量转发的服务器上，需要连接到面板端进行管理。

::: tip 同一宿主可部署多个 agent
同一台宿主机上可以并行运行多个 agent：每个 agent 按安装位置隔离内核目录与 nftables 表（表名由安装根目录派生），白名单物化与计量互不干扰，多节点场景无需一台宿主只装一个 agent。过渡期注意：2.1.13 之前的旧 agent 仍共用明文表名，同一宿主机混用新旧 agent 时请先把旧 agent 手动升级到 2.1.13 或更高（见升级说明）。
:::

### 用面板生成的命令安装（推荐）

1. **网站配置** 里填好“面板后端地址”，节点通过它回连面板：面板经 HTTPS（Caddy/nginx 反向代理）对外时必须填 `https://域名`（可带端口，与 `panel_install.sh` 配置域名反代时的提示一致），明文面板填 `http://面板IP:6365`。只填裸地址（不带 `https://` / `http://`）时，生成安装命令的连接方式取自你当前访问面板的方式，可能与节点回连面板的方式不一致，请优先写明 scheme；生成结果为明文 `ws://` 时面板会给出显式告警。
2. **节点** 页新建节点，点击 **安装**，会打开安装命令弹窗，在弹窗里点 **复制** 按钮复制命令（复制按钮失效时可以手动选中命令文本复制）。若生成的命令用的是明文地址（`ws://`），点击安装时面板会先弹出明文告警提示、弹窗顶部也有常驻的告警横幅，确认无误后再复制使用
3. 到节点服务器上以 root 执行

### 手动安装

在节点服务器上运行：

```bash
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/install.sh -o install.sh && curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/install.sh.sha256 -o install.sh.sha256 && sha256sum -c install.sh.sha256 && chmod +x install.sh && ./install.sh
```

Alpine Linux 最小化安装若未包含 `curl`，可使用系统自带的 `wget` 下载（与 curl 版同口径：先取校验文件、`sha256sum -c` 通过后才执行，校验失败不要绕过）：

```bash
wget -O install.sh https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/install.sh && wget -O install.sh.sha256 https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/install.sh.sha256 && sha256sum -c install.sh.sha256 && chmod +x install.sh && ./install.sh
```

> 校验口径提示：上面两条手抄命令（curl / wget）的脚本与校验文件都走同一条线路、只有单锚（同源校验）；在面板里点「安装」生成的节点安装命令里，**脚本形态**是双锚（脚本与校验文件分走两条不同线路互相复核），但有两条例外、不要按双锚预期：① 实际获胜的是直连 / 面板源线路、且另一条线路没有应答时，命令会**点名降级为单锚**继续，输出里会写明；② **docker / docker-compose 两种容器形态**的生成命令没有脚本校验链，只靠镜像 tag 拉取。能从面板复制命令时优先用生成的命令，并留意输出里对校验形态的点名。

脚本会在 Alpine 上自动安装 Bash、`curl` 和 CA 证书，并使用 OpenRC 注册、启动和管理 `flmx_agent` 服务；其他受支持的 Linux 发行版继续使用 systemd。

**安装过程中会提示输入：**
- **服务器地址**: 面板端的通信地址（通常是 `http://<面板IP>:<后端端口>`，例如 `http://1.2.3.4:6365`）。
- **密钥**: 节点页里该节点的密钥。

或者直接使用带参数的命令（适用于自动化部署）：

```bash
# 替换 <面板地址> 和 <密钥>
./install.sh -a "http://1.2.3.4:6365" -s "your_node_secret"
```

> 密钥传递提示：`-s` 的值会出现在进程参数里（安装期间本机其他用户可用 `ps` 看到）。多用户共用的节点机上，建议改用环境变量传密钥：`FLMX_NODE_SECRET="<密钥>" ./install.sh -a "http://1.2.3.4:6365"`（不带 `-s` 时脚本读该变量、读后即从环境移除）；`-s` 仅作兼容回退，两者同时给出时以 `-s` 为准。

### 验证安装
安装脚本显示“安装完成”只代表 agent 服务已启动，**不代表节点已连上面板**，请务必再做一步确认：
- 回到面板 **节点管理** 页面，该节点状态应显示为 **在线**——这是唯一算数的上线判据。
- systemd 查看状态: `systemctl status flmx_agent`
- Alpine/OpenRC 查看状态: `rc-service flmx_agent status`
- 若面板未显示在线，看 agent 的 WebSocket 连接日志（地址 scheme 是否与面板对外方式一致、是否被 308/握手拒绝等都会写在日志里），再对照下一节排查：systemd 节点执行 `journalctl -u flmx_agent -n 100`；Alpine/OpenRC 节点没有 journald，日志在文件 `/var/log/flmx_agent.log`（错误日志 `/var/log/flmx_agent.err.log`），用 `tail -n 100 /var/log/flmx_agent.log` 查看。
- 改名前安装的节点在迁移前，服务名、安装目录与日志文件仍是旧名（`flux_agent`、`/etc/flux_agent`、`/var/log/flux_agent.log`），上述命令把名字换成旧名即可；迁移只在节点机上重新执行安装命令（或脚本更新）时发生，完成后统一为新名——面板内的节点升级只换二进制、不迁布局，不做迁移也不影响使用，旧名在过渡期内仍被脚本与面板识别。

### 小内存机自动建 swap（装机防线）

节点安装/更新脚本带一条小内存防线（新装与脚本更新两条路径都会执行）：

- **触发条件**：脚本实读 `/proc/meminfo`，仅当整机内存 **≤512MiB 且当前完全没有 swap**（SwapTotal=0）时才动手；内存大于 512MiB、或已有任意 swap（含 zram/分区）时不动，只输出现状。
- **动作**：自动建 **1G** `/swapfile` 并启用，同时写入 `/etc/fstab` 持久化（重启不丢）；重复执行幂等（已建形态二跑零改动、缺 fstab 行时只补行）。任一步失败会回滚半成品并明示「swap 未建、OOM 风险在」，但不中止装机流程。
- **退出开关**：不想自动建时，在运行脚本前设置环境变量 `FLMX_SKIP_SWAP=1`（大小写不敏感）跳过本步，脚本会明示 OOM 风险自担。
- **完成后自检**：`free -h; swapon --show` ——Swap 一行应显示约 1G 且 `swapon --show` 里有 `/swapfile`。
- 背景：无 swap 的 ≤512MiB 小机上，Agent 长活驻留内存涨满后会被内核 OOM 反复杀死重启；这 1G swap 是装机即兜底。手动装机后想补建，重新跑一次安装脚本即可（幂等）。

### 节点机上已有其他面板的 agent 时（归属守卫）

脚本形态 agent 与旧版/同类面板的 agent 共用同一个安装槽：现行槽为目录 `/etc/flmx_agent`、服务 `flmx_agent`；改名前的旧槽（目录 `/etc/flux_agent`、服务 `flux_agent`）在过渡期内仍被脚本识别——机器上已有旧槽 agent 时同样先做**归属判定**，判定为本面板的在本面板安装命令（或脚本更新）执行时迁移到新槽，他人面板的照样不改动。往一台已经装有其他面板 agent 的节点机上执行本面板的安装命令时，脚本先做归属判定，判定不过不会改动现有 agent 的任何文件：

- **守卫键**：现有 agent 配置文件里的 `addr` 字段（该 agent 当前连接的面板地址；新槽看 `/etc/flmx_agent/config.json`，旧槽看 `/etc/flux_agent/config.json`，两槽都会被检查）。脚本读取后先做 JSON 转义归一化（`\/`、`\uXXXX` 等写法差异不会造成误判），再与本次安装命令的面板地址**逐字比对**：相等才认定是本面板的旧 agent、允许就地覆盖安装（面板迁移、存量升级走的正是这条路）；不相等、或根本读不到 `addr`，一律按「他人面板的 agent」处理。守卫只比地址不比密钥：地址是面板的连接端点，同机端口唯一、异机地址不同，两套不同面板不可能撞同一串地址，同串即同一端点。
- **触发面**：安装（面板生成的一键命令与手动 `./install.sh -a … -s …` 都一样）；维护菜单的**更新**与**卸载**（菜单上下文没有「本次面板地址」可比对，口径更严：非交互一律拒绝，交互时先明示现存 `addr` 再要求确认）。
- **被拒绝时长什么样**（安装、非交互路径）：

  ```
  ⚠️ /etc/flmx_agent 已存在一个 agent，其连接地址为 https://other.example.com:443，与本次面板地址 https://mine.example.com:443 不一致。
     （地址比对已做 JSON 转义归一化，写法差异不会造成误判。）
     它可能属于其他面板；继续安装会覆写它的配置、把它改连到本面板，本脚本不会未经确认这样做。
  ❌ 安装终止：未改动 /etc/flmx_agent 中的任何文件。
     如确认该 agent 属于本面板（例如面板换过地址），请在交互终端重跑并在提示时确认，或设置 FLMX_AGENT_TAKEOVER=1 后重试。
  ```

- **两种解法**：① 确认机器上的 agent 本就属于本面板（典型情形：面板换过域名/地址）——在节点的交互终端重跑脚本，在提示时输 `y`；② 自动化场景无法交互时，显式设置环境变量 `FLMX_AGENT_TAKEOVER=1` 再执行，例如 `FLMX_AGENT_TAKEOVER=1 ./install.sh -a "https://…" -s "…"`，脚本会打印一行接管留痕。**防错警告**：TAKEOVER 是「我确认这台机器的 agent 归本面板、后果自担」的开关——机器上的 agent 若真属于另一套在用的面板，接管会覆写它的配置、把它改连到本面板，对方节点随即掉线；不要为了让命令跑通随手加。
- **旧式 gost 形态的清理同样守卫**：安装/更新时脚本还会检查机器上有没有更老形态的 gost 式 agent（`gost.service` ＋ `/etc/gost` 落位）。它与旧谱系面板的 agent 完全同形、仅凭落位无法区分归属，故同样先比对 `/etc/gost/config.json` 的 `addr`：与本面板地址（或现装 agent 的地址）一致才清理；不一致或读不到时**跳过清理并在输出中明示**（以 `ℹ️ 已跳过旧式 gost 安装的清理` 开头），安装/更新本身不受影响。解法同上（交互确认或 `FLMX_AGENT_TAKEOVER=1`）。
- **自检**：动手前先看节点机上现有 agent 连的是谁：`grep -o '"addr"[^,]*' /etc/flmx_agent/config.json`（改名前装的节点看 `/etc/flux_agent/config.json`，旧式形态看 `/etc/gost/config.json`），输出的地址与本面板地址一致才属同一面板。安装被守卫拒绝后，再看 `systemctl status flmx_agent`（或 `rc-service flmx_agent status`；旧槽节点看 `flux_agent`）与上述配置文件未被改动，即守卫按预期工作。

### 与同类面板共存：装前预检

往一台已经装有其他面板（或另一套 FLMX 面板）agent 的节点机上装 agent 之前，先跑一遍下面的预检命令——只读，不改任何东西。面板只在自己的库内判重，另一套面板占用的端口它看不见，撞上了只能装前查出来。

**能改的与不能改的**：

- **面板端口可以改**：前端默认 `6366`、后端默认 `6365`，安装时输入别的端口即可；同机第二套面板必须把两端端口都改开（见上文「同机多面板共存」一节）。
- **agent 默认槽固定**：脚本形态固定占目录 `/etc/flmx_agent`、服务 `flmx_agent`（systemd 单元文件 `/etc/systemd/system/flmx_agent.service`，Alpine/OpenRC 为 `/etc/init.d/flmx_agent`），不要手工挪。例外只有两处：改名前装的旧槽（`/etc/flux_agent`、`flux_agent`）和带实例键的容器形态 agent（`/etc/flmx_agent-<实例键>`，见上文派生命名表）。
- **旧式 gost 槽**：更老形态的 agent 占 `/etc/gost`、服务 `gost.service`；归属判定与清理口径见上一节。
- **伴生 Clash API 口段固定**：`41000–41999`（面板与 agent 双侧同值，按节点密钥派生）；段内具体是哪个口不固定——派生口被占时 agent 在重启/重建时自动换一个段内空闲口，运行中的实例保持原口。旧固定口 `40900` 已退役，新实例不再用。转发、入站、隧道入口也不要选进这个段。
- **转发入口不要重叠**：同一台节点机上，两套面板/两个 agent 的转发、入站、隧道入口端口不能撞——一个端口同一时间只能被一个监听者占用。面板建转发/隧道时用统一的四源谓词判重（转发/入站/隧道/伴生口），但只覆盖本面板自己的库；跨面板的冲突它拦不住，只能装前查。
- **nft 表名按安装根派生、不是任意一个固定名**：默认脚本形态（内核根 `/etc/flmx_agent/kernels`）与容器形态都用 `flmx_kernel_<该根路径 sha256 前 8 位十六进制>`，不再用 plain `flmx_kernel`；改名前旧根（`/etc/flux_agent/kernels`）用 `flmx_kernel_legacy`；自定义 `FLMX_KERNEL_DIR` 或挪位安装的根同样按路径哈希。旧名 `flvx_kernel` 与 plain `flmx_kernel` 只在一次性升级迁移时删（升级后首次成功同步，且该 agent 此前确有成功下发的旧签名、曾拥有 plain 表；迁移后即记已迁移，此后同步不再删），卸载时另行清理；平时同步不碰它们。面板侧 nftables 模式的主表同样按安装隔离：`flmx_<sha256(实例键 | 安装身份)前 8 位十六进制>`（与 agent 侧同口径；安装身份未配置时回落为只按实例键派生，默认实例为 `flmx_e3b0c442`）；另有一张暂停/配额表 `flmx_quota_<同一安装哈希>`。plain 面板表 `flmx`/`flvx` 也只在升级迁移时删，且须持归属证明（本面板曾对该节点成功下发过 nft 规则）——无证明时节点上的 plain 表可能是另一套面板的 live 表，绝不能碰。所以预检时不要只查一张表，把 `flmx_*` 的表全列出来比对。

**粘贴即用的预检命令**（只读；没有 systemd 或 nft 的机器对应行只打印一行提示，不报错退出）：

```bash
echo '== 监听端口 =='; ss -tulnp 2>/dev/null || echo 'ss 不可用'
echo '== agent / 旧式 gost 落位 =='; ls -lad /etc/flmx_agent /etc/flux_agent /etc/gost 2>&1
echo '== 相关 systemd 单元 =='; systemctl list-units --all 'flmx_agent*' 'flux_agent*' 'gost*' 2>/dev/null || echo '无 systemd 或查询失败'
echo '== nft 表 =='; nft list tables 2>/dev/null || echo 'nft 未安装或无权限（需 root）'
```

其中 `ss -tulnp` 覆盖 TCP 与 UDP（任务书所说的 `ss -tlnp` 只看 TCP；转发含 UDP 入口，故多看 `-u`）。

**怎么读结果**：监听端口里出现 `6365`/`6366`（或你改过的面板端口）说明该机已有面板在跑；`/etc/flmx_agent` 或 `/etc/flux_agent` 存在说明已有 agent——再按上一节查它 `config.json` 里的 `addr` 归属；`/etc/gost` 存在说明有旧式 gost 形态；`flmx_kernel*` / `flmx_quota` 表存在说明已有 agent 或面板侧 nft 规则在用这台机器。以上任一项命中，都不要直接装：先按上一节做归属判定，或把端口/实例错开。

### 内核目录覆盖（FLMX_KERNEL_DIR）

agent 把各内核的二进制、`state.json` 等状态文件与 geodata 都放在一个**内核状态根目录**下。默认不用设置：脚本形态是 `/etc/flmx_agent/kernels`（改名前装的节点迁移前为 `/etc/flux_agent/kernels`），容器形态由镜像把该目录钉在数据卷上（容器重建不丢内核状态）。要改到别的目录，用 agent 进程的环境变量 `FLMX_KERNEL_DIR=<绝对路径>` 覆盖（旧名 `FLVX_KERNEL_DIR` 的双读已于 2.0.0 退场，不再识别）。

- **设置方法（脚本形态）**：执行 `systemctl edit flmx_agent`，在覆盖文件里写 `[Service]` 段加一行 `Environment=FLMX_KERNEL_DIR=<绝对路径>`，保存后 `systemctl daemon-reload && systemctl restart flmx_agent` 生效。容器形态下此变量由镜像与面板生成的安装命令管理，**不要手工改**——改了会与数据卷锚点口径冲突，容器重建后内核状态对不上。
- **防错**：目录必须落在**持久化存储**上（放进容器可写层或内存盘，重启 / 重建即丢全部内核状态）；换目录等于换一整套内核状态根，旧目录的内核与状态**不会自动迁移**，切换后按全新根由面板重新下发，切换前先确认能接受节点内核短暂重建。
- **自检**：重启 agent 后，`systemctl show flmx_agent -p Environment` 应能看到所设值，且所设目录下应出现 `state.json` 等状态文件。

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

::: tip
面板用户可直接运行 `panel_install.sh`、选菜单 **5. 配置域名反向代理（自动 HTTPS）**，脚本会自动安装 Caddy 并生成配置，无需手工执行本节步骤；以下手工步骤仅供独立部署 Caddy 时参考。**前置（硬性）**：菜单 5 与菜单 6（配置订阅前置域名）依赖 **systemd**——脚本会检测 `systemctl`，无 systemd 的系统（如 Alpine / OpenRC 面板机）会被直接拒绝并提示「本功能依赖 systemd…操作未执行」；这类系统请按下面手工步骤配置 Caddy，不要指望菜单自动完成。
:::

```bash
# Debian / Ubuntu
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
# GPG key：先下载到临时文件并核对官方指纹，通过后才写入 keyring
# （与 panel_install.sh 同口径；各步以 && 串联，任一步失败即停步、不要继续）
caddy_key_tmp=$(mktemp) && \
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' -o "$caddy_key_tmp" && \
gpg --show-keys --with-colons "$caddy_key_tmp" | grep -q '65760C51EDEA2017CEA2CA15155B6D79CA56EA34' && \
sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg "$caddy_key_tmp" && \
rm -f "$caddy_key_tmp"
# 源列表：同样先落临时文件，下载成功且非空才写入 /etc/apt
caddy_list_tmp=$(mktemp) && \
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' -o "$caddy_list_tmp" && \
test -s "$caddy_list_tmp" && \
sudo tee /etc/apt/sources.list.d/caddy-stable.list < "$caddy_list_tmp" > /dev/null && \
rm -f "$caddy_list_tmp"
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
- 使用 Caddy 反向代理后，可以让前端端口只监听本机，避免绕过域名直接暴露。**不要**把 `.env` 的 `FRONTEND_PORT` 写成 `127.0.0.1:6366` 这种「IP:端口」形态：该字段必须是纯数字端口，写成这种形态后脚本更新会直接报「FRONTEND_PORT 不是有效端口」并终止，脚本菜单 5 / 6 读端口时也会解析失败并回退到默认端口 6366（自定义端口时 Caddy 会反代到错的端口）；回退不是无声的：解析未命中时脚本会当场打印告警（说明将按 6366 生成反代配置，并给出修正方法——cd 到部署目录重跑菜单，或以 `PANEL_DEPLOY_DIR` 指定部署目录后重试），菜单 5 / 6 配置完成时还会打印一行「实际反代端口: 127.0.0.1:<端口>」供核对，请确认该端口与面板前端端口一致再继续。正确做法是 `FRONTEND_PORT` 保持纯数字，另在**部署目录**（放 `docker-compose.yml` 与 `.env` 的目录）新建 `docker-compose.override.yml`，把端口映射覆盖为仅本机绑定（映射里的宿主端口与 `.env` 的 `FRONTEND_PORT` 保持一致）：
  ```yaml
  services:
    frontend:
      ports: !override
        - "127.0.0.1:6366:80"
  ```
  其中 `!override` 不可省略（省略时新旧两条映射会叠加、宿主端口必然冲突），且需要 Docker Compose 2.20 及以上。保存后在部署目录执行 `docker compose up -d frontend` 生效；自检：`docker compose ps` 里 frontend 的端口应显示为 `127.0.0.1:6366->80`，且从外网直接访问 `http://<服务器IP>:6366` 应不通、经域名访问正常。该 override 文件独立于 `docker-compose.yml`，脚本更新替换主 compose 文件时不会动它。
- **反代开启访问日志时，先脱敏 query 再落盘**：面板的节点实时连接（`/system-info`）与流量上报（`/flow/*`）把凭据放在 URL 的 query 里——两类凭据共用同一个 `secret` 参数：节点上报时它的值是节点密钥，浏览器打开节点实时连接时它的值是登录 JWT（有效期 7 天）；并不存在单独的 `token` 参数。面板自身的请求日志只记路径、不记 query，但反代的访问日志默认记录完整 URL，开启后这些凭据会以明文写入日志文件，拿到日志即拿到可用凭据。脚本菜单 5 生成的 Caddyfile 已自带过滤式 `log` 块（filter 编码删 `secret` 查询键、JSON 封装、写入 Caddy 默认输出目标），菜单部署已达标、无需另行处理；Caddy 默认不记录请求日志（站点块没有 `log` 指令即不记），手动部署自行启用时在站点块里用 filter 编码删掉 `secret` 这个 query 键（示例里顺带删的 `token` 是防御性冗余：当前没有接口用它传凭据，多删无害，防将来新增面漏网）：
  ```txt
  log {
      format filter {
          request>uri query {
              delete secret
              delete token
          }
      }
  }
  ```
  改完先 `caddy validate --config /etc/caddy/Caddyfile` 校验、再重启 Caddy，自检：在日志文件里执行 `grep -c 'secret=' <日志文件>` 应为 0。用 nginx 时同理：默认 `combined` 格式的 `$request` 含完整 URL，不要对面板站点使用含 `$request`、`$request_uri` 或 `$args` 的日志格式——或直接 `access_log off;`，或定义只记不带 query 的 `$uri` 的格式并引用它：
  ```nginx
  log_format panel_no_query '$remote_addr - $remote_user [$time_local] "$request_method $uri $server_protocol" $status $body_bytes_sent';
  access_log /var/log/nginx/panel.access.log panel_no_query;
  ```
  改完 `nginx -t` 校验并 reload，自检同上（日志中不应再出现 `secret=`）。
- **节点 secret 仍走 URL query（C4-05 与 OPEN-2 已合并处理）**：上面说的是同一件事的另一面——agent 到面板的四个通道都把节点 secret 放在 URL 的 query 里：实时连接（`/system-info`）、流量上报（`/flow/upload`）、配置拉取（`/flow/config`）、连接日志（`/flow/connlog/v2`，复用上报 URL 只改路径、query 原样跟随）。目前的残留敞口只在**运维自加的前置反代 / WAF / 日志采集**把完整 URL 落盘时；面板自身的请求日志只记路径、不记 query，脚本生成的 Caddy 已过滤 `secret`，nginx 按上条配好即无此口径。分阶段迁移法（本轮不做——需 agent 与面板协同发版）：① 面板先同时接受新请求头 `X-FLMX-Node-Secret` 与 query；② agent 改为头与 query 双发；③ 待最低支持 agent 版本都发头后，面板再下掉 query 支持、发版说明里公告。只发头不下 query 等于没改——在 query 下掉之前不要动这四处任一处的 query 读取。浏览器打开的实时连接不在此列：它走 30 秒单次票据，不带节点 secret。

### 登录限流与可信代理跳数

面板的登录限流按客户端真实 IP 生效。当面板前面有反向代理时，后端通过
`X-Forwarded-For` 从右向左按可信跳数解析真实 IP：

| 你的拓扑 | 跳数设置 |
|---|---|
| Caddy → 前端 → 后端（`panel_install.sh` 默认：脚本生成的 Caddy 配置反代的是**前端端口**，再由前端容器的 nginx 把 API 请求转给后端，链上共两跳；按本文手工配 Caddy 反代前端端口同理） | `2`（随版模板默认即 2，**开箱即对、无需任何设置**） |
| nginx（frontend 容器）→ 后端（docker 默认、没有 Caddy） | `1`（须在 `.env` 显式覆盖，见下） |
| Caddy 直接反代**后端端口**（自备配置、绕过前端容器） | `1`（须在 `.env` 显式覆盖，见下） |

随版 compose 模板（`docker-compose-v4.yml` / `docker-compose-v6.yml`）的 backend `environment:` 已自带传递行，默认就是默认拓扑的两跳：

```yaml
      FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}
```

`panel_install.sh` 安装、更新与迁移时都会保证这行存在（旧 compose 缺行时自动补入），面板自升级覆写 compose 时也会保留这行与已显式设置的值——所以默认拓扑下什么都不用做。**自定义拓扑的覆盖方法**：在部署目录的 `.env` 里写一行 `FLMX_TRUSTED_PROXY_HOPS=<你的跳数>`（如单跳写 `=1`），然后 `docker compose up -d backend` 重建后端生效；可填值就是链上可信代理的跳数（正整数）。**防错警告**：只有用随版模板或脚本安装时传递行才必然存在；若你手工自备 compose，请照上面那行原样加进 backend 的 `environment:`——只写 `.env`、没有传递行，变量进不了后端进程、不会生效，此时会回落到后端代码默认值 1。

跳数配少了，限流键会取到代理自己的 IP（多用户共享一个桶）：例如两跳拓扑下生效值是 1（手工 compose 丢了传递行、或把 `.env` 误设成 1），全体登录用户会共用同一个 10 次/分钟的桶，别人连续输错几次密码就能把所有人的正常登录连带限流（返回 429）。配多了，
面板会拒绝信任 XFF、回退到直连对端 IP（同样是代理 IP、共享桶），并
在日志里告警提示检查配置。两种配错都不会取到攻击者伪造的条目。**自检**：后端启动日志会打印 `proxy trust: FLMX_TRUSTED_PROXY_HOPS=<生效值>`，请核对它与你的实际拓扑一致；出现「多人共用一个桶」式的连带 429 时先查这个生效值。面板直连公网（无代理）时忽略 XFF。

后端对 XFF 的采信先按**直接对端是否可信**判别，再谈跳数。可信判别只有两条（C4-01 起）：对端是**回环**，或对端落在 `.env` 的 **`FLMX_TRUSTED_PROXIES`** 显式登记的 CIDR 名单内。除此之外的对端——**包括 RFC1918 私网在内**——一律不采信其 XFF：

- **直接对端是公网地址**（面板直连公网、前面没有代理）：XFF 整体忽略，限流键恒为对端 IP。此时 XFF 只能是对端自填的，轮换它换限流桶的做法无效。
- **直接对端可信**（本机回环反代，或容器网络里的前端等已登记进名单的代理）：才按上表的跳数从右向左解析真实 IP；跳数超过实际可信链长时回退对端 IP 并告警（见上）。
- **直接对端是未登记的私网/内网地址**：XFF 不被采信，限流键恒为对端真实 IP。内网客户端不经代理直连后端端口时，它自填的 XFF 不再能换限流桶——这正是收窄的目的；后端端口仍不要直接暴露在不可信范围内。需要内网直达且想按真实客户端 IP 限流时，把该直连来源子网登记进名单即可，但须清楚：**登记即信任**，名单内任何机器都能伪造 XFF 冒充任意 IP，登记范围只应覆盖真实转发者所在子网，绝不要填 `0.0.0.0/0`。

**名单的填法**：部署目录 `.env` 写 `FLMX_TRUSTED_PROXIES=<转发者所在子网 CIDR>`（随版模板缺省子网为 `172.30.0.0/16`，照缺省部署即填此值；改过子网的用 `docker network inspect <面板网络名>` 查 `IPAM.Config` 的 `Subnet` 按实际填；链上多跳用逗号分隔）。随版 compose 模板的 backend `environment:` 已自带该变量的传递行，脚本安装/更新都会保留；手工自备 compose 须补 `FLMX_TRUSTED_PROXIES: ${FLMX_TRUSTED_PROXIES:-}` 一行，只写 `.env` 不生效。改完 `docker compose up -d backend` 重建后端生效。**自检**：换一个网络（如手机流量）登录一次，登录记录里的客户端 IP 是新网络的公网 IP 即生效，仍是容器地址即子网没填对；但名单经 `printenv` 核对无误仍是容器地址时，先查跳数——没有 Caddy、只经 `:6366` 直连的部署链上只有 1 跳，须在 `.env` 另写 `FLMX_TRUSTED_PROXY_HOPS=1` 再重建后端（跳数配多了后端会 fail-closed 回前端容器 IP，见上文跳数表）。存量部署升级后的完整口径见 [升级](./upgrade.md) 的「可信代理名单收窄」一节。

> **唯一的例外**：节点安装命令的 scheme 推断（后端据 `X-Forwarded-Proto` 判断生成的安装命令里节点回连地址用 ws 还是 wss）不走上面的名单口径，仍按「回环/私网/链路本地即本机代理链」的宽口径判别——默认拓扑下后端对端就是私网里的前端容器，若按名单口径它恒不可信、HTTPS 面板的安装命令会被误写成明文 ws。该面只在管理员生成安装命令时出现，产物仅是链接 scheme（且明文结果会附警告），不参与登录限流与任何鉴权判定；登录限流的可信判别仍以上文名单口径为准。

限流的具体阈值（固定值，不在设置里开放调整）：

| 维度 | 阈值 | 说明 |
|---|---|---|
| 来源 IP | 10 次 / 分钟 | 每个 IP 独立计数，超限返回 429 |
| 来源 IP（被收紧后） | 3 次 / 分钟（收紧状态持续 30 分钟） | 某用户名触发下方信号且未启用登录验证码时，该来源 IP 被收紧 |
| 用户名 | 30 次 / 10 分钟 | 只作信号与渐进延迟依据，**永不直接锁账号**（防用错密码把正常用户锁死） |

登录成功签发的会话（JWT）有效期为 **7 天**。

> 密钥轮换提醒：`.env` 里的 `JWT_SECRET` 同时也是个人订阅链接签名（`?user=&sig=`）的密钥。轮换 `JWT_SECRET` 后，所有已登录会话都会失效需重新登录，全部带 `?user=&sig=` 的个人订阅链接也会一并失效，用户需在面板里重新复制链接。

> 子网提示：旧版本部署的容器网络子网是 `172.80.0.0/16`，它不在 RFC1918 私网段内。注意 C4-01 之后判别口径已与「是不是私网段」脱钩：**任何子网都不是默认可信来源**——`172.80.0.0/16` 不可信，换成模板的 `172.30.0.0/16` 同样不会自动可信；后端只采信回环与 `FLMX_TRUSTED_PROXIES` 显式登记名单内的对端（填法见上文）。因此经前端进来的登录请求想按访客真实 IP 限流，须把**在用子网**登记进名单，而不必为此改子网：存量 `172.80.0.0/16` 部署就在 `.env` 里写 `FLMX_TRUSTED_PROXIES=172.80.0.0/16` 并重建后端，即可恢复真实 IP 粒度（登记即信任该子网内的机器可伪造 XFF，容器网络里只有自家前端容器时风险可控；不要顺手登记超出实际转发者的网段）。新安装的模板子网为 `172.30.0.0/16`，**该模板子网只对全新安装生效**：1.3.4 起两条升级路径（脚本更新与面板内升级）都会把存量 `docker-compose.yml` 里在用的子网与网络名原样保留进新文件，不会因为升级模板而重建容器网络。**存量部署也不要手工改在用子网**（会重建容器网络、容器 IP 全变）——按在用子网登记名单即可，不需要动网络本身。

### 日志脱敏豁免前缀（FLMX_LOG_REDACT_EXEMPT_PREFIXES）

面板写日志时会对 URL 脱敏：query 整体丢弃，路径里疑似 token 的随机段掩码成 `***`（字母与数字混合的段长度 ≥ 8 即掩；纯字母或纯数字、无分隔符的整段长度 ≥ 16 才掩）——订阅源拉取等外发 URL 的日志走的也是这条规则，机场订阅链接里的 path 型 token 因此不会明文落盘。只有在排障确实需要看原文路径、且该路径段确知是**公开资源 ID 而不是凭据**时，才用这个变量豁免：`FLMX_LOG_REDACT_EXEMPT_PREFIXES` 是**后端进程的环境变量**，取值为逗号分隔的 URL 路径前缀（每项以 `/` 开头），路径命中任一前缀的 URL 在日志里**整条路径原文落盘**、不再做段掩码；旧名 `FLVX_LOG_REDACT_EXEMPT_PREFIXES` 的双读已于 2.0.0 退场、不再识别；不设（默认）即无豁免。

> **风险警告（这是放松脱敏的开关）**：命中豁免前缀的路径若实际含凭据（典型如机场订阅链接 `https://机场/link/<token>`），token 会**明文写进面板日志文件**，日志一旦外发排障就等于凭据外泄。只豁免确知段内容为公开 ID 的前缀，排障完及时撤下。另注意：豁免只作用于上述「疑似 token 段」的启发式掩码，面板自身订阅路径 `/api/v1/open_api/sub/<token>` 的硬掩码不受豁免影响、永远掩码。

- **设置位置与生效方法**：与 `FLMX_FETCH_*` 同口径——随版 `docker-compose.yml` 的 `backend` 服务默认没有它的传递行，只写部署目录 `.env` 不会进入后端进程、不会生效。需在部署目录 `docker-compose.yml` 的 `backend` 服务 `environment:` 段补上 `- FLMX_LOG_REDACT_EXEMPT_PREFIXES=<前缀1>,<前缀2>`（或写 `- FLMX_LOG_REDACT_EXEMPT_PREFIXES=${FLMX_LOG_REDACT_EXEMPT_PREFIXES}` 再把值放进同目录 `.env`），再执行 `docker compose up -d backend` 重建后端容器才生效。
- **自检**：`docker exec <后端容器> printenv FLMX_LOG_REDACT_EXEMPT_PREFIXES` 能打印出所设值，即已进入后端进程；打印为空就是传递行没补对。再触发一次命中该前缀的外发请求（如刷新对应订阅源），日志里该 URL 的路径应为原文，即豁免已生效。

### 登录验证码（Cloudflare Turnstile）

在 **网站配置 → 基本设置** 里可以开启登录验证码，三项缺一不可：先打开 **启用验证码** 开关，再填入 **Cloudflare Site Key** 与 **Cloudflare Secret Key**（只填两把钥匙、不打开开关，不会生效）。开启后，登录页会出现人机验证，登录请求必须携带有效验证结果才会被处理。未配置密钥时该功能不启用、登录流程与原来一致。核验路径要分清：登录页的常态流程是把 Turnstile 的验证结果随登录请求一并提交、由后端在处理登录时当场向 Cloudflare 核验，**不依赖任何预先铸造的令牌**；面板另有一个预校验接口（`/captcha/verify`），先行通过它校验会铸造一枚一次性令牌，该令牌只在登录提交它时被核销作废——日常登录并不经过这条令牌路径，不要把「登录必须带有效验证结果」读成「登录必须先领一枚令牌」。两把钥匙的区别要分清：**Secret Key** 是敏感凭据，只存面板配置，保存后不回显、任何角色（含管理员）都无法读回已保存的值——配置页里该字段恒为空待填是正常现象，不要据此判断没保存成功；**Site Key** 按 Turnstile 的设计本来就是公开值，会随登录页下发、在未登录的浏览器里渲染验证组件，公开它没有安全影响。它同时影响上文的限流收紧逻辑（启用验证码后，用户名信号不再触发 IP 收紧）。

两把钥匙要先在 Cloudflare 控制台创建 Turnstile 站点组件取得：登录 Cloudflare 控制台 → 左侧 **Turnstile** → **添加站点**，站点名随意、域名填面板的访问域名，创建后在该站点的设置里复制 **Site Key** 与 **Secret Key**，分别填回面板的 **Cloudflare Site Key** / **Cloudflare Secret Key** 字段。自检：保存并打开 **启用验证码** 开关后退出登录，登录页出现人机验证组件即已生效；组件不出现时，先检查开关与两把钥匙是否都已填写。

升级见 [升级](./upgrade.md)。

## 外观（网站配置）

### 品牌与外观字段

同在 **网站配置** 页保存。**品牌设置**卡片：

| 字段（配置键） | 语义 |
|---|---|
| 应用名称（`app_name`） | 在浏览器标签页和导航栏显示的应用名称 |
| 网页角标 Logo（`app_logo`） | 页面左上角的导航角标，上传后自动转换为 PNG 并持久化保存 |
| 浏览器缩略图标（`app_favicon`） | 浏览器标签页图标，上传后自动转换为 PNG 并持久化保存 |
| 隐藏页面底部 FLMX 版权信息（`hide_footer_brand`） | 开关，开启后页面底部不再显示 Powered by FLMX |

**基本设置**卡片中的外观类字段：

| 字段（配置键） | 语义 |
|---|---|
| 主界面壁纸（`app_bg_image`） | 登录后主界面的背景，有两个上传器：「默认背景」即本键 `app_bg_image`，是未单独设置覆盖时的兜底背景；「☀️ 壁纸」（`app_bg_image_light`）是单独上传的覆盖背景，设置后主界面优先显示它、未设置时才用默认背景。全站只有亮色模式，无需分模式上传 |
| 登录页壁纸（`login_bg_image`） | 登录界面的背景壁纸，留空则跟随主界面壁纸 |
| 规则页面精简模式（`forward_compact_mode`） | 开关（全局配置），开启后规则页面列表使用精简样式 |
