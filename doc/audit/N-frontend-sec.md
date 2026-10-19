# 审查域 N 报告：前端安全与逻辑

## 概要
- 审查范围：XSS、敏感信息存储、API 错误处理、路由守卫、订阅签名、表单验证
- 发现问题数：P0 0 个 / P1 1 个 / P2 2 个 / P3 1 个
- 审查方式：静态代码分析（无浏览器环境，页面实际行为标注「需人工验证」）

## P1 严重

### N-001 前端 Token 有效性校验不验证签名，路由守卫可被伪造 Token 绕过
- **文件**：`vite-frontend/src/utils/jwt.ts:73-79`
- **现象**：
  ```ts
  export function isTokenValid(token: string): boolean {
    const payload = getPayloadFromToken(token);
    if (!payload) return false;
    const now = Math.floor(Date.now() / 1000);
    return payload.exp > now;  // 只检查过期时间，不验证签名
  }
  ```
- **复现步骤**：
  1. 在浏览器控制台执行：`localStorage.setItem("token", btoa(JSON.stringify({alg:"none"})) + "." + btoa(JSON.stringify({exp: 9999999999, role_id: 0})) + ".")`
  2. 刷新页面，前端 `isLoggedIn()` 返回 true，`ProtectedRoute` 放行，可看到管理后台 UI
- **期望**：前端应验证 JWT 签名，或至少在路由守卫处调用后端验证接口
- **实际**：仅检查 `exp` 字段，任意伪造的未过期 token 都能通过前端守卫
- **影响范围**：所有 `ProtectedRoute` 包裹的页面。实际危害有限——后端 API 会返回 401，前端 `network.ts` 的 `handleTokenExpired()` 会跳转登录。但用户能短暂看到 UI 框架，可能造成"已登录"的误导。
- **修复方向**：在 `isTokenValid` 中增加签名格式校验（至少检查三段式结构），或路由守卫挂载时调用一次后端 `/api/v1/user/info` 验证。

## P2 一般

### N-002 `isAdmin()` 依赖 localStorage，可被篡改以显示管理 UI
- **文件**：`vite-frontend/src/utils/auth.ts:36-40`、`vite-frontend/src/utils/session.ts:1-7`
- **现象**：`role_id` 和 `admin` 标志存于 localStorage，用户可在控制台任意修改。`config.tsx:1700` 等处用 `isAdmin()` 控制管理功能的显示隐藏。
- **期望**：前端权限判断应以 token 解析为准，而非独立的 localStorage 字段
- **实际**：`isAdmin()` → `getCurrentUserRoleId()` → 从 token 解析 role_id（`jwt.ts`），实际是从 token 取的，不是独立字段。但 `session.ts` 仍写入独立的 `role_id`/`admin` 键，存在混淆风险。
- **影响范围**：低。后端 API 有独立鉴权，前端绕过仅能看到 UI，不能执行操作。
- **修复方向**：移除 `session.ts` 中冗余的 `role_id`/`admin` localStorage 写入，统一从 token 解析。

### N-003 403 错误无全局处理，各页面自行处理不一致
- **文件**：`vite-frontend/src/api/network.ts`（仅处理 401）
- **现象**：API 层 `network.ts` 全局拦截 401 跳转登录，但 403（权限不足）无全局处理，散落在各页面（`monitor.tsx:118` 等 5 处）。
- **期望**：403 应有统一的用户提示（如 Toast "权限不足"）
- **实际**：未处理 403 的页面会静默失败或显示通用错误
- **影响范围**：中。用户越权操作时得不到明确反馈。
- **修复方向**：在 `network.ts` 的 catch 中增加 403 分支，统一 Toast 提示。

## P3 体验

### N-004 前端订阅名长度无限制，后端 100 字截断导致体验不一致
- **文件**：后端 `go-backend/internal/http/handler/subscription.go:496`（`len([]rune(req.Name)) > 100`）；前端 `vite-frontend/src/pages/subscription.tsx` 无对应 `maxLength`
- **现象**：用户输入超长订阅名，前端允许提交，后端返回错误
- **修复方向**：前端 Input 增加 `maxLength={100}`

## 已验证通过

1. **XSS**：全仓无 `dangerouslySetInnerHTML`、`innerHTML` 直接赋值。React 默认转义，节点名/订阅名等用户输入经 JSX 渲染，**无 XSS 风险**。
2. **订阅签名**：`sig` 由后端生成（`my-subscription.tsx:35` 从 `data.sig` 取），前端仅拼接 URL 参数，不参与 HMAC 计算。**无前端伪造风险**。
3. **401 处理**：`network.ts:109-120` 全局拦截 401（HTTP 状态码和业务 code 双通道），调用 `handleTokenExpired()` 跳转登录页。**token 过期后正确跳转**。
4. **敏感信息存储**：localStorage 仅存 `token`（JWT，必需）、UI 偏好（`sidebar_collapsed` 等）。无密码、密钥明文存储。`config.ts` 的 `purgeSensitiveConfigCache()` 主动清理 `jwt_secret` 等敏感配置缓存。**符合预期**。
5. **API 认证头**：`network.ts:95` 统一附加 `Authorization: <原始token>`（无 Bearer 前缀，符合项目约定）。401 时双通道拦截（HTTP 状态码 + 业务 code）并跳转登录。

## 未能验证的项

- **页面实际渲染**：无浏览器环境，LatencyCell/RouteModal 等新组件的实际显示效果、弹窗交互、保存→预览端到端流程均需人工在浏览器确认。
- **路由直接访问**：`ProtectedRoute` 对未登录直接访问 `/node` 等路径的重定向行为需人工验证（代码层面逻辑正确）。
- **大订阅性能**：1000+ 节点时前端渲染性能未测试。
