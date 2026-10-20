# NOTES.md - fix/security-r4 分支

## 已完成

### S-02 (commit 39235fa)
关闭 UpgradeAgent 下载链：
- 面板：`isValidAgentVersion` 严格校验 `^v?[0-9]+\.[0-9]+\.[0-9]+$`，只发送 `version` 参数
- Agent：拒绝面板传入的 `downloadUrl`，用编译期常量 `iiiiiiiii7125-art/flmx-max` 本地拼 URL
- Checksum 强制：拿不到/为空/不匹配 → 删除临时文件并中止，不再降级跳过

测试：
- `TestS02_ValidAgentVersion`：修复前编译失败（函数不存在），修复后通过
- `TestS02_InvalidAgentVersionRejected`：同上

## 变体搜索：面板下发到 Agent 的下载/执行路径

| 路径 | 文件:行号 | 来源 | 校验 | 执行身份 | 可触发者 | 状态 |
|------|-----------|------|------|----------|----------|------|
| UpgradeAgent | upgrade.go:203 | 面板构造 | **已强制** | root | 管理员 | ✅ 已修 |
| 批量升级 | upgrade.go:277 | 面板构造 | **已强制** | root | 管理员 | ✅ 已修 |
| 内核安装 | mutations.go:633 | 面板指定版本 | 无 | root | 管理员 | ⚠️ 需收敛 |
| Xray升级 | kernel/manager | 版本号 | 无 | root | 管理员 | ⚠️ 需收敛 |
| 证书下发 | federation.go | 联邦peer | 无 | root | 联邦peer | ⚠️ 需评估 |
| service hook | service.go:329 | 本地配置 | **已拒绝** | root | 无 | ✅ S-01已修 |

**建议**：内核安装/Xray升级也应采用"版本号+本地拼URL+强制校验"模式，与 S-02 一致。

## 未完成（需继续）

### 任务2: S-04 离线删除
- 需 DB 迁移：forward 表加 `pending_delete` 状态
- 需 API 变更：离线时返回"已排队"而非"成功"
- 需 Agent 重连上报机制

### 任务3: S-05 nft 暂停/恢复
- 需 nftables 规则操作逻辑
- 需 root 环境测试

### 任务4: S-06 隧道换入口
- 需差集计算与清理下发

### 任务5: SSRF/R-001/R-002
- SSRF：需 IP 黑名单 + 重定向校验
- R-001：限流键去端口 + CIDR 信任
- R-002：改密判断逻辑

## 需要人工决策

1. **旧 Agent 引导**：已部署的旧 Agent 仍接受任意 URL。是否在面板加版本检查，拒绝向旧 Agent 下发升级？
2. **签名校验**：用户模板提到 ed25519 签名（任务1e）。是否现在实施，还是留待后续？
3. **内核安装收敛**：是否将 Xray/sing-box 安装也改为"版本号模式"？
