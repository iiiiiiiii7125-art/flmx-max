# R13 修复文档批 D · 批报告（fix-r13/bDoc）

- 基线：main @ `60a4392`（`integrate-r12` 树，产品内容 `f49dc0d`、tip `60a4392`，最近发布 tag 1.4.4）。
- 分支：从 `integrate-r13` 切出 `fix-r13/bDoc` 施工；完工后提交到该分支，**未合入 `integrate-r13`**（协调者统一集成）。
- 依据（施工前全读）：`R13-X-ledger.md`、`R13-X-verdicts.md` §2（R12X-002 重定建议全文）、
  `R13-FIX-PLAN.md` §1/§5/§6。另逐字核对：`R13-X-verdicts.md` 证据链行（X-0 / L6 `118131f` / `canonicalXHTTPMode`）
  与 BC 登记行（§0.3、§3.2）。
- 范围：只写文档与 BC 草稿，不改产品代码，不占产品号。

## 交付 1：R12X-002 重定记录预备文本

- 落点：`audit-out-132/r13/R13-R12X002-redetermination-draft.md`
- 字节数：4896 字节（`wc -c` 落盘自验，见 §自验）
- 内容清单：
  - [x] 原 P3 → 建议重定 P2，写明为历史定级修正、非新立案、不占号
  - [x] 触发条件证据链：R12 条件正本逐字引用；R12 批 X 施工前探针 X-0
        （`mode: "AUTO"` / `mode: "Packet-Up"` exit 23，规范对照 `auto` exit 0）；
        R13 L6 独立复探（同版本 Xray 25.9.5，`118131f`：修后渲染四份全部 exit 0，
        逐字对照 ctrl-AUTO / ctrl-Packet-Up exit 23；L8 §5 渲染归一验证）
  - [x] 修复已在 R12 批 X 落地（渲染侧归一：`canonicalXHTTPMode` + 四处消费点），
        经 R12 靶向验证门与 R13 L6/L8 双路复验关闭；重定不产生新修复面，
        旁路规范化不在范围，欲改修复口径须另立条目
  - [x] 不溯改 R12-X-ledger 原值、不回填 R13 轮计数、不改变收敛结论（P2 不触 P0/P1 判据）
  - [x] 若终局审计不采纳须写明理由，不得仅以「已修复」为由；两种结论下 R12 ledger 原值均不动
  - [x] 供终局审计直接采用的记录文本草稿段落（§6），并全篇标注「草稿，未定版；重定权归终局候选版审计」

## 交付 2：BC 文案草稿

- 落点：`audit-out-132/r13/R13-BC-draft.md`
- 字节数：4948 字节（`wc -c` 落盘自验，见 §自验）
- 内容清单：
  - [x] BC-R13-01（R-a / R13X-001）：恢复后在线节点转发数据面改为与正常写路径一致重推；
        无用户操作、无迁移；重推可能致在线服务短暂重建，注记与可转发文案中均写明；
        离线节点重连路径不变；回执披露重推结果（至少失败节点清单）
  - [x] BC-R13-02（R-c / R13X-005）：未知类型由静默成功改为报错（导出/导入双侧，文案点名未知值）；
        直接调用方若依赖旧语义须修正类型清单，合法类型行为不变；无迁移；
        注明 UI 路径当前被前端 16 值清单掩盖，主要影响直接调用方
  - [x] 两条均标注「随终局版发布」；终局版发布后生产最终升级包须含两条可转发文案
        （`R13-FIX-PLAN.md` §6）
  - [x] 其余五条无 BC 的理由一并登记：R13X-002 守卫类 fail-closed 收口；
        R13X-003 内部口径对齐；R13X-004 计数类加法字段；R13X-006 派生字段修正；
        R13X-007 恢复面门与保存面门对齐的 fail-closed 收口；R12X-002 重定本身亦不产生 BC

## 纪律自证

- 未使用 `git add -A`；仅对本批三份明确文件逐个 `git add`（见提交）。
- 产品代码路径零改动：`git diff --name-only` 为空；
  `git status --short -- go-backend go-gost vite-frontend doc install.sh panel_install.sh` 零输出。
- 未合入 `integrate-r13`；提交仅落在 `fix-r13/bDoc`。

## 自验（落盘实测）

- `R13-R12X002-redetermination-draft.md`：4896 字节；
  尾行：`— R13-R12X002-redetermination-draft END · 草稿，未定版；重定权归终局候选版审计 · END`
- `R13-BC-draft.md`：4948 字节；
  尾行：`— R13-BC-draft END · 两条 BC 随终局版发布 · 其余五条无 BC，理由见上表 · END`

— R13-FIX-BDOC-report END · 文档批 D 两项交付完成，未合入 integrate-r13 · END
