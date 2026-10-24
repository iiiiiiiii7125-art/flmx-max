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

**判读注记（R10X-016 / J-REG-01，浏览器版本口径写死）**：demo check 必须以**锁定版 Chromium** 运行——`playwright-core` **1.55.0**（`vite-frontend` devDependency、pnpm-lock 锁定）所锁定的 **chromium revision 1187（Chromium 140.0.7339.16）**；CI 在 demo check 步前安装该锁定版并以 `CHROME=<锁定版路径>` 注入执行行（`ci-build.yml` frontend job），本地复跑同口径。宿主机系统 **Chrome 152** 的 Local Network Access（LNA）拦截会使全部走查项（14 admin + 5 user 页 + 2 交互步，合计 21 项）假 FAIL，属**环境性质、不得据此判演示/产品回归**（`check.mjs` 自带的 `--disable-features=LocalNetworkAccessChecks` 在 152 上不足以解除）；以系统 Chrome 复跑得到的全败结果不作判读依据。录制面 `record_pages.mjs` 同经 playwright-core 选浏览器、同受此口径约束。发版门清单的 demo check 项固定注明此浏览器口径（chromium-1187），见 catalog 附则 B 第 4 条。

**demo check 覆盖面登记**（R9 基建批同步复核，与 K 域口径对齐）：`check.mjs` 的覆盖按**走查路径枚举**——admin 14 页（`ADMIN_PAGES`，R14 A10-13 补 `change-password`）+ user 5 页（`USER_PAGES`）逐页访问，外加 `INTERACTIONS` 两步交互（user monitor 的隧道页签、user my-subscription 的流量统计展开）；断言面 = page error / 被弹回登录页 / `[demo] not recorded` / 空白页 / 交互控件缺失。**枚举外即盲区**：走查不打开的对话框与不点击的控件（典型：内核版本对话框）不在本闸覆盖内，其保真由内容闸（含通用 code 闸）与审计全集扫描兜底，不得把 demo check 通过表述为「演示全覆盖」。新增页面/交互面时须同步扩 `ADMIN_PAGES`/`USER_PAGES`/`INTERACTIONS` 并在本登记续行。settle 为条件轮询口径（R9 基建批采纳条件化）：先等最小 settle（默认 2500ms，`DEMO_CHECK_SETTLE_MS` 可调），未就绪则轮询至上限（默认 10000ms，`DEMO_CHECK_SETTLE_MAX_MS` 可调）；高负载下单条时序 FAIL 先按此口径复跑判读，不得以拉长 settle 的诊断副本冒充本体通过。

新功能加了接口，失败信息会列出缺的路径：按上面的步骤重录，或者把新接口加进 `record_items.mjs`。新页面要加进 `check.mjs` 的页面列表。

另两道对已入库 fixtures 的常设检查（发版门判读）：

```bash
node scripts/demo/check-shape-drift.mjs      # 声明形状：types.ts + api/index.ts 双源必填字段 × fixtures（R7X-007）
node scripts/demo/check-fixtures-content.mjs # 内容非空：关键键清单逐条 data 非空（R7X-006，与 build_fixtures 内容闸同口径）+ 通用 code 闸：全册每条录制 code 须为 0（逐格白名单 13 格除外，R9 基建批）
```
