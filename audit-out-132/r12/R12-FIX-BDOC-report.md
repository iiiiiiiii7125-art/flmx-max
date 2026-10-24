# R12 修复报告 · 文档批 D（文字项与 BC 定稿，不占产品号）

- **基线**：`main` @ `cefb566`（= `integrate-r11` 尖端，任务书 pin 死值）。
- **工作分支**：`fix-r12/bdoc`（从 `cefb566` 新切；未动 `main`、未动 `integrate-r12`，集成由协调者做）。
- **依据**：`audit-out-132/r12/R12-FIX-PLAN.md` §1 文档批 D 与 §3 BC、`R12-X-verdicts.md` §2.2 / §4。
- **口径纪律**：未改任务书口径；断言/注释更新仅限 FIX-PLAN 点名的 D-01 两处注释文本；未用 `git add -A`，仅逐文件 add 本批明确文件（见 §提交）。
- **产品行为**：本批零产品行为变更（D-01 纯注释、D-02 定稿文案、D-03 措辞纪律）。

## D-01 · R10X-015 表述订正 — 完成

两处注释均删「渲染函数全集口径」断言，改为裁定一致表述；其余注释行与全部代码行未动：

| 处 | 改前（删） | 改后 |
|---|---|---|
| `go-backend/internal/kernel/chain.go:300-301`（`chainTargetFromInbound` 内 SS 支） | 改经 Segments 逐段归一，与进口同面及自建 Clash 面齐一（渲染函数全集口径） | 改经 Segments 逐段归一；自建 SS 服务端两落点维持单段 helper |
| `go-backend/internal/kernel/share.go:505-506`（`singboxOutbound` 内 SS 支） | 同上 | 同上 |

- 保留的后半句与裁定一致：多段仅在直调渲染函数时逐段归一（chain.go 续「解码字节不变」；share.go 续「坏段仍由 helper 原样返回、恒渲染口径不变」）。
- 裁定对应：`R12-X-verdicts.md` §2.2——自建 SS 服务端两落点（`render_singbox.go:133/138`、`render_xray.go:98`）维持单段 helper；多段仅在直调渲染函数时逐段归一。本批不立案、不占 R12X 编号、不重议 R11X-013。
- 验证：`grep -rn "渲染函数全集口径" go-backend/internal/kernel/` 无命中；`git diff` 两文件各 2 行改 2 行（4 insertions / 4 deletions，纯注释）。本机无 `gofmt` 可用，但 diff 证实仅注释行文本变化、缩进未动，格式风险为零。

## D-01 核对 · Q 矩阵 :8 — 未改（核对结果）

- `doc/audit/Q-protocol-kernel-format-matrix.md:8` 原文：「catalog 入站能力矩阵（面板可创建组合）∪ 导入节点全集。」
- 判定：此「导入节点全集」指导入节点的完整集合，与被删的「渲染函数全集口径」（渲染函数覆盖全集的断言）为**另一语义**，属 verdicts §2.2 已核查排除项。**该文件未作任何修改**，无 diff。

## D-02 · BC-R12-01 定稿 — 完成

- 定稿文件：`audit-out-132/r12/R12-BC-draft.md`（新建，入 git，随终局版发布）。
- 可转发文案要点（逐字口径，源自 FIX-PLAN §3）：
  - dashboard 计费方向标签订正：Flow 域外值（主要为联邦建隧道存量行）此前在本人卡片显「双向计费」，订正后显「未知」；
  - 联邦新建隧道改按单向计费建值；
  - 计费入账口径本身未变（此前即按单向入账），无资损、无需操作。
- 用户动作：无。
- 其余三条（T-c、T-d、D-01）无用户可见行为变更，依 FIX-PLAN §3 不列 BC，本文件亦未将其列入。

## D-03 · W-3 措辞纪律登记 — 完成

- **全形写法（今后一切引用必须如此）**：「`+`11 位数字（token 12 字符）拒 / `+`10 位数字（token 11 字符）放行」。
- **禁令**：禁止单写「11 位」而不注明基准。
- **依据**：`R12-X-verdicts.md` §4 核对点①——按数字位数读法为准；任务书 shorthand「恰 11 位含符号」不得读作 token 恰 11 字符（否则所指格为放行的 E02，与 W-3 原格双面拒冲突，该读法不成立）。
- **落点（以不改产品行为为限）**：
  1. 本报告本节（主登记）；
  2. `R12-BC-draft.md` 末尾「附注（内部注记，不转发）」一节——明确标注不属 BC 正文、转发时只取可转发文案一节。
- 未动 FIX-PLAN 原文（避免改任务书口径）；未动任何代码与口径结论（E02/E03 结论不变）。

## 提交

- 分支：`fix-r12/bdoc`（基底 `cefb566`）。
- 入本批文件（逐文件 add，未用 `git add -A`）：
  - `go-backend/internal/kernel/chain.go`（D-01 注释）
  - `go-backend/internal/kernel/share.go`（D-01 注释）
  - `audit-out-132/r12/R12-BC-draft.md`（D-02 定稿 + D-03 附注注记）
  - `audit-out-132/r12/R12-FIX-BDOC-report.md`（本报告）
- 未改文件：`doc/audit/Q-protocol-kernel-format-matrix.md`（核对后排除）、`R12-FIX-PLAN.md`（任务书口径未动）、`main` / `integrate-r12`（未动，集成待协调者）。
- 提交号：见下方提交记录（提交后补记）。

— R12-FIX-BDOC END · D-01 / D-02 / D-03 三项全部完成 · END
