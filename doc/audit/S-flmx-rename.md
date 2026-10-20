# S 改名：FLVX → FLMX（含仓库 flvx-max → flmx-max）（2026-10-20，版本 1.0.24）

原则：展示与新数据全用新名；凡硬改会断存量（.env、已发链接、磁盘状态、
内核活规则）的，一律双读/双发/迁移清理，过渡完成面与新名一致。

## 后端（盘点 A：go-backend + go-gost，58 文件命中）

### 直接改
- 展示：app_name 种子与兜底（repository.go:529、kernel_relay.go:653、
  notify.go:118）现行值 FLVX-MAX → FLMX；Passkey RPDisplayName（passkey.go:62）
- 日志前缀 `[flvx]`（go-gost/x/kernel manager/geodata、socket/process）、
  nft runner 的 "FLVX rules" 文案
- User-Agent `flvx-panel/1.0` → `flmx-panel/1.0`（secure_fetch.go:258）
- `FLVX-PING` 探测载荷 → `FLMX-PING`（接收方不校验内容）
- 仓库常量：handler/upgrade.go:26、go-gost/x/socket/websocket_reporter.go:1445
  → iiiiiiiii7125-art/flmx-max（测试 system_upgrade_test.go 同步）

### 双读兼容（新名优先、旧名兜底）
- 环境变量 6 个运行时 + 1 个测试专用：FLMX_TRUSTED_PROXY_HOPS、
  FLMX_FETCH_ALLOW_PRIVATE_NETWORKS、FLMX_FETCH_PRIVATE_ALLOWLIST、
  FLMX_FEDERATION_TRUSTED_PROXIES、FLMX_WEBAUTHN_ORIGIN（后端）；
  FLMX_KERNEL_DIR（agent）；FLVX_POSTGRES_TEST_DSN（CI/测试，直接改名）
- 订阅签名：新增 `flmx-subsig-v3` 生成新链接；校验 v3→v2→v1 三代通吃
  （user_inbound.go），存量链接不断
- 流量上报头：agent 双发 `X-Flmx-Seq` + `X-Flvx-Seq`；面板先读新头回退旧头
- agent seq 文件 `flvx_agent_seq.json` → `flmx_agent_seq.json`，新文件缺失时
  读旧文件迁移（防 seq 重置导致重复计账）
- 内核用户标识：渲染改 `u%d@flmx` / `flmx-u%d`（users.go:17,163），流量
  归属解析双认 flmx/flmx 两种前缀，过渡期新旧配置并存不丢账

### 改名 + 清理 legacy（防残留生效）
- nft 表：面板 `inet flvx` → `inet flmx`、节点 `flvx_kernel` → `flmx_kernel`；
  应用/启动路径显式删除 legacy 表，否则旧表在内核继续生效
- sysctl 调优文件 `/etc/sysctl.d/99-flvx-tuning.conf` → `99-flmx-tuning.conf`，
  写入时顺带删除旧文件（99-flmx 字典序在前，残留旧文件会静默覆盖新值）
- 部署目录探测：`/opt/flmx-panel` 优先、`/opt/flvx-panel` 兜底
  （system_upgrade.go:28，存量面板装在旧目录）
- 升级状态文件 `.flvx_upgrade_status.json` → `.flmx_upgrade_status.json`
  （Go 与生成的 helper 脚本同改；读时回退旧名）
- GH 镜像缓存目录 `flvx-github-cache` → `flmx-github-cache`（旧缓存孤儿无害）
- 升级 helper 临时名 `flvx-upgrade-helper-%d` → `flmx-…`

### 不动
- FLUX_ 族（flux_agent 二进制/服务名、/etc/flux_agent/、FLUX_VERSION）：
  不是 FLVX 字样，且牵动每台节点的 systemd 与安装路径，另行立项
- JWT（无品牌串；密钥文件名 jwt_secret 与品牌无关）

## 前端（盘点 B：vite-frontend，51 处命中 / 21 文件）

### 直接改
- index.html 标题/meta、vite.config.ts 的 PWA manifest、site.ts 站点名默认
  与兜底（现行 FLVX-MAX → FLMX）、version-footer 页脚字样、config.tsx 文案、
  主题元数据（FLVX Team/Community → FLMX Team/Community）、package.json 包名
- 仓库 URL：.env.production:1 与 site.ts:33 → flmx-max（GitHub 重定向兜底）
- 转发导入格式判别符 `"flvx"` → `"flmx"` + UI 标签（判别符从不写入导出
  文件内容，旧文件导入不受影响——盘点 B 已证）

### 本地存储键（无一存凭证：token 键为裸 "token"，站点缓存为 vite_config_）
- 全部 flvx* 键改 flmx*；其中主题两键（`flvx:theme` 三处耦合含 index.html
  内联引导脚本、`flvx:active-theme` 含 context.tsx:123 字面量删除点）加
  一次性回退读旧键，无感迁移；其余键（公告已读、入站/中转标签与分组）
  硬改，后果上限为偏好重置一次
- 内部标识 `data-flvx-theme`（5 处耦合）与 `@keyframes flvx-scanline`
  原子同改

（脚本/工作流/文档部分待盘点 C 回收后补入本文件。）

## 脚本 / 工作流 / 文档（盘点 C + 实施侧全量扫校）

### 直接改
- 仓库地址：install.sh:31、panel_install.sh:11 的 REPO 常量、README 与
  doc 全部链接、VitePress base `/flvx-max/` → `/flmx-max/`；
  工作流中的仓库地址本就动态取 `github.repository`，改名后自动适配
- GHCR 镜像：`flvx-flux-panel-backend` / `flvx-vite-frontend` →
  `flmx-flux-panel-backend` / `flmx-vite-frontend`（docker-build.yml 的
  构建标签、compose 注入、冒烟拉取与根目录两份 compose 同步）；面板升级
  时新 compose 整体替换，镜像切换随之生效，旧镜像留存 GHCR 无害
- compose 容器侧部署路径 `/opt/flvx-panel` → `/opt/flmx-panel`
  （PANEL_DEPLOY_DIR 与挂载目标同改；宿主机目录不变）。panel_install.sh
  的 .env 探测列表双认两个路径
- compose 的 Passkey 环境透传改双名：`FLMX_WEBAUTHN_ORIGIN`（带旧名
  展开兜底）与旧名键并列，避免新装用户按新文档设新名却透不进容器
- 技能包：目录 skills/flvx-api → skills/flmx-api，包名 @flvx/skill-api →
  @flmx/skill-api，publish-skill.yml 路径同步；包内与 doc/ai-skill.md 的
  `FLVX_*` 示例环境变量、~/.flvx 目录约定同步改 FLMX_*/~/.flmx（纯文档）
- doc/package.json 名 flvx-docs → flmx-docs；tests/e2e 的说明文字与包名

### 保留（有意的历史/法律/夹具）
- NOTICE（上游 FLVX 的法律归属声明，非本产品品牌）、doc/audit/* 与
  docs/superpowers/*、plans/*、doc/BUILD_v1.0.0.md、doc/PORT-from-flyx.md
  （均为带日期的历史记录，不回改）
- test-install-scripts-proxy.sh 中 "flvx-panel" 是模拟存量部署项目名的
  夹具（验证升级保留既有项目名），保留原字面

## 实施中抓到的一个真坑（已修 + 测试锁死）

文案批量替换一度把 5 处 `config.GetEnvAny("FLMX_*", "FLVX_*")` 的旧名兜底
参数也改成了新名（双读变单读）——被 health 包以旧名 env 运行的既有测试
当场抓出。已逐处修复，并新增 rename_compat_test.go（旧 env 兜底 + 新名
优先 + Passkey Origin 旧名 + 三代签名通吃）永久防回归。

## 基线新增登记

- tests/contract 的 TestPasskeyDiscoverableLoginAndOwnership 在干净 HEAD
  (559f0b6) 上同样失败（登录限流 429，合同测试间状态串扰），与改名无关，
  2026-10-20 经 worktree 基线对照确认，登记为预存失败。

## C 路交叉核对补遗（2026-10-20 21:40）

C 报告与实施侧自查互核后补修三处：panel_install.sh 自读的
`FLVX_VERIFY_DOWNLOADS` 改双读（`${FLMX_VERIFY_DOWNLOADS:-${FLVX_…:-1}}`，
A 路盘点只覆盖 Go 代码、实施侧 grep 漏检此项）；doc/guide/import.md 的
UA 描述 `flvx-panel/1.0` → `flmx-panel/1.0`；nodes.md 的调优文件名、
postgresql.md 的备份示例路径同步新名。C 报告其余高危项（卷名/容器名/
项目名不动、Pages base、镜像同版切换、WEBAUTHN 三方一致）均已按其
结论落实或本来就未触碰。
