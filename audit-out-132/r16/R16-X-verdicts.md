# R16 裁定（X-verdicts）

基线：main @ b54b44f。裁定只认落盘原始日志+名单存档（N-37），摘要计数不得作裁定依据。

## V-01：新立案典则编号

- 五路原始候选 4 件（L1-CAND-01、L1-CAND-02、L3-CAND-01、L5-CAND-01），无撞号。典则编号按对象定：
  - **R16-X-01 = L1-CAND-01**：入站页 `sheetProbe` null.ok（inbound.tsx:1020-1024），产品·演示面 P3。
  - **R16-X-02 = L3-CAND-01**：`userList` 分页乘法溢出致 slice panic（handler.go:740），产品 P3。
  - **R16-X-03 = L1-CAND-02**：`inboundProbeView` 死代码 + 注释失实（node_latency.go:460-484），产品 P3。
- L5-CAND-01（served-chunk 静态证据过期）为观察项，不单独立案（见 V-03）。

## V-02：三项新立案定级均为 P3

- R16-X-01 定 P3：演示面缺口——生产实时路径经由已知 API 形态不可达（`inbound/list` 以 `omitempty` 省略无结果 probe、永不发 null）；演示走查点击可达、页面崩溃为实测；无数据丢失、无凭据外泄。先例：R15-FIX-01 同类空指针亦为演示面（修复中发现，未单独定级；本项为其第二位点）。
- R16-X-02 定 P3：触发须管理员身份 + 主动构造溢出量级参数；生产由 Recover 兜底回 code -2，不 crash、不泄漏；属健壮性缺口，非可用性/安全缺口。来源为 R1 深审疑似项，本轮首次实证确认。
- R16-X-03 定 P3：死代码零运行时影响；注释失实误导后续维护。修法为删除或接线并订正注释（见 R16-FIX-PLAN F-03）。

## V-03：L5-CAND-01 不立案

- served-chunk 静态证据（58/58，5bd5a90 构建）因 fix-r15 改动 inbound.tsx 而过期，属证据基线失效、非产品缺陷。处置：记观察项；浏览器任务执行前须先以 b54b44f 构建重定 served chunk 基线。不另编号，不计入产品面。

## V-04：R15-FIX-REPORT.md 去向与 §3 结论裁定

- 该文件确实存在，但以未入库形态存于修复 worktree `~/workspace/.fixr15/audit-out-132/r15/`（9,164 字节，sha256 `fda006d9d0e18fb5ae6a96d36902c90aba7f1d368f7aeddf2df0b2982fb761da`，b54b44f 提交后 3 秒落盘）。协调者基线注记“工作树与全部历史中均不存在”修正为“共享工作树与提交历史中不存在，修复 worktree 内未入库存在”。
- 处置：已原样归档至 `audit-out-132/r15/R15-FIX-REPORT.md`（同字节数、同哈希，内容未改），不再视为丢失。
- 其 §3“全仓同型仅此一处”结论**不成立**：L1 本轮同型搜索实证第二处位点 sheetProbe（inbound.tsx:1020-1024）与修复前旧式逐字同构，且点击探针 Run D 复现崩溃。以本轮探针为准；该文件 §3 结论作废，不作关闭依据。文件本身留存未改写。

## V-05：R15-FIX-01 未关闭裁定

- renderInbound 一腿已修在位（敏感性探针 Run B 复现旧态 20 problems、首因 null.ok；修复后该腿闭合），但关闭条件“确认全仓同型仅此一处”不成立（V-04）。裁定：**R15-FIX-01 未关闭，结转 R17**；第二位点单独立案 R16-X-01，不与 R15-FIX-01 合并计数。R17 关闭验证须同时复核两处位点。

## V-06：哨兵承继形态裁定

- 53 行：51 PASS / 0 回退 / 2 本地不可判定·承继（S-16、S-34；S-41 PG 子格同口径承继，行级不降格）。承继形态与 R13/R14/R15 一致，不标 PASS、不计失败、不以同域其他探针顶替（N-35）。

## V-07：VMess/SS/Trojan 计时腿承继裁定

- 不承继 148B 门证据。`7244fbb..b54b44f` 触及 6 个渲染文件（render_xray.go、chain.go、split.go、model.go、handler/kernel.go、handler/kernel_chain.go），不满足“零改动”承继条件。L5 的非干涉子分析（直连 Xray 入站下发物逐 hunk 核对行为等价）为代码读数结论，不得顶替实测。登记须重跑，留待发版门或专项验证。

## V-08：观察项处置

- L5-CAND-01、L3 O-L3-1/O-L3-2/O-L3-3、R15 观察项（L1 OBS-1/OBS-2、O-R15-A5-1/O-R15-A5-2、VG-09、VG-10）一律结转观察、不立案；下轮复核时点名。R4×C8 本轮已单列补格闭合，不再结转。

R16-X-verdicts END
