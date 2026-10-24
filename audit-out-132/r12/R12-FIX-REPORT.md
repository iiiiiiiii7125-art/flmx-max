# R12 修复总报告（靶向轮 · 集成完成点，未发版、未打 tag、未合 main）

- **基线**：main @ `cefb566`（= integrate-r11 尖端）；最近发布 tag 仍为 `1.4.4`。
- **集成树**：`integrate-r12 @ f49dc0d`（merge: fix-r12/bT into integrate-r12）。
- **关闭数：4/4**（R12X-001…004）+ 文档批 3 项（D-01/D-02/D-03，不占产品号）。
- **合并**：批 X 链、批 T 各一次 `--no-ff` 合并，**零冲突**；批提交 4/4 祖先对账全 IN（`7225763`、`215e67a`、`e4a424d`、`2c5f4d5`），零丢提交。
- **差异**：相对 `cefb566` 共 12 文件 +362/−12（产品 7 文件 + 报告/BC 草稿 5 文件）。

## 1. 逐条关闭给由

### R12X-001（P3 · 产品，批 T，断言 T-a / T-b 分开）
- **T-a 展示三态**：`dashboard.tsx` 卡片改三态——`1→单向计费`（蓝）/`2→双向计费`（橙）/其他→「未知」（新增中性灰态，未新增计费语义）；逐值演算 `0/1/2/3 → 未知/单向/双向/未知`，与列表面 `getTunnelFlowDisplay` 分区一致。关闭证据为代码级；**真机逐值确认留待靶向门 #1 补做**（批 T 如实标未做，不冒充）。
- **T-b 联邦产出**：`CreateFederationTunnel` 的 `Flow: 0 → 1`（与计费 meta `<=0 ⇒ 1` 同值）；完备性 grep：`repository_federation.go` 内 `Flow` 字面仅此一处，联邦 handler 无另建隧道字面，全仓 `Flow: 0` 非测试命中仅此一处——**登记无第二处，同批无并入项**；先红后绿（修复前新建联邦隧道落库 `flow=0` FAIL，修复后 `flow=1` PASS）。计费面真机留待门 #2。
- **不做**：存量 `Flow:0` 行未迁移（三态下面显「未知」即诚实态）；`validateTunnelFlow` 调用点未扩展。

### R12X-002（P3 · 产品，批 X，+ X-0 二进制探针证据）
- **X-0 先行探针（施工前，对象为备份逐字恢复变体本身、未经 Normalize，N-35 合规）**：Xray 25.9.5 `run -test`——`mode: "AUTO"` **exit 23**（`unsupported mode: AUTO`）、`mode: "Packet-Up"` **exit 23**（`unsupported mode: Packet-Up`）、规范对照 `auto` exit 0。**后果等级判定：exit≠0 已证实 → 按 verdicts §0.2 写死的条件，终局候选版审计可据此将本格重定为 P2（不溯改 R12 ledger）**。探针注记：原始渲染产物含 `listen: ""` 在 25.9.5 下无论 mode 均 panic（与 mode 无关的构造 artifact），三份配置统一置 `listen=127.0.0.1:14443` 使 mode 为唯一变量；mihomo 面注明不适用（Xray 入站 JSON 非 mihomo 输入格式、本机无 mihomo 二进制；客户端面由 Clash 产物逐字证据覆盖）。
- **X-1 施工**：新增 `canonicalXHTTPMode`（TrimSpace 后小写，与 `normalizeTransport` 同合法集 `{auto, packet-up, stream-up, stream-one}`；愈合后仍域外含空值回退默认 `auto`），落点 `render_xray.go:125`、`chain.go:487`（空值语义不变）、`share.go:192` 与 `share.go:306-307`（空值省略键的既有守卫未动）。未改备份恢复逐字落库（R5X-024 受保护行为不动）、未改 `normalizeTransport` 本体；grep 确认 sing-box 自建入站不消费 `Transport.Mode`，三处即全集。
- **X-2 证据**：先红后绿（新测试施工前 FAIL、施工后 PASS，全程不调 Normalize、断言渲染不改动原对象落库值）；四面产物逐字（`AUTO`→`auto`、`Packet-Up`→`packet-up`，垃圾值 `bogus`/`packet_up`/`auto-up` 四面均回退 `auto`）；修后三份 Xray 配置 `run -test` 均为 exit 0，与 X-0 的 exit 23 形成前后对照；`TestR11B1BackupRestoreXHTTPModeGate` PASS。

### R12X-003（P3 · 产品，批 T，断言 T-c）
- `tunnelCreate` 内、默认化之前补与 `tunnelUpdate` 同形 FIX2-A 守卫：`flow` 不可解析→「flow 参数无效」、`trafficRatio` 不可解析→「trafficRatio 参数无效」，点名拒绝、不落库（仅此二字段，未扩到 `type`/`status`）；update 侧守卫未动。
- 先红后绿：修复前 `flow:"abc"` / `flow:1.5` / `trafficRatio:"abc"` 三格均 `code 0` 且各落库 1 行，修复后三格均被拒且 0 行，合法值 `flow:2, trafficRatio:1.5` 照常入库。

### R12X-004（P3 · 测试基建，批 T，断言 T-d）
- 仅 `tunnel_ip_preference_contract_test.go:48` create 负载 `"flow":99999 → "flow":1`（FIX-PLAN 点名，测试夹具改动）；`:105` update 负载未动（diff 核对）。
- 修复前三子测全部 SKIP（vacuous skip 实证），修复后 `v4/v6/empty` 三子测全部 PASS 且 `ip_preference` 落库断言实际执行。

### 文档批（不占产品号）
- **D-01**：`chain.go:300-301` 与 `share.go:505-506` 两处注释删「渲染函数全集口径」断言，改为裁定一致表述；grep 确认无残留；`doc/audit/Q-protocol-kernel-format-matrix.md:8`「导入节点全集」核对为另一语义、**未改**。
- **D-02**：`R12-BC-draft.md` 已入 git（BC-R12-01，随终局版发布）。
- **D-03**：W-3 全形措辞「`+`11 位数字（token 12 字符）拒 / `+`10 位数字（token 11 字符）放行」登记于批报告与 BC 草稿附注（标注不转发）。

## 2. 提交清单（集成树内容）

| 提交 | 内容 |
|---|---|
| `e4a424d` | 文档批 D（注释订正 + BC 草稿 + W-3 措辞纪律，零产品行为变更） |
| `215e67a` | 批 X 代码（4 文件：render_xray.go、chain.go、share.go、新测试） |
| `2c5f4d5` | 批 X 报告（含 X-0 探针定级） |
| `7225763` | 批 T 代码 + 报告（5 文件，88 增 / 4 删） |
| `f4bfd9a` | 合并批 X 链入 integrate-r12 |
| `f49dc0d` | 合并批 T 入 integrate-r12（集成尖端） |

- **分支事故两则（均已修复并验盘）**：① 文档批提交初落 `fix-r12/bX`（共享工作区被并行批切走分支），内容与父提交正确，集成时按链合并（`cefb566 → e4a424d → 215e67a → 2c5f4d5`）自然包含；② 批 T 提交初次误落 `fix-r12/bX` 尖端（`51de1b7`），批 T 代理已用 cherry-pick 在 `fix-r12/bT` 上重建（`7225763`，父级确为 `cefb566`，相对基线恰 5 文件），并将 `fix-r12/bX` 指回原尖端 `2c5f4d5`（批 X 提交未动）；集成前逐项核对：`bT` 相对 `cefb566` 恰 5 文件、`bX` 尖端恢复、`main` 仍为 `cefb566`。

## 3. 集成回归（集成树实跑）

- 后端 `go build ./...` / `go vet ./...` exit 0。
- 后端 `go test ./...` 全量：除下述两项外全部 ok（含 kernel、repo、contract 全量）。
- **首轮全量 handler 包 FAIL（证据缺口如实登记）**：首轮全量运行时 `internal/http/handler` 报 FAIL，但输出经 `tail -30` 截断，**失败用例名未留存**（集成者自身截断所致，不推诿）。处置：集成树上 handler 包连续重跑两次，均为 **ok（317.4s / 317.8s，零 FAIL 行）**；批 T 分支上 handler 包亦 ok（438.9s）。判定：首轮 FAIL 未复现、定性为偶发（全量并行负载下）而非产品回归信号；但因首轮用例名缺失，本项记为**证据缺口**，不以「产品失败 0」一句话带过——靶向门中与 handler 改动面相关的门 #4（create 守卫真机）与门 #2（联邦真机）须作为后备验证，不得省略。
- **health 环境红单列**：`TestValidateMonitorTCPTarget/公网域名放行` 一格 FAIL，签名与基线逐字一致（沙箱 fake-IP DNS 使 example.com 落保留段，`err=目标地址不允许（内网/保留地址）`），历轮定性环境红，不计产品失败。
- 前端：`tsc --noEmit` exit 0（注：本机无 pnpm，tsc 经 npx 回退执行）；`vite build` exit 0（PWA precache 65 entries）。

## 4. 未验证清单（靶向验证门 8 项预登记，逐字对象以 FIX-PLAN §2 为准）

| 门项 | 对象 |
|---|---|
| 门 #1 | L4-001 三态卡片真机：Flow∈{0,1,2,3} 四值在 dashboard 卡片面逐值显（未知/单向/双向/双向），与列表面 `getTunnelFlowDisplay` 对照一致 |
| 门 #2 | 联邦建隧道值真机：新建联邦隧道落库 Flow=1，卡片显「单向计费」，计费面按单向入账 |
| 门 #3 | L6-01 备份恢复变体渲染双二进制：备份含 `AUTO`/`Packet-Up` 的 XHTTP 入站恢复后，三面产物（Xray 链式 / ShareURI / Clash）逐字小写规范值；Xray 25.9.5 `run -test` exit 0 |
| 门 #4 | L6-03 create 数字守卫真机：create 面 `flow:"abc"` / `flow:1.5` / `trafficRatio:"abc"` 逐格点名拒绝、不落库；合法值照常入库；update 侧守卫不回归 |
| 门 #5 | L6-02 契约负载处置：`tunnel_ip_preference` create 契约测试实跑非 Skip、ip_preference 落库断言执行 PASS；`:105` update 负载未动 |
| 门 #6 | F-S01 两条计数管线 PG 契约逐字对象（ConcurrentDoubleApply + CompanionPartialUniqueIndex）实跑，禁 seq 顶替；PendingDelete `-count=10` |
| 门 #7 | 哨兵 53 行抽核：S-52 三面门 floor 逐字、S-53 六名 + 八值逐字、S-34/S-16 以门补验为准（本地不可判定项点名，不冒充 PASS） |
| 门 #8 | 升级面常规 + 验后 pristine 恢复对账（部署序列「先停 agent → 换面板 → 再启 agent」） |

另：R12X-002 的 mihomo 客户端面（XHTTP 不适用，见 X-0 注记）与 144 格客户端后果、VG-09、拨号期语义维持未验证登记。

— R12-FIX-REPORT END · 集成树 integrate-r12 @ f49dc0d；关闭 4/4；回归见 §3（含首轮 handler FAIL 证据缺口登记）· END
