# FLMX 2.1.18 复审第一轮 — R1-08b lane 裁决记录（无处置记录项补决策）

- **基线**：`fix/reaudit-1-2118 = 3ebe948a`；本分支 `fix/r1-adjudicate-record-2118`。
- **证据**：首轮主报告 `~/workspace/flmx-audit-2118/audit-out/AUDIT-REPORT-2.1.18.md`（§五、§八）；
  复审报告 `~/workspace/flmx-audit-2118/audit-out/AUDIT-REPORT-2.1.18-REAUDIT-1.md`（§二、§四 D）；
  各 lane 定稿 `~/workspace/flmx-reaudit-2118/r2-b1/REAUDIT.md`、`r2-c1/REAUDIT.md`、`r2-b3/REAUDIT.md`。
- **口径**：用户门禁（2026-11-16）——只看是否影响用户体验或功能；P3 只修零风险项，其余登记 legacy 并附理由。
- **形式**：沿用首轮主报告 §八①（仅登记）的写法：项 + 理由；修过的项给 commit + 回归测试 + 红绿证据。
- **范围**：只裁决本 lane 的 13 项（B10-01/02、B11-01/02/03/04、C2-02/03、B9-04、C1-02、B1-02），不碰其他 lane。

## 一、已修（7 项）

| 项 | commit | 回归测试 | 红证（旧代码 FAIL → 新代码 PASS） |
|---|---|---|---|
| R18-B10-01（P3）：未注册 `/api/v1/*` 回裸文本 404 | `a45ec8a7` | `TestR18B1001UnregisteredAPIPathReturnsEnvelope404`（`internal/http/router_r18_b10_01_test.go`） | 旧：`Content-Type: text/plain` + `404 page not found`；新：HTTP 404 + 信封 JSON（`code=-1, msg=接口不存在`）；未登录仍先拦为信封 401；非 `/api/` 路径仍裸文本 404 |
| R18-B10-02（P3）：公开镜像端点把上游 Go 原始错误直出 | `b2a9a026` | `TestR18B1002GithubMirrorUpstreamErrorNotLeaked`（`handler/gh_mirror_r18_b10_02_test.go`） | 旧：502 体含 `Head "http://127.0.0.1:1/…"： dial tcp … connection refused`；新：502 体只含固定文案 `GitHub 上游暂时不可达`，原始错误只记服务端日志 |
| R18-B11-01（P3）：`LOG_DIR` 是死配置 | `3c60ce2b` | `TestR18B1101LogDirDeadConfigRemoved`（`config/config_r18_b11_01_test.go`） | 旧：反射见 `Config.LogDir` 字段在位；新：字段已删，`install.md` 明言面板日志走 stdout、`LOG_DIR` 无效 |
| R18-B11-02（P3）：调度 worker 写缓存无时间戳守卫 | `33aa6fb1` | `TestR18B1102CheckerWorkerWriteDoesNotClobberNewerRunOnce`（`health/checker_r18_b11_02_test.go`） | 旧：种子未来时间戳的手动结果被调度结果覆盖回旧时间戳；新：守卫留住新时间戳，`lastRun` 只向前推进 |
| R18-B11-03（P3）：`runChecks` 轮次守卫名不副实 | `aef2f28a` | `TestR18B1103OverlappingRoundsDoNotExceedWorkerLimit`（`health/checker_r18_b11_03_test.go`） | 旧：慢检查在途时第二轮照常分发，并发拨号峰值 2；新：第二轮被 CAS 挡回，峰值恒为 1；关机取消时不等（`ctx.Done` 即返回，关机时延与修前一致） |
| R18-C2-02（P3）：`downloadSpec` default 分支死代码发单锚 `checksumUrl` | `397782bc` | `TestR18C202DownloadSpecWithoutAnchorFailsClosed`（`kernel/fix_b6_c202_xray_digest_test.go`） | 旧：无锚 `Download` 回含 `checksumUrl` 的 spec、`err=nil`；新：三例（仅 URL / 含 ChecksumURL / 含 LatestURL+LatestSum）均返回错误，不再发出 `checksumUrl` |
| R18-B1-02（P3）：Caddy 侧修法未落地（R18-R2-B1-01） | `bf6d4ebb` | `test_panel_menu5_site_block_filters_secret_query_from_access_log`（`test-install-scripts-proxy.sh`，`delete pwd` 出现 2 次） | 旧：生成 Caddyfile 中 `delete pwd` 出现 0 次；新：菜单 5/6 两处 filter 各含 `delete pwd`，出现 2 次 |

## 二、登记 legacy（4 项，§八①写法）

| 项 | 理由 |
|---|---|
| R18-B9-04（P3）：备份导出/导入无流式与内存预算：导出全量 struct + 全量 JSON 缓冲（2 倍库体积），导入 100MB 体 + 解码后结构 + 快照三重驻留 | 不修。流式重构属非零风险（导出改两段式落盘 + 体积上限 + 流式下发；导入改流式 Decoder + 预算前置；快照改落盘流式编码），按 P3 口径应暂缓，复审 R2-B3 亦明确“不主张在本轮补修”。触发面窄：需大库（数万用户/分配、数十 MB 级）才触发；触发者为管理员合法操作；失败方向多为 fail-closed（OOM 致面板进程被杀除外）。W2-05 在 `wave2-status.md` 已有一行 `Legacy: B9-04 (1 item)`，但无理由；主报告 §八①此前漏登此项，本记录即补登并附理由。后续：下个版本排期流式重构，届时按首轮建议的三点改法实施并补回归测试（超大体在解码前被拒 / 峰值 RSS 采样）。 |
| R18-C1-02（P3）：`inbound_conn_log` 无前导索引，保留期 prune 与 stale 清扫每 chunk 全表扫描——**处置口径更新** | 不修，口径更新如下。原延期理由的前提已变，不可沿用：C5-02（`7f9a9a73`）把剪枝/行数帽/清扫一律改按面板 `created_time` 执行（`connlog.go:475,492,538,585`），`InboundConnLog.CreatedTime`（`model.go:1426`）**没有任何索引**；原建议的 `start_time` 索引今后只帮 `ListNodeConnLogs` 的 `ORDER BY start_time`，已不解决 prune。未来修法：给 `created_time` 加索引（或 `(status, created_time)` 复合，一并覆盖 stale 清扫 `status = ? AND created_time < ?`），并按本仓版本化迁移口径补存量库建索引 + 回归测试（`EXPLAIN QUERY PLAN` 对 prune 子查询走 `SEARCH` 而非 `SCAN`）。暂缓理由仍成立：在大表上建索引会阻塞启动（SQLite `CREATE INDEX` 持写锁；PG 非 `CONCURRENTLY` 同理），且沙箱无 PG 可验双库迁移；性能面，无数据正确性影响。 |
| R18-C2-03（P3）：ACME DNS 凭据明文落库并随内核配置下发到节点，保存期不校验凭据范围、UI 无提示 | 不修，三面分别处置：(1) 明文落库是设计使然——凭据必须可逆下发到运行此入站的每个节点，无独立密钥管理时 at-rest 加密只是换地方放钥匙，收益为零（首轮建议原文亦明确不建议）；(2) 保存期范围探测（调 Cloudflare `GET /client/v4/user/tokens/verify`）须产品定口径：探测失败不得静默放行超范围 Key 的保存，也不得把探测失败变成新的保存阻塞点，且保存期引入外部网络依赖——未定口径前不做；(3) UI 内联提示（与 `doc/guide/nodes.md:59` 同文案）未做：属文档级提示，无可测试行为，仓内 `AGENTS.md` 明文不加前端测试，待文档批量修订时一并补，文档本身（`nodes.md:59`）已有正确指引。另澄清 ID 复用：主报告 §八②所列“C2-03 sing-box 远程 rule-set 无完整性字段”是**上一轮的旧 C2-03 豁免**，与本项 R18-C2-03（ACME 凭据）是两个不同的项；复审 R2-C1 核对的“豁免前提仍成立”（所钉 sing-box 仍为 `1.14.4-flmx1`、`RuleSetURL` 仍为固定 SagerNet 模板）针对的是旧豁免，不构成对本项的处置，本项此前实际无任何处置记录。 |
| R18-B11-04（P3）：`Checker.Stop()` 从未被调用：在途检查不参与关机等待，与 `repo.Close()` 竞态 | 不修。影响仅限关机窗口：SIGTERM 时若恰有检查在途，`InsertServiceMonitorResult` 以 `database is closed` 失败记一行日志，丢一个监控采样（内存缓存已有该结果，下轮重采）；无数据错乱、无用户可见错误。修法代价不成比例：只接线 `Stop()` 会让关机等在途检查最长 `MaxTimeoutSec+2s`（62s），超过 `main.go` 10s 关机预算；要不等须把 checker ctx 穿进 `executeCheck`/`checkTCP` 与 `nodeCommander` 接口（改多处签名 + commander 面），属调度器行为改动。P3 关机日志问题不值得。维持现状；若将来做 ctx 穿透，再接线 `Stop()`。 |
| R18-F1-02（P3）：Agent `Download.validate()` 不约束 URL scheme/host；`install()` 的 checksumUrl 腿允许 http 同源单锚（当前 catalog 无内核走此腿，属纵深缺口） | 不修，登记 legacy。面板侧永不发出 checksumUrl（R18-C2-02 已修面板侧 `downloadSpec` default 分支，见主报告 ADJ-4“并存，不同侧”裁决）；当前 catalog 无内核走 agent 侧 sumURL 腿，触发须上游或配置显式下发含 checksumUrl 的 spec。修法须收紧 agent 侧 `validate()`（约束 URL/ChecksumURL 的 scheme/host）或去掉 sumURL 腿，属 agent 下载校验行为改动，按 P3 口径暂缓。复审首轮报告称“已有处置，登记 legacy”，但主报告 §八①与本文件此前均无二者记录，本次补登。 |
| R18-F1-04（P3）：`panel_install.sh` 在 Docker 自动安装失败时向操作员回显未校验的 `curl -fsSL https://get.docker.com \| sh`，与其自身 F-050 pin 校验口径不一致 | 不修，登记 legacy。仅为失败时向操作员回显的手动安装命令，不自动执行；操作员手动执行前可核对。修法属文案/口径（改为回显带 pin 校验的安装步骤或去掉该回显），按 P3 口径暂缓。复审首轮报告称“已有处置，登记 legacy”，但主报告 §八①与本文件此前均无二者记录，本次补登。 |

## 三、记录更正：R18-B1-02 的 W2-06 legacy 记录失实

- **失实原文**（`wave2-status.md` W2-06 Legacy 行）：“B1-02 sub_store ?pwd= window (compat to 2027-01-17 Sunset; Caddy side is W2-02)”。
- **事实**（复审新发现 R18-R2-B1-01 核实）：Caddy 侧修法从未落地——菜单 5/6 两处 filter 尖端仍只 `delete secret`，全仓无 `delete pwd`；W2-02（A2-12）只给菜单 6 块补了 `delete secret`，未动菜单 5、未加 `pwd`。`GET ?pwd=` 仍进 Caddy 日志。
- **更正方式**：选 (a) 补修，不选 (b) 仅更正记录——理由：修法是两处 filter 各加一行 `delete pwd`，零行为风险（Caddy filter 语法与既有 `delete secret` 同形，老版本 Caddy 会在 validate 环节明确失败并回滚，不会静默带病生效）；且属口令明文落盘的安全面，不宜只改记录了事。补修 commit：`bf6d4ebb`；回归测试为 proxy 套件既有 `test_panel_menu5_site_block_filters_secret_query_from_access_log` 追加的 `delete pwd` 出现 2 次断言。
- **GET 窗口本身**：仍按原口径兼容至 Sunset 2027-01-17（首轮建议的提前关闭未采纳，兼容窗口不变）；到期删除 GET 分支时须同步确认 Caddy filter。
- **同步说明**：`wave2-status.md` 的 W2-06 行在协调工作区内，不属本分支；请协调者按本节同步该行文案（“Caddy side is W2-02”改为已补修 + commit）。
- **方法注记**：proxy 全套件在沙箱单跑时 F-049 先红（`PANEL_FORCE_REINSTALL=1 should allow reinstall`，未改动代码上单跑即红；套件自身注释称 CI 实测 F-049 单红、宿主网络漂移所致），且套件 `set -e` 遇首个 FAIL 即中止，故全套件跑不到菜单 5 测试；本 lane 的 B1-02 红绿证据改用单函数运行器（`grep -v '^test_[a-z0-9_]*$'` 去调用行后 source 再单调该函数，见汇报）取得，全套件其余部分未重跑。
