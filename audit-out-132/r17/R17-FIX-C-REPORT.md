# R17 FIX-C 报告 — 取证前提（agent 日志可查）+ 复现 harness + 测试机验收

- 基线：main @ 5473377（`5473377a67253c470edcad0d2bca2d53d986ed1d`）
- 分支：`fix-r17-c`（自 main 切出，worktree `~/workspace/.fixr17-c`）
- 日期：2026-10-25（CST），测试机 Lightsail Debian 12（公网 16.146.199.68 / tailnet 100.80.182.11）
- 约束遵守：不合 main、不打 tag、不发版、不改版本字面量；禁用 `git add -A`（只 add 明确文件）；`systemctl start` 前先 `reset-failed`
- 状态：**步骤 1（日志可查）与步骤 2（基线 harness）已完成并验证；步骤 3（集成树验收 + 计时腿）待主控通知集成树就绪后执行**
- **基线 0 命中（10/10 MISS）：按任务书要求，先报基线结果、等主控定夺再继续，不加循环、不调触发条件、不伪造命中**

## 0. 结论

1. **日志可查 — PASS。** 毒化 gost.json 重启 agent，失败原因在 journal 与 `agent-startup.log` 两处均可查到，且指名了重复服务（见 §1）。
2. **基线 harness — 0/10 命中。** 未修复树（main @ 5473377 原样 bits）面板+agent 同启循环 10 次，落盘 gost.json 每次均为 pristine（md5 `d48f09e1…`、328 行、12 服务、无重复名），agent 每次 active、NRestarts=0。R17 原始部署 2 次启动中 1 次命中的触发条件在本 harness 的 10 次循环中一次未复现（见 §2）。
3. **机器已全量恢复 pristine**（见 §4）：计数 1/4/6/578/18、agent `aff338e3…`、gost `d48f09e1…`、unit 原件、`1.3.5` 面板、`GET /` 200、表 50（49 + node_traffic）。

## 1. 步骤 1 — agent 日志可查

### 1.1 改动（分支 fix-r17-c，提交 5233d8d）

| 文件 | 行 | 改动 |
|---|---|---|
| `install.sh` | 463–515（`write_flux_agent_service`） | systemd unit：`StandardOutput=null` → `StandardOutput=journal`，`StandardError=null` → `StandardError=journal`，加 `SyslogIdentifier=flux_agent`（行 479–485）；OpenRC：`output_log/error_log` 由 `/dev/null` 改为 `/var/log/flux_agent.log` / `/var/log/flux_agent.err.log`（行 502–505） |
| `go-gost/main.go` | 116–133（新增 `logStartupFailure`）、142–146、171–174 | 启动失败（`LoadConfig` 失败与 `svc.Run` 返回错误）先以时间戳行追加写 `agent-startup.log`（可执行文件同目录，即与 `agent-upgrade.log` 同级，`/etc/flux_agent/`），再按原路径退出/ Fatal；写日志本身失败只报 stderr，不掩盖启动错误 |
| `go-gost/x/config/loader/loader.go` | 217–222、230–236 | `ParseService` 失败包一层 `parse service %q`、`Register` 失败包一层 `register service %q`——裸错误（`address already in use` / `registry: duplicate object`）不带服务名，取证时无法对应到毒化文件里的重复项 |
| `go-gost/x/config/loader/duplicate_service_test.go` | 新增 | 两则回归测试（见 §1.3） |

取证口径说明（二选一的抉择）：两条腿都做了——unit 改 journal（部署模板，治本，stdout/stderr 全量可查）与二进制写 `agent-startup.log`（兜底，journal 不可用/轮转时失败原因仍在）。journal 覆盖运行期日志，文件只记启动失败；两者互补，不是重复。

别处写 unit 的排查：全仓只 `install.sh:463` 定义、`install.sh:786` 一处调用 `write_flux_agent_service`；`panel_install.sh` 不写 unit（无任何对 install.sh unit 函数的引用）；`test-install-scripts-*.sh` 只 mock，不写生产 unit。`StandardOutput`/`StandardError` 全仓唯一命中即 `install.sh:479-480`（已改）。

### 1.2 本地验证（先红后绿的发现）

- 首版只给 `Register` 加了服务名；本地二进制探针（毒化文件原件 md5 `e381299b…`，`go build CGO_ENABLED=0`，与 R17 同标志、不带 `-s -w`）显示真实失败路径**不是注册重复**：`ParseService` 建监听器时就绑端口，第二个 `6_1_0_tcp` 先死于 `bind: address already in use`，根本到不了 `Register`。据此补了 `parse service %q` 包裹（loader.go:217–222），重跑探针得到：
  `2026-10-24T23:29:18Z agent startup failed [start]: parse service "6_1_0_tcp": listen tcp 0.0.0.0:14601: bind: address already in use`
- install.sh 渲染测试：抽取 `ensure_service_manager` + `write_flux_agent_service` 在沙箱跑，systemd/OpenRC 两分支渲染均正确；测试中发现我自己写进 unit 注释的反引号 `` `journalctl -u flux_agent` `` 会被 `<<EOF` 未引用 heredoc 当场执行并把输出嵌进 unit 文件——已改为无反引号纯文本，复测渲染干净；`bash -n install.sh` 通过；`gofmt` 干净。

### 1.3 单元测试与 vet

- `go test ./config/loader/`（go-gost/x 模块内）：全过。新增 `TestLoadDuplicateServiceNamesError`（`:0` 端口、注册路径、断言含 `register service "6_1_0_tcp"` 与 `registry: duplicate object`）与 `TestLoadDuplicateServiceBindErrorNamesService`（固定端口、解析路径、断言含 `parse service "6_1_0_tcp"` 与 `address already in use`）。
- `go vet ./...`（go-gost）：exit 0；`go vet ./config/loader/`：干净。
- main 包无法加 Go 单元测试：`main.go:init()` 的 `flag.Parse()` 拒收 `-test.*` 参数（既有约束，非本次引入）——`logStartupFailure` 改以二进制级探针验证（§1.2 本地 + §1.4 真机）。

### 1.4 真机验证（测试机，fix-C agent md5 `a03d72814b2b32d8d25c12351330ebc7`，50,420,431 字节）

步骤（`R17-FIX-C-logverify.sh`，日志 `R17-FIX-C-logverify.log`）：停 agent → 换 fix-C 二进制 → 用 fix-r17-c `install.sh` 渲染的 unit 文件覆盖 `/etc/systemd/system/flux_agent.service` → `daemon-reload` → pristine gost 起 agent（active）→ **停 agent 后**放毒化文件 → `reset-failed` → start → 观测 → 恢复 pristine 起 agent。

- 干净启动的 journal 已有 agent 输出（`✅ 配置加载成功`、各服务 `listening on` 行）——journal 腿在干净启动上即证实。
- 毒化重启：`failed`、`NRestarts=5`、无 agent 进程——与 R17 严重性探针（`R17-L4-severity-probe.log`）逐字一致。
- **journal 里的确切失败原因**（`journalctl -u flux_agent`，23:55:48–49，5 次尝试、5 行，PID 677489/677513/677535/677557/677579）：
  `{"level":"fatal","msg":"parse service \"6_1_0_tcp\": listen tcp 0.0.0.0:14601: bind: address already in use","time":"2026-10-24T23:55:48.927Z"}`
  另有同名 listener error 行指名 `service 6_1_0_tcp`。
- **`agent-startup.log` 里的确切内容**（`/etc/flux_agent/agent-startup.log`，5 行，与 5 次尝试一一对应）：
  `2026-10-24T23:55:48Z agent startup failed [start]: parse service "6_1_0_tcp": listen tcp 0.0.0.0:14601: bind: address already in use`
- 恢复 pristine 后 agent 一次起成（PID 677688，active）。
- 过程教训（已写进脚本注释）：第一版脚本在 agent 运行中放毒后直接 `systemctl start`——对 active unit 是 no-op，根本没加载毒化文件；且运行中 agent 的下一次持久化会把内存里的干净配置写回磁盘、把外来毒化文件“自愈”。正确的毒化重启必须是：停 agent → 放毒 → start。R17 严重性探针的做法与此一致。

## 2. 步骤 2 — 复现 harness 与基线命中率

### 2.1 Harness（提交 792a46c，`audit-out-132/r17/R17-FIX-C-harness.sh`）

每循环复刻 R17-L4 部署/复现顺序：停 agent → `docker compose restart backend frontend`（面板启动同步）→ 等 backend healthy → `reset-failed` + start agent → 等 75s（R17 毒化出现在启动后约 30s；隔离 cell 等 75–90s）→ 快照 gost.json（md5/行数/python 计重名服务）+ agent 状态。命中 = md5/行数变、或出现重复服务名、或 agent 非 active。命中循环把毒化文件存 `/root/r17c/cycle-<i>-gost.json`，恢复 pristine 再进下一循环（循环间独立）；恢复后 agent 起不来则中止（不级联误计）。

### 2.2 基线部署（未修复树，R17 原样 bits）

`R17-FIX-C-deploy.sh`，日志 `R17-FIX-C-deploy.log`：本地 `~/.r17-build/` 产物 md5 与 R17 build-record 逐字一致后复用——agent `b92b61993dbfd6134db048e7f256552b`、paneld `f574c43e9be9f92c3506bfa62c29e691`、stage.tgz `902cac4808eb5f7de36aca098e936b49`、nginx.conf `8e4c555a283c283a31110b5c9601b479`；镜像 tag 用 `r17c-base` 以区别于 R17 残留（行为与 r17 同）。部署后候选面板 healthy、agent active（PID 667648）、`GET /` 200、计数 1/4/6/578/18；部署后约 90s 时 gost 仍 pristine——本次同启本身未命中（记为第 0 次）。

### 2.3 基线结果：0/10

- 运行：`harness-r17c-base.log`，2026-10-24T23:36:37Z–23:51:55Z，循环 10 次，每循环约 92s。
- 每循环落盘：md5 `d48f09e132ebe99fe97a619dd72d42d4`、328 行、12 服务、`DUP:none`、agent active、NRestarts=0；agent PID 每循环更新（668581、669405、670208、671010、671849、672666、673464、674295、675107、675932）；gost mtime 每循环推进（agent 每次都重写了文件，内容与 pristine 逐字一致）。
- **命中率 0/10（cycles_done=10，hits=0，hit_cycles=[]）。**
- 有效性核对：循环确实走了同启路径（面板容器每循环 restart、backend healthy、agent 新 PID、gost 被重写、面板 `node reconnect probe: nodes=[1]` 显示面板-agent 通道在线）。10 次 0 命中不是 harness 没跑起来，而是该触发条件在同启循环里未复现。R17 的 1/2（原始部署命中、重跑未命中）+ 本轮 0/10，合计全对启动 1/12——触发远比“每次同启 50%”更窄，或依赖首部署特有的条件（如全新镜像冷启动、agent 二进制刚换过、面板首次同步的某时序），本 harness 未覆盖。

## 3. 步骤 3 — 待通知（未开始）

- 集成树（fix-r17，含 A 路去重+启动自愈与 B 路幂等守卫）就绪后：在集成树上重跑 harness（循环次数 ≥10、日志在位），命中率须归零；重验计时腿（VLESS/VMess/SS/Trojan，connIdle=5，带无值对照、计时观测）+ 反复同启循环无毒化文件；门验后恢复 pristine 并对账。
- 本报告步骤 3 一节在集成验收完成后补写；当前版本计时腿结果与修复后命中率均为“未验证”，不得冒充。

## 4. Pristine 恢复对账

恢复脚本 `R17-FIX-C-restore.sh`，日志 `R17-FIX-C-restore.log`（另有一次手工补正，见下）。

| 对账项 | 基线（门前 pristine 1.3.5） | 门后 | 一致 |
|---|---|---|---|
| 面板版本 | 1.3.5（`flmx-backend:r1gate-1.3.5`） | 容器 FLUX_VERSION=1.3.5，镜像 r1gate-1.3.5，backend healthy，`GET /` 200 | ✓ |
| 节点 / tunnel / forward / inbound / subscription | 1 / 4 / 6 / 578 / 18 | 1 / 4 / 6 / 578 / 18（最终快照 2026-10-25T00:01:46Z） | ✓ |
| agent 二进制 | md5 `aff338e3676bc56d0f3986a1a62f8fdd` | 同 md5，active，新 PID 679404 | ✓ |
| gost.json | md5 `d48f09e132ebe99fe97a619dd72d42d4`、328 行 | 同 md5、328 行、12 服务、无重复（23:59:56 后稳定，20s 复测不变） | ✓ |
| systemd unit | md5 `a12afd9c36fba7b76287210efaa199cb`（StandardOutput=null） | 同 md5（手工补正后，见下） | ✓ |
| xray 配置 | policy 无、规则 3 | 门后未重测（本轮未创建测试入站，计时腿在步骤 3） | —（步骤 3 补） |
| 表 | 50（49 + node_traffic） | 50，node_traffic=1（SQLite gost.db 只读拷贝计数；机上 DB_TYPE=sqlite，Postgres 库 `flux_panel` 为空库未用） | ✓ |
| compose / .env | tag r1gate-1.3.5 / FLUX_VERSION=1.3.5 | 已从 `.pristine` 备份恢复（md5 逐一核对） | ✓ |
| 候选镜像 / staging | — | `flmx-backend:r17c-base` / `flmx-frontend:r17c-base` 已删；`/root/r17c/` 保留 stage.tgz、二进制与脚本（主控若加跑基线可直接复用） | ✓ |
| 磁盘 | 4.2G 空闲 | 3.9G 空闲（本轮消耗约 0.3G：docker 构建层残留，与 R17 同量级） | ✓（记录） |

两处恢复插曲（已处理，如实登记）：

1. **unit 恢复初版失败**：`R17-FIX-C-logverify.sh` 第一版（有 bug 的那次）先备份了原件，第二版运行时备份的已是新 unit，导致 `flux_agent.service.pristine` 存的是 fix-C unit；恢复脚本据此还原后 md5 仍是 `05449a73…`（journal 版）。发现后按 23:21 实测原件内容手工重建，md5 与原件 `a12afd9c…` 逐字一致，`daemon-reload` 后核对通过；agent 未重启（unit 只影响下次启动）。
2. **gost.json 一度读到 `06a389b5…`**：恢复后快照（23:59:55，agent 启动约 1s 后）md5 为 `06a389b5…`、328 行；1s 后（23:59:56）即回到 `d48f09e1…` 并稳定。事后语义比对：12 服务同名、顶层键相同、deep-equal 为真——属 agent 重启/面板重同步期间的持久化瞬态（疑似服务表重建顺序差异），非配置变更；终态以稳定后的 `d48f09e1…` 为准。

## 5. 未验证项（明文登记，不得冒充）

1. **基线命中率 0/10，竞态假说未证实**——10 次同启循环一次未命中；是否加循环、调触发条件（如全新镜像冷启动、agent 二进制热换、面板首次同步时序），等主控定夺。
2. **步骤 3 全部**：集成树 harness 重跑（命中率归零）、计时腿（VLESS/VMess/SS/Trojan，connIdle=5，带无值对照）、反复同启无毒化——待主控通知集成树就绪。
3. OpenRC 分支的日志改动（`/var/log/flux_agent*.log`）只做了模板渲染测试，测试机是 systemd，未真机验证。
4. 面板侧 `AddService` 下发记录仍未留存（FIX-PLAN 取证前提的另一半）：agent 侧日志 + harness 只能证实“失败原因是什么”，不能证实“面板何时、因何判定补发”。建议主控划归 B 路（幂等守卫）或另立取证项；C 路任务书未含此项，未擅自改后端。
5. VLESS 行为腿、Python 直连 SS 客户端腿：本轮未跑（步骤 3 的计时腿覆盖）。
6. 远端节点与非 Debian 内核：仅本机 Debian + Xray 26.3.27（计时腿环境与 R17 同）。

## 6. 证据清单（裁定只认落盘原始日志）

本地 `~/workspace/.fixr17-c/audit-out-132/r17/`（分支 fix-r17-c 内；R17 证据原件未动）：

| 文件 | 内容 |
|---|---|
| `R17-FIX-C-harness.sh` | 复现 harness（已提交 792a46c） |
| `R17-FIX-C-deploy.sh` / `R17-FIX-C-logverify.sh` / `R17-FIX-C-restore.sh` / `R17-FIX-C-snapshot.sh` | 部署 / 日志验证 / 恢复 / 计数快照脚本 |
| `R17-FIX-C-baseline.log` | 门前/门中/门后计数快照（POST 法，见 §7） |
| `R17-FIX-C-deploy.log` | 基线部署全程 |
| `harness-r17c-base.log` | 基线 10 循环原始日志 |
| `R17-FIX-C-logverify.log` | 日志验证全程（含第一版脚本的 no-op 事故现场） |
| `agent-startup.log` | 真机毒化重启的 5 行失败记录（原件） |
| `journal-poison-window.log` | 毒化窗口 journal 原始摘录（5 行 fatal 在内） |
| `R17-FIX-C-restore.log` | 恢复全程 |
| `R17-FIX-C-REPORT.md` | 本报告 |

机上原件保留于 `/root/r17c/`（stage.tgz、两版 agent 二进制、毒化文件、各脚本与日志、`.pristine` 备份）。

## 7. 方法订正与观察（披露在先）

- **M1 — 1.3.5 面板的 `*/list` 是 POST 不是 GET。** 首版快照脚本用 GET 得到 `code -1 "请求失败"`（http 200），初判 token 失效并重新登录一次；实测 POST `{}` 即 code 0。R17 的 token 本就有效，重新登录是多余的（无害）。快照脚本已改为 POST；候选面板同样接受 POST。
- **O-1 — 运行中 agent 会“自愈”外来毒化文件。** 第一版 logverify 事故中，运行中 agent 在面板命令持久化时把内存里的干净配置写回磁盘，覆盖了刚放进去的毒化文件。真正的毒化态是内存+磁盘一致（R17 原始形态），外来单改磁盘文件不构成持久毒化——复现与修复验收都须以“agent 重启时加载”为判据，不能只看文件。
- **O-2 — gost.json 在 agent 重启/面板重同步窗口内 md5 可能瞬变。** 恢复时一次快照读到 `06a389b5…`，1s 后回到 `d48f09e1…` 并稳定（§4 插曲 2）。harness 的快照时点在启动后 75s+，避开了该窗口；门验对账应以稳定后复测为准。

## 8. 交付与待主控定夺

- 分支 `fix-r17-c`：提交 `5233d8d`（日志可查：install.sh + main.go + loader.go + 回归测试）、`792a46c`（harness）；其余脚本/日志/报告随本报告一并提交（不合 main）。
- **待定夺 1（基线 0 命中）**：是否加跑循环（如再 10–20 次）、或调整触发条件（全新镜像冷启动 / agent 二进制热换后首同步 / 面板首次同步时序），由主控定。
- **待定夺 2（步骤 3）**：集成树 `fix-r17` 就绪后通知本路执行重跑 harness + 计时腿 + 恢复对账。
- **建议（非定夺项）**：面板侧 `AddService` 下发记录缺失（§5.4）宜在集成验收前补齐，否则竞态机理仍无法闭环取证。

— R17-FIX-C-REPORT END · 自证：本行之前正文字节数 17050 · END
