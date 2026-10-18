<div align="center">

<img src="doc/public/logo.png" width="88" alt="FLVX" />

# FLVX-mod

转发 + 节点搭建面板

在一个面板里管理中转与落地：一键部署 Xray / sing-box / Snell 节点，对接中转，生成订阅。

[使用文档](https://iiiiiiiii7125-art.github.io/flvx-mod/) · [快速上手](https://iiiiiiiii7125-art.github.io/flvx-mod/guide/getting-started) · [下载](https://github.com/iiiiiiiii7125-art/flvx-max/releases)

</div>

## 功能

- **多内核节点**：Xray、sing-box、Snell v4 / v5 按节点安装；VLESS Reality、Hysteria2（端口跳跃）、TUIC、AnyTLS、NaïveProxy、ShadowTLS 等模板一键创建
- **证书**：自签、节点上的证书文件，或由 sing-box 自动申请（ACME，HTTP / Cloudflare / 阿里云 DNS 验证）
- **对接中转**：端口转发、隧道转发，或"前置 → IX → 落地"多跳线路；入站白名单、真实客户端 IP
- **导入节点**：粘贴分享链接、Clash / sing-box / Surge 配置或订阅地址，别处搭好的节点也能对接中转
- **订阅**：多个订阅各自选落地、过滤、改名、加国旗、按地区分组，可分享给他人（独立凭据、到期自动断开）；按客户端自动返回 Clash / Mihomo、sing-box、Surge 或通用 base64；支持按入站（`?inbound=`）、按中转（`?relay=`）单独订阅
- **订阅分配**：按用户细化到"入站直连 / 指定中转"级别分配订阅权限；用户在"我的订阅"页只看到自己被分配的链接（链接带防篡改签名）
- **流量转发**：TCP / UDP 端口转发与隧道转发，配额、限速、单向或双向计费
- **面板内升级**：检查更新、一键升级

## 安装

面板（需要 Docker，脚本会自动安装缺失的 Docker，支持 `DOCKER_MIRROR=aliyun`）：

```bash
curl -L https://github.com/iiiiiiiii7125-art/flvx-max/releases/latest/download/panel_install.sh -o panel_install.sh && chmod +x panel_install.sh && ./panel_install.sh
```

国内机器 GitHub 直连慢的话，用代理加速下载：

```bash
curl -L https://gcode.hostcentral.cc/https://github.com/iiiiiiiii7125-art/flvx-max/releases/latest/download/panel_install.sh -o panel_install.sh && chmod +x panel_install.sh && ./panel_install.sh
```

选 `1. 安装面板`，完成后访问 `http://服务器IP:6366`，默认账号 `admin@user` / `admin@user`，登录后请立即修改密码。

菜单另有 `配置域名反向代理（自动 HTTPS）`，输入域名即可自动签发证书并反代面板。

节点：在面板 **节点** 页新建节点，复制生成的安装命令到服务器上执行。

中转机 / 落地机（国内机器代理加速版，把下面命令里的面板地址和密钥换成你自己的）：

```bash
curl -L https://gcode.hostcentral.cc/https://github.com/iiiiiiiii7125-art/flvx-max/releases/latest/download/install.sh -o install.sh && chmod +x install.sh && PROXY_ENABLED=true PROXY_URL=https://gcode.hostcentral.cc ./install.sh -a 面板地址 -s 密钥
```

> 提示：面板 **节点** 页生成的安装命令已按你的代理设置自动拼接好，直接复制用就行。
>
> 常用 GitHub 加速代理（把要加速的地址拼在后面即可）：
>
> - `https://gcode.hostcentral.cc`
> - `https://gh-proxy.com`
> - `https://ghfast.top`
> - `https://mirror.ghproxy.com`
> - `https://gh.llkk.cc`

更多说明见 [使用文档](https://iiiiiiiii7125-art.github.io/flvx-mod/)：[安装部署](https://iiiiiiiii7125-art.github.io/flvx-mod/guide/install) · [搭建节点](https://iiiiiiiii7125-art.github.io/flvx-mod/guide/nodes) · [对接中转](https://iiiiiiiii7125-art.github.io/flvx-mod/guide/relay) · [导入节点](https://iiiiiiiii7125-art.github.io/flvx-mod/guide/import) · [升级](https://iiiiiiiii7125-art.github.io/flvx-mod/guide/upgrade) · [常见问题](https://iiiiiiiii7125-art.github.io/flvx-mod/guide/faq)

## 免责声明

本项目仅供个人学习与研究使用，仅限合法、合规用途。使用者须确保其使用行为符合所在国家或地区的法律法规，并自行承担使用本项目带来的一切风险。作者不对因使用本项目导致的任何后果承担责任，亦不提供任何形式的担保。禁止将本项目用于网络攻击、数据窃取、非法访问等任何违法或未经授权的行为。
