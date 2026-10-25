# R24 修复集成报告（FIX-INT）

- 集成树：`~/workspace/.r14-merge`，分支 `fix-r24` @ `9576abe`（merge commit；父提交 `a90edfe` F-02 + `dc852bd` F-01；基线 main @ `da43584`，`da43584` 为祖先）。
- 回归窗口：2026-10-25 18:18–18:35 UTC（02:18–02:35 CST）。长跑 TMPDIR 指大盘（`audit-out-132/r24/tm/`）。
- 结论先行：**合并就绪**。最终基线全量重跑产品失败 0；唯一红为已知 health 沙箱假 IP 环境红；三方对账差集 ∅；无 BC 项；断言更新 0 条。

## 一、集成内容核对

- 两路报告在位（字节数+尾行 END 自证）：`R24-FIX-F01-REPORT.md` 11,930 字节；`R24-FIX-F02-REPORT.md` 3,806 字节。
- 合并结构：`9576abe` 的父提交确为 F-02 尖端 `a90edfe` 与 F-01 尖端 `dc852bd`；两路提交内容与各自报告一致；工作区干净（回归前核对 `git status` 为空；回归中产生的 `tm/go-build*` 构建缓存已清理；前端 tsc 用的 `node_modules` 软链已删）。
- F-01（产品，R24-X-01）：新增共享 helper `trustedXFFChainLen`，以“从右向左连续可信代理条目数 + 直接对端”估实际可信代理数 P；`hops <= P` 才采信 XFF，`hops > P` 即 fail-closed 回 RemoteAddr 并记告警日志；两处（`loginClientIP`、`federationClientIP`）同修；`install.md` hops 表失败模式表述随同纠正。
- F-02（测试基建，O-A-1 驱动）：日报测试 `now` 固定到本小时 30 分，消除 :59 启动跨小时 flake；产品代码零改动。

## 二、回归结论（最终基线全量重跑）

### 2.1 go-backend：build 0 / vet 0 / test exit 1（唯一红为已知环境红）

| 包 | 结论 |
|---|---|
| 16 个包（app/auth/config/client/handler/middleware/kernel/logredact/metrics/monitoring/netguard/nftables/security/repo/ws/contract） | 全部 ok |
| `internal/health` | FAIL——唯一红 `TestValidateMonitorTCPTarget/公网域名放行`（`example.com:443` 被判内网/保留地址；沙箱 fake-IP DNS 把 example.com 解析落 198.18/15 保留段；R21/R22/R23 同款签名，CI 真实 DNS 下绿） |

计数（`go test -count=1 -v ./...` 全量重跑，`R24-INT-backend-verbose.log`，1,589,739 字节）：

- 全部（含子测试）：**2,500 PASS / 2 FAIL 行 = 1 个测试**（父项 + 子项 `公网域名放行`，同一失败）。
- 顶层测试函数：1,812 PASS / 1 FAIL。
- 分包（含子测试）：handler **1,361**（R23 为 1,355，+6 = 新增 6 项 R24A 测试，逐项在位、全部 PASS，见 §2.4）、kernel 436（R23：436，未变）、repo 267（R23：267，未变）、contract 243（R23：243，未变）、netguard 8（R23：8，未变）、health 33 PASS + 1 失败。
- handler 顶层 1,033 PASS / 0 FAIL；SKIP 13（contract 9、handler 4，与既往口径一致）。

### 2.2 go-gost：build 0 / test 0

- `go build ./...` exit 0；`go test ./...` exit 0（`tests/lifecycle` ok 6.684s；根包无测试文件）。
- 注记：首轮 go-gost 曾以 exit 127 失败（`go: command not found`）——因首版并行命令中 `&&` 链被 `&` 整体后台化，父 shell 未继承 `PATH` 导出；与产品无关。改用绝对路径重跑后绿（日志 `R24-INT-gost.log`，419 字节）。

### 2.3 前端：tsc --noEmit exit 0

- F-01/F-02 均不碰前端文件，本轮为确认性重跑；`tsc --noEmit` exit 0、无输出（日志 `R24-INT-tsc.log`，0 字节）。
- 注记：本 worktree 无 `node_modules`，以软链复用 `.rel149` 已装依赖跑 tsc；软链已删，未入提交，工作区干净。

### 2.4 新增 R24A 测试逐项在位

详细跑中 6 项全部 PASS：`TestR24A_XFFOverconfiguredHopsSpoofedBucket`、`TestR24A_LoginRateLimitEvadedBySeededXFF`、`TestR24A_FederationClientIPSpoofedBySeededXFF`、`TestR24A_XFFCorrectTwoHopChainUnaffected`、`TestR24A_FederationCorrectTwoHopChainUnaffected`、`TestR24A_XFFCorrectHopsUnaffected`。

### 2.5 真机腿（F-01 lane 已做，本集成回归未重跑）

F-01 lane 真机腿 Leg A-redo（hops=1 默认，11 次：-1 ×10 → 第 11 次 429）、Leg A2（正确口令登录 code=0）、Leg B（hops=2 超配，20 次：-1 ×10 → 第 11–20 次全 429）全部 PASS，fail-closed 告警 30 行与代码路径逐项一致；恢复对账后测试机面板已回 da43584 基线（paneld `ae5607fc…`、served index `57ab941e…`），供 R25 审前 pin 死。本集成回归为沙箱全量套件 + tsc，不重复占测试机。

## 三、三方对账（总账 ↔ FIX-PLAN ↔ 落地集，含级别列与面别列）

| 面别 | 总账 | FIX-PLAN | 落地集 | 差集 |
|---|---|---|---|---|
| 产品 | R24-X-01（P2）×1 | F-01（产品） | `login_rate_limit.go` +32、`federation.go` +17/−1、`install.md` +4/−3（文档子点）、新测试 `r24_a_xff_test.go` +105、夹具 2 处 setup 修正 | ∅ |
| 测试基建 | 0 | F-02（O-A-1 驱动，单列） | `notify_r2_b10_test.go` +5/−1（只改 `now` 固定，断言一字不动） | ∅（不计入产品面） |
| 文档 | 0（install.md 表述写反为 R24-X-01 的文档子点，不单独立项） | 随 F-01 同修 | 同上 `install.md` | ∅ |
| 流程基建 | 0 | — | — | ∅ |

- 落地集实测（`git diff --numstat da43584..9576abe -- go-backend go-gost vite-frontend doc/guide/install.md`）：**7 文件 +171/−7**（`install.md` 4/3、`federation.go` 17/1、`federation_a001b_test.go` 4/1、`login_rate_limit.go` 32/0、`login_rate_limit_a001_test.go` 4/1、`notify_r2_b10_test.go` 5/1、`r24_a_xff_test.go` 105/0；新增 1 文件）。
- 断言更新 **0 条**：三处测试文件 diff 已逐行核对——`federation_a001b_test.go` 与 `login_rate_limit_a001_test.go` 只改 XFF 夹具中的代理条目 IP（改为落在可信范围的值）+ 注释；`notify_r2_b10_test.go` 只改 `now` 构造 + 注释；断言行一字未动。
- **前提订正（N-36）**：任务书称“产品 diff 7 文件 +267/−6”，与实测不符——产品面实测 +171/−7；含审计报告在内的全量 diff 为 12 文件 +475/−7。+267/−6 不匹配任何实测切分，属任务书数字误记；本报告以实测为准，7 文件数与文件清单一致。

## 四、合并就绪结论

- **fix-r24 @ 9576abe 合并就绪**：F-01/F-02 均已合入集成树；最终基线全量重跑产品失败 0；唯一红为已知 health 沙箱环境红（定性如 §2.1，CI 真实 DNS 下绿）；三方对账差集 ∅；无 BC 项；断言更新 0 条；工作区干净。
- 未合 main、未推 origin——主控验盘后自行合入。
- 残余（F-01 报告 §六沿袭，不阻塞合并）：①窄角——真实客户端 IP 本身落在可信范围且攻击者预置同范围条目时，链长估计可能把客户端误算为代理条目（须同时满足私网客户端 + 私网预置 + 超配三条件；互联网攻击者不受影响）；②行为收紧——首跳代理在公网（如 CDN 边缘在 Caddy 之前）且 hops 按全链配置时，正确配置也会 fail-closed 回 RemoteAddr（共享桶，安全方向、精度降级；默认部署不受影响）；③F-01 多节点真机腿仍属未验证清单（仅 1 节点），结转 R25。
- 下一步（按 FIX-PLAN）：修完开 R25（轮换域 A12 = B 域：订阅核心）。

## 五、证据

- 集成回归日志（`audit-out-132/r24/tm/`，`*.log` 依 `.gitignore` 不入 git）：`R24-INT-backend.log`（6,713 字节，build/vet/test 非详细跑）、`R24-INT-backend-verbose.log`（1,589,739 字节，`-count=1 -v` 全量重跑）、`R24-INT-gost.log`（419 字节）、`R24-INT-tsc.log`（0 字节，tsc 无输出 exit 0）。
- 两路报告：`R24-FIX-F01-REPORT.md`（11,930 字节）、`R24-FIX-F02-REPORT.md`（3,806 字节）；两路证据日志随各自 worktree（`~/workspace/.fixr24-f01`、`~/workspace/.fixr24-f02`）备查，不随提交入库。
- 总账与计划：`~/workspace/flvx-max/audit-out-132/r24/R24-X-ledger.md`、`R24-FIX-PLAN.md`（FIX-PLAN 自证正文 2,053 字节）。
— R24-FIX-INT-REPORT END · 自证：本行之前正文字节数 7981 · END
