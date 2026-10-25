# R19-L4 常设全集复跑报告 — 附则 A / 附则 A-2 / 版本门 / ENC 文法 / 路由与数据表 / 同字段消费面 / 列表与详情字段对账

- **审计轮次**：R19（靶向轮·常设全集复跑，L4 车道）
- **基线（pin 死）**：`main @ d66042c`（完整 `d66042cc0b7ac011ddfad09f3f2709b90dafda2e`，「docs(r18): archive R18 audit records + integration report (FIX-INT-REPORT)」）；相对 R18 基线 `a0c18a0` 的增量 = fix-r18-f01（`go-backend/internal/http/handler/tunnel_delete_resolution.go`，隧道删除中途失败披露已删转发）+ fix-r18-f02f04（go-gost 管理 API 按名去重：`program.go`、`x/api/config_*.go` 16 文件、`x/config/dedupe.go`、`x/socket/chain.go`、`x/socket/limiter.go`）+ fix-r18-f03（去重 chains/limiters + 启动自愈毒化 gost.json）+ 测试与审计归档；**kernel / proxy_parse / proxy_produce / credentials / store/repo / handler.go / handler/kernel.go / middleware / vite-frontend / doc 零改动**（`git diff a0c18a0..d66042c -- go-backend/internal/kernel/ go-backend/internal/store/ vite-frontend/src/ go-backend/internal/http/handler/handler.go go-backend/internal/http/handler/kernel.go go-backend/internal/http/middleware/ doc/` 输出 0 行）
- **依据**：`~/workspace/user/files/flmx-audit-catalog-v132.md`（附则 A、附则 A-2 为正本）与 r18/R18-L4-matrices.md（上轮方法与结果）及 R18 原料表 `R18-L4-routes-raw.csv` / `R18-L4-tables-raw.txt`。护栏 N-01…N-37 全程有效
- **性质**：只读审计。不改产品代码、不合 main、不打 tag、不发版。不碰测试机（纯本地工作）
- **读数树**：只读 worktree `~/workspace/.r19-tree @ d66042c`（detached HEAD；本轮结束时 `git status` 仅 `?? audit-out-132/r19/` 一项未跟踪——为基线 lane 预置的 `R19-00-baseline.md` / `R19-00-sentinel.md`，本车道未改读数树内任何文件）；共享工作树 `~/workspace/flvx-max` 未用作读数树、未 checkout、产品文件未改
- **测试隔离**：快照 `~/workspace/.r19-tmp/r19base`（`git archive d66042c` 解包）；探针文件 `r19_l4_probe_test.go`（kernel 包）、`r19_l4_r5_probe_test.go`（repo 包）只放快照内，跑后即删（N-37 细则④）；删后快照内无 `r19*` 探针文件残留（已核查，`find -name 'r19*'` 仅命中快照目录本身）
- **证据纪律（N-37）**：本报告全部探针类结论只认落盘原始日志 + 完整名单存档（`R19-L4-*.log`，见 §11）；批报告摘要计数不得作为裁定依据；探针输入全部为本轮自构（主机/端口/密码/UUID 均换值：`r19-` 前缀、`203.0.113.31+`、`59595595-5955-4555-8555-595955955595`、`29xxx` 端口），不以 R18 日志摘要顶替本轮实测
- **Go 工具链**：`~/go/bin/go`（go1.25.1；`go` 不在 exec 默认 PATH 内，须用绝对路径）；`TMPDIR=/home/hatch/tmp-go`（避开 /tmp tmpfs 满的环境红，见 R17-FIX-B §3.4 同型）

## §0 结论总表

| 全集 | 规模 | 复跑结果 | 新立案 |
|---|---|---|---|
| 附则 A 转换器×凭据矩阵 | 60 格（R1–R5 × C1–C12） | **0 静默丢弃**（搬运/透传/显式拒绝/不适用逐格有据） | 0 |
| 附则 A-2 枚举字段合法集 | 11 行 | **11/11 通过**（进口谓词 + 存储规范化 + 消费面四面） | 0 |
| 版本门全集 | 13 行 | **12 行二进制/代码复证通过，1 行（VG-09）未验证**（沿袭 R14/R15/R16/R17/R18 观察，非新缺口） | 0 |
| ENC 文法全表 | 99 格 | **99/99 PASS**（`TestR11B3X007Full99Table`，谓词/进口/存量/双二进制四方一致） | 0 |
| 路由钉死值 | handler.go 201 + router.go 1 = 202 | **与 R18 原料表双向差集 ∅**（鉴权/守卫分布逐字一致；`source` 列行号零漂移） | 0 |
| 数据表钉死值 | 49 张 | **与 R18 原料表双向差集 ∅**（逐字一致，含行号口径） | 0 |
| 同字段消费面全集 | 22 CELL | **22/22 通过**，无毒化值下发 | 0 |
| 列表/详情字段对账 | 节点列表 32 可能键 + 入站列表 44 键 + 嵌套 3 组 + 延迟形态 1 组 | **差集 ∅**（入站 44/44、嵌套全等、延迟形态等价；节点键数订正见 §7.1 注——R18「31 键」为算术误记，实为 32 键，结论不变；观察项 3 条见 §7.3，不立案） | 0 |

**新发现问题：0 条。L4-CAND-## 立案：无（P0=0 / P1=0 / P2=0 / P3=0）。** R18 在本车道范围内的修复复验见 §8，全部维持关闭；基线新增的 fix-r18-f02f04 去重测试 3/3 通过（socket 包，见 §8 末）。

---

## §1 附则 A 转换器 × 凭据矩阵 60 格（主责逐格回勾）

探针：`TestR19L4MatrixR1Singbox` / `R2Clash` / `R3URI` / `R4Surge`（`R19-L4-matrix-probe.log`，CELL 193 行）+ `TestR19L4MatrixR4SnellShadowTLSCompanion`（同日志内单跑，CELL 4 行，计入 193 行内）+ `TestR19L4MatrixR5Restore`（`R19-L4-r5-probe.log`，CELL 23 行）。统一入口 `ParseImport`（R5 为 `repo.Import`）。格状态定义按 catalog 附则 A 格定义注记（以源格式字段存在性为准）。

列：C1 `password`、C2 `uuid`、C3 `psk`、C4 `cipher/method/security`、C5 `encryption`、C6 `flow`、C7 `username`、C8 `plugin/plugin-opts`、C9 `reality-opts`、C10 `obfs/obfs-password`、C11 `version/mode`、C12 `alterId`。

### 1.1 R1 sing-box JSON `singboxToProxy`（12 格全搬运✓，CELL 39 行）

14 个节点出站（另 2 个 `shadowtls` detour 出站非节点）零错误入库。逐格：

| 格 | 状态 | 实证（CELL） |
|---|---|---|
| C1 password | 搬运✓ | ss `r19-ss-pw1`/`r19-ss-pw2`/`r19-ss-pw3`、trojan `r19-tj-pw1`、hy2 `r19-hy2-pw1`、tuic `r19-tuic-pw1`、anytls `r19-at-pw1`、naive `r19-na-pw1`、socks `r19-so-pw1`、http `r19-ht-pw1` 逐字 |
| C2 uuid | 搬运✓ | vmess/vless/tuic `59595595-5955-4555-8555-595955955595` 逐字 |
| C3 psk | 搬运✓ | snell `r19-snell-psk1` / `r19-snell-psk2` 逐字 |
| C4 cipher | 搬运✓ | ss `aes-256-gcm`/`aes-128-gcm`、vmess `aes-128-gcm`（←security）逐字 |
| C5 encryption | 搬运✓ | vless ENC 全串逐字（法定测试串 `mlkem768x25519plus.native.0rtt.nxXrBunl3L3TglUpHVbQ3QuLltBtwDVfmCWBTurY-Ag`） |
| C6 flow | 搬运✓ | vless `xtls-rprx-vision` 逐字 |
| C7 username | 搬运✓ | naive `r19-na-user`、socks `r19-so-user`、http `r19-ht-user` 逐字 |
| C8 plugin | 搬运✓ | ss obfs `mode=http host=r19ob.example.net`；ss shadow-tls detour → `plugin=shadow-tls` + `plugin-opts{password=r19-stls-pw1, host=stls.r19.example.net}`；snell shadow-tls detour → `shadow-tls{password=r19-stls-pw2, host=sn.r19.example.net}` |
| C9 reality-opts | 搬运✓ | vless tls.reality → `reality-opts{public-key=32 零字节 RawURL, short-id=cdef}` 逐字 |
| C10 obfs | 搬运✓ | hy2 `obfs=salamander`、`obfs-password=r19-hy2-obfs1`、`ports=30000-30010`（`server_ports ["30000:30010"]` 合并形态） |
| C11 version/mode | 搬运+愈合✓ | snell `version=6`、`mode=unshaped`（v6+unshaped 保留） |
| C12 alterId | 搬运✓ | vmess `alter_id=1` → `alterId=1` |

### 1.2 R2 Clash YAML 透传（12 格全透传✓，CELL 23 行）

11 个节点零错误入库，逐列逐值断言：

| 格 | 状态 | 实证 |
|---|---|---|
| C1–C10 | 透传✓ | password/cipher/uuid/flow/encryption/reality pbk+sid/obfs/obfs-password/plugin-opts `{mode=tls, host=r19cob.example.net}` 全部逐字同值（CELL 逐项） |
| C11 version/mode | 透传✓（逐值） | snell `version=6`、`mode=unshaped` 逐值断言 |
| C12 alterId | 透传✓（逐值） | vmess `alterId=1` 逐值断言 |

### 1.3 R3 URI 逐参数（10 格搬运✓ + 2 格显式拒绝✓，CELL 31 行）

8 条链接零错误入库；另 2 条显式拒绝：

| 格 | 状态 | 实证 |
|---|---|---|
| C1 password | 搬运✓ | ss/trojan/hy2/tuic/anytls/socks `password` 逐字 |
| C2 uuid | 搬运✓ | vmess/vless/tuic uuid 逐字 |
| C3 psk | 显式拒绝✓ | `snell://` → `暂不支持 snell:// 链接`，0 节点入库（非静默丢弃） |
| C4 cipher | 搬运✓ | ss userinfo `aes-256-gcm`、vmess JSON `scy=aes-128-gcm` 逐字 |
| C5 encryption | 搬运✓ | vless query `encryption` 全串逐字 |
| C6 flow | 搬运✓ | vless `flow=xtls-rprx-vision` 逐字 |
| C7 username | 搬运✓ | socks5 userinfo `r19-uri-so-user`；http `http://` → 显式拒绝「`http(s):// 是订阅地址，请单独粘贴在输入框里`」（该 scheme 无节点载体，属有意保留） |
| C8 plugin | 搬运✓ | ss plugin 参数 `obfs=http obfs-host=r19uob.example.net` 逐字 |
| C9 reality-opts | 搬运✓ | vless `pbk`/`sid` → `reality-opts{public-key, short-id=ab12}` 逐字 |
| C10 obfs | 搬运✓ | hy2 `obfs=salamander`、`obfs-password=r19-uri-hy2-obfs` 逐字 |
| C11 version/mode | 显式拒绝✓ | 同 C3（`snell://` 整体不受支持） |
| C12 alterId | 搬运✓（逐值） | vmess JSON `aid=1` → `alterId=1` 逐值断言 |

### 1.4 R4 Surge/Loon 行（9 格搬运✓ + 2 格显式拒绝✓ + 1 格不适用，CELL 48 行主表 + 4 行补格）

13 行零错误入库；另 vless 行显式拒绝：

| 格 | 状态 | 实证 |
|---|---|---|
| C1 password | 搬运✓ | ss/trojan/hy2/tuic/anytls/socks/http `password` 逐字 |
| C2 uuid | 搬运✓ | vmess/tuic `username=`/`uuid=` 逐字 |
| C3 psk | 搬运✓ | snell `psk=r19-sg-sn-psk1` / `r19-sg-sn-psk2` / `r19-sg-sn-psk3` 逐字 |
| C4 cipher | 搬运✓（R10X-013 订正格） | vmess `encrypt-method=aes-128-gcm` 逐字落库；ss `encrypt-method` + Loon positional `aes-128-gcm, "r19-sg-loon-pw2"` 两路均搬运 |
| C5 encryption | 显式拒绝✓ | vless 行 → `暂不支持 vless 类型的节点`，0 节点入库 |
| C6 flow | 显式拒绝✓ | 同 C5（vless 行整体拒绝） |
| C7 username | 搬运✓ | socks5/http `username=` 逐字 |
| C8 plugin | 搬运✓ | ss `obfs=http + obfs-host` → `plugin-opts{mode, host}`；ss shadow-tls 伴生 `shadow-tls-password/sni/version` → `plugin-opts{password, host, version=3}`；snell shadow-tls 伴生见注② |
| C9 reality-opts | 不适用 | 字段存在性证据：reality 只随 vless 承载（`parseSurgeLine` 全分支无 reality 载体），而 vless 行整体显式拒绝——无载体故无丢弃可能 |
| C10 obfs | 搬运✓ | hy2 `salamander-password` → `obfs=salamander` + `obfs-password`，`port-hopping` → `ports=30000-30010`；snell `obfs=http` → `obfs-opts{mode=http}` |
| C11 version/mode | 搬运+愈合✓ | snell `version=6`、`mode=unshaped` 逐字 |
| C12 alterId | 搬运✓ | vmess `vmess-aead=false` → `alterId=1` |

注②：R4×C8 的 snell shadow-tls 伴生**本轮仍单列断言**（`TestR19L4MatrixR4SnellShadowTLSCompanion`，CELL 4 行）：Surge snell 行 `shadow-tls-password/sni/version` 经 `applySurgeShadowTLS` 落 `shadow-tls{password=r19-sg-sn-stls-pw, host=snst.r19.example.net, version=3}`，psk 与 version=6 本体字段同时在位。本轮初版即用 `shadow-tls-sni=` 键（沿 R17 手记①教训），一次通过。

**附则 A 结论：60 格 0 静默丢弃。** 任一格均无「输入带字段、产物无字段、无拒绝无回执」。

### 1.5 R5 备份 Extra 载体（10 行合法 + 3 行坏值 + WhitelistExtra 3 格，CELL 23 行）

探针 `TestR19L4MatrixR5Restore`（`R19-L4-r5-probe.log`，1/1 PASS）：

| 行 | 状态 | 实证 |
|---|---|---|
| ss / trojan / hysteria2 / tuic / vmess / vless / snell / naive / socks5 / ss-plugin（10 行） | 逐字恢复✓ | `InboundsImported=13`（含下述 3 行白名单行）；每行 `Extra` 与备份字节逐字相等（DB 回读 `GetNodeInbound(id).Extra`）；其中 ss-plugin 行备份故意写错 `trojan`，落库为 `shadowsocks`（R13X-006 重推导在位） |
| 坏 ENC vless / 坏 Reality pbk vless / 坏 SS cipher ss（3 行） | 整行跳过✓ | `SkippedRows[inbounds]=3`；三行 DB 零行；三行名逐一点名进 `ImportWarnings` |
| WhitelistExtra 合法 `10.0.0.0/8, 1.2.3.4` | 全留✓ | 逐字恢复（行 #301） |
| WhitelistExtra 混合 `10.0.0.0/8, not-an-ip, 192.168.0.0/16` | 去坏留好✓ | 落库 `10.0.0.0/8, 192.168.0.0/16`，行本身照常恢复（行 #302） |
| WhitelistExtra 全坏 `not-an-ip, also bad!!` | 清空✓ | 落库空串；warnings 点名 `#302` / `#303` 两行（行 #303） |

探针手记（本轮探针构造侧三处修正，均非产品问题，修正后一次通过）：① R1/R3 的 ss obfs 插件参数须用 `applySSPlugin` 的键名 `obfs=`/`obfs-host=`（初版误写 `mode=`/`host=` 致 mode 为空）；② R5 导入型入站行的 `Listen` 不可用 `::`（`ValidateImportedProxyRowAddress` 判未指定地址拒收），改与 Extra 同值的 `203.0.113.120`；③ R5 坏行 absent 断言须判 `GetNodeInbound` 返回指针为 nil（该函数未找到时返回 `(nil, nil)` 而非 error）。

---

## §2 附则 A-2 枚举字段合法集 11 行（主责逐行回勾）

进口谓词探针 `TestR19L4A2Predicates`（26 CELL：每行坏值显式拒绝 + 好值携带/愈合）+ 消费面探针 `TestR19L4FacesConsumption`（22 CELL，见 §6）+ 库内枚举测试全量（`R19-L4-kernel-full.log` 内 61 项 `TestR5B1/TestR6B1/TestR10B1/TestR10B3/TestR11B1/TestR11B2/TestR11B5/TestR4B1/TestR8B1/TestR9B2/TestR3B2/TestR2B5` 前缀测试全 PASS，与 R18 同数；全包 271 PASS / 0 FAIL，见 §4）。

| 行 | 字段 | 进口谓词 | 存储规范化 | 渲染面处置 | 结论 |
|---|---|---|---|---|---|
| A2-01 | client-fingerprint | 垃圾值显式拒绝（点名合法集） | `Chrome` → `chrome` 愈合落库（进口谓词本身小写愈合） | 存量 `Chrome` 经 `forEndpoint` 愈合、sing-box 产物 `chrome`；垃圾值四面谓词全不过、两渲染面整节点略过 | ✅ |
| A2-02 | xhttp-opts.mode | 垃圾值显式拒绝（点名 {auto, packet-up, stream-up, stream-one}） | 嵌套 `packet-up` 携带；`PACKET-UP` 经进口谓词本身小写愈合 `packet-up` | 同谓词口径 | ✅ |
| A2-03 | SS plugin-opts.mode | 垃圾值显式拒绝（点名 {http, tls}） | `TLS` → `tls` 愈合落库 | 同谓词口径 | ✅ |
| A2-04 | Snell obfs-opts.mode / v6 顶层 mode | 垃圾值显式拒绝（点名 {http, tls, shadow-tls, restls, jls, ∅}） | 别名 `shadowtls` → `shadow-tls` 愈合；`UNSHAPED` 进口愈合 `unshaped`、存量经 `forEndpoint` 愈合 `unshaped` | 同谓词口径 | ✅ |
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

二进制复证日志 `R19-L4-binary-probe.log`（在盘二进制：sing-box 1.11.15/1.12.17/1.13.14（`~/workspace/.r9bin/sing-box-1.1x` 文件本体）、1.14.3（`~/workspace/.r4b1-bin/sing-box-1.14.3-linux-amd64/sing-box`）、Xray 25.3.6/25.5.16/25.6.8/25.9.5（`.r9bin`）、25.8.29（`~/workspace/.r16-tmp/bin/xv-25.8.29/xray`，构建哈希 `81b7cd7` 与 R15/R16/R17/R18 的 25.8.29 binary 一致，本轮仍在盘复用）。代码门行号按本轮基线树（`handler/kernel.go` 三处 ENC 门 :2007/:2032/:2118，与 R18 逐字同行；函数定义 :1997/:2031/:2083；出站 prefer `kernel_egress.go:48` 逐字不变）。

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
| VG-09 | Snell 服务端 | ⚠️ **未验证**（在盘全根搜索 `snell-server` 0 命中；代码自洽测试 `TestR2B5Snell*`/`TestKernelVersionGateSnell` PASS 不等于服务端实测）——沿袭 R14/R15/R16/R17/R18 观察 O-L4-1，非新缺口，不立案 |
| VG-10 | Reality 默认 1.0.0 | ✅ 三方一致：`kernel/model.go:211` `DefaultRealityMinClientVer = "1.0.0"`、`doc/guide/faq.md:61`、前端 `inbound-form-modal.tsx:915` 描述 / `:917` placeholder——**指针零漂移**（与 R18 逐字同行；doc/ 与 vite-frontend/ 在 R18→R19 间 diff 0 行） |
| VG-11 | 双栈回程 agent 1.2.0 | ✅ 版本考古（非二进制实测）：tag `1.1.1` 两侧 family 计数 0/0；tag `1.2.0` 两侧 10/9；引入提交 `58dadac`/`9756636` 首含 tag 均为 `1.2.0`；现行树仍 10/9；文档 `nodes.md:213` 下限逐字属实（注：计数口径为 `grep -ci family`，与 R18 同法） |
| VG-12 | Naive 出站 1.13 | 1.12.17 exit 1（unknown outbound type: naive）/ 1.13.14 exit 0；naive 入站在 1.11.15 已解码、仅因测试证书文件缺失失败（`/tmp/cert.pem` 不存在） |
| VG-13 | XHTTP 无门 | 观察：25.3.6 与 25.9.5 对面板形 xhttp 配置均 exit 0，下限 ≤25.3.6，维持观察 |
| — | 「及以上」反扫 | 无第 14 个门：`versionBelow/versionBelow3/versionBelowPadded` 全部调用点 19 处枚举完毕（R18 同口径 grep），下限集 = {1.12, 1.13, 1.14, 25.6.8, 25.9.5}，与 13 行定义逐一对应 |

**版本门结论：12 行复证通过 + VG-09 未验证（沿袭）+ 无第 14 门。**

---

## §4 ENC 文法全表 99 格

`TestR11B3X007Full99Table` 在 `R19-L4-kernel-full.log` 内 **PASS**（表内 `len(r11b3Cells99) == 99` 断言在位；逐格四方对账：谓词 `ValidVLESSEncryption` / 进口 `ParseImport(Clash VLESS)` / 存量谓词 `validImportedProxyFields(·, ssFaceClash)` / 双二进制实测正本 `BinaryAccept`，除空白特殊格 F13/S1/S2 外三方一致；该三格口径为 Pred=false、BinaryAccept=false、ImportStored=true，修剪形 Clash 下发由 `TestR11B3X004*` 另行销账）。

谓词 `ValidVLESSEncryption`（`kernel/credentials.go:521` 起，与 R18 同行）与 catalog A2-10 逐字相符：段名大小写精确、`{∅, none}` 合法、元素数 {3,4,5}、首组前三数、位数按 token 原始长度 ≤11 计符号、4 段官方默认串接受、服务端形（ticket 占 rtt 位）拒。本轮另有 ENC 相关 17 项 PASS（`TestR8B1ENC*` / `TestR9B2*` / `TestR10B2*` / `TestR11B3*` 前缀，含 `TestR8B1ENCVersionGateBoundary` handler 门测试，见 §8），URI 面谓词门在位（`proxyURI` vless/trojan 支 `kernel/proxy_produce.go:15` 起，:84 附近 `validImportedProxyFields(p, ssFaceOther)`；进口谓词 `validateProxyCredentials` vless 支 `kernel/proxy_parse.go:251`）。

kernel 全量：**271 PASS / 0 FAIL / 0 SKIP**（与 R18 同数；基线 264 项不变 + 本轮探针 7 项）。

**ENC 结论：99/99 PASS，无回归。**

---

## §5 路由与数据表钉死值（同 R18 口径重做，非抄旧数）

方法：普查脚本 `R19-L4-census.py`（R18 脚本仅改读数树路径 `.r18-tree` → `.r19-tree`，其余逐字同口径）：`handler.go` 的 `Register` 内全部 `mux.HandleFunc`（字面量 200 条 + `ghMirrorPath` 常量 1 条，`gh_mirror.go:32`）+ `router.go:13` 的 `/system-info` 1 条；鉴权分类器逐字复刻 `middleware/auth.go` 的 `shouldSkip` / `allowsOptionalAuth` / `requiresAdmin`；方法守卫逐 handler 函数体 `r.Method` 检查推导；数据表取 `repository.go:327` 的 AutoMigrate 模型集逐项抽取。原料表 `R19-L4-routes-raw.csv`（203 行含表头）、`R19-L4-tables-raw.txt`（49 行）。

| 项 | 本轮实测 | 与 R18 原料表双向差集 |
|---|---|---|
| 路由总数 | **202**（handler.go 201 + router.go 1；`/api/v1` 197 + 非 API 5） | ∅（路径/ handler / 守卫 / 鉴权 / `source` 行号五列逐行一致，**零漂移**；R18 的 +15 行号漂移未延续，因 `handler.go` 本轮零改动） |
| 鉴权分布 | ADMIN 135 / JWT 44 / PUBLIC 17 / OPTIONAL-AUTH 1 / 非 API 5 | ∅（与 R18 分布逐字一致） |
| 方法守卫分布 | POST only 131 / ANY 53 / GET only 13 / not(Get,Post) 1 / GET,HEAD 2 / GET,POST 1 / GET (WS upgrade) 1 | ∅（与 R18 分布逐字一致） |
| 数据表总数 | **49**（48 个 `model.*` + 仓库内 `NodeSeq`） | ∅（集合与顺序逐行一致，逐字 `diff` 空） |

注：`NodeTraffic` 本轮实测在 `repository.go:360`（与 R18 逐字同行）；模型集合与顺序与 R18 原料表逐行一致。

**TableName() 口径注记**：`TableName()` 定义计数 48 是 **model 包口径**——`internal/store/model/` 内 48 个模型定义全数有 `TableName()`；AutoMigrate 49 项中**没有**缺 `TableName()` 定义的模型：第 49 个是 `NodeSeq`（`internal/store/repo/node_seq.go:20` 定义，`TableName()` 在同文件 :28，返回 `"node_seq"`），它不在 model 包内，故不计入 48。另有一个非数据表定义 `schemaMigration`（`migration_r5b4_v1.go:30`，迁移辅助表）不在 AutoMigrate 集内。结论：48 与 49 之差 = `NodeSeq` 的包归属差异，**非漂移、非缺口**，不得误报（与 R18 结论逐字一致，本轮重测确认）。

**路由/数据表结论：钉死值 202 / 49 维持，双向差集 ∅。**

---

## §6 同字段消费面全集（与附则 A-2 邻接，22 CELL 全集）

探针 `TestR19L4FacesConsumption`（`R19-L4-matrix-probe.log`，22 CELL 全过）：

- **坏值面 11 CELL**：fingerprint / flow / TUIC cc / Hy2 obfs / VMess cipher / SS cipher / ENC / SS plugin mode / Snell obfs / Reality pbk / Reality sid，坏存量在 `validImportedProxyFields` 四面（sing-box / Clash / other / Xray 链式）谓词**全不过**。
- **愈合面 3 CELL**：存量 `Chrome` → `forEndpoint` 愈合 `chrome`、存储不动、sing-box 产物 `utls.fingerprint=chrome`；存量 `PACKET-UP` → 愈合 `packet-up`；存量 Snell v6 `UNSHAPED` → 愈合 `unshaped`。（探针手记：愈合渲染断言须给端点置 `tls: true`，否则 sing-box 产物无 tls 块可验——探针构造问题，非产品问题。）
- **好值面 4 CELL**：flow / TUIC cc / VMess cipher / ENC 合法值四面谓词全过；SS mihomo-only `chacha20` 分面 1 CELL：仅 Clash 面过、sing-box/other/Xray 链式三面不过（与 A2-09 单面集口径逐字一致）。
- **渲染略过面 1 CELL**：fingerprint 坏值在 sing-box 与 Clash 两渲染面整节点略过（`proxySingbox` / `proxyClash` 均不产出）。
- **URI 面 2 CELL**：合法 ENC 串 `proxyURI` 逐字下发；坏 ENC 不发链。
- **链式面**：`TestR6B1ChainTargetGuards`（库内，`R19-L4-kernel-full.log` 内 PASS）+ F-03 修复复验（见 §8）。

**消费面结论：22 CELL 全过，无原样下发的毒化值，无新增立案。**

---

## §7 列表/详情字段对账（列表接口实际返回字段 vs 页面渲染字段逐项枚举）

方法：以后端列表 handler 实际构造的返回键集为准（非文档、非类型声明），逐项对前端 `api/types.ts` 声明与页面实际读取；凡后端发、前端不读的键单列为“发送未读”，凡前端读、后端不发的键单列为“读取无源”（后者即断链）。对账脚本 `R19-L4-fieldcheck.py`（输出 `R19-L4-fieldcheck.log`），关键行段另逐行人工复核。

### 7.1 节点列表 `/node/list`（`nodeList`，`handler.go:776`）

后端键集 = `repo.ListNodes()` 27 键 + `syncRemoteNodeStatuses` 条件 3 键 + `attachNodeLatency` 条件 2 键，共 **32 个可能键**：

| 来源 | 键 |
|---|---|
| `ListNodes`（`repository.go:1065-1089`，恒发） | `id, inx, name, remark, expiryTime, renewalCycle, ip, serverIp, serverIpV4, serverIpV6, extraIPs, port, tcpListenAddr, udpListenAddr, version, http, tls, socks, status, isRemote, forwardMode, remoteUrl, remoteToken, remoteConfig, expiryReminderDismissed, interfaceName, sshConfig`（27 键——`ip` 与 `serverIp` 为同一值的双键；`sshConfig` 仅 nftables 节点有值、其余为 null） |
| `syncRemoteNodeStatuses`（`federation.go:2510-2518`，仅远端节点且同步有结果时） | `status`（覆写）、`syncError`、`panelVersion`、`versionWarning`（后三者仅有值时才附） |
| `attachNodeLatency`（`node_latency.go:593`，条件附） | `latency`（仅内存有 ≥1 点结果时）、`latencyTargets{points, pinned}`（仅节点有显式自选时，含显式空选） |

**键数订正（对上轮报告算术的更正，非产品变化）**：R18 §7.1 正文明「`ListNodes()` 26 键……共 31 个可能键」，但其同一节表格内逐项列出的键实为 27 个（`id, inx, name, …, sshConfig` 逐数 27），26/31 为算术误记；本轮逐键重数确认恒发 27 键、总可能键 32。同理 `NodeApiItem` 具名声明实为 **28 个字段**（R18 称 27 个；逐数：27 个恒发键全覆盖 + `syncError` = 28），R18「26 个恒发键中的 25 个」一并订正为「27 个恒发键全覆盖」。订正只改计数，不改结论：无读取无源的断链键。

前端对照：

- `NodeApiItem`（`api/types.ts:1`）具名声明 28 个字段，覆盖 27 个恒发键全部 + `syncError`；**未具名声明**的后端键：`latency`、`latencyTargets`、`panelVersion`、`versionWarning`——但该接口带 `[key: string]: unknown` 索引签名，四键均可合法读取，`node.tsx` 实际读取 `node.latency`（:1434/:1441/:2340）与 `node.latencyTargets`（:1410/:1440/:2342）共 6 处，渲染链路通（“不测延迟”药丸分支 `latencyTargets.points.length === 0` 与“自定义”注记 `latencyTargets ? "自定义"` 均以列表实回字段为源，无 1.4.8 式断链）。
- **发送未读**：`panelVersion`、`versionWarning`——全前端 `grep` 0 命中（`types.ts` 亦未声明），后端只对远端节点附带、无页面消费（版本警告另由服务端 `logFederationVersionWarning` 落日志）。属冗余发送、非断链，见观察 L7-3。
- **读取无源**：0。`node.tsx` 另读的 `systemInfo` / `connectionStatus` / `upgradeLoading` / `rollbackLoading` / `copyLoading` 均为页面内客户端自有富化字段（`systemInfo` 来自逐节点的 `/system-info` 另取，`connectionStatus` 来自 WS/状态轮询，`…Loading` 为本地交互态），不是 `/node/list` 的承诺字段，不计入差集。
- `latency` 形态对账：后端 `externalProbe{ok, skipped?, latencyMs?, error?, checkedAt, points?: latencyPoint[]}`（`kernel_probe.go:29-37`），`latencyPoint{name, pinned?, ok, latencyMs?, lossPct?, error?, route?, routeV6?}`（`node_latency.go:49-60`）；前端以 `InboundProbe{ok, skipped?, latencyMs?, lossPct?, from?, error?, checkedAt, points?: InboundProbePoint[]}` 消费，`InboundProbePoint` 字段集与 `latencyPoint` **逐字相同**；`lossPct` / `from` 为前端可选字段、后端恒不发，`probePoints()` 的无点回退单点分支（`name: probe.from || "面板服务器"`，`latency-cell.tsx:54`）在列表附带形态下不可达（`attachNodeLatency` 只在 `len(Points) > 0` 时附 `latency`）。形态等价，无断链。

### 7.2 入站列表 `/inbound/list`（`inboundList`，`kernel.go:584`）

后端 `inboundView`（`kernel.go:62-122`）44 个 JSON 键 vs 前端 `InboundApiItem`（`api/types.ts:880`）44 个字段：**逐项双向差集 ∅**（脚本比对，后端独有 0、前前端独有 0）。嵌套三组亦全等：`settings` 13/13、`transport` 5/5、`security` 20/20（含 `kernelSecurityView` 的 `acmeDnsSet` 覆写键；响应侧 `certPem/keyPem/acmeDnsKey/acmeDnsSecret` 以 `omitempty` 且后端遮蔽、从不回显，与 `InboundSecurity` 注释「响应侧被后端遮蔽」一致）。入站页无独立详情接口（编辑弹窗直接复用列表项），故列表即详情源，同一对账覆盖。

### 7.3 观察项（不立案）

| # | 内容 | 本轮状态 |
|---|---|---|
| L7-1 | `NodeApiItem` 未具名声明 `latency` / `latencyTargets` / `panelVersion` / `versionWarning`，只靠索引签名兜底；与 `InboundApiItem` 具名声明 `probe` 的形态不一致。无类型断裂（`tsc` 侧由索引签名承接），仅可读性/可发现性口径差 | **维持**：`types.ts:1-30` 未具名声明四键、`[key: string]: unknown` 在位；`node.tsx` 6 处读取在位 |
| L7-2 | `latencyTargets` 的“无自选则键缺失”语义仍在：未设置过选择的节点列表项无该键，前端以“键存在 = 自定义”判定；与 1.4.8 修复口径一致，本轮复核渲染分支（:1410/:1440）与之对齐，无断链 | **维持**：`node_latency.go:618-626` 仅显式自选时附键（含显式空选），与前端判定对齐 |
| L7-3 | `panelVersion` / `versionWarning` 由 `/node/list` 发送但全前端 0 读。冗余发送、非断链；是否下线由产品方定夺，本轮不立案 | **维持**：前端 `grep -c` 两键均为 0；`federation.go:2510-2518` 附带条件未变 |

**列表/详情结论：节点 32 可能键与入站 44 键双向差集 ∅，无读取无源的断链键，无新增立案。**

---

## §8 R18 修复在本车道范围内的复验（按对象本身，N-35）

本轮基线与 R18 基线间 `go-backend` 产品代码变化仅 fix-r18-f01 一项（隧道删除披露，`tunnel_delete_resolution.go`）与 go-gost 去重/自愈两批，**均不在本车道面内**（F-01/F-02/F-03 的关闭验证归 L1 车道）；下列为 R14 修复在本车道的维持复验（与 R18 §8 同对象集，门测试重跑）+ 基线新增去重测试：

| R14 项 | 修复批 | 本轮复验对象 | 结果 |
|---|---|---|---|
| A5-01（P1）链式+空闲超时+分流直连悬空 `flmx-idle-*` | F-03（`render_xray.go` / `split.go` / `chain.go`） | 库内 `TestR14F03*` 4 项（`R19-L4-kernel-full.log`） | ✅ 全部 PASS，关闭维持 |
| 148-P3-1 connIdle 越界文案 | F-04 | `TestR14F06F04ConnIdleZeroWording`（同日志） | ✅ PASS |
| 148-P3-4 `attachNodeLatency` 吞 targets 查询错误 | F-04 | `TestR14F06F04AttachLogsTargetsQueryError`（`R19-L4-handler-gate.log`） | ✅ PASS |
| 148-P3-5 单族 sock_diag 失败无声吞掉 | F-04 | `TestR14F06F04SingleFamilyFailureWarns`（`R19-L4-socket-tests.log`，socket 包 99 PASS 内） | ✅ PASS |
| A5-08（P3）Snell+Via 保存/渲染不对称 | F-08 | `TestR14F08ValidateChainVersionRejectsSnellVia` / `TestR14F08SnellViaSplitSaveRejected` / `TestR14F08SnellPoisonedRowHealedOnApply`（handler）+ `TestR14F08ImportStripsSnellPoisonedRouting`（repo）（`R19-L4-handler-gate.log` / `R19-L4-repo-gate.log`） | ✅ 全部 PASS |
| A3-01（P1）单键 `config/get` 读 gist 凭据明文 | F-01 | `TestConfigGetNeverReturnsGistCredentials` / `TestConfigGetAllowsSensitiveKeysForAdmin`（`R19-L4-handler-gate.log`） | ✅ PASS（gist 两键永不返回；其余 sensitive 键管理员仍可读，既有行为不变） |
| A3-02（P3）`gist/save` 无密码守卫 | F-02 | `TestGistDue` / `TestGistStatusTokenHint`（同日志） | ✅ PASS |

门测试小计：handler-gate **24/24 PASS**（与 R18 逐项同名同数：ENC 版本门边界 `TestR8B1ENCVersionGateBoundary`、F-08×5、F-04×1、gist×4、R17B 守卫×10，另 `-run` 模式命中的同批 `TestR14F08BatchResumeDbWriteFailureReportsRealReason` / `TestR14F08ParamErrorsUseErrDefault` 与 `TestConfigGet*` 3 项计入 24 项内）、repo-gate 1/1 PASS。

**基线新增（fix-r18-f02f04）**：socket 包新增去重测试 3/3 PASS（`TestCreateChainDedupesExistingConfigEntry` / `TestCreateConnLimiterDedupesExistingConfigEntry` / `TestCreateLimiterDedupesExistingConfigEntry`）；socket 包本轮 **99 PASS / 0 FAIL / 6 SKIP**（R18 为 96/0/6，+3 全为该批新增；SKIP 集与 R18 同名同因：沙箱网络/权限前提不在场致跳过）。

---

## §9 观察项（不立案）

| # | 内容 |
|---|---|
| O-L4-1 | VG-09 Snell 服务端仍未验证（在盘全根搜索 `snell-server` 0 命中；沿袭 R14 O-A5-2 / R15 O-L4-1 / R16 O-L4-1 / R17 O-L3-1 / R18 O-L4-1）。代码自洽测试 PASS 不等于服务端实测；出现可达的官方 snell-server 二进制时再逐格销账 |
| O-L4-2 | VG-13 XHTTP 无门，下限 ≤25.3.6（25.3.6 对面板形 xhttp 配置 exit 0），维持观察 |
| O-L4-3 | VG-10 指针零漂移复核：`model.go:211`、`faq.md:61`、前端 `inbound-form-modal.tsx:915/:917` 与 R18 逐字同行同值；ENC 门三处 `:2007/:2032/:2118`、函数定义 `:1997/:2031/:2083`（`:2083` 为 `checkKernelVersionGates`）、`kernel_egress.go:48` 亦与 R18 逐字一致；路由 `source` 行号零漂移（R18 的 +15 漂移未延续） |
| O-L4-4 | socket 包 6 SKIP 行（4 项顶层测试 + 2 条子测试）：`TestR6B6HandleUdpPingPrivateListenerSucceeds`、`TestTCPPingQueueSkipsExpiredTargets`、`TestTCPPingExecutionHonorsCancellation`、`TestTCPPingQueueStopsWithPendingAndRunningProbes`（沙箱网络/权限前提不在场致跳过，与 R18 同名）+ `TestTCPPingQueueCompletesMoreThanEightTargets/targets_9`、`targets_30`（子测试 SKIP）：本地不可判定，不计 PASS、不计失败。socket 包本身 99 PASS / 0 FAIL（+3 为基线新增去重测试） |
| O-L4-5 | SIP023 四面（Surge/Loon/QX/URI 原样下发多段）与 144 格仍未验证（无新官方二进制）；`withSpeed=true` 测速腿未验证（无 live browser / 远端节点）——沿袭 R18 §9 O-L4-5 |
| O-L4-6 | 列表对账观察见 §7.3（L7-1…L7-3）：`NodeApiItem` latency 系字段未具名声明、`panelVersion`/`versionWarning` 发送未读——均为可读性/冗余口径，不立案；另本轮订正 R18 §7.1 的两处计数（ListNodes 27 键非 26、NodeApiItem 28 字段非 27，总可能键 32 非 31），订正只改计数、不改结论 |
| O-L4-7 | R18→R19 间 `repository.go` 零改动：`NodeTraffic` 行号本轮实测 :360，与 R18 逐字一致，无漂移 |

## §10 未验证项（单列）

| # | 内容 | 原因 |
|---|---|---|
| U-1 | 浏览器 DOM 腿（前端渲染、弹窗交互、demo 页实际展示） | 无 live browser |
| U-2 | 真 PG 方言面 | 本轮 SQLite 进程内验证；PG 契约归发版门 |
| U-3 | 真实 GitHub Gist 端到端、真机恢复后节点终态 | 无真实令牌/真机；mock 与门证据承继历轮 |
| U-4 | handler 全量重跑 | 本轮任务书未要求；handler-gate 24/24 与 kernel 全量 271/0 已覆盖本车道门面；handler 全量由 L3 车道另行交付（`r19/R19-L3-handler-full.log` 在盘，属并行车道产物、非本报告证据） |

## §11 证据清单（`~/workspace/flvx-max/audit-out-132/r19/`）

| 文件 | 内容 | 字节数 |
|---|---|---|
| `R19-L4-matrix-probe.log` | 附则 A R1–R4 + A-2 进口谓词 + 消费面探针：7/7 PASS，CELL 193 行（R1 39 + R2 23 + R3 31 + R4 主表 48 + 补格 4 + A2 26 + 消费面 22） | 16996 |
| `R19-L4-r5-probe.log` | 附则 A R5 探针：1/1 PASS，CELL 23 行 | 2805 |
| `R19-L4-kernel-full.log` | go-backend kernel 包全量 `-v`：271 PASS / 0 FAIL / 0 SKIP（基线 264 + 本轮 7 个新探针；含 ENC 99 格、A-2 枚举 61 项、F-03 修复测试） | 65517 |
| `R19-L4-handler-gate.log` | handler 门测试：24/24 PASS（ENC 版本门边界、F-08×5、F-04×1、gist×4、R17B 守卫×10、另 `-run` 命中同批 4 项） | 14517 |
| `R19-L4-repo-gate.log` | repo 门测试：1/1 PASS（F-08×1） | 1268 |
| `R19-L4-socket-tests.log` | go-gost/x socket 包全量 `-v`：99 PASS / 0 FAIL / 6 SKIP（与 R18 同 SKIP 集；+3 PASS 为基线新增去重测试） | 16268 |
| `R19-L4-binary-probe.log` | 版本门 13 行二进制/代码复证：VG-01…VG-08/VG-12/VG-13 二进制通过、VG-09 搜索 0 命中、VG-10 三方、VG-11 tag 考古、反扫无第 14 门 | 16628 |
| `R19-L4-routes-raw.csv` | 路由原料表：203 行含表头（202 条） | 25543 |
| `R19-L4-tables-raw.txt` | 数据表原料表：49 行 | 969 |
| `R19-L4-census.py` | 路由/数据表普查脚本（R18 脚本仅改读数树路径） | 8243 |
| `R19-L4-census.log` | 普查输出：路由 202、鉴权/守卫分布、数据表 49 | 462 |
| `R19-L4-binary-probe.sh` | 版本门二进制复证脚本（R18 脚本仅改读数树路径与基线号） | 6840 |
| `R19-L4-fieldcheck.py` | 列表/详情字段对账脚本 | 4456 |
| `R19-L4-fieldcheck.log` | 字段对账输出：入站 44/44、嵌套全等、节点键集与前端读取统计 | 4677 |

## 结论

附则 A 60 格 0 静默丢弃；附则 A-2 11 行全部通过；版本门 13 行 12 行复证通过（VG-09 沿袭未验证）；ENC 文法 99 格全表 PASS；路由 202 / 数据表 49 双向差集 ∅（`source` 行号零漂移；TableName 48 口径已澄清：第 49 个模型为 repo 包的 `NodeSeq`，自带 `TableName()`，无缺口）；同字段消费面全集 22 CELL 全过；列表/详情字段对账：节点 32 可能键与入站 44 键双向差集 ∅（入站嵌套 Settings/Transport/Security 亦全等），无读取无源的断链键；上轮三条观察 L7-1/L7-2/L7-3 状态均未变化（仍为观察、不立案）；R14 本车道修复复验全部维持关闭，基线新增 fix-r18-f02f04 去重测试 3/3 通过。**新立案 0 条（P0/P1/P2/P3 均为 0）。**

END
