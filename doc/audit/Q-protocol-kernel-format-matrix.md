# Q 审计：协议×内核×客户端格式 三方匹配（2026-10-20，版本 1.0.18）

起因：1.0.17 修复 Clash 渲染器跳过 XHTTP 后，用户要求系统审计——入站协议组合与
内核能力、与各客户端订阅格式的匹配性全面对齐，不匹配的创建时拒绝并提示。

方法：五路并行只读审计（R1 Clash/Shadowrocket、R2 sing-box/通用URI、R3 Surge/Loon/QX、
K1 Xray/Snell 服务端、K2 sing-box 服务端+创建路径），基准为 capabilities.go 的
catalog 入站能力矩阵（面板可创建组合）∪ 导入节点全集。

## 一、协议 × 内核（创建侧）

结论：catalog 与三个内核的真实能力逐项吻合，**无硬超卖**；创建/更新共用
inboundSave → kernel.Normalize 四元组强校验（协议/传输/安全层逐项查 catalog），
API 直调绕不过；拒绝以信封 msg 返回具体中文原因，前端 toast.error 直接展示。
下发链 Render 不复检的问题由本版新增复检兜底（见修复 5）。

本版增量：
- **AnyTLS 版本门**：AnyTLS 为 sing-box 1.12 新增协议，原先无下限校验（对照 ACME
  有 1.14 门）——老版本节点可创建成功、下发才失败。本版在 inboundSave 紧挨 ACME
  门补同款门（<1.12 拒绝并提示节点当前版本）。
- **catalog 欠卖补齐**：sing-box 的 VLESS×gRPC、Trojan×gRPC 漏列 Reality（Xray 侧
  有、sing-box 共享 TLS 层实现 Reality 与传输无关）。已补（追加在安全层列表末尾，
  不改变默认安全层）。
- 欠卖登记（不开）：VMess×xhttp（Xray）、http/httpupgrade 传输词表、SS 方法
  xchacha20-ietf-poly1305、VLESS Encryption（mlkem）——均为产品面决策，非缺陷。

## 二、入站 × 客户端格式（订阅侧）

原生 catalog 组合在 8 条渲染路径（URI/Clash/sing-box/Surge/Loon/QX/Shadowrocket
含 profile 与 config 两形态）逐项核对：除下表修复项外全部完整渲染；其余跳过均为
客户端真不支持（附证据），未臆造渲染。

本版修复（均带回归测试 render_matrix_regression_test.go）：
1. **导入 XHTTP 节点被 proxyClash 整体跳过**（与 1.0.17 同款过时注释的另一入口，
   波及 Clash/Shadowrocket 及经 endpointProxy 的 Loon/QX 导入路径）。已删跳过，
   xhttp-opts 透传。
2. **SS 密码 2022-blake3-chacha20-poly1305 缺失于 Loon/QX 密码白名单**（标准
   SS2022 三件套独缺其一，整节点静默消失）。已补两份白名单。
3. **UA 识别缺口**：Surfboard、Egern（消费 Surge 格式）未映射，会回退通用格式。
   已映射 FormatSurge。
4. **proxySingbox 缺 http/quic 传输分支**：导入的 VMess+http、VLESS/Trojan+quic
   节点在 sing-box 被丢弃而 URI 正常。已补两分支（http 与 h2 同形、quic 直映射）。

真不支持登记（跳过正确）：VLESS+XHTTP→sing-box（官方传输列表无 xhttp，证据确凿，
与 Clash 情形不同，不可硬渲染；同理 Snell→sing-box/URI、Naive→Mihomo 系、
VLESS→Surge（官方出站清单无 VLESS）、grpc/xhttp→Loon/QX、Reality→Surge。

存疑登记（无一手证据，不动）：Snell 在 SR/Loon/QX 的支持、QX 的 Hysteria2/TUIC/
AnyTLS 现状、Loon 的 TUIC、SR config 路径丢弃 WireGuard（profile 路径保留，两路
不一致）、Naive 的 sing-box 渲染需 ≥1.13 且 with_naive_outbound 构建、Surge×Snell
的 udp-relay 参数。拿到官方证据或真机实测后再定。

## 三、下发前复检（新增，handler.buildKernelApplyPayload）

逐行以 kernel.ComboSupported 复检启用入站的 (协议,传输,安全层) 是否在该内核
catalog 内，不合法整单拒绝并报明入站名与组合；同时检查 Snell+ShadowTLS 的
回环 BackendPort 互撞及与监听端口相撞（原先只在单入站 Normalize 内查重，
跨入站随机相撞会到下发 bind 才失败）。脏数据不再被渲染层静默降级为裸 TCP/明文。

## 四、残余观察

- sing-box 订阅在端点全被某格式丢弃时回退为仅 direct 的可用配置（如全部入站
  都是 XHTTP 时），用户无感知——与 Surge 用户面对纯 VLESS 入站同类，属通用
  性质；如需提示，建议在订阅预览/入站页做格式级计数提示，另行立项。
- repo 层 CreateNodeInbound/SaveNodeInbound 自身无校验，校验只在 handler 漏斗；
  现无绕开漏斗的写入方，未来新增写入方须走 inboundSave 或自行 Normalize。
- VLESS flow 在非 TCP 传输被 Normalize 静默清零（与 Xray vision 仅限 TCP 一致，
  正确归一化，UI 无提示，仅登记）。
