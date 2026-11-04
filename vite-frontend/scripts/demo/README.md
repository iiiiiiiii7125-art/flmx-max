# 在线演示的数据

在线演示（`VITE_DEMO=true` 构建，随文档发布在 `/flmx-max/demo/`）没有后端：`src/demo/index.ts` 在浏览器里用 `src/demo/fixtures.json` 回答所有接口。这些回答是从一个装好示例数据的真实面板上录下来的，页面改了接口以后按下面的步骤重新录一遍。

1. 用空数据库启动后端：`DB_PATH=/tmp/demo/gost.db SERVER_ADDR=127.0.0.1:16370 go run ./cmd/paneld`
2. 依次运行 `seed_1_inbounds.py`、`seed_2_relays_users.py`、`seed_3_subscriptions_history.py`，参数是数据库文件路径
3. 前端开发服务器指向这个后端：`VITE_API_BASE=http://127.0.0.1:16370 pnpm exec vite --port 3114`
4. 录制：`CHROMIUM=/path/to/chrome node record_pages.mjs raw.json`，再 `node record_items.mjs raw.json`（需要 `playwright-core`，自 R10X-016 起为 devDependency 锁定 1.55.0、进 package.json 与 pnpm-lock，见下方判读注记）
5. 生成：`python3 build_fixtures.py raw.json ../../src/demo/fixtures.json`。没有节点 agent 时录不到的延迟、回程、内核状态由它补上。生成前有两道闸。**通用 code 闸**（R9 基建批，J O-1 通则）：两册**每条**录制的响应 code 必须为 0，唯一例外是逐格白名单 13 格（admin `config/get` 配置不存在 3 格 + user `config/get` 配置不存在 4 格 + user 册 403 权限探针 6 格，逐格枚举在 `build_fixtures.py` 的 `CODE_WHITELIST` 与 `check-fixtures-content.mjs` 中、两处须逐格一致）；白名单增删须逐格登记理由、**不得整类放行**（不得按路径/code/msg 整类豁免）。**内容闸**（R7X-006，R8X-006 扩格）：关键键（admin `node/traffic/month`、两册 traffic 面）每条录制的 data 必须非空，admin `kernel/versions` 的 xray/singbox 两格必须是成功响应（code 0 + 非空版本清单），录到失败响应（如 GitHub 403）同样不过闸，不过闸不写文件——只对键集存在性对账放过了一次「种子从未产出该事实、重录把演示数据冲空」的回退，种子缺事实就补种子（如 `node_traffic` 当月行在 seed_3），不要在 fixtures 里手工补值销账；`kernel/versions` 录制时点须为版本发现链健康窗口，链不健康时等健康窗口重录，不得把失败响应固化入册。

演示里的时间会按“录制到现在”的间隔整体后移，所以不用经常重录。

## 纪律（不要破）

- **录制源头唯一**：只能对上面第 1 步的空库种子面板录制（后端地址在脚本里写死 `127.0.0.1:16370`）。对真实面板录制会把真实配置与密钥写进公开发布的 fixtures。
- **敏感键闸**：`build_fixtures.py` 落盘前会把后端敏感配置键（`jwt_secret`、`license_key`、`license_expiry`、`license_machine_id`、`machine_fingerprint`、`cloudflare_secret_key`、`node_sub_token`、`notify_telegram_bot_token`、`notify_webhook_url`，真源在后端 `config_policy.go`）的配置行整行置空并打印剔除了哪些键；随后做一遍校验扫描，任一敏感键仍带值、或命中拒绝清单（环境变量 `DEMO_FIXTURE_DENYLIST` 或本地未入库的 `denylist.local.txt`）就**失败退出、不写文件**。不要绕过这道闸。
- **订阅是我方模型**：`seed_3` 按 UserSubscription 分配模型造数（订阅建在 `subscription` 表、分配走 `/user-subscription/set`），没有上游的订阅模板与 `subscription.user_id`，不要从上游搬。

## CI 检查

CI 会打包演示站，用无头 Chrome 把每个页面（管理员和普通用户）走一遍：页面报错、被弹回登录页、或者请求了没录到的接口（控制台里的 `[demo] not recorded: …`）都会让 CI 失败。本地跑：

```bash
VITE_DEMO=true VITE_BASE=/flmx-max/demo/ pnpm exec vite build --outDir dist-demo
node scripts/demo/check.mjs dist-demo   # CHROME=/path/to/chrome 指定浏览器，口径见下行判读注记
```

**判读注记（R10X-016 / J-REG-01，浏览器版本口径写死）**：demo check 必须以**锁定版 Chromium** 运行——`playwright-core` **1.55.0**（`vite-frontend` devDependency、pnpm-lock 锁定）所锁定的 **chromium revision 1187（Chromium 140.0.7339.16）**；CI 在 demo check 步前安装该锁定版并以 `CHROME=<锁定版路径>` 注入执行行（`ci-build.yml` frontend job），本地复跑同口径。宿主机系统 **Chrome 152** 的 Local Network Access（LNA）拦截会使全部走查项（13 admin + 5 user 页 + 4 交互步，合计 22 项）假 FAIL，属**环境性质、不得据此判演示/产品回归**（`check.mjs` 自带的 `--disable-features=LocalNetworkAccessChecks` 在 152 上不足以解除）；以系统 Chrome 复跑得到的全败结果不作判读依据。录制面 `record_pages.mjs` 同经 playwright-core 选浏览器、同受此口径约束。发版门清单的 demo check 项固定注明此浏览器口径（chromium-1187），见 catalog 附则 B 第 4 条。

**demo check 覆盖面登记**（R9 基建批同步复核，与 K 域口径对齐）：`check.mjs` 的覆盖按**走查路径枚举**——admin 13 页（`ADMIN_PAGES`，R14 A10-13 补 `change-password`、分组页已随功能移除删除）+ user 5 页（`USER_PAGES`）逐页访问，外加 `INTERACTIONS` 四步交互（user monitor 的隧道页签、user my-subscription 的流量统计展开、admin config 的 Gist 上传——R3-C5-02 补入：填令牌＋密码后点「立即上传」，其 `backup/gist/upload` 回答为真实形状合成录制条目（result＋status 全字段，源头录制无法产生 GitHub 上传成功响应、按延迟/内核状态同例由合成补上），重录若冲掉该条目本步即红；admin tunnel 的删除隧道对话框——RR-C5-05 补入：两阶段，点首个可用的「删除」按钮等单删预览落定、再进批量选择模式开批量删除对话框等批量预览落定，各点「取消」退出；落定标记只认数据到达才出现的分支（单删的「发现关联规则/关联规则预览」、批量的「条仍被规则使用」——「可以直接删除」是预览为 null 时弹窗的瞬时默认分支，不得作落定标记），其 `tunnel/delete-preview` 与 `tunnel/batch-delete-preview` 回答由 adapter 按录制清单合成（`src/demo/index.ts` 的 `deletePreviewAnswer`），预览回退为 data:null 时对话框被页面自行关闭、本步即红）；断言面 = page error / 被弹回登录页 / `[demo] not recorded` / 空白页 / 交互控件缺失。**枚举外即盲区**：走查不打开的对话框与不点击的控件（典型：内核版本对话框）不在本闸覆盖内，其保真由内容闸（含通用 code 闸）与审计全集扫描兜底，不得把 demo check 通过表述为「演示全覆盖」。新增页面/交互面时须同步扩 `ADMIN_PAGES`/`USER_PAGES`/`INTERACTIONS` 并在本登记续行。RR-C5-05 同批另两面登记：`node/tune`（节点调优对话框）由 adapter 合成 agent 形状的状态回答（`tuneAnswer`：支持自动调优、全部分组已生效、尚无测速结果与任务），对话框不在走查点击面内，其保真由本登记与审计全集扫描兜底；`user/passkey/login/begin` 与 `user/passkey/register/begin` **登记为盲区、不合成**——begin 的 data 即 WebAuthn 仪式选项本体，而 finish 两面在演示里是「成功＋data:null」的写回答，合成 begin 会让真实认证器对演示域名签发真凭据、把 finish 推进假成功与空会话写路径，比仪式不可用更糟；且两册录制的 `user/passkey/status` 均为 `enabled:false`、UI 本就不呈现仪式入口。本登记的前提是该状态保持 false：若重录使其变 true，须先给 begin 补显性拒绝回答再发布。settle 为条件轮询口径（R9 基建批采纳条件化）：先等最小 settle（默认 2500ms，`DEMO_CHECK_SETTLE_MS` 可调），未就绪则轮询至上限（默认 10000ms，`DEMO_CHECK_SETTLE_MAX_MS` 可调）；高负载下单条时序 FAIL 先按此口径复跑判读，不得以拉长 settle 的诊断副本冒充本体通过。

**R4-C5 续行（覆盖面扩展与剩余盲区登记）**：`INTERACTIONS` 扩至九步。新增/扩展：admin config 步扩为五段链——Gist 上传（原有）→ 备份导出（选择并导出→全选→确认导出，`backup/export` 由 adapter 合成 attachment 回答：真实 BackupData 信封、版本 "1.0"，configs 段不合成以免外带录制配置行）→ 备份导入（选择并导入→全选→下一步选择文件，巡检经 CDP 把自备巡检备份文件喂给隐藏 file input——两用户一节点＋一条 legacy tunnelGroups，`backup/import` 合成回执按提交文件本身逐段计数、已移除段按后端原文点名 *Ignored/警告）→ 确认导入（落定标记认回执面板「共导入」行）→ Gist 恢复（刷新版本列表→恢复此版本→密码→恢复，成功整页刷新后认 sessionStorage 留存的「最近一次 Gist 恢复结果」回执面板）；admin node 步新增：节点行操作表→「安装命令」，`node/install` 合成示例命令串（落定认命令文本本身）；admin panel-sharing 步新增：导入远程节点全流程（`federation/node/import` 合成带 panelVersion 的回执，落定认弹窗关闭）；admin inbound 步新增：导入节点对话框粘贴一条分享链接提交（`inbound/import` 按非空行数合成 created，落定认弹窗关闭）；admin subscription 步新增：打开模板管理器认录制模板行呈现。adapter 同批合成（`receiptAnswer`，形状以行为探针逐项对真实回执核过）：`subscription/template/refresh` 回录制模板行（清 lastError、重盖 lastFetch）、`backup/gist/restore` 按录制清单计数的零跳过回执、`subscription/source/refresh` 零变更 sync 结果、`monitor/services/run` 一条成功结果行、`backup/gist/save` 按令牌/密码/间隔语义合成的 GistBackupStatus（不再回请求体 echo）；`backup/gist/versions` 录制册为空表（种子面板无真实 Gist 可列），同 upload 合成先例由 adapter 给出演示上传产生的那一条版本，否则恢复入口在演示站根本不渲染。**剩余盲区登记（两处，均为控件在演示数据下不渲染、非走查可达面）**：① 模板「立即刷新」点击——刷新按钮仅网址型模板有，录制册唯一模板为源码型、不渲染该按钮；其合成回答已由 adapter 形状兜底，待录制册出现网址型模板时把点击补进 subscription 步；② 节点操作表「监控」之后的服务监控「立即检查」——`monitor/services/run` 的合成回答已就位，但其入口在多级菜单链后（行操作表→监控弹窗→服务监控下拉），走查不驱动多级菜单链，其保真由 adapter 形状与审计全集扫描兜底。

新功能加了接口，失败信息会列出缺的路径：按上面的步骤重录，或者把新接口加进 `record_items.mjs`。新页面要加进 `check.mjs` 的页面列表。

另两道对已入库 fixtures 的常设检查（发版门判读；BG-01/R2-026 起已接入常设自动面——`scripts/run-checks.mjs` 递归枚举含 demo 子目录，两道随前端 check 全集在 CI 每次运行，下面的命令仍可单独手工复跑）：

```bash
node scripts/demo/check-shape-drift.mjs      # 声明形状：types.ts + api/index.ts 双源必填字段 × fixtures（R7X-007）
node scripts/demo/check-fixtures-content.mjs # 内容非空：关键键清单逐条 data 非空（R7X-006，与 build_fixtures 内容闸同口径）+ 通用 code 闸：全册每条录制 code 须为 0（逐格白名单 13 格除外，R9 基建批）
```
