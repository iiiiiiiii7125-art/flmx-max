# R19 修复集成报告（fix-r19）

- 基线：main @ `d66042c`（R18 修复 fix-r18 已合入并推 origin）
- 集成树：分支 `fix-r19`（worktree `~/workspace/.fixr19`），四路零冲突合入；未合 main、未打 tag、未发版、未改版本字面量
- 合入提交：`c3f2c3e`（F-02）、`84fc757`（F-01）、`382753a`（F-03）、`e027018`（T-01+P-01）
- 集成 diff（相对 d66042c，报告与归档提交前）：55 文件 +4130/−37（产品/测试）

## 1. 各路交付核对

| 路 | 分支 @ 提交 | 内容 | 报告 |
|---|---|---|---|
| F-02（P2 优先） | fix-r19-f02 @ `47b5acb` | nil 全族守卫 + loader 滤 null + OnUpdate defer 解锁 + 启动 nil-strip 自愈；42 文件 +2828/−6 | R19-FIX-F02-REPORT.md（19,835 字节，END 在位） |
| F-01（P3） | fix-r19-f01 @ `10649ca` | 删隧道披露残留两处补披露；5 文件 +531/−27 | R19-FIX-F01-REPORT.md（13,538 字节，END 在位） |
| F-03（P3，机理先行） | fix-r19-f03 @ `13c2e49` | 机理证实：删转发从不发 DeleteCLimiters；补发 + 接线两处；4 文件 | R19-FIX-F03-REPORT.md（13,574 字节，END 在位） |
| T-01+P-01 | fix-r19-t01p01 @ `553cd80` | R1B3 补 env + 命中断言 + V-08 钉子；原子化锁脚本；4 文件 +392/−1 | R19-FIX-T01P01-REPORT.md（11,541 字节，END 在位） |

四份报告逐一验过字节数与尾行 END 自证（见上表）；四路均只 add 明确文件，未用 `git add -A`。

## 2. 计数订正与机理结论（本轮立案外的重要结论）

- **F-02 同族计数订正**：L2 的「约 49 处」实查为 **60 处运行时点**（x/api 46、x/socket 14），漏 11 处（api 批量 pause/resume/rollback 6 处、socket 5 处），已全部补守卫并在 F-02 报告 §5 逐类登记；另 loader 17 类中 16 类补干净报错（services 既有，未动）。
- **F-03 机理证实**：删转发路径确实从不清理 climiter（三线证据：代码——创建/更新只发 AddCLimiters，全部删转发漏斗只发 DeleteService，全仓除 federation peer-share 外无任何删转发代码发过 DeleteCLimiters；真机复现——forward 29 在 03:18:32 `AddCLimiters $ 5` 后 03:20:36 两次 DeleteService 删服务，终态 `rule_conn_limit_29` 仍在，R19 全部 L1/L5 日志中 DeleteCLimiters 出现 0 次；原始孤儿 `rule_conn_limit_12` 符合该机理）。另发现第二种孤儿机理 `rule_conn_limit_2`（forward 2 存活但限制已清零——改转发去掉限流同样不删 climiter，属更新路径，不在本次任务范围，未修，记入 F-03 报告 §5 后续项）。
- **F-01 另含两处同型面**（报告中已明示）：Mark 失败 `:814`（措辞与「已删」严格区分）与批量删隧道 `:2708`（与单删同病，属「修 A 漏 B」纪律内同型面）。
- **T-01P01 口径纠正**：锁文件实际位于本机共享工作区 `~/workspace/.r19-testmachine-lock`，不在测试机磁盘上；仓库内无任何锁脚本，旧机制是各 lane 手工 `echo … > 锁文件` 的文字约定。故原子化锁脚本已落到仓库内 `scripts/testmachine-lock.sh`（145 行，可执行），不只写文字纪律。

## 3. 集成全量回归（集成树，`go test ./... -count=1`，TMPDIR 指大盘）

- go-backend：`go build`/`go vet` exit 0；`go test ./...` **16 包 ok**，唯一 FAIL 为已知沙箱 fake-IP DNS 环境红——`internal/health` 的 `TestValidateMonitorTCPTarget/公网域名放行`（`monitor_ssrf_test.go:39`，`example.com` 被沙箱 DNS 解析到 198.18/15 保留段），与 R19 基线及各路基线签名一致，与本次改动无关。handler 包 ok 412.603s、repo ok 124.884s、contract ok 47.339s。**产品失败 0**。日志 `/home/hatch/tmp-r19int/backend-full.log`（68 行）。
- go-gost：`go build` exit 0；`go test ./...` lifecycle ok 21.943s，0 FAIL。
- go-gost/x：`go build`/`go vet` exit 0；`go test ./...` **27 包 ok**，0 FAIL。日志 `/home/hatch/tmp-r19int/gostx-full.log`。
- 前端：tsc `--noEmit` exit 0；vite build exit 0（本轮 vite-frontend diff 0 行；node_modules 符号链接借用共享工作树，验后已删；dist/ 未入 git）。
- gofmt：改动 Go 文件中仅 `go-backend/internal/http/handler/control_plane.go:170` 一处注释空格未对齐；核对基线 d66042c 同文件亦未对齐（既存 gofmt 版本漂移，F-03 改动在 :697 与 :2238–2260，不在该行），按 R16 先例未重排。
- 版本字面量：`skills/`、`doc/ai-skill.md`、`**/package.json` 相对 d66042c diff 为空。

## 4. 流程 §P-01：测试机互斥锁纪律（R20 审计亦按此执行）

锁脚本 `scripts/testmachine-lock.sh`（锁本体为目录 `$HOME/workspace/.testmachine-lock.d/`，`mkdir` 单次原子抢占，内含 `owner` 文件 `LANE ISO8601-UTC`），14 项沙箱实测全过（他 lane 抢锁 DENIED、错 lane check/release 拒绝、`--wait` 超时拒绝、并发抢锁恰一人胜出、break 无原因拒绝、有原因成功并打印原持有者；自测中暴露并修复一个真 bug：父目录不存在时 `mkdir` 失败，已补 `mkdir -p` 父目录，原子步骤仍是单次 `mkdir` 锁目录）。

纪律全文（6 条，取自 T-01P01 报告）：
1. 抢锁用脚本 `acquire [--wait]`，失败打印持有者并退出 1，绝不覆盖；
2. 每次重启/部署前 `check` 复核锁归属（锁内 lane 名须为本 lane）；
3. 窗口起止与 lane 归属写入各路报告首行；
4. 结束即 `release`（仅 owner 可放）；
5. 僵死锁仅协调者 `break --reason` 并抄入报告；
6. 违反者该窗口证据作废重跑。

旧轮次锁文件（`~/workspace/.r19-testmachine-lock`）作废。

## 5. 口径：数据表数 49 → 50

standing 数据表数自 R19 起为 **50**（不含 `sqlite_sequence`）；多出的一张是 `node_traffic`，为 d66042c 产品迁移表 + 历史回填（11 小时桶），非测试残留（`repository.go` 在位，L1/L5 两次普查均为 50）。L4 普查方法 R20 起纳入 `node_traffic`。节点列表键数订正为 27/28/32（R19-OBS-03），结论不变。

## 6. 证据缺口与说明（不冒充）

- **F-03 handler 全量日志未完整留存**：F-03 报告所称 `~/workspace/.test-tmp-r19f03/handler-full.log` 实为 167 字节尾行 stub（仅 warning 行 + `ok handler 649.426s`），完整日志未落盘；该腿结论由本集成报告 §3 的 handler ok 412.603s 独立重跑佐证，不依赖该 stub。已删除误导性复制件，未归档。
- **F-01 基线全量受 /tmp 满污染**：基线全量进行时 /tmp（512M tmpfs）被并发 go-build 目录占满 96%，致 `TestR2B10…` 与 contract 7 项 `database or disk is full (13)`；修后改走 `TMPDIR/GOTMPDIR=/home/hatch/tmp-r19int` 后全部转绿；报告中已按实际签名拆分说明，未把污染数当产品基线。
- **R19-OBS-04 注记**：F-01 报告所称红态 `6 FAIL / 1 PASS` 在树内无红日志、按字面不可复现（纯 a0c18a0 产品文件 + 新测试无法编译，`undefined: formatDeletedForwardsSummary`）；R18-X-01 关闭不依赖该数字（依据为本轮绿测 + 7/2 红重构 + 真机腿）。证据级注记，不立案、不阻塞关闭（同 R18 V-06 口径）。
- **未验证项（结转 R20）**：真机验证一律未做（本次修复全部沙箱证据，未上 Lightsail 测试机、无 R19 独占窗口；F-02 报告 §6：面板在线下发遇 nil、SIGHUP 重载毒化文件、生产 gost.json 含 null 的启动均未在真机复核）；F-03 真机端到端（面板删转发 → 测试机 gost.json）须 R20 在测试机上跑，存量孤儿 `rule_conn_limit_12`/`rule_conn_limit_2` 未清理；SS 直连腿方法失效、远端/非 Debian 内核、浏览器 DOM 腿、kill-freeze K2、VLESS 1MB 10.21s 观察继续结转。

## 7. 归档

随本次提交归档进库（显式 `git add` 明确文件 + `git add -f` 逐文件显式入库 `*.log`，未用 `git add -A`）：
- R19 审计产物 115 文件（主检出 `audit-out-132/r19/` 未入库部分：R19-00-*、R19-FINAL-*、R19-FIX-PLAN.md、R19-L*、R19-X-*）
- F-01 9 份原始日志 `r19f01-*.log`、F-02 13 份红绿日志（已随 F-02 分支入库）
- 四份修复报告 + 本集成报告

— R19-FIX-INT-REPORT END · 自证：本行之前正文字节数 8204 · END
