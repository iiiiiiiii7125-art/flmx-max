# 导入节点

在 **落地** 页点击 **导入**，可以把不是面板部署的节点（脚本搭的、没装 agent 的机器、机场节点）加进来，用来对接中转和加入订阅。转换规则参考 Sub-Store：所有格式先转成 Mihomo（Clash Meta）节点格式保存，再按订阅格式输出，绝大多数参数会完整保留；少数只在 sing-box 原格式里有落点的字段没有对应搬运位置、转换时会丢弃，例如 Hysteria2 的 `up_mbps` / `down_mbps`、VLESS 的 `packet_encoding`。

## 支持的输入

- 分享链接（每行一个）：`ss://`（含 obfs / v2ray-plugin / shadow-tls 插件）、`vmess://`（v2rayN 格式）、`vless://`（含 Reality、WS、gRPC、H2、XHTTP）、`trojan://`、`hysteria2://` / `hy2://`、`tuic://`、`anytls://`、`socks5://`、`naive://` / `naive+https://`（NaïveProxy，含面板自产的分享链接）
- base64 订阅内容
- Clash / Mihomo 配置（读取 `proxies`）
- sing-box 配置（读取 `outbounds`）
- Surge / Loon 节点行，例如 `名称 = snell, 1.2.3.4, 6160, psk=..., version=4`
- 订阅地址（`https://...`）：面板服务器以 `flmx-panel/1.0` 为 User-Agent 去下载（IMP-002：手册曾写"以 Mihomo 的身份"，与实际不符，已更正），拿到的通常是参数最完整的 Clash 配置。打开 **定时同步**（默认打开）会存成 [订阅源](./subscription.md#订阅源)，按时更新；关掉则只导入一次。注意：依赖 UA 嗅探返回差异化内容的订阅源，可能返回参数不完整的内容

## 手动填写

导入窗口顶部可以切到 **手动填写**：选好协议（SS / VLESS / VMess / Trojan / Hysteria2 / TUIC v5 / AnyTLS / SOCKS5），逐项填地址、端口、密码 / UUID，以及 TLS / Reality、传输方式（TCP / WebSocket / gRPC）等参数，UUID 可以点 **生成** 一键生成。点 **添加** 后，面板把填好的内容拼成标准分享链接，走与粘贴导入完全相同的解析与校验链路；添加后的节点和粘贴导入的节点一样，可以对接中转、放进订阅。Hysteria2 / TUIC / AnyTLS 作中转目标只能走 sing-box 内核，Xray 内核会拒绝。

- 表单只显示当前协议可用的字段与选项（例如 VMess 没有 Reality；Trojan / Hysteria2 / TUIC / AnyTLS 固定用 TLS）。流控（Vision）只在 VLESS + TCP 且安全不是「无」时可选；传输选 WebSocket / gRPC 或安全选「无」时选不出 Vision（已选的会被置空并提示），后端也会拒绝无 TLS 且无加密的 flow
- 地址栏只填域名或 IP，不要带端口（端口填在端口栏）；地址不能是本机地址（`127.0.0.1` 等）或保留段/组播段，带空标签的主机名（如 `a..b`）也会被拒绝；端口必须是 1–65535 的整数
- 按协议必填：VLESS / VMess / TUIC 须填 UUID；SS / Trojan / Hysteria2 / TUIC / AnyTLS 须填密码（TUIC 同时须填 UUID 和密码，是唯一同时要两样的协议）；VLESS + Reality 须填公钥（pbk）与 SNI（伪装站域名）；SOCKS5 的用户名和密码须成对填写（或都留空）；缺了会在点 **添加** 时直接提示，不会先建一个坏节点
- 名称可以留空，留空时用「地址:端口」作为节点名称

## 说明

- 暂不支持 WireGuard、SSR、Hysteria v1 等类型，导入时会逐条提示
- Shadowsocks 类条目必须带密码：1.3.4 起，无密码（或加密方式为空）的 SS / SSU / TAP 配置在节点侧会**直接报错、服务起不来**，不再像旧版本那样静默以明文运行。导入或手工编写 gost 配置、手工链里有这类条目时，请先补上密码再下发；报错信息会明确指出缺 auth。面板正常创建的 SS 入站与 SS 链恒带密码，不受影响
- 节点地址不能是本机地址（`127.0.0.1` 等 loopback 地址或 `0.0.0.0`），导入时会被逐条拒绝并提示「地址不能是本机或 0.0.0.0」
- 保留段与组播段地址同样拒绝：云元数据地址、保留地址段（如 `0.x.x.x`）与组播地址（如 `224.0.0.0/4` 段）在导入与连通性检测时同样会被拒绝；公网、私网与运营商 NAT（CGNAT）地址不受影响
- Shadowsocks 2022（`2022-blake3-*`）节点的密钥会校验格式：base64 解码后须为 16/32 字节，SIP023 冒号多段密钥按段逐段校验；不合规的单条节点会被拒绝并提示原因，其余节点照常导入。SIP023 多段合法节点在 sing-box 订阅中会逐段规范化为带填充的标准 base64 后下发（解码出的密钥字节不变；sing-box 1.11 / 1.12 / 1.14 均已实测可正常解析多段密钥）。此前已导入的异常节点（任一段密钥不合规）在**所有**订阅格式（sing-box、Clash、通用 base64、Loon、Quantumult X、Surge）渲染时都会被整节点略过、不再输出——面板不会为此单独提示，使用者在客户端里找不到该节点时多半是这个原因
- Reality 链接参数（`pbk` / `sid` / `spx` / `realityDest`）都会解析保留：`realityDest` 须为 `host:port` 形式，坏值（缺端口、端口越界、空标签主机名如 `a..b:443`）整条拒绝，与创建入站同口径；`spx` 不能含空白或控制字符。合法值存入节点参数（入站详情的导入参数中可见 `reality-opts.dest` / `reality-opts.spider-x`），分享链接会原样回放，Xray 链式出站也会透传。sing-box 订阅格式没有对应的 `dest` / `spiderX` 字段，下发时不带这两项；Clash 家族（含 Stash、Shadowrocket 的 Clash 档案）则会把 `reality-opts` 整段透传输出，`dest` / `spider-x` 会随之带出（客户端一般会忽略这两个未知字段，不影响使用）。两种情况下值都仍保留在节点记录中，再导出分享链接时还在。VMess 链接（v2rayN JSON）同样支持 `tls=reality`：`sni` / `fp` / `alpn` / `pbk` / `sid` / `spx` / `realityDest` 都会解析保留，坏值（缺 `pbk`、坏 `realityDest`、缺 SNI 等）整条拒绝；`tls` 取值不在 `tls / reality / 留空（明文）` 内时整条拒绝，不会静默降级为明文；VLESS / Trojan 链接的 `security` 取未识别值时同样整条拒绝。
- 经中转连接时，原节点用域名连接的，会自动把域名保留为 TLS SNI 和 WebSocket Host；Hysteria2 端口跳跃只在直连线路上保留
- 面板不会部署、修改这些节点，也没有流量统计、白名单、换密钥等功能
- 原节点的地址或参数变了，点击该节点 → **编辑**，修改地址或粘贴新的链接替换参数，相关中转会自动指向新地址
- 勾选“订阅里只保留中转线路”后，订阅和分享中不再包含直连线路（该节点没有中转时就不会出现在订阅里）
- 删除只会删除面板里的记录和相关中转，不影响原服务器
- 同一个地址和端口在导入节点之间不会重复导入（与面板自建入站同地址同端口不算重复，自建与导入可以共存）
- 单次导入上限 500 个节点（IMP-008）：**手动导入**（粘贴链接/内容）超过 500 个会直接拒绝，请分批导入；**订阅源同步**超过 500 个会只保留前 500 个并给出提示

## 连通性检测

面板服务器每 3 分钟会连一次所有导入的节点，落地页上方的 **检测** 按钮可以马上测一遍：

- 连得上：绿点，后面显示延迟（例如 `· 85 ms`）
- 连不上：红点，并显示原因（连接超时、域名解析失败、无法连接）

检测是从 **面板服务器** 发起的 TCP 连接，只能作为参考：面板连不上不代表中转机也连不上，反之亦然。Hysteria2、TUIC 只用 UDP，无法这样检测，不显示状态。开启 [通知](./notify.md) 后，导入节点连续两次连不上会推送提醒。

## 对接中转

导入后的节点和面板部署的入站一样：点击节点 → **中转** → 选择隧道即可，端口转发和隧道转发（包括经 IX 的多跳）都可以用。Hysteria2 / TUIC / AnyTLS 作中转目标只能走 sing-box 内核，Xray 内核会拒绝。见 [对接中转](./relay.md)。

## 订阅源地址安全策略

订阅源 URL 默认仅支持 **https 公网地址**。如需使用 http 或内网地址，需管理员显式配置：

- `FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true`：放行所有私网地址（不推荐）
- `FLMX_FETCH_PRIVATE_ALLOWLIST=192.168.0.0/16,10.0.0.0/8`：仅放行指定 CIDR（推荐）

**设置位置与生效方法**：这两个变量是**后端进程的环境变量**。随版 `docker-compose.yml` 的 `backend` 服务 `environment:` 段默认**没有**它们的传递行——只把变量写进部署目录的 `.env` 文件**不会生效**（后端读的是进程环境，不是 `.env` 文件本身）。正确做法：在部署目录的 `docker-compose.yml` 里给 `backend` 服务补上对应传递行（写字面量，或写 `- FLMX_FETCH_PRIVATE_ALLOWLIST=${FLMX_FETCH_PRIVATE_ALLOWLIST}` 再把值放进同目录 `.env`），保存后执行 `docker compose up -d backend` 重建后端容器才生效。自检：`docker exec <后端容器> printenv FLMX_FETCH_PRIVATE_ALLOWLIST` 能打印出所设值，即已进入后端进程；打印为空就是传递行没补对。全部 `FLMX_FETCH_*` 变量与 `FLMX_GIST_API_BASE` 均为同一口径。

**风险说明**：放行私网地址会允许面板服务器访问内网资源。请确保订阅源可信，并注意：
- 云元数据地址（`169.254.169.254`）在任何配置下都被拒绝
- 私网放行会记录审计日志
- 公网 http 在任何配置下都被拒绝（必须使用 https）
