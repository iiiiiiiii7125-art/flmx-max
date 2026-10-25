# R24 修复 F-02 报告：日报测试 `now` 固定到分钟 30

基线 main @ `da43584`。依据：`audit-out-132/r24/R24-FIX-PLAN.md` F-02、观察项 O-A-1。分支 `fix-r24-f02`（worktree `~/workspace/.fixr24-f02`）。**断言更新 0 条；无 BC 项；产品代码零改动。**

## 一、根因（O-A-1）

`TestR2B10DailyReportPendingVerbatimAndSnapshot`（`go-backend/internal/http/handler/notify_r2_b10_test.go:196`）用 `time.Now()` 取 `now`，并把 `now.Hour()` 存为日报小时配置；随后 `maybeDailyReport(now.Add(time.Minute))`（补发腿）与 `maybeDailyReport(now.Add(2 * time.Minute))`（同日不再重发腿）依赖 `now` 与 `now+1m/+2m` 处于同一小时。`maybeDailyReport`（`notify_reports.go:239-242`）在 `now.Hour() != h.dailyReportHour()` 时提前返回。

测试于 16:59 启动时，`now.Add(time.Minute)` = 17:00 跨小时，补发腿被提前返回吞掉，断言 `webhook daily deliveries after resend = 0, want 1` 失败。R24 审计首跑即于 16:59:15 命中此 flake（`notify_r2_b10_test.go:242`，重跑 3.30s PASS）。

## 二、修复

只改 `notify_r2_b10_test.go:200` 的 `now` 构造，固定到本小时的 30 分：

```go
// R24-FIX F-02（O-A-1）：把 now 固定到本小时的 30 分，避免 :59 启动
// 时 now.Add(1m)/Add(2m) 跨小时、maybeDailyReport 因小时不符提前
// 返回导致补发腿 flake。仅改夹具时间固定，断言一字不动。
t0 := time.Now()
now := time.Date(t0.Year(), t0.Month(), t0.Day(), t0.Hour(), 30, 0, 0, t0.Location())
```

`now`、`now+1m`（:31）、`now+2m`（:32）恒处同一小时、同一天，与墙钟分钟无关。diff：1 文件 +5/−1；断言、产品代码、其他测试均未动。

## 三、先红（复现）

临时把 `now` 钉到本小时 59 分 15 秒（与 R24 首跑 16:59:15 同构），单测确定性复现 O-A-1：

- 日志：`R24-FIX-F02-red.log`（1,431 字节）
- `notify daily_report failed: Webhook: HTTP 500`（预期注入失败，非产品问题）
- `notify_r2_b10_test.go:245: webhook daily deliveries after resend = 0, want 1`（行号 245 而非 242，因临时补丁多 3 行；失败文案逐字一致）
- `--- FAIL: TestR2B10DailyReportPendingVerbatimAndSnapshot (3.34s)`

临时补丁已 revert，未进入提交。

## 四、后绿

修复后单测 `-count=5` 全部 PASS（3.30s / 3.31s / 3.31s / 3.33s / 3.36s），日志 `R24-FIX-F02-green-single.log`（6,049 字节）。

全量回归（`go test ./... -timeout 30m`，TMPDIR 指大盘，日志 `R24-FIX-F02-fulltest.log`，6,377 字节）：

| 包 | 结论 |
|---|---|
| `internal/http/handler` | ok 647.528s（含已修日报测试） |
| `internal/store/repo` | ok 257.845s |
| `tests/contract` | ok 105.149s |
| 其余 13 个有测试的包（app/auth/config/client/middleware/kernel/logredact/metrics/monitoring/netguard/nftables/security/ws） | 全部 ok |
| `internal/health` | FAIL——唯一红为已知沙箱假 IP 环境红：`TestValidateMonitorTCPTarget/公网域名放行`（`example.com:443` 被判内网/保留地址，沙箱 DNS 把 example.com 解析落 198.18/15 保留段；R21/R22/R23 同款签名，CI 真实 DNS 下绿） |

产品失败 0。另：`go build ./...` OK；`go vet ./...` exit 0；`gofmt` 干净。

前端：本次 diff 零前端文件；前端构建在 R24 L3 对同一棵树（da43584）已验绿（exit 0、PWA precache 65、dist index md5 `57ab941e…`），本路未重跑。

测试机：本路无需测试机，未取锁。

## 五、提交

- 提交 1（修复 + 证据日志）：`fix-r24-f02` @ `208e222`
- 提交 2（本报告）：`fix-r24-f02` 分支 tip（本报告所在提交）
- `git status` 干净；未合 main、未打 tag、未发版。
— R24-FIX-F02-REPORT END · 自证：本行之前正文字节数 3727 · END
