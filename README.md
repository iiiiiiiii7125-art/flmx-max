<div align="center">

<img src="https://raw.githubusercontent.com/iiiiiiiii7125-art/flmx-max/main/doc/public/logo.jpg" width="88" alt="FLMX" />

# FLMX

转发 + 节点搭建面板

在一个面板里管理中转与落地：一键部署 Xray / sing-box / Snell 节点，对接中转，生成订阅。

[使用文档](https://iiiiiiiii7125-art.github.io/flmx-max/) · [演示站](https://iiiiiiiii7125-art.github.io/flmx-max/demo/) · [快速上手](https://iiiiiiiii7125-art.github.io/flmx-max/guide/getting-started) · [下载](https://github.com/iiiiiiiii7125-art/flmx-max/releases)

</div>

## 功能

- **多内核节点**：Xray、sing-box、Snell v4 / v5 / v6（v6 为 beta）按节点安装；VLESS Reality、Hysteria2（端口跳跃）、TUIC、AnyTLS、NaïveProxy、ShadowTLS 等模板一键创建
- **证书**：自签、节点上的证书文件，或由 sing-box 自动申请（ACME，HTTP / Cloudflare / 阿里云 DNS 验证）
- **对接中转**：端口转发、隧道转发（gost / realm 双引擎），或"前置 → IX → 落地"多跳线路；入站白名单、真实客户端 IP
- **导入节点**：粘贴分享链接、Clash / sing-box / Surge 配置或订阅地址，别处搭好的节点也能对接中转
- **订阅源**：订阅链接或直接上传订阅文件（Base64 / Clash / sing-box）接入，可标用途（普通 / 中转 / 落地）；链式代理可按整个订阅源设角色，机场改名增删自动跟上
- **订阅**：多个订阅各自选落地、过滤、改名、加国旗、按地区分组，可分享给他人（独立凭据、到期自动断开）；按客户端自动返回 Clash / Mihomo、sing-box、Shadowrocket、Loon、Quantumult X、Surge、Stash 或通用 base64（Snell 按来源分面下发：自建入站下发到 Surge、Stash、Shadowrocket 与 sing-box 1.14+，不进 Clash / Mihomo；导入的 Snell 节点按版本下发，Clash / Mihomo 仅收 v1–v3）；支持按入站（`?inbound=`）、按中转（`?relay=`）单独订阅
- **入站分享地址**：每个入站可选自动 / 节点 IPv4 / 节点 IPv6 / 自定义域名，分享链接、订阅下发与链式下一跳统一按此地址生成
- **订阅流量统计**：订阅详情与"我的订阅"里按小时 / 日 / 月看上下行曲线（整条订阅的合计流量），并标注统计起算日期
- **订阅分配**：按订阅给用户分配访问权限（订阅管理里建好订阅后逐个分配给用户，删除订阅自动清除其分配关系）；每个订阅可用中转范围（relayIds）限定只包含直连或指定中转的线路；用户在"我的订阅"页只看到自己被分配的订阅（链接认订阅 token 不认人：删分配只收回面板可见性、已复制的链接仍可拉取，真要断链须重置或删除订阅）
- **订阅规则引擎**：分流规则开关 + 规则分组多选（广告拦截、AI 服务、YouTube、Netflix 等）；开启后 Clash/sing-box 输出策略组和规则，Loon/QX/Shadowrocket 输出完整配置
- **订阅脚本**：自定义 JS 改名脚本（goja 沙箱，1 秒超时），死循环自动终止
- **流量转发**：TCP / UDP 端口转发与隧道转发，配额、限速、单向或双向计费（按入 / 出方向如实统计，gost / realm 双引擎同一计费口径）
- **节点延迟**：自定义延迟测试点，节点页卡片/列表显示各点延迟（绿/橙/红），节点可自选测试点；探测带排队与失败原因区分（超时 / 拒绝 / 节点无响应），节点重连、内核重启后自动重测
- **节点本月流量**：节点页按网卡口径显示本月流量（上行 / 下行），是整机用量参考，与订阅统计、转发计费口径分开
- **节点调优**：一键应用 BBR、缓冲区、TCP 等 6 组 sysctl 优化，重启保持，可恢复默认
- **回程路由**：自动识别 CN2/163/4837/9929/CMI 线路，延迟旁显示线路名
- **GitHub 下载加速**：内核 / 规则下载与版本检查由面板与节点共用内置候选线路自动测速直选，面板侧已无单独的代理加速设置；版本列表带短时缓存（内核版本列表 30 分钟、面板升级检查 10 分钟）
- **玻璃界面与壁纸**：适中透明度的毛玻璃界面，支持自定义壁纸透过面板呈现；小字信息走不透明托盘，文字对比度不打折
- **数据备份**：网站配置里一键备份 / 恢复，用户、节点、入站、订阅与分配关系全量导出、换机还原
- **面板内升级**：检查更新、一键升级
- **与既有面板共存**：节点安装脚本带归属守卫——机器上已有的旧式 gost 安装只有在连接地址与本次安装地址对得上时才会被接管清理；对不上的既有安装一律不动，并在安装输出中点名说明跳过了什么，装过同系其他面板的机器可并行安装、互不影响
- **旧 agent 兼容窗口**：旧版节点 agent 的 v1 流量上报继续入账至 2027-02-02；此后无序号（无重放防护）的旧上报形态整批拒收，请在此之前把节点 agent 升级到当前版本（带序号的 v1 与 v2 上报不受影响）

## 安装

面板（需要 Docker，脚本会自动安装缺失的 Docker，支持 `DOCKER_MIRROR=aliyun`）：

```bash
curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh -o panel_install.sh && curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh.sha256 -o panel_install.sh.sha256 && sha256sum -c panel_install.sh.sha256 && chmod +x panel_install.sh && ./panel_install.sh
```

国内机器 GitHub 直连慢的话，可以让脚本本体走代理加速下载，但校验文件必须走 GitHub 直连获取——两条线路互相复核，代理无法同时替换脚本与校验值伪造出自洽的一对；校验不一致时 `sha256sum -c` 会失败并中止，不要绕过校验执行：

```bash
curl -L https://gcode.hostcentral.cc/https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh -o panel_install.sh && curl -L https://github.com/iiiiiiiii7125-art/flmx-max/releases/latest/download/panel_install.sh.sha256 -o panel_install.sh.sha256 && sha256sum -c panel_install.sh.sha256 && chmod +x panel_install.sh && ./panel_install.sh
```

选 `1. 安装面板`，完成后访问 `http://服务器IP:6366`，默认账号 `admin@user`，密码没有固定默认值：新装播种时由后端生成随机初始密码（或采用部署时指定的环境变量 `FLMX_ADMIN_PASSWORD` 的值），从安装完成输出或一次性文件 `initial-admin-password.txt` 查看，只用于首次登录，登录后会被强制修改密码。

菜单另有 `配置域名反向代理（自动 HTTPS）`，输入域名即可自动签发证书并反代面板。

节点：在面板 **节点** 页新建节点，复制生成的安装命令到服务器上执行。

中转机 / 落地机（把下面命令里的面板地址和密钥换成你自己的）：

> 提示：直接复制面板 **节点** 页生成的安装命令即可——命令会逐个候选试取安装脚本，脚本运行后内置多代理自动测速直选（先测直连、直连不通按延迟选代理），无需再手工拼代理前缀或设置面板里的 GitHub 加速（该设置已移除）。确需固定某条路线时，可在执行安装脚本时用环境变量 `PROXY_URL=<代理前缀>` 手动覆盖，或 `PROXY_ENABLED=false` 强制直连。

更多说明见 [使用文档](https://iiiiiiiii7125-art.github.io/flmx-max/)：[安装部署](https://iiiiiiiii7125-art.github.io/flmx-max/guide/install) · [搭建节点](https://iiiiiiiii7125-art.github.io/flmx-max/guide/nodes) · [对接中转](https://iiiiiiiii7125-art.github.io/flmx-max/guide/relay) · [导入节点](https://iiiiiiiii7125-art.github.io/flmx-max/guide/import) · [升级](https://iiiiiiiii7125-art.github.io/flmx-max/guide/upgrade) · [常见问题](https://iiiiiiiii7125-art.github.io/flmx-max/guide/faq)

## 免责声明

本项目仅供个人学习与研究使用，仅限合法、合规用途。使用者须确保其使用行为符合所在国家或地区的法律法规，并自行承担使用本项目带来的一切风险。作者不对因使用本项目导致的任何后果承担责任，亦不提供任何形式的担保。禁止将本项目用于网络攻击、数据窃取、非法访问等任何违法或未经授权的行为。
