# known-baseline.md — 已知基线（预先存在的失败/限制，非本轮缺陷）

记录审计开始前就存在的、与本轮修复无关的已知问题。后续轮次遇到同样现象不得重报。

## 已知测试失败（2026-10-19，fix/r5-audit 时记录）

- `TestExternalImportFromSubscriptionURLContract`：在纯净 main（b04a218）同样失败。原因：H-15 SSRF 拦截测试用 localhost URL，沙箱环境问题，与修复无关。
- `TestRemoteSourceContract`：同上，同因。

## 环境限制（本侧，2026-10-19 R0 确认）

- 本侧出站 SSH 被平台阻断，无法连接用户服务器做真实节点/内核验证。
- Go 工具链：本机无预装，R0 从 go.dev 下载安装 1.25.1 到 ~/go。面板机（ip-172-26-0-47）内存不足不可编译，仅本侧/CI 做 Go 验证。
- shellcheck：apt 不可用，R0 从 GitHub release 下载静态二进制 0.11.0。

## 版本/血统约束（历史记忆）

- 版本按序列顺延自动取号；release tag 必须是纯数字（带 v 前缀的 tag 不触发 `[0-9]*` 流水线，造成"有 tag 无 Release"假象）。
- install.sh 1.0.1 起强制校验 .sha256（fail-closed）。
