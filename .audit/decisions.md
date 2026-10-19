# decisions.md — 自行决定记录（v6 规范名称）

确需产品决策的写入此处，选保守方案（保持现有行为）继续，不阻塞循环。
替代 v5 的 DECISIONS_NEEDED.md（已删除，避免双源）。

（R0 初始化，待填）

## R1

- SCR-012：`install_docker` 的 curl|sh（官方 get.docker.com 脚本）是否加校验。保守方案：保持现有行为（不硬改），理由：加 pin/校验会破坏 `DOCKER_MIRROR=aliyun` 路径与官方脚本的跟随更新；Docker 官方安装脚本为上游信任链。继续观察，不阻塞。
