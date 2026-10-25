# R18 修复计划（FIX-PLAN）

基线：main @ a0c18a0。依据：R18-X-ledger.md、R18-X-verdicts.md。修完开 R19 重审，不发版。

## 产品修复（4 项，全 P3）

### F-01：R18-X-01——删隧道 gost 分支中途失败须披露已删转发

- 对象：`tunnel_delete_resolution.go:766-780`（`cleanupForwardsDataPlane` gost 分支）。
- 改法：逐条清理改为 fail-closed + 回执披露——中途硬失败时回执列出已删转发数与标识，不得只称「隧道未删除」；已删项不回滚（与现有不回滚语义一致），只补披露。
- 回归：先红后绿——构造中途硬失败（第 N 条失败），断言回执含已删数与标识；成功路径与全删路径行为不变；`go test ./...` 产品失败 0。

### F-02：R18-X-02——管理 API 两处 services 盲 append 按名去重

- 对象：`x/api/config_service.go:85`（`createService`）、`:171`（`createServices` 第 4 阶段）。
- 改法：与 R17-X-01 同款——append 前按名查配置成员，已存在则跳过/覆盖，不得重复落盘。
- 回归：先红后绿——paused 重建、批内重复、毒化文件不再增份三形；入口仍不可达（部署三启用门不动）。

### F-03：R18-X-03——x/socket 三处盲 append 按名去重 + 自愈扩展到 chains/limiters

- 对象：`chain.go:35`、`limiter.go:29`、`:118`（`AddChains`/`AddLimiters`/`AddCLimiters`）。
- 改法：同款按名去重；启动自愈校验从仅 Services 扩展到 chains/limiters（去重告警、不退出，与 Services 同口径）。
- 回归：先红后绿——同名重复下发单份落盘；毒化 chains/limiters 文件启动自愈去重并告警；现有 Services 自愈行为不变。

### F-04：R18-X-04——x/api 其余 15 类 create 盲 append 按名去重

- 对象：admission/auther/bypass/chain/conn_limiter/hop/hosts/ingress/limiter/observer/rate_limiter/recorder/resolver/router/sd 共 15 类 create。
- 改法：与 F-02 同款，可与 F-02 同批施工、同批回归（逐类一形断言）。

## 流程项（1 项，不计产品收敛）

### P-01：R18-PROC-01——测试机 lane 间互斥排期

- R19 起测试机实行 lane 间互斥排期（协调者排期、独占窗口内他 lane 不得上线/重启/清文件）。
- L5 items 2–5（VLESS 用户凭证腿、SS 直连腿、Trojan 最小对照、下游供数/1MB 批量腿）与 kill-freeze 改期独占窗口重跑。

## 执行要求

- 分支 `fix-r18`（独立 worktree；禁用 `git add -A`，只 add 明确文件；每修复带先红后绿回归测试；修后 go build/vet/test + 前端 tsc 全绿，产品失败 0）。
- 无 BC 项。R17 修复报告 §10 剩余未验证项（远端/非 Debian 内核、浏览器 DOM 腿、订阅 1 流量计数器响应性）继续结转 R19 登记。
- 修复报告落盘 audit-out-132/r18/（字节数 + 尾行 END 自证）；R18 审计产物随本次提交归档进库。
— R18-FIX-PLAN END · 自证：本行之前正文字节数 2945 · END
