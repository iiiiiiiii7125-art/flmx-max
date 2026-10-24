# Gist 移植变更面重审（PORT-3342-REA-UDIT）

- 基线：main @ 7e5715a（047e1ef + Gist 集成 11 文件 +2294/−7；开工时 main HEAD 已为 7e5715a，与任务书基线一致，无偏差）。
- 依据：DELTA-3342.md、PORT-3342-G-backend/G-frontend/G-recovery/INT-gist 四份报告；本轮全部结论以集成树亲核为准（工作树 /tmp/gist-audit @ 7e5715a，只读探针，未改产品代码）。
- 审面：Gist 变更面及其邻接面（5 端点、8 键、T-4、信封、16 段恢复、尾钩子、O7-010、限额/白名单/tick/版本数、前端默认态、调优剔除）。

## 结论

**新增产品 P0 = 0、P1 = 0，符合可发布标准；新增 P2 = 0、P3 = 1。**

## 逐面核对（全部亲核在位）

| 面 | 结论 | 证据 |
|---|---|---|
| 5 端点与路由钉死值 202 | PASS | handler.go HandleFunc 201 行（含 gist 5 条 L266–270：status/save/upload/versions/restore）+ router.go /system-info 1 条 = 202；INT 报告算式复核一致 |
| 双前缀镜像缺口 | 观察 O-1（非缺陷，见下） | 文件备份 export/import/restore 有 /api/v1/api/v1 镜像（handler.go L273–275，自 v1.0.0 沿袭）；gist 5 端点无镜像；前端 gist 调用全为单前缀（api/index.ts L604–630），无调用方走双前缀，故无用户可达路径受影响；若日后补镜像，钉死值须从 202 重钉 |
| 8 config 键 | PASS | gist_token、gist_password、gist_id、gist_interval_hours、gist_last_at、gist_last_error、gist_last_hash、gist_last_upload，共 8 键 |
| sensitive + systemManaged 双表隔离 | PASS | repo/config_policy.go：敏感集（L55–56）含 token/password；systemManaged 集（含 8 键全部）禁通用 config/update 写入、FilterBackupConfigs 排除在备份 configs 段外；handler.go getConfigs L686–687 即使对管理员亦 delete token/password；status 端点仅回 tokenHint |
| T-4 明文拒绝双侧 | PASS | gistUploadLocked 开头门禁（gist_backup.go L357–359，位于导出与跳过判断之前，零 GitHub 请求）；手动上传经 gistUpload → 同门禁；自动循环经 runGistBackupLoop → gistDue → gistUpload → 同门禁；前端保存提交拒绝（无密码且非清令牌时 toast 拒）、上传按钮禁用、「不再加密」入口已删 |
| scrypt + AES-256-GCM 信封双认 | PASS | gistOpen（L165–180）：raw["flyx"] 或 raw["flmx"] 等于信封格式即解密，否则按明文原样返回；写侧仍写 flyx（线格式与上游互通，注释已钉死不得「修正」）；参数 scrypt N=32768/r=8/p=1 + AES-256-GCM 与上游逐字节一致 |
| 16 段恢复全集（T-3） | PASS | gistRestoreTypes（L655–660）16 值逐值与任务书清单一致，未用上游 11 项表 |
| 尾钩子同一函数复用 | PASS | gist 恢复（L720）与文件导入（handler.go L2171）调同一 runBackupImportTailHooks（L2184 起：计量对账、configs 缓存失效、内核 reconcile、撤销清理、转发重推、AutoBackup）；无第二份手写尾钩子 |
| FINAL-P2-1 七段谓词不动 | PASS | backup_import_repush.go 对 047e1ef diff 为空 |
| O7-010 恢复前自动备份失败语义 | PASS | 恢复前 ExportAllContext 失败只记日志、autoBackup 置 nil、继续导入（L712 附近）；未照抄上游失败即中止语义 |
| 20MB 上限 / 间隔白名单 / 5 分钟 tick / 最近 10 版本 | PASS | 超 20MB 拒（L318）；gistIntervals = {0,6,12,24,168}，save 端强制校验（非法拒）；gistCheckInterval = 5 分钟；versions 取 history 前 10 条即 break，committedAt 转 unix ms |
| 前端 T-2 默认关 / T-1 个人 token 文案 | PASS | intervalHours 初始值 0（INTERVALS 首项「关闭」），status 载入后以服务端为准；文案「个人账号 classic token 需 gist 权限，保存后不再显示」「默认关闭：未开启前不向外上传」；5 端点全部 isAdminRequest（L470/485/542/570/680） |
| 调优剔除 | PASS | TuneProbe、bdpTune、tuneSizesFor、defaultTuneParams、bdpTuneMinAgentVersion、tuning-params、TuneParams、lineSupported 全树 0 命中；tune-modal.tsx 对基线 diff 0 行；doc/ diff 0 行（DOC 分支本次未合入，见未验证） |
| 独立复跑 | PASS | go build ./... exit 0；TestGist 靶向本轮独立复跑 21/21 PASS、FAIL 0（与 INT 报告一致） |

## 新增立案

### REA-P3-1：自动循环在「令牌+间隔已设、密码被清」态下可达，每配置间隔一次失败尝试、失败通知至多 1 条/30 分钟（P3）

【R14 订正 2026-10-25】原标题“每 5 分钟发一次失败通知”及下文“enqueueNotify 无去重窗口”失实，见 R14-A3 §A3-02 机理订正（探针实证：同指纹两次 dispatchNotify，第一次 Sent、第二次 Suppressed；`notify.go:227 notifyDupWindow = 30 分钟`）。

- 机理：runGistBackupLoop 只认 gistDue（令牌 + 间隔），不认密码；该态下到期时走 gistUpload → T-4 门禁拒绝 → log + notify（notifyEventBackup「Gist 备份失败」）；失败刷新 lastAt，下一次尝试按配置间隔再到期（非每 5 分钟一轮）；通知经 dispatchNotify 30 分钟去重窗，同一失败文案至多 1 条/30 分钟；lastError 每次失败改写，直至人工干预。
- 可达性：前端 save 已挡（无密码且非清令牌时拒绝提交），但后端 save 允许 ClearPassword 与 interval 并存（save 端无「清密码时 interval 须归 0」守卫），故该态经直接 API 调用可达；UI 常规路径不可达。
- 定 P3 非 P2：无数据丢失、无错误状态、无凭据外泄，仅通知噪声 + lastError  churn；修复建议（不阻塞发布）：save 端清密码时若 interval > 0 一并拒绝或归 0，或循环内密码缺失时只记 lastError 不通知。是否修由发版方定，不回填本计数。

## 未验证专节

1. 真实 GitHub Gist 端到端（上传/版本列表/恢复）：本轮测试均为 httptest mock，无真实令牌；save 端令牌校验调 live GitHub API 的行为亦未实测。留给发版门（须真实个人 token 持有者操作）。
2. 长定时循环实测：5 分钟 tick + 间隔到期仅 gistDue 单测覆盖，无数小时真实等待。
3. 前端恢复点按：未做 live-browser 鼠标实点；证据为接线开关 RESTORE_WIRING_PENDING = false + restoreGistBackup 调用链 + 成功 toast/刷新逻辑。
4. DOC 文档批（Gist 指南 + BC 草稿）本次集成未合入，文档后补合入；发版说明汇编时须以 DOC 分支内容为准复核，调优文档一律剔除。
5. 双前缀镜像（O-1）：按无镜像交付；若发版门决定补，路由钉死值须从 202 重钉并同步文档。

## 收敛影响

本轮 P2/P1 = 0，不改变 R12+R13 收敛结论；Gist 为 1.4.5 新增变更面，本重审即其变更面重审，审过后可进字面量同步与发版汇编（DOC 后补合入与发版门真实 Gist 端到端仍为前置项）。

— PORT-3342-REA-UDIT END · 基线 main@7e5715a · 新增产品 P0/P1=0、P2=0、P3=1 · 未改产品代码 · 未打 tag · 未发版 · END
