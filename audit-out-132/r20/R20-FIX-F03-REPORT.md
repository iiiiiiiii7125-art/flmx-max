# R20 修复报告 F-03：VLESS 空闲超时语义为 2×connIdle（P3）

窗口（UTC）：测试机锁 F03 2026-10-25T06:28:25Z–06:47:02Z；gost 复核窗口 06:47:16Z–06:49:16Z（锁已放）。基线：main @ b8fb567。分支：`fix-r20-f03`（提交号见 §7）。只修、不合 main、不打 tag、不发版。

## 结论（先说）

- **修法：② 文案改称实测语义。行为零变化，无 BC。**
- 2× 不是面板算错：面板下发的 `policy.levels[].connIdle` 与 `flmx-idle-*` 出站的 `userLevel` 均与用户设定值逐字一致（§1 定位 + 真机渲染核验）。2× 是上游 Xray `ActivityTimer` 的窗口化检查粒度：以 N 秒为一格、每格检查一次，全程无数据约 N 秒断，有数据的会话自末次数据起 **N–2N 秒**后断（相位决定具体值）。两条新增相位腿（§5）证实了窗口模型、证伪了“计时自会话建立起算 2N”。
- 修法 ①（计时改为“末次数据起算恰好 N 秒断”）在面板侧做不到：Xray 只暴露 `connIdle` 秒数这一档配置，量化粒度在上游 `common/signal` 实现里，面板不 vendoring Xray core（节点跑下载的 Xray 二进制）；把下发值减半会让静默会话提前到 N/2 断开，是更坏的方向（§2）。
- UI 文案与文档现与实测语义一致（§3）；渲染值逐字原样由新增回归测试钉死（§4）。

## 1. 2× 来源定位

面板侧（`go-backend/internal/kernel/render_xray.go`，b8fb567）：

- `renderXray:45`：`lv[strconv.Itoa(l)] = obj{"connIdle": in.ConnIdleSeconds}`——设定值逐字写入 policy level，无任何换算。
- `xrayConnIdleRoutes:164-176`：`flmx-idle-<id>`（及按用户分户版）freedom 出站 `settings.userLevel = level`，逐字；`xraySettings` 的 VLESS/VMess/Trojan 客户端 `level` 字段同样逐字。真机渲染核验（§5 setup）：policy `{1:{connIdle:5}, 2:{connIdle:5}, 3:{connIdle:5}}`（后扩至 `{…, 4:{connIdle:10}, 5:{connIdle:10}}`），`flmx-idle-958 {userLevel:1}` 等，入站客户端 level 与之一致。

上游侧（xray-core v1.260327.0，`~/go/pkg/mod/github.com/xtls/xray-core@v1.260327.0`，节点所用同版本系）：

- VLESS 入站正常路径不装空闲计时器：`proxy/vless/inbound/inbound.go:281` 只取 `ForLevel(0)` 作握手 deadline；`CancelAfterInactivity` 只出现在 fallback 分支（`:416`）。会话唯一的计时器是 freedom 出站 `proxy/freedom/freedom.go:169-174`：`timer := signal.CancelAfterInactivity(ctx, cancel, plcy.Timeouts.ConnectionIdle)`，其中 `plcy = ForLevel(h.config.UserLevel)`——即面板下发的 level 对应的 `connIdle`。
- 计时器语义（`common/signal/timer.go:78` `CancelAfterInactivity` + `SetTimeout` + `common/task/periodic.go`）：`SetTimeout(N)` 建 `Interval=N` 的 `Periodic` 检查任务；`Start()` 立即执行一次 `check`（消耗 `SetTimeout` 时预置的 `Update` 记号），此后每 N 秒检查一次；`check` 仅在自上次检查起无任何 `Update()` 时关闭会话；`buf.Copy` 经 `UpdateActivity` 在每份数据到达时调 `Update()`（`common/buf/copy.go:27`）。
- 推论（检查格锚定在计时器创建≈会话建立时）：
  - 全程无数据：首次定时检查（约 N）即断 → 静默腿 5.20s。
  - 窗口内有数据：该次检查消耗记号存活，下一次检查（约 2N）才断 → 带数据腿 10.21s（N=5）、20.20s（N=10）。
  - 一般地，自末次数据起的实际关断在 **(N, 2N]** 区间：末次数据刚过某格线 → 约 2N；临近格线 → 约 N。不是恒等于 2N，也不是“自会话建立起算 2N”——R20 任务书/V-04 的“计时自会话建立起算、2×connIdle”是带数据单 burst 腿的表观描述，本轮相位腿（§5）将其修正为窗口模型。

## 2. 修法选择：②，及 ① 不可行之理由

- ① 要求把关断时刻改为“末次数据 + 恰好 N 秒”。面板能下发的只有 `connIdle` 秒数；Xray 不提供检查粒度、相位或“精确 idle 计时”开关。实现 ① 须改上游 `ActivityTimer`（fork/patch Xray core 并改节点内核分发）或在数据面另建面板侧会话看门狗——均超出本次面板修复范围，且后者是新机制、非一行修复。
- 唯一面板侧能动的标量是下发值本身，而任何标量都修不好：减半下发（N/2）→ 带数据腿约 N 秒断，但静默腿提前到约 N/2 秒断（静默会话是更常见的空闲形态，且 UI 承诺的“约 N 秒”随之被破坏）；加倍更无意义。标量补偿在两种腿上方向相反，故不存在正确标量。
- 因此定 ②：行为不动，文案与文档改称窗口化实测语义；并在渲染处留锚注（§3），防止日后有人以“减半修复”重蹈。

## 3. 改动（4 个文件）

| 文件 | 改动 |
|---|---|
| `vite-frontend/src/pages/inbound/inbound-form-modal.tsx:1165` | 空闲超时字段说明由“连接无数据超过该秒数即断开”改为“Xray 以该秒数为一格检查空闲——全程无数据约该秒数断开，有数据时自末次数据起 1–2 倍该秒数后断开；留空或填 0 不下发该段、走默认 300 秒” |
| `doc/guide/nodes.md` §空闲超时 | 首段承诺改为“空闲关断窗口”；新增“关断语义（上游 Xray 机制，非面板缺陷）”条：N–2N 区间、相位决定因素、实测值（N=5 带数据 10.2s/静默 5.2s、N=10 带数据 20.2s）、下发值逐字一致、减半之害；删除旧“约为设定值的 2 倍”恒定倍数表述；VMess/SS 约 3.1s/静默约 1.1s 的观察保留并与 R20 口径对齐 |
| `go-backend/internal/kernel/render_xray.go`（注释） | idle 出站注释块追加 R20-F03 锚注：逐字下发是设计、2× 是上游窗口量化、禁减半、指回归测试 |
| `go-backend/internal/kernel/r20_f03_test.go`（新增） | 回归测试（§4） |

BC 行为变化：**无**。本次修复不改任何下发值与计时行为；此前带数据会话 2N 秒断、静默 N 秒断，修复后完全相同。发版说明无需单列行为变化，只需说明文案与实测语义对齐（修法 ② 的语义：空闲关断为窗口化 N–2N，静默约 N）。

## 4. 回归：先红后绿

### 4a. 新增测试 `TestR20F03*`（`go-backend/internal/kernel/r20_f03_test.go`）

- `TestR20F03ConnIdleRenderedVerbatim`：N=5 渲染 policy `connIdle == 5`（非 2.5/10）、`flmx-idle-7 userLevel == 1`、渲染产物恰含一处 `connIdle:5`。该断言修前修后均绿——它是防“减半修复”的护栏，不是本次红绿项。
- `TestR20F03IdleCopyStatesWindowedSemantics`：UI 表单须含“自末次数据起 1–2 倍该秒数后断开”且不含旧承诺“连接无数据超过该秒数即断开”；文档须含“N–2N 秒”且不含“空闲超过设定秒数后由节点主动断开”与“约为设定值的 2 倍”。
- 红（修前，`go test ./internal/kernel/ -run TestR20F03 -v -count=1`）：`TestR20F03IdleCopyStatesWindowedSemantics` FAIL（`inbound form still promises N-second idle cutoff`），verbatim 项 PASS。日志 `R20-F03-test-red.log`。
- 绿（修后，同命令）：两项 PASS（0.04s）。日志 `R20-F03-test-green.log`。

### 4b. `go test ./...`（go-backend）

- 基线（修前，`go test ./...`，日志 `R20-F03-baseline-gotest.log`，exit=1）：唯一产品红为已知沙箱 fake-IP DNS 致 `TestValidateMonitorTCPTarget/公网域名放行` 一格（`internal/health`）。另有并行跑噪声：`internal/http/handler` 10m 超时于 `TestR2B10NodeRecoveryDisabledDoesNotResend`、`internal/store/repo` `TestNodePortRangeOccupantNamesCompanion` 报 `database or disk is full`、`internal/ws` 与 `tests/contract` `[build failed]`——三者逐个重跑均 PASS（handler 该单测 2.11s、repo 单测 0.79s、ws 包 22.65s），定性为多 lane 并行跑共享 go-build 缓存/资源争用，非产品失败。
- 修复后全量（`go test -p 2 -timeout 20m ./...`，日志 `R20-F03-postfix-gotest.log`，exit=1）：handler 全包 PASS（756.8s）、kernel PASS；健康红与基线同签名（仅 `公网域名放行` 一格，`example.com:443` 被判内网/保留地址）；另有 9 包 `[build failed]`，原因为共享 go-build 缓存对象在并行跑中途消失（`link: cannot open file /home/hatch/.cache/go-build/02/022d18…-d: no such file or directory`，8 包同引同一缓存对象）——缓存争用，非产品失败。
- 修复后重跑（`go test -p 1 -count=1 -timeout 20m` 逐包顺序，日志 `R20-F03-postfix-rerun.log`）：9 包全 PASS（logredact、metrics、monitoring、netguard、runtime/nftables、security、store/repo 143.1s、ws、tests/contract 68.5s）。结合全量跑，修复后除 health 已知一格红外产品失败为 0，与基线同签名。
- `go build ./...` 与 `go vet ./internal/kernel/` 全绿；前端 `tsc --noEmit` 全绿（exit=0）。

## 5. 真机腿（修复版部署 r20f03，锁 F03）

部署：paneld 由 fix-r20-f03 在沙箱重建（CGO_ENABLED=0、-trimpath、go1.25.1，与 L5 口径一致；md5 `e6c6ab2c…`）；前端 dist 由同分支 vite 构建（index.html md5 `754a9b36…`，59 个 asset）；agent 复用 R20 r20d（md5 `3b06de9d…`，本次未改 agent）。镜像 `flmx-backend:r20f03` / `flmx-frontend:r20f03`，`FLUX_VERSION=1.4.8-r20f03`。部署插曲：首版 dist 文件权限 660（沙箱 umask）致 nginx `open() index.html failed (13: Permission denied)`、GET / 403；`chmod -R a+rX stage/dist` 后重建前端镜像，GET / 200（index md5 与 stage 一致）；served 的 `inbound-form-modal-*.js` 内新文案在位、旧承诺计数 0。部署日志 `R20-F03-deploy.log`。

测试入站（r20f03 面板建）：VLESS 59924（id 958，idle=5）、VMess 59925（959，idle=5）、SS 59927（960，idle=5）、VLESS 59928（961，idle=10）、VMess 59929（962，idle=10）、VLESS 59930（963，idle=0）、VMess 59934（964，idle=0）。渲染核验：policy `{1:{connIdle:5}, 2:{connIdle:5}, 3:{connIdle:5}, 4:{connIdle:10}, 5:{connIdle:10}}`，`flmx-idle-958/959/960` 均带 `userLevel`。腿日志 `R20-F03-legs.log`（窗口 06:42:15–06:44:52Z）。

### 5a. 7 腿复测集（行为与 R20 一致——修复不改行为，属预期）

| 腿 | 结果（本轮） | R20 |
|---|---|---|
| A VLESS idle=5 1MB ×3 | 10.20 / 10.21 / 10.21s | 10.21 / 10.22 / 10.21s |
| A VMess idle=5 1MB ×1 | 3.14s | 3.10s |
| A SS idle=5 1MB ×1 | 3.11s | 3.10s |
| B′ VLESS idle=10 1MB ×2 | 20.24 / 20.22s | 20.20 / 20.24s |
| B′ VMess idle=10 ×1 | 3.16s | 3.11–3.21s |
| C′ VLESS idle=0 ×1 | STILL_OPEN @20s | STILL_OPEN @20s |
| C′ VMess idle=0 ×1 | 3.16s | 3.13s |
| D 静默 VLESS ×1 | 5.20s | 5.20s |
| D 静默 VMess ×1 | 1.11s | 1.12s |

### 5b. 相位腿（新证，窗口模型 vs “会话起算 2×”）

dummy 在 accept 后即发 1MB（burst1，保活过首格检查），再于 D 秒后发 4KB（burst2），随后 hold；客户端记 burst2 到达时刻与 EOF 时刻。N=5：

| 腿 | burst2 发出 / 客户端见 | CLOSED | 自末次数据起空闲 | 窗口模型预测 | “会话起算 2×”预测 |
|---|---|---|---|---|---|
| P-9.5（dummy 59942） | 9.50s / 9.85s | **15.21s** | **5.36s（≈N）** | ~15.2s，空闲 ~N | ~10.2s |
| P-5.5（dummy 59943） | 5.50s / 5.86s | **15.20s** | **9.34s（≈2N）** | ~15.2s，空闲 ~2N | ~10.2s |

两腿同在 ~15.2s（同一检查格线）关闭、空闲分别为 5.36s 与 9.34s——“会话起算 2×”预测的 10.2s 被双腿证伪；窗口模型（N–2N 区间、相位决定）双腿证实。注：burst2 发出到客户端见有 ~0.35s 面板侧传输延迟，空闲按客户端所见数据时刻计（用户面口径）。

### 5c. 离场恢复对账

测试入站 958–964 七个全删，inbound 回 578；脚手架 8 个进程按记录 PID 精确 kill（无 pkill），599xx 监听归零；agent/unit/gost 恢复 pristine（`aff338e3…` / `a12afd9c…` / `d48f09e1…`，328 行），面板 1.3.5 恢复（index md5 `0f189624…`，GET / 200）；计数 node=1 / tunnel=4 / forward=6 / inbound=578 / subscription=18，forward 状态全 1；表 50；gost 服务 12、climiters 仅 pristine 两项；r20f03 镜像已删；机上凭据已清。teardown 日志 `R20-F03-teardown.log`。

离场插曲两则（均已闭合）：
- 计数循环首跑 5 个 list 全数未落文件（循环内 stderr 被重定向丢弃，原因未直接留证；首要怀疑是切换回 1.3.5 后端后缓存 JWT 被拒，fresh login 即通）；凭据重传 + 可见输出重跑 6/6 通过（`R20-F03-counts-fixup.log`）。
- gost.json 在恢复后约 06:46:38 被 agent 以服务顺序调换重写（md5 `5f015682…`；服务集合、limiters、climiters 与 pristine 逐项相等，仅 `6_1_0_*` 与 `4_5_4_*` 两组服务换位，系 agent 重序列化顺序 artifact）。已停 agent、按 pristine 字节恢复、重启后 md5 回 `d48f09e1…` 且 60s 后仍稳定。

## 6. 未验证清单

- 浏览器 DOM 鼠标实点：未做（能力不可验）。新文案以 served chunk 直接散列验证（§5 部署段），未做人工点按。
- 文档站（VitePress）全站构建未在本地跑；本次文档改动为纯 prose，Go 回归测试已断言其关键句在位。
- Trojan 腿：本轮未复测（与 R20 同因：面板要求 TLS、裸客户端无 TLS 能力；R19 Trojan 静默 5.09s 仅作旁证）。本次修复不改行为，Trojan 计时路径不受影响。
- VMess/SS 约 3.1s 的上游行为未深究（R20 观察项，已登记，不立案）。

## 7. 交付

- 分支 `fix-r20-f03`；提交号见 lane 汇报（报告落盘先于提交，提交号不自指写入本文件）。
- 改动文件（`git diff --stat`，3 改 + 1 新增；禁用 `git add -A`，逐文件 add；`*.log` 以 `git add -f` 显式入库）：`vite-frontend/src/pages/inbound/inbound-form-modal.tsx`、`doc/guide/nodes.md`、`go-backend/internal/kernel/render_xray.go`、`go-backend/internal/kernel/r20_f03_test.go`（新增）；审计产物 `audit-out-132/r20/R20-FIX-F03-REPORT.md`（本报告）及 `R20-F03-*.log`（deploy/setup/legs/teardown/counts-fixup/test-red/test-green/baseline-gotest/postfix-gotest/postfix-rerun）。
- 未合 main、未打 tag、未发版。

— R20-FIX-F03-REPORT END · 自证：本行之前正文字节数 14280 · END
