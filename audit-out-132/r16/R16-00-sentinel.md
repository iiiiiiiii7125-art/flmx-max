# R16-00 回归哨兵抽样表（承 R15 表 53 行 S-01…S-53 逐字沿用；基线重钉为 main @ b54b44f；开工前落盘）
- 下文为 R15 哨兵表原文复件（其前言内基线 5bd5a90 为 R15 口径，行内容 S-01…S-53 沿用）；本轮 operative 基线以 `R16-00-baseline.md`（main @ b54b44f）为准。轮换全量抽审域：A2（API）。本轮无新增预登记行（R16 协调者开工前查核：本轮无新增哨兵预登记文件）。
- 下文为 R14 哨兵表原文复件（其前言内基线 7244fbb 为 R14 口径，行内容 S-01…S-53 沿用）；本轮 operative 基线以 `R15-00-baseline.md`（main @ 5bd5a90）为准。轮换全量抽审域：A5。
- 订正注记（R15 协调者，L4 指认）：复件前言多处仍写 7244fbb，属复件原文，行内容不受影响；不得据此前言行号作本轮裁定依据。
# R14-00 回归哨兵抽样表（承 R13 表 53 行逐字沿用，基线更新为 main @ 7244fbb（1.4.8 已发布，tag 1.4.8 @ bd9f99d）；本基线员第一动作落盘，先于各域开工）

- **落盘时点**：2026-10-25（Asia/Shanghai），**先于 R14 各域实质开工**（N-37 细则①③：抽样表先行、协调者收到本表落盘回执后才派各域）。本文件为基线员本轮第一动作落盘，其后才写 `R14-00-baseline.md` 与全量测试日志。
- **基线形态（任务钉死、基线员复核，详见 `R14-00-baseline.md` §1）**：内容基线 = `main` @ `7244fbb`（完整提交号 `7244fbbaa769d8262405c5a13021f415e29169a7`，「merge(fix-connidle-level): enforce per-inbound idle timeout via exit-side userLevel」，2026-10-24 17:48:28 +0000）；本地 `refs/heads/main` 与 `refs/remotes/origin/main` 均为 `7244fbb`（本员实测，`git log -1 origin/main` 同值）；只读 worktree `~/workspace/.r14-tree` @ `7244fbb`（detached HEAD）供各域读数、永不修改；共享工作树现处 `feat-latency-hint` @ `a99d68d` 且有未提交审计文件残留，不得用作读数树。树哈希：`7244fbb^{tree}` = `a39952f00929f6bc1d07c19532f86f85d98bd45c`。**最近发布 tag 为 `1.4.8`**（`bd9f99d629aff7b7ad58f03830dd0e65abf8373e`，「release(1.4.8): sync literals to 1.4.8, connIdle guide, release notes」，2026-10-24 18:36:14 +0000）；release 分支 = main + 1 提交（`git log 7244fbb..bd9f99d` 仅该 1 提交；diff 5 文件：`audit-out-132/final/RELEASE-148-NOTES.md`（新增 61 行）、`doc/ai-skill.md`、`doc/guide/nodes.md`、`skills/flmx-api/SKILL.md`、`skills/flmx-api/package.json`——版本字面量与发版说明，**产品代码差集 = 0**），故 main @ 7244fbb 的产品代码与已发布 1.4.8 同一。
- **发版节奏与收敛定位（写死）**：R13 决胜轮结论为正式收敛（`r13/R13-FINAL-summary.md` §3：R12+R13 连续两轮归零——R13 探针全集内新增产品 P0=0/P1=0 且哨兵 53 行零回退，PASS 51 / 本地不可判定承继 2（S-16、S-34）/ 回退 0；定稿 7 条 P2=1/P3=6；发布待门 + 终局候选版审计）。其后终局候选版审计执行（`final/FINAL-CANDIDATE-AUDIT.md`：R12X-002 采纳重定为 P2，历史定级修正、不溯改 R12 ledger、不回填 R13 计数），1.4.5/1.4.6/1.4.7/1.4.8 相继发布（1.4.5 tag `544dc0a`、1.4.6 `0f0c4fe`、1.4.7 `b809b37`、1.4.8 `bd9f99d`）。**R14 为 1.4.8 发布后的全面审计轮**（任务钉死），基线 main @ 7244fbb；任务未钉 R14 收敛判据，基线员不自造口径，本表只承哨兵抽样与回勾载体。
- **抽样构成**：承 R13 哨兵表 **53 行**（S-01…S-53，被测对象逐字沿用、认领分域不变、基线更新为本轮基线树 `main` @ `7244fbb`），合计 **53 行**。构成 = R8 的 51 行（S-01…S-51）+ R8 修复轮预登记增行 S-52/S-53。
- **R14 哨兵预登记查核（本轮第一动作的组成部分）**：已查 1.4.5–1.4.8 各修复批指定文件全集——`port-3342/PORT-3342-REA-UDIT.md`、`PORT-3342-TUNING-REA-UDIT.md`、`PORT-3342-PROBE-FIX-GATE2.md`（`port-3342/` 目录整目 grep 亦无命中）、`PORT-148-REA-UDIT.md`、`PORT-148-GATE.md`、`PORT-148B-GATE.md`、`final/RELEASE-146-NOTES.md`（工作树内唯一 `final/RELEASE-14*-NOTES.md` 匹配文件）、`final/RELEASE-146-GIST-DRAFT.md`，以及发布树内 `RELEASE-147-NOTES.md`（tag 1.4.7 树，`git show 1.4.7:…` 直读）与 `RELEASE-148-NOTES.md`（tag 1.4.8 树，`git show bd9f99d:…` 直读）、`r13/R13-FIX-REPORT.md` 全文与 `r13/` 目录——**无** `R14-sentinel-prereg.md` 类文件、**无** S-54 起的预登记行；全文 grep `sentinel|预登记|S-54` 仅命中 `r13/R13-FIX-REPORT.md` §5「靶向验证门 8 项预登记」（真机门项、非哨兵预登记行，不得按哨兵行定号并入，N-35）与 `final/lane-*.md` 的 R13 哨兵抽核记述（非 R14 预登记）。R13 修复轮关闭项（`R13-FIX-REPORT.md` 关闭 7/7；`R13-FINAL-summary.md` 定稿 7 条 P2=1/P3=6）亦未升哨兵行，其关闭复核由各域按对象本身复验（N-35），不得以 S-01…S-53 任一行顶替其复验。故本表**无新增行**，表尾按任务口径注记「本轮无新增预登记行」。
- **S-52 / S-53 关闭版本口径（本轮更新，核对后写实，不得虚标）**：两行原编号（R8X-001 / R8X-003）的修复代码已随 **1.4.5** 发布——核对：1.4.4 树内 ENC 门 `versionBelow3(version, 25, 9, 5)` 命中 0 处、`aes-128-gcm-siv` 命中 0 处；1.4.5 树（tag `544dc0a`，文案「release(1.4.5): version literals to 1.4.5 + release notes (R8-R13 24 BC + FINAL-P2-1; no Gist)」）内三处 ENC 门 `:1990/:2015/:2101` 与 `ssCiphersMihomoOnly` 六值 `:627` 逐字在位。关闭版本栏写实为 **1.4.5**。R13 关闭项未升哨兵行（见上条）。R12X-002 重定已由终局候选版审计采纳为 P2（`final/FINAL-CANDIDATE-AUDIT.md` :44，历史定级修正、不溯改 R12 ledger、不回填 R13 计数），本表不预判、不占行。
- **S-53 口径注记（R8 修复轮裁定，`r8/R8-FIX-REPORT.md` §5.1 为正本，沿 R13 表；本轮基线树重钉在位）**：S-53 行正本以 mihomo-only **六名**为准（`chacha20` + R8 补收 5 名），`ssCiphersXrayChain` 八值作同行**伴生格分格登记、分格销账**——两集被测对象与面别不同，不得互相顶替（N-35）。「八值分面」旧表述自裁定起以此为准、原文不回改。本轮基线树在位核对（基线员 grep 实测，`kernel/credentials.go` 自 `60a4392` 至 `7244fbb` diff = 0 行；`handler/kernel.go` 同区间 diff = 19+/2−、6 hunks 均在 :927 之上（connIdle 字段接入）、门体逐字未动、行号 +17；`kernel_egress.go` diff = 0 行）：`kernel/credentials.go:627` `ssCiphersMihomoOnly` 六名逐字 = {`chacha20`、`aes-128-gcm-siv`、`aes-256-gcm-siv`、`chacha8-ietf-poly1305`、`aes-128-ccm`、`aes-256-ccm`}；`:644` `ssCiphersXrayChain` 八值逐字 = {`2022-blake3-aes-128-gcm`、`2022-blake3-aes-256-gcm`、`2022-blake3-chacha20-poly1305`、`aes-128-gcm`、`aes-256-gcm`、`chacha20-ietf-poly1305`、`xchacha20-ietf-poly1305`、`none`}。S-52 行号同理按本轮基线树重钉（`kernelVersionGate` ENC 门 :2007 / 文案 :2008、`encDialGate` 门 :2032 / 文案 :2035、换版扫描面 ENC 格 :2118 / 文案 :2121、函数定义 `kernelVersionGate` :1997 / `encDialGate` :2031 / `checkKernelVersionGates` :2083；出站 prefer `versionBelowPadded(25, 6, 8)` `kernel_egress.go:48` 不变）。路径全称：`go-backend/internal/http/handler/kernel.go`（R13 表简写 `handler/kernel.go`，同文件）、`go-backend/internal/http/handler/kernel_egress.go`、`go-backend/internal/kernel/credentials.go`。
- **S-16 / S-34 本地不可判定项的承继形态（本轮沿用，基线员只登记形态、不代判结果）**：
  - **S-16（三类事件「恢复」通知）**：本地沙箱前提不在场——R13 形态：节点腿与延迟腿 PASS、导入腿 `TestR2B10InboundRecoveryFailureResendsOnce` 本地 SKIP（沙箱无非 loopback RFC1918 地址；R11 形态为沙箱 connect EPERM，SKIP 原因的措辞差属环境差、不是行定义差）。承继：R10 发版门 #8 测试机实跑该用例 PASS + R11 门抽验无回归信号 + R13 L7 抽核（R13-FINAL-summary §3：哨兵 PASS 51 / 本地不可判定承继 2），门内销账维持。**R14 认领形态**：I 域重跑三腿，本地仍 SKIP 的腿单列为「本地不可判定·承继门证据」，**不标 PASS、不计失败、不以同域其他探针顶替**（N-35）；只有测试机真机实跑 PASS 方可改写该腿状态。
  - **S-34（真 PG 并发契约）**：本地无 PG DSN——两条 `TestPostgresApply*ConcurrentDoubleApplyContract` 本地 SKIP（文案 `set FLMX_POSTGRES_TEST_DSN to run`），**禁以 SQLite 或 seq 契约顶替**（N-35）。承继：R10 发版门 #7 真 PG 实跑 3/3 PASS + R11 门抽验无回归信号 + R13 抽核，门内销账维持。**R14 认领形态**：F 域同上单列，不标 PASS、不计失败；真 PG 实跑方为销账证据。
  - **S-41 PG 子格（分格，不另占行）**：主断言本地可验，`TestPostgresCompanionPartialUniqueIndex` 本地 SKIP 时分格登记为本地不可判定·承继门证据（承 R10 发版门 #7 同批真 PG PASS），行级不因此降格为不可判定。
- **销账纪律**：各域认领后按「复验对象」逐字执行，销账按 **N-35** 对账探针对象本身（被测函数/端点/样本/断言三对齐，禁以同域其他探针顶替）；援引关闭登记先按 **N-36** 核前提有效性；探针类结论只认 **N-37** 落盘原始日志 + 完整名单存档（`*-probe.log` 形态随当轮报告目录存档），批报告摘要计数不得作裁定依据；探针文件带域标（细则④，如 `r14_<域>_probe_*`）、跑后即删。
- **本基线员只出表、不复验**；复验结果由认领域在各自 R14 报告中逐行回勾本表哨兵号，并填入下表最末列（本表落盘时该列**全部留空**）。

| 哨兵号 | 原编号 | 级别（面别） | 关闭版本 | 复验对象（函数/端点/探针，逐字） | 认领域 | R14 回勾状态（各域填，落盘时留空） |
|---|---|---|---|---|---|---|
| S-01 | R1 X-P1-1 | P1（产品） | 1.3.5 | `tunnelDelete`（`mutations.go`）+ `tunnel_delete_resolution.go`：离线入口节点 + 挂 gost/nft 转发删隧道，断言删除立即生效、无数据面清理硬门中止 | E | |
| S-02 | R1 X-P1-2 | P1（产品） | 1.3.5 | 分组授权撤销五路径全集（`userUpdate` 改 groupIds / `groupTunnelAssign` / `groupDelete` 等）：逐路断言组建 user_tunnel 与既有转发同步撤销、无残留 | G | |
| S-03 | R1 X-P1-3 | P1（产品） | 1.3.5 | `ensureMonitoringAccess`（`monitoring.go`）+ services create/update/delete/run 四写入口：仅查看权限普通用户断言四写全拒、读可用 | A | |
| S-04 | R1 X-P2-1 | P2（产品） | 1.3.5 | `ValidateImportHost`（`kernel/import.go`）：整数/十六进制/八进制/简写 IP + localhost 变体逐个导入，断言全部 fail-closed | C | |
| S-05 | R1 X-P2-7 | P2（产品） | 1.3.5 | `importUserSubscriptions`：不存在 user 的悬空分配，断言不落库、回执不假报 imported | H | |
| S-06 | R1 X-P2-8 | P2（产品） | 1.3.5 | `kernel_error` / `route_change` 两类通知：发送失败后断言后续补发、不永久丢失（发送成功才置位/回滚比对基准） | I | |
| S-07 | R1 X-P2-11 | P2（产品） | 1.3.5 | `install.sh` 更新判活路径：坏二进制 flap/延迟倒下桩，断言双采样判活失败且 `.bak` 回滚源不被删 | J | |
| S-08 | R1 X-P2-13 | P2（产品） | 1.3.5 | 联邦端口分配 vs 本地 `forwardCreate` 锁内复检（含 `peer_share_runtime` 预留口）：同节点同口断言只能分配一次 | L | |
| S-09 | R1 X-P2-14 | P2（产品） | 1.3.5 | 订阅重置对 SS/Snell/导入节点（共用凭据）：重置后拉取断言行为与 `subscription.md` 披露文案逐字一致（N-17/N-20 口径内） | B | |
| S-10 | R2 R2X-001 | P1（产品） | 1.3.6 | `IsSafeRemoteAddr`（`handler/security_utils.go`）：普通用户建指向 `169.254.169.254` 的转发/拨号面，断言被拒 | C | |
| S-11 | R2 R2X-002 | P1（产品） | 1.3.6 | 导入 Snell 经 sing-box 渲染（`kernel/proxy_produce.go`）：version∈{1,2,3,7,99,缺失} 断言被拒或不下发、sing-box `check` exit 0 | D | |
| S-12 | R2 R2X-005 | P2（产品） | 1.3.6 | `ValidateImportHost` 别名格：`ip6-localhost`/`ip6-loopback` 导入 + 面板探测，断言 fail-closed、不构成回环扫描预言机 | C | |
| S-13 | R2 R2X-008 | P2（产品） | 1.3.6 | 入站白名单第五批路径全集（带转发删隧道单/批、批量换隧道、删用户、节点改址）：逐路后核落地入站 allow 集与 DB 现行集逐一相等 | E | |
| S-14 | R2 R2X-011 | P2（产品） | 1.3.6 | gost seq 管线 `CheckSeq` / `advanceSeqTx`：并发同 seq 双推流量，断言只入账一次 | F | |
| S-15 | R2 R2X-014 | P2（产品） | 1.3.6 | 备份导入 `importForwards` / `importUserTunnels` 限速引用：悬空 SpeedID 断言不落库、回执不假报成功 | H | |
| S-16 | R2 R2X-017 | P2（产品） | 1.3.6 | 三类事件「恢复」通知（`notify.go` / `kernel_probe.go` / `node_latency_history.go`）：首发失败断言后续轮次补发 | I | |
| S-17 | R2 R2X-018 | P2（产品） | 1.3.6 | 通知通道级台账（`notify.go` / `notify_reports.go`）：部分通道失败断言成功通道不重复收、日报补发数值不失真 | I | |
| S-18 | R2 R2X-019 | P2（产品） | 1.3.6 | `install.sh` 安装路径：桩造服务启动失败，断言 exit 非 0（以被测函数本体、非整函数桩代替） | J | |
| S-19 | R2 R2X-020 | P2（产品） | 1.3.6 | `install.sh` systemd 启动段：flap 后回滚重启，断言先 `reset-failed`、不被启动限速拒绝 | J | |
| S-20 | R2 R2X-021 | P2（产品） | 1.3.6 | 后端结构化回执四落点（分组分配 failures / 批量升级 results / 单节点 alreadyLatest / maxConn warnings）：前端断言逐个消费呈现、非假成功 | K | |
| S-21 | R2 R2X-004 | P2（产品） | 1.3.6 | 幂等接线面：`monitorServiceCreate` / `inboundRelayCreate` / `federationShareCreate` / `remoteSourceSave` 新建分支 + 前端发键，断言同键重放不双建 | L | |
| S-22 | R3 R3X-002 | P2（产品） | 1.3.7 | 单条转发编辑切隧道：断言 `refreshInboundWhitelist` 已接 + `RemoteAddr` 重算（与批量换隧道同口径） | E | |
| S-23 | R3 R3X-003 | P2（产品） | 1.3.7 | 计费层 TrafficRatio=0：gost/nft/realm 三面各推定量流量，断言配额增量 0、forward 行 raw 保留 | F | |
| S-24 | R3 R3X-004 | P2（产品） | 1.3.7 | 存量停用分组组建授权追溯撤销：sync 与 0→0 更新两路断言存量授权被清（不只 1→0 转换沿） | G | |
| S-25 | R3 R3X-005 | P2（产品） | 1.3.7 | 备份导入写关联表三 facet（成员替换不撤销 / 占位删除孤儿化 / 停用与 grant 直写）：逐 facet 断言与运行期授权生命周期同口径执法 | H | |
| S-26 | R3 R3X-007 | P2（产品） | 1.3.7 | `tunnelUpdate` warnings 回执 × 隧道表单消费点：触发 RemoteAddr 重算告警的编辑，断言表单呈现 warning 而非无条件成功 | K | |
| S-27 | R3 R3X-008（追溯升 P1：R3X-008-T） | P2→P1（产品） | 1.3.7 | 导入侧 SS2022 密钥格式校验：坏 key 四形态逐一导入，断言逐条被拒、其余节点入库、订阅双客户端可加载 | D | |
| S-28 | R3 R3X-010 | P2（产品） | 1.3.7 | netguard Reserved/Multicast 消费面：`0.0.0.0/8` 非零、224/4、240/4、198.18/15 等地址导入 + probe，断言不被周期直拨 | C | |
| S-29 | R4 R4X-001 | P1（产品） | 1.4.1 | SS2022 cipher 名大小写变体：合法/坏密钥各一，断言合法自愈渲染、坏值整节点略过、双客户端 exit 0（凭据族） | D | |
| S-30 | R4 R4X-002（导入半边） | P1（产品） | 1.4.1 | Reality 公钥/short-id 四导入路径：逐路投垃圾 pbk/非 hex sid，断言逐条入 errs、其余节点不连坐（存量半边归 S-36，不得以本行覆盖——N-36） | B | |
| S-31 | R4 R4X-003 | P2（产品） | 1.4.1 | 删隧道 replace 迁移回滚两子面（执行失败 / 删源失败）：断言回滚后 `refreshInboundWhitelists` 已补刷、allow 与 DB 现行集相等（删除族） | E | |
| S-32 | R4 R4X-005 | P2（产品） | 1.4.1 | 双权限对共享同一 UT 的撤销顺序：creator 侧先撤、最后一组再撤，断言 UT 入候选被撤销、无永久残留放行（grant 族） | G | |
| S-33 | R4 R4X-006 | P2（产品） | 1.4.1 | 备份导入 `userTunnels` / `forwards` 两段：断言经 grant 台账执法、不复活已撤销授权（差集/pair 形态不在本行覆盖内——N-36，归 R5X-015 登记口径） | H | |
| S-34 | R4 F-S01 | P3（产品） | 1.4.1 | 真 PG 并发契约，被测对象逐字 = `ApplyInboundCounterSamples` / `ApplySubscriptionCounterSamples`：断言恰入账一次（禁以 seq 契约顶替——N-35；计数管线） | F | |
| S-35 | R4 R4X-024 | P3（产品） | 1.4.1 | `federationShareDelete` 后 allow：断言已删隧道入口地址退出（常驻用例 `TestR4S2FederationShareDeleteRefreshesWhitelist` 同口径；删除族） | L | |
| S-36 | R5 R5X-001 | P1（产品） | 1.4.2 | VMess cipher / Hy2 obfs-type 全集表逐格：垃圾值全导入路径断言被拒/略过、合法不误拒、双二进制 exit 0（凭据族） | B | |
| S-37 | R5 R5X-002 | P1（产品） | 1.4.2 | Reality 坏存量渲染守卫：直构坏 pbk/空 pbk/坏 sid 存量 + 健康节点同订阅，断言坏节点不出现、`SubscriptionNodeCount` 同口径、双客户端 exit 0（渲染守卫） | D | |
| S-38 | R5 R5X-003 | P2（产品） | 1.4.2 | 单客户端毒化四子项逐格（VLESS flow→sing-box / TUIC congestion-controller→sing-box / VLESS encryption→mihomo / Snell obfs mode→mihomo）：断言逐格略过/被拒、整表可加载 | D | |
| S-39 | R5 R5X-004 | P2（产品） | 1.4.2 | 删除族六子面：子面故障注入下断言失败回执不变且已删行拨号地址退出 allow（删除统一收尾口径） | E | |
| S-40 | R5 R5X-005 | P2（产品） | 1.4.2 | 备份导入同名组合并去重：cb=1 grant 断言来源 OR/提升保留、继任组停用时无静默残留授权（grant 族） | H | |
| S-41 | SS 线（非 R5X 编号）SS 按订阅计量 | —（功能线，产品） | 1.4.2 | companion 伴生入站全生命周期：开计量拉订阅→推非对称流量断言按订阅归因、方向按 1.3.1 口径、零串账；删订阅/改指后 companion 回收归零；PG 侧以 `TestPostgresCompanionPartialUniqueIndex` 按索引对象本身复验（N-35） | B | |
| S-42 | R6 R6X-001 | P1（产品） | 1.4.3 | SS cipher 未知方法名：`ValidSSCipher` 合法集 18 逐值 + 双拒 11 值——四导入路径逐条拒（健康不连坐）、存量双面整节点略过、计数同口径、双二进制逐值复验（以 R6 批 1 落盘逐值表 + 原始日志为对象，禁引旧摘要——N-37） | D | |
| S-43 | R6 R6X-002 | P2（产品） | 1.4.3 | `proxyClash` 复用 `ValidateSS2022Password` 逐段略过：SS2022 坏 key 存量 + 健康节点同订阅断言 mihomo exit 0、计数同口径、SIP023 合法多段逐字不变（渲染守卫/防过修） | D | |
| S-44 | R6 R6X-003 | P2（产品） | 1.4.3 | `chainTargetFromProxy` 五格坏存量（Reality/flow/encryption/SS2022 key 等）：断言面板侧被拒/报错、不产出可下发配置；健康三格 Xray exit 0 | D | |
| S-45 | R6 R6X-004 | P2（产品） | 1.4.3 | `importPermissions` grant 四格（占位 cb × 备份有效 cb）：(1,0) 格断言提升保留行 cb=1 且 warning 含合并披露、后续组撤销可达该 UT（grant/导入族） | H | |
| S-46 | R6 R6X-005 | P2（产品） | 1.4.3 | sweep 豁免逐条 `grantPairMembershipHoldsTx`：ShieldChain 例 UT 断言被裁定、合法覆盖变体存续逐字不变（grant 族） | G | |
| S-47 | R6 R6X-006 | P2（产品） | 1.4.3 | dashboard 转发卡「总流量」列（原「计费」列）：flow=1/ratio=1 与 ratio=0 两格显示核对，断言不再以 raw 合计冒充计费量（计数管线消费面） | F | |
| S-48 | R6 R6X-007 | P2（产品） | 1.4.3 | 备份导入 forwards 段 status 枚举门：status∉{0,1,2} 逐行跳过 + warning；status=2 按批 3 裁定 (b) 归一暂停的终态断言可见/可终删、不永占端口 | H | |
| S-49 | R6 R6X-014 | P3（产品） | 1.4.3 | login / sub_store 两入口停用谓词 `Status != 1`：status=2/-1 存量行断言两入口均拒绝、合法 0/1 行为逐字不变（认证谓词族） | A | |
| S-50 | R6 R6X-019 | P3（产品） | 1.4.3 | 计量开启入站改方法/安全层：断言开关块以禁用态 + 原因文案保留可关、「关计量 + 改方法」同单一次保存可达（前端回执/表单族） | K | |
| S-51 | R7 R7X-005 | P3（产品） | 1.4.4 | VLESS ENC「IX 中转专用协议」标识三面：inbound 列表三面（grid 卡片 / list 默认视图 / 移动端 sheet）ENC 标识逐面在位、且以 `inbound.tsx` 全部徽标×三面全集表为对象（不得只验 ENC 一格） | K | |
| S-52 | R8 R8X-001 | P1（产品） | 1.4.5 | ENC 版本门下限三面 + 文案：`handler/kernel.go` 三处门 floor `versionBelow3(version, 25, 9, 5)`——`kernelVersionGate`（kernel.go:2007，函数定义 :1997）/ `encDialGate`（kernel.go:2032，函数定义 :2031）/ `checkKernelVersionGates` 换版扫描面（kernel.go:2118，函数体起于 :2083）——25.8.29 节点逐面被拒、25.9.5 与 latest 逐面放行；面板 ENC 产物双面（客户端填充串 + 服务端 decryption 串）Xray 25.9.5/latest `run -test` exit 0、25.8.29 exit 23；三处文案逐字含「25.9.5 及以上」（kernel.go:2008/:2035/:2121）、无「25.6.8 及以上」ENC 门文案（25.6.8 字样仅见于出站 prefer-IP 面 kernel_egress.go:49 与历史注释/测试，非 ENC 门）；防误伤格：`handler/kernel_egress.go` 出站 prefer 的 `versionBelowPadded(version, 25, 6, 8)`（kernel_egress.go:48）逐字不变（预登记正本 = `r8/R9-sentinel-prereg.md` §1；行号按本轮基线树重钉，相对 R13 表 +17，connIdle 字段插入所致、门体逐字未动） | D | |
| S-53 | R8 R8X-003 | P2（产品） | 1.4.5 | SS 单面集（mihomo-only **六值**）分面 + 伴生格：**正本格** = `kernel/credentials.go` `ssCiphersMihomoOnly` 六名逐名 {`chacha20`、`aes-128-gcm-siv`、`aes-256-gcm-siv`、`chacha8-ietf-poly1305`、`aes-128-ccm`、`aes-256-ccm`} 经 `ValidSSCipher`/`ssCipherMihomoOnly`/`validImportedProxyFields` 四档分发逐值逐面断言（进口不拒 / Clash 面保留 / sing-box 面略过 / Xray 链式面拒 / sing-box 链式面 SKIP→报错；共同集 18 值与双拒 11 值既有处置逐格不变、防过修 faces 机器 diff=∅）；**伴生格（分格登记、分格销账）** = `ssCiphersXrayChain` 八值 {`2022-blake3-aes-128-gcm`、`2022-blake3-aes-256-gcm`、`2022-blake3-chacha20-poly1305`、`aes-128-gcm`、`aes-256-gcm`、`chacha20-ietf-poly1305`、`xchacha20-ietf-poly1305`、`none`}：子集内放行/子集外共同集 10 值点名拒（与 R7X-001 关闭断言同口径）；两格对象不同不得互替（N-35；裁定正本 = `r8/R8-FIX-REPORT.md` §5.1，预登记正本 = `r8/R9-sentinel-prereg.md` §2/§3；行号 `credentials.go:627/:644` 与 R13 表逐字同值，本轮基线树 diff = 0 行） | D（B 协查 faces/计数面） | |

## 分域分布（认领计数）

| 域 | A | B | C | D | E | F | G | H | I | J | K | L | 合计 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 行数 | 2 | 4 | 4 | 10 | 5 | 4 | 4 | 7 | 3 | 3 | 4 | 3 | **53** |

- 分轮分布：R1 = 9 行（S-01…S-09）/ R2 = 12 行（S-10…S-21）/ R3 = 7 行（S-22…S-28）/ R4 = 7 行（S-29…S-35）/ R5+SS 线 = 6 行（S-36…S-41）/ R6 = 9 行（S-42…S-50）/ R7 = 1 行（S-51）/ R8 = 2 行（S-52…S-53）/ R11 = 0 行（R11 关闭项未升哨兵行）/ R12 = 0 行（R12 关闭项未升哨兵行，其复核由各域按对象本身承担）/ R13 = 0 行（R13 关闭项未升哨兵行，其复核由各域按对象本身承担），合计 53 行。
- D 域维持 R13 表的 10 行（S-52 ENC 版本门、S-53 SS 单面集均 D 主责；S-53 的 faces/计数面由 B 协查，不另计 B 行数）。
- 高风险族分布：凭据族 ≥9 行（S-27/S-29/S-30/S-36/S-38/S-42/S-43/S-44/S-53）、计数管线 4 行（S-14/S-23/S-34/S-47，另 S-41 计量归因）、grant/授权生命周期 6 行（S-02/S-24/S-25/S-32/S-33/S-40/S-45/S-46 中取主集）、导入族 ≥7 行（S-04/S-05/S-12/S-15/S-25/S-27/S-30）、删除族/白名单收尾 6 行（S-01/S-13/S-22/S-31/S-35/S-39）、渲染守卫 5 行（S-11/S-29/S-37/S-43/S-44）。
- A 域说明：A 的 2 行认领为 S-03 与 S-49；A 域另以本轮路由鉴权全集对账为本域主业，哨兵行少不等于覆盖少——历轮产品面 P1/P2 中纯 A 域关闭项本就稀少，此为抽样事实、如实登记（承 R8/R9/R10/R11/R12/R13 表注记）。
- S-52/S-53 增行说明（承 R13 表）：两行源 R8 修复轮预登记（`r8/R9-sentinel-prereg.md`），关闭对象为 R8 总账唯二的产品面 P1/P2（R8X-001 P1 / R8X-003 P2）；R8X-002（P2）与 R8X-004/005/006（P3）及修复轮立案 R8X-003-S1（P2）未升哨兵行，其关闭复核由各域按对象本身抽验承担、不得以 S-52/S-53 顶替其复验（N-35）。**本轮更新：两行关闭版本栏由「终局版（未定）」写实为 1.4.5**（核对见表前口径条：1.4.4 树 0 命中、1.4.5 树逐字在位、tag 文案「R8-R13 24 BC」）。
- **本轮无新增预登记行**：已查 1.4.5–1.4.8 各修复批指定文件全集（`port-3342/PORT-3342-REA-UDIT.md`、`PORT-3342-TUNING-REA-UDIT.md`、`PORT-3342-PROBE-FIX-GATE2.md`、`PORT-148-REA-UDIT.md`、`PORT-148-GATE.md`、`PORT-148B-GATE.md`、`final/RELEASE-146-NOTES.md`、`final/RELEASE-147-NOTES.md`（tag 1.4.7 树）、`final/RELEASE-148-NOTES.md`（tag 1.4.8 树）、`r13/R13-FIX-REPORT.md` 全文与 `r13/` 目录）——1.4.5–1.4.8 各修复批均未预登记新哨兵行，故本表不新增 S-54 起任何行，53 行逐字承 R13 表（S-52 行号按本轮基线树重钉 +17、S-53 行号与 R13 表逐字同值；S-52/S-53 关闭版本栏写实为 1.4.5）。

— R14-00-sentinel END · 53 行（承 R13 的 53 行，本轮无新增预登记行），A–L 全覆盖，先行落盘（N-37 细则①③），回勾状态列落盘时全部留空
