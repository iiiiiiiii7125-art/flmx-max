# blocked.md — 阻塞项

需 PG/真实节点/真实内核/双实例/真机环境的项写入此处，给替代验证并标注，不得标通过。

## R0

- `TestDownloadReleaseAssetUsesGithubProxyWhenEnabled` / `TestDownloadReleaseAssetRejectsOversizedBody`（go-backend/internal/http/handler/system_upgrade_test.go:63/103）：`tls: first record does not look like a TLS handshake`——本沙箱对 `example.invalid` 有 DNS/透明代理干扰，测试期望的"不可达"错误分支走不通。替代验证：GitHub Actions CI（ci-build.yml 后端测试 job，真实 DNS 下 example.invalid NXDOMAIN）。结论：未验证（环境受限），非产品缺陷。
- S4 动态验证（干净临时目录执行安装、重复执行幂等比对、注入失败 404/坏校验、Alpine ash 实跑、`bash -x` 变量展开）：R0 未执行。部分可在本机做（干净目录+桩替换下载、bash -x），Alpine ash 实跑需 docker（待确认本机 docker 可用性）。留待 Round 1（主视角轮换到脚本正确性时或 R1 覆盖推进中执行）。
- PG 后端：本机无 PG。DB 改动验证用 SQLite + 标注 PG 未验证（v6 §8）。
- 真实节点/真实内核/双实例：本侧出站 SSH 被平台阻断，无真机。用 fake agent / fake runner + 金文件，标注替代验证。
