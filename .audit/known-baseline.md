# known-baseline.md — 已知基线（预先存在的失败/限制，非本轮缺陷）

记录审计开始前就存在的、与本轮修复无关的已知问题。后续轮次遇到同样现象不得重报。

## 已知测试失败（2026-10-19，fix/r5-audit 时记录）

- `TestExternalImportFromSubscriptionURLContract`：在纯净 main（b04a218）同样失败。原因：H-15 SSRF 拦截测试用 localhost URL，沙箱环境问题，与修复无关。
- `TestRemoteSourceContract`：同上，同因。

## 已知测试失败（2026-10-19 R1，-race 下发现，纯净 main@9a2810b 同样失败）

- `TestPeerShareApplyPersistsBeforeCommand`（handler/peer_share_runtime_lifecycle_test.go:151）：`-race -count=3` 下稳定复现 DATA RACE。根因：测试内 `newCleanupAgent` 启动的 WS server，其 `handleNode.func2`（ws/server.go:355）在后台调 `UpdateNodeStatus`（GORM `processor.Execute` 读），与测试主 goroutine 注册 GORM 回调（`callback.Register` → `processor.compile` 写）竞态。测试注释已意识到 teardown 时的竞态但漏了注册时窗口。纯测试脚手架问题，产品代码无竞态（`UpdateNodeStatus` 与回调机制本身正常）。R1 未触碰相关文件（ws/server.go、repository.go UpdateNodeStatus、该测试文件均未改）。修测试脚手架需精确理解 WS 生命周期，风险收益比低，记基线；不报缺陷。
- `TestTunnelUpdateRecoversFromAddressInUseContract`（tests/contract/limiter_sync_failure_contract_test.go:655）：`-race -count=3` 下复现。`expected exit AddService twice, got 4`——重试计数断言对 -race 负载下的时序敏感。隔离 `-count=1` 通过。纯净 main 同样失败。R1 未触碰隧道更新/服务下发路径。记基线；不报缺陷。
- `TestDownloadReleaseAssetUsesGithubProxyWhenEnabled` / `TestDownloadReleaseAssetRejectsOversizedBody`（handler/system_upgrade_test.go:63/103）：`tls: first record does not look like a TLS handshake`——沙箱对 `example.invalid` 的 DNS/透明代理干扰。R0 已记 blocked.md，此处并入基线统一管理。

## 环境限制（本侧，2026-10-19 R0 确认）

- 本侧出站 SSH 被平台阻断，无法连接用户服务器做真实节点/内核验证。
- Go 工具链：本机无预装，R0 从 go.dev 下载安装 1.25.1 到 ~/go。面板机（ip-172-26-0-47）内存不足不可编译，仅本侧/CI 做 Go 验证。
- shellcheck：apt 不可用，R0 从 GitHub release 下载静态二进制 0.11.0。

## 版本/血统约束（历史记忆）

- 版本按序列顺延自动取号；release tag 必须是纯数字（带 v 前缀的 tag 不触发 `[0-9]*` 流水线，造成"有 tag 无 Release"假象）。
- install.sh 1.0.1 起强制校验 .sha256（fail-closed）。
