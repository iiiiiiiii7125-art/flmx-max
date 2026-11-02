# 升级

## 面板内升级

**推荐升级顺序：先备份 → 先停 agent（节点侧）→ 换面板 → 再启 agent。** 该顺序同样适用于下面的脚本升级：升级前先备份（网站配置 → 数据备份与恢复 → 下载备份；脚本升级会在更新前自动备份到部署目录的 `backups/`），再到每个节点上停掉 agent（systemd 节点：`systemctl stop flux_agent`；Alpine / OpenRC 节点：`rc-service flux_agent stop`），换完面板后重新启动 agent（`systemctl start flux_agent` / `rc-service flux_agent start`）。不停 agent 直接换面板时，在线 agent 会在面板重建期间重试/重连（有重连对账兜底），但面板与 agent 版本会短暂不一致。

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

数据会保留，更新前会自动备份到部署目录的 `backups/`。

> 升级本身不会更换 `JWT_SECRET`；若升级时手工轮换了 `.env` 的 `JWT_SECRET`，所有已登录会话都会失效需重新登录，全部带 `?user=&sig=` 的个人订阅链接也会一并失效（签名用的是同一密钥），用户需在面板里重新复制链接。

> 升级不会改变在用的容器网络：脚本更新与面板内升级都会保留存量 `docker-compose.yml` 的子网与网络名，模板里的新子网（`172.30.0.0/16`）只对全新安装生效，见 [安装部署的子网提示](./install.md)。

脚本更新按版本感知执行：默认更新到最新正式版（测试版不参与默认更新）；需要定版时用 `VERSION=<版本号> ./panel_install.sh` 运行并选 `2`，会按指定版本更新。想跟进测试版时，用 `FLMX_RELEASE_CHANNEL=dev`（或 `beta`）再跑脚本，或显式 `VERSION=<测试版号>`。目标版本低于当前版本时会被**拒绝**（防降级，与面板内升级口径一致），同版本则按重装继续。当前版本以**后端容器实际运行的镜像版本**为准交叉核对：`.env` 记录与实际运行版本不一致时脚本会打印告警并按实际版本判定（取不到实际版本时才回退 `.env` 口径），防止 `.env` 被手改或停留在失败升级的目标版本时把降级误放行。

## 指定版本

到 [Releases](https://github.com/iiiiiiiii7125-art/flmx-max/releases) 找到目标版本，把下面命令里的 `<目标版本号>` 换成该版本号再执行（不要带着占位符原样执行）：

```bash
VERSION=<目标版本号>
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/download/$VERSION/panel_install.sh -o panel_install.sh && curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/download/$VERSION/panel_install.sh.sha256 -o panel_install.sh.sha256 && sha256sum -c panel_install.sh.sha256 && chmod +x panel_install.sh && ./panel_install.sh
```

## 降级到旧版本（手动）

脚本与面板内升级都拒绝降级；确需手动回退到旧版本时注意：如果用过 Shadowsocks「按订阅计量」（1.4.2 起），**回退前必须先关闭全部入站的「按订阅计量」开关，并确认计量专用入口已消失（入站列表不再显示「计量中」徽标、对应端口已释放）再回退**。旧版本不认识计量专用入口，会把它当作普通 Shadowsocks 入站对外分发，其凭据可被直接使用且不计量。

## 节点 agent 升级

**节点** 页选择节点 → **升级**，留空版本即使用最新版。也可以在节点上重新运行节点安装脚本。通过脚本更新同样防降级：目标版本低于节点现装版本时脚本会拒绝更新并说明（如确需降级请手动处理），同版本按重装继续，版本不可比时放行。

> 节点升级与面板自升级的同版本口径不同（有意差异）：节点目标版本与节点当前版本相同时，面板直接提示「已是最新版本」、不下发升级、不重启 agent；而面板自升级（含脚本更新）指定同版本会按**重装**继续执行，用于修复面板自身文件损坏等场景。两处都拒绝降级。

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
