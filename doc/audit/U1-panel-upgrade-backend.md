# U1 复审：面板在线自升级后端全流程（只审不改）

- 范围：`go-backend/internal/http/handler/system_upgrade.go`（824 行）为主，含 `secure_fetch.go`、`upgrade.go`（代理配置/版本工具）、`middleware/auth.go`、`.github/workflows/docker-build.yml` 资产命名、前端 `vite-frontend/src/api/index.ts` 调用面
- 背景配置：用户已开启 GitHub 加速代理，地址 `https://ghfast.top`（gh-proxy 风格）
- 方法：代码审计 + 常量推导 + 定向跑测（未真实请求外网）
- 总评：**P0 0 / P1 0 / P2 3 / P3 4**。核心下载链路无 agent 侧同类 bug；主要风险在「版本发现 API 经代理」单点、helper 无健康校验、异步阶段并发防护缺失、一组失效的回归测试。

---

## 一、与 agent 侧同类 bug 的排查（重点项，结论：不存在）

agent 侧 bug 的本质是 **CheckRedirect 域名白名单**遗漏 `release-assets.githubusercontent.com`。面板侧下载走的是完全不同的机制：

- `downloadReleaseAsset`（system_upgrade.go:574-599）经 `SecureFetch`（secure_fetch.go:219-280）下载，**没有任何域名白名单**：校验模型是「scheme 白名单 + 逐 IP 分级判定（元数据黑名单/私网 allowlist/公网强制 https）」，且 `CheckRedirect` 对每一跳重跑 `validateFetchURL`（secure_fetch.go:236-249）。
- 推导链：`https://github.com/…/releases/download/<ver>/docker-compose-v4.yml` → 302 → `https://release-assets.githubusercontent.com/…`（公网 IP + https）→ 逐跳校验通过。**直连形态全程可行。**
- 代理形态：`buildSystemUpgradeDownloadURL`（:463-470）拼 `<proxy>/https://github.com/…`，与 gh-proxy 约定一致（与 install.sh 的 `maybe_proxy_url`、gh_mirror 同一模式，有测试形态断言 system_upgrade_test.go:70 的 wantURL）。SecureFetch 先校验代理域名 `ghfast.top`（公网 + https ✓）；gh-proxy 若服务端取回直出 200 则无跳转，若 302 到资产 CDN 则逐跳复检通过；上限 5 跳（secureFetchMaxRedirects）足够。
- 大小/超时匹配：被下载资产只有 compose 文件（数 KB）+ 其 `.sha256`（工作流 `docker-build.yml:404-409` 上传 `docker-compose-v4.yml`/`-v6.yml` 及同名 `.sha256`，与 `selectComposeAsset`（:287-292）的返回值一致）；`secureFetchMaxBody=50MB`、`secureFetchTotalTimeout=60s` 对慢代理绰绰有余；两段下载共用一个 60s ctx（:577），compose+sha 都是小文件，无挤占问题。
- sha256 强校验 fail-closed（:588-598）：校验文件下载失败/为空/不匹配一律中止；工作流生成的 `.sha256` 为纯哈希（`awk '{print $1}'`）或 `hash  filename` 两种形态，代码 `Split(" ")[0]` 均可解析。

---

## 二、已修项复核（逐个在位 ✅）

| 项 | 证据 |
|---|---|
| G-001 version 字符集 | `validateUpgradeVersion`（:218-234）逐字符校验 `[0-9A-Za-z.\-_]`，拼 URL（:510 区）与写 .env（:712 区）之前已收紧；与 `updateEnvVersion` 内二次校验（:378）形成纵深 |
| O9-001 拒绝降级 | systemUpgrade :755-763：`compareVersions(version, currentVer) < 0` 时拒绝并 `log.Printf` 记录；同版本仅记录不拦截 |
| O9-002 helper 回滚 | helperScript（:294-350）`rollback()` 覆盖 pull 失败、up 失败两分支，成功才删 `.bak`；Go 侧 `replaceCompose`/`updateEnvVersion`/`startHelper` 失败均调 `restoreUpgradeBackups`（:803-824） |
| O9-003 下载前校验 | systemUpgrade :745-748 注释明示且代码在位，位于 `downloadReleaseAsset`（:777）之前 |
| O9-004 语义版本比较 | `systemUpgradeVersionResponse`（:644）与 `systemCheckUpdates`（:711 区）两处均为 `compareVersions(latest, current) > 0`，`1.0.10 > 1.0.9` 推导正确 |

其他验证通过项：

- **入口与鉴权**：`/api/v1/system/version|check-updates|upgrade` 注册于 handler.go:204-206；`middleware/auth.go:154` 对 `/api/v1/system/` 前缀统一 `requiresAdmin=true`；写代理配置的 `/api/v1/config/update(-single)` 同样在 requiresAdmin 名单内——无越权改代理/触发升级路径。
- **并发（同步段）**：`systemUpgradeMu.TryLock`（:721），二次请求返回「已有面板升级任务执行中」；`TestSystemUpgradeRejectsConcurrentRequests` 实测通过。
- **currentPanelVersion**：读 `FLUX_VERSION` 环境变量（:162-168），与 compose 资产 `FLUX_VERSION: ${FLUX_VERSION:-dev}`、镜像 tag `${FLUX_VERSION}` 的注入方式一致；缺失时为 `dev`，`compareVersions` 对 dev 的处理（视为最低）使检查更新/升级不被卡死。
- **channel 解析**：`normalizeReleaseChannel` 非 dev 一律 stable；`releaseChannelFromTag` 以 `^\d+(\.\d+)+$` 判 stable、含 alpha/beta/rc 判 dev（upgrade.go:56-76）；纯数字 tag（如 `1.0.10`）归类正确。
- **capability 前置检查**（:241-285）：docker CLI/socket/compose/.env/后端镜像逐项检查且失败原因（含旧挂载丢失的修复指引）随响应返回，升级在 `capability.Capable=false` 时于落盘前拒绝。
- **失败点落盘顺序**：下载 → 备份 compose → 备份 .env → 替换 compose → 改 .env → 起 helper；任一步失败都在「容器被动重启之前」，且已改文件有 restore 兜底。
- **错误信息**（同步段）：均为可定位的中文错误（下载失败带文件名与底层错误、校验失败带期望/实际哈希、能力不足带逐项原因），未发现吞错；与本节 P2/P3 的问题集中在异步段与 API 段。

定向跑测（本沙箱）：`TestSystemUpgrade*`、`TestSelectCompose*`、`TestUpdateEnvVersion*`、`TestBuildHelperRunArgs*`、`TestUpgradeBackup*` 等全部通过；例外见 U1-004。

---

## 三、新发现问题

### U1-001（P2）：版本发现 API 经代理是单点故障，无直连降级

- 证据：`fetchSystemUpgradeReleases`（system_upgrade.go:472-500）在代理开启时把 API 地址也拼上代理前缀：`https://ghfast.top/https://api.github.com/repos/iiiiiiiii7125-art/flvx-max/releases?per_page=50`；全链路无 fallback。
- 触发条件：代理开启 且 该 gh-proxy 实现不转发 `api.github.com`（gh-proxy 类服务对主站/资产域名的支持普遍，对 api 子域的支持因实现而异，无法从代码判定 ghfast.top 的行为）。
- 影响：`/system/version` 返回 Reason 报错、左下角更新徽标失效；`/system/check-updates` 直接 -2 失败，前端版本列表为空、无从选版；无显式 version 的 `/system/upgrade` 同样失败。**资产下载链路本身不受影响**——显式带 version 的升级请求完全绕过 API（:738-746 仅在 version 为空时才调用 resolve），故此项不阻断「已知版本号」的升级，只阻断「发现新版本」。
- 附带差异（与 SecureFetch 对比）：该路径用裸 `http.Client`（:478）+ `systemUpgradeHTTPGet`，无 SSRF 分级校验（URL 由常量+管理员代理配置构成、无用户输入，风险可控）、无 User-Agent（靠 Go 默认 UA，GitHub 接受）、`json.NewDecoder(resp.Body).Decode` 无大小上限、15s 超时经慢代理拉 50 条 release 时可能偏紧（见 U1-006）。

### U1-002（P2）：helper 以「up 命令返回 0」为成功标准，无新版本健康校验，且成功即删备份

- 证据：helperScript（:294-350）：`docker compose up -d --force-recreate` 成功后直接 `log "升级完成"` 并 `rm -f docker-compose.yml.upgrade.bak .env.upgrade.bak`。
- 触发条件：新镜像能被 compose 拉起但进程本身起不来/立崩（镜像内容损坏、DB 迁移失败、与旧 .env 不兼容等）。`up -d` 对「容器已创建」即返回 0，不等待进程健康。
- 影响：面板进入 crash-loop 宕机，自动回滚不触发（O9-002 的 rollback 只覆盖 pull 失败与 up 命令本身失败），且 `.bak` 已被删除，恢复只能靠人工 SSH + 旧镜像 tag 手工处理。同时全程无状态查询接口（见 U1-007），前端只能靠轮询 `/system/version` 猜结果。

### U1-003（P2）：并发防护只覆盖同步段，helper 异步执行期间可二次发起升级

- 证据：`systemUpgradeMu` 在 handler 返回时 `defer Unlock`（:721-725），而 helper 是 `docker run -d` 脱离进程（startHelper :558-572），拉镜像/重建可持续数分钟；系统内无「升级进行中」的持久标记或状态接口。
- 触发条件：管理员在第一次升级的 helper 尚在运行时再次调用 `/system/upgrade`（前端按钮在首次请求返回后即恢复可用）。
- 影响链：第二次调用重新 `backupFile` → **`.bak` 被覆盖为第一次升级已替换后的 compose/.env，原始版本备份被污染**，此后任一侧 rollback 恢复的都是中间态；两个 helper 先后/同时对同一 compose 项目执行 pull/up 互相干扰；helper 名为秒级时间戳（:818），同秒撞名还会让第二次 `docker run` 直接失败（该分支会触发 restore，把第一次的成果回滚掉）。

### U1-004（P2）：downloadReleaseAsset 的两个回归测试自 D16 起失效，断言的 1MB 上限在生产代码中不存在

- 证据：
  - `system_upgrade_test.go:28` `TestDownloadReleaseAssetUsesGithubProxyWhenEnabled` 与 `:75` `TestDownloadReleaseAssetRejectsOversizedBody` 均 stub `systemUpgradeHTTPGet` 并据此断言（前者断言最终 URL 形态，后者断言超 `maxSystemUpgradeComposeAssetBytes`(1MB) 时报「过大」）。
  - 但 `systemUpgradeHTTPGet` 在生产代码中仅被 `fetchSystemUpgradeReleases`（:483）使用；`downloadReleaseAsset` 自提交 `dbeae5a`（R5-B5 D16）起改走 `SecureFetch`，从未调用该 stub；`maxSystemUpgradeComposeAssetBytes` 全仓仅被该测试引用，生产无任何 1MB 检查，「过大」错误串在该路径不存在。
  - 实测（本沙箱）：两测试均 FAIL，表面原因是沙箱 DNS 拦截的 TLS 报错；但即使在真实 DNS 环境，stub 永不命中、gotURL 恒为空、断言同样不可能通过——`.audit/known-baseline.md` 将其归为「TLS 环境」失败并不准确，实为测试与实现脱节。
- 影响：① compose 资产实际上限是 SecureFetch 的 50MB 而非设计意图的 1MB（有 sha256 钉死，实际风险低）；② 升级资产的代理下载路径**没有有效的回归覆盖**，URL 拼接类回归无人拦截。

### U1-005（P3）：带 `v` 前缀的版本号被误判为降级

- 证据：`validateUpgradeVersion`（:218-234）字符集允许字母，故 `v1.0.10` 可通过校验；但 systemUpgrade 的版本比较（:755）未做节点侧已有的 `strings.TrimPrefix(version, "v")` 处理（对照 upgrade.go 的 nodeUpgrade），`compareVersions` 对段 `v1` 的 `Atoi` 失败按 0 计 → `v1.0.10` vs 当前 `1.0.9` 返回 -1 → 回复「目标版本低于当前版本，不允许降级」。
- 触发条件：管理员在自定义版本输入处手填 `v1.0.10`。官方 tag 为纯数字、版本列表给出的也是纯数字，正常流程不触发；且纵使绕过比较，该 tag 在 GitHub 上不存在，下载会 404。仅为错误信息误导 + 与节点侧行为不一致。

### U1-006（P3）：fetchSystemUpgradeReleases 的超时与体积防护偏弱

- 证据：:478 客户端超时 15s（经代理多一跳 + 50 条 release 的 JSON 体积，慢代理下有超时风险，失败表现为「获取版本列表失败」）；:497-499 解码无 `LimitReader`，异常代理可返回超大 body。URL 无用户输入，风险等级低。

### U1-007（P3）：升级进度/结果无可查询通道

- 证据：startHelper 成功后 handler 立即返回 `systemUpgradeRunData`（含一句固定文案），此后无任何状态端点；helper 的 `upgrade.log` 写在宿主机部署目录，前端不可读；后端被重建后进程内状态归零，无法回答「上次升级成功还是失败」。用户排障只能 SSH。这是 U1-002 的伴生项。

### U1-008（P3）：内网/HTTP 代理与 SecureFetch 私网策略冲突且无说明

- 证据：代理地址来自管理员配置（`getGithubProxyConfig`，upgrade.go:88-110，仅补 scheme 前缀、无形态校验）。若用户填内网代理（如 `http://192.168.x.x:8080` 或解析到内网的主机名），资产下载会在 `validateFetchIP`（secure_fetch.go:128-143）以 `private network not allowlisted` 被拒，除非部署时另设 `FLVX_FETCH_ALLOW_PRIVATE_NETWORKS=true`/`FLVX_FETCH_PRIVATE_ALLOWLIST`；UI 文案与报错均未提示这一前置条件。报错文本本身可定位，故仅 P3。用户当前填的 `ghfast.top` 为公网 https，不受影响。

### U1-009（P3）：updateEnvVersion 前缀匹配过宽

- 证据：:390 以 `strings.HasPrefix(line, "FLUX_VERSION=")` 匹配，会连带命中 `FLUX_VERSION_EXTRA=` 等同前缀变量并改写其值。.env 常规内容下不存在此类变量，概率低、无现网影响。

---

## 四、结论：两种配置下能否走通

### 代理关（直连）
可走通，无阻断点：API 直连 `api.github.com`（15s 超时为唯一软肋，U1-006）→ 资产经 SecureFetch 直连 github.com 并逐跳跟到 release-assets（已推导可行）→ sha256 强校验 → 文件备份/替换（失败可恢复）→ helper pull/up。
注意：代码默认值是**代理开**（`defaultGithubProxyEnabled=true`、`defaultGithubProxyURL=https://gcode.hostcentral.cc`，upgrade.go:37-38；且 `getGithubProxyConfig` 语义为配置值 `!= "false"` 即启用），「关」需在设置中显式关闭开关。

### 代理开（当前 ghfast.top）
- **资产下载段：可走通**（URL 形态、SecureFetch 逐跳校验、大小、超时、sha 校验均已逐项推导/验证，无 agent 式域名白名单坑）。
- **版本发现段：取决于 ghfast.top 是否转发 api.github.com（U1-001）**——这是代理开配置下唯一的潜在阻断点，且代码无降级。若其支持（主流 gh-proxy 实现多为全 GitHub 系域名转发），全流程走通；若不支持，现象为检查更新报错/版本列表为空，但用已知版本号发起的升级不受影响。
- 两种配置共有：U1-002（新版本进程级失败无自动回滚、备份已删）、U1-003（异步窗口二次升级）为执行段残余风险，与代理无关。

修复优先级建议：U1-001（API 失败时回退直连或给出明确报错）→ U1-003（落盘进行中标记/锁延伸到 helper 终态）→ U1-002（helper 增加健康探针后再删备份）→ U1-004（重写两个失效测试，恢复代理下载回归覆盖）。
