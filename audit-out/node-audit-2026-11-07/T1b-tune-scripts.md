# T1b 调优换代（安装脚本面收口）报告

- 分支：`tune-vpsing-scripts` @6f64f43a，自 main 尖端 a8f2e26c 出（与 T1 同基线；13d6dd85 之上一笔为脚本修复，避开它会与本路同文件冲突）。
- 范围：`install.sh`（agent unit 模板＋卸载宿主清理）与脚本套件 `test-install-scripts-proxy.sh`。改动 2 文件，+199 / −36。

## 改动点

### 1. agent systemd unit 补 LimitNOFILE（T1 点名偏差 2）

`write_flmx_agent_service` 的 systemd 模板在 `LimitCORE=0` 后新增 `LimitNOFILE=1048576`，附注释点明：调优面③ 写的 `/etc/security/limits.d`（nofile 1048576）只作用于 PAM 登录会话、对已注册的 systemd 服务无效，agent 的句柄上限必须由 unit 本体承载；两路并存、互不替代。

有意例外：`write_legacy_flux_agent_service`（改名迁移回滚时复原旧 unit 的模板）**未**加 LimitNOFILE——其职责是复原迁移前 unit 的原貌、旧 unit 本无此项；模板头注已同步注明这一例外，防后人按「同模板」误判为漏改。OpenRC 模板无 unit 概念，本项不适用（且 limits.d 对 OpenRC 拉起的服务同样不自动生效，存量 OpenRC 节点需另行处理，本路未动）。

### 2. 卸载宿主清理换新足迹全集（T1 点名偏差 3）

`FLMX_TUNING_*` 常量集重构为两代分组（变量化纪律不变，测试夹具可改指）：

- 第二代：`FLMX_TUNING_KERNEL_CONF`（/etc/sysctl.d/99-network-performance.conf）、`FLMX_TUNING_LIMITS_CONF`（/etc/security/limits.d/99-network-performance.conf）、`FLMX_TUNING_BBR_CONF`（/etc/sysctl.d/10-bbr.conf）、`FLMX_TUNING_GAI_CONF`＋`FLMX_TUNING_GAI_BACKUP`（/etc/gai.conf(.bak)）、`FLMX_TUNING_GAI_PRECEDENCE_LINE`、`FLMX_TUNING_RPS_UNIT(_FILE)`（flmx-tuning-rps.service）、`FLMX_TUNING_RPS_SCRIPT`（/usr/local/bin/flmx-tuning-rps.sh）。
- 第一代兼容：原五常量（99-flmx conf、99-flvx legacy conf、flmx-bbr modules-load、qdisc unit）原样保留。

`uninstall_flmx_agent_host_cleanup` 第 3)～7) 步与 agent 侧 `cleanupHostTuningFiles` 逐项对齐（同序）：

| 步 | 动作 |
|---|---|
| 3 | RPS 与 qdisc 两 unit 先 `disable --now` 再删文件，两件处理完统一 `daemon-reload` 一次（旧实现 qdisc 一件单独 reload） |
| 4 | MSS 钳制规则：`iptables -t mangle -C POSTROUTING -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu` 探在位才 `-D`（参数与 agent 侧 `tuneMSSRuleArgs` 逐字一致）；删除失败只点名、不计 failures（与 agent `tuneMSSClampRemove` 尽力而为同口径） |
| 5 | gai.conf 兜底还原：备份在位且 IPv4 优先行在位时先按备份逐字还原本体、再删备份；备份无论还原与否都在本步删除；gai.conf 本体只还原不删除 |
| 6 | 文件集删除：二代三 conf＋RPS 助手脚本＋一代两件（99-flmx conf、flmx-bbr 模块） |
| 7 | legacy 99-flvx conf：归属守卫口径原样不变（有旧式 gost 足迹跳过并披露） |
| 8 | nft 三表（原第 5 步，内容未动） |

第 1)、2) 步（一代整形队列回放、tuning-backup.json 原值写回）保留：二代面板路径已不执行，但备份与整形脚本都在即将删除的安装目录里，脚本卸载是其最后恢复机会；产物不存在时为空转。函数头注（R6-02）已按新面板路径与新步序改写。

## 与 T1 契约的核对

- 足迹全集与 agent 侧 `hostTuningFileSet()`（tune-vpsing-agent @d7df4548 的 uninstall.go）逐项比对一致：二代 7 项（3 conf＋gai 备份＋RPS unit＋RPS 脚本，gai 本体另计还原）＋一代 3 项＋legacy 守卫 1 项＋非文件项 MSS 规则。无出入。
- MSS 规则参数与 `tuneMSSRuleArgs` 逐字一致（-t mangle / POSTROUTING / -p tcp / --tcp-flags SYN,RST SYN / -j TCPMSS / --clamp-mss-to-pmtu）。
- gai 判定口径：脚本以 `grep -qE "^<precedence line>[[:space:]]*$"` 近似 agent 的逐行 TrimRight 精确比对（正则中 `.` 为通配，实际 gai.conf 场景无误判风险），报告在此点名此近似。

## 存量生效条件（LimitNOFILE，必答项）

unit 只在以下路径被重写，LimitNOFILE 仅随这些路径落到存量节点：

- install.sh 全新安装与重装（`install_flmx_agent` 无条件调用 `write_flmx_agent_service`）；
- install.sh 改名迁移（存量旧布局 → 新布局时重写新 unit）。

**不刷新 unit 的路径**：面板驱动的 agent 自升级、节点面板的升级单/批量、脚本菜单「更新」在新布局下的一路——三者都只替换二进制并重启服务，不重写 unit 文件。因此存量节点在常规升级后 unit 仍无 LimitNOFILE，agent 进程 rlimit 不变；生效需运维择一：重跑新版 install.sh 走重装/迁移，或人工给 unit 加 `LimitNOFILE=1048576`（或 drop-in）后 `daemon-reload` 并重启服务。本路未假装全覆盖——面板侧若要批量收口，需另立「unit 刷新」下发面，不在本路范围。

## 测试结果

- `test-install-scripts-proxy.sh` 全量通过（`install script tests passed`）：R6-02 主用例扩入二代全集夹具与断言（三 conf/RPS unit 先 disable 再删/RPS 脚本/gai 按备份还原且备份删除/MSS 先 -C 后 -D）；幂等用例扩入二代 kernel conf 与「gai 备份在位但优先行不在位」形态（备份删除、本体不动、二跑无 ⚠️）；C4-F02 与队列备份丢失两用例补齐新变量的临时目录改指，保持夹具密封；新增 `test_agent_service_unit_carries_limit_nofile` 钉模板含 LimitNOFILE=1048576 且保留 LimitCORE=0。
- `test-install-scripts-alpine.sh`：本机无 docker、无法按其要求在一次性 Alpine 容器运行；该套件内容为 OpenRC 安装生命周期，与调优足迹零交集（全文 grep tuning 零命中），仅做 `sh -n` 语法校验通过。此项未真跑，点名披露。
- `scripts/check-script-version-bump.sh`：PASS（install.sh 无 SCRIPT_VERSION 印记，该门核 panel_install.sh，不受影响）。
- `scripts/check-doc-consistency.sh`：FAIL，但与基线 main（a8f2e26c 干净 worktree）逐项同形——失败全为环境性（vite-frontend/node_modules 缺失致 typescript 无法解析＋超时预算登记表存量不一致），非本路引入。
- 整块脚本纪律未破：install.sh 仍为单文件整块交付形态，`bash -n` 通过，未触碰 REPO/版本/PINNED_VERSION 行。
