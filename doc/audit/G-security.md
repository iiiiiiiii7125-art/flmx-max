# 安全审计报告（G 域）

## 概要
- 审查范围：注入 / 认证会话 / SSRF / 越权 / 敏感信息
- 发现问题数：**P0 0 个 / P1 1 个 / P2 2 个 / P3 3 个**
- 审查方式：代码审查 + 实际运行测试验证（JWT 行为测试、HMAC 测试、路由-鉴权交叉脚本、middleware 测试套件）
- 审计员：安全审计子代理，2026-10-20

## P1 严重

### G-001 自升级 `version` 参数校验不足（路径注入）
- **文件**：`go-backend/internal/http/handler/system_upgrade.go:178-188`（`validateUpgradeVersion`），调用点 `:671`（`req.Version`）、`:510`（`buildSystemUpgradeDownloadURL`）、`:712`（`updateEnvVersion`）
- **现象**：`validateUpgradeVersion` 仅拒绝控制字符（`<0x20`、`0x7f`），未拒绝 `/`、`.`、`..` 等路径字符。用户提交的 `version` 直接拼入下载 URL：
  ```go
  base := fmt.Sprintf("%s/%s/releases/download/%s/%s", ..., version, filename)
  ```
  构造 `version = "1.0.0/../../x"` 可改变 URL 路径结构。
- **复现步骤**：
  1. 以管理员身份 POST `/api/v1/system/upgrade`，body `{"version": "1.0.0/../../../etc", "channel": "stable"}`
  2. 后端构造的下载 URL 路径被改写（实际请求仍命中 github.com，故只会 404，不会 SSRF 到他站）
- **期望**：version 应匹配 `^[0-9][0-9A-Za-z.\-]*$`（与工作流的版本号校验一致，见 `docker-build.yml` 的 `^[0-9][0-9A-Za-z.-]*$`）
- **实际**：仅校验控制字符
- **影响范围**：需管理员权限方可触发；实际危害限于 URL 路径改写导致下载失败（fail-closed），以及 `.env` 中 `FLUX_VERSION` 被写入畸形值（不执行，仅影响 compose 解析）
- **修复方向**：`validateUpgradeVersion` 改为正则 `^[0-9A-Za-z.\-]+$`，拒绝 `/`；或在 `buildSystemUpgradeDownloadURL` 中对 version 做 `url.PathEscape`
- **严重度说明**：P1 而非 P0，因为需管理员权限、SecureFetch 锁定目标 host、无实际 SSRF/RCE。

## P2 一般

### G-002 联邦 `AllowedIPs` 的 XFF 信任链（残余）
- **文件**：`go-backend/internal/http/handler/federation.go:1762-1800`（`federationClientIP`）
- **现象**：注释明确说明信任模型（仅 RemoteAddr 落在可信代理范围时才解析 XFF，从右按 `FLVX_TRUSTED_PROXY_HOPS` 取），逻辑正确。但 `isPeerIPAllowed` 的 allowlist 解析未在本轮实测（需真实联邦对端）。
- **影响范围**：联邦 peer 白名单绕过（历史 FGH-007 已修复，本轮代码审查确认逻辑正确，未发现新问题）
- **修复方向**：无代码修改；建议补充联邦 IP 白名单的集成测试
- **分级说明**：P2（防御纵深建议，非可利用漏洞）

### G-003 订阅 `user=` 参数仅绑定 userID，未绑定其他可变参数
- **文件**：`go-backend/internal/http/handler/user_inbound.go:19-29`（`subURLSig`/`verifySubURLSig`）、`kernel_relay.go:540-548`
- **现象**：HMAC 仅覆盖 `flvx-subsig-v1:<userID>`。`?relay=`、`?format=`、`?inbound=` 参数不在签名内。攻击者持有合法订阅链接时，可任意改 `relay` 参数枚举该用户有权限的所有中转订阅（但不能越权到无权限的中转——后端仍会按 `user_inbound` 表二次过滤）。
- **复现步骤**：
  1. 获取用户 A 的合法订阅链接（含有效 `user=2&sig=xxx`）
  2. 修改 `relay=2` 为 `relay=3`（用户 A 有权限的中转）
  3. 可获取用户 A 在中转 3 下的订阅（仍是用户 A 自己的数据）
- **期望**：签名绑定全部过滤参数，或文档明确说明
- **实际**：仅绑定 userID；越权被后端二次过滤拦截（`ListUserInboundAssignments`），故无 IDOR，但存在"参数可变性"
- **影响范围**：信息泄露仅限用户自己的已授权数据，无跨用户越权（已用代码路径确认：`kernel_relay.go:543-585` 的二次过滤）
- **修复方向**：将 `relay`/`inbound` 纳入 HMAC，或保持现状并在注释中说明威胁模型
- **分级说明**：P2（设计权衡，非漏洞）

## P3 体验/备注

### G-004 CORS `*` 与 WebSocket `CheckOrigin: true`
- **文件**：`go-backend/internal/http/middleware/cors.go:6`、`go-backend/internal/ws/server.go:162`
- **说明**：已在项目设计基线中明确为"设计权衡"（面板需被任意域名反代访问），非新发现。JWT 认证不依赖 Cookie，故 CORS `*` 不导致 CSRF 劫持会话。记录备查。

### G-005 JWT 无 `iat` 未来时间校验
- **文件**：`go-backend/internal/auth/jwt.go:84-108`（`ParseClaims`）
- **现象**：仅校验 `exp`，不校验 `iat` 是否为未来时间。攻击者无法伪造签名，故无法利用；仅为健壮性备注。
- **实测**：有效/篡改/过期/错误密钥/alg=none 混淆/空 token 六种场景全部按预期拒绝（自写 `TestAuditGJWTBehavior` 通过，已清理）

### G-006 `/api/v1/config/get` 的 `allowsOptionalAuth`
- **文件**：`go-backend/internal/http/middleware/auth.go:117-119`
- **现象**：`/api/v1/config/get` 允许无 token 访问，但 handler 内对非公开 key 要求登录（`handler.go:531-534`），敏感 key 要求管理员（`:527-530`）。逻辑正确，记录备查。

## 已验证通过的安全机制（实测）

| # | 机制 | 证据 |
|---|---|---|
| 1 | SQL 注入 | 全部 `db.Raw` 为静态 SQL 或 `?` 参数化；`Order()` 全为静态字符串；无字符串拼接 Where。抽查 `repository.go:409/3204/3306/3423/4044/4057`、`migration_r5b4_v1.go:114` |
| 2 | 命令注入 | `system_upgrade.go` 的 `exec.CommandContext` 参数均为固定值或经 `validateBackendContainerName`（`^[A-Za-z0-9_.-]+$`）校验；`buildHelperRunArgs` 的 imageID 来自 docker inspect 输出 |
| 3 | 路径穿越 | `gh_mirror.go`：host 白名单 + `ghMirrorSegment` 正则 + 显式拒绝 `.`/`..` + 缓存路径为 URL 哈希；`tuning.go` 备份路径为硬编码常量 |
| 4 | JWT | 自研 HMAC-SHA256：`hmac.Equal` 常量时间比较；签名覆盖 header（alg 混淆不可能）；过期校验；7 天有效期；`PwdChangeRequired` 受限 token 仅放行改密接口。六场景实测通过 |
| 5 | 登录限流 | R5-B2 A-001 已修复：XFF 从右按 `FLVX_TRUSTED_PROXY_HOPS` 解析，仅可信代理场景采信；另有按用户名独立限流（30/10min + 指数退避） |
| 6 | SSRF | `SecureFetch`：scheme 白名单、私网默认拒绝、元数据地址硬黑名单（169.254.169.254、fd00:ec2::254，含 IPv4-mapped 展开）、公网强制 https、逐 IP 判定、重定向校验；订阅导入、系统升级下载均经此通道 |
| 7 | 订阅 IDOR | `verifySubURLSig` 为 `subtle.ConstantTimeCompare`；`TestSubURLSigRoundTrip/RejectsForged/DifferentSecrets` 通过 |
| 8 | 越权 | 182 条路由与 middleware `requiresAdmin`/`shouldSkip` 交叉验证：`/api/v1/forward/*` 为按隧道授权的 operator 设计（`resolveForwardAccess`→`ensureForwardAccessByActor`），非遗漏；联邦接口走 peer token 鉴权（`authPeer` + 命令白名单） |
| 9 | 敏感信息 | 日志无密钥/token（抽查）；`IsSensitiveConfigKey` 覆盖 jwt_secret 等 9 项；`getConfigByName`/`getConfigs` 过滤；前端 `SENSITIVE_CONFIG_KEYS` 从 localStorage 清除 |
| 10 | 流量上报 | `/flow/upload` 凭节点 secret（AES）鉴权；H-17 负值丢弃（`flow_upload_batch.go:43`）；seq 幂等防重放 |
| 11 | Passkey | 标准 webauthn 库；origin 严格校验（https 或本地 http）；challenge 2 分钟 TTL |
| 12 | 调优/备份 | 备份路径硬编码，无用户输入参与 |

## 未能验证的项
- 联邦 AllowedIPs 的真实跨机器白名单绕过测试（需两个面板实例）
- 大规模并发下的限流桶内存占用（`loginRateLimitMaxBuckets=10000` 上限存在，逻辑正确）
- PostgreSQL 下的信息 schema 查询（代码路径与 SQLite 一致，参数化）
