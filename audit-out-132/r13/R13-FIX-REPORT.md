# R13 修复集成报告（收官：7/7 关闭）

- 基线：`main @ 60a4392`（integrate-r12 树，产品内容 `f49dc0d`；最近发布 tag 1.4.4）。
- 集成树：`integrate-r13`（本报告提交后 tip 见尾行；工作区 `~/workspace/.r13int`）。
- 依据：`R13-X-ledger.md`（7 条：产品 P2=1/P3=6）、`R13-X-verdicts.md`、`R13-FIX-PLAN.md`（批 T2 + 批 R + 文档批、BC-R13-01/02、靶向验证门 8 项）。
- 状态总述：**7/7 关闭**；R13X-001（P2）为发布阻断项，本地级关闭、真机终态移交靶向验证门 #1（见 §5）；未发版、未打 tag、未合 main。

## §1 集成对账

- 合并顺序：`fix-r13/bR` → `fix-r13/bT2` → `fix-r13/bDoc`，**零冲突**。
- 合并提交：`8e055c4`（bR）、`099c318`（bT2）；bDoc 单独合显示 Already up to date——因 bT2 分支切自含 bDoc 提交的检出，其历史已含 `9571433`，内容无丢失（见 §4 登记）。
- 批提交祖先对账：`1efe62a`（bR 产品）、`4bc17f0`（bR 报告）、`cad99e4`（bT2 产品）、`65ab5df`（bT2 报告）、`9571433`（bDoc）——`git merge-base --is-ancestor` 5/5 IN，零丢提交。
- 相对基线 diff：23 文件 +1449/−56。
- 纪律：各批均禁 `git add -A`（仅明确文件）；集成未改任务书口径、未增删条目。

## §2 逐条关闭（7 关闭 / 0 阻塞）

| 编号 | 级别 | 批 | 断言 | 关闭给由（摘要，详见批报告） |
|---|---|---|---|---|
| R13X-001 | P2 | 批 R | R-a | backupImport 成功后、撤销清理之后同步补推：先按正常写路径口径重算中转 RemoteAddr（隧道侧 + 入站侧），再整节点复用 redeployLocalNodeRuntime（onNodeOnline 同款，nft 经 syncForwardServices 内 syncNftablesNode 口径覆盖）；仅 ws live 连接节点执行，离线节点留给未改动的重连路径；失败节点进回执 repushFailedNodeIds + warnings，不改写导入成功结论；触发门以实落计数把关（纯内核、纯订阅导入不触发）；未改 agent 协议、未新增 DB schema。本地级关闭：mock-agent 探针证明命令下发与调用点在位，真机终态移交门 #1 |
| R13X-002 | P3 | 批 T2 | T2-a | tunnelCreate 守卫由两键扩为与 tunnelUpdate 同形的四键（type/flow/status + trafficRatio）；type:"abc"、status:"abc"、type:1.5 均点名拒绝、不落库；对照组 flow:"abc" 仍被拒（R12X-003 在位）；合法整数建库逐字不变；status 数值域外枚举门（O-3）按边界未补 |
| R13X-003 | P3 | 批 T2 | T2-b | 面板编辑表单按方向 ① 省略：仅编辑模式、值与存量原值相等（未改选）且原值域外时删键提交，后端 presence 口径回填存量；显式改选恒全量提交；校验侧对被省略 ratio 做占位豁免（占位值不进负载）；表单形态探针仍拒、省略形态探针（既有 R11B4 存量保留测试）通过、合法行不变；构建产物核对到 `delete re.flow / re.trafficRatio` 且仅作用于编辑路径；R11X-009 断言本身未动 |
| R13X-004 | P3 | 批 R | R-b | ImportResult 新增 7 项与既有 *Imported 同形字段（kernels/relays/counters/subscriptions/subscriptionCounters/sources/legacy users），只计实落行，跳过行仍走 SkippedRows |
| R13X-005 | P3 | 批 R | R-c | ExportPartial/Import 双侧 validBackupTypes（16 值）校验，文案点名未知值；前端 config.tsx 16 值清单与后端集合逐值比对，集合与顺序均一致 |
| R13X-006 | P3 | 批 R | R-d | 仅 KernelID≤0 的导入型行以 kernel.ProxyProtocol(Extra) 重推导 Protocol（与创建/编辑路径同函数）；Extra 逐字落库，自建行原样。注记：任务书「row.Protocol 为 ss」即该口径下的面板协议名 "shadowsocks"，测试按此断言（批报告已注记映射） |
| R13X-007 | P3 | 批 T2 | T2-c | importTunnels 门前置于链路预删之前，Flow=99999、TrafficRatio=-5、TrafficRatio=200、Flow=0 四行均跳过且回执逐行点名，SkippedRows["tunnels"]=5（含同 id 存量行），合法行逐字落库、被跳过行的存量链路不被预删 |

批报告（均在 `audit-out-132/r13/`，字节数 + 尾行自验）：
- 批 R：`R13-FIX-BR-report.md` 12,879 字节；批 T2：`R13-FIX-BT2-report.md` 11,786 字节；文档批：`R13-FIX-BDOC-report.md` 4,106 字节。
- 文档批交付：`R13-R12X002-redetermination-draft.md`（4,896 字节，R12X-002 重定记录预备文本，标注草稿、重定权归终局候选版审计）+ `R13-BC-draft.md`（4,948 字节，BC-R13-01/02 草稿，均标注随终局版发布）。

## §3 集成回归（集成树实跑，日志 `~/workspace/.r13int-full.log`）

- `go build ./...` exit 0；`go vet ./...` exit 0。
- `go test ./...` 全量：16 包 ok（含 handler 342.093s、repo 101.716s、tests/contract 46.141s）；**产品失败 0**。
- 唯一 FAIL 包：`internal/health` 仅 `TestValidateMonitorTCPTarget/公网域名放行` 一格（`example.com:443` 在沙箱 DNS 下解析到保留地址），与基线同签名、属历轮定性的环境红，单列不计产品失败。
- 前端：`tsc --noEmit` exit 0；`vite build` exit 0（dist/sw.js 等产物正常生成；node_modules 以 gitignored 符号链接接通，未入版本库）。

## §4 集成登记（诚实项，结论不变）

1. **T2-c 非字面函数复用**：FIX-PLAN 写「恢复面复用保存面两门」，施工因包循环（handler 依赖 repo，repo 引用 handler 的校验函数将成环）改为同合法集本地门（`validateImportedTunnelFlow/TrafficRatio` 镜像实现），注释已写明漂移即失效。断言（四行跳过 + 点名）全过，关闭对象是断言本身；若终局候选版审计坚持字面复用，须另立重构断言，不得以本批关闭冒充字面复用。
2. **bT2 分支历史含 bDoc 提交**：bT2 切自含 bDoc 提交的检出，故其历史含 `9571433`；集成时 bDoc 单独合为 Already up to date，文档内容已随 bT2 合入，无丢失、无重复提交。
3. **批施工期共享检出干扰**：批 R 与批 T2 并行施工期共享工作树一度互见对方在途改动（bR 报告：主树出现 T2-c 门所致的 8 项失败；bT2 报告：批 R 文件一度进入不可构建中间态）。两批均以隔离方式自证干净（bR 隔离 worktree repo/handler 全量 PASS；bT2 补丁过滤只暂存己方 hunk）。集成在全新 worktree `~/workspace/.r13int` 内完成并重跑全量回归，上述干扰不带入集成结论。
4. **批 T2 夹具归一**：首轮全量暴露 9 项既有失败（handler 1 + repo 8），根因为既有备份夹具隧道 flow 为 R11 前占位值（1000/99999/缺省 0）而与用例目的无关；已将这些夹具归一为 `Flow: 1`（不改产品门与用例断言），靶向重跑全绿后再全量通过。共享种子 helper 未动。
5. **断言更新全集**：无裁定点名更新（各批 FIX-PLAN 断言逐字执行；夹具归一与 setup 值调整均非断言改动）。

## §5 靶向验证门 8 项预登记（逐字对象以 R13-FIX-PLAN §4 为准）

1. 门 #1（R-a 真机）：面板 + 在线 agent 节点：导入含 forwards 的备份后**不重连**，核节点 gost 服务 / nft 规则终态与 DB 一致；同 id 改参旧服务被替换；中转 RemoteAddr 重算；离线节点重连后终态一致。
2. 门 #2：T2-a / T2-c——create 守卫四键真机拒格 + 恢复面域外行跳过点名（DB 核对）。
3. 门 #3：T2-b——面板编辑表单：存量域外行无关编辑成功、存量值保留；显式改选仍提交新值。
4. 门 #4：R-b / R-c / R-d——inbounds 子段计数逐段核对；未知类型双侧报错点名；Protocol 重推导行展示与筛选一致。
5. 门 #5：F-S01 契约顺跑——两条计数管线逐字对象 + companion 索引，**禁 seq 顶替**；PendingDelete `-count=10`。
6. 门 #6：哨兵抽核——53 行抽核，重点 S-16/S-34（本地不可判定项以门内真机/真 PG 实跑改写状态，不得以 SQLite/seq 顶替）、S-52/S-53。
7. 门 #7：变更面回归——受影响包全量回归产品失败 0；升级面常规；验后 pristine 恢复对账。
8. 门 #8：144 格 / 墓碑沿袭——无新增可验官方二进制则维持未验证；墓碑三触发逐条对账。

## §6 未验证清单（除门 8 项外）

- R13X-001 的真机终态（归门 #1，不以本地 SQLite + mock-agent 探针顶替）。
- T2-b 面板实点（归门 #3；本地证据为 SQLite 探针 + 构建产物）。
- T2-c 字面复用问题（见 §4 第 1 项，归终局候选版审计裁量）。
- R12X-002 重定本身（归终局候选版审计；本文档批只准备了记录文本）。

— R13-FIX-REPORT END · 集成树 integrate-r13（tip 见提交）· 关闭 7/7 · 产品回归失败 0 · END
