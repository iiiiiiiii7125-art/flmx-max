# R22 基线（开工前落盘）

- 审计对象：main @ b823421（`b82342137cb3157197ad43906c41ccbc6145b64d`，R21 修复 fix-r21 已合入并推 origin；2026-10-25，`docs(r21): R21 fix integration report (F-01..F-03)`）。
- origin/main 与本地 main 均为 b823421（实测一致）。
- 树哈希：b823421^{tree} = e4f93c725287c0e0be4f9416e02b207761ebcbd9。
- 最近发布 tag 仍为 1.4.9（a751016）；本轮不发版、不打 tag、不改产品代码（只读审计）。
- 审计轮：靶向复审轮。五路：L1 R21 三项关闭验证、L2 R21 结转复核、L3 哨兵 + 轮换域 A9（K 域）、L4 常设全集、L5 未验证清单复核。
- 轮换域：A9（K 域：前端页面与交互一致性；catalog §2 K 域必查点 K-01…K-08；上轮本域基线 `r14/R14-A9-report.md`（42,586 字节，R14 全面审计 main @ 7244fbb，实物内容核对，V-06 教训：不得凭编号推断））。
- 口径：数据表 50 张 = 49 张 AutoMigrate（含 node_traffic）+ 迁移创建的 schema_migration；路由钉死值 202；节点列表键数 27/28/32。
- 护栏：N-01…N-37 全程有效；测试机纪律：锁抢占原子化（仓库内 `scripts/testmachine-lock.sh`，锁本体 `$HOME/workspace/.testmachine-lock.d/`，mkdir 单次原子抢占）+ 每次重启/部署前 `check` 复核锁归属 + 窗口起止与 lane 归属写入各路报告首行；违反者该窗口证据作废重跑。
- 只读审计：不改产品代码、不打 tag、不发版。探针测试文件带域标后缀（`r22_<lane>_probe_test.go`），跑后即删；读数用只读 worktree `~/workspace/.r22-tree` @ b823421，探针跑测在快照副本；长跑/录制/测试一律 TMPDIR 指大盘（/tmp 512M tmpfs 满会饿死 Chrome/造伪失败）。

## 测试机面板基线（N-36 前提订正闭环，R22 审前必做已做）

- 任务书前提订正来源：R21 集成报告 §5——运行中 paneld md5 `69d166cc`、served index `edd0e1db` 均为 7d93792 构建；88f10db 的 paneld 为 `153bc896`。本轮开工第一步实查复核：部署前运行态确为 new1（7d93792：容器内 `/app/paneld` md5 `69d166cc…`、served index `edd0e1db53e640c7d3da7bf5db646baa`），与 INT §5 一致；机上 stage/paneld 已是 88f10db（`153bc896…`）但运行容器仍为 new1。
- 已按任务书要求重部署为 b823421 构建（标签 new4；COPY-only，GHCR 1.4.8 底座；部署脚本 `/root/new1/deploy-r22.sh`，日志 `/root/new1/deploy-r22.log`；stage `/root/r22-stage/`）：
  - 后端：本地构建（`.r22-tree/go-backend`，`CGO_ENABLED=0 go build -o paneld ./cmd/paneld`，与 R21 修复 lane 同口径）md5 `1ca4572b454bf6450455f057bcc8bb55`（43,902,880 字节）——本地构建 = 镜像内 `/app/paneld` = 运行容器内 `/app/paneld`，三处逐字一致；镜像 `flmx-backend:new4`（`4ce6f5e43082`）。
  - 前端：本地构建（`.r22-tree/vite-frontend`，`pnpm install` + `pnpm run build`，tsc + vite 全绿，PWA precache 65）dist index md5 `57ab941e7fc6025b52700b2bf36f2241`——与 REL149 new3 参考值逐字一致（b823421 前端与 88f10db/1.4.9 前端同源，fix-r21 无前端改动）；served index md5 与本地 dist 逐字一致，GET / 6366 → 200；镜像 `flmx-frontend:new4`（`ae77aba5aca9`）。
  - 三容器 healthy（backend new4 / frontend new4 / postgres 未用）；`/flow/test` 6365 → 200。
  - agent：`27787cbf52e9390685e75399ff9da821`，active；Xray 26.10.21 running；gost.json 0 服务 / 0 limiter；节点 w10-test（id=1）status=1、forwardMode=agent、version `dev (debian/amd64)`（源码构建预期现象）。
  - 计数（登录后实测）：node=1 / tunnel=0 / forward=0 / inbound=1 / subscription=1，与 R21-F03 恢复态 `1/0/0/1/1` 一致，pristine。
- 部署事故（已处理，记入）：首版 new4 前端 GET / → 403——本沙箱 umask 007 使本地 dist 文件权限为 660/770，nginx worker（`nginx` 用户）读不到 `/usr/share/nginx/html/index.html`（`open() failed (13: Permission denied)`）。处置：本地 `chmod -R a+rX dist` 后重新上传、重建 `flmx-frontend:new4`、仅重起 frontend，GET / → 200、served md5 一致。教训：今后 stage 上传前须 chmod a+rX（后端容器以 root 运行不受影响）。
- 版本串口径：`.env` 中 `FLUX_VERSION=1.4.9-new`（REL149 门恢复遗留），`/system/version` 报 currentVersion `1.4.9-new`、latestVersion `1.4.9`、hasUpdate false。该串不作为构建依据——构建以 paneld/served md5 为准；节点 version `dev (debian/amd64)` 为源码构建预期现象。
- VLESS smoke：基线部署未单独跑，由 L1 首腿复核（部署已证 backend healthy + /flow/test 200 + 节点 status=1 + agent/xray 在跑）。
— R22-00-baseline END
