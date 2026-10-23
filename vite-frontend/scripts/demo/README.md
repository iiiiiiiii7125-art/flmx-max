# 在线演示的数据

在线演示（`VITE_DEMO=true` 构建，随文档发布在 `/flmx-max/demo/`）没有后端：`src/demo/index.ts` 在浏览器里用 `src/demo/fixtures.json` 回答所有接口。这些回答是从一个装好示例数据的真实面板上录下来的，页面改了接口以后按下面的步骤重新录一遍。

1. 用空数据库启动后端：`DB_PATH=/tmp/demo/gost.db SERVER_ADDR=127.0.0.1:16370 go run ./cmd/paneld`
2. 依次运行 `seed_1_inbounds.py`、`seed_2_relays_users.py`、`seed_3_subscriptions_history.py`，参数是数据库文件路径
3. 前端开发服务器指向这个后端：`VITE_API_BASE=http://127.0.0.1:16370 pnpm exec vite --port 3114`
4. 录制：`CHROMIUM=/path/to/chrome node record_pages.mjs raw.json`，再 `node record_items.mjs raw.json`（需要 `playwright-core`，本地工具、不进 package.json）
5. 生成：`python3 build_fixtures.py raw.json ../../src/demo/fixtures.json`。没有节点 agent 时录不到的延迟、回程、内核状态由它补上。生成前有一道**内容闸**（R7X-006）：关键键（admin `node/traffic/month`、两册 traffic 面）每条录制的 data 必须非空，不过闸不写文件——只对键集存在性对账放过了一次「种子从未产出该事实、重录把演示数据冲空」的回退，种子缺事实就补种子（如 `node_traffic` 当月行在 seed_3），不要在 fixtures 里手工补值销账。

演示里的时间会按“录制到现在”的间隔整体后移，所以不用经常重录。

## 纪律（不要破）

- **录制源头唯一**：只能对上面第 1 步的空库种子面板录制（后端地址在脚本里写死 `127.0.0.1:16370`）。对真实面板录制会把真实配置与密钥写进公开发布的 fixtures。
- **敏感键闸**：`build_fixtures.py` 落盘前会把后端敏感配置键（`jwt_secret`、`license_key`、`license_expiry`、`license_machine_id`、`machine_fingerprint`、`cloudflare_secret_key`、`node_sub_token`、`notify_telegram_bot_token`、`notify_webhook_url`，真源在后端 `config_policy.go`）的配置行整行置空并打印剔除了哪些键；随后做一遍校验扫描，任一敏感键仍带值、或命中拒绝清单（环境变量 `DEMO_FIXTURE_DENYLIST` 或本地未入库的 `denylist.local.txt`）就**失败退出、不写文件**。不要绕过这道闸。
- **订阅是我方模型**：`seed_3` 按 UserSubscription 分配模型造数（订阅建在 `subscription` 表、分配走 `/user-subscription/set`），没有上游的订阅模板与 `subscription.user_id`，不要从上游搬。

## CI 检查

CI 会打包演示站，用无头 Chrome 把每个页面（管理员和普通用户）走一遍：页面报错、被弹回登录页、或者请求了没录到的接口（控制台里的 `[demo] not recorded: …`）都会让 CI 失败。本地跑：

```bash
VITE_DEMO=true VITE_BASE=/flmx-max/demo/ pnpm exec vite build --outDir dist-demo
node scripts/demo/check.mjs dist-demo   # CHROME=/path/to/chrome 指定浏览器
```

新功能加了接口，失败信息会列出缺的路径：按上面的步骤重录，或者把新接口加进 `record_items.mjs`。新页面要加进 `check.mjs` 的页面列表。

另两道对已入库 fixtures 的常设检查（发版门判读）：

```bash
node scripts/demo/check-shape-drift.mjs      # 声明形状：types.ts + api/index.ts 双源必填字段 × fixtures（R7X-007）
node scripts/demo/check-fixtures-content.mjs # 内容非空：关键键清单逐条 data 非空（R7X-006，与 build_fixtures 内容闸同口径）
```
