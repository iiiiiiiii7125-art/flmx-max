# R18 裁定（X-verdicts）

基线：main @ a0c18a0。裁定只认落盘原始日志 + 名单存档；摘要计数不得作裁定依据。

## V-01：R17-X-01 CLOSE；R17D-OPEN-01 SOLVED，不再结转

L1 四项复核无 FAIL（机理、去重 12/12、自愈一次起成 NRestarts=0、harness 10/10 MISS、计时 8 腿全过）。OPEN-01 悖论已解：本轮对照组一次 redeploy 内天然形成正反两例——`3_5_3` 回退 sent-ok 后持久重复（14 服务、384 行、md5 `c26d7e04…`，50ms 轮询 766 次命中，随后一次重启即死——R17-X-01 全周期复现，新标本已归档）；`2_5_2` 回退 sent-ok 后无重复。分支机理已定位：毒化与否取决于 UpdateService 失败后回滚 restore 能否重新注册旧服务——restore 成功则 AddService 被第 1 阶段 `IsRegistered` 挡下（sent-ok 是 `tolerateExists` 掩盖），restore 失败则直通第 4 阶段盲 append。INT 的「49ms 窗口清理」假说不成立，作废。

## V-02：R18-X-01 立案，产品 P3

A4-008 携入未闭合，定级 P3 不变。依据：`tunnel_delete_resolution.go:766-780` 逐条先清数据面后删库，中途硬失败时已删转发数不披露；全仓 grep 证实无任何一轮 ledger/FIX-REPORT 关闭过 A4-008（协调者独立抽查：A4-008 仅见于 A4-forward.md 与 X4-docs-final.md 的披露修复清单，R14–R17 四轮总账/裁定/修复计划零命中；X2-datapath.md:150 仍登记为 △）。修复方向：fail-closed + 回执披露已删转发数/标识。

## V-03：R18-X-02 立案，产品 P3

两处 blind append 与 R17-X-01 同型（仅 registry `IsRegistered` 守卫、append 前不查配置成员、经 `OnUpdate→persist()` 落盘）。定 P3：管理 API 三启用门（`-api` / `GOST_API` / 配置文件 `api:` 段）在产品部署中全部关闭（安装脚本裸启动 `install.sh:476`、面板后端零引用只走 WebSocket、文档/compose/前端均无启用点），16 个引用点逐一枚举、0 可达；且 R17-A 启动自愈覆盖 Services（实测：暂停→重启→同名重建→`svc-a` 计数 2，自愈随即去重回 1 并告警）。修复口径：R17-X-01 同款按名去重。

## V-04：R18-X-03 立案，产品 P3

x/socket 3 处盲 append（`chain.go:35`、`limiter.go:29`、`:118`）经面板 WebSocket 常规下发可达，定 P3 不升 P2：触发机理未立（变更命令单队列串行、无 paused 式漂移），无实测复现。修复口径：同款按名去重 + 启动自愈扩展到 chains/limiters（现仅覆盖 Services）。

## V-05：R18-X-04 立案，产品 P3

x/api 其余 15 类 create 盲 append，与 R18-X-02 同一不可达 API，定 P3。修复口径与 R18-X-02 同款，可同批施工。

## V-06：INT「守卫日志 48 条」不可作关闭依据——证据级注记，不立案、不阻塞关闭

L1 复核：R17-FIX-INT-REPORT 的「48 条」无法从落盘证据重算（落盘仅 26 条、源头容器已删）。该数字不作为关闭依据；守卫功能成立的依据改以 R18 窗口新产生的 14 条完整判定链（send 7 / sent-ok 6 / sent-failed 1）。R17-X-01 关闭不受影响（其余三项验收均有独立新证据）。

## V-07：L2/L5「R17-FIX-A/B/C/INT-REPORT.md 仓内不存在」之说订正

协调者独立核实：四份报告均在 main @ a0c18a0 提交树内（`git ls-tree a0c18a0` 四条在位），仅当前工作树 HEAD（feat-latency-hint @ a99d68d）没有。L2 改以 FIX-PLAN + 合并提交 + 代码实读为准、L5 以 `git show a0c18a0:` 直读为准，两路结论不受影响。此条为记录订正，不立案。

## V-08：R18-PROC-01（流程 P1）：测试机 lane 间互斥排期

L5-CAND-01 采纳，转流程项。事实：Phase A 窗口（01:40–01:45Z）内他 lane 上线 r17d 面板、agent 二进制在 `aff338e3…`→`23aa260d…`→`ab628bcd…` 间切换、3 次非本路重启、A4 末 agent inactive、A6 窗口内毒化文件被他 lane 清理；本路 6 次重启亦可能污染他 lane 证据。处置：R19 起测试机实行 lane 间互斥排期；L5 items 2–5 与 kill-freeze 改期独占窗口重跑。流程面不计入产品收敛判据。

## V-09：L5 items 2–5「仍未验证」定性为排期阻塞，非技术不可验

VLESS 用户凭证腿、SS 直连腿、Trojan 最小对照、下游供数/1MB 批量腿四项未跑系测试机并行污染所致（V-08），R17 值（12.14s/12.11s、3.11/3.12s）不得冒充本基线值。结转 R19 独占窗口重跑。

## V-10：哨兵、常设全集、R13/FINAL-P2-1 维持结论成立

哨兵 51 PASS / 2 承继（S-16、S-34）/ 0 回退；常设全集六项 + 列表/详情对账差集全部 ∅；R13 关闭 7 项与 FINAL-P2-1 七段谓词维持关闭（L3 逐项点名）。VG-09 沿袭未验证（在盘 `snell-server` 0 命中），非新缺口，不立案。
— R18-X-verdicts END · 自证：本行之前正文字节数 4843 · END
