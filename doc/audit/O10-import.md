# O10 导入功能全量审计

- 审计域：导入节点（分享链接 / 订阅内容 / 订阅源 URL / 批量导入 / 去重 / SSRF / 连通性检测）
- 对照手册：`doc/guide/import.md`
- 结论：**P0 0 / P1 0 / P2 3 / P3 6**
- 只审计未修复。

## P2

### IMP-002 手册与代码不符：订阅抓取 UA 不是 Mihomo
- 文件：`go-backend/internal/http/handler/secure_fetch.go:254`、`go-backend/internal/http/handler/kernel_external.go:229`
- 手册称："面板服务器会以 Mihomo 的身份去下载，拿到的通常是参数最完整的 Clash 配置"。
- 实际代码：`req.Header.Set("User-Agent", "flvx-panel/1.0")`。`kernel_external.go:229` 的注释 "The Mihomo user agent makes most providers return a Clash profile" 已过时，代码并不发送 Mihomo UA。
- 影响：依赖 UA 嗅探的订阅源可能返回参数不完整的内容（例如只给 base64 而不给 Clash）。`ParseImport` 能解析多种格式，影响有限，但手册承诺与实现不一致。
- 复现：任意订阅源抓包观察 User-Agent。

### IMP-003 订阅源的 RelayOnly 修改不同步到已有节点
- 文件：`go-backend/internal/http/handler/remote_source.go`（`syncRemoteSource` 更新分支）
- 更新分支只写 `Name/Protocol/Listen/Port/ShareHost/Extra/UpdatedTime`，从不写 `row.RelayOnly`：
  ```go
  row.Name, row.Protocol, row.Listen, row.Port = fresh.Name, fresh.Protocol, fresh.Listen, fresh.Port
  row.ShareHost, row.Extra, row.UpdatedTime = fresh.ShareHost, fresh.Extra, now
  ```
  `fresh.RelayOnly`（来自 `src.RelayOnly`）被丢弃。
- 更严重：`remoteSourceSave` 中 URL 未变时 `fetch=false`，直接返回不同步，RelayOnly/IntervalHours 的修改当时完全不落地到节点。
- 影响：用户在订阅源上勾选"订阅里只保留中转线路"后，已有节点保持旧值，订阅行为与界面显示不一致。需删除重建订阅源才能生效。
- 复现：建订阅源（RelayOnly=关）→ 同步出节点 → 编辑订阅源改 RelayOnly=开（URL 不变）→ 节点 RelayOnly 仍为 0。

### IMP-004 编辑导入节点可绕过去重，产生重复 host:port
- 文件：`go-backend/internal/http/handler/kernel_external.go`（`inboundExternalUpdate`）
- `inboundImport` 建节点时用 `seen[externalKey(host,port)]` 去重（手册："同一个地址和端口不会重复导入"），但 `inboundExternalUpdate` 修改地址后没有任何去重检查。
- 复现：导入 A（1.1.1.1:8388）、B（2.2.2.2:8388）→ 编辑 B 的地址为 1.1.1.1:8388 → 成功，库里出现两个同地址节点。
- 影响：订阅渲染出现重复节点；与手册去重承诺矛盾。

## P3

### IMP-005 `validateSubscriptionURL` 已是死代码
- 文件：`go-backend/internal/http/handler/kernel_external.go:63`
- 该函数（旧 H-15 实现）除测试文件 `kernel_external_h15_test.go` 外无任何生产调用；实际抓取走 `SecureFetch`。留存易误导后人以为有两套校验。

### IMP-006 http 订阅源地址"保存成功、同步必败"
- 文件：`go-backend/internal/http/handler/remote_source.go:262`（`remoteSourceSave`）
- `isSubscriptionURL` 接受 `http://`，保存成功；但 `SecureFetch` 对公网 http 在任何配置下都拒绝（手册亦写明）。用户得到"保存成功"后看到同步失败 `public http not allowed`，体验割裂。应在保存时即提示必须 https（或给出明确文案）。

### IMP-007 公网 http 拒绝信息为英文
- 文件：`go-backend/internal/http/handler/secure_fetch.go`（`validateFetchIP`）
- 中文 UI 下返回 `public http not allowed (use https)`，与其他中文错误文案不一致。

### IMP-008 单次 500 上限未在手册说明，且两处行为不一致
- 文件：`go-backend/internal/http/handler/kernel_external.go:27`（`maxImportNodes = 500`）
- 手动导入超 500 直接拒绝（"一次最多导入 500 个节点"）；订阅源同步超 500 静默截断（记一条 Errors）。手册两处均未提及。

### IMP-009 同步的名称匹配大小写敏感，主机名大小写抖动导致反复"已更新"
- 文件：`go-backend/internal/http/handler/remote_source.go`（`byName` / `sameImported`）
- `byName[row.Name]` 与 `sameImported` 均用原始大小写比较；`externalKey` 虽然 lower 了主机名，但 `sameImported` 比较的是 `row.Listen == node.Host`（未 lower）。服务商某次返回主机名大小写变化 → 每次同步都判为更新，重写行并触发 `retargetRelays` 检查。

### IMP-010 YAML 别名放大的理论风险（低）
- 文件：`go-backend/internal/kernel/proxy_parse.go`（`parseClashProfile`）
- `yaml.v3` 会展开锚个别名；精心构造的多级嵌套别名可放大内存。实测 9 级 doubling 锚点解析仅 0.3ms、无异常（别名解码共享引用，不指数爆炸的简单形状）。且导入接口仅管理员可调用（`/api/v1/inbound/*` 走 `requiresAdmin` 前缀），攻击者即管理员本人；请求体另有 10MB 上限。评 P3，仅建议保持现状并知晓。

## 已验证通过（附证据）

1. **分享链接边界**（实测，`TestProbeImportEdges` 临时测试，已清理）：
   - `ss://...@127.0.0.1:8388` → "第 1 行：地址不能是本机或 0.0.0.0"（`proxy_parse.go` → `ValidateImportHost`，`import.go:27`）
   - `vmess://not-base64!!!` → "第 1 行：vmess 链接格式不正确（只支持 v2rayN 格式）"
   - 无名节点 → 默认名为 `host:port`（`finishProxy`）
   - `wg://` / `ssr://` → "第 1 行：暂不支持 wg:// 链接"，与手册"暂不支持 WireGuard、SSR…逐条提示"一致
2. **去重**：`inboundImport` 用 `externalKey`（`net.JoinHostPort(strings.ToLower(host), port)`）建 `seen`，同地址端口不重复导入，批内重复同样拦截；与手册一致（`kernel_external.go:140-146,170-176`）。
3. **SSRF**：订阅抓取统一走 `SecureFetch`（`fetchSubscriptionInfo`）：scheme 白名单 → 元数据硬黑名单（含 IPv4-mapped 展开）→ 逐 IP 判定（私网需 `FLVX_FETCH_ALLOW_PRIVATE_NETWORKS` / `FLVX_FETCH_PRIVATE_ALLOWLIST`）→ 公网强制 https → 拨号时重解析防 DNS rebinding → 重定向逐跳重校验（`secure_fetch.go` 全文件）；与手册"订阅源地址安全策略"节一致。
4. **连通性检测**：`externalProbeInterval = 3 * time.Minute`（`kernel_probe.go:20`），与手册"每 3 分钟"一致；`externalProbeSkipped` 对 hysteria2/tuic 跳过（`kernel_probe.go:121`），与手册"Hysteria2、TUIC…不显示状态"一致。
5. **订阅源同步**：按名称优先、地址兜底匹配（`syncRemoteSource`）；节点地址/参数变更后调 `retargetRelays` + `applyChainReferrers`，中转自动指向新地址，与手册一致；删源时有中转/被链式引用的节点保留（Kept），与"删除只会删除面板里的记录和相关中转"基本一致。
6. **鉴权**：`/api/v1/inbound/import`、`/api/v1/subscription/source/*` 均在 `/api/v1/inbound/`、`/api/v1/subscription/` 前缀下，受 `requiresAdmin` 保护（J 域已交叉验证）。
7. **DoS 上限**：`decodeJSON` 请求体 10MB（`handler.go:1594`）；订阅体 5MB（`kernel_external.go:30,247`）；`SecureFetch` 50MB（外层先被 5MB 拦截）。
8. **无 XXE**：解析仅涉及 JSON / YAML / base64 / URL，无 XML 解析。

## 未能验证
- 真实订阅源端到端抓取（含重定向、私网 allowlist 生效）——无可用的外部订阅 URL 及受控 DNS，沙箱 DNS 被拦截；
- 前端导入页交互（粘贴/定时同步开关/错误展示）——无浏览器环境。
