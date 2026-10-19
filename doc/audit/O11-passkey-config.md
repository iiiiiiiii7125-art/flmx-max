# O11 — Passkey 与系统配置功能审计

**审计员**：Passkey/配置专项
**日期**：2026-10-20
**范围**：`go-backend/internal/http/handler/passkey.go`、`repository_passkey.go`、`model/passkey.go`、配置读写（`handler.go:517-600,1107-1210`、`config_access.go`、`repo/config_policy.go`）、前端 `passkey-manager.tsx`、`utils/passkey.ts`、登录页 passkey 入口
**参考**：`doc/guide/passkey.md`
**结论**：P0 0 / P1 0 / **P2 1** / **P3 3**。核心 WebAuthn 流程（challenge 单次性/过期/origin 绑定/可发现登录归属校验/克隆检测）实现正确，3 个合约测试 + 1 个单元测试全部通过。

---

## P2

### O11-P2-1：`passkeyLoginBegin` 无限流，可被洪水填满 challenge 池导致通行证密钥功能 DoS

- **文件**：`go-backend/internal/http/handler/passkey.go`
  - `putPasskeyCeremony`（约 L84–101）：`passkeyPending` 上限 1000，满则返回 `"", false` → 调用方返回"无法创建通行证密钥挑战"
  - `passkeyLoginBegin`（约 L280–305）：公开接口（`middleware/auth.go:126` 在 `shouldSkip` 中放行），**全程未调用 `checkLoginRateLimit`**
- **复现**：
  ```bash
  for i in $(seq 1 1100); do
    curl -s -X POST 'https://panel/api/v1/user/passkey/login/begin' \
      -H 'Content-Type: application/json' -d '{}' > /dev/null
  done
  # 此后合法用户的 begin 请求返回 code != 0（"无法创建通行证密钥挑战"），持续到池内 challenge 过期（TTL 2 分钟）
  ```
- **分析**：池子按 2 分钟 TTL 自然过期 + 每次 `put` 时顺带清扫过期项，因此攻击者需以约 ≥8.3 req/s 持续发送即可长期占满。密码登录不受影响（独立限流 `handler.go:400`），故定为 P2 而非 P1。
- **关联**：K-auth.md 的 K-005 已指出 `passkeyLoginFinish` 无限流；本条是 `begin` 侧的池耗尽向量，两处应一并加限流。
- **建议**：对 `passkeyLoginBegin`/`passkeyLoginFinish` 复用 `checkLoginRateLimit`（或独立的 IP 桶）。

---

## P3

### O11-P3-1：删除通行证密钥不吊销已签发的 JWT 会话

- **文件**：`go-backend/internal/http/handler/passkey.go` → `passkeyDelete`（约 L390–430）
- **现象**：删除密钥后，该用户此前通过任何方式（密码或通行证密钥）登录签发的 token 在过期前（最长 7 天）仍然有效。
- **说明**：与改密不吊销旧 token 的现有设计一致（M-05 用 `PwdChangeRequired` claim 做软控制），属已知权衡；但密钥被盗用的场景下，用户删除密钥时期望"踢掉盗用者会话"，当前行为不符合该直觉。定 P3。

### O11-P3-2：`updateConfigs`（批量）与 `updateSingleConfig`（单个）校验不一致，且批量无事务

- **文件**：`go-backend/internal/http/handler/handler.go:1107-1154` vs `1156-1199`
- **现象 1**：`updateSingleConfig` 拒绝空值（`if value == "" && name != "app_logo" && name != "app_favicon"` → "配置值不能为空"，L1184），但 `updateConfigs` 循环内无此检查，可经批量接口写入空值。
- **现象 2**：批量循环逐个 `UpsertConfig`，中途某个 key 校验失败（`normalizeAndValidateConfigValue`）时，已写入的 key 不回滚 → 配置处于部分更新状态。
- **影响**：仅管理员可调用（中间件 `requiresAdmin`，`auth.go:203`），属管理面健壮性问题。定 P3。
- **建议**：抽取公共校验函数；批量改用 `repo.UpsertConfigs` 并在事务内先全量校验再写入。

### O11-P3-3：`getConfigs` 中变量 `isAdmin` 实际含义是"已认证"，命名误导

- **文件**：`go-backend/internal/http/handler/handler.go:568-572`
  ```go
  claims, isAdmin := ctxClaims.(auth.Claims)
  if !isAdmin || claims.RoleID != 0 {
      cfgMap = repo.FilterSensitiveConfigs(cfgMap)
  }
  ```
- **分析**：`isAdmin` 实为类型断言是否成功（即是否已登录），真正的管理员判断是 `claims.RoleID != 0`。当前逻辑正确（未登录或非管理员都过滤敏感项），但变量名与含义不符，后续维护者极易误读为"已是管理员"而改错分支。定 P3（代码卫生）。

---

## 验证通过（附证据）

### PK-01 challenge 单次性与过期
- `putPasskeyCeremony` 生成 32 字节随机 `sessionId`（`crypto/rand`）；`takePasskeyCeremony`/`takePasskeyLoginCeremony` 先 `delete` 再校验 → 失败也消耗，防重放。
- 过期检查 `time.Now().After(c.session.Expires)`，TTL=2 分钟（`passkeyTTL`）；`webauthn.Config` 的 Login/Registration Timeout 均 `Enforce: true`。
- 单元测试 `TestPasskeyChallengeExpiresAndIsSingleUse` 通过（实测：`go test -run TestPasskey` ✅）。

### PK-02 origin 绑定，fail-closed
- `passkeyConfig()`（`passkey.go:41-72`）：严格解析 `FLVX_WEBAUTHN_ORIGIN`，拒绝带 path/query/userinfo、含空格、非 https（localhost/127.0.0.1 允许 http）；`RPOrigins=[origin]`，`RPID=hostname`。
- ceremony 在 begin 时记录 `origin`，finish 时用**重新解析**的 env origin 比对（`c.origin != origin` → 拒绝）。配置非法时 `wa==nil` → 所有 begin/finish 直接拒绝，且已存 ceremony 的 origin 必然不匹配空字符串 → fail-closed。
- 与手册"未设置或设置错误时拒绝新的绑定和登录"一致。

### PK-03 可发现登录的归属校验（防跨账号冒用）
- `passkeyLoginFinish` 的 `FinishPasskeyLogin` 回调（`passkey.go:310-328`）：用 credential ID 查库得 owner，再比对 `userHandle`（8 字节 big-endian userID）与 owner 的 `WebAuthnID()` 是否一致；注释明确"supplied userHandle cannot select another account"。
- 合约测试 `TestPasskeyDiscoverableLoginAndOwnership` 通过 ✅（含跨账号冒用负例）。

### PK-04 克隆检测与 sign counter CAS
- `credential.Authenticator.CloneWarning` → 拒绝登录（`passkey.go:336`）。
- `UpdatePasskeyCredential` 用 `WHERE credential_json = oldJSON` 做 compare-and-swap（`repository_passkey.go:40-45`），并发断言不会用旧 counter 覆盖。

### PK-05 注册/删除需当前密码 + 仅能操作自己
- `passkeyRegisterBegin`/`passkeyDelete` 均校验 `security.VerifyPassword(user.Pwd, req.Password)`；`userID` 取自 JWT（`userIDFromRequest`），`DeletePasskey(userID, id)` 带 user_id 条件 → 不能删别人的密钥。
- 停用账号（`Status != 1`）在 register/begin/login-finish 三处均被拒绝。

### PK-06 登录后 token 与 M-05 改密闭环
- `passkeyLoginFinish` 复用 `isStoredDefaultPassword` → 默认口令签发受限 token（`GenerateTokenAtWithPwdChangeRequired`），与密码登录一致；返回 `requirePasswordChange` 字段。

### PK-07 公开面最小化
- `shouldSkip` 仅放行 `status`/`login/begin`/`login/finish`（`auth.go:126`）；`register/*`、`list`、`delete` 均需 JWT。`passkeyList` 返回 id/name/createdAt/lastUsedAt，不泄露 `credential_json`。

### PK-08 配置读写权限
- `getPublicConfigByName` 仅允许 `IsPublicConfigKey`（`config_access.go:27`）；`getConfigByName` 对敏感 key 要求 admin（`handler.go:532`），未登录只能读公开 key（`handler.go:537`）；`updateConfigs`/`updateSingleConfig` 在 `requiresAdmin` 中间件（`auth.go:203`）+ handler 内二次检查敏感/系统管理 key（纵深防御）。
- `normalizeAndValidateConfigValue` 对 `app_logo`/`app_favicon` 做 PNG data-URL 前缀 + base64 合法性 + 大小上限校验；监控类 key 做枚举/数值归一化。

### PK-09 配置热更新
- DB 配置均为按请求 `GetConfigByName` 读取（`handler.go` 等 15+ 处），无启动时缓存 → **改配置无需重启**；`updateConfigs`/`updateSingleConfig` 内显式触发 `notifyTunnelQualityConfigChanged`（tunnel-quality 探针）与 `onLatencyConfigChanged`（延迟探测池）热生效。
- **例外**：通行证密钥 origin 取自环境变量 `FLVX_WEBAUTHN_ORIGIN`（`passkeyConfig()` 每次请求重读 env），改值需重建后端容器 —— 与 `doc/guide/passkey.md` 步骤 2 的说明一致，属已文档化的设计。

### PK-10 合约测试
- `go test ./tests/contract/ -run TestPasskey -count=1`：3/3 通过（`TestPasskeyConfigurationFailsClosedAndPasswordLoginStillWorks`、`TestPasskeyRegistrationAndOwnership`、`TestPasskeyDiscoverableLoginAndOwnership`）。

---

## 未能验证（需人工/真机）

1. 真实 authenticator 的完整 WebAuthn ceremony（注册+登录），含 resident key 要求 `ResidentKeyRequirementRequired` 在各类设备上的表现 —— 无浏览器/硬件环境，标注未验证。
2. 前端登录页"选择通行证密钥登录"按钮的实际渲染与 `isSecureContext` 分支 —— 仅代码走读（`pages/index.tsx:282-287`、`utils/passkey.ts` 的 base64url 编解码经核对符合规范：`atob` 对合法无填充输入的 `length%4==2/3` 情况按 WHATWG 规范可正常解码）。
3. 多后端实例部署时的 challenge 共享 —— 手册已声明"只保存在后端内存中…请保持单个后端实例"，属已知限制。

---

## 备注（不计入缺陷）

- `passkeyStatus` 公开返回 `enabled` 布尔值：轻微信息泄露（暴露管理员是否配置了 passkey），但登录页渲染依赖此接口，为功能必需。
- 单用户可注册的密钥数量无上限：每次注册需密码 + 完整 ceremony，自限速，不构成问题。
- 通行证密钥登录绕过 Cloudflare Turnstile：手册明确为设计意图（"不依赖 Cloudflare Turnstile"），且需物理设备签名，不视为漏洞。
