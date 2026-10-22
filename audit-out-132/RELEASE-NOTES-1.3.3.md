# FLMX 1.3.3 发版说明（紧急修复版）

紧急修复版：只修一个问题——面板更新后浏览器死缓存，无其他行为变更。本版自 1.3.2 基线切出，仅含缓存修复，不含 1.3.2 全量审计的其他在修内容（那些随后续版本顺延）。

## 问题

**现象**：面板升级到新版本后，浏览器打开仍是旧界面；刷新、反复刷新都不变，只有手动清浏览器缓存（或换浏览器）才能看到新版。

**根因**：前端的 Service Worker 用的是「提示式更新」（vite-plugin-pwa `registerType: "prompt"`）。新版本发布后，新的 Service Worker 装好却停在等待状态，必须等用户点掉更新提示条上的「刷新」才肯接管；用户点「稍后」、提示被遮挡或根本没注意时，旧 Service Worker 就无限期继续用它预缓存的旧界面应答——浏览器刷新走的是 Service Worker 而不是网络，HTTP 层再怎么刷新都绕不过去。次因是静态文件缓存口径：无哈希的 public 文件（favicon、PWA 图标、背景图）也被发了 30 天 immutable 强缓存，换图一个月刷不出来，且响应里有两份 Cache-Control 头互相打架。

## 修复内容

- **Service Worker 改自动更新**：`registerType` 改为 `autoUpdate` 并开启 `skipWaiting` / `clientsClaim`，新版本一就绪立即接管并清掉旧预缓存；前端删除提示式更新流（不再有需要用户点「刷新」的更新条）。
- **带哈希的构建产物**（`/assets/`）：单份 `Cache-Control: public, max-age=31536000, immutable`。文件名即版本，新版引用新文件名，强缓存不会错版。
- **无哈希静态文件**（favicon、图标、logo、背景图）：改为 `public, max-age=86400` 并去掉 immutable 与旧的双头，过期自动回源验证。
- **入口文档与 sw.js / manifest**：维持 `no-cache, no-store, must-revalidate`（本来就对）；后端直出的 404 错误页补 `Cache-Control: no-cache`，全部浏览器出站面口径统一。

## 用户侧效果

- 升级到 1.3.3 后，**下次打开面板即自动换上新界面**：浏览器照例检查 sw.js（no-store 保证拿到新版）→ 检出新 Service Worker → 自动接管并清旧预缓存 → 页面进入新版。
- **存量用户无需手动清缓存、无需任何操作**，至少再打开一次面板即可自愈。
- 此后每次发版同理：更新后刷新即见新版，不再死缓存。

## 其他说明

- 除上述缓存行为外，**无任何功能或行为变更**：后端业务逻辑、节点 agent、协议与数据格式均与 1.3.2 一致，节点无需升级。
- 手机端与桌面端是同一入口、同一 Service Worker，本修复一并覆盖，无独立面遗漏。

## 凭据

（发版后回填：main 提交、tag、CI 与资产核验结果见 audit-out-132/fix132/HOTFIX-133-RELEASE.md）
