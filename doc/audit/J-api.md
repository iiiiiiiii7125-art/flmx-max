# 审查域 J 报告：API 接口全量审计

## 概要
- 审查范围：`go-backend/internal/http/handler/handler.go` 注册的全部 187 个路由
- 鉴权模型：`middleware/auth.go` 的 JWT 中间件（`shouldSkip`/`requiresAdmin`）
- 发现问题数：**P0 0 / P1 1 / P2 3 / P3 5**
- 实际执行的验证：路由表全量导出分类、6 个新增移植路由逐行审查、开放接口滥用面分析、鉴权回归测试运行

## 鉴权模型（基线确认）

| 类别 | 路径 | 说明 |
|---|---|---|
| 完全开放（无鉴权） | `/flow/*` | 节点 agent 上报，用 `secret` 查询参数鉴权 |
| 完全开放 | `/api/v1/open_api/*` | 订阅/ GitHub 镜像，token 或 HMAC 鉴权 |
| 完全开放 | `/api/v1/captcha/*`、`/api/v1/user/login`、passkey 登录、`public/config/get` | 登录前必需 |
| 完全开放 | `/api/v1/federation/connect`、`/api/v1/federation/tunnel/create`、`/api/v1/federation/runtime/*` | 联邦对等，用 `authPeer` 包装器鉴权 |
| 仅管理员 | `/api/v1/node/*`、`/api/v1/kernel/*`、`/api/v1/inbound/*`（除 `sub/info`）、`/api/v1/subscription/*`、`/api/v1/system/*`、`/api/v1/backup/*`、`/api/v1/tunnel/*`（除 user 子路径）、`/api/v1/speed-limit/*`、`/api/v1/group/*`、`/api/v1/federation/share/*`、`/api/v1/monitor/permission/*` | `requiresAdmin` 按路径前缀判定 |
| 登录用户 | 其余（`user-inbound/mine`、`tunnel/user/*`、`monitor/*` 非 permission 路径等） | handler 内二次鉴权 |

`requiresAdmin` 采用前缀匹配而非精确路由表——新增路由只要落在上述前缀下自动受保护。6 个新增移植路由中 5 个落在 `/api/v1/node/*` 下（自动 admin），1 个（gh_mirror）落在 `/api/v1/open_api/` 下（故意开放）。

## P1 严重

### J-001 `/api/v1/user-inbound/list` 与 `/api/v1/user-inbound/set` 依赖 handler 内鉴权而非中间件
- 文件：`go-backend/internal/http/middleware/auth.go:149-230`（`requiresAdmin` 未覆盖 `/api/v1/user-inbound/` 前缀）；`go-backend/internal/http/handler/user_inbound.go:35-71`
- 现象：`getUserInbounds`（返回**任意用户**的全部订阅分配）和 `setUserInbounds`（修改任意用户分配）这两个高敏接口，在中间件层被视为"普通登录用户可访问"，仅靠 handler 内的 `userRoleFromRequest` + `roleID != 0` 检查挡住。
- 风险：纵深防御缺失——一旦某次重构误删 handler 内检查（或新增同类接口时忘记加），即变为任意登录用户可读写全站订阅分配的 IDOR/越权。`requiresAdmin` 的前缀表是主要防线，此处开了缺口。
- 修复方向：在 `requiresAdmin` 中加入 `strings.HasPrefix(path, "/api/v1/user-inbound/")` 且 `path != "/api/v1/user-inbound/mine"`，与 handler 内检查形成双层防护。

## P2 一般

### J-002 GitHub 镜像开放代理可被滥用于带宽/磁盘消耗
- 文件：`go-backend/internal/http/handler/gh_mirror.go:172`（`githubMirror`）；`middleware/auth.go:118`（`shouldSkip` 覆盖 `/api/v1/open_api/`）
- 现象：`/api/v1/open_api/gh/` 无需任何鉴权。虽有 10 仓库白名单、单文件 300MB、总缓存 2GB、单航班去重，但攻击者仍可遍历白名单仓库的大文件（如反复请求不同 tag 的内核二进制）打满 2GB 缓存、消耗面板出站带宽。`ghMirrorLatestTag` 的 HEAD 请求也可被触发。
- 风险：中。属设计权衡（国内节点需经面板拉内核），但无任何限流。
- 修复方向：对该路径加独立 IP 限流（如每 IP 每分钟 20 次），或要求节点 secret 查询参数。

### J-003 `nodeLatencyProbe` 空 IDs 探测全部节点无二次确认
- 文件：`go-backend/internal/http/handler/node_latency.go:512-537`
- 现象：`ids` 为空时探测**全部**节点（注释明示 "all of them when empty"）。每节点 16 并发 × 每点 4 次 ping，百节点下是显著的出站流量与耗时。admin 误点即触发，无确认。
- 修复方向：空 IDs 时拒绝并要求显式传参，或前端加二次确认。

### J-004 `nodeLatencyTargetsSet` 的 `ids` 长度无上限
- 文件：`go-backend/internal/http/handler/node_latency.go:436-510`
- 现象：`req.IDs` 数组长度不受限（body 上限 10MB），`SetNodesLatencyTargets` 逐 ID 更新，大数组可造成慢查询。admin-only，风险低。
- 修复方向：限制 `len(req.IDs) <= 1000`。

## P3 体验/备注

### J-005 `nodeTune` 的 `groups` 参数名未经白名单校验（agent 侧已兜底）
- 文件：`go-backend/internal/http/handler/node_tune.go:33-44`
- 现象：`groups` 任意字符串透传给 agent。agent 侧 `applyTuning`（`go-gost/x/socket/tuning.go:211`）对未知 ID 返回"未知调优项"错误，面板侧将其包装为"调优失败"返回。无安全风险，但错误信息可更友好。
- 状态：agent 侧已有校验，无需修复，仅记录。

### J-006 `/flow/*` 的节点 secret 走 URL 查询参数
- 文件：`go-backend/internal/http/handler/handler.go:1021-1038`
- 现象：`secret` 在 query string 中，会进访问日志、代理日志。属既有设计（agent 上报），不建议改，仅记录。

### J-007 联邦 `runtime/command` 开放路径依赖 `authPeer`
- 文件：`middleware/auth.go:126-133`（`shouldSkip` 放行 `/api/v1/federation/runtime/*`）；`handler.go:350`（`h.authPeer(...)` 包装）
- 现象：中间件放行后由 `authPeer` 做对等鉴权。双层结构正确，但 `shouldSkip` 的放行注释不足，新人易误以为无鉴权。
- 状态：实现正确，仅建议补注释，无需修复。

### J-008 备份导入错误码混用 500/-2
- 文件：`go-backend/internal/http/handler/handler.go:1757-1790`
- 现象：参数错误返回 `Err(500)`，业务失败返回 `Err(-2)`，同一接口混用两种错误码体系。前端若只处理一种会漏提示。
- 修复方向：统一为业务错误码（低优先级）。

### J-009 `openAPISubStore` 的 GET 凭据兼容窗口
- 文件：`go-backend/internal/http/handler/handler.go:699-760`
- 现象：用户名密码仍可走 GET query（已标记 `Deprecation: true` + `Sunset`），凭据进 URL 日志。有独立 IP+用户名限流。属过渡期设计，窗口结束后删除即可。
- 状态：已知过渡设计，无需修复，仅记录。

## 已验证通过

1. **6 个新增移植路由鉴权正确**：
   - `POST /api/v1/node/latency/probe`、`POST /api/v1/node/latency/targets`（`node_latency.go:436,512`）：admin-only（`/api/v1/node/` 前缀），方法/参数校验完整，`decodeJSON` 10MB 上限 + `DisallowUnknownFields`。
   - `POST /api/v1/node/tune`（`node_tune.go:18`）：admin-only，action 白名单（`""`/`status`/`apply`/`restore`），`apply` 要求非空 groups，旧 agent 错误被翻译为中文提示。
   - `POST /api/v1/node/route`、`POST /api/v1/node/route/probe`（`node_route.go:340,345`）：admin-only，`id > 0` 校验，无 pool 时拒绝 probe。
   - `GET /api/v1/open_api/gh/`（`gh_mirror.go:172`）：故意开放；路径穿越被 `ghMirrorSegment` 正则 + `..` 拒绝；重定向被 `ghMirrorCheckRedirect` 限制在 github.com / *.githubusercontent.com（R5R26-001）；单文件 300MB 在 header 与 body 双重 enforcement；缓存原子写入（tmp + rename）；2GB prune。
2. **订阅开放端点**：`/api/v1/open_api/sub/` 的 `?user=`/`sig` 经 `verifySubURLSig` 恒定时间比较（`user_inbound.go:28-33`），无 sig 篡改即 403。
3. **监控端点**：`ensureMonitoringAccess`（`monitoring.go:872`）对非 admin 校验 `HasMonitorPermission`，未越权。
4. **登录限流**：`M-04` 的登录限流与 `subStoreLimiter` 独立桶均在位。
5. **备份导入 100MB 上限**（`handler.go:1765`，R5R22-001）在位。

## 未能验证的项
- 实际 HTTP 发请求验证各接口返回码（无运行中的面板实例；代码审查+回归测试替代）
- 联邦对等鉴权 `authPeer` 的密钥轮换路径（需双面板环境）
