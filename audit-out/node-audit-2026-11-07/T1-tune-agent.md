# T1 调优换代（agent 侧）报告

- 分支：`tune-vpsing-agent`，自 main 出。注：本地 main 尖端为 a8f2e26c（13d6dd85 之上一笔脚本修复，与调优无关），本路建在 main 尖端。
- 范围：`go-gost/x/socket` 调优面全量换代。改动 24 文件，+1466 / −9393（净删约 7.9k 行，大头为 tcpfit 引擎脚本与自动调优/深度整形代码）。

## 新模型（按简报参数表落地，唯一真值）

新 `tuning.go` 整写，四面＋回退：

| 面 | 实现 | 状态判定（与参考脚本菜单同口径） |
|---|---|---|
| apply-ipv4 | gai.conf 备份→启用/追加 `precedence ::ffff:0:0/96  100`；文件缺失时按脚本模板新建；已激活时空转且不覆盖备份 | gai.conf 含未注释的该行 |
| apply-bbr | 写 `/etc/sysctl.d/10-bbr.conf` 两行＋逐键生效 | 当前 cc == bbr |
| apply-kernel | 写 `99-network-performance.conf`（23 键，缓冲区 = MemTotal×5%，按脚本整数步序 ((KB×5)/100)×1024 计算）＋`limits.d` 同名文件（nofile 1048576 / nproc 65535）＋MSS 钳制（iptables 先 -D 去重再 -A，不在时回执注明跳过） | 内核 conf 文件存在 |
| apply-nic | 网卡集按脚本正则排除 lo/docker/veth/br-/any/tung3/sit0/tun/wg；ethtool 环形拉预设最大值（取不到 1024）；rps_cpus 全核掩码、rps_flow_cnt 4096、rps_sock_flow_entries 32768；持久化 = oneshot unit `flmx-tuning-rps.service`＋脚本 `/usr/local/bin/flmx-tuning-rps.sh`（开机重放 RPS 面；环形同脚本一样不持久化） | rps_sock_flow_entries == 32768 |
| rollback | 删三 conf＋RPS unit/脚本＋一代残留三件＋legacy conf；gai 有 .bak 还原、无则注释回；内存值 cc=cubic、qdisc=pfifo_fast、rps_sock_flow_entries=0；删 MSS 规则；rps_cpus 清 0；sysctl --system 重载 | — |

TuneStatus 回报四面布尔＋当前 tcp_congestion_control、default_qdisc、rps_sock_flow_entries＋nofile 概要（agent 进程自身 RLIMIT_NOFILE 软/硬值＋limits 文件是否在位）。

## 线协议（与 T2 收敛契约）

- 新增命令类型 `Tune`，data.action ∈ {apply-ipv4, apply-bbr, apply-kernel, apply-nic, rollback}；`TuneStatus` 保留命令名、回新结构。
- 退役命令 `TuneApply` / `TuneStart` / `TuneCancel` 仍在派发表中应答，回明确错误「旧调优命令 X 已退役，不再支持：请使用新版调优（Tune 命令）」，不落未知命令兜底。
- **收敛注意（唯一红项）**：`TestD1CommandSetContract` 的面板半边现报 `missing=Tune`——go-backend 尚未发 Tune（T2 在途）。金标准已按 agent 派发集更新（新增 Tune；旧三型因仍在派发表中应答而保留）。T2 落地面板改发 Tune 时，若同时停发 TuneApply/TuneStart/TuneCancel，金标准需同步删此三型（agent 侧应答可保留）。此项之外 x/socket 全量测试全绿。

## 退役处置

删除：`tuning_auto.go`（iperf3 自动调优/对端测速/任务机）、`tuning_queues.go`（深度整形/tc 队列）、`tuning_restore.go`（整形事务恢复）、`tcpfit/` 整目录（引擎 4474 行脚本＋bridge＋LICENSE＋README）、启动时的 `recoverAutomaticTuningOnStart` 调用（事务恢复＋容器整形重下发，新模型靠 conf/unit 开机自重放，不再需要）。测试同删：tuning_auto/bdp/d1d2/d4/deep/deep_manual/portreview/state_dir 共 8 个文件。

保留并复用的共享面：`writeFileAtomic`、`tuneShellQuote`、`tuneCommand`/`runTuneCommand`、`commandExists`、`tuningOperation` 锁、`prepareTuneProcess`（tuning_process_*.go 未动）。

## 探针保留处置（简报修正项，单列）

- `tuning_probe.go`、`tuning_probe_linux.go`、`tuning_probe_other.go` 与 `r14_f06f04_test.go`、`tuning_probe_linux_test.go`、`tuning_probe_r7_test.go` **原样保留未动**（git 无 diff）。派发器里 TuneProbe 的 referenceHost 注入按原样保留；分发中 TuneProbe 仍走 `probeTuneContext`。
- 为使探针在旧 tuning.go 删除后仍可编译，其结果词汇 `tuneParams` 与 `defaultTuneParams` 移入新 tuning.go 并注明「探针词汇保留」（探针文件本身未改一字）。
- `tuning_probe_test.go` 仅一处被迫适配：`TestTuneProbeDoesNotWrite` 的快照面原为旧模型状态文件（99-flmx conf / tuning-backup.json / tuning-params.json / modules-load 文件），这些路径变量已随旧模型删除；改为新模型写面（内核 conf、BBR conf、limits conf、gai 备份不得新建），断言逻辑与探针只读语义不变。其余探针用例原样通过。fakeProcSys 测试助手按新模型重写（同签名，种新模型读取/应用的键集）。
- 回退/卸载足迹不波及探针：探针无任何宿主足迹（只读），回退与清理集均未包含探针相关路径。

## 自卸载足迹同步

`hostTuningFileSet()` 换为新全集（机械枚举）：内核 conf、limits conf、BBR conf、gai 备份、RPS unit、RPS 脚本、一代残留（99-flmx conf、flmx-bbr modules-load、flmx-tuning-qdisc unit）、legacy 99-flvx conf（归属守卫不变：检出旧 gost 足迹时跳过并点名）。清理动作同步：两 unit 先 disable --now 再删＋daemon-reload；MSS 规则兜底删除；gai 备份未被恢复步消费且调优行在位时，清理先按备份还原 gai.conf 本体再删备份。恢复步 `restoreTuningForUninstall` 改走新 rollback（尽力而为，失败汇 warnings 进日志，卸载不中断）。回执预检改版：有足迹在位时 warnings 预告「回退调优＋将删文件清单」（沿用 uninstallWarnings 呈现面），无足迹保持纯成功形态；旧的「队列备份丢失必然失败」预检随整形退役删除。

## 偏差点名

1. **`tcp_congestion_control_version` 不写**（按简报）：非主线 sysctl，逐键应用会误报失败；代码注释与本报告双点名。
2. **agent systemd unit 无 LimitNOFILE**：install.sh 的 `write_flmx_agent_service` 模板只有 LimitCORE=0。limits.d 只作用于 PAM 登录会话，**对已运行的 agent 服务无效**——面③ 的 nofile 1048576 对 agent 本体不生效（TuneStatus 的 nofile 概要报进程真实 rlimit，便于面板侧识别）。修法在 install.sh 模板加 `LimitNOFILE=1048576`，属脚本面（T2/脚本路），本路未动 install.sh。
3. **install.sh 的 FLMX_TUNING_* 常量仍是旧足迹集**（99-flmx-tuning.conf 等）：其 `uninstall_flmx_agent_host_cleanup` 与 agent 侧清理集需由脚本路同步换新全集，否则脚本形态卸载会漏删新足迹（agent 面板连带卸载路径已同步）。
4. rollback 主动删 legacy 99-flvx conf（回退是节点属主在面板的显式动作）；卸载清理对该文件保留归属守卫——两者口径差异是沿用旧有区别（面板回退 vs 卸载清理），非疏漏。

## 验证

- `go build ./...`（go-gost 主模块＋x 模块全包）全绿；`go vet ./socket/` 过；gofmt 干净（fixh2_legacy_gost_guard_test.go 的存量格式问题与本路无关、未碰）。
- `go test ./socket/ -count=1` 全量：仅 `TestD1CommandSetContract` 面板半边一条红（missing=Tune，T2 收敛点，见上）；其余全过，含新增 tuning_vpsing_test.go 11 例（键集精确对表、缓冲区公式、gai 生命周期三形态、BBR/内核落盘与 MSS 顺序、NIC 排除集与 RPS 值、回退足迹全删、退役命令应答）、改写的 uninstall_host_cleanup_test.go 7 例、探针全套（DoesNotWrite/Fallback/SpeedtestOptional/Median/RejectsBadURL＋r14 复跑）。
