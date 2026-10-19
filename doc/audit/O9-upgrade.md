# 系统升级功能审计报告（O9）

**审计日期**：2026-10-20
**对照手册**：`doc/guide/upgrade.md`
**审计范围**：版本检查、面板升级、节点升级、GitHub 镜像代理、升级失败恢复

## 审计结论

**发现问题：P1 2 个 / P2 4 个 / P3 1 个**

---

## P1 严重

### O9-001：面板自升级无版本比较，允许静默降级

**文件**：`go-backend/internal/http/handler/system_upgrade.go:650` (`systemUpgrade`)

**问题描述**：
`systemUpgrade` handler 接受用户指定的任意版本号，直接下载并升级，**不与当前版本比较**。用户（或被劫持的管理员会话）可指定旧版本号，面板会静默降级到旧版本，无任何警告。

**复现步骤**：
1. 当前面板版本为 1.0.7
2. 调用 `POST /api/v1/system/upgrade`，body `{"version": "1.0.0"}`
3. 面板下载 1.0.0 的 compose 文件并重启，降级完成，无警告

**手册对照**：
手册「指定版本」节说明可指定版本安装，但未说明降级风险。降级可能导致：
- 数据库 schema 回退不兼容（新版本的迁移不可逆）
- 安全修复被回滚

**证据**：
```go
// system_upgrade.go:650 systemUpgrade()
version := strings.TrimSpace(req.Version)
if version == "" {
    version, err = h.resolveSystemUpgradeLatestReleaseByChannel(channel)
    // ...
}
// 此处无版本比较，直接下载
newCompose, err := h.downloadReleaseAsset(version, composeAsset)
```

**建议**：升级前比较 `version` 与 `currentPanelVersion()`，如为降级需二次确认（前端弹窗警告）。

---

### O9-002：升级 helper 失败无自动回滚

**文件**：`go-backend/internal/http/handler/system_upgrade.go:250` (`helperScript`)

**问题描述**：
Go 代码在启动 helper 前备份了 `docker-compose.yml` 和 `.env`（`.upgrade.bak`），但 helper 脚本本身失败时**不自动恢复**：

```bash
log "拉取新镜像..."
if ! docker compose pull backend frontend >> "$LOGFILE" 2>&1; then
  log "错误: 拉取镜像失败"
  exit 1    # ← 直接退出，不恢复备份
fi

log "重启服务（force-recreate）..."
if ! docker compose up -d --force-recreate --remove-orphans backend frontend >> "$LOGFILE" 2>&1; then
  log "错误: 重启服务失败"
  exit 1    # ← 直接退出，不恢复备份
fi
```

**影响**：
- `docker compose pull` 失败时：`docker-compose.yml` 和 `.env` 已被替换为新版本，但镜像未更新，处于不一致状态
- `docker compose up` 失败时：容器可能未启动，面板宕机，需人工介入恢复 `.upgrade.bak`

**复现步骤**：
1. 触发面板升级到新版本
2. 在 helper 执行期间断开外网（`docker compose pull` 失败）
3. 观察：`docker-compose.yml` 已是新版本内容，`.upgrade.bak` 存在但未自动恢复
4. 面板可能无法启动（compose 文件引用了不存在的镜像 tag）

**手册对照**：
手册「面板内升级」节称「约 1 分钟后刷新页面即可」，未说明失败时的恢复流程。

**建议**：helper 脚本在失败时自动 `cp docker-compose.yml.upgrade.bak docker-compose.yml` 并恢复 `.env`。

---

## P2 一般

### O9-003：`validateUpgradeVersion` 未在下载前调用

**文件**：`go-backend/internal/http/handler/system_upgrade.go:650`

**问题描述**：
G-001 修复将 `validateUpgradeVersion` 的字符集收紧为 `[0-9A-Za-z.\-_]`，但该函数仅在 `updateEnvVersion`（下载**之后**）被调用，`systemUpgrade` handler 在下载前未校验版本号。

**影响**：
- 恶意版本号（如超长字符串）在下载阶段才被拒绝，浪费一次 GitHub 请求
- 防御纵深缺失：校验应尽早（fail-fast）

**证据**：
```go
// systemUpgrade() 中无 validateUpgradeVersion 调用
version := strings.TrimSpace(req.Version)
// ...
newCompose, err := h.downloadReleaseAsset(version, composeAsset)  // 直接下载
// ...
if err := exec.updateEnvVersion(envPath, version); err != nil {  // 此处才校验
```

**建议**：在 `systemUpgrade` 解析 `version` 后立即调用 `validateUpgradeVersion`。

---

### O9-004：`HasUpdate` 用字符串相等而非语义版本比较

**文件**：`go-backend/internal/http/handler/system_upgrade.go:581,643`

**问题描述**：
```go
HasUpdate: latest != "" && latest != current,
```

仅做字符串不等比较，非语义版本比较。导致：
- `current="1.0.10"`, `latest="1.0.9"` → `HasUpdate=true`（实际是降级，不应提示更新）
- 无法判断 `latest` 是否真正"更新"

**影响**：用户可能被误导降级。

**建议**：实现语义版本比较（如 `1.0.10` > `1.0.9`），仅当 `latest` 大于 `current` 时 `HasUpdate=true`。

---

### O9-005：节点升级无版本比较，允许降级（L-005）

**文件**：`go-backend/internal/http/handler/upgrade.go:177` (`nodeUpgrade`)、`upgrade.go:239` (`nodeBatchUpgrade`)

**问题描述**：
节点升级接口接受任意版本号，不与节点的当前版本比较。用户可指定旧版本将节点 agent 降级，无警告。

**复现步骤**：
1. 节点 agent 当前版本为 1.0.7
2. 调用 `POST /api/v1/node/upgrade`，body `{"id": 1, "version": "1.0.0"}`
3. 节点降级到 1.0.0，无警告

**手册对照**：
手册「节点 agent 升级」节：「留空版本即使用最新版」。未说明指定旧版本的行为。

**备注**：
- 已有独立的 `nodeRollback` 接口（`upgrade.go:371`）用于回退，`nodeUpgrade` 的降级能力与其重复且更隐蔽
- L-001（批量升级 goroutine）已修复，本问题未修

**建议**：升级前查询节点当前版本，降级时需二次确认。

---

### O9-006：前端升级 UI 无降级警告

**文件**：`vite-frontend/src/pages/config.tsx`（升级相关组件）

**问题描述**：
前端「面板自升级」和「节点升级」UI 在用户选择旧版本时无任何降级警告。与 O9-001、O9-005 联动，用户可能误操作降级。

**建议**：当前端检测到所选版本低于当前版本时，显示红色警告并要求二次确认。

---

## P3 备注

### O9-007：`..` 可通过版本号字符集校验

**文件**：`go-backend/internal/http/handler/system_upgrade.go:178` (`validateUpgradeVersion`)

**问题描述**：
G-001 修复后的字符集 `[0-9A-Za-z.\-_]` 允许 `.`，因此版本号 `..` 可通过校验。构造的下载 URL 为：
```
https://github.com/iiiiiiiii7125-art/flvx-max/releases/download/.../docker-compose-v4.yml
```

**实际影响**：低。`SecureFetch` 会请求该 URL，GitHub 返回 404，fail-closed。但属于校验不严谨。

**建议**：显式拒绝 `..` 和以 `.` 开头/结尾的版本号。

---

## 已验证通过

### ✅ G-001 修复确认
`validateUpgradeVersion` 字符集已收紧为 `[0-9A-Za-z.\-_]`（`system_upgrade.go:178-190`），`/`、控制字符均被拒绝，路径注入已堵住。

### ✅ 下载校验（fail-closed）
`downloadReleaseAsset`（`system_upgrade.go:511-536`）：
- 经 `SecureFetch`（SSRF 防护 + 超时）
- 强制下载 `.sha256` 校验文件，缺失即 fail-closed
- `hmac.Equal` 比较哈希，防时序攻击

### ✅ 备份机制（Go 层）
`systemUpgrade` 在替换前备份 `docker-compose.yml` 和 `.env` 为 `.upgrade.bak`（`system_upgrade.go:347-362`），`replaceCompose`/`updateEnvVersion`/`startHelper` 失败时自动恢复（`system_upgrade.go:700-730`）。

### ✅ GitHub 镜像代理
`gh_mirror.go` 全量检查：
- 白名单 10 仓库（`ghMirrorRepos()`），非白名单拒绝
- 路径段正则 `^[A-Za-z0-9._-]+$`，拒绝 `.`/`..`
- 重定向限制：仅允许 `github.com` 和 `*.githubusercontent.com`，最多 5 跳
- 单文件 300MB（`ContentLength` + `LimitReader` 双重 enforcement）
- 总缓存 2GB（`ghMirrorPrune` 按时间淘汰）
- GitHub 不可达时返回旧缓存，不直接报错（手册一致）
- 手册仓库列表与代码白名单一致（10 个）

### ✅ 节点升级 S-02 防护
`nodeUpgrade`/`nodeBatchUpgrade` 使用 `isValidAgentVersion` 校验版本号格式（`upgrade.go:125`），面板不再发送 `downloadUrl`（agent 侧本地拼 URL），防 SSRF。

### ✅ 并发控制
`systemUpgrade` 使用 `systemUpgradeMu.TryLock()` 防并发升级（`system_upgrade.go:653`），二次调用返回「已有面板升级任务执行中」。

### ✅ L-001 已修复
`nodeBatchUpgrade` 先获取信号量再启动 goroutine（`upgrade.go:285-288`），避免大量 IDs 时内存 spike。

---

## 手册一致性检查

| 手册描述 | 代码实现 | 状态 |
|---------|---------|------|
| 面板内升级：网站配置→面板自升级→检查更新→立即升级 | `systemVersion`/`systemCheckUpdates`/`systemUpgrade` 三接口对应 | ✅ |
| 脚本升级：重新运行安装脚本选 2 | `panel_install.sh`（未审计脚本本身） | — |
| 指定版本安装 | `systemUpgrade` 支持 `version` 参数 | ✅ |
| 节点升级：留空即最新版 | `nodeUpgrade` 的 `version==""` 时查最新 | ✅ |
| 升级失败修复：docker-compose.yml 丢失 | `capability()` 检测并给出修复提示 | ✅ |
| GitHub 镜像：10 仓库白名单 | 代码与手册一致 | ✅ |
| 单文件 300MB、总缓存 2GB | 代码 enforcement 与手册一致 | ✅ |
| GitHub 不可达返回旧副本 | `ghMirrorFetch` 的 `os.Chtimes` 逻辑 | ✅ |

---

## 未能验证

- 实际面板升级流程（需 Docker 环境，无）
- 实际节点升级（需在线节点，无）
- 大文件下载的中断恢复（无真机）
- `panel_install.sh` 脚本本身（手册引用，未审计）
