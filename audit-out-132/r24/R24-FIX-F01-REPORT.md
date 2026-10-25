# R24 修复报告 F-01：XFF 信任边界不可由左侧预置移动

- 基线：main @ da43584（R24 再审计基线；1.5.0 终局版已从该基线发布）。
- 立案：R24-X-01（产品 P2），依据 `R24-FIX-PLAN.md` F-01、`R24-X-ledger.md` §三、`R24-X-verdicts.md` V-04、`R24-L3-sentinel-a.md` §3.9。
- 分支：`fix-r24-f01`，独立 worktree `~/workspace/.fixr24-f01`（`git worktree add ~/workspace/.fixr24-f01 -b fix-r24-f01 da43584`）。
- 窗口：2026-10-25T17:44–18:15Z（01:44–02:15 CST）。工具链：Go go1.25.1 linux/amd64（每次 go 调用均带 `warning: both GOPATH and GOROOT are the same directory`，为沙箱环境变量所致的良性警告）；长跑/测试 TMPDIR 指大盘。

## 一、问题与机理

`loginClientIP`（`login_rate_limit.go:88-92`）与 `federationClientIP`（`federation.go:2313-2319`）以 `idx = len(entries) - hops` 取客户端 IP，仅当 `idx < 0` 才回退 RemoteAddr。`len(entries)` 包含攻击者预置条目，故 hops 超配（大于实际可信代理数 P）且预置条目 ≥ hops−P 时，`idx` 落在攻击者可控条目上：IP 维登录限流可规避（每请求轮换预置首条目即得全新限流桶）、联邦 AllowedIPs 可欺骗。注释（`login_rate_limit.go:79`）声称“链长 < 配置跳数…改为 fail-closed”，但长度校验量的是总条目数，不是可信条目数。

定 P2 的理由（立案口径，维持）：须超配误配置方可触发（默认 hops=1 不受影响），且用户名桶 + 渐进延迟第二道与开验证码部署不受影响；误配置即可规避 IP 维限流并欺骗联邦白名单，属安全边界失效，故非 P3。

## 二、修法（两处同修 + 文档子点）

新增共享 helper `trustedXFFChainLen(entries, isTrusted)`（`login_rate_limit.go`；两处已共用 `splitXFFEntries`/`parseIPLiteral`/`TrustedProxyHops`，helper 复用与既有结构一致；两解析函数本身仍按既有注释分开实现——信任门不同）：

- XFF 由可信代理逐跳追加：每跳代理追加一个条目（它看到的上游 IP）。故从右向左连续的可信代理 IP 条目数 + 直接对端（RemoteAddr 本身就是一跳可信代理）= 实际可信代理数 P；左侧预置条目不在此列。
- 调用方以 `hops <= P` 为采信 XFF 的前提；`hops > P` 即超配，fail-closed 回 RemoteAddr 并记告警日志（含总条目数、实测链长、配置跳数），不得取到预置条目。
- 判定谓词由调用方给：浏览器侧用 `isTrustedProxyIP`（回环/私网/链路本地）；联邦侧用“落在已配置可信 CIDR 内”（`federationTrustedProxyCIDRs()` 调用提升一次、循环内复用，行为不变）。
- 既有 `idx < 0` 分支保留为第二道防线；两函数的信任门（直连忽略 XFF、联邦 CIDR 准入）、限流阈值、用户名桶、渐进延迟一字未动。

文档子点同修（`doc/guide/install.md` hops 表，失败模式表述写反）：配少（hops < P）得代理 IP（共享桶），不是被骗条目；配多（hops > P）修复后为 fail-closed 回 RemoteAddr（= 代理 IP、共享桶）+ 告警日志，不再取到攻击者可控条目。

## 三、先红后绿

红（修复前，`R24-FIX-F01-red.log`，847 字节；3 FAIL + 1 PASS，与 L3 探针日志 `R24-L3-a-probe.log` 逐字同义）：

```
--- FAIL: TestR24A_XFFOverconfiguredHopsSpoofedBucket
    over-configured hops: loginClientIP = 203.0.113.7, want 127.0.0.1 (fail-closed to RemoteAddr)
--- FAIL: TestR24A_LoginRateLimitEvadedBySeededXFF
    seeded XFF evaded the IP rate limit: 20 attempts all allowed (want 429 on 11th)
--- FAIL: TestR24A_FederationClientIPSpoofedBySeededXFF
    over-configured hops: federationClientIP = 203.0.113.7, want 10.0.0.5 (fail-closed to RemoteAddr)
--- PASS: TestR24A_XFFCorrectHopsUnaffected
```

绿（修复后，`R24-FIX-F01-green.log`，2,946 字节；8 PASS + 0 FAIL）：上述 4 项全绿，另加新增对照 2 项与修正后既存 2 项——

- 新增 `TestR24A_XFFCorrectTwoHopChainUnaffected` / `TestR24A_FederationCorrectTwoHopChainUnaffected`：正确 2 跳链（客户端公网 IP + 代理条目落在可信范围）仍按 hops 取客户端，合法多跳部署不受影响。
- 既存 `TestA001_TrustedProxyHops` / `TestA001b_FederationXFFMultiEntryFromRight`：修正后 PASS。

夹具修正 2 处（只改 setup 值，断言一字不动；留痕给由）：

1. `TestA001_TrustedProxyHops` hops=2：XFF 代理条目由公网 TEST-NET `192.0.2.1` 改为私网 `192.168.1.1`。由：该条目在夹具中扮演 Caddy（代理），而真实部署的代理在回环/私网；旧夹具用公网 IP 纯因旧实现不校验条目范围。断言（`want 198.51.100.9`）一字不动。
2. `TestA001b_FederationXFFMultiEntryFromRight` hops=2：XFF 代理条目由 `10.0.0.1`（不在已配置可信 CIDR `192.168.1.0/24` 内）改为 `192.168.1.1`。由：同上，联邦链上的代理 IP 应落在已配置可信 CIDR 内。断言（`want 1.1.1.1 (second from right)`）一字不动。

**断言更新 0 条。**全量首轮曾因此 2 处夹具红 2 项（`R24-FIX-F01-fulltest.log`，816,709 字节），夹具修正后全绿（§四）；非断言放宽、非删测试、非吞错误。

## 四、全量回归

- `go build ./...`（go-backend）exit 0；`go vet ./...` exit 0；`CGO_ENABLED=0 go build ./...`（go-gost）exit 0。
- `go test ./...`（go-backend 全量，`R24-FIX-F01-fulltest2.log`，6,389 字节）：**产品失败 0**——handler ok（576.195s）、repo ok、tests/contract ok、kernel ok、netguard ok、其余包全 ok。唯一红为 `TestValidateMonitorTCPTarget/公网域名放行`（internal/health），已知沙箱 fake-IP DNS 环境红（R21–R24 同签名；CI 真实 DNS 下绿）。
- `go test ./...`（go-gost，`R24-FIX-F01-gosttest.log`，218 字节）：exit 0，全 ok。
- 前端 `tsc --noEmit` exit 0（`vite-frontend/package.json` 与 `pnpm-lock.yaml` 在基线与本分支间无改动；`node_modules` 以软链复用主仓已装依赖，软链已删、未入提交；本修复不碰前端文件）。

## 五、真机腿（测试机 ip-172-26-4-158，锁 R24-F01，窗口 2026-10-25T17:51:46Z–18:06Z）

部署前基线（只读实查）：三容器 healthy（backend `flmx-backend:new5` / frontend `flmx-frontend:new5` / postgres:16-alpine）；容器内 paneld md5 `ae5607fc89628223e8aebf31d1532cdd`（= da43584 构建）；served index `57ab941e7fc6025b52700b2bf36f2241`；agent active（`27787cbf52e9390685e75399ff9da821`）；live 目录 `/root/flmx-panel`（未用陈旧副本 `/root/new1` 的 compose 文件）。

部署（每次操作前 `testmachine-lock.sh check R24-F01` 均通过）：本地构建 fixed paneld（`CGO_ENABLED=0 go build`，go1.25.1）md5 `d050081ec401ea9aa5c73ace33de99bb`（43,904,091 字节）；COPY-only 镜像 `flmx-backend:new6`（`366a357a278e`，GHCR 1.4.8 底座）；compose 切 new6 → backend healthy；容器内 paneld md5 `d050081e…`；GET / 6366 → 200；served index 不变。

部署插曲（已处理）：首版 `docker run --rm flmx-backend:new6 md5sum /app/paneld` 未覆 entrypoint，paneld 直接启动致 SSH 断开（exit 255）；残留容器 `laughing_lehmann` 已 `docker rm -f` 清理，改 `--entrypoint md5sum` 复核通过（`d050081e…`）；机上无残留。该容器未挂任何卷、无端口映射，未动面板数据。

腿（全部经公网 :6366 + 前端 nginx；nginx 以 `$proxy_add_x_forwarded_for` 追加 XFF，预置头可直达后端；信封 code 为准，WriteJSON 不写 HTTP 状态）：

- Leg A（首版，**作废**）：curl 每进程新建 CONNECT 致沙箱出口 IPv6 每连接轮换，11 次全 code=-1。腿设计失效，非产品问题；如实记录，改单连接复用重跑。
- Leg A-redo（hops=1 默认；单 CONNECT 隧道，`leg-r24f01-py.py`）：固定 XFF `203.0.113.7`，用户 `r24f01-lega2`，11 次：code=-1 ×10 → 第 11 次 429，**PASS**。默认行为不变。
- Leg A2：正确口令登录 code=0、token 在位。登录功能不受影响。
- Leg B（hops=2 超配；`.env` + compose 置 `FLMX_TRUSTED_PROXY_HOPS=2`，容器内 `printenv`=2、启动日志 `proxy trust: FLMX_TRUSTED_PROXY_HOPS=2`）：轮换预置 XFF 中段 `203.0.113.7, 198.51.100.<1..20>`，用户 `r24f01-legb`，20 次：code=-1 ×10 → 第 11–20 次全 429，**PASS**。修复前同型输入应全 -1（红相单元测试已证）。
- 后端日志实证：fail-closed 告警 30 行（`XFF has 3 entries but trusted proxy chain is 1 hops (FLMX_TRUSTED_PROXY_HOPS=2); ignoring XFF (fail-closed), using RemoteAddr`）——前 10 次各 2 行（限流检查与用户名信号路径各调一次 `loginClientIP`），后 10 次各 1 行（限流命中提前返回），与代码路径逐项一致。
- 一次性用户名（`r24f01-lega`/`r24f01-lega2`/`r24f01-legb`）各 ≤20 次，远低于用户名桶 30 次阈值，无收紧、无渐进延迟污染。

联邦侧真机腿未做：无第二台对等面板；`federationClientIP` 以单元测试（红 1 + 绿 2）与同一 helper 覆盖，见 §三。

恢复对账：删 hops、compose 恢复备份 → new5；paneld `ae5607fc…`、served `57ab941e…`、三容器 healthy、agent active（`27787cbf…`）、gost.json `23ca5d88…`；登录 code=0；计数 node=1 / tunnel=0 / forward=0 / inbound=1 / subscription=1（与 R24 台账 pristine 一致）；锁已放。面板已回到 da43584 基线，供 R25 审前 pin 死。

## 六、残余风险与口径

1. 窄角：真实客户端 IP 本身落在可信范围（局域网客户端）且攻击者预置同范围条目时，链长估计可能把客户端误算为代理条目——须同时满足三条件（私网客户端 + 私网预置 + 超配）；互联网攻击者（公网客户端）不受影响。
2. 行为收紧：首跳代理在公网（如 CDN 边缘在 Caddy 之前）且 hops 按全链配置时，链长估计会低估（公网代理条目不可验证），正确配置也会 fail-closed 回 RemoteAddr（共享桶，安全方向、精度降级）。默认部署（Caddy/nginx 在回环/私网）不受影响。
3. 未验证：F-01 多节点真机腿仍属未验证清单（仅 1 节点），结转 R25。

## 七、三方对账与提交

- 产品面：总账 R24-X-01（P2）↔ FIX-PLAN F-01 ↔ 本批落地，差集 ∅。**无 BC 项。断言更新 0 条**（夹具 setup 修正 2 处，断言一字不动，见 §三）。
- 改动（`git diff --stat`，另加新测试文件 `r24_a_xff_test.go` 105 行）：

```
 doc/guide/install.md                               |  7 +++--
 go-backend/internal/http/handler/federation.go     | 18 +++++++++++-
 .../internal/http/handler/federation_a001b_test.go |  5 +++-
 .../internal/http/handler/login_rate_limit.go      | 32 ++++++++++++++++++++++
 .../internal/http/handler/login_rate_limit_a001_test.go |  5 +++-
```

- 提交纪律：禁用 `git add -A`；本次只显式 add 7 个文件：上述 5 个改动文件 + 新测试文件 `go-backend/internal/http/handler/r24_a_xff_test.go` + 本报告 `audit-out-132/r24/R24-FIX-F01-REPORT.md`（提交 hash 见分支尖端，主控核验）。
- 证据日志（`*.log`）说明：仓库 `.gitignore` 的 `*.log` 规则使全部 `*.log` 不可入 git，与 R22 审计日志同口径——日志落盘在 worktree 内 `audit-out-132/r24/`（红/绿/全量 ×2/go-gost/真机腿，共 6 个），腿脚本（`leg-r24f01.sh` / `leg-r24f01-py.py` / `counts-check.py`）同目录保留，随 worktree `~/workspace/.fixr24-f01` 备查，不随提交入库；报告正文已摘录全部关键行。口令只从 `~/workspace/w10-run/panel-cred-new.txt`（600）读入内存，未写入任何日志或报告。
- 未合 main、未打 tag、未发版。分支 `fix-r24-f01` 基线 main @ da43584 未动；main 仓 HEAD 未碰。
— R24-FIX-F01-REPORT END · 自证：本行之前正文字节数 11850 · END
