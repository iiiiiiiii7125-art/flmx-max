# R18 L1 再审计：R17-X-01 关闭验证

- 基线（pin 死）：main @ a0c18a0（`a0c18a0dc90b2f9c7a67e8a2a016cc1d97505fb5`，R17 修复 fix-r17 已合入并推 origin；`git merge-base --is-ancestor` 核对 c922e5d、6d4a51a、5233d8d、792a46c、5125ff5 均为 a0c18a0 祖先）
- 依据：`audit-out-132/r17/R17-X-ledger.md`、`R17-FIX-PLAN.md`、`R17-FIX-A-REPORT.md`、`R17-FIX-B-REPORT.md`、`R17-FIX-C-REPORT.md`、`R17-FIX-INT-REPORT.md`、`R17-X-verdicts.md`；毒化文件原件 `audit-out-132/r17/R17-L4-evidence/gost.json-candidate-written.json`（md5 `e381299b2aa931d2f7c097661ead6702`，378 行）
- 审计方式：只读审计（不改产品代码、不打 tag、不发版）；代码核对在 `~/workspace/.r18-l1-wt`（a0c18a0 工作树）做；行为腿在 Lightsail 测试机（16.146.199.68 / tailnet 100.80.182.11，用户授权）重跑；裁定只认本轮落盘原始日志与名单存档，不继承 R17 修复结论——R17 报告中的数值凡被引用均已用落盘原始日志重算核对（见各节）
- 销账对账（名称/对象/断言三对齐）：名称 R17-X-01；对象 `/etc/flux_agent/gost.json` 内的重复服务对（R17 原件为 `6_1_0_tcp`/`6_1_0_udp`，本轮新标本为 `3_5_3_tcp`/`3_5_3_udp`）；断言：重复落盘 → agent 重启失败；修复后重复写不进落盘、存量毒化读入可自愈、不再自砖

## 0. 结论总表

| 项 | 结论 |
|---|---|
| 1. 机理定位复核 | **PASS**（第 13 个服务 ParseService eager 绑定报错；Register 分支为备用） |
| 2.① 确定性去重 | **PASS**（单元 12/12 + live 回退 sent-ok 无重复） |
| 2.② 启动自愈 | **PASS**（一次起成 + 去重告警可查 + 落盘无重复；对照复现失败） |
| 2.③ harness 10/10 MISS | **PASS**（本轮重跑 10/10 MISS；无回归记录） |
| 2.④ 计时腿 + 守卫日志 48 条 | **计时 PASS；守卫日志部分复核**（INT 的 48 条无法从落盘证据重算；R18 新产生 14 条完整判定链，功能成立） |
| 3. R17D-OPEN-01 | **已解（SOLVED）**——control-5 悖论的分支机理已定位，不再结转 |
| 4. 关闭判定建议 | **CLOSE** |

## 1. 机理定位复核 — PASS

### 1.1 毒化原件复核（本轮直读）

`gost.json-candidate-written.json`：14 个服务；`6_1_0_tcp` 位于第 11、13 位，`6_1_0_udp` 位于第 12、14 位；地址均为 `[::]:14601`；md5 `e381299b2aa931d2f7c097661ead6702`，378 行。与 A 报告 §1 逐字一致。

### 1.2 失败点：ParseService eager 绑定（实测，非复述）

本轮沙箱重跑 `TestLoadPoisonedConfigFailsWithDuplicate`（a0c18a0，`R18-L1-unit-a.log`），实测错误：

`loader.Load(poisoned) error (startup failure point): parse service "6_1_0_tcp": listen tcp 0.0.0.0:14601: bind: address already in use`

代码链（a0c18a0 直读）：

- `go-gost/x/config/loader/loader.go:256-270`：按序加载全部服务（`paused` 跳过）；`ParseService` 失败包 `parse service %q`，`Register` 失败包 `register service %q`。
- `go-gost/x/config/parsing/service/parse.go:42` `ParseService` 内构造监听器并 `ln.Init(...)`，源码注释明文：`Listener initialization binds the port`——绑定发生在解析时（eager），早于注册。
- `go-gost/x/registry/registry.go:63-70`：`Register` 用 `LoadOrStore`，重复名报 `ErrDup`（`registry: duplicate object`）——此为备用分支，仅当两副本地址不同、绑定不冲突时才走到；本次毒化（同地址）走 bind 分支。
- `loader.Load` 任一分支失败 → `program.Start`（`go-gost/program.go:71-72`，`parser.Parse()` 后）返回错误 → agent 退出。

真机对照亦一致：未修复 agent 对毒化文件重启，journal 5 次 fatal 均为裸 `listen tcp 0.0.0.0:14601: bind: address already in use`（`R18-L1-leg2-control.log`）；修后 agent 同文件一次起成（`R18-L1-leg2-fixed2.log`）。

### 1.3 定位结论

A/C 两路的机理定位成立：失败点确为第 13 个服务（第二个 `6_1_0_tcp`）的 ParseService 监听器 eager 绑定；Register 重复分支为备用路径。FIX-PLAN 的“面板按上报服务表做差集”假说已被 B 路证伪（面板侧无服务表查询命令，全仓零命中）；真实窗口为 UpdateService 失败 → 1 号回退 AddService + agent 侧检查-注册-落盘竞态——本轮以 3_5_3 现场进一步证实（见 §3）。

## 2. 验收四项复核

### 2.1 ① 确定性去重 — PASS

- 代码（a0c18a0 直读）：`go-gost/x/socket/service.go` 第 4 阶段（注释 79-85 行，upsert 循环 88-108 行）按名替换、清除配置内多余同名副本；与 `updateServices` 第 3 阶段（196-214 行）同口径；循环内对 `nil` 配置项做 guard（`updateServices` 直接取 `.Name`，`nil` 项会 panic，此处保留原项不 panic）。D1-06 回滚语义未动（`OnUpdate` 落盘失败仍 `config.Set(originalConfig)` + 注销已注册服务）。
- 单元重跑（本轮新证据，`R18-L1-unit-a.log`，7,025 字节）：`TestCreateServicesDedupesExistingConfigEntry` PASS、`TestCreateServicesDedupesAlreadyPoisonedConfig` PASS；`x/config` 6 项（`TestDeduplicateServices*`、`TestPersistNow*`、`TestHealPersistedServices*`）PASS；`x/config/loader` 4 项（含 C 路 `TestLoadDuplicateServiceNamesError`、`TestLoadDuplicateServiceBindErrorNamesService` 与 A 路 `TestLoadPoisonedConfigFailsWithDuplicate`、`TestLoadHealedConfigSucceeds`）PASS。合计 **12 PASS / 0 FAIL**。
- 红→绿核对：`~/.fixr17-a` 红日志中两 dedup 测试 FAIL（`--- FAIL: TestCreateServicesDedupesExistingConfigEntry` / `TestCreateServicesDedupesAlreadyPoisonedConfig`），绿日志 8 PASS；本轮在 a0c18a0 重跑绿态 12/12（含 C 路 2 项），与 A 报告 §4 一致。
- Live（`R18-L1-leg1-fixed1.log`）：修后 agent 上真实面板回退 `AddService` sent-ok（self-occupied-rebind，6_1_0）后落盘无重复，md5 不变（`3f172a90…`，334 行暂停态）；harness 10 循环中 6 条回退判定行，落盘始终 pristine。
- 同型残留（不阻塞）：`go-gost/x/api/config_service.go:84-85`（`createService` 单个）与 `:169-171`（`createServices` 批量）盲 append 仍在（a0c18a0 直读）；管理 API 当前部署不可达——`install.sh` 的 `ExecStart` 为裸二进制（无 `-api` 参数），全仓无 `GOST_API` 设置点（唯一读取在 `parser.go:77`），与 A 报告 §6 结论一致。建议单独立项。

### 2.2 ② 启动自愈 — PASS

- 代码（a0c18a0 直读）：`go-gost/x/config/dedupe.go`（`DeduplicateServices` 保留首次出现、`HealPersistedServices` 去重后立即重新落盘）；`go-gost/program.go:76-77`（`Start()`，`parser.Parse()` 之后、`loader.Load` 之前）与 `:302-303`（`reloadConfig()`）接 `healPersistedConfig`；Warn 日志格式 `startup config self-heal: removed duplicate service(s) [...] from persisted config`，落盘失败只告警、不阻塞启动。
- 真机重跑（`R18-L1-leg2-fixed2.log`，journal unit 在位）：放毒化原件（md5 `e381299b…`、378 行）后重启——`active`、`NRestarts=0`（一次起成）；journal 警告行逐字：`{"level":"warning","msg":"startup config self-heal: removed duplicate service(s) [6_1_0_tcp 6_1_0_udp] from persisted config","time":"2026-10-25T01:43:36.396Z"}`；无 `agent-startup.log`（无失败，符合预期）；落盘后 12 服务、无重复。
- 对照（`R18-L1-leg2-control.log`，未修复 agent `ab628bcd…`）：`active=failed`、`NRestarts=5`、无 agent 进程；journal burst 1 共 5 次 fatal（裸 bind 错误，PID 715564/715586/715608/715630/715651）；落盘仍为毒化原件（`e381299b…`、378 行、14 服务）。
- 两处诚实注记：
  - (a) 首跑（`R18-L1-leg2-fixed.log`，pristine null unit 下）结果相同，但 journal 无告警行——恢复后的 pristine unit 为 `StandardOutput=null`，agent stdout 被丢弃；告警“可查”依赖 fix-C 的 journal unit（部署脚本安装，md5 `05449a73…`）。自愈行为本身不受 unit 影响（落盘已愈合）。
  - (b) 自愈后落盘 md5：本轮两次均为 `d48f09e1…`（pristine），与 INT 的 `2ff44f86…`（334 行）不同——去重后文件本为 334 行（378−44），面板重同步在 25s 等待窗内以 UpdateService 重写为 pristine（328 行）；INT 抓到的是重同步前的中间态。两者一致（12 服务、无重复），非矛盾。
- 二进制级旁证（`R18-L1-strings.log`）：修后 agent（`23aa260d…`）含 `startup config self-heal` ×2、`parse service` ×1、`DeduplicateServices` ×2；未修复 agent（`ab628bcd…`）三者均为 0——与 INT §3 一致。

### 2.3 ③ harness 10/10 MISS — PASS

- 本轮重跑（`R18-L1-harness-r18.log`，5,974 字节，nohup）：`cycles_done=10 hits=0 hit_cycles:[ ]`、`hit_rate=0/10`；10 条 `MISS (gost pristine, agent active)`；终态 gost `d48f09e1…`/328 行/12 服务/无重复，agent active、`restarts=0`。
- 过程插曲（披露）：首跑以前台 SSH 起跑，约 79s 时 `Broken pipe` 中断（cycle 1 的 75s 等待窗内零输出，疑似代理空闲掐断；首跑日志 `R18-L1-harness.log` 留存）；机上检查确认 cycle 1 同步未毒化（gost pristine、agent active），后以 nohup 重跑完整 10 循环。中断未污染结果。
- 基线对照：C 路基线 harness（机上 `/root/r17c/harness-r17c-base.log`）复核：`cycles_done=10 hits=0`，10 MISS；INT r17d harness（落盘日志）同为 10/10 MISS。三者一致。
- 定性（与 INT 一致）：本项为无回归记录（基线亦 0/10），非区分证据；区分证据见腿 1（§2.1）与腿 2（§2.2）。

### 2.4 ④ 计时腿 + 守卫日志 48 条 — 计时 PASS；守卫日志部分复核

- 计时重跑（`R18-L1-leg4-*.log`，修后树 r17d，VLESS 行为腿在内）：
  - D3 PASS（`R18-L1-leg4-create.log`）：5 入站 code 0（936 VLESS 59904、937 VMess 59905、938 VMess-ctl 59908、939 SS 59907、940 Trojan 59906；计数 578→583）；policy `levels 1–4 connIdle 5`；`flmx-idle-*` 出站 10 条（936/937/940 各 3 条含 per-user，939 仅通用——与 INT 同形）；规则顺序 idle 用户规则 → 通用规则 → mark 规则。
  - 行为腿（`R18-L1-leg4-legs.log`）：VL-1 VLESS 直连 CLOSED 5.26s（INT 5.25s）；VM-2 VMess 供数停发 10.02s、CLOSED 16.06s（INT 16.02s，停发后 6.04s）；SS-2 SS 供数停发 10.00s、CLOSED 15.04s（INT 15.02s，停发后 5.04s）；TR-1 Trojan 直连 CLOSED 5.09s（INT 5.09s）；CTL / VM-2m / SS-2m 均为 STILL_OPEN@30s；VM-1 VMess 静默 CLOSED 2.13s（见 R18-OBS-02）。
  - D4 PASS（`R18-L1-leg4-delete.log`）：5 入站逐个删除 code 0；计数回 578、r17 残留 []、policy null、`flmx-idle-*` []、idle 规则 []、出站回 `[direct, block, flmx-u1, flmx-u4, flmx-u12]`、规则 3、inbounds 11；脚手架进程与测试端口全清，仅剩 agent 自身 xray。
  - INT 腿 4 落盘证据重算：`leg4-legs.log` 原始值与 INT §7.3 逐项一致；`xray-config-created.json`（policy 1–4 级、10 条 idle 出站、规则顺序）与 `xray-config-after-delete.json`（policy null、idle []、规则 3）重算一致；创建日志中 VLESS 一次 code −1（payload 路径错）后补正建出 id 935 的过程与 INT 披露一致。
- 守卫日志 48 条——**未验证（源头已灭失），不继承为结论**：落盘原始日志中仅能数出 26 条 `r17-addservice-guard` 行（`R17-FIX-D-*.log`、`probe-control-*.log` 等，含跨文件重复引用）；机上 `/root/r17d/*.log` 仅 7 条。48 条的源头是验收时已删除的 r17d 后端容器的 json 日志，无法从落盘证据重算。该数字不作为关闭依据。
- 守卫功能新证据（`R18-L1-guard-count.log`）：R18 窗口内当前后端容器 json 日志共 **14 条**判定行——按 decision：`send` 7、`sent-failed` 1、`sent-ok` 6；按 reason：`update-fallback` 12、`self-occupied-rebind` 2；按服务：`6_1_0` ×6、`3_5_3` ×2、`2_5_2` ×2、`7_1_0` ×4。每条均有完整判定链（send→sent-ok/sent-failed），代码面 `guardedAddService` 为三处回退唯一出口（`control_plane.go:465/518/558`，`freshAbsence` 一律为真）、B 路守卫测试 10/10 PASS（`R18-L1-unit-b.log`）。守卫“打行、可 grep、判定覆盖回退路径”的功能复核成立。

## 3. R17D-OPEN-01 状态 — 已解（SOLVED），不阻塞关闭

### 3.1 悖论原状

INT control-5：未修复 agent 上一次真实面板回退 `AddService` sent-ok（转发 2，`2_5_2`）后落盘仍 12 服务、无重复；微观机理未解，记观察项 R17D-OPEN-01。

### 3.2 本轮新现场：同一 redeploy 内正反两例

对照组重跑（未修复 agent `ab628bcd…`，转发 6 暂停态）中，一次 agent 重启触发的 redeploy 内，`3_5_3` 与 `2_5_2` 各一次 update-fallback sent-ok（guard 时间戳 01:51:31.335 与 01:51:31.441），结果相反——天然对照实验：

- **3_5_3——毒化分支（R17-X-01 全周期复现）**：UpdateService 失败（udp/tcp bind，journal 01:51:31.327/.328；其中 `.328` 是回滚 restore 重新绑定失败）→ 服务未注册 → AddService 01:51:31.330 收到 → `listening on [::]:13102/tcp|udp`（.331）→ persist（.334）→ 落盘 14 服务、384 行、md5 `c26d7e04…`，`3_5_3_tcp`/`3_5_3_udp` 各 2 份（位置 7/8 原件 + 13/14 追加——盲 append 签名）。50ms 高速轮询 40s 内 **766 次命中**（`R18-L1-leg1-ctl-fast1.log`，41,740 字节），重复项非瞬态；随后一次 agent 重启即死（5 次 fatal，`listen tcp 0.0.0.0:13102: bind: address already in use`，`NRestarts=5`）。新毒化标本已归档：`R18-L1-poisoned-3_5_3.json`（md5 `c26d7e04…`，384 行）与机上 `/root/r17d/r18-poisoned-3_5_3.json`。
- **2_5_2——无毒化分支（control-5 同形）**：UpdateService 失败（udp bind，.438）→ 回滚 restore **成功**（tcp `listening on [::]:13101/tcp`，.438，旧服务重新注册）→ AddService 01:51:31.440 收到后**无 listening 行、无 persist 行**（.440→.448 间唯一持久化属于其他命令）→ 第 1 阶段 `IsRegistered` 拒绝；面板侧 `tolerateExists`（`control_plane.go:831-834`，`isAlreadyExistsMessage` 即归一化为成功）把拒绝记为 sent-ok。重复项从未存在过——INT 的“49ms 窗口内被直接推清理”假说不成立，无需该假说。
- 旁证：同次 redeploy 的直接推 UpdateService（3_5_3，.448/.449）成功重建服务后落盘仍 ×2——`updateServices` 第 3 阶段只替换首个匹配项、不删多余副本（代码直读），直接推无法清理已落盘的重复。

### 3.3 已定位的分支机理

毒化与否取决于 UpdateService 失败后**回滚 restore 能否重新注册旧服务**（`restoreServiceRuntime`，`service.go`：注销当前 → `ParseService` 旧配置 → `Register` → `Serve`；任一步失败即服务保持未注册）：

- restore 成功 → 回退 AddService 在第 1 阶段被 `IsRegistered` 挡下（面板 sent-ok 为 `tolerateExists` 掩盖，无 append、无毒化）——control-5 与本轮 2_5_2。
- restore 失败（端口仍被旧监听占用）→ 回退 AddService 直通第 4 阶段盲 append → 持久毒化，下次重启自砖——R17 原件（转发 6）与本轮 3_5_3。

### 3.4 对关闭判定的影响

**无影响，且强化关闭依据**：悖论已解，不再结转。修复覆盖全部两个分支——A 路第 4 阶段去重堵住 restore-失败分支（重复写不进落盘），自愈兜底存量毒化读入分支；restore-成功分支本来就不会产生毒化。R17D-OPEN-01 关闭。

## 4. 关闭判定建议 — CLOSE

建议关闭 R17-X-01。依据（与 INT §9 同构，证据全部换成本轮新证据）：

1. 漏洞已在单元级证实并闭合：红（两 dedup 测试 FAIL）→ 绿（12/12 PASS，本轮 a0c18a0 重跑）+ 代码行号核对。
2. 修复在 live 路径证实有效：修后 agent 真实回退 sent-ok 后落盘无重复（腿 1）；harness 10 循环零毒化（其间 6 条回退判定行）。
3. 毒化危害已被自愈兜底：修后 agent 对毒化原件一次起成、去重告警可查（journal unit 下）、落盘无重复；对照组复现失败（failed、NRestarts=5）。
4. 面板侧补发面已被守卫收敛：B 路 10/10 单元通过；R18 窗口 14 条判定行完整可 grep；`tolerateExists` 掩盖行为已定位并纳入机理（非未解）。
5. （新增）原始毒化机理已从“假说”升级为“已定位”：本轮以 3_5_3 现场完整复现 R17-X-01 全周期（写毒化 → 运行无恙 → 下次重启自砖），并以微秒时序的 journal+guard 双链解出 control-5 悖论的分支条件。

无 OPEN 理由：四项复核无 FAIL；R17D-OPEN-01 已解；残留项（§5）均不触及 R17-X-01 的对象本身。

## 5. 残留与观察项

- **R18-OBS-01（新）**：leg2-control 出现第二重启 burst（01:44:33.47，6 次 fatal，计数器复位后再次打满限速；INT 对照组只出现一个 burst）。触发源未定位（无 timer/cron；脚本内无第二次 start）。不影响对照结论（agent 始终未起来、NRestarts=5、毒化文件未变）。建议后续若再现，抓 `systemctl start` 调用方审计。
- **R18-OBS-02（新）**：VM-1 静默腿 2.13s、复测 2.15s，均略高于 O-148B-1 带宽上限 2.0s；INT 的复测 1.14s 本轮未重现。静默腿不计 FAIL；可能带宽上沿约 2.2s 或与测量开销有关。不影响 D1/D2 结论。
- **残留（自 A 报告 §6）**：`x/api/config_service.go:84-85` 与 `:169-171` 盲 append 仍在；管理 API 当前部署不可达，不属 R17-X-01 向量；建议单独立项，不阻塞关闭。
- **守卫 48 条**：INT 数字无法从落盘证据复核（源头容器已删）；R18 新产生 14 条完整判定链，功能复核成立。48 条本身不作为关闭依据。
- **恢复脚本说明**：`R17-FIX-D-restore.sh` 本轮未作为文件执行（安全提示）；改为逐步手动恢复，并发现该脚本的前提已失真——部署时 `/root/r17d/flux_agent.pristine` 被已换入的修后 agent 覆盖（`23aa260d…`），按脚本恢复会装错二进制；手动恢复改用 `/root/r17c/flux_agent.pristine`（`aff338e3…`，已核对）。最终状态已全量对账（§6）。
- **方法注记**：并行 SSH 曾致 host key 校验失败（后证实 known_hosts 指纹与服务器一致，属短暂代理异常；此后只用单路 SSH）；长静默 SSH 会话约 79s 时 Broken pipe（首跑 harness 中断），harness 须以 nohup 在机上跑；`/tmp` 工作树中途被清，已改在 `~/workspace/.r18-l1-wt` 重建。`R18-L1-paradox-journal2.log` 落盘时曾含 KernelApply 回显的凭据原文，已按凭据 hygiene 修订为仅保留审计相关行（14 行，0 凭据命中）；r18/ 目录下其他 lane（L3/L5）的文件未动。

## 6. 门后恢复对账（手动逐步恢复，2026-10-25 02:19Z）

| 对账项 | 基线（门前 pristine 1.3.5） | 门后 | 一致 |
|---|---|---|---|
| 面板版本 | 1.3.5，镜像 `flmx-backend:r1gate-1.3.5` / `flmx-frontend:r1gate-1.3.5`，backend healthy，`GET /` 200 | 同（compose/env md5 `3afbac11…` / `fffcd005…`） | ✓ |
| 节点 / tunnel / forward / inbound / subscription | 1 / 4 / 6 / 578 / 18 | 1 / 4 / 6（status 全 1）/ 578 / 18（items 实数） | ✓ |
| agent 二进制 | md5 `aff338e3676bc56d0f3986a1a62f8fdd` | 同 md5，service active | ✓ |
| agent unit | md5 `a12afd9c36fba7b76287210efaa199cb`，`StandardOutput=null` | 同 md5，`StandardOutput=null` / `StandardError=null` | ✓ |
| gost.json | md5 `d48f09e132ebe99fe97a619dd72d42d4`，328 行 | 同 md5、328 行、12 服务、无重复 | ✓ |
| 表数 | 49 表口径（`node_traffic` 为新版面板 autoMigrate 补建） | 50 表（含 `node_traffic`=1） | ✓ |
| 候选镜像 | — | `flmx-backend:r17d` / `flmx-frontend:r17d` 已删；仅剩 `r1gate-1.3.5` | ✓ |
| 磁盘 | 3.6G 空闲 | 3.6G 空闲 | ✓ |

## 7. 证据清单（本轮落盘，`audit-out-132/r18/`）

| 文件 | 字节 | 内容 |
|---|---|---|
| `R18-L1-unit-a.log` | 7,025 | A/C 路单元重跑 12 PASS（socket 2 + config 6 + loader 4） |
| `R18-L1-unit-b.log` | 1,300 | B 路守卫测试 10/10 PASS |
| `R18-L1-recon.log` / `R18-L1-recon2.log` | 2,241 / 1,535 | 门前机上状态与二进制/毒化文件 md5 核对 |
| `R18-L1-stage-check.log` | 393 | stage.tgz md5 `8f5ee797…` 与 journal unit `05449a73…` 核对 |
| `R18-L1-deploy.log` | 3,612 | r17d 部署（修后 agent + journal unit + 面板） |
| `R18-L1-leg2-fixed.log` | 1,018 | 腿 2 修后首跑（null unit，结果同，journal 无告警行） |
| `R18-L1-leg2-fixed2.log` | 1,233 | 腿 2 修后重跑（journal unit，告警行逐字在位） |
| `R18-L1-leg2-control.log` | 4,282 | 腿 2 对照（failed、NRestarts=5、毒化文件未变） |
| `R18-L1-control-journal.log` / `R18-L1-control-journal2.log` | 6,104 / 7,562 | 对照组 journal 精确时序（burst 1 = 5 次 fatal） |
| `R18-L1-burst2-check.log` | 2,119 | 第二重启 burst 核查（无 timer/cron，触发源未定位） |
| `R18-L1-unit-check.log` | 2,637 | null unit 下 journal 仅含 systemd 行的核查 |
| `R18-L1-leg1-setup.log` / `R18-L1-leg1-ctl-setup.log` | 463 / 258 | 腿 1 前置（转发 6 暂停/恢复、agent 换版） |
| `R18-L1-leg1-fixed1.log` | 2,262 | 腿 1 修后（rebind sent-ok，落盘无重复） |
| `R18-L1-leg1-ctl-fast1.log` | 41,740 | 悖论重探：50ms 轮询 766 次命中（3_5_3 持久重复） |
| `R18-L1-paradox-forensics.log` | 4,353 | 毒化现场取证（gost 384 行、journal fatal、guard 全量 8 行） |
| `R18-L1-paradox-journal.log` | 12,117 | 01:51:31 redeploy 命令时序（3_5_3 毒化分支） |
| `R18-L1-paradox-journal2.log` | 2,012 | 2_5_2 分支时序（已按凭据 hygiene 修订，14 行） |
| `R18-L1-poisoned-3_5_3.json` | 7,316 | 新毒化标本（md5 `c26d7e04…`，384 行，3_5_3 位于 7/8 + 13/14） |
| `R18-L1-recover.log` | 476 | 毒化现场恢复（pristine + 修后 agent + 转发 6 恢复） |
| `R18-L1-harness.log` | 844 | harness 首跑中断记录（Broken pipe，约 79s） |
| `R18-L1-harness-relaunch.log` | 346 | harness nohup 重跑启动 |
| `R18-L1-harness-r18.log` | 5,974 | harness 重跑 10/10 MISS（cycles_done=10 hits=0） |
| `R18-L1-harness-baseline-check.log` | 159 | C 路基线 harness 机上复核（10/10 MISS） |
| `R18-L1-harness-abort-state.log` | 1,090 | 中断后机上状态检查（gost pristine、agent active） |
| `R18-L1-leg4-scaffold.log` | 2,481 | 腿 4 脚手架（dummy + 9 xray） |
| `R18-L1-leg4-create.log` | 2,296 | 腿 4 创建 + D3（入站 936–940） |
| `R18-L1-leg4-legs.log` | 1,455 | 腿 4 行为腿 8 项 + VM-1 复测 |
| `R18-L1-leg4-delete.log` | 1,150 | 腿 4 清理 + D4（计数回 578） |
| `R18-L1-guard-count.log` | 490 | R18 窗口守卫判定行统计（14 条） |
| `R18-L1-strings.log` | 248 | 二进制字面量复核（修后 2/1/2，未修复 0/0/0） |
| `R18-L1-restore.log` | 1,875 | 手动逐步恢复（agent/unit/gost/compose/镜像） |
| `R18-L1-reconcile.log` | 591 | 最终对账（1/4/6/578/18、表 50、磁盘 3.6G） |
| `R18-L1-pristine-agent-check.log` | 697 | `/root/r17c/flux_agent.pristine` = `aff338e3…` 核对 |

工作树：`~/workspace/.r18-l1-wt`（a0c18a0，`/tmp/r18-a0c18a0` 中途被清后重建）；机上标本：`/root/r17d/r18-poisoned-3_5_3.json`（md5 `c26d7e04…`）。

— R18-L1-x01-closure END · 自证：本行之前正文字节数 23540
END
