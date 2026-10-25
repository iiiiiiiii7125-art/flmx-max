# R25 修复集成报告（F-01）

- 集成树：`~/workspace/.r14-merge`，分支 `fix-r25` @ `f20e247`（F-01 单路快进合入，父提交 main @ `ef87bc6`）
- 基线：main @ `ef87bc6`（R24 修复 fix-r24 已合入并推 origin）
- 审计对象：R25-X-01（产品 P2，节点 ServerIP 含端口后缀致默认订阅全格式拉取为空）
- 回归窗口：2026-10-26 04:05 CST 前后（日志内 UTC 戳 2026-10-25 20:05Z 起）

## 一、集成树核对

- tip `f20e247`，log 链：`f20e247`（F-01 报告）← `0fff578`（修复）← `ef87bc6`（main）。
- F-01 报告 `audit-out-132/r25/R25-FIX-F01-REPORT.md` 8,946 字节，尾行 END 自证在位。
- `git status` 干净，无未提交改动；`tm/` 证据日志依 `.gitignore` 不入 git，随工作树备查。
- 产品 diff vs `ef87bc6`：3 文件 +130/−2（`kernel_share_address.go` +21/−2；新测试 2 文件 +88/+21）；全量 diff 17 文件 +776/−2（含报告与证据日志/脚本）。

## 二、回归结论（最终基线全量重跑，TMPDIR 指大盘）

- **go-backend**：`go build` exit 0、`go vet` exit 0（GOPATH 与 GOROOT 同目录警告为已知无害噪声）。
- `go test ./... -count=1`：17 个包，16 ok，1 FAIL（`internal/health`）。唯一红为已知沙箱假 IP 环境红 `TestValidateMonitorTCPTarget/公网域名放行`（example.com 被沙箱假 DNS 解析落 198.18/15 保留段；R21–R25 同款签名，CI 真实 DNS 下绿）。**产品失败 0**。
- 计数（`-count=1 -v` 全量重跑，`R25-INT-backend-verbose.log` 1,592,211 字节）：全级 2,504 PASS / 1 FAIL（父项+子项为同一失败）；顶层 1,816 PASS / 1 FAIL；SKIP 13。
- 分包（全级）：handler **1,364**（R24 为 1,361，+3 = 新增 3 项 handler 测试，逐项在位全部 PASS）、kernel **437**（R24 为 436，+1 = 新增 kernel 锚点测试）、repo 267、contract 243、netguard 8——除 handler/kernel 外与 R24 计数逐项一致。
- 新增 4 项测试在 verbose 日志中逐项 PASS：`TestR25L3BNodeAddressAcceptsHostPort`、`TestR25ShareHostPortStrippedAtDerivation`、`TestR25InboundEndpointsHostPortServerIP`（handler）、`TestR25L3BShareHostRejectsHostPort`（kernel）。
- **go-gost**：`CGO_ENABLED=0 go build` exit 0；`go test ./...` exit 0（`tests/lifecycle` ok 8.891s）。
- **前端**：`tsc --noEmit` exit 0（确认性重跑；F-01 不碰前端文件；`node_modules` 以软链复用 `.rel149` 依赖，软链已删、未入提交）。
- 真机腿未重跑（F-01 lane 已做：红/绿双腿 + docker logs 前后对照；恢复对账后测试机面板已回 da43584 基线，供 R26 审前 pin 死）。

## 三、三方对账（总账 ↔ FIX-PLAN ↔ 落地集，差集 ∅）

| 面别 | 总账 | FIX-PLAN | 落地集 |
|---|---|---|---|
| 产品 | R25-X-01（P2）×1 | F-01 | `kernel_share_address.go` +21/−2（新增 `stripShareHostPort` helper + 两处调用点改用）；新测试 handler `r25_share_host_port_test.go` +88（3 项）+ kernel `r25_share_host_port_test.go` +21（1 项锚点）；断言更新 0 条；无 BC 项 |
| 测试基建 | 0 | — | ∅ |
| 文档 | 0 | — | ∅ |
| 流程基建 | 0 | — | ∅ |

- 落地集实测：产品代码 1 文件 +21/−2；测试 2 文件 +109；与 FIX-PLAN 逐项一致。**断言更新 0 条，无 BC 项。**

## 四、口径订正（N-36）

- FIX-PLAN 要求“四档 nodeCount ≥ 1”，与实测不符：surge 档 post-fix 仍为 `[0]`，裸拉 surge 仍为 97 字节空架，与修复前逐字节相同。
- 根因（已实查代码）：`SurgeLine`（`internal/kernel/share.go:584-586`）注释明言 “Surge has no VLESS, Reality, gRPC or XHTTP support, so those endpoints are skipped”；协议 switch 含 SS/Snell/Hysteria2/TUIC/AnyTLS/VMess/Trojan，**无 `ProtocolVLESS` case**，VLESS 端点落入 `default` → `return "", false`，按设计被跳过，与本次立案无关。
- 正确口径：**default/clash/singbox ≥ 1、surge 档对 VLESS 入站恒为 0（设计行为，非回归）**。实测证据：`R25-FIX-F01-leg-post.log` 四行（default `[1]`、clash `[1]`、singbox `[1]`、surge `[0]`）；F-01 报告 §七已按此口径登记。本报告以实测为准，不可沿用 FIX-PLAN 的四档表述。

## 五、残余风险（F-01 报告 §八沿袭，不阻塞合并）

1. `stripShareHostPort` 对 `host:badport`（非数字端口）仍取 host——与 `net.SplitHostPort` 语义一致；剥离出的 host 仍须过守卫，坏值不会下发。
2. 方向 (c) 的可见告警未做；其余被跳过情形仍仅以后端日志为信号。
3. F-01 多节点真机腿仍属未验证清单，结转 R26。

## 六、结论：合并就绪

- 回归产品失败 0；三方对账差集 ∅；断言更新 0 条；无 BC 项；口径订正已按实测写定。
- 主控验盘后可快进合入 main 并推 origin，修完开 R26（轮换域 A13 = C 域：订阅源与外部抓取）。

— R25-FIX-INT-REPORT END · 自证：本行之前正文 4930 字节
