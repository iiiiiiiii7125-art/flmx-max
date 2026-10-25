# R25 修复 F-01 报告：分享地址派生剥离端口

- 基线：main @ ef87bc6（`ef87bc6d8b2e34e4ec88370f69150eebd6092f18`）
- 分支：`fix-r25-f01`（父提交即基线，未合 main、未打 tag、未发版）
- 问题：R25-X-01（产品 P2，B 域）——节点 ServerIP 含端口后缀时默认订阅全格式拉取为空
- 修复口径：FIX-PLAN 首选 (a)，无 BC；断言更新 0 条

## 一、问题

节点 ServerIP 含端口后缀（如测试机实值 `16.146.199.68:6366`）时，写侧
`IsValidNodeAddress`（`security_utils.go:86-116`）注释明文放行 host:port；
读侧 auto 模式 `inboundShareAddress` 把 ServerIP 原样当分享 host，
`ValidateImportHost` 拒之，`inboundEndpoints`（`kernel_relay.go:719-745`）
逐次跳过该节点全部端点——默认订阅全格式拉取为空（四档 nodeCount 全 0、
裸 token 拉 Clash 200 但 `proxies: []` 376 字节、base64 0 字节），失败完全
静默（200 空档）。

## 二、改了什么

产品代码 1 文件（`go-backend/internal/http/handler/kernel_share_address.go`，
+21/−2）：

- 新增 `stripShareHostPort`：`net.SplitHostPort` 成功则取 host（括号 IPv6 的
  括号由 SplitHostPort 一并去掉），失败则原样返回并沿用此前的括号修剪。
  非 host:port 值（裸 host、裸 IP、无端口括号 IPv6）恒等通过；剥离后仍
  异常的值继续由拉取期守卫拒绝——本 helper 永不绕过守卫。
- `inboundShareAddress` auto 档首选候选改走 `stripShareHostPort(node.ServerIP)`；
  `nodeShareIP` 候选循环内 `netip.ParseAddr` 之前同样剥离——ipv4/ipv6 档
  在仅有带端口 ServerIP 时亦可解析，auto 档的 ServerIPV4/V6 回落同效。
- 守卫（`validateShareHostValue` → `kernel.ValidateImportHost`）一字不动；
  写侧 `IsValidNodeAddress` 一字不动（仍放行 host:port，无 BC）。

单点派生覆盖全部消费面（`inboundShareAddress` 为唯一派生点）：直连端点
（`kernel_relay.go:719`）、链式落地（`kernel_chain.go:55`）、入站列表
ShareAddress 展示（`kernel.go:688`）同修。

## 三、同查结果（catalog §3.2“一处修，全处查”）

- 中转入口 host：无同 pattern。`entry.Host` 源自
  `repo.ForwardEntries`（`repository_kernel.go:727`），其每对地址已在源头
  经 `net.SplitHostPort` 拆分，host 恒为裸 host；`rel.ShareHost` 写侧已
  经 `validateShareHostValue` 校验、渲染期非法只回退入口 host 而不丢端点。
  无需改动。
- 联邦分享地址：无独立代码路径。远端节点 ServerIP 在联邦导入时原样落库
  （仅拒结构字符，`federation.go:1117-1124`），其入站渲染走同一
  `inboundShareAddress`，本次修复自动覆盖。无需改动。

## 四、方向 (c) 不在本批做的理由

FIX-PLAN 附加方向 (c)（订阅渲染 0 端点且存在被跳过入站时在列表/preview/
拉取侧给出可见告警）不在本批做：(a) 已消除本次立案的唯一静默失效触发
类（合法配置带端口）；其余被跳过情形（真正非法的存量 ShareHost）按
R3-E-01 有意 fail-closed + 日志，本就是设计行为；通用 0 端点告警涉及
列表/preview/拉取三面 API 形态与前端渲染，需单独立项设计（何面给信号、
何谓“被跳过”、信号形态），不宜钉在一次端口剥离修复上。

## 五、先红后绿

### 红（修复前，`R25-FIX-F01-red.log`，2,182 字节）

产品文件临时 stash 回基线后复跑（测试文件不动）：

- `TestR25ShareHostPortStrippedAtDerivation`：5 项 FAIL——auto host:port
  原样返回 `16.146.199.68:6366`；括号 v6:port 被 Trim 错切成
  `2001:db8::1]:6366`；hostname:port 原样返回；ipv4/ipv6 档由带端口
  ServerIP 解析为空。
- `TestR25InboundEndpointsHostPortServerIP`：FAIL——0 端点（与 L3 live
  空拉取同因）。
- 两处锚点 PASS（修复前后均应 PASS）：`TestR25L3BShareHostRejectsHostPort`
 （守卫仍拒带端口值）、`TestR25L3BNodeAddressAcceptsHostPort`（写侧仍
  放行 host:port）。

### 绿（修复后，`R25-FIX-F01-green-single.log`，1,477 字节）

- 上述 3 项 handler 测试 + 1 项 kernel 锚点测试全部 PASS（含回环剥离后
  仍被守卫拒绝的对照：`127.0.0.1:6366` → 剥出 `127.0.0.1` →
  `validateShareHostValue` 仍报错）。

### 全量回归（`R25-FIX-F01-fulltest.log`，6,378 字节）

- `go test ./... -timeout 30m`：16 包 ok，**产品失败 0**。唯一 FAIL 为
  `internal/health` 的已知沙箱假 IP 环境红
  （`TestValidateMonitorTCPTarget/公网域名放行`，example.com 解析落
  198.18/15 保留段，R21–R25 同款签名；CI 真实 DNS 下绿）。
- `go build` / `go vet`（go-backend）exit 0；`CGO_ENABLED=0 go build`
  （go-gost）exit 0、go-gost 测试 exit 0
  （`R25-FIX-F01-gostbuild.log` 120 字节、`R25-FIX-F01-gosttest.log`
  217 字节）；前端 `tsc --noEmit` exit 0
  （`R25-FIX-F01-tsc.log` 0 字节，本修复不碰前端文件；
  `node_modules` 以软链复用主仓已装依赖，已删、未入提交）。
- `gofmt` 干净。

## 六、真机腿（测试机 ip-172-26-4-158，锁 R25-F01，窗口 2026-10-25T19:56:53Z–20:02:21Z 部署+复验；live 目录 `/root/flmx-panel`，未用 `/root/new1` 陈旧副本）

部署：本地构建 fixed paneld md5 `35bfe03dbd9b7f9a143c54d5d837d9f4`
（43,904,479 字节）→ COPY-only 镜像 `flmx-backend:new7`
（`c0a73dbbba60`，GHCR 1.4.8 底座）→ compose 后端 new6→new7；
容器内 paneld md5 `35bfe03d…`；GET / 200；served index 不变
`57ab941e…`；agent 重启后 active。部署日志
`R25-FIX-F01-deploy.log`（1,949 字节）。

### 红腿（修复前，new6，`R25-FIX-F01-leg-pre.log`）

- 节点 serverIp `16.146.199.68:6366`，入站 new1-vless :59940 在位。
- nodeCount 四档全 0；裸 token 拉 Clash 200、`proxies: []`、376 字节；
  singbox 1051 字节空架、surge 97 字节空架；base64 0 字节。
- 与 L3 §3.25 复现逐项一致。

### 绿腿（修复后，new7，`R25-FIX-F01-leg-post.log`）

- nodeCount：default [1]、clash [1]、singbox [1]；surge [0]——见 §七口径订正。
- 裸 token 拉 Clash 200、571 字节，含 `new1-vless` vless 节点；
  singbox 1260 字节含节点；base64 152 字节，解码为
  `vless://<uuid>@16.146.199.68:59940?encryption=none&security=none&type=tcp#new1-vless`。
- 后端日志（`R25-FIX-F01-dockerlogs-post.log`，1,793 字节）：新容器
  生命周期内 `invalid share host` 行 0 条。

### V-02 证据缺口闭合

修复前原始日志已补采落盘：`R25-FIX-F01-dockerlogs-pre.log`（8,129 字节，
`docker logs flux-panel-backend --since 24h` 原始输出），含 18 行
`inboundEndpoints: invalid share host for inbound ID 1 (node 1), mode auto,
skipping endpoint`（含本腿两次拉取触发的 03:59:25/26 两行）；L3 引用的
10 次跳过行同属该原始日志文件，缺口闭合。

### 恢复对账

后端换回 new6（compose 还原）；paneld 回 `8867e99f…`、served index 回
`57ab941e…`、三容器 healthy、agent active；计数 node/tunnel/forward/
inbound/subscription = 1/0/0/1/1，与 R25 基线 pristine 一致；锁已放。

## 七、口径订正（N-36）

FIX-PLAN 要求“四档 nodeCount ≥ 1”，与实测不符：surge 档 post-fix 仍为
[0]，且裸拉 surge 仍为 97 字节空架——与修复前逐字节相同。原因在
`SurgeLine`（`internal/kernel/share.go:584-586`）无 VLESS case，VLESS
端点按设计被跳过，与本次立案无关。正确口径：default/clash/singbox ≥ 1、
surge 档对 VLESS 入站恒为 0（设计行为，非回归）。本报告以实测为准。

## 八、残余风险

1. `stripShareHostPort` 对 `host:badport`（非数字端口）仍取 host——与
   `net.SplitHostPort` 语义一致；剥离出的 host 仍须过守卫，坏值不会下发。
2. 方向 (c) 的可见告警未做（见 §四）；其余被跳过情形仍仅以后端日志为信号。
3. 真机只验单节点直连腿；多节点/联邦侧的同 pattern 覆盖以代码同查为准
   （见 §三），未另起第二节点实机。

## 九、交付

- 修复报告：`audit-out-132/r25/R25-FIX-F01-REPORT.md`（本文件）
- 证据日志（同目录，`*.log` 依 `.gitignore` 不入 git，随 worktree 备查）：
  红/绿/全量/tsc/gost×2、真机腿 pre/post、docker logs pre/post、部署日志；
  腿脚本 `leg-r25f01-sub.py` 与部署脚本 `deploy-r25f01.sh` 同目录保留。
  全部日志均不含密码与订阅 token（token 仅记长度；后端访问日志本身掩码
  token 为 `***`）。
- 断言更新 0 条；无 BC 项；产品代码改动 1 文件 +21/−2。
- 三方对账：总账 R25-X-01（产品 P2）↔ FIX-PLAN F-01 ↔ 落地集（本修复），差集 ∅。
— R25-FIX-F01-REPORT END · 自证：本行之前正文 8876 字节
