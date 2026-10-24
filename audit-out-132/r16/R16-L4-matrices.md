# R16-L4 常设全集复跑报告 — 附则 A / 附则 A-2 / 版本门 / ENC 文法 / 路由与数据表 / 同字段消费面

- **审计轮次**：R16（靶向轮·常设全集复跑，L4 车道）
- **基线（pin 死）**：`main @ b54b44f`（完整 `b54b44f772d8b4efdd6d08b1fbbbc40c8091a143`，「docs(r15): archive V-06 verdict corrections (were working-tree-only)」）；相对 R15 基线 `5bd5a90` 的增量 = R15 修复合入，`git diff 5bd5a90..b54b44f -- go-backend/` 为 **0 行**（产品代码零改动；增量仅文档/前端 demo/审计归档）
- **依据**：`~/workspace/user/files/flmx-audit-catalog-v132.md`（附则 A、附则 A-2、版本门全集 13 行、ENC 文法）、R15-L4 全集探针口径（R15-L4-matrices.md），护栏 N-01…N-37 全程有效
- **性质**：只读审计。不改产品代码、不打 tag、不发版
- **读数树**：只读 worktree `~/workspace/.r16-tree @ b54b44f`（detached HEAD；本轮结束时 `git status` 空，pristine）；共享工作树 `feat-latency-hint` 未用作读数树、未 checkout、未改文件
- **测试隔离**：快照 `~/workspace/.r16-tmp/r16base`（`git archive b54b44f` 解包；`handler.go` sha256 与 `git show b54b44f:` 逐字节一致 `9e46dd82…`）；探针文件 `r16_l4_probe_test.go`（kernel 包）、`r16_l4_r5_probe_test.go`（repo 包）、`r16_l4_supplement_test.go` 只放快照内，跑后即删（N-37 细则④）；删后 `go test ./internal/kernel/` 仍 exit 0（`ok … 0.212s`）
- **证据纪律（N-37）**：本报告全部探针类结论只认落盘原始日志 + 完整名单存档（`R16-L4-*.log`，见 §11）；批报告摘要计数不得作为裁定依据；探针输入全部为本轮自构（主机/端口/密码/UUID 均换值：`r16-` 前缀、`203.0.113.21+`、`33333333-3333-4333-8333-333333333333`、`18xxx–19xxx`/`58xxx` 端口），不以 R15 日志摘要顶替本轮实测

## §0 结论总表

| 全集 | 规模 | 复跑结果 | 新立案 |
|---|---|---|---|
| 附则 A 转换器×凭据矩阵 | 60 格（R1–R5 × C1–C12） | **0 静默丢弃**（搬运/透传/显式拒绝/不适用逐格有据；R15 遗留未单列项 R4 snell shadow-tls 伴生本轮已单列补格，见 §1.4 注②） | 0 |
| 附则 A-2 枚举字段合法集 | 11 行 | **11/11 通过**（进口谓词 + 存储规范化 + 消费面四面） | 0 |
| 版本门全集 | 13 行 | **12 行二进制/代码复证通过，1 行（VG-09）未验证**（沿袭 R14/R15 观察，非新缺口） | 0 |
| ENC 文法全表 | 99 格 | **99/99 PASS**（`TestR11B3X007Full99Table`，谓词/进口/存量/双二进制四方一致） | 0 |
| 路由钉死值 | handler.go 201 + router.go 1 = 202 | **与 R15 原料表双向差集 ∅**（鉴权/守卫分布逐字一致） | 0 |
| 数据表钉死值 | 49 张 | **与 R15 原料表双向差集 ∅** | 0 |
| 同字段消费面抽核 | A-2 邻接面（forEndpoint + 四面谓词 + 两渲染面 + URI 面） | **22 CELL 全过**，无毒化值下发 | 0 |

**新发现问题：0 条。L4-CAND-## 立案：无（P0=0 / P1=0 / P2=0 / P3=0）。** R15 在本车道范围内的修复复验见 §7，全部维持关闭。

---

## §1 附则 A 转换器 × 凭据矩阵 60 格（主责逐格回勾）

探针：`TestR16L4MatrixR1Singbox` / `R2Clash` / `R3URI` / `R4Surge`（`R16-L4-matrix-probe.log`，CELL 131 行）+ `TestR16L4MatrixR4SnellShadowTLSCompanion`（`R16-L4-matrix-supplement.log`，CELL 1 行）+ `TestR16L4MatrixR5Restore` / `R5Whitelist`（`R16-L4-r5-probe.log`，CELL 18 行）。统一入口 `ParseImport`（R5 为 `repo.Import`）。格状态定义按 catalog 附则 A 格定义注记（以源格式字段存在性为准）。

列：C1 `password`、C2 `uuid`、C3 `psk`、C4 `cipher/method/security`、C5 `encryption`、C6 `flow`、C7 `username`、C8 `plugin/plugin-opts`、C9 `reality-opts`、C10 `obfs/obfs-password`、C11 `version/mode`、C12 `alterId`。

### 1.1 R1 sing-box JSON `singboxToProxy`（12 格全搬运✓）

14 个节点出站（另 2 个 `shadowtls` detour 出站非节点）零错误入库。逐格：

| 格 | 状态 | 实证（CELL） |
|---|---|---|
| C1 password | 搬运✓ | ss `r16-ss-pw1`/`r16-ss-pw2`/`r16-ss-pw3`、trojan `r16-tj-pw1`、hy2 `r16-hy2-pw1`、tuic `r16-tuic-pw1`、anytls `r16-at-pw1`、naive `r16-na-pw1`、socks `r16-so-pw1`、http `r16-ht-pw1` 逐字 |
| C2 uuid | 搬运✓ | vmess/vless/tuic `33333333-3333-4333-8333-333333333333` 逐字 |
| C3 psk | 搬运✓ | snell `r16-snell-psk1` / `r16-snell-psk2` 逐字 |
| C4 cipher | 搬运✓ | ss `aes-256-gcm`/`aes-128-gcm`、vmess `aes-128-gcm`（←security）逐字 |
| C5 encryption | 搬运✓ | vless ENC 全串逐字（与 R15 同一法定测试串） |
| C6 flow | 搬运✓ | vless `xtls-rprx-vision` 逐字 |
| C7 username | 搬运✓ | naive `r16-na-user`、socks `r16-so-user`、http `r16-ht-user` 逐字 |
| C8 plugin | 搬运✓ | ss obfs `mode=http host=r16ob.example.net`；ss shadow-tls detour → `plugin=shadow-tls` + `plugin-opts{password=r16-stls-pw1, host=stls.r16.example.net}`；snell shadow-tls detour → `shadow-tls{password=r16-stls-pw2, host=sn.r16.example.net}` |
| C9 reality-opts | 搬运✓ | vless tls.reality → `reality-opts{public-key=32 零字节 RawURL, short-id=cdef}` 逐字 |
| C10 obfs | 搬运✓ | hy2 `obfs=salamander`、`obfs-password=r16-hy2-obfs1`、`ports=30000-30010`（`server_ports ["30000:30010"]` 合并形态） |
| C11 version/mode | 搬运+愈合✓ | snell `version=6`、`mode=unshaped`（v6+unshaped 保留） |
| C12 alterId | 搬运✓ | vmess `alter_id=1` → `alterId=1` |

### 1.2 R2 Clash YAML 透传（12 格全透传✓）

11 个节点零错误入库，逐列逐值断言：

| 格 | 状态 | 实证 |
|---|---|---|
| C1–C10 | 透传✓ | password/cipher/uuid/flow/encryption/reality pbk+sid/obfs/obfs-password/plugin-opts `{mode=tls, host=r16cob.example.net}` 全部逐字同值（CELL 逐项） |
| C11 version/mode | 透传✓（逐值） | snell `version=6`、`mode=unshaped` 逐值断言 |
| C12 alterId | 透传✓（逐值） | vmess `alterId=1` 逐值断言 |

### 1.3 R3 URI 逐参数（10 格搬运✓ + 2 格显式拒绝✓）

8 条链接零错误入库（注：R15 为 9 条，差 1 条为探针构造差异——本轮未另设第二条 ss 链接；12 格覆盖无缺，见下表）；另 2 条显式拒绝：

| 格 | 状态 | 实证 |
|---|---|---|
| C1 password | 搬运✓ | ss/trojan/hy2/tuic/anytls/socks `password` 逐字 |
| C2 uuid | 搬运✓ | vmess/vless/tuic uuid 逐字 |
| C3 psk | 显式拒绝✓ | `snell://` → `暂不支持 snell:// 链接`，0 节点入库（非静默丢弃） |
| C4 cipher | 搬运✓ | ss userinfo `aes-256-gcm`、vmess JSON `scy=aes-128-gcm` 逐字 |
| C5 encryption | 搬运✓ | vless query `encryption` 全串逐字 |
| C6 flow | 搬运✓ | vless `flow=xtls-rprx-vision` 逐字 |
| C7 username | 搬运✓ | socks5 userinfo `r16-uri-so-user`；http `http://` → 显式拒绝「`http(s):// 是订阅地址，请单独粘贴在输入框里`」（该 scheme 无节点载体，属有意保留） |
| C8 plugin | 搬运✓ | ss plugin 参数 `mode=http host=r16uob.example.net` 逐字 |
| C9 reality-opts | 搬运✓ | vless `pbk`/`sid` → `reality-opts{public-key, short-id=ab12}` 逐字 |
| C10 obfs | 搬运✓ | hy2 `obfs=salamander`、`obfs-password=r16-uri-hy2-obfs` 逐字 |
| C11 version/mode | 显式拒绝✓ | 同 C3（`snell://` 整体不受支持） |
| C12 alterId | 搬运✓（逐值） | vmess JSON `aid=1` → `alterId=1` 逐值断言 |

### 1.4 R4 Surge/Loon 行（9 格搬运✓ + 2 格显式拒绝✓ + 1 格不适用）

13 行零错误入库；另 vless 行显式拒绝：

| 格 | 状态 | 实证 |
|---|---|---|
| C1 password | 搬运✓ | ss/trojan/hy2/tuic/anytls/socks/http `password` 逐字 |
| C2 uuid | 搬运✓ | vmess/tuic `username=`/`uuid=` 逐字 |
| C3 psk | 搬运✓ | snell `psk=r16-sg-sn-psk1` / `r16-sg-sn-psk2` 逐字 |
| C4 cipher | 搬运✓（R10X-013 订正格） | vmess `encrypt-method=aes-128-gcm` 逐字落库；ss `encrypt-method` + Loon positional `aes-128-gcm, "r16-sg-loon-pw2"` 两路均搬运 |
| C5 encryption | 显式拒绝✓ | vless 行 → `暂不支持 vless 类型的节点`，0 节点入库 |
| C6 flow | 显式拒绝✓ | 同 C5（vless 行整体拒绝） |
| C7 username | 搬运✓ | socks5/http `username=` 逐字 |
| C8 plugin | 搬运✓ | ss `obfs=http + obfs-host` → `plugin-opts{mode, host}`；ss shadow-tls 伴生 `shadow-tls-password/host/version` → `plugin-opts{password, host, version=3}`（本轮单列断言）；snell shadow-tls 伴生见注② |
| C9 reality-opts | 不适用 | 字段存在性证据：reality 只随 vless 承载（`parseSurgeLine` 全分支无 reality 载体），而 vless 行整体显式拒绝——无载体故无丢弃可能 |
| C10 obfs | 搬运✓ | hy2 `salamander-password` → `obfs=salamander` + `obfs-password`，`port-hopping` → `ports=30000-30010`；snell `obfs=http` → `obfs-opts{mode=http}` |
| C11 version/mode | 搬运+愈合✓ | snell `version=6`、`mode=unshaped` 逐字 |
| C12 alterId | 搬运✓ | vmess `vmess-aead=false` → `alterId=1` |

注②：R15 遗留未单列项（R15-L4 §1.4 注①：R4×C8 的 snell shadow-tls 伴生未单列 CELL 断言）**本轮已补格**：`TestR16L4MatrixR4SnellShadowTLSCompanion`（`R16-L4-matrix-supplement.log`）单列断言 Surge snell 行 `shadow-tls-password/host/version` 经 `applySurgeShadowTLS` 落 `shadow-tls{password=r16-sg-sn-stls-pw, host=snst.r16.example.net, version=3}`，psk 与 version=6 本体字段同时在位。R4×C8 至此无未单列伴生。

**附则 A 结论：60 格 0 静默丢弃。** 任一格均无「输入带字段、产物无字段、无拒绝无回执」。

### 1.5 R5 备份 Extra 载体（10 行合法 + 3 行坏值 + WhitelistExtra 3 格）

探针 `TestR16L4MatrixR5Restore` + `TestR16L4MatrixR5Whitelist`（`R16-L4-r5-probe.log`，2/2 PASS，CELL 18 行）：

| 行 | 状态 | 实证 |
|---|---|---|
| ss / trojan / hysteria2 / tuic / vmess / vless / snell / naive / socks5 / ss-plugin（10 行） | 逐字恢复✓ | `InboundsImported=10`；每行 `Extra` 与备份字节逐字相等（DB 回读 `node_inbound.extra`）；`Protocol` 与重推导值一致，其中 ss-plugin 行备份故意写错 `trojan`，落库为 `shadowsocks`（R13X-006 重推导在位） |
| 坏 ENC vless / 坏 Reality pbk vless / 坏 SS cipher ss（3 行） | 整行跳过✓ | `SkippedRows[inbounds]=3`；三行 DB 零行；三行名逐一点名进 `ImportWarnings` |
| WhitelistExtra 合法 `10.0.0.0/8, 1.2.3.4` | 全留✓ | 逐字恢复（行 #301） |
| WhitelistExtra 混合 `10.0.0.0/8, not-an-ip, 192.168.0.0/16` | 去坏留好✓ | 落库 `10.0.0.0/8, 192.168.0.0/16`，行本身照常恢复（行 #302） |
| WhitelistExtra 全坏 `not-an-ip, also bad!!` | 清空✓ | 落库空串；warnings 点名 `#302` / `#303` 两行（行 #303） |

---

## §2 附则 A-2 枚举字段合法集 11 行（主责逐行回勾）

进口谓词探针 `TestR16L4A2Predicates`（24 CELL：每行坏值显式拒绝 + 好值携带/愈合 + 2 行愈合复核）+ 消费面探针 `TestR16L4FacesConsumption`（22 CELL，见 §6）+ 库内枚举测试全量（`R16-L4-kernel-full.log` 内 61 项 `TestR5B1/TestR6B1/TestR10B1/TestR10B3/TestR11B1/TestR11B2/TestR11B5/TestR4B1/TestR8B1/TestR9B2/TestR3B2/TestR2B5` 前缀测试全 PASS，与 R15 同数；全包 270 PASS / 0 FAIL，见 §4）。

| 行 | 字段 | 进口谓词 | 存储规范化 | 渲染面处置 | 结论 |
|---|---|---|---|---|---|
| A2-01 | client-fingerprint | 垃圾值显式拒绝（点名合法集） | `Chrome` → `chrome` 愈合落库（本轮复核：进口谓词本身小写愈合） | 存量 `Chrome` 经 `forEndpoint` 愈合、sing-box 产物 `chrome`；垃圾值四面谓词全不过、两渲染面整节点略过 | ✅ |
| A2-02 | xhttp-opts.mode | 垃圾值显式拒绝（点名 {auto, packet-up, stream-up, stream-one}） | 嵌套 `packet-up` 携带；存量 `PACKET-UP` 经 `forEndpoint` 愈合 `packet-up`（本轮复核：进口谓词本身小写愈合） | 同谓词口径 | ✅ |
| A2-03 | SS plugin-opts.mode | 垃圾值显式拒绝（点名 {http, tls}） | `TLS` → `tls` 愈合落库 | 同谓词口径 | ✅ |
| A2-04 | Snell obfs-opts.mode / v6 顶层 mode | 垃圾值显式拒绝（点名 {http, tls, shadow-tls, restls, jls, ∅}） | 别名 `shadowtls` → `shadow-tls` 愈合；存量 `UNSHAPED` 经 `forEndpoint` 愈合 `unshaped` | 同谓词口径 | ✅ |
| A2-05 | VMess cipher | 垃圾值显式拒绝 | `aes-128-gcm` 携带 | 四面谓词：坏值全不过、好值全过 | ✅ |
| A2-06 | VLESS flow | 垃圾值显式拒绝 | `xtls-rprx-vision` 携带 | 四面谓词：坏值全不过、好值全过 | ✅ |
| A2-07 | Hysteria2 obfs-type | 垃圾值显式拒绝 | `salamander` 携带 | 四面谓词：坏值全不过、好值全过 | ✅ |
| A2-08 | TUIC congestion-controller | `new-reno`（连字符）显式拒绝 | `bbr` 携带 | 四面谓词：坏值全不过、好值全过 | ✅ |
| A2-09 | SS cipher | 垃圾值显式拒绝；mihomo-only `chacha20` 进口不拒 | 逐字落库 | 分面：Clash 面保留、sing-box/other/Xray 链式三面略过（`credentials.go:722` `face != ssFaceClash` 门逐字在位） | ✅ |
| A2-10 | VLESS encryption | 垃圾值显式拒绝 | 合法 ENC 串携带 | 四面谓词 + URI 面：合法逐字下发、坏串不发链；99 格见 §4 | ✅ |
| A2-11 | Reality 形态格 | 坏 pbk / 坏 sid 均显式拒绝（点名 32 字节 RawURL / 偶数位小写十六进制≤16 位） | 精确形逐字落库 | 横切谓词 `validateRealityOpts`：坏值四面全不过、合法四面全过 | ✅ |

**附则 A-2 结论：11/11 通过，无新增立案。**

---

## §3 版本门全集 13 行（逐行核对门号与版本比较）

二进制复证日志 `R16-L4-binary-probe.log`（在盘二进制：sing-box 1.11.15/1.12.17/1.13.14（`~/workspace/.r9bin/sing-box-1.1x` 文件本体）、1.14.3（`~/workspace/.r4b1-bin/sing-box-1.14.3-linux-amd64/sing-box`）、Xray 25.3.6/25.5.16/25.6.8/25.9.5（`.r9bin`）、25.8.29（本轮重下，见 §8 O-L4-6：直连 GitHub release 资产，构建哈希 `81b7cd7` 与 R15  binary 一致））。代码门行号按本轮基线树（`handler/kernel.go` 三处 ENC 门 :2007/:2032/:2118，与 R15 逐字同行；函数定义 :1997/:2031/:2083；出站 prefer `kernel_egress.go:48` 逐字不变）。

| 门 | 内容 | 复证结果 |
|---|---|---|
| VG-01 | ACME 1.14 | 1.13.14 exit 1（unknown field certificate_providers）/ 1.14.3 exit 0 |
| VG-02 | AnyTLS 1.12 | 1.11.15 exit 1（unknown inbound type: anytls）/ 1.12.17 exit 0 |
| VG-03 | Snell 入站 1.14 | 1.13.14 exit 1（unknown inbound type: snell）/ 1.14.3 exit 0 |
| VG-04 | ENC 25.9.5 双面 | 客户端串与服务端 decryption 串，25.8.29 exit 23 / 25.9.5 exit 0（`Configuration OK.`） |
| VG-05 | Snell 出站 v4 1.14 | 1.13.14 exit 1（unknown outbound type: snell）/ 1.14.3 exit 0；自构 v5 形被 1.14.3 拒（unsupported version: 5），面板从不产出 |
| VG-06 | 出站策略 sing-box 1.12 | 1.11.15 exit 1（unknown field domain_resolver）/ 1.12.17 exit 0 |
| VG-07 | Xray prefer 25.6.8 | happyEyeballs 符号计数 25.3.6=0 / 25.5.16=0 / 25.6.8=3 / 25.9.5=3 |
| VG-08 | http_clients 1.14 | 1.13.14 exit 1（unknown field http_clients）/ 1.14.3 exit 0 |
| VG-09 | Snell 服务端 | ⚠️ **未验证**（在盘全根搜索 `snell-server` 0 命中；代码自洽测试 `TestR2B5Snell*`/`TestKernelVersionGateSnell` PASS 不等于服务端实测）——沿袭 R14/R15 观察 O-A5-2，非新缺口，不立案 |
| VG-10 | Reality 默认 1.0.0 | ✅ 三方一致：`kernel/model.go:211` `DefaultRealityMinClientVer = "1.0.0"`、`doc/guide/faq.md:61`、前端 `inbound-form-modal.tsx:915` 描述 / `:917` placeholder（注：前端文件已移至 `vite-frontend/src/pages/inbound/inbound-form-modal.tsx`，行号与 R15 逐字一致，见 §8 O-L4-7） |
| VG-11 | 双栈回程 agent 1.2.0 | ✅ 版本考古（非二进制实测）：tag `1.1.1` 两侧 family 计数 0/0；tag `1.2.0` 两侧 10/9；引入提交 `58dadac`/`9756636` 首含 tag 均为 `1.2.0`；现行树仍 10/9；文档 `nodes.md:213` 下限逐字属实（注：计数口径为 `grep -ci family`，与 R15 同法；区分大小写计数为 6/5，结论不变） |
| VG-12 | Naive 出站 1.13 | 1.12.17 exit 1（unknown outbound type: naive）/ 1.13.14 exit 0；naive 入站在 1.11.15 已解码、仅因测试证书文件缺失失败（`/tmp/cert.pem` 不存在） |
| VG-13 | XHTTP 无门 | 观察：25.3.6 与 25.9.5 对面板形 xhttp 配置均 exit 0，下限 ≤25.3.6，维持观察 |
| — | 「及以上」反扫 | 无第 14 个门：`versionBelow/versionBelow3/versionBelowPadded` 全部调用点 19 处枚举完毕，下限集 = {1.12, 1.13, 1.14, 25.6.8, 25.9.5}，与 13 行定义逐一对应 |

**版本门结论：12 行复证通过 + VG-09 未验证（沿袭）+ 无第 14 门。**

---

## §4 ENC 文法全表 99 格

`TestR11B3X007Full99Table` 在 `R16-L4-kernel-full.log` 内 **PASS**（表内 `len(r11b3Cells99) == 99` 断言在位；逐格四方对账：谓词 `ValidVLESSEncryption` / 进口 `ParseImport(Clash VLESS)` / 存量谓词 `validImportedProxyFields(·, ssFaceClash)` / 双二进制实测正本 `BinaryAccept`，除空白特殊格 F13/S1/S2 外三方一致；该三格口径为 Pred=false、BinaryAccept=false、ImportStored=true，修剪形 Clash 下发由 `TestR11B3X004*` 另行销账）。

谓词 `ValidVLESSEncryption`（`kernel/credentials.go:521` 起）与 catalog A2-10 逐字相符：段名大小写精确、`{∅, none}` 合法、元素数 {3,4,5}、首组前三数、位数按 token 原始长度 ≤11 计符号、4 段官方默认串接受、服务端形（ticket 占 rtt 位）拒。本轮另有 `TestR8B1ENC*` / `TestR9B2*` / `TestR10B2*` 全 PASS（同日志，ENC 相关 12 项），URI 面谓词门在位（`proxyURI` vless/trojan 支 `kernel/proxy_produce.go:15` 起，:84 附近 `validImportedProxyFields(p, ssFaceOther)`；进口谓词 `validateProxyCredentials` vless 支 `kernel/proxy_parse.go:251`）。

**ENC 结论：99/99 PASS，无回归。**

---

## §5 路由与数据表钉死值（同 R15 口径重做，非抄旧数）

方法：普查脚本 `R16-L4-census.py`（R15 脚本仅改读数树路径 `.r15-tree` → `.r16-tree`，其余逐字同口径）：`handler.go` 的 `Register` 内全部 `mux.HandleFunc`（字面量 200 条 + `ghMirrorPath` 常量 1 条，`gh_mirror.go:32`）+ `router.go:13` 的 `/system-info` 1 条；鉴权分类器逐字复刻 `middleware/auth.go` 的 `shouldSkip` / `allowsOptionalAuth` / `requiresAdmin`；方法守卫逐 handler 函数体 `r.Method` 检查推导；数据表取 `repository.go:327` 的 AutoMigrate 模型集逐项抽取。原料表 `R16-L4-routes-raw.csv`（203 行含表头）、`R16-L4-tables-raw.txt`（49 行）。

| 项 | 本轮实测 | 与 R15 原料表双向差集 |
|---|---|---|
| 路由总数 | **202**（handler.go 201 + router.go 1；`/api/v1` 197 + 非 API 5） | ∅（路径/ handler / 守卫 / 鉴权四列逐行一致；`diff` 空） |
| 鉴权分布 | ADMIN 135 / JWT 44 / PUBLIC 17 / OPTIONAL-AUTH 1 / 非 API 5 | ∅（与 R15 分布逐字一致） |
| 方法守卫分布 | POST only 131 / ANY 53 / GET only 13 / not(Get,Post) 1 / GET,HEAD 2 / GET,POST 1 / GET (WS upgrade) 1 | ∅（与 R15 分布逐字一致） |
| 数据表总数 | **49**（48 个 `model.*` + 仓库内 `NodeSeq`） | ∅（集合与顺序逐行一致） |

**TableName() 口径注记（任务书要求确认的模型）**：`TableName()` 定义计数 48 是 **model 包口径**——`internal/store/model/` 内 48 个模型定义全数有 `TableName()`；AutoMigrate 49 项中**没有**缺 `TableName()` 定义的模型：第 49 个是 `NodeSeq`（`internal/store/repo/node_seq.go:20` 定义，`TableName()` 在同文件 :28，返回 `"node_seq"`），它不在 model 包内，故不计入 48。另有一个非数据表定义 `schemaMigration`（`migration_r5b4_v1.go:30`，迁移辅助表）不在 AutoMigrate 集内。结论：48 与 49 之差 = `NodeSeq` 的包归属差异，**非漂移、非缺口**，不得误报。

注：`5bd5a90..b54b44f` 间 go-backend 零改动，故 Register 行号与模型集零漂移是预期内的实测结论，非未改动的假象。

**路由/数据表结论：钉死值 202 / 49 维持，双向差集 ∅。**

---

## §6 同字段消费面全集抽核（与附则 A-2 邻接）

探针 `TestR16L4FacesConsumption`（`R16-L4-matrix-probe.log`，22 CELL 全过）：

- **坏值面**：fingerprint / flow / TUIC cc / Hy2 obfs / VMess cipher / SS cipher / ENC / SS plugin mode / Snell obfs / Reality pbk / Reality sid，坏存量在 `validImportedProxyFields` 四面（sing-box / Clash / other / Xray 链式）谓词**全不过**；fingerprint 坏值另在 sing-box 与 Clash 两渲染面整节点略过（`proxySingbox` / `proxyClash` 均不产出）。
- **愈合面**：存量 `Chrome` → `forEndpoint` 愈合 `chrome`、存储不动、sing-box 产物 `utls.fingerprint=chrome`；存量 `PACKET-UP` → 愈合 `packet-up`；存量 Snell v6 `UNSHAPED` → 愈合 `unshaped`。
- **好值面**：flow / TUIC cc / VMess cipher / ENC 合法值四面谓词全过；SS mihomo-only `chacha20` 分面：仅 Clash 面过、sing-box/other/Xray 链式三面不过（与 A2-09 单面集口径逐字一致）。
- **URI 面**：合法 ENC 串 `proxyURI` 逐字下发；坏 ENC 不发链。
- **链式面**：`TestR6B1ChainTargetGuards`（库内，`R16-L4-kernel-full.log` 内 PASS）+ F-03 修复复验（见 §7）。

**消费面结论：抽核 22 CELL 全过，无原样下发的毒化值，无新增立案。**

---

## §7 R15 修复在本车道范围内的复验（按对象本身，N-35）

本轮基线与 R15 基线间 go-backend 零改动（§5 注），R15 修复批本身无产品代码可复验；下列为 R14 修复在本车道的维持复验（与 R15 §7 同对象集，门测试重跑）：

| R14 项 | 修复批 | 本轮复验对象 | 结果 |
|---|---|---|---|
| A5-01（P1）链式+空闲超时+分流直连悬空 `flmx-idle-*` | F-03（`render_xray.go` / `split.go` / `chain.go`） | 库内 `TestR14F03RoutingTagsResolve` / `TestR14F03ChainedIdleSplitDirect` / `TestR14F03ChainedIdleNoSplitDirectGetsNoIdleOutbound` / `TestR14F03SplitFallbackWhenIdleOutboundMissing`（`R16-L4-kernel-full.log`） | ✅ 全部 PASS，关闭维持 |
| 148-P3-1 connIdle 越界文案 | F-04 | `TestR14F06F04ConnIdleZeroWording`（同日志） | ✅ PASS |
| 148-P3-4 `attachNodeLatency` 吞 targets 查询错误 | F-04 | `TestR14F06F04AttachLogsTargetsQueryError`（`R16-L4-handler-gate.log`） | ✅ PASS |
| 148-P3-5 单族 sock_diag 失败无声吞掉 | F-04 | `TestR14F06F04SingleFamilyFailureWarns`（`R16-L4-socket-tests.log`，socket 包 94 PASS 内） | ✅ PASS |
| A5-08（P3）Snell+Via 保存/渲染不对称 | F-08 | `TestR14F08ValidateChainVersionRejectsSnellVia` / `TestR14F08SnellViaSplitSaveRejected` / `TestR14F08SnellPoisonedRowHealedOnApply`（handler）+ `TestR14F08ImportStripsSnellPoisonedRouting`（repo）（`R16-L4-handler-gate.log` / `R16-L4-repo-gate.log`） | ✅ 全部 PASS |
| A3-01（P1）单键 `config/get` 读 gist 凭据明文 | F-01 | `TestConfigGetNeverReturnsGistCredentials` / `TestConfigGetAllowsSensitiveKeysForAdmin`（`R16-L4-handler-gist.log`） | ✅ PASS（gist 两键永不返回；其余 sensitive 键管理员仍可读，既有行为不变） |
| A3-02（P3）`gist/save` 无密码守卫 | F-02 | `TestGistDue` / `TestGistStatusTokenHint`（同日志） | ✅ PASS |

门测试小计：handler-gate 7/7 PASS、handler-gist 4/4 PASS、repo-gate 3/3 PASS（日志见 §11）。

---

## §8 观察项（不立案）

| # | 内容 |
|---|---|
| O-L4-1 | VG-09 Snell 服务端仍未验证（在盘全根搜索 `snell-server` 0 命中；沿袭 R14 O-A5-2 / R15 O-L4-1）。代码自洽测试 PASS 不等于服务端实测；出现可达的官方 snell-server 二进制时再逐格销账 |
| O-L4-2 | VG-13 XHTTP 无门，下限 ≤25.3.6（25.3.6 对面板形 xhttp 配置 exit 0），维持观察 |
| O-L4-3 | VG-10 指针：`model.go` 默认 Reality 版本常量行号 :211（与 R15 同值，无漂移）；值逐字未变 |
| O-L4-4 | socket 包 6 SKIP 行（4 项顶层测试 + 2 条子测试）：`TestR6B6HandleUdpPingPrivateListenerSucceeds`、`TestTCPPingQueueSkipsExpiredTargets`、`TestTCPPingExecutionHonorsCancellation`、`TestTCPPingQueueStopsWithPendingAndRunningProbes`（沙箱网络/权限前提不在场致跳过，与 R15 同名）+ `TestTCPPingQueueCompletesMoreThanEightTargets/targets_9`、`targets_30`（子测试 SKIP，本轮首次计数）：本地不可判定，不计 PASS、不计失败；另 `TestNftScriptPortHopping` SKIP 1 项（x 全包跑出的非 socket 包项）。socket 包本身 94 PASS / 0 FAIL，与 R15 同数 |
| O-L4-5 | SIP023 四面（Surge/Loon/QX/URI 原样下发多段）与 144 格仍未验证（无新官方二进制）；`withSpeed=true` 测速腿未验证（无 live browser / 远端节点）——沿袭 R15 §8 O-L4-5 |
| O-L4-6 | Xray 25.8.29 二进制：R15 的 `~/.tmp/xv-25.8.29` 已不在盘；本轮经直连 GitHub release 资产重下（`Xray-linux-64.zip` 20,188,542 字节，构建哈希 `81b7cd7` 与 R15 的 25.8.29 binary 逐字一致），存于 `~/workspace/.r16-tmp/bin/xv-25.8.29/xray`。附带发现：`ghfast.top` 代理现对 XTLS/Xray-core 返回 403 `Forbidden by black list`（与 MEMORY 记载的 XTLS 分线路 403 一致），直连 GitHub 正常 |
| O-L4-7 | VG-10 前端文件已搬家：`vite-frontend/src/pages/inbound-form-modal.tsx` → `vite-frontend/src/pages/inbound/inbound-form-modal.tsx`；描述 :915 / placeholder :917 行号与 R15 逐字一致，值未变 |
| O-L4-8 | R3 成功链接数本轮 8 条 vs R15 9 条：探针构造差异（本轮未另设第二条 ss 链接），12 格逐格证据齐全，非产品行为差 |

## §9 未验证项（单列）

| # | 内容 | 原因 |
|---|---|---|
| U-1 | 浏览器 DOM 腿（前端渲染、弹窗交互、demo 页实际展示） | 无 live browser |
| U-2 | 真 PG 方言面 | 本轮 SQLite 进程内验证；PG 契约归发版门 |
| U-3 | 真实 GitHub Gist 端到端、真机恢复后节点终态 | 无真实令牌/真机；mock 与门证据承继历轮 |

## §10 探针修正手记（诚实记录）

本轮探针自身五次修正，均已重跑并以最终日志为准：

1. **R1 ss2 密码算法**：初版用 `chacha20-poly1305`，得显式拒绝「Shadowsocks 加密方式 cipher 不受支持」——核查确认该方法名不在 `ValidSSCipher` 合法集内（mihomo-only 单面集是 `chacha20`，非 `chacha20-poly1305`）；拒绝行为本身正确，是探针选值错、非产品缺陷。改用 `aes-128-gcm` 后重跑通过。
2. **R4 ss shadow-tls 伴生键**：初版断言 `sub("shadow-tls")`，实测伴生落在 `plugin-opts` 下（ss 分支与 snell 分支键位不同）；是探针断言错、非产品行为错。按 `plugin-opts` 修正后重跑通过。
3. **R5 Listen 地址**：初版用 `0.0.0.0`，13 行整行被拒「地址不能是本机或 0.0.0.0」——`ValidateImportedProxyRowAddress` 的既有门；是探针构造错、非产品缺陷。改用 `203.0.113.100` 后重跑通过。
4. **VG-04 客户端配置文件路径**：初版脚本误用 `~/.tmp-big/r11m3/enc-canonical.json`（该文件不存在，xray 报 no such file）；R15 用的是 `~/.r9bin/cfg/enc-canonical.json`。修正路径后重跑：25.8.29 exit 23 / 25.9.5 exit 0，与 R15 逐字一致。
5. **VG-11 计数口径 / VG-10 前端路径**：初版区分大小写 `grep -c family` 得 6/5、前端旧路径报 No such file；核查确认 R15 用的是 `grep -ci family`（10/9）且前端文件已搬至 `src/pages/inbound/` 子目录（行号不变）。修正后重跑，与 R15 逐字一致。

## §11 证据清单（`~/workspace/flvx-max/audit-out-132/r16/`）

| 文件 | 内容 | 字节数 |
|---|---|---|
| `R16-L4-matrix-probe.log` | 附则 A R1–R4 + A-2 进口谓词 + 消费面探针：6/6 PASS，131 行 CELL | 12943 |
| `R16-L4-matrix-supplement.log` | R4 snell shadow-tls 伴生补格：1/1 PASS，CELL 1 行 | 421 |
| `R16-L4-r5-probe.log` | 附则 A R5 探针：2/2 PASS，18 行 CELL | 3937 |
| `R16-L4-kernel-full.log` | go-backend kernel 包全量 `-v`：270 PASS / 0 FAIL / 0 SKIP（R15 264 + 本轮 6 个新探针；含 ENC 99 格、A-2 枚举 61 项、F-03 修复测试） | 61464 |
| `R16-L4-handler-gate.log` | handler 门测试：7/7 PASS（ENC 版本门边界、F-08×4、F-04×1） | 7004 |
| `R16-L4-handler-gist.log` | handler Gist 守卫测试：4/4 PASS（F-01×2、F-02 相关×2） | 2871 |
| `R16-L4-repo-gate.log` | repo 门测试：3/3 PASS（R5 WhitelistExtra×2、F-08×1） | 2454 |
| `R16-L4-socket-tests.log` | go-gost/x 全包 `-v`：250 PASS / 0 FAIL / 7 SKIP；其中 socket 包 94 PASS / 0 FAIL（与 R15 同数） | 74083 |
| `R16-L4-binary-probe.log` | 版本门 13 行二进制/代码复证：VG-01…VG-08/VG-12/VG-13 二进制通过、VG-09 搜索 0 命中、VG-10 三方、VG-11 tag 考古、反扫无第 14 门 | 11453 |
| `R16-L4-routes-raw.csv` | 路由原料表：203 行含表头（202 条） | — |
| `R16-L4-tables-raw.txt` | 数据表原料表：49 行 | — |
| `R16-L4-census.py` | 路由/数据表普查脚本（R15 脚本仅改读数树路径） | — |

## 结论

附则 A 60 格 0 静默丢弃（R15 遗留 R4 snell shadow-tls 伴生未单列项本轮已补格闭合）；附则 A-2 11 行全部通过；版本门 13 行 12 行复证通过（VG-09 沿袭未验证）；ENC 文法 99 格全表 PASS；路由 202 / 数据表 49 双向差集 ∅（TableName 48 口径已澄清：第 49 个模型为 repo 包的 `NodeSeq`，自带 `TableName()`，无缺口）；同字段消费面抽核 22 CELL 全过；R14 本车道修复复验全部维持关闭。**新立案 0 条（P0/P1/P2/P3 均为 0）。**

END
