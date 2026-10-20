# R 功能：VLESS 加密（ENC / mlkem768x25519plus）（2026-10-20，版本 1.0.21）

用户定位：ENC 在面板中标识为「IX 中转专用协议」（创建表单开关旁与入站列表
均有同名徽章）。

## 规范依据（一手：xtls.github.io 入站/出站文档；2026-10-20 研究核实）

- 串文法（点分段）：服务端 decryption = `mlkem768x25519plus.<外观>.<ticket>.<填充链>.<服务端认证段>`；
  客户端 encryption = `mlkem768x25519plus.<外观>.<0rtt|1rtt>.<填充链>.<客户端认证段>`。
- 外观：native / xorpub / random。填充链与 ticket 用 vlessenc 默认形态
  （`100-111-1111.75-0-111.50-0-3333`、`600s`，客户端 `0rtt`），始终输出完整形态。
- 认证二选一：x25519（服务端存 32B 私钥，客户端段为派生公钥 43 字符）或
  mlkem768（服务端存 64B seed，客户端段为派生公钥 1184B/1579 字符）。
  面板以 Go 标准库 crypto/ecdh + crypto/mlkem 自生成，无新依赖；
  只存服务端秘密，客户端串单向派生（vless_encryption.go）。

## 创建与下发匹配（防"建得出、用不了"）

- Normalize：非 Xray 内核或非 VLESS 协议启用加密 → 直接拒绝并提示；
  认证方式/外观非法值拒绝；秘密缺失自动生成、已有则校验长度；
  vision flow 在加密启用时于 security=none+TCP 也保留（与 Xray 行为一致）。
- 创建/更新（handler inboundSave）：节点 Xray < 25.6.8 拒绝，文案含当前版本
  （versionBelow3 精确到修订号；latest/不可解析不拦，与既有版本门语义一致）。
- 下发前复检（buildKernelApplyPayload）：启用加密的入站遇节点 Xray < 25.6.8
  整单拒绝并报入站名，防止节点降级后静默下发坏配置。
- 凭据轮换（Regenerate）同步轮换加密秘密。
- sing-box 内核渲染：Normalize 已在创建层拒绝，渲染层无 decryption 概念，不涉及。

## 订阅匹配（逐格式核对，含导入形态；均有回归测试）

| 格式 | 原生 ENC 入站 | 导入 ENC 节点 | 依据 |
|---|---|---|---|
| 通用 URI | ✅ encryption= 客户端串 | ✅ 保真透传（原代码写死 none，已修） | 参数名 encryption，一手/生态实现 |
| Clash/Mihomo | ✅ encryption 键 | ✅ proxyClash 全键透传 | Mihomo VlessOption.encryption（源码级证据） |
| sing-box | ⏭ 跳过 | ⏭ 跳过 | 官方 changelog/字段文档零支持，跳过防坏节点 |
| Surge | ⏭（本就无 VLESS） | ⏭（同） | — |
| Loon / QX | ⏭ 跳过（line 函数守卫） | ⏭ 跳过 | 无已证实支持；endpointProxy 全字段转换已核 |
| Shadowrocket | profile 保留 / config 跳过 | 同左 | SR 支持未证实，维持 Q 审计时现状，登记 |
| 链式出站 | ✅ chainTarget 贯通客户端串 | ✅ 同 | relay 跳 Xray 出站 user.encryption |

## 已知边界（登记）

- 订阅预览/节点计数对"被某格式跳过"无计数提示（与 Q 审计残余观察同项）：
  表单已在开关下注明哪些格式不含此入站。
- v2rayN 等 URI 客户端对 ENC 的承接取决于其内置 Xray 版本（≥25.6.8），面板
  无法代验，属客户端侧下限。
- 外观 xorpub/random 与 padding 链未做真机互通实测（本侧无 Xray 25.6.8+ 节点
  环境）；native+x25519 形态与官方文档示例逐段一致。建议用户首个 ENC 入站
  建好后以 Clash/通用链接真机拉取验证一次。

## 1.0.22 补充：一键标准配置

开启 ENC 开关时表单自动套用 IX 中转标准组合（TCP + 安全无 + Vision 流控 +
ML-KEM-768 认证 + xorpub 外观），避免逐项误选导致组合不可用；开启后仍可
手动调整单项（后端 Normalize 与版本门继续兜底校验）。

## 1.0.23：生态标准对齐 + E2 独立复审修复

- **生态标准配置核对**（argosbx / yahuisme 脚本源码、官方文档三方互证）：
  一键默认 8 项中 7 项与通行做法一致（TCP、安全无、Vision、ML-KEM-768、
  600s、0rtt、默认填充链），唯外观 `xorpub` 偏离——两脚本与官方示例的默认
  落点均为 `native`，一键默认已改回 `native`（顺带消除 xorpub 未做真机
  互通实测的未验证面）。
- **E2 复审 P1（已修）**：`parseUserURI` 未读取 `encryption` 查询参数，
  vless:// 导入的 ENC 节点全格式静默退化为普通 VLESS 坏节点，且以 Proxy
  字段为据的防坏节点守卫（sing-box/Loon/QX/SR）被一并绕过。已补透传，
  回归测试 TestParseImportPreservesVLESSEncryption 覆盖解析→再导出→守卫。
- **E2 复审 P2（已修）**：Shadowrocket profile 路径未过 shadowrocketSkips
  过滤（config 路径有），ENC 与 WireGuard 节点会以坏形态进入 profile。
  已改为与 config 同口径跳过，回归测试 TestShadowrocketProfileSkipsEncryption。
- **E2 登记（P3，不修）**：旧 Mihomo 核心忽略未知 encryption 键（不会整表
  失败），但该节点会被当普通 VLESS 静默握手失败；面板无可信版本信号可
  门控——表单文案已提示"使用近一年内的 Mihomo 核心"。Mihomo 支持 ENC 的
  确切版本下限经查官方 wiki/changelog 无法钉死（wiki 有字段文档、无版本
  标注），不臆测。
- 长串健壮性经 E2 实测无坑：URI/Clash YAML/链式 JSON 逐字一致，goja
  脚本引擎不接触节点串。
