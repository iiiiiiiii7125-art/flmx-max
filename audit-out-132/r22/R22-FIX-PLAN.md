# R22 修复计划（FIX-PLAN）

基线：main @ b823421。依据：R22-X-ledger.md、R22-X-verdicts.md。修完开 R23 重审，不发版。

## 产品修复（1 项）

### F-01：R22-X-01——`generateCaptcha` 死导出指向未注册端点（P3）

- 对象：`vite-frontend/src/api/index.ts:382`（`generateCaptcha: () => Network.post('/captcha/generate')`）。
- 改法：删除该行导出（纯前端死代码清理；`verifyCaptcha`（`:383`）亦为死导出但其端点已注册，与本项同批清理即可，不另编号）。
- 回归：先红后绿——API 调用点全集重核（190 → 189，全部映射到后端注册表，缺失 0 条）；`grep -rn generateCaptcha vite-frontend/src scripts/` 0 命中；前端 `pnpm run build`（tsc + vite）全绿；`go test ./...` 产品失败 0。

## 文档修复（1 项）

### F-02：R22-X-02——`forward.md:67` 宣称的「节点页的 Pending Delete 管理接口」不存在（文档 P3）

- 对象：`doc/guide/forward.md:67`（尾部 nftables 删除 pending-delete 小节）。
- 改法：改该句为「通过 `POST /api/v1/node/nftables/pending-deletes` 查看」（与 `nodes.md:231-246` 口径一致）。另立案做页面入口属产品决策，不在本项范围。
- 回归：文档一致性核对——`forward.md:67` 与 `nodes.md:231-246` 不再矛盾；`doc/` 构建 exit 0；`grep -rn pending-delete vite-frontend/src` 仍 0 命中（无 UI 宣称）。

## BC 注记（单独拎出，不占产品编号）

无 BC 项。F-01 为前端死代码删除（零调用点）；F-02 为文档措辞修正（与已在位的正确文档口径对齐）。

## 执行要求

- 分支 `fix-r22`（独立 worktree；禁用 `git add -A`，只 add 明确文件；每修复带先红后绿回归测试；修后 go build/vet/test + 前端 tsc 全绿，产品失败 0）。
- 测试机纪律：锁原子化（`scripts/testmachine-lock.sh`）+ 每次重启/部署前复核锁归属 + 窗口起止写入报告首行。
- R22 未验证项（远端/非 Debian 内核、浏览器 DOM 腿、真实 Gist 端到端、单族 sock_diag 真机形态、F-01 多节点真机腿、VG-09、真 PG 方言面、SIP023 四面与 144 格、`withSpeed=true` 测速腿、socket 4 SKIP、Alpine/OpenRC 套件、demo 内容闸）继续结转 R23 登记。
- 修复报告落盘 audit-out-132/r22/（字节数 + 尾行 END 自证）；R22 审计产物随本次提交归档进库。
— R22-FIX-PLAN END · 自证：本行之前正文字节数 2427 · END
