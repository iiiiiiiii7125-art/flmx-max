# blocked.md — 阻塞项

需 PG/真实节点/真实内核/双实例/真机环境的项写入此处，给替代验证并标注，不得标通过。

## R0

- `TestDownloadReleaseAssetUsesGithubProxyWhenEnabled` / `TestDownloadReleaseAssetRejectsOversizedBody`（go-backend/internal/http/handler/system_upgrade_test.go:63/103）：`tls: first record does not look like a TLS handshake`——本沙箱对 `example.invalid` 有 DNS/透明代理干扰，测试期望的"不可达"错误分支走不通。替代验证：GitHub Actions CI（ci-build.yml 后端测试 job，真实 DNS 下 example.invalid NXDOMAIN）。结论：未验证（环境受限），非产品缺陷。
- S4 动态验证（干净临时目录执行安装、重复执行幂等比对、注入失败 404/坏校验、Alpine ash 实跑、`bash -x` 变量展开）：R0 未执行。部分可在本机做（干净目录+桩替换下载、bash -x），Alpine ash 实跑需 docker（待确认本机 docker 可用性）。留待 Round 1（主视角轮换到脚本正确性时或 R1 覆盖推进中执行）。
- PG 后端：本机无 PG。DB 改动验证用 SQLite + 标注 PG 未验证（v6 §8）。
- 真实节点/真实内核/双实例：本侧出站 SSH 被平台阻断，无真机。用 fake agent / fake runner + 金文件，标注替代验证。

## R1

- SCR-006（P3）：`install.sh` 全程无 `set -e/-u/pipefail`。改动面大（整文件错误处理语义变更，回归风险高），留后轮。替代：当前各关键路径已有显式 `|| return 1` 检查（R1 SCR-007 补强了架构/下载链）。
- SCR-010（P3）：测试 source `panel_install.sh` 触发顶层 `self_update_check`，PR+版本号递增时可能 `curl -o "$0"` 覆盖测试脚本自身。需细查 self_update_check 与测试 source 的交互，留后轮。
- SCR-012（P3→产品决策）：`install_docker` 用 curl|sh 无校验。硬改会破坏 `DOCKER_MIRROR=aliyun` 路径；是否加校验/镜像 pin 属产品决策，记 decisions.md。
- SCR-016（P3→接受风险）：compose `POSTGRES_PASSWORD` 弱默认值 `flux_panel_change_me`。install 流程必写随机密码、无 5432 publish，风险低；拟记 accepted-risks.md 书面接受，待 R5 复核确认。
- S4 未验证项（本机限制）：Alpine 容器跑 test-install-scripts-alpine.sh（本机 docker info 不可用）；install_panel 全流程（需真实 docker+compose）；SCR-011 busybox 复现（本机无 busybox，静态已确认）；真实下载/真实服务（按任务要求桩化）。
