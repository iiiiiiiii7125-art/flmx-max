# R19 修复计划（FIX-PLAN）

基线：main @ d66042c。依据：R19-X-ledger.md、R19-X-verdicts.md。修完开 R20 重审，不发版。

## 产品修复（3 项）

### F-01：R19-X-01——删隧道披露残留（nft 分支 + deleteTunnelByID）

- 对象：`tunnel_delete_resolution.go:789`（nft 分支删库失败返裸 err）、`:832` 与 `mutations.go:2609`（`deleteTunnelByID` 失败时「转发已全删」未披露）。
- 改法：与 R18-X-01 同款——fail-closed + 回执披露已删转发数与标识；已删项不回滚；nft 分支注明「规则已下线、库行已删」。
- 回归：先红后绿——构造 nft 分支删库失败与 deleteTunnelByID 失败两形，断言回执含已删数与标识；成功路径行为不变；`go test ./...` 产品失败 0。

### F-02：R19-X-02——nil 全族守卫 + OnUpdate 解锁（P2 优先）

- 对象：x/api 17 类 update+delete（34 处）+ pause/resume/status（6 处）；x/socket 9 处（updateChain/limiter/connLimiter/updateServices/deleteChain 等）；loader 17 类 parse；`config.go:135-137` 的 `OnUpdate` 非 defer 解锁。
- 改法：三件——① 各 update/delete 入口对 nil 项 fail-closed（拒绝或跳过并记日志，不得 panic）；② loader parse 滤 null（nil 项拒绝落盘，不得进自愈保留）；③ `OnUpdate` 改 defer 解锁（panic 后锁必释放，recover 不得 wedged）。
- 回归：先红后绿——L2 的 5/5 panic 形全部不再 panic；JSON null 落盘不再致启动崩溃循环；panic 后 `config.Set` 不死锁；`go test ./...` 产品失败 0。

### F-03：R19-X-03——孤儿 climiter 规则核查 + 清理（机理先行）

- 对象：`rule_conn_limit_12` 孤儿规则；删转发路径的 climiter 清理。
- 改法：先核机理——删转发是否清 climiter（复现：建带限流转发 → 删转发 → 查 gost.json 是否残留）；若确认，删转发路径补清 climiter；若机理不成立（残留另有来源），本项降级为观察、只清存量孤儿。
- 回归：先红后绿——删转发后 gost.json 无孤儿 climiter 规则；`go test ./...` 产品失败 0。

## 测试基建（1 项，不计产品收敛）

### T-01：R1B3 测试补 env 加命中/文案断言

- 对象：`TestR1B3TunnelDeleteOnlineRealFailureAborts`。
- 改法：补 `FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true`；加伪装节点命中次数断言（≥1）与失败文案断言；并补一形批内同名第 2 阶段 fail-closed 回归钉子（V-08）。

## 流程项（1 项，不计产品收敛）

### P-01：R19-PROC-01——测试机锁原子化 + 操作前归属复核

- R20 起测试机锁抢占原子化（`mkdir` 式，建锁失败即等待、不得覆盖）；每次重启/部署前复核锁归属（锁内 lane 名须为本 lane）；窗口起止与 lane 归属写入各路报告首行；违反者该窗口证据作废重跑。

## 执行要求

- 分支 `fix-r19`（独立 worktree；禁用 `git add -A`，只 add 明确文件；每修复带先红后绿回归测试；修后 go build/vet/test + 前端 tsc 全绿，产品失败 0）。
- 无 BC 项。R19 未验证项（SS 直连腿方法失效、远端/非 Debian 内核、浏览器 DOM 腿、kill-freeze K2、VLESS 1MB 10.21s 观察）继续结转 R20 登记。
- 修复报告落盘 audit-out-132/r19/（字节数 + 尾行 END 自证）；R19 审计产物随本次提交归档进库。
— R19-FIX-PLAN END · 自证：本行之前正文字节数 3407 · END
