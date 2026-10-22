# FLMX 1.3.7 发布报告

- 版本：**1.3.7**（R3 审计修复版；R3 总账 32 条 32/32 关闭，发版门两轮——首轮不放行 → 定点修复 3 项 → 复验全 PASS 放行——后按用户预授权链发版）
- Release：https://github.com/iiiiiiiii7125-art/flmx-max/releases/tag/1.3.7
- 状态：**Published**（非 draft / 非 prerelease），发布时间 `2026-10-22T21:03:51Z`，target `4662af8`
- 发版说明：`audit-out-132/r3/RELEASE-NOTES-1.3.7.md`（凭据节已回填）

## 1. 提交链

| 提交 | 内容 |
|---|---|
| `2356a3e` | merge integrate-r3（`integrate-r3 @ bf4423d` 合入 main；`f5d5fa2` 为祖先，47 提交、90 文件 +6187/−277，零丢提交、零冲突） |
| `801da26` | 版本字面量同步 1.3.6 → 1.3.7（`skills/flmx-api/package.json`、`SKILL.md`、`doc/ai-skill.md` 示例 tag 两处，共四处）；`scripts/check-doc-consistency.sh` PASS（三处一致） |
| `4662af8` | 发版说明定稿（tag `1.3.7` 指向点；定稿落盘后已做字节数 + 尾部探针复核，17626 字节完整无截断） |
| `2a889ff` | 发布后凭据回填（发版说明凭据节写入 CI run 号与资产核对结论；tag 不动） |

仓库内 `PINNED_VERSION` 维持流水线口径（仓库副本为空、发布副本由 release job 改写），未手工编辑，与 AGENTS.md 反模式条一致。

## 2. CI 结论（如实记录）

| 工作流 | run | 结论 |
|---|---|---|
| Build and Push Images（tag 触发，主发版流） | `39852615134` | **success**（Check Version / Build & Push Vite Frontend / Build & Push Go Backend / Build & Compress GOST Binary / Create Release 五 job 全绿） |
| CI Build Check（main @ `4662af8`，attempt 1） | `39852611344` | **failure（单点）**，见 §3 定案 |
| CI Build Check（attempt 2，失败 job 复跑） | `39852611344` | **failure（同一单点）**，其余 5 job 两次均全 success |
| Publish Skill to npm（tag 触发） | `39852615163` | failure：Publish to npm 步 `ENEEDAUTH`（历版惯例红，不阻塞发布） |
| Deploy Docs（main 推送） | `39852611246` | **success** |

main CI 两次运行的 job 明细一致：Build Frontend **success**——其中 **`Check online demo` 步 success**（1.3.6 的单点红本版转绿，批 12 录制补齐 + check.mjs 新增交互步在 CI 实测通过）、**`Check contract fields` 步 success**（C7 口径对账案一落地，批 13）；Go Backend PostgreSQL Contract **success**（真 PG 实跑通过）；Build Go Backend、Build Agent、Test Installer Scripts（双 init 契约，含 Alpine 套件——本地环境门控未跑的一格由 CI 补齐）均 **success**。唯一红：Go Full Tests (backend + agent) 的一条契约用例，定案见 §3。

## 3. 异常及定案：main CI 单点红（契约用例时序竞态）

- 现象：两次运行均只有 `TestFederationRuntimeCommandPortRangeEnforcement`（`go-backend/tests/contract`）失败，签名逐字一致：`federation_dual_panel_contract_test.go:722: expected code 0 for in-range port, got -1 (msg: 节点不在线)`，用例耗时 0.15s / 0.11s。同用例中范围外端口的 403 断言（该用例的核心被测逻辑：联邦运行时命令的端口范围执法）两次均通过。
- 机理：该用例以 mock 节点经 `/system-info` WS 接入面板后**立即**发命令；范围内 AddService 的派发要求面板侧会话注册已完成，注册与首命令之间是测试侧固有竞态——`startMockNodeSession` 在客户端 dial 成功即返回，用例未按同文件 `waitNodeStatus` 的轮询模式等待注册就绪。CI 以 `go test ./...` 多包并行满载运行，注册 goroutine 调度延迟放大，竞态两连败；本地调度宽松时恒赢。
- 非本版引入的证据：① 受测代码路径本版零 diff——`federation.go`（runtime/command 处理）与 `handler.go` 的 `WebSocketHandler`/会话注册在 `f5d5fa2..4662af8` 间均未改动，R3 各批未触碰该路径；② 本地同 HEAD（`4662af8`）隔离证据三组：单用例 `-count=3` 与 `-count=10` 全过（合计 13/13）、2 核 CPU 饱和下 6/6 过、完整 `go test ./...` 全量套件（正确 TMPDIR 下）`tests/contract` 包 **ok（39.0s）**、handler 包 ok（268.5s），全量唯一红为已定性的沙箱 fake-IP DNS health 单格（环境红、与历轮口径一致，真实 DNS 已在 R2/R3 发版门实测放行）；③ 同型先例在案：1.3.4 发布时 CI 契约用例曾以「节点在线判定」时序 flake 定案，R3 修复报告 §6/§8 亦登记了同域负载敏感 flake（`TestFix2AOfflineRealmSwitchSettlesOnReconnect`，未改动基线同法可复现）并建议 deflake 立项。
- 影响面：仅 CI 信号；不影响发布镜像与面板功能——发版门已在真机以双面板 rig 实证联邦删除/编辑/回滚全路径，且本用例被测的端口范围执法逻辑（403 断言）在失败运行中本身是通过的。
- 定案：**测试侧既存时序竞态，非 R3 回归、非产品缺陷，不阻塞 1.3.7 发布**——主发版流 success、Release 已 Published 且资产全量核对通过（§4）。处置：登记 R4 deflake 立项（§5 第 7 项），与 R3 修复报告 §8 第 5 项的 handler flake 建议合并处理；本发布任务不擅自改判、不为过检临时改测试。

## 4. Release 资产核对（12/12）

12 个资产逐个下载、以发布 `.sha256` 逐个比对：**12/12 一致**。

| 资产 | 字节 | sha256（实测 = 发布值） |
|---|---|---|
| gost-amd64 | 9197776 | `f6d9780f92a57299a69f11c1f0c2fd6b214a55d31e5998a27a2daca87ac9f551` |
| gost-arm64 | 7383672 | `05421d1ddb3a61c778aa9aa1e5d059f901bd25346cdc954463b0cda1c6c10b5e` |
| install.sh | 30708 | `5620b8d74acf6099630b269d750f3b85890652e9e63d2ad6944827bef9db8dd7` |
| panel_install.sh | 62244 | `e1b2c90ac6f53bfc44344e62c831db2574622c32fca5a62f955cb5f431423d39` |
| docker-compose-v4.yml | 3509 | `29f908047ad988bd7d0c446183e8d9e60c0a94448c74381997d046a2a6c732a3` |
| docker-compose-v6.yml | 3569 | `b17eb9b85b689a8d70e39340a1ddf0a9d34472bd9ed7df1fe7361a9b0be1b0fc` |

发布副本内容核对：

- `install.sh` / `panel_install.sh`：`PINNED_VERSION="1.3.7"`（各一份、行首定版）、`REPO="iiiiiiiii7125-art/flmx-max"` ✓
- `docker-compose-v4.yml` / `docker-compose-v6.yml`：`flmx-flux-panel-backend:1.3.7`、`flmx-vite-frontend:1.3.7` ✓
- 发布 `gost-amd64 -V` 实跑：`gost 1.3.7 (go1.25.14 linux/amd64)`——正式版本注入，非 dev 占位 ✓
- GHCR 标签：当前 token 无 `read:packages`，API 返回 Not Found / 403，**未做 API 侧核对**（与历版同）；镜像推送以主发版流 Build & Push Frontend/Backend 两 job success 为据，不假报已核。
- 新头像随版生效：品牌提交 `f5d5fa2` 在本版基线内，发版门复验已实测候选构建 `/logo.jpg` 与新图逐字一致（md5 `91f2dc35f4dc79b5ca099782c2a8da97`），发布前端镜像由同一主发版流构建。

## 5. 遗留登记（转 R4）

1. **R3X-008 追溯升 P1（R4 台账动作）**：批 2 修复轮内实测官方 mihomo v1.19.33 对坏 SS2022 key 整份配置加载期 fatal（整表毒化坐实），触发 R3-X-verdicts 的升格条款。本版按现行总账以 P2 关闭（导入校验 + GATE-F1 渲染规范化后坏 key 无法入库、存量非规范 key 拉取即自愈，风险已消除）；R4 交叉复核须做定级修订登记，把 R3X-008 追溯升 P1 并回写总账级别列。
2. **sing-box 多段略过解除裁定**：SIP002/SIP023 多段 key 现口径为 Clash 面逐段归一、sing-box 面保守略过；GATE-F1 探针已实证 sing-box 1.14.3 对 Std 多段 `check` exit 0，解除与否的裁定材料已备，R4 裁定后落地（解除则 sing-box 面改逐段归一下发）。
3. **Surge/Loon/QX 多段残余**：多段 key 的非规范段在 Surge/Loon/QX/Shadowrocket 面维持原样下发（无官方二进制可证其解码口径），待 iOS 真机证据后 R4 评估是否同口径规范化。
4. **F2 登记 8 键 presence 立项评估**：入站更新的 `shareHost`/`remark`/三 bool/`whitelistExtra`/`outboundId`/`splitRules` 共 8 键维持现状语义（普通配置/全量替换科，未 presence 化，GATE-F2 批报告 §4 已明示口径）；R4 评估是否立项 presence 化。附：`security` 在场为空对象 `{}` 仍按目录默认的口径同批明示。
5. **项 5 分钟级持续性双主机专验（R4）**：发版门复验中，项 5 任务断言（初始物化、切隧道失败后 allow 保持切换前口径）已 PASS；但失败事件后约 +30–60s allow 集自 live nft 表消失、重保存入站后未即刻再物化——与首轮未解释观察、GATE-F3 §4 预登记同源，同机双 agent 共享 `table inet flmx_kernel` 的多路整表重建干扰未排除（含复验中段 Tailscale 通道中断约 5 分钟、逐秒归因留空）。R4 以双主机 rig 专验「allow 集分钟级持续性 + 重保存再物化」一环。
6. **R3 已接受口径的残余四项**（经 R3 双路复核仍成立、不升级，发版说明已向用户披露）：日报/台账 pending 为进程内存态，部分送达后重启可能重算重发；R2X-049 提交段复检在 PG Read Committed 下仍有理论窗口（裁定即以复检为止）；tunnelCreate 在联邦 CreateTunnel 后失败时远端预留无释放（既有同形态）；system_upgrade 版本 API 失败日志直印面板构造 URL 一处（无 token，观察级）。
7. **CI 契约用例 deflake 立项（本版新增）**：`TestFederationRuntimeCommandPortRangeEnforcement` 的 mock 会话注册竞态（定案见 §3）与 R3 修复报告 §8 第 5 项登记的 `TestFix2AOfflineRealmSwitchSettlesOnReconnect` 负载敏感 flake 合并，R4 评估统一 deflake（发命令/断言前按 `waitNodeStatus` 模式等待就绪；断言语义不得放宽）。
8. **F-S01 关闭回写（备查，非遗留缺陷）**：计数管线 PG 并发理论双计疑似已由发版门首轮项 11 硬探针实测关闭——测试机 PG16 上 `TestPostgresSeqConcurrentDoubleApplyContract` PASS（双并发同 seq 只入账一次，不触发升格立案）；R4 台账回写关闭状态即可，无代码动作。

## 6. 结论

FLMX 1.3.7 已正式发布：主发版流 success、Release Published、12/12 资产 sha 一致、发布副本版本注入正确；1.3.6 的两处 CI 红（demo 录制、C7 契约字段）本版在 CI 实测转绿。唯一异常为 main CI 的一条契约用例时序竞态单点红（§3 已定案、不阻塞发布）与 publish-skill 的惯例 ENEEDAUTH 红，均如实记录并转 R4 跟进。

—— 本报告以本行结尾（落盘字节数与尾行探针见交付汇报）。
