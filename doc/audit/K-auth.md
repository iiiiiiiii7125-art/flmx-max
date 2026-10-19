# 审查域 K 报告：认证与会话安全

## 概要
- 审查范围：JWT 签发/验证、登录限流、密码策略、订阅 HMAC 签名、权限模型、前后端鉴权一致性
- 发现问题数：**P0 0 个 / P1 1 个 / P2 5 个 / P3 4 个**
- 实际执行的测试：代码路径走查、bcrypt 时序差推理、`subURLSig` 作用域分析、限流信任模型推演、中间件路径覆盖核对

## P1 严重

### K-001 登录时序用户枚举（bcrypt timing oracle）
- **文件**：`go-backend/internal/http/handler/handler.go:460-470`
- **现象**：错误信息统一为"账号或密码错误"，但响应时序泄露用户名存在性。
  ```go
  user, err := h.repo.GetUserByUsername(req.Username)  // handler.go:460
  if user == nil {
      response.WriteJSON(w, response.ErrDefault("账号或密码错误"))  // 立即返回，无 bcrypt
      return
  }
  passwordMatched, _ := security.VerifyPassword(user.Pwd, req.Password)  // bcrypt DefaultCost ≈ 75-150ms
  if !passwordMatched {
      response.WriteJSON(w, response.ErrDefault("账号或密码错误"))  // bcrypt 之后返回
  }
  ```
  `GetUserByUsername` 对不存在的用户返回 `(nil, nil)`（`repository.go:548-551`），直接跳过 `bcrypt.CompareHashAndPassword`。存在用户走完整 bcrypt（`security/password.go:18`，DefaultCost）。
- **复现步骤**：
  1. `POST /api/v1/user/login` body `{"username":"definitely-not-exist-xyz","password":"x"}`，记录响应时间 T1（约 2-5ms）
  2. `POST /api/v1/user/login` body `{"username":"<真实用户名>","password":"wrong"}`，记录 T2（约 80-150ms）
  3. 多次采样取中位数，T2 - T1 > 50ms 即稳定可区分
- **期望**：两种情况时序不可区分
- **实际**：差值约两个数量级，网络抖动下多采样仍可稳定枚举
- **影响范围**：用户名枚举是暴力破解/撞库的前置步骤；`recordAttempt` 虽对不存在用户名也计数（防行为枚举），但时序侧信道绕过了该防护
- **修复方向**：用户不存在时仍执行一次 dummy bcrypt（如对固定假哈希 `VerifyPassword("$2a$10$......................", req.Password)`），使两条路径时序一致

## P2 一般

### K-002 订阅 HMAC 签名未绑定订阅 token
- **文件**：`go-backend/internal/http/handler/user_inbound.go:19-23`
  ```go
  func (h *Handler) subURLSig(userID int64) string {
      mac := hmac.New(sha256.New, []byte(h.jwtSecret))
      fmt.Fprintf(mac, "flvx-subsig-v1:%d", userID)  // 仅绑定 userID
      return hex.EncodeToString(mac.Sum(nil))[:32]
  }
  ```
- **现象**：签名输入仅为 `flvx-subsig-v1:{userID}`，不包含订阅 token、inbound、relay、format 等任何参数。一个为用户 2 签发的 sig 可用于**任意**订阅 token 的 URL（`?user=2&sig=...`）。
- **影响**：低。订阅 token 本身是 128 位随机（`repository_subscription.go:24-28`，`crypto/rand` 16 字节），不可猜测；sig 的作用是防 IDOR（持有一个 token 的用户不能通过改 `?user=` 越权），该目标仍达成。但若某用户的 sig 泄露（如分享链接被转发），攻击者可将其拼到其**他** token 上复用，扩大了泄露 blast radius。
- **修复方向**：签名输入改为 `flvx-subsig-v1:{token}:{userID}`（token 即 URL 路径中的订阅 token），使 sig 与订阅一一绑定。注意这是破坏性变更：旧链接的 sig 失效，需发版说明。

### K-003 分布式密码喷洒仅部分缓解
- **文件**：`go-backend/internal/http/handler/login_rate_limit.go:357-375`
- **现象**：用户名桶是"信号"而非"门控"（注释明确"永不硬拒"），触发后仅收紧**来源 IP** 阈值至 3/min（`tightenIPRateLimit`）。攻击者使用 N 个出口 IP 可获得约 3N 次/分钟的尝试预算（如 1000 IP → 3000/min）。
- **影响**：中等。单 IP 爆破被有效遏制；大规模 botnet 仍可慢速喷洒。6 位最小密码策略（见 K-007）放大了该风险。
- **修复方向**：用户名桶触发后，对该用户名启用强制验证码（已有 `captchaEnabled` 分支逻辑，需确保默认部署开启验证码）或渐进式延迟。

### K-004 XFF 链长不足时回退到最左条目（fail-open）
- **文件**：`go-backend/internal/http/handler/login_rate_limit.go:101-109`
  ```go
  idx := len(entries) - hops
  if idx < 0 {
      log.Printf("warning: XFF has %d entries ...; using leftmost entry", ...)
      idx = 0  // 取最左 = 最易伪造的条目
  }
  ```
- **现象**：实际 XFF 链长小于 `FLVX_TRUSTED_PROXY_HOPS` 配置时，取最左条目（攻击者完全可控）作为限流 key，仅打一条 warn 日志。攻击者可通过发送短 XFF 链轮换限流桶。
- **影响**：低-中。仅当部署拓扑与跳数配置不符时触发；默认单跳 Caddy/nginx 下，攻击者在可信代理之后无法伪造（代理会重写 XFF）。但多层代理误配时，此回退使 M-04 限流形同虚设。
- **修复方向**：链长不足时回退到 `RemoteAddr`（可信代理 IP，所有经此代理的请求共享桶，fail-closed），并提升日志级别。

### K-005 Passkey 登录无速率限制
- **文件**：`go-backend/internal/http/handler/passkey.go:309-392`（`passkeyLoginFinish`）
- **现象**：密码登录调用 `checkLoginRateLimit`，但 `passkeyLoginFinish` 全程未调用任何限流。`passkeyLoginBegin` 同样无限制。
- **影响**：低。WebAuthn  ceremony 是单次有效、有 TTL 的密码学挑战，无法离线爆破；`Begin` 泛洪仅能造成内存 ceremony 堆积（`putPasskeyCeremony` 疑似有上限，需确认）。不构成凭证爆破风险，但属于防护不一致。
- **修复方向**：在 `passkeyLoginBegin` 加 IP 限流（如 20/min），与密码登录对齐。

### K-006 JWT 7 天有效期、无刷新、无吊销（除改密/轮换）
- **文件**：`go-backend/internal/auth/jwt.go:14`（`expireTime = 7 * 24 * time.Hour`）
- **现象**：token 有效期 7 天；吊销手段仅有：改密（`PasswordChangedAt` 使旧 token 失效，`middleware/auth.go:71`）、轮换 JWT 密钥（全量失效）。无 refresh token 机制，无主动登出吊销（`logout.ts` 仅前端清 localStorage）。
- **影响**：低。7 天是可用性与安全的折中；token 存 localStorage（设计权衡，见设计基线），XSS 即 token 失窃，失窃后最长 7 天有效。用户已知悉该权衡。
- **修复方向**：如需加强，引入短期 access token + refresh token，或服务端 token 黑名单。当前可接受，建议文档中明确说明。

## P3 体验/备注

### K-007 密码策略仅 6 位最小长度
- **文件**：`go-backend/internal/http/handler/handler.go:1449`（`len([]rune(req.NewPassword)) < 6`）
- **现象**：无复杂度要求（大小写/数字/符号），无常见弱密码黑名单。
- **备注**：产品决策范畴；结合 K-003，6 位纯数字密码在分布式喷洒下风险较高。建议至少 8 位或启用验证码。

### K-008 限流降级分支用带端口的 RemoteAddr 做 key
- **文件**：`go-backend/internal/http/handler/login_rate_limit.go:307-320`
  ```go
  } else {
      key = r.RemoteAddr  // 含端口！
  }
  ```
- **现象**：`loginClientIP` 与 `parseIPLiteral` 双双返回 nil 的极端情况下（RemoteAddr 完全非法），key 带端口，攻击者轮换源端口即换桶。注释声称"防端口轮换绕过"，但此分支恰恰未防。
- **影响**：极低。`parseIPLiteral` 能处理 `host:port` 和裸 IP，走到此分支需 RemoteAddr 完全畸形，实际不可达。属代码卫生问题。

### K-009 JWT 自研实现（非标准库）
- **文件**：`go-backend/internal/auth/jwt.go` 全文件
- **现象**：手写 JWT（header/payload/signature 拼接），未使用 `golang-jwt` 等标准库。
- **审计结论**：经检查无 `alg` 混淆风险（`ParseClaims` 根本不解析 header 的 alg 字段，只验 HMAC-SHA256）；签名比较用 `hmac.Equal`（常量时间）；`exp` 校验存在。实现正确，但自研 crypto 代码是长期维护负担。**不建议此次改动**（能用且正确），记录备查。

### K-010 密钥轮换使所有订阅 sig 同时失效
- **文件**：`user_inbound.go:20`（sig key = `h.jwtSecret`）
- **现象**：`subURLSig` 直接使用 JWT 密钥，轮换密钥后所有已发出的 `?user=&sig=` 订阅链接 403。
- **备注**：用户已知悉（发版通知文案要求）。建议未来将 sig key 与 JWT key 分离（如 `jwt_secret` 文件旁的 `subsig_secret`），降低轮换 blast radius。

## 已验证通过的项

1. **JWT 持久化**（`config/jwt_secret.go`）：`EnsureJWTSecret` 语义正确——环境变量优先 → 文件（0600）→ 生成 32 字节；`JWTEphemeral` 标志与 `FromEnv` 临时生成逻辑兼容；失败回退临时密钥不崩溃。文件权限 0600/目录 0700。
2. **空 HMAC key 防护**（`config.go:29-37`）：`JWTSecret` 为空时 fatal，不会用空 key 签发。
3. **限流信任模型**（R5-B2 重写后）：直连时忽略 XFF；可信代理时从右按跳数解析；`isTrustedProxyIP` 覆盖回环/私网/链路本地；XFF 长度/条目数封顶。M-04 绕过（A-001）已修复。
4. **登录错误信息统一**：用户不存在/密码错误均返回"账号或密码错误"，无枚举（消息层面）；`recordAttempt` 对不存在用户名也计数。
5. **M-05 强制改密**：`PwdChangeRequired` claim 方案正确；中间件仅放行 `/api/v1/user/updatePassword`（`auth.go:82`）；改密后 `PasswordChangedAt` 使旧 token 失效（`auth.go:71` 的 `claims.IatMs <= state.PasswordChanged
...[truncated 1276 chars]