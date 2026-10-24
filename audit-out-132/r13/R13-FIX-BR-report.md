# R13 修复批 R · 批报告（备份恢复面）

- 批归属：批 R（R13X-001、R13X-004、R13X-005、R13X-006）
- 基线：`integrate-r13` 切出分支 `fix-r13/bR`（基线 main @ `60a4392`，最近发布 tag 1.4.4）
- 依据：`R13-X-ledger.md`、`R13-X-verdicts.md` §0/§3、`R13-FIX-PLAN.md` §3（断言逐字执行，无点名更新）
- 施工顺序：R-a（发布阻断项）最先施工；R-b/R-c/R-d 随后；完工提交到 `fix-r13/bR`，**不合入 `integrate-r13`**（协调者统一集成）
- 纪律：禁 `git add -A`（只 add 明确文件）；每修复先红后绿回归测试；修后 go build/vet + 受影响包测试；Go 工具链 `export PATH=$HOME/go/bin:$PATH`；长测试 TMPDIR 指大盘 `~/workspace/.tmp-big`；未改动 agent 协议、未新增 DB schema；未触碰有意设计护栏；未动兄弟批（批 T2）文件

## 1. 关闭结论总表

| 断言 | 条目 | 级别 | 结论 |
|---|---|---|---|
| R-a | R13X-001 | P2（发布阻断项） | 关闭（本地探针级；真机门移交靶向验证门 #1） |
| R-b | R13X-004 | P3 | 关闭 |
| R-c | R13X-005 | P3 | 关闭 |
| R-d | R13X-006 | P3 | 关闭 |

关闭数：**4 / 4**（R-a 的真机终态结论不在本地关闭范围内，见 §5 未验证清单）。

## 2. R-a（R13X-001，P2）—— 备份导入后在线节点转发重推

### 施工

- 新增 `go-backend/internal/http/handler/backup_import_repush.go`：
  - `backupImportNeedsForwardRepush`：触发条件 = 任一实落计数 > 0（forwards / tunnels / nodes / inbounds 行 / inbounds relays）。纯内核、纯订阅、纯来源的 inbounds 导入不触发；relays-only 导入单独计入触发（否则 inbounds 行数为 0 会漏触发）。
  - `repushForwardRuntimeAfterImport`：受影响节点集 = `repo.ListForwardNodeIDs()`（forward_port 去重节点集；无转发端口的节点本就无 gost/nft 转发态可推）；仅 ws live 连接节点执行（`wsServer == nil` 视为无 live 连接，全跳过）；每节点先中转 RemoteAddr 重算（隧道侧 `resyncManagedRelayRemoteAddrsForTunnel` 按节点 active 隧道、入站侧 `resyncManagedRelayRemoteAddrsForInbound` 按备份内入站行，均为正常写路径口径），再整节点 `redeployLocalNodeRuntime`（onNodeOnline 同款：逐隧道 `redeployTunnelAndForwards` + 逐转发 `syncForwardServices`，nft 模式转发经 `syncForwardServices` 内 `syncNftablesNode` 口径覆盖）。
  - 同步执行、失败不静默：失败节点进回执 `repushFailedNodeIds` + `importWarnings`（失败不改写导入成功结论；失败节点重连时经 `onNodeOnline` 全量重布再次收敛）。
- `handler.go` 的 `backupImport`：在撤销清理（R3X-005）之后、回执发出之前调用重推，使重推下发的是导入终态（已清理的撤销转发不再被重推）。
- `model.ImportResult` 新增 `repushNodeIds` / `repushFailedNodeIds`（omitempty，旧客户端忽略）。
- `repo.ListForwardNodeIDs()`（`repository_control.go`）：`forward_port.node_id` 去重清单。
- 未改动：agent 协议、DB schema、`onNodeOnline` / `redeployLocalNodeRuntime` 本体（复用，未改实现）。

### 断言逐条结果

| FIX-PLAN 断言 | 结果 |
|---|---|
| 导入含 forwards 的备份后，在线节点（不重连条件）gost 服务与 nft 规则终态与 DB 一致 | 本地：mock-agent 场景证明在线入口节点收到带新参数的 `UpdateService`（见测试）；真机 gost/nft 终态结论移交靶向验证门 #1（本地探针不能顶替，见 §5） |
| 同 id 改参/改端口的旧服务被替换（不按旧参数服役） | 本地通过：改端口导入后入口节点收到同一转发的新端口 `UpdateService`（UpdateService 按服务名替换，为正常写路径同款语义）；agent 侧服务替换终态移交门 #1 |
| 中转 RemoteAddr 在 nodes/tunnels 段部分导入后重算 | 通过：nodes 导入改落地节点地址后，托管中转转发行的 DB RemoteAddr 重算为新地址，且新地址下发到在线入口节点 |
| 离线节点重连路径行为不变 | 通过：无 live 连接时重推全跳过（attempted/failed 均空）；`onNodeOnline` 代码零改动 |

### 先红后绿

- `TestR13BRBackupImportForwardsRepushesOnlineNode`：施工前超时失败（10s 内无带 20001 的 UpdateService）；施工后通过（0.26s），回执 `repushNodeIds` 含入口节点、`repushFailedNodeIds` 为空。
- `TestR13BRBackupImportNodesResyncsRelayRemoteAddr`：施工前 RemoteAddr 仍为旧地址而失败；施工后通过（DB RemoteAddr = 新地址 + 入口节点收到新地址 UpdateService）。
- `TestR13BRRepushSkipsWhenNoLiveAgent` / `TestR13BRRepushTrigger`：离线跳过与触发条件表。

### 设计说明（协调者注意）

- 受影响节点集取「全部转发服务节点」而非仅备份内转发所指节点：节点粒度与 `redeployLocalNodeRuntime` 自身的粒度一致（该函数本就是整节点重布）；导入是低频管理操作，幂等重推的代价可接受。触发门以实落计数把关，避免无关导入（如纯订阅）触发重推。
- 重推放在撤销清理之后：若放在清理之前，被撤销的转发会被重推一次再清理，终态虽一致但多一次无效下发。

## 3. R-b（R13X-004，P3）—— inbounds 子段导入计数

### 施工

- `model.ImportResult` 新增 7 个与既有 `*Imported` 同形字段（无 omitempty）：`inboundKernelsImported`、`inboundRelaysImported`、`inboundCountersImported`、`inboundSubscriptionsImported`、`inboundSubscriptionCountersImported`、`inboundSourcesImported`、`inboundLegacyUsersImported`。
- `repository_inbound_backup.go`：`importInbounds` 改为返回 `inboundImportCounts`（kernels / inbounds / relays / counters / subscriptions / subscriptionCounter / sources / legacyUsers）；各子段成功落库处计数，跳过行不计（走既有 `SkippedRows`，与 `InboundsImported` 口径一致）；订阅段按循环索引区分原订阅与 legacy 用户（legacy 用户由 `b.Users` 转换追加，顺序在后），其余行为（token 匹配、subRemap、空 token 跳过）零改动。
- `repository.go` 的 `Import`：把 7 项计数填入回执。

### 断言逐条结果

| FIX-PLAN 断言 | 结果 |
|---|---|
| 导入 2 内核 + 2 入站 + 1 中转 + 1 入站计数基线 + 2 订阅 + 1 订阅计数基线后，回执逐段计数与 DB 落库数一致 | 通过（另含 1 来源 + 1 legacy 用户，计数亦一致；新库种子订阅 1 条已在 DB 核对时按 token 排除） |
| 旧客户端忽略新增字段 | 通过：新增字段为纯加法 JSON 字段，无既有字段改动 |

## 4. R-c（R13X-005，P3）—— 未知类型即报错

### 施工

- `repository.go` 新增 `validBackupTypes`（16 值）+ `validateBackupTypes`：`ExportPartial` 入口与 `Import`（版本校验之后）双侧校验；文案点名每一个未知值（`未知备份类型："bogus_type"` 形）。
- handler 层缺口随之堵上：handler 本就把 `ExportPartial` / `Import` 的错误原样回给调用方（导出 code -2、导入 `导入失败：…`），无需 handler 层另加门（「全是未知类型」同样被 repo 层拦下）。

### 断言逐条结果

| FIX-PLAN 断言 | 结果 |
|---|---|
| `ExportPartial(["users","bogus_type","inbound"])` 报错点名 `bogus_type` | 通过 |
| `Import(backup, ["bogus_only"])` 报错点名 | 通过 |
| 合法类型清单行为不变 | 通过（16 值全量导出/导入空备份均成功） |
| 前端 16 值清单与后端集合一致性复核 | 通过：逐值比对，前端 `config.tsx` 的 `BACKUP_TYPE_OPTIONS`（16 值）与后端 `validBackupTypes` 集合与顺序均一致 |

## 5. R-d（R13X-006，P3）—— 导入型入站 Protocol 重推导

### 施工

- `repository_inbound_backup.go` 入站落库前：仅当 `KernelID <= 0`（导入型行）且 Extra 可解析为代理对象且 `kernel.ProxyProtocol(p)` 非空时，以推导值覆写 `Protocol`；Extra 逐字落库不变；`KernelID > 0` 的自建行原样落库。
- 口径与创建路径 `externalRow`、编辑路径 `row.Protocol = kernel.ProxyProtocol(proxy)` 相同函数；整行跳过备选不采。

### 断言逐条结果

| FIX-PLAN 断言 | 结果 |
|---|---|
| `Protocol="trojan"` + ss Extra 的行导入后 row.Protocol 为 ss | 通过（任务书「为 ss」即面板协议名：`ProxyProtocol` 的 importTypes 口径下 ss → `"shadowsocks"`，与创建路径落库值同值；测试断言 `"shadowsocks"` 并注记此映射） |
| 数据面（订阅端点、链式目标）行为不变 | 通过（Extra 逐字落库；订阅端点与链式目标均经 `importedProxy` 由 Extra 构造，与 row.Protocol 无关——此为既有架构事实，本地以 Extra 逐字性验证） |
| 失配行不再逐字落库错误标签 | 通过（同上，行 Protocol 已纠正） |
| 整行跳过备选不采 | 执行：未采用（行照常落库，仅派生字段纠正） |

## 6. 回归结论

- `go build ./...` + `go vet`（handler / repo / model）：通过。
- 本批测试：repo 层 `TestR13BR*`（3 项）全过；handler 层 `TestR13BR*`（4 项）全过；每项均先红后绿（R-b 为字段缺失编译红；R-c/R-d 为行为红；R-a 为 mock-agent 命令缺失/地址未变红）。
- 隔离回归（关键结论）：主工作树混有兄弟批 T2 的在途改动（T2-c 的 `importTunnels` 新门已 stage 进 `repository.go`），主树 repo 全量出现 8 项隧道/备份面失败。经核查，8 项失败的根因均为 T2-c 门对旧 fixture 行为的改变（例：`TestR2B8ImportTunnelTrafficRatioZero` 期望 ratio 0 行存活，现被 T2-c 门跳过），与本批零交集。为拿本批干净结论，另建隔离 worktree（基线 = 本分支 HEAD，仅贴本批 8 个文件/hunks）重跑全量：
  - `go test ./internal/store/repo/ -count=1`：**PASS（97s）**
  - `go test ./internal/http/handler/ -count=1`：**PASS（384s）**
  - 故本批受影响包全量回归产品失败 0；主树 8 败归属批 T2，移交协调者（本批提交不含 T2 的任何 hunk，见 §7）。
- 未跑：真机门 #1/#4（见 §8）、PG 方言面（沿历轮归发版门）。

## 7. 提交与文件清单（只 add 明确文件）

- 分支：`fix-r13/bR`（从 `integrate-r13` 切出；**未合入 `integrate-r13`**）
- 产品提交：`1efe62a`（8 文件，+704/-37）；本报告为随后的报告提交，同分支追加
- 注意：共享检出在施工中途被兄弟批切到 `fix-r13/bT2` 且混入其在途改动；为防误提交他人 hunk，本批改动经独立 worktree（基线 = 本分支 HEAD `60a4392`）核对后提交，`repository.go` 仅含本批 3 个 hunks、经 grep 确认无 T2 门 hunk；主检出内的 T2 改动一概未碰、未提交
- 新增：
  - `go-backend/internal/http/handler/backup_import_repush.go`
  - `go-backend/internal/http/handler/backup_r13_br_test.go`
  - `go-backend/internal/store/repo/backup_r13_br_test.go`
- 修改：
  - `go-backend/internal/http/handler/handler.go`（backupImport 尾钩子接重推）
  - `go-backend/internal/store/model/model.go`（ImportResult：7 项子段计数 + 2 项重推字段）
  - `go-backend/internal/store/repo/repository.go`（validBackupTypes + 双侧校验 + inbounds 计数回填）
  - `go-backend/internal/store/repo/repository_inbound_backup.go`（计数结构 + Protocol 重推导）
  - `go-backend/internal/store/repo/repository_control.go`（ListForwardNodeIDs）
- 未动：兄弟批 T2 文件（`mutations.go`、`tunnel.tsx` 等工作树内他人改动一概未碰、未提交）；agent 协议；DB schema。

## 8. 未验证清单（移交靶向验证门 / 终局候选版审计）

1. **门 #1（R-a 真机）**：面板 + 在线 agent 节点，导入含 forwards 的备份后不重连，核节点 gost 服务 / nft 规则终态与 DB 一致、同 id 改参旧服务被替换、中转 RemoteAddr 重算、离线节点重连后终态一致。本地 SQLite + mock-agent 探针只能证明调用点与命令下发在位，**不能顶替门结论**（FIX-PLAN §3 原话）。
2. **门 #4（R-b / R-c / R-d）**：真机/真 PG 下的回执逐段核对、未知类型双侧报错点名、Protocol 重推导行展示与筛选一致（本地为 SQLite 进程内验证；PG 方言面沿历轮归发版门判读）。
3. **BC 文案**：BC-R13-01（恢复后重推行为补齐注记：在线服务重建可能短暂，注记写明）、BC-R13-02（未知类型由静默成功改为报错注记）由文档批 / 终局升级包统一成文，本批不另出文案。
4. **修复面边界自证**：施工中未发现超出有界范围的情形（无需改动 agent 协议或 DB schema），故未触发「停手回主控重裁」条款。

— R13-FIX-BR-report END · 批 R 4/4 关闭（本地级）· 真机门 #1/#4 待靶向验证门 · END
