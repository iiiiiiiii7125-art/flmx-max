# R16-L2 V-06 归档验证 + 文档/基建关闭验证

- 单路：R16-L2（V-06 归档验证 + 文档/基建关闭验证）
- 基线：main @ b54b44f（完整提交 b54b44f772d8b4efdd6d08b1fbbbc40c8091a143）
- 只读读数树：~/workspace/.r16-tree @ b54b44f（detached HEAD）；核对前后 `git status --porcelain` 均为空、`git rev-parse HEAD` 均为 b54b44f772d8b4efdd6d08b1fbbbc40c8091a143，读数树未修改
- 裁定来源：R15-X-verdicts.md V-06（工作树未跟踪文件，仅作裁定描述来源；归档在位与否一律以 `git show b54b44f:<path>` 直读树内已入库内容为准，未读工作树未跟踪文件作证据）
- 性质：只读审计；未改产品代码、未打 tag、未发版；唯一写出为本报告文件本身

## 结论总览

| 核对项 | 结论 |
|---|---|
| V-06 (a) PORT-3342-REA-UDIT.md 机理订正 | 在位 |
| V-06 (b) R14-00-routes-raw.csv（idx 60/109 均为 GET,HEAD） | 在位 |
| V-06 (c) R14-00-baseline.md（:38 分布行已改） | 在位 |
| diff 5bd5a90..b54b44f 范围 | 恰含三处归档 + fix 提交 6 文件，共 9 文件；无多余、无缺失 |
| 对照 RELEASE-146-NOTES.md:20 | 在位 |
| 对照 gist-backup.md:22 /:29 | 在位 |
| check-doc-consistency.sh（b54b44f 树重跑） | 全绿，exit 0，RESULT: PASS |
| 字面量三处版本一致 | 一致，均为 1.4.8（任务书“如为 1.4.6”条件未触发，见 §4） |
| 新发现 | 0 项（无 L2-CAND 编号） |

## 1. V-06 三处归档逐项核对（`git show b54b44f:` 直读）

三处在 5bd5a90 树内均不存在（`git show 5bd5a90:<path>` 均 fatal，与 V-06“L5 实读：三文件皆未跟踪”一致），在 b54b44f 树内均已入库；归档提交 b54b44f 本身只含这三文件，425 行新增、0 行删除（三个文件均为新文件）。

### (a) audit-out-132/port-3342/PORT-3342-REA-UDIT.md —— 在位

- 树内在位：`git show b54b44f:audit-out-132/port-3342/PORT-3342-REA-UDIT.md`，52 行；b54b44f diff：+52/−0，新文件。
- 机理订正内容与裁定描述一致（R14-A3 §A3-02 机理订正，纠“每 5 分钟发一次失败通知 / enqueueNotify 无去重窗口”失实）：
-:32 标题已改：`### REA-P3-1：自动循环在「令牌+间隔已设、密码被清」态下可达，每配置间隔一次失败尝试、失败通知至多 1 条/30 分钟（P3）`
-:34 `原标题“每 5 分钟发一次失败通知”及下文“enqueueNotify 无去重窗口”失实，见 R14-A3 §A3-02 机理订正（探针实证：同指纹两次 dispatchNotify，第一次 Sent、第二次 Suppressed；notify.go:227 notifyDupWindow = 30 分钟）。`
-:36 机理段：`runGistBackupLoop 只认 gistDue（令牌 + 间隔），不认密码；……失败刷新 lastAt，下一次尝试按配置间隔再到期（非每 5 分钟一轮）；通知经 dispatchNotify 30 分钟去重窗，同一失败文案至多 1 条/30 分钟；lastError 每次失败改写，直至人工干预。`

### (b) audit-out-132/r14/R14-00-routes-raw.csv —— 在位

- 树内在位：`git show b54b44f:audit-out-132/r14/R14-00-routes-raw.csv`，203 行（含表头），202 行数据，idx 1..202 连续无断号；b54b44f diff：+203/−0，新文件。
- idx 60（文件行 61）：`60,/api/v1/open_api/gh/,"h.githubMirror (via const ghMirrorPath, subtree pattern)",githubMirror,"GET,HEAD",PUBLIC (JWT skip; own credential: captcha/login/peer-token/none),handler.go:303`
- idx 109（文件行 110）：`109,/api/v1/open_api/sub/,h.openSubscription,openSubscription,"GET,HEAD",PUBLIC (JWT skip; own credential: captcha/login/peer-token/none),handler.go:352`
- 全表仅 2 行含 `GET,HEAD`（即上述两行），与基线文件:38 的“GET,HEAD 2”互洽。

### (c) audit-out-132/r14/R14-00-baseline.md —— 在位

- 树内在位：`git show b54b44f:audit-out-132/r14/R14-00-baseline.md`，170 行；b54b44f diff：+170/−0，新文件。
-:38 分布行已改（行号以 `nl -ba` 实测）：
- `方法守卫分布（逐 handler 函数体机械推导，r.Method 检查行逐字复核；197 条旧行与 R13 值逐行一致）：POST only 131 / ANY（no method check in body）53 / GET only 13 / not(Get,Post) 1 / GET,HEAD 2 / GET,POST 1 / GET (WS upgrade) 1（合计 202；……）。method_guard 列统一记允许方法集（R14 A2-4 订正：open_api/sub/ 与 open_api/gh/ 均为 GET,HEAD）。`
- 自洽复核：方法守卫分布求和 131+53+13+1+2+1+1 = 202；:37 鉴权分布求和 135+44+17+1+5 = 202；均与 CSV 的 202 行数据一致。

## 2. diff 5bd5a90..b54b44f 范围核对

`git log 5bd5a90..b54b44f` 仅 2 提交：583cec0（fix）与 b54b44f（归档）。`git diff --numstat 5bd5a90..b54b44f` 共 9 文件，504 行新增 / 3 行删除，无多余、无缺失：

| 文件 | 增/删 | 归属 |
|---|---|---|
| audit-out-132/port-3342/PORT-3342-REA-UDIT.md | +52/−0 | b54b44f 归档（V-06 a） |
| audit-out-132/r14/R14-00-baseline.md | +170/−0 | b54b44f 归档（V-06 c） |
| audit-out-132/r14/R14-00-routes-raw.csv | +203/−0 | b54b44f 归档（V-06 b） |
| skills/flmx-api/references/errors.md | +2/−1 | 583cec0 fix（R15-X-01） |
| vite-frontend/scripts/demo/build_fixtures.py | +27/−0 | 583cec0 fix（R15-X-02） |
| vite-frontend/scripts/demo/check-fixtures-content.mjs | +36/−0 | 583cec0 fix（R15-X-02） |
| vite-frontend/scripts/demo/record_items.mjs | +6/−0 | 583cec0 fix（R15-X-02） |
| vite-frontend/src/demo/fixtures.json | +1/−1 | 583cec0 fix（R15-X-02） |
| vite-frontend/src/pages/inbound.tsx | +7/−1 | 583cec0 fix（R15-X-02） |

小计：归档 425 行新增 / 0 删除；fix 79 行新增 / 3 行删除；合计 504/3，与 diff 总计一致。

## 3. 对照核对：V-06 提及的同批已入库项

- 合并 95f328c（`merge(fix-r14/f01f02): config/get gist guard + gist/save no-password guard`）是 b54b44f 的祖先（`git merge-base --is-ancestor 95f328c b54b44f` 成立）；该合并的 stat 显示其文档改动恰为以下两文件。
- `audit-out-132/final/RELEASE-146-NOTES.md:20`（`git show b54b44f:` 行号实测）：REA-P3-1 行，含订正后机理——“每配置间隔一次失败尝试（失败刷新 lastAt，下次尝试再推一个间隔），失败通知经 30 分钟去重窗压至至多 1 条/30 分钟……（R14 订正：原‘每 5 分钟发一次失败通知’失实，见 R14-A3 §A3-02 机理订正。）”——在位。
- `doc/guide/gist-backup.md:22`：状态里的 `tokenHint` 只露令牌首 4 位与末 4 位；Gist 的 8 个配置键全部不进本地备份文件——在位。
- `doc/guide/gist-backup.md:29`：“后台每 5 分钟检查一次是否到期；到期按‘上次尝试时间’算（gist_last_at，成功失败都刷新）……失败尝试本身按配置间隔触发（不是每 5 分钟一次），且同一失败文案在 30 分钟去重窗内只通知一次——不会每 5 分钟发一条失败通知。”——在位。

## 4. 文档一致性：check-doc-consistency.sh 在 b54b44f 树上重跑

- 执行：`bash ~/workspace/.r16-tree/scripts/check-doc-consistency.sh`（脚本声明只读，仅 grep/比较；执行前后读数树 `git status` 均为空、HEAD 仍为 b54b44f，未改树）。
- 结果：exit 0，全绿，`RESULT: PASS（known-pending 项不计失败）`：
- `OK: nodes.md「Clash / Mihomo」行与 share.go 的 XHTTP 渲染一致`
- `OK: nodes.md「Shadowrocket」行与 share.go 的 XHTTP 渲染一致`
- `OK: 技能包覆盖 handler.go 全部 197 条 /api/v1 路由`
- `OK: 技能包版本三处一致：1.4.8`
- `OK: install.md 维护菜单与 panel_install.sh 一致（6 项）`
- `OK: install.md Caddy 示例反代目标与脚本一致（127.0.0.1）`
- `OK: 默认账号字面量一致：admin@user`（F-064 known-pending 项本次未触发 PENDING 分支，直接报 OK）
- 字面量三处版本一致，独立复核（`git show b54b44f:` 直读行号）：
- `skills/flmx-api/SKILL.md:6`：`version: "1.4.8"`
- `skills/flmx-api/package.json:3`：`"version": "1.4.8"`
- `doc/ai-skill.md:219`：`git tag 1.4.8`
- 三处均为 1.4.8，一致。任务书“当前 main 字面量口径如为 1.4.6 而非 1.4.8”这一条件未触发——实测即为 1.4.8，无需援引历轮已知的发布分支模式解释，不构成缺口。旁证：归档的 R14-00-baseline.md:17 亦记最近发布 tag 为 1.4.8（bd9f99d），口径一致。

## 5. 新发现清单

无。未立 L2-CAND 编号（0 项）。本轮核对未发现与 V-06 描述不符、diff 范围外溢出、对照项缺失或文档一致性失败之处。

## 落盘自证

- 本文件：`~/workspace/flvx-max/audit-out-132/r16/R16-L2-v06-docs-closure.md`
- 自证字节数：008906 bytes（wc -c 实测，见下行复核命令输出；本行占位数字与实测值等长替换，替换后复核字节数不变）
- 尾行：本文件末行即 END 行（见文件末尾）

— R16-L2-v06-docs-closure END
