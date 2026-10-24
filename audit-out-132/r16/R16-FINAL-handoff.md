# R16 FINAL 交接（致 R17）

基线：main @ b54b44f。本轮不发版。

## 未关闭项（R17 须复核）

- **R15-FIX-01（OPEN）**：入站页空指针。renderInbound 一腿已修，sheetProbe 一腿未修（已单独立案 R16-X-01）。R17 关闭验证须同时复核两处位点。
- **R16-X-01**（sheetProbe 空指针，产品·演示面 P3）：修复见 R16-FIX-PLAN F-02；走查须覆盖 sheet 打开一步。
- **R16-X-02**（userList 分页溢出，产品 P3）：修复见 F-01；先红后绿用 L3 两溢出形。
- **R16-X-03**（inboundProbeView 死代码，产品 P3）：修复见 F-03；删除或接线二选一。

## 结转观察

- L5-CAND-01：浏览器任务执行前须先以 b54b44f 构建重定 served chunk 基线（R15 的 58/58 证据已过期）。
- VMess/SS/Trojan 计时腿：登记须重跑（渲染路径有改动，不承继 148B 证据），由发版门或专项验证安排。
- 仍未验证：真实 Gist 往返、单族 sock_diag 真机形态、远端/非 Debian 内核、生产 DOM 实点。
- R15 观察项（L1 OBS-1/OBS-2、O-R15-A5-1/O-R15-A5-2、VG-09、VG-10）、L3 O-L3-1/O-L3-2/O-L3-3 结转；R4×C8 已闭合。

## 轮换

- R15 轮换 A5，R16 轮换 A2。R17 轮换另一域（建议 A3 或 A6，由 R17 协调者定）。
- 哨兵表：R17 承本轮 R16-00-sentinel.md 53 行逐字沿用，基线重钉为 R17 基线提交。

## 护栏

- N-01…N-37 全程有效；哨兵表开工前落盘；销账对账探针对象本身；裁定只认落盘原始日志。
- 修复分支独立 worktree，禁用 `git add -A`；修完重审，不发版。

R16-FINAL-handoff END
