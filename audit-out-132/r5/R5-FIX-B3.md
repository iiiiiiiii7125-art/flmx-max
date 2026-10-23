# R5 修复 · 批 3 报告（授权台账族）

- 批次：R5 批 3（R5-FIX-PLAN §1 批 3）；分支 `fix-r5/b3`，基线 `integrate-r5` @ `112a911`（1.4.1 发布态 + 文档提交）。不合并、不发版、不打 tag。
- 认领条目：R5X-005（P2）、R5X-014（P3）、R5X-015（P3，定案项）。三方对账：本批认领 = FIX-PLAN 批 3 分配 {005, 014, 015}，差集 ∅。
- 文件域：`go-backend/internal/store/repo/repository.go`（`deleteSameNameGroupTx` / `importPermissions` / `sweepImportedAuthorizationTx` + 新增 helper `userTunnelCoveredByAnyPairTx`）；新增回归 `go-backend/internal/store/repo/backup_r5_b3_test.go`。`repository_mutations.go` 未改（共享 helper `grantPairMembershipHoldsTx` / `promoteCreatorFlagTx` 只读复用）。
- 纪律：逐条先红后绿（红态证据见各条）；提交只 `git add` 明确文件；**断言更新：无**——既有断言零修改，`TestR4B4_GS02_GrantMembershipMismatchWarns` 在 014 降级后仍按原文 PASS（降级 warning 保留「成员对应」语义且行照常落库，见 §2 回执语义）。

## 1. R5X-005（P2）—— 关闭（代码修复 + 四格回归）

- 落点：`repository.go:4399` `deleteSameNameGroupTx` 的 grants 合并去重分支（用户组/隧道组两侧共用同一函数，一处修复双面受益）。
- 改法：dup 判定由 `Count` 改为取回保留行全集；被删（占位侧）行 `created_by_group=1` 时，先把保留行中 cb=0 者 `Update(created_by_group, 1)`（来源 OR/提升，与 R4X-005 `promoteCreatorFlagTx` 同口径），再删占位行。permission 表无 cb、不受影响；无同键保留行时仍走原重指向分支，行为不变。
- 先红：`TestR5B3_005_SameNameMergeCreatorFlagOR/deleted_cb=1_kept_cb=0` 修复前 FAIL（`merged grant created_by_group = 0, want OR(1,0) = 1`），隧道组侧同格 FAIL；其余三格修复前平凡 PASS（防过修对照）。
- 四格回归（格 = (占位/被删行 cb, 继任/保留行 cb)，断言合并后保留行 cb 与继任组 `RevokeGroupGrantsByGroup` 后 UT10 终态）：
  - (1,0)：合并后 cb=1；撤销返回含 (2,1) 对、UT10 删除。本条缺陷格。
  - (0,1)：保留行本已 cb=1，被删行 cb=0 不得覆盖/降级保留行——合并后仍 cb=1、UT10 随撤销删除。此格为防过修格：防「以被删行 cb 覆写保留行」的错误修法。
  - (1,1)：合并后 cb=1、UT10 删除。
  - (0,0)：合并后 cb=0、UT10 存活且 revoked 为空——防「一律提升 cb=1」的过修。
- 隧道组侧：`TestR5B3_005_SameNameMergeTunnelGroupSide` 单独构造 (1,0) 格实测（补 R5-G §八「隧道组侧未单独构造探针」的未验证项）：合并后 cb=1、按 tunnel_group 撤销后 UT10 删除。
- 行为变更：否（合法导入终态的授权集合不变，仅台账来源属性保真；与 R5-G 定性一致，不单列 BC）。

## 2. R5X-014（P3）—— 关闭（窄硬门案 A 落地）

- 落点：`repository.go:4570` `importPermissions` grants 循环，写回点同一判断处（谓词 `grantPairMembershipHoldsTx` 既有接线，零新增查询）。
- 案 A 写死执行（verdicts §5.3）：cb=1 且 `!holds` → 写回降级 cb=0（`effectiveCB`），warning 升级为明示降级（逐字含「已降级为普通登记来源（created_by_group=0）……不再具备随分组撤销连带删除用户隧道的能力」）；cb=0 且 `!holds` → 原 warning 逐字不变、无「降级」字样；`holds` 成立的行逐字不变。全量硬门（案 C）未做。
- 回执字段语义（施工前与 H 域对齐一套，H5/S-01 口径）：降级走 `ImportWarnings`（`rep.warnf`），**不计 `SkippedRows`、不跳行**——grant 行照常落库、`PermissionsImported` 与 grant 落库计数不变；与 H 域「成员对应校验为 warning 披露面、非跳过面」的既有回执语义一致，两域不各写一套。`TestR5B3_014_MismatchCB1DowngradedOnWriteBack` 对此三点（行在位 / SkippedRows 为空 / warning 含「成员对应」+「降级」）逐项断言。
- 先红：同用例修复前 FAIL（`mismatched cb=1 grant must be written back downgraded to cb=0, got 1`）；cb=0 格与合法对照格修复前平凡 PASS。
- 第二跳封堵实测：降级导入后 `RevokeGroupGrantsByGroup("user_group", 1)` 不再连带删除手工 UT10，revoked 不含 (2,1)；合法对照（成员齐备 cb=1）导入后撤销仍按组建口径删除 UT10（`TestR5B3_014_MatchingCB1Unchanged`）。
- 行为变更：是——BC-R5-03 已在 FIX-PLAN §2 登记（错位备份的组建授权降级写回；合法备份不受影响）。

## 3. R5X-015（P3，定案项）—— 按定案形态关闭（定案登记 + 过渡 warning 落地 + 已知限制文本）

- 关闭形态声明（verdicts §5.4 写死）：**不做墓碑表本体**；本条关闭 = 定案登记（墓碑方向保留、触发条件三条，见 §4 已知限制文本）+ 过渡 warning 落地 + 已知限制入发版说明遗留清单。**warning 落地不作本条的关闭依据**，关闭依据是定案本身。
- 过渡 warning 落点：`repository.go:3744` `sweepImportedAuthorizationTx` 事务尾 UT 循环 + 新 helper `userTunnelCoveredByAnyPairTx`（`:3706`，任一权限对覆盖判据，不论组状态）。判据三连：本次新落（存量快照外）∧ 无 grant ∧ 现行无任何权限对覆盖 → 汇总增计一条通用 warning（逐字含「将以无台账授权存续，后续分组撤销对其不可见……本次恢复无法识别（已知限制）」）。行不删、不计 skipped、导入计数不变（零误杀）。
- 结构调整说明：sweep 原「grants>0 或 groupStateFromBackup 即 continue」拆为——grants>0 直接略过；停用对覆盖且非同批组状态时仍按 R4X-006 删除（原 warning/计数不变，且被删行不重复计无覆盖 warning）；停用对覆盖但组状态来自同批备份时有权限对覆盖、不计；其余以任一权限对覆盖判据分流。
- 先红后绿：子形态 ①（pair 保留启用、用户已移出成员）与 ②（pair 已移除、成员保留）两用例修复前均 FAIL（`must add a generic warning, got []`）、修复后 PASS 且 UT 照常落行；防过修对照（启用对覆盖的手工 UT 不新增该 warning；停用对覆盖的 UT 仍被 sweep 删除且不重复计）修复前后均 PASS。
- 同批甄别：sweep 的 grants>0 豁免面（R5-G-S02 疑似）不在本批范围，未动、维持结转。

## 4. 已知限制登记文本（R5X-015，供发版说明遗留清单取用）

> 已知限制（R5X-015，定案缓做）：以成员差集或权限对移除方式撤销的分组授权不留痕迹记录。此后若用只勾选 userTunnels 的部分备份导入同一（用户，隧道）的旧授权行，该行会被恢复，且后续分组撤销对其不可见。自本版起，此类恢复会在导入回执中增计一条通用提示（该行无授权台账、且现行无任何分组权限对覆盖，将以无台账授权存续），不再静默；但提示不区分该行是「曾授权后撤销」还是「从未授权的手工授权」，恢复结果本身不变。根治方案为撤销墓碑表（方向保留），触发条件（满足任一即启动立项）：① 出现一例部分导入复活已撤销授权的真实运营事件；② 后续版本本就要做 user_tunnel/grant 相关 schema 迁移（搭载窗口）；③ 备份导入增加「恢复到时间点」类语义。启动前置：先落墓碑写点/清点的全集清点表并逐点接线验收。在此之前不得以 R4X-006 已关闭表述覆盖差集/pair 形态。

## 5. 测试结论（被测对象逐字，N-35）

- 本批新用例（`go test ./internal/store/repo/ -run 'TestR5B3' -count=1 -v`，被测对象 = `deleteSameNameGroupTx` 合并分支 / `importPermissions` 写回 / `sweepImportedAuthorizationTx`）：8 个测试函数全 PASS（005 四格子测试 + 隧道组侧 1、014 三格、015 两子形态 + 防过修对照 2 子测试）。
- 存量回归子集（`-run 'TestBackup|TestR4B4|TestR3B6|TestR2B8|TestR1B5|TestFix2B|TestImport|TestR5B3'`，repo 包）：PASS（115.7s）——含 R3B6 facet-b 合并/全量往返零 warning、R4B4_006 sweep、GS02 既有用例，均未回退。
- repo 全包（`go test ./internal/store/repo/ -count=1`）：PASS（323.9s）。
- handler 子集（`-run 'TestR4B4|TestR4B3|TestBackup|TestR3B5|TestR3B6'`）：PASS（32.9s）；`go build ./...` PASS；`go vet ./internal/store/repo/` 无告警。
- 环境登记：首轮全包跑批中 `/tmp`（512M tmpfs）被多路并行 scratch 写满致 paneld 链接报 `no space left on device`（非代码失败）；改以 `TMPDIR`/`GOTMPDIR` 指向 worktree 内目录后 build 与全包测试全绿。
- 未验证：PG 方言未实跑（四处落点均为方言无关的 GORM 操作，与 R5-G §八同口径登记）；真机导入抽验属发版门清单第 8 项（同名合并 cb 保真 / cb 降级两例），留发版轮。

## 6. 残余与移交

- R5X-015 墓碑本体：定案缓做，触发条件见 §4；不得在后续轮次以「warning 已落地」单独宣称根治。
- G-S02（sweep grants>0 豁免面）：维持疑似结转 R6，本批未动。
- 本批无新增 BC（014 的 BC-R5-03 已在 FIX-PLAN §2 在册；005/015 按 FIX-PLAN 口径不单列 BC，015 已知限制文本见 §4）。
—— 自证：本行系全文末行（落盘复核：字节数 + 尾行探针，见域外汇报）。
