# U2 审计：面板在线升级前端与接口契约

审计员：U2 ｜ 日期：2026-10-20 CST ｜ 基线：main（1.0.10 已发布后）｜ 方法：代码审查 + 前后端对表 + `npx tsc --noEmit`（只读，未改代码）

## 结论速览

- **P0 0 / P1 0 / P2 3 / P3 5**
- 面板自升级主链路（配置页：加载状态 → 检查更新 → 选版本 → 确认 → 后端执行）在契约层面**通顺**：三个接口路径/方法/字段与后端逐一对表通过，代理配置被检查与下载两条路径正确消费，用户填的 `https://ghfast.top` 归一化后可直接生效。
- 会卡住/误导用户的点不在主链路，而在三处外围：① WebView 下左下角版本是硬编码 "1.0.0"（更新红标常亮）；② 代理开关 UI 与后端默认值不一致（未保存过时 UI 显示"已禁用"、后端实际默认开启并用默认镜像）；③ 全站错误 Toast 只停留 2 秒且长字符串不换行——**这正是用户刚才看不到完整升级报错的直接原因**（面板自升级报错走同一 Toast）。

---

## P2-1 WebView 下当前版本硬编码 "1.0.0"，更新红标常亮

- **证据**：
  - `vite-frontend/src/config/site.ts:9`：`const APP_VERSION = "1.0.0";`（硬编码，与实际版本 1.0.10 脱节，无任何机制随发版更新）
  - `vite-frontend/src/pages/index.tsx:303` 与 `src/pages/profile.tsx:337`：页脚版本取值 `isWebView ? siteConfig.app_version : siteConfig.version`
    - 普通浏览器走 `siteConfig.version` = `import.meta.env.VITE_APP_VERSION`，发版流水线在 `.github/workflows/docker-build.yml:182` 注入真实版本，**正确**
    - WebView（App 内嵌）走 `siteConfig.app_version` = 硬编码 "1.0.0"，**永远错误**
  - `vite-frontend/src/components/version-footer.tsx:74`：`hasVersionUpdate(version, latestVersion)` 用该错误版本比对 → App 内左下角恒显示"有新版本"红标（与用户 05:09 投诉的红标问题同类，当时只处理了 GitHub 残留 release 这一半，代码层面的另一半在此）
- **影响**：仅 WebView 场景；普通浏览器不受影响。分级 P2（持续误导，无功能损害）。

## P2-2 代理开关 UI 状态与后端默认值不一致（未保存过的安装）

- **证据**：
  - 后端 `go-backend/internal/http/handler/upgrade.go:37-38`：`defaultGithubProxyEnabled = true`、`defaultGithubProxyURL = "https://gcode.hostcentral.cc"`
  - 后端 `upgrade.go:89-108`（getGithubProxyConfig）：DB 无 `github_proxy_enabled` 行时保持默认 **enabled=true**；判断为 `enabledCfg.Value != "false"`（只有字面 "false" 才关）
  - 前端 `vite-frontend/src/pages/config.tsx:1193`：开关 `isSelected={configs[item.key] === "true"}`，DB 无该行时 `configs` 里没有这个键 → 开关显示**已禁用**
- **影响**：从未保存过代理设置的面板，UI 显示"加速已禁用"，实际后端在用默认镜像 gcode.hostcentral.cc 下载（节点安装命令、内核、面板升级、节点升级下发全受影响）。管理员对"流量到底走没走第三方镜像"的认知与实际相反。**本用户已显式保存 ghfast.top，不受此条影响**（保存后行存在，UI 与后端一致）。分级 P2。

## P2-3 错误 Toast 2 秒消失 + 长字符串不换行，升级报错看不全（用户实测复现）

- **证据**：
  - `vite-frontend/src/provider.tsx:21-44`：全局 Toaster 配置，`error: { duration: 2000 }`（所有错误提示统一 2 秒），且未给 toast 内容设置 `maxWidth`/`wordBreak`，react-hot-toast 默认样式对无空格长串（URL）不折行，内容横向溢出视口被截断
  - 面板自升级失败同样走此通道：`vite-frontend/src/pages/config.tsx` handleConfirmSystemUpgrade 的 `toast.error(response.msg || "面板升级失败")`；检查更新失败 handleCheckSystemUpgrade 的 `toast.error(...)` 同理
  - 节点升级报错链（用户截图）已核实完整送达前端：后端 `upgrade.go:233` SendCommand 拿到 agent 错误后以 `升级失败: %v` 返回，`vite-frontend/src/pages/node.tsx:1136` `toast.error(res.msg)` 呈现——**数据没丢，是呈现层截断 + 2 秒消失**
- **影响**：所有带长 URL/长堆栈的失败都不可诊断，用户只能截图到一半。分级 P2（可诊断性）。
- 修复方向（仅建议）：error toast 时长提高（如 8-10s）并加 `word-break: break-all; max-width: 90vw`；或升级类错误改在弹窗内/卡片内联展示并提供复制。

## P3-1 升级确认框不回显"将升级到哪个版本"，无更新时也可确认（同版本重装）

- `vite-frontend/src/pages/config.tsx:1945-2018`：确认弹窗只有注意事项与降级警告（O9-006 在 :1966-1985，复核**有效**：选中低版本时红框警告出现），未回显目标版本；版本留空时不显示"将升级到最新版 X"
- "立即升级"按钮启用条件 `canTriggerSystemUpgrade`（:368-372）不要求 hasUpdate；后端 `system_upgrade.go` 对目标==当前（cmp==0）仅打日志、照常执行全量升级。结果是无更新时点确认会白跑一次同版本重装（备份/换 compose/重启俱全）。分级 P3。

## P3-2 升级进行中状态不持久，刷新后无迹可寻

- 前端执行态仅 React state（`system_upgrade.go` 后端有 TryLock 防并发，冲突文案"已有面板升级任务执行中"）；升级触发后页面刷新/重启期间用户无从得知进度，只能等约 1 分钟手动刷新（成功 Toast 文案即如此引导）。后端进程在升级中会被 helper 替换，锁随进程消失；重启后再点一次升级会走同版本重装路径（见 P3-1），不会造成版本错乱。分级 P3（体验）。

## P3-3 页脚更新检测由浏览器直连 api.github.com，与后端代理路径不一致

- `vite-frontend/src/components/version-footer.tsx:58` → `src/utils/version-update.ts:175`：浏览器 `fetch("https://api.github.com/repos/.../releases?per_page=50")` 直连，无代理、无鉴权（60 次/小时/IP 限额），失败静默（badge 不显示）
- 同一台机器上配置页的后端检查走代理（见下"已验证"），两条路径结果可不一致：页脚无红标 ≠ 无更新。分级 P3。

## P3-4 代理地址归一化的小缺口

- `upgrade.go` getGithubProxyConfig：TrimSpace、缺协议头补 `https://`、TrimSuffix 去**一个**尾斜杠。地址带多个尾斜杠（如 `https://ghfast.top//`）会拼出 `https://ghfast.top//https://github.com/...` 双斜杠路径，多数镜像可容忍但不保证；保存时前端对该字段**无任何格式校验**（config.tsx handleSave 仅对隧道质量间隔做校验），填错只能在下载失败时暴露且报错经 P2-3 的 Toast 难以看全。分级 P3。
- 附带说明（非缺陷）：该代理只加速 GitHub HTTP 下载（release 文件 + API），面板自升级后段的 GHCR 镜像 `docker compose pull` 不经此代理；面板机连不上 ghcr.io 时升级会在 helper 阶段失败，与加速设置无关，UI 对此无任何提示。

## P3-5 死代码：buildGithubDownloadURL 无调用方

- `go-backend/internal/http/handler/upgrade.go:111` `buildGithubDownloadURL` 全仓无调用（面板自升级实际用 `system_upgrade.go:463` 的 `buildSystemUpgradeDownloadURL`）。不影响功能，易误导后续维护。分级 P3。

---

## 已验证通过（契约对表）

| 前端调用 | 后端注册 | 请求 | 响应字段对表 |
|---|---|---|---|
| `getSystemUpgradeVersion` POST `/system/version`（api/index.ts:323） | handler.go:204 `systemVersion` | 无体 | currentVersion/latestVersion/hasUpdate/channel/capability{capable,reasons,deployDir,backendContainer} 与 `types.ts:540-547` **逐字一致**；后端额外字段 pendingMigrations/adminBootstrapRequired 前端未声明，无害 |
| `checkSystemUpgrade` POST `/system/check-updates`（api/index.ts:326） | handler.go:205 `systemCheckUpdates` | `{channel}` ↔ `systemUpgradeRequest{version,channel}` | +releases[]{version,name,publishedAt,prerelease,channel} 与 `types.ts:211-217` **一致** |
| `runSystemUpgrade` POST `/system/upgrade`（api/index.ts:331，超时 60s） | handler.go:206 `systemUpgrade` | `{version:"",channel}` | SystemUpgradeRunApiData{version,channel,composeAsset,helperContainer,backendImageId,message} 与后端 systemUpgradeRunData **一致** |
| `upgradeNode` POST `/node/upgrade`（超时 5min ↔ 后端 SendCommand upgradeTimeout=5min，upgrade.go:31） | — | {id,version,channel} | 1.0.10 新增的 proxyUrl 是面板→agent 内部下发，前端不感知，**正常** |

- **当前版本来源（配置页）**：后端 `currentPanelVersion()` 读 FLUX_VERSION 环境变量（system_upgrade.go:162），compose 以 `${FLUX_VERSION:-dev}` 注入（docker-compose-v4.yml:19），升级时由 updateEnvVersion 改写 .env——**权威且与实际运行版本一致**，与页脚的前端常量是两套来源（见 P2-1/P3-3 的不一致风险）。
- **代理消费点全核对**（键名均为 `github_proxy_enabled`/`github_proxy_url`，与前端字段 config.tsx:223/229 **一致**）：面板检查更新 `fetchSystemUpgradeReleases`（system_upgrade.go:479 前缀拼接）、面板下载资产 `buildSystemUpgradeDownloadURL`（:463）、节点安装命令（mutations.go:709）、内核下载（kernel.go:1231）、节点升级下发 proxyUrl（upgrade.go nodeUpgrade/nodeBatchUpgrade，1.0.10）。用户值 `https://ghfast.top`（有协议头、无尾斜杠）归一化后即 `<proxy>/https://github.com/...` 形式，可直接生效。
- **O9-006 降级警告复核**：config.tsx:1966-1985 在位且逻辑正确（compareVersions(选定, 当前) < 0 时显示）；后端 O9-001 同步拒绝降级，前后端双保险。注意警告不禁用确认按钮，最终靠后端拒绝 + Toast 报错（受 P2-3 影响可读性）——可接受。
- **防重复**：确认按钮执行中 disabled、弹窗执行中不可关闭（config.tsx:1949-1953、2004-2012），后端 TryLock 兜底。
- **渠道**：前端 `getUpdateReleaseChannel()` 写死 stable（version-update.ts:47-48，注释明示本 fork 只发稳定版），后端 systemVersion 同样写死 stable，check-updates 按请求渠道过滤——三方一致，无 dev 渠道误导入口（弹窗文案里"已选择正确的更新通道"一句与无渠道选择器的现状略有出入，纯文案，归 P3-1 附带）。
- **前端构建**：`npx tsc --noEmit` **通过**（exit 0，无输出）。

## 未验证

- ghfast.top 对 `api.github.com` 路径与 release 资产的实际代理可用性（沙箱网络无法代表性验证）：建议用户在面板点一次「检查更新」——能列出版本列表即 API 代理通，能完成一次面板升级即资产代理通。
- WebView 场景为代码推断（isWebViewFunc 分支），无真机 App 验证。

## 给用户的操作路径结论

普通浏览器下：系统设置 → 面板自升级 →（当前/最新版本由后端给出，可信）→ 检查更新 → 选版本（或留空）→ 立即升级 → 确认 → 等约 1 分钟刷新，**整条路径可走通**；两个注意点：升级报错请第一时间截图（2 秒就消失，见 P2-3）；App 内嵌页面的左下角红标不要信（见 P2-1），以配置页显示的后端版本为准。
