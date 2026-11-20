#!/usr/bin/env bash
# check-doc-consistency.sh — 文档与代码一致性只读检查（审计 B1 批引入）。
#
# 覆盖：
#   1. nodes.md 订阅格式表 XHTTP 行 vs kernel/share.go 实际渲染（xhttp-opts）
#   2. skills/flmx-api 接口清单 vs handler.go＋router.go 路由集 diff（整域缺失即失败）
#   3. 技能包版本字面量三处一致（SKILL.md / package.json / doc/ai-skill.md 发布示例）
#   4. install.md 维护菜单 vs panel_install.sh show_menu 实际菜单
#   5. install.md Caddy 示例反代目标 vs 脚本刻意使用的 127.0.0.1
#      （双侧，G-R4-04：反面查无 localhost、正反面查含 127.0.0.1、
#      脚本侧查生成行——只查反面时示例整行被删仍全绿）。
#   23. 本轮文档断言兜底（R2-R4 G-R4-02）：install.md 子网示例值、
#      身份六键清单、notify.md 机器人菜单小节、upgrade.md:83
#      V1-01 残留段——四处真值已人工核对通过，本节守「改动漂移即
#      红」，每项双侧对账（文档声称 vs 模板/脚本/代码实值）。
#   24. F1-N2 回归测试在场门（R2-R4 G-R4-03）：test-install-scripts-
#      proxy.sh 须含 report_compose_up_failure 双分支回归测试且已
#      在文件末尾的执行清单里注册——测试被删或漏注册即红（CI
#      install-scripts job 跑本文件，测试本身即执行面）。
#   25. S05 前端计量默认开门（D-R7-01 常设门）：转跑
#      scripts/check-hist-metering.sh——新建入站表单 emptyForm 须默认
#      meteringEnabled=true 且 toPayload 须按 SS2022 适格性设防（Issue B
#      根因面）；本门此前全仓无接线，在此转跑才有 CI 执行点。
#   6. [known-pending] 默认账号字面量：guide vs seed/脚本——F-064 属 §十一.4
#      待产品决策，本项只报告、不计入失败（决策落地后应改为硬检查）；
#      但任一侧提取为空属前提失效、点名硬红，不得伪装一致
#      （RR4-C6-I03）。
#   17. 初始口令口径门（FIX-DOC 常设门）：用户面（getting-started.md、
#      install.md、README.md）不得再宣称固定默认口令形态
#      「admin@user / admin@user」；三处必须各含「没有固定默认值」
#      口径字面（与 initial_admin.go 的随机播种实现对齐）。提取/
#      命中为空属前提失效、点名硬红。§6 只对账用户名字面量，
#      本项对账口令口径，两门互补。
#   19. 计量伴生端口口径门（FIX-DOC-R 常设门）：subscription.md
#      伴生端口文案必须为实现口径 [20000, 60000)，且代码面
#      pickFreePortWithFallback 的实参必须仍是 (20000, 60000)
#      （kernel.go）——F1-03 上界漂移（文档 59999 vs 实现 [20000,
#      60000)）防复发。
#   20. 监控目标口径门（FIX-DOC-R 常设门）：monitoring.md 必须同
#      时含两面口径——本机面「非公网全集」拒绝＋经节点面「只拒硬
#      拒集」——且代码面 monitor_ssrf.go 两函数同在、本机面注释
#      仍含 NonPublic 全集口径、节点面注释仍含「只补硬拒子集」。
#      F2-01 的旧形态（两面混写成只拒硬拒集）防复发。
#   21. 服务监控限值披露门（FIX-DOC-R 常设门）：monitoring.md
#      「系统级限值」行须逐项披露 7 个 service_monitor_* 键的缺省
#      与上限（扫描 1s / 并发默认 20 上限 50 / 间隔最小·默认 1s /
#      超时最小 1s·默认 5s·最大 60s、三超时键保存 ≤60），文案关键
#      数字与 limits.go 的 DefaultServiceMonitorLimits 及域上限
#      （MaxServiceMonitorWorkerLimit=50 / MaxServiceMonitorTimeoutSec
#      =60）机械对账——F2-03 披露段防漂移。
#   7. notify.md Webhook `event` 取值枚举 vs handler notifyEvent* 常量全集
#      （R7-M-01 常设门：文档自称列全的枚举与代码常量集 diff 必须为 ∅）。
#   8. 超时预算登记表 vs 产品代码（R8 T-GATE 常设门：转跑
#      scripts/check-timeout-budgets.sh，前端超时/服务端放宽/后端界三层
#      与 doc/audit/timeout-budgets.tsv 逐格对账，漂移点名失败）。
#   8b. nginx 代理超时 vs 服务端写截止（N06 常设门：转跑
#      scripts/check-nginx-timeouts.py，25 路按 nginx 最长匹配逐路
#      对账，服务端值取自 write_deadline.go / gh_mirror.go 具名常量）。
#   9. 安装脚本打印的文档站链接 vs doc 路由表（X05-001 常设门：
#      panel_install.sh / install.sh 中本站 github.io 链接逐个按
#      doc 结构 + cleanUrls 规则映射，映射不到即失败——成功提示曾
#      打印 guide.html，该地址在 cleanUrls 下必然 404）。
#   10. 易漂移计数对账（X05-OPT-01 / R3-K11 常设门）：文档与注释中写死的
#      TestPostgres* 用例数、go-gost/x 有/无测试包数，须以方括号标记
#      [K11-COUNT:<键>=<数字>] 声明（标记藏于 HTML/YAML 注释内、渲染
#      不可见），本门机械实数与声称值对账，不符即失败。新机制合入若
#      新增同类写死计数，必须改用标记形态并在本门登记对账键；裸数字
#      计数无机械门可守，只能靠审计枚举人工复扫（立此规矩防 K10 式
#      「新增 4 个 PG 契约用例、文档仍写 6 个」的静默漂移复发）。
#      x 包计数需 go 工具链（go list）：CI 的 frontend job 跑本门时
#      GitHub 托管镜像自带 go；本地无 go 时该子项以 PENDING 显式报告
#      未核（不计失败、不得静默跳过）。TestPostgres 子项自修复轮 7
#      起同理由 doc-extract 按编译集（go/ast＋构建约束）统计，无
#      go 时同样 PENDING 未核（R7-27；此前为纯 grep 恒硬核）。
#   11. panel_install.sh SCRIPT_VERSION 字面量门（L2-30 常设门）：
#      self_update_check 以 SCRIPT_VERSION 相等为「无更新」短路，字
#      面量缺失/重复/格式漂移会让脚本自更新静默失效。硬核：定义恰
#      一处、格式 YYYYMMDD.NN 定宽、日期段为合法年月日。install.sh
#      无此概念（版本跟随 PINNED_VERSION），不在本门。
#   12. 私网段集常设门（R8-004 / R8-022）：转跑
#       scripts/check-private-ranges.sh，私网拦截正本段集两处拷贝
#       逐段一致才算通过。
#   13. 站内锚点对账门（R8-L7-02 常设门）：VitePress 构建不校验锚点，
#      链接 slug 写错时页面可达但落不到节。转跑
#      scripts/check-doc-anchors.mjs：机械复算 doc 全部标题 id 集，
#      与全部站内链接的目标锚点逐条对账，差集非空即失败。需 node
#      运行时；无 node 时以 PENDING 显式报告未核（不计失败、不得
#      静默跳过），判定本身不变。
#   14. 前端 check 全集（A-F-01 常设门）：转跑
#      vite-frontend/scripts/run-checks.mjs——机械枚举
#      vite-frontend/scripts/check-*.mjs 全集逐个执行（纯 ENC 预设、
#      敏感键、质量历史条、toPayload 设防、隧道质量开关、路由三清
#      单、文字对比度），任一红即本门红。此前前端 check 无统一入
#      口、门禁清单只跑仓库根 scripts/，check-pure-enc-preset 在集
#      成树上红而无人看见，故立此转跑项；无 node 时以 PENDING 显
#      式报告未核（不计失败、不得静默跳过），判定本身不变。前端
#      check 需 vite-frontend 的 node_modules（typescript）：CI 前
#      端 job 先 pnpm install 再跑本门，本地未装依赖时子脚本会以
#      非零码失败并点名，按其输出先装依赖再复跑。
#   15. 迁移预处理字面量对账门（RR-LIVE-01 常设门）：panel_install.sh
#      迁移预处理在副本上删的函数索引名与 schema_migration 版本字
#      面量，必须与后端 migration_k10_v5.go 的常量
#      usernameFoldIndexName / migrationK10V5Version 逐字一致——任
#      一侧改名而另一侧未跟，预处理会删错/漏删：漏删索引 pgloader
#      仍 KABOOM、漏删版本记录则 PG 侧函数索引静默缺失。
#   16. 技能包示例面对账门（R1-11-01 常设门）：§2 只核端点路径存
#      在性，示例里写错方法/虚构配置键时旧门全绿。本项两面：
#      (a) 技能包文档中的方法声明（标题行、`**Method:**` 行、
#      内联代码 `METHOD /api/v1/…`、curl `-X` 行）必须落入该路由
#      handler 的接受方法集——方法集由 doc-extract routemethods
#      按 AST 机械推导（守卫/switch/无语句=ANY），不许手抄清单；
#      参数化文档路径按最长已注册尾斜杠前缀路由归属。(b)
#      references/config.md 中呈现的配置键（反引号蛇形标识 + JSON
#      示例键）除信封白名单外必须全部 ∈ doc-extract configkeys
#      给出的 config_policy 分类键并集——虚构键（如旧文档的
#      site_name）即红。无 go 时两面同 §10 口径 PENDING 显式未核。
#   26. 技能包例文形态门（E-05-NEW-1/2 常设门）：tunnels.md
#      tunnel/create 例文的 inNodeId/outNodeId 须为含 nodeId 的对象
#      （裸数字会被后端 asMapSlice 静默丢弃致“入口不能为空”，面板
#      B live 复现）；forwards.md forward/create 例文不得含 speedId
#      （普通用户在场即拒，面板 B live 复现），字段表须标 admin-only。
#   27. 内核视图字段对齐门（E-02 常设门）：后端 kernelView 新增字段
#      须同步前端 KernelApiItem 类型与一处渲染点——companionSplitNote
#      三侧同在，后端加字段而前端无类型／无渲染即红。
#   28. 内核安装原子交换＋离线缓存门（C NEW-2 常设门）：nodes.md
#      的原子交换（只在替换件落盘验证后才换旧二进制）与离线重装
#      本地缓存安装包口径，须与 install.go 的 swapBinDir／
#      restoreInterruptedSwap／lookup+storeArchiveCache 三锚点同在。
#   29. 请求体例文地址门（R4D-01 常设门）：forward/create 类请求体
#      例文与 ai-skill.md 对话例的 remoteAddr／目标地址不得为私网
#      地址（普通用户照抄即 403 "resolves to internal IP"）；auth.md
#      的 list 响应回显（R4D-02 已裁定非问题）排除在外。
#   30. 改密请求体对齐门（R18-A5-01 常设门）：auth.md 的
#      updatePassword 请求体例文键集合须与后端 changePasswordRequest
#      的 json tag 集合一致（旧例文的 oldPassword 键后端根本不读，
#      缺 3 个必填字段，照抄即报“新用户名不能为空”）；skills 内不
#      得再出现 oldPassword。
#   31. 迁移镜像 digest 钉选门（R18-A2-02 常设门）：panel_install.sh
#      内全部 docker run 镜像引用必须带 digest——字面形态须含
#      @sha256:，或引用 FLMX_MIGRATE_*_IMAGE 常量（常量定义本身须
#      为含 @sha256: 的钉选值）；任一 docker run 行不满足即红。
#   32. 安装预检回归测试在场门（R18-A2-06 常设门）：2.1.18 共存
#      改造交付的预检函数组（panel_preflight_install_conflicts
#      及其 5 个探活/点名/选口助手）须在 test-install-scripts-
#      proxy.sh 中有非注释行按词引用——新增 panel_* 函数须有同
#      名测试引用，防再次漏测。只守本函数组：全仓 panel_* 函数
#      的通用门会把历史上大量无测试的助手一并判红，不属本项范围。
#   33. v6 判别锚对账门（R18-A2-11 常设门）：panel_install.sh
#      以 fd00:dead:beef 字面量判别部署是否 v6 世代，该锚须与
#      docker-compose-v6.yml 的 IPv6 子网前缀逐字一致；模板改
#      子网后存量 v6 部署更新时会误判为 v4、下载错世代 compose。
#
# 退出码：0 = 全部硬检查通过；1 = 存在不一致。只读，不修改任何文件。
set -u
cd "$(dirname "$0")/.." || exit 2

FAIL=0
note_fail() { echo "FAIL: $*"; FAIL=1; }
note_ok()   { echo "OK:   $*"; }
note_pend() { echo "PENDING(known, F-064): $*"; }

# 提取助手 doc-extract（修复轮 7，R7-27 / G-C4-10 机制处置）：
# §1 前提、§7 事件集、§10(a) 计数原以 awk 词法剥注释（go_strip）
# ＋grep 提取——剥离器保留原始字符串内容、文件枚举不认构建约
# 束，原始字符串里的声明形文本与 //go:build ignore 文件的文本
# 都能冒充编译集成员（把真实常量/函数搬进原始字符串、调用点改
# 字面量，集合与计数不变而真身已漂移）。本轮改走真解析器：
# scripts/doc-extract.go 以 go/ast 取声明、go/build 按构建约束
# 定文件集（即编译集本身）。无 go 工具链时三项以 PENDING 显式
# 报告未核（口径与 §10(b)/§12/§14 一致，不计失败、不得静默跳
# 过）；go 在场但助手构建失败属门禁自身缺陷，按前提失效硬红。
# go_strip 随三处提取面迁毕一并退役。
DOC_EXTRACT=""
EXTRACT_TMP=""
if command -v go >/dev/null 2>&1; then
  EXTRACT_TMP="$(mktemp -d)"
  trap 'rm -rf "$EXTRACT_TMP"' EXIT
  if go build -o "$EXTRACT_TMP/doc-extract" scripts/doc-extract.go 2>/dev/null; then
    DOC_EXTRACT="$EXTRACT_TMP/doc-extract"
  fi
fi

# sh_strip_comments：shell 词法剥注释（awk 状态机：单引号、双引
# 号、反斜杠转义；# 仅在词首——行首或空白/分隔符之后——起注
# 释）。供 §4 菜单提取：注释掉的 echo 行整体消失、与删除同效
# （修复轮 7，R7-08 / G-C4-02：§4 是轮 6 迁移时的枚举漏项，
# 当时只迁了 §1/§7/§10(a) 的 Go 面、本节 shell 面未迁）。
sh_strip_comments() {
  awk '
    BEGIN { sq = sprintf("%c", 39) }
    {
      line = $0; out = ""; n = length(line); st = 0
      for (i = 1; i <= n; i++) {
        c = substr(line, i, 1); nx = substr(line, i + 1, 1)
        if (st == 0) {
          if (c == "#") {
            prev = (i == 1) ? " " : substr(line, i - 1, 1)
            if (prev ~ /[[:space:];|&()]/) break
            out = out c
          } else if (c == sq) { st = 1; out = out c }
          else if (c == "\"") { st = 2; out = out c }
          else if (c == "\\") { out = out c nx; i++ }
          else out = out c
        } else if (st == 1) {
          out = out c
          if (c == sq) st = 0
        } else if (st == 2) {
          out = out c
          if (c == "\\") { out = out nx; i++ }
          else if (c == "\"") st = 0
        }
      }
      print out
    }'
}

# ── 1. XHTTP：share.go 输出 xhttp-opts 时，nodes.md 的 Clash/Shadowrocket 行不得再列 XHTTP 为「不包含」
#    前提沿革：初版无引号 grep 被注释散文提及自满足（R5-03 同族）；
#    第五轮改引号化 grep 后，第六轮复证把注释里的键名加上引号即可
#    再满足（R6-C4-12）——逐形态收紧关不死。第六轮改对 go_strip
#    剥注释后的代码面判定；第七轮（R7-27）再迁真解析器：前提＝
#    share.go 的 AST 中存在值恰为 xhttp-opts 的字符串字面量（注
#    释、原始字符串内的键名形文本均不是字面量节点）；发射行删除
#    时前提失效、整节按 fail-closed 报红。无 go 时 PENDING 未核。
#    第九轮（I-C6-03）再迁发射点断言：strlit 只证字面量存在——未
#    用变量持同值字面量一行即满足、不证发射（原 H-C4-06，历轮迁
#    移只换提取基底、断言逻辑型未变）。现前提＝keyemit：以
#    xhttp-opts 为键的 yamlField 复合字面量位于 append(...) 实参
#    子树内（share.go 渲染的真实发射形态）；未用变量与未追加的
#    yamlField 赋值均不满足，删发射行同样失效。
SHARE_GO="go-backend/internal/kernel/share.go"
NODES_MD="doc/guide/nodes.md"
xhttp_premise=""
if [ -n "$DOC_EXTRACT" ]; then
  if "$DOC_EXTRACT" keyemit "$SHARE_GO" "yamlField" "xhttp-opts" >/dev/null 2>&1; then
    xhttp_premise="yes"
  else
    xhttp_premise="no"
  fi
elif command -v go >/dev/null 2>&1; then
  note_fail "XHTTP 前提失效：doc-extract 助手构建失败，share.go 发射面未核（请人工复核）"
else
  echo "PENDING: XHTTP 前提未核（当前环境无 go 工具链，share.go 的 xhttp-opts 发射需 AST 提取）；请在装有 Go 的环境复跑本门"
fi
if [ "$xhttp_premise" = "yes" ]; then
  for row in "Clash / Mihomo" "Shadowrocket"; do
    line=$(grep -F "| $row |" "$NODES_MD" | head -1)
    if [ -z "$line" ]; then
      note_fail "nodes.md 订阅格式表找不到「$row」行"
    elif echo "$line" | grep -q "XHTTP"; then
      note_fail "nodes.md「$row」行仍把 XHTTP 列入不包含，但 share.go 已输出 xhttp-opts"
    else
      note_ok "nodes.md「$row」行与 share.go 的 XHTTP 渲染一致"
    fi
  done
elif [ "$xhttp_premise" = "no" ]; then
  note_fail "share.go 未找到 xhttp-opts（检查前提失效，请人工复核）"
fi

# ── 2. 技能包接口覆盖：注册面全部 /api/v1 路由必须在技能包或 ai-skill.md 中被引用
HANDLER="go-backend/internal/http/handler/handler.go"
ROUTER="go-backend/internal/http/router.go"
GH_MIRROR_GO="go-backend/internal/http/handler/gh_mirror.go"
# 路由提取四路并集：
#  (a) 逐行字面量 mux.HandleFunc("/api/v1/...", ...)——handler.go 与
#      router.go 两文件同扫（AG-04 起扩面：此前只扫 handler.go，
#      router.go 若新增 /api/v1 注册，AST 分类普查会强制其分类、
#      本门却不红，技能覆盖完备性靠约定兜底；router.go 现有注册
#      仅 /system-info 一条 mux.Handle，非 /api/v1 不入集）；
#  (a2) router.go 的 mux.Handle("...", ...) 字面量同理（与分类普查
#      的 .Handle/.HandleFunc 双算口径对齐）；
#  (b) 路由表清单行 {"/api/v1/...", h.<fn>}——B14-002 起备份端点改由同一
#      清单循环注册，逐行字面量提取识别不到表行（Phase E 补齐此路）；
#  (c) 表行派生的双前缀别名：循环体对每个表行另注册 "/api/v1"+path，
#      与表行同源派生——删一行则规范路径与其别名同时从路由集消失，
#      门禁两侧（未覆盖/幽灵）都应点名报红。
# 残余依赖（AG-04 明示）：本门是文本提取，只认字面量形态；注册面
# 之所以不会绕开本门，是因为 AST 分类普查（route_classification_
# test.go）以文件清扫＋非字面量白名单逐一相等守住「注册只许出现
# 在 handler.go/router.go 且必须字面量」——若将来有人放宽那道门
# 的注册面规则，必须同步扩本门提取面，否则技能覆盖会静默漏检。
table_routes=$(grep -oP '\{\s*"\K/api/v1/[^"]+(?="\s*,\s*h\.)' "$HANDLER" | sort -u)
routes=$( { grep -oP 'mux\.HandleFunc\("\K[^"]+' "$HANDLER";
            grep -oP 'mux\.HandleFunc\("\K[^"]+' "$ROUTER";
            grep -oP 'mux\.Handle\("\K[^"]+' "$ROUTER";
            echo "$table_routes";
            echo "$table_routes" | grep . | sed 's|^|/api/v1|';
            grep -oP 'ghMirrorPath\s*=\s*"\K[^"]+' "$GH_MIRROR_GO"; } | grep '^/api/v1/' | sort -u )
refs=$(grep -rhoP '/api/v1/[a-zA-Z0-9/_-]+' skills/flmx-api/ doc/ai-skill.md | sort -u)
missing=$(comm -23 <(echo "$routes") <(echo "$refs"))
if [ -n "$missing" ]; then
  n=$(echo "$missing" | wc -l)
  note_fail "技能包未覆盖 $n 条路由："
  echo "$missing" | sed 's/^/    - /'
else
  note_ok "技能包覆盖 handler.go＋router.go 全部 $(echo "$routes" | wc -l) 条 /api/v1 路由"
fi
# 反向：技能包不得引用不存在的路由（幽灵接口）
ghost=$(comm -13 <(echo "$routes") <(echo "$refs"))
if [ -n "$ghost" ]; then
  note_fail "技能包引用了不存在的路由："
  echo "$ghost" | sed 's/^/    - /'
fi

# ── 3. 技能包版本三处一致（SKILL.md / package.json / ai-skill.md 发布示例 tag）
v_skill=$(grep -oP 'version:\s*"\K[^"]+' skills/flmx-api/SKILL.md | head -1)
v_pkg=$(grep -oP '"version":\s*"\K[^"]+' skills/flmx-api/package.json | head -1)
v_example=$(grep -oP 'git tag \K[0-9][0-9.]*' doc/ai-skill.md | head -1)
if [ -n "$v_skill" ] && [ "$v_skill" = "$v_pkg" ] && [ "$v_skill" = "$v_example" ]; then
  note_ok "技能包版本三处一致：$v_skill"
else
  note_fail "技能包版本不一致：SKILL.md=$v_skill package.json=$v_pkg ai-skill.md示例=$v_example"
fi

# ── 4. 维护菜单：install.md 清单 vs panel_install.sh show_menu
# 脚本侧先经 shell 词法剥注释再提取（R7-08）：注释掉的菜单行与
# 删除同效，不再以原文形态留在提取面内。
menu_script=$(sed -n '/^show_menu()/,/^}/p' panel_install.sh | sh_strip_comments | grep -oP 'echo "\K[0-9]+\. [^"]+' | sort)
menu_doc=$(sed -n '/### 维护菜单/,/^## /p' doc/guide/install.md | grep -oP '^[0-9]+\. .*' | sed 's/[[:space:]]*$//' | sort)
if [ "$menu_script" = "$menu_doc" ]; then
  note_ok "install.md 维护菜单与 panel_install.sh 一致（$(echo "$menu_script" | wc -l) 项）"
else
  note_fail "install.md 维护菜单与 panel_install.sh 不一致："
  diff <(echo "$menu_script") <(echo "$menu_doc") | sed 's/^/    /'
fi

# ── 5. Caddy 示例：install.md 不得再写 reverse_proxy localhost（脚本刻意用 127.0.0.1 避免 IPv6 解析 502）
# 双侧（G-R4-04）：反面查无 localhost、正反面查含 127.0.0.1、脚本侧
# 查生成行——只查反面时，示例整行被删仍全绿（表象在场断言同族）。
if grep -q "reverse_proxy localhost" doc/guide/install.md; then
  note_fail "install.md Caddy 示例仍写 reverse_proxy localhost，脚本口径为 127.0.0.1"
elif ! grep -q "reverse_proxy 127\.0\.0\.1" doc/guide/install.md; then
  note_fail "install.md Caddy 示例缺 reverse_proxy 127.0.0.1 正面断言（示例行被删亦须红，请人工复核）"
elif ! grep -q "reverse_proxy 127\.0\.0\.1:" panel_install.sh; then
  note_fail "panel_install.sh 未生成 reverse_proxy 127.0.0.1:端口（脚本侧反代目标漂移，请人工复核）"
else
  note_ok "install.md Caddy 示例反代目标与脚本一致（127.0.0.1）"
fi

# ── 6. [known-pending] 默认账号字面量（F-064，§十一.4 待决策，只报告）
seed_user=$(grep -oP '"\Kadmin@[^"]+' go-backend/internal/store/repo/repository.go | head -1)
guide_user=$(grep -oP '默认账号 `\K[^`]+' doc/guide/getting-started.md | head -1 | awk '{print $1}')
if [ -z "$seed_user" ] || [ -z "$guide_user" ]; then
  # RR4-C6-I03：提取为空属前提失效——旧实现落入 else 分支打印
  # 「OK: 默认账号字面量一致：（空）」，提取失效伪装成一致。
  # 本项的不一致处置仍是 known-pending 只报告，但前提失效必须
  # 点名硬红（与 §7/§15 提取失败同口径），不得伪装 OK。
  note_fail "默认账号字面量检查前提失效：提取为空（seed=${seed_user:-（空）}、guide=${guide_user:-（空）}），请人工复核"
elif [ "$seed_user" != "$guide_user" ]; then
  note_pend "guide 默认账号为 $guide_user，seed 为 $seed_user（改文档还是改 seed 待用户决策）"
else
  echo "OK:   默认账号字面量一致：$guide_user"
fi

# ── 7. Webhook event 枚举：notify.md `event` 取值 vs 代码 notifyEvent* 常量全集（R7-M-01）
# 代码侧机械枚举全部 notifyEvent* 常量定义（排除测试文件）；文档侧取 notify.md
# 中以 "- `event`" 开头的那一行的全部反引号取值（首个 `event` 本身除外，
# 含行尾以「测试消息是 `test`」形式列出的 test）。
NOTIFY_MD="doc/guide/notify.md"
# 代码侧事件集由 doc-extract 按编译集枚举（R7-27：go/ast 取包级
# notifyEvent* 字符串常量的值、go/build 按构建约束定文件集；
# 注释掉的常量行、原始字符串里的声明形文本、构建排除文件均不
# 入集——R6-C4-14 的注释口径并入编译集口径）。助手异常（值非
# [a-z_]+ 形态等）按前提失效硬红；无 go 时 PENDING 未核。
# 发射面同遍受守（修复轮 9，R9-27）：发射入口与 notifyJob/
# notifyMessage 字面量的事件值若为字面量直发、或来源不可静态
# 证明为 notifyEvent* 常量，助手退出 3 点名——「事件只来自常
# 量」不再是隐含前提。
code_events=""
events_state="ok"
if [ -n "$DOC_EXTRACT" ]; then
  if ! code_events=$("$DOC_EXTRACT" events go-backend/internal/http/handler 2>"$EXTRACT_TMP/events.err"); then
    events_state="broken"
  fi
elif command -v go >/dev/null 2>&1; then
  events_state="buildfail"
else
  events_state="nogo"
fi
doc_events=$(grep -P '^- `event`' "$NOTIFY_MD" | grep -oP '`[a-z_]+`' | tr -d '`' | grep -vx event | sort -u)
if [ "$events_state" = "nogo" ]; then
  echo "PENDING: notify.md event 枚举未核（当前环境无 go 工具链，代码 notifyEvent* 常量全集需编译集提取）；请在装有 Go 的环境复跑本门"
elif [ "$events_state" = "buildfail" ]; then
  note_fail "notify.md event 枚举检查前提失效：doc-extract 助手构建失败（请人工复核）"
elif [ "$events_state" = "broken" ]; then
  note_fail "notify.md event 枚举检查前提失效：doc-extract 提取异常（$(cat "$EXTRACT_TMP/events.err")）"
elif [ -z "$code_events" ] || [ -z "$doc_events" ]; then
  note_fail "notify.md event 枚举检查前提失效（代码常量集或文档枚举为空，请人工复核）"
elif [ "$code_events" = "$doc_events" ]; then
  note_ok "notify.md event 枚举与代码 notifyEvent* 常量全集一致（$(echo "$code_events" | wc -l) 项）"
else
  note_fail "notify.md event 枚举与代码 notifyEvent* 常量全集不一致（< 代码有文档无 / > 文档有代码无）："
  diff <(echo "$code_events") <(echo "$doc_events") | sed 's/^/    /'
fi

# ── 8. 超时预算常设门（R8 T-GATE）：转跑 check-timeout-budgets.sh
# R5-D-01：该脚本 exit 2 为前提失效（/tmp 容量、node/typescript
# 缺失等），不是预算不一致——须按 exit 码分流：exit 2 报
# PENDING 未核、不计 FAIL；仅 exit 1（漂移/缺格）才报 FAIL。
# 此前不分 exit 码一律 note_fail，btrfs 上 inode 前置恒 exit 2
# 致全套 doc 门在磁盘 TMPDIR 下恒 FAIL。
bash scripts/check-timeout-budgets.sh > /tmp/.timeout-budgets.out 2>&1
tb_rc=$?
if [ "$tb_rc" -eq 0 ]; then
  note_ok "超时预算登记表与产品代码逐格一致（$(grep -oP 'timeout-budgets: \K[0-9]+' /tmp/.timeout-budgets.out) 格）"
elif [ "$tb_rc" -eq 2 ]; then
  echo "PENDING: 超时预算门前提失效（exit 2），预算一致性未核：$(grep -m1 '^FAIL: 前提失效' /tmp/.timeout-budgets.out | sed 's/^FAIL: 前提失效，//')；请按明细处理前提后复跑，不计 FAIL"
  sed 's/^/    /' /tmp/.timeout-budgets.out
else
  note_fail "超时预算登记表与产品代码不一致（明细见下）"
  sed 's/^/    /' /tmp/.timeout-budgets.out
fi
rm -f /tmp/.timeout-budgets.out

# ── 8b. nginx 代理超时与服务端写截止对账（N06，AUDIT-HIST L7）：
# 转跑 check-nginx-timeouts.py（25 路：N06 失配 13 条＋已对齐 12 条
# 回归网）。服务端值取自 write_deadline.go / gh_mirror.go 具名常量，
# 代理值按 nginx 最长匹配取 location 的 proxy 超时。
if python3 scripts/check-nginx-timeouts.py > /tmp/.nginx-timeouts.out 2>&1; then
  note_ok "nginx 代理超时与服务端写截止逐路对齐（$(grep -oP 'nginx-timeouts: OK \(\K[0-9]+' /tmp/.nginx-timeouts.out) 路）"
else
  note_fail "nginx 代理超时与服务端写截止失配（明细见下）"
  sed 's/^/    /' /tmp/.nginx-timeouts.out
fi
rm -f /tmp/.nginx-timeouts.out

# ── 9. 脚本文档站链接须命中 doc 路由表（X05-001）
# 路由映射规则（cleanUrls=true、base=/flmx-max/）：站点根 → doc/index.md；
# 路径 <p> → doc/<p>.md 或 doc/<p>/index.md；doc/public 下同名静态文件亦可。
# 映射不到即 404（如旧值 guide.html：doc 下无 guide.md、亦无同名静态文件）。
site_urls=$(grep -hoP 'https://iiiiiiiii7125-art\.github\.io/flmx-max[^"'\'' ]*' panel_install.sh install.sh | sort -u)
if [ -z "$site_urls" ]; then
  note_fail "安装脚本中未找到文档站链接（检查前提失效，请人工复核）"
else
  bad_urls=""
  while IFS= read -r u; do
    rel=${u#https://iiiiiiiii7125-art.github.io/flmx-max}
    rel=${rel#/}
    rel=${rel%/}
    if [ -z "$rel" ]; then
      [ -f doc/index.md ] || bad_urls="$bad_urls $u"
    elif [ -f "doc/public/$rel" ] || [ -f "doc/$rel.md" ] || [ -f "doc/$rel/index.md" ]; then
      :
    else
      bad_urls="$bad_urls $u"
    fi
  done <<SITEEOF
$site_urls
SITEEOF
  if [ -n "$bad_urls" ]; then
    note_fail "安装脚本打印的文档站链接无法映射到 doc 路由（线上将 404）：$bad_urls"
  else
    note_ok "安装脚本文档站链接全部命中 doc 路由表（$(echo "$site_urls" | wc -l) 条）"
  fi
fi

# ── 10. 易漂移计数对账（X05-OPT-01 / R3-K11）：TestPostgres* 用例数 + x 包计数
# 声称值只认 [K11-COUNT:<键>=<数字>] 标记（可多处声明，各处须彼此一致且等于实数）。
# (a) TestPostgres* 契约用例数：文档声称 vs tests/contract 的 func TestPostgres* 实数。
# 计数由 doc-extract 按编译集统计（R7-27：go/ast 数顶层 FuncDecl、
# go/build 按构建约束定文件集；注释掉的测试函数、原始字符串里
# 的函数形文本、构建排除文件均不计入——R6-C4-13 的注释口径并
# 入编译集口径）。自修复轮 9（R9-26）起递归子目录包，与
# go test ./... 的执行面一致（testdata 与 _/. 前缀目录两面同
# 跳过）。无 go 时 PENDING 未核（口径同 §10(b)）。
tp_actual=""
if [ -n "$DOC_EXTRACT" ]; then
  tp_actual=$("$DOC_EXTRACT" testfns go-backend/tests/contract TestPostgres 2>/dev/null | tr -d ' ')
elif command -v go >/dev/null 2>&1; then
  note_fail "计数对账前提失效：doc-extract 助手构建失败，TestPostgres* 用例数未核"
else
  echo "PENDING: 计数对账 TestPostgres* 子项未核（当前环境无 go 工具链，用例数需编译集提取）；请在装有 Go 的环境复跑本门"
fi
tp_claims=$(grep -rhoP '\[K11-COUNT:testpostgres=\K[0-9]+' doc/guide/postgresql.md AGENTS.md 2>/dev/null | sort -u)
if [ -z "$DOC_EXTRACT" ]; then
  : # 未核处置已在上方按环境分流报告（PENDING 或构建失败硬红）。
elif [ -z "$tp_actual" ] || [ "$tp_actual" = "0" ]; then
  note_fail "计数对账前提失效：go-backend/tests/contract 未数到 TestPostgres* 用例"
elif [ -z "$tp_claims" ]; then
  note_fail "计数对账：doc/guide/postgresql.md 与 AGENTS.md 未声明 [K11-COUNT:testpostgres=N] 标记（实数 $tp_actual，裸数字计数不受门守）"
elif [ "$tp_claims" != "$tp_actual" ]; then
  note_fail "计数对账：TestPostgres* 文档声称「$(echo "$tp_claims" | tr '\n' ' ')」≠ 实数 $tp_actual（doc-extract 编译集统计 go-backend/tests/contract）"
else
  note_ok "计数对账：TestPostgres* 用例数文档声称与实数一致（$tp_actual）"
fi
# (b) go-gost/x 包计数：ci-build.yml 注释声称 vs go list 实数（TestGoFiles+XTestGoFiles）。
if command -v go >/dev/null 2>&1; then
  x_total=$(cd go-gost/x && go list ./... 2>/dev/null | wc -l | tr -d ' ')
  x_tested=$(cd go-gost/x && go list -f '{{if or .TestGoFiles .XTestGoFiles}}1{{end}}' ./... 2>/dev/null | grep -c 1 | tr -d ' ')
  if [ -z "$x_total" ] || [ "$x_total" = "0" ]; then
    note_fail "计数对账前提失效：go list 无法枚举 go-gost/x 包"
  else
    x_untested=$((x_total - x_tested))
    x_claims_t=$(grep -hoP '\[K11-COUNT:x-tested=\K[0-9]+' .github/workflows/ci-build.yml | sort -u)
    x_claims_u=$(grep -hoP '\[K11-COUNT:x-untested=\K[0-9]+' .github/workflows/ci-build.yml | sort -u)
    if [ -z "$x_claims_t" ] || [ -z "$x_claims_u" ]; then
      note_fail "计数对账：ci-build.yml 未声明 [K11-COUNT:x-tested/x-untested=N] 标记（实数 有测试 $x_tested / 无测试 $x_untested / 共 $x_total）"
    elif [ "$x_claims_t" = "$x_tested" ] && [ "$x_claims_u" = "$x_untested" ]; then
      note_ok "计数对账：x 模块包计数与 go list 实数一致（有测试 $x_tested / 无测试 $x_untested / 共 $x_total）"
    else
      note_fail "计数对账：x 包计数声称 tested=${x_claims_t} untested=${x_claims_u} ≠ 实数 tested=$x_tested untested=$x_untested（共 $x_total）"
    fi
  fi
else
  note_pend "计数对账：当前环境无 go 工具链，x 模块包计数未核（TestPostgres 子项已在 (a) 处单独 PENDING）；请在装有 Go 的环境复跑本门"
fi

# ── 11. panel_install.sh SCRIPT_VERSION 字面量门（L2-30）
# 提取口径与 check-script-version-bump.sh 的 stamp_at 统一
# （RR4-C6-I01）：认 ^SCRIPT_VERSION= 前缀行、值剥可选双引号。
# 旧口径本项只数带引号行、无引号的第二行不入计数，而 bump 门
# 把它当印记行处理——两门盲区互补，第二定义可双双逃逸；统一
# 后任何第二行 SCRIPT_VERSION= 定义（带不带引号）都使计数为 2。
sv_count=$(grep -c '^SCRIPT_VERSION=' panel_install.sh)
sv_value=$(grep -m1 '^SCRIPT_VERSION=' panel_install.sh | sed 's/^SCRIPT_VERSION=//; s/^"//; s/"$//')
if [ "$sv_count" != "1" ]; then
  note_fail "SCRIPT_VERSION 门：panel_install.sh 中定义应恰一处，实数 $sv_count"
elif ! printf '%s' "$sv_value" | grep -qE '^[0-9]{8}\.[0-9]{2}$'; then
  note_fail "SCRIPT_VERSION 门：字面量格式须为 YYYYMMDD.NN 定宽，实际「$sv_value」"
else
  sv_y=$(printf '%s' "$sv_value" | cut -c1-4)
  sv_m=$(printf '%s' "$sv_value" | cut -c5-6)
  sv_d=$(printf '%s' "$sv_value" | cut -c7-8)
  if ! date -d "2000-01-01" >/dev/null 2>&1; then
    # BSD date 等不支持 GNU -d 的环境无从判定日期合法性：按本脚本
    # 惯例（同上计数对账项）降级 PENDING，不硬失败；判定本身不变，
    # 请在装有 GNU date 的环境复跑本门。
    note_pend "SCRIPT_VERSION 门：当前环境 date 不支持 -d，日期合法性未核（$sv_value）；请在装有 GNU date 的环境复跑本门"
  elif date -d "$sv_y-$sv_m-$sv_d" >/dev/null 2>&1; then
    note_ok "SCRIPT_VERSION 门：panel_install.sh 字面量格式合法（$sv_value，恰一处）"
  else
    note_fail "SCRIPT_VERSION 门：日期段不是合法年月日（$sv_value）"
  fi
fi

# ── 12. 私网段集常设门（R8-004 / R8-022）：转跑 check-private-ranges.sh
if bash scripts/check-private-ranges.sh > /tmp/.private-ranges.out 2>&1; then
  note_ok "私网拦截正本段集两处拷贝逐段一致（$(grep -oP 'private-ranges: \K[0-9]+' /tmp/.private-ranges.out) 段）"
else
  note_fail "私网拦截正本段集两处拷贝不一致（明细见下）"
  sed 's/^/    /' /tmp/.private-ranges.out
fi
rm -f /tmp/.private-ranges.out

# ── 13. 站内锚点对账（R8-L7-02 常设门，转跑 check-doc-anchors.mjs）
if command -v node >/dev/null 2>&1; then
  if node scripts/check-doc-anchors.mjs doc; then
    note_ok "站内锚点对账门：doc 全部站内链接锚点命中标题 id 集"
  else
    note_fail "站内锚点对账门：存在失效锚点（见上）"
  fi
else
  echo "PENDING: 站内锚点对账门未核（当前环境无 node 运行时）；请在装有 node 的环境复跑本门"
fi

# ── 14. 前端 check 全集（A-F-01 常设门，转跑 vite-frontend/scripts/run-checks.mjs）
if command -v node >/dev/null 2>&1; then
  if (cd vite-frontend && node scripts/run-checks.mjs); then
    note_ok "前端 check 全集门：vite-frontend/scripts/check-*.mjs 全数通过"
  else
    note_fail "前端 check 全集门：存在失败项（见上）"
  fi
else
  echo "PENDING: 前端 check 全集门未核（当前环境无 node 运行时）；请在装有 node 的环境复跑本门"
fi

# ── 15. 迁移预处理字面量对账（RR-LIVE-01 常设门）：脚本侧删除目标
#    vs 后端 v5 迁移常量（索引名 + 版本字面量逐字一致）。
mig_go_file="go-backend/internal/store/repo/migration_k10_v5.go"
mig_go_index=$(grep -o 'usernameFoldIndexName = "[^"]*"' "$mig_go_file" | head -n1 | cut -d'"' -f2)
mig_go_version=$(grep -o 'migrationK10V5Version = "[^"]*"' "$mig_go_file" | head -n1 | cut -d'"' -f2)
# 脚本侧 DELETE 语句嵌在单引号 docker 载荷里，SQL 引号以 '"'"' 转
# 义形态出现，先剥掉转义序列再按普通形态提取版本字面量。
mig_sh_version=$(grep -m1 'DELETE FROM schema_migration' panel_install.sh | sed "s/'\"'\"'/'/g" | grep -o "version='[^']*'" | sed "s/.*version='//; s/'$//")
if [ -z "$mig_go_index" ] || [ -z "$mig_go_version" ]; then
  note_fail "迁移预处理字面量门：后端 $mig_go_file 中 usernameFoldIndexName / migrationK10V5Version 常量提取失败"
elif ! grep -o 'DROP INDEX IF EXISTS [A-Za-z0-9_]*' panel_install.sh | awk '{print $NF}' | grep -Fxq "$mig_go_index"; then
  note_fail "迁移预处理字面量门：panel_install.sh 预处理未删后端函数索引 $mig_go_index（与 usernameFoldIndexName 不一致或缺失）"
elif [ "$mig_sh_version" != "$mig_go_version" ]; then
  note_fail "迁移预处理字面量门：panel_install.sh 预处理删除的 schema_migration 版本字面量「$mig_sh_version」与后端 migrationK10V5Version「$mig_go_version」不一致"
else
  note_ok "迁移预处理字面量门：panel_install.sh 删除目标与后端 v5 常量逐字一致（$mig_go_index / $mig_go_version）"
fi

# ── 16. 技能包示例面对账（R1-11-01 常设门）：方法声明 vs 路由方法集
#    ＋ config.md 示例键 vs config_policy 键集。
if [ -n "$DOC_EXTRACT" ]; then
  route_methods=$("$DOC_EXTRACT" routemethods go-backend/internal/http/handler 2>/dev/null)
  if [ -z "$route_methods" ]; then
    note_fail "技能包示例面门：doc-extract routemethods 提取为空（前提失效）"
  else
    # 文档方法声明提取（四形态）：标题行、`**Method:**` 行（归属
    # 于同文件最近标题路径）、内联代码 `METHOD /api/v1/…`、curl
    # `-X METHOD` 同行 URL。
    method_claims=$(for f in $(find skills/flmx-api -name '*.md' | sort); do
      awk -v file="$f" '
        /^#{2,4} / {
          cur = ""
          if (match($0, /(GET|POST|PUT|DELETE|HEAD)[[:space:]]+\/api\/v1\/[A-Za-z0-9\/._<>{}-]+/)) {
            s = substr($0, RSTART, RLENGTH); split(s, a, /[[:space:]]+/); cur = a[2]
            print file "\t" a[1] "\t" a[2]
          }
          next
        }
        /\*\*Method:\*\*/ {
          if (cur != "" && match($0, /(GET|POST|PUT|DELETE|HEAD)/))
            print file "\t" substr($0, RSTART, RLENGTH) "\t" cur
          next
        }
        /`(GET|POST|PUT|DELETE|HEAD) \/api\/v1\// {
          if (match($0, /(GET|POST|PUT|DELETE|HEAD)[[:space:]]+\/api\/v1\/[A-Za-z0-9\/._<>{}-]+/)) {
            s = substr($0, RSTART, RLENGTH); split(s, a, /[[:space:]]+/)
            print file "\t" a[1] "\t" a[2]
          }
          next
        }
        /-X (GET|POST|PUT|DELETE|HEAD)/ {
          m = $0; sub(/.*-X /, "", m); sub(/[^A-Z].*/, "", m)
          if (match($0, /\/api\/v1\/[A-Za-z0-9\/._<>{}-]+/))
            print file "\t" m "\t" substr($0, RSTART, RLENGTH)
        }
      ' "$f"
    done | sort -u)
    method_bad=0
    while IFS="$(printf '\t')" read -r cfile cmethod cpath; do
      [ -n "$cpath" ] || continue
      # 归属解析：精确命中优先；否则按最长已注册前缀（尾斜杠前
      # 缀路由承接 <param> 段）逐级回退。
      probe="$cpath"; set_found=""
      while [ -n "$probe" ]; do
        set_found=$(printf '%s\n' "$route_methods" | awk -F '\t' -v p="$probe" '$1 == p {print $2; exit}')
        [ -n "$set_found" ] && break
        case "$probe" in */*) probe="${probe%/*}" ;; *) probe="" ;; esac
        # 回退一层后只接受以 / 结尾的已注册前缀路由
        if [ -n "$probe" ]; then
          cand=$(printf '%s\n' "$route_methods" | awk -F '\t' -v p="$probe/" '$1 == p {print $2; exit}')
          if [ -n "$cand" ]; then set_found="$cand"; break; fi
        fi
      done
      if [ -z "$set_found" ]; then
        note_fail "技能包示例面门：$cfile 声明 $cmethod $cpath，但该路径无从归属任何注册路由"
        method_bad=1
      elif [ "$set_found" != "ANY" ] && ! printf '%s' "$set_found" | tr ',' '\n' | grep -Fxq "$cmethod"; then
        note_fail "技能包示例面门：$cfile 声明 $cmethod $cpath，但 handler 接受方法集为 $set_found"
        method_bad=1
      fi
    done <<EOF_CLAIMS
$method_claims
EOF_CLAIMS
    if [ "$method_bad" -eq 0 ]; then
      note_ok "技能包示例面门：$(printf '%s\n' "$method_claims" | grep -c .) 条方法声明全部落入路由方法集"
    fi
  fi

  config_keys_truth=$("$DOC_EXTRACT" configkeys go-backend/internal/store/repo/config_policy.go 2>/dev/null)
  if [ -z "$config_keys_truth" ]; then
    note_fail "技能包示例面门：doc-extract configkeys 提取为空（前提失效）"
  else
    # config.md 呈现键：反引号蛇形标识全集 + JSON 示例中行首
    # "key": 形态；信封/通用字段白名单不计。
    doc_config_keys=$( { grep -oP '`[a-z][a-z0-9_]+`' skills/flmx-api/references/config.md | tr -d '`';
                         grep -oP '^\s*"\K[a-z][a-z0-9_]+(?="\s*:)' skills/flmx-api/references/config.md; } | sort -u )
    key_allow="code data enabled id msg name time ts value"
    key_bad=0
    for k in $doc_config_keys; do
      case " $key_allow " in *" $k "*) continue ;; esac
      if ! printf '%s\n' "$config_keys_truth" | grep -Fxq "$k"; then
        note_fail "技能包示例面门：config.md 呈现了产品不存在的配置键「$k」（不在 config_policy 分类键集内）"
        key_bad=1
      fi
    done
    if [ "$key_bad" -eq 0 ]; then
      note_ok "技能包示例面门：config.md 呈现的 $(printf '%s\n' "$doc_config_keys" | grep -c .) 个键全部 ∈ config_policy 键集（$(printf '%s\n' "$config_keys_truth" | grep -c .) 键）"
    fi
  fi
else
  echo "PENDING: 技能包示例面门未核（当前环境无 go 工具链，routemethods/configkeys 无法提取）；请在装有 go 的环境复跑本门"
fi

# ── 17. 初始口令口径门（FIX-DOC）：用户面不得宣称固定默认口令，三处必含「没有固定默认值」
for doc_file in doc/guide/getting-started.md doc/guide/install.md README.md; do
  if grep -Fq 'admin@user` / `admin@user' "$doc_file"; then
    note_fail "初始口令口径门：$doc_file 仍宣称固定默认口令「admin@user / admin@user」（实现为随机初始口令，见 initial_admin.go）"
  elif ! grep -Fq '没有固定默认值' "$doc_file"; then
    note_fail "初始口令口径门：$doc_file 缺「没有固定默认值」口径字面（提取为空/口径漂移，请人工复核）"
  else
    note_ok "初始口令口径门：$doc_file 与随机播种口径一致"
  fi
done
# 双侧（G-R4-04）：文档口径须与 initial_admin.go 的随机播种实现对齐——
# 只查文档时，实现改回固定口令仍全绿。代码侧断言：crypto/rand 导入
# 在场且 generateInitialAdminPassword 内调 rand.Read。
INIT_ADMIN_GO="go-backend/internal/store/repo/initial_admin.go"
if ! grep -q '"crypto/rand"' "$INIT_ADMIN_GO"; then
  note_fail "初始口令口径门：$INIT_ADMIN_GO 未导入 crypto/rand（随机播种实现漂移，请人工复核）"
elif ! grep -q "rand\.Read" "$INIT_ADMIN_GO"; then
  note_fail "初始口令口径门：$INIT_ADMIN_GO 未调用 rand.Read（随机播种实现漂移，请人工复核）"
else
  note_ok "初始口令口径门：initial_admin.go 以 crypto/rand 随机播种"
fi

# ── 19. 计量伴生端口口径门（FIX-DOC-R）：文案区间与代码面下发实参同为
#    [20000, 60000)（F1-03 防复发）。
SUB_MD="doc/guide/subscription.md"
KERNEL_GO="go-backend/internal/http/handler/kernel.go"
if ! grep -Fq '在 [20000, 60000) 内随机取空闲端口' "$SUB_MD"; then
  note_fail "计量伴生端口口径门：subscription.md 伴生端口文案缺「[20000, 60000)」实现口径（上界漂移回 59999 式表述，请人工复核）"
elif ! grep -Fq 'pickFreePortWithFallback(20000, 60000' "$KERNEL_GO"; then
  note_fail "计量伴生端口口径门：$KERNEL_GO 未找到 pickFreePortWithFallback(20000, 60000 实参（实现漂移或提取失效，请人工复核）"
else
  note_ok "计量伴生端口口径门：subscription.md 文案与 kernel.go 实参同为 [20000, 60000)"
fi

# ── 20. 监控目标口径门（FIX-DOC-R）：monitoring.md 两面口径齐备且代码面
#    两校验函数与注释口径同在（F2-01 防复发）。
MON_MD="doc/guide/monitoring.md"
SSRF_GO="go-backend/internal/health/monitor_ssrf.go"
if ! grep -Fq '按**非公网全集**拒绝' "$MON_MD" || ! grep -Fq '只拒**硬拒集**' "$MON_MD"; then
  note_fail "监控目标口径门：monitoring.md 两面口径不全（本机面须「非公网全集」拒绝、经节点面须「只拒硬拒集」，F2-01 旧形态复发，请人工复核）"
elif ! grep -Fq 'func ValidateMonitorTCPTarget' "$SSRF_GO" \
     || ! grep -Fq 'func ValidateNodeMonitorTarget' "$SSRF_GO" \
     || ! grep -Fq 'NonPublic' "$SSRF_GO" \
     || ! grep -Fq '只补硬拒子集' "$SSRF_GO"; then
  note_fail "监控目标口径门：$SSRF_GO 两校验函数或注释口径提取异常（ValidateMonitorTCPTarget/ValidateNodeMonitorTarget/NonPublic/只补硬拒子集任一缺失，请人工复核）"
else
  note_ok "监控目标口径门：monitoring.md 两面口径与 monitor_ssrf.go 两校验函数一致"
fi

# ── 21. 服务监控限值披露门（FIX-DOC-R）：monitoring.md 披露行的键集与关键
#    数字同 limits.go 缺省/域上限机械对账（F2-03 防漂移）。
LIMITS_GO="go-backend/internal/monitoring/limits.go"
limits_line=$(grep -F '系统级限值' "$MON_MD" | grep 'service_monitor_' | head -1)
limits_keys_want=$(grep -oP 'ConfigServiceMonitor\w+\s*=\s*"\Kservice_monitor_[a-z_]+' "$LIMITS_GO" | sort -u)
def_block=$(sed -n '/func DefaultServiceMonitorLimits/,/^}/p' "$LIMITS_GO")
def_scan=$(printf '%s' "$def_block" | grep -oP 'CheckerScanIntervalSec:\s*\K[0-9]+')
def_worker=$(printf '%s' "$def_block" | grep -oP 'WorkerLimit:\s*\K[0-9]+')
def_minint=$(printf '%s' "$def_block" | grep -oP 'MinIntervalSec:\s*\K[0-9]+')
def_defint=$(printf '%s' "$def_block" | grep -oP 'DefaultIntervalSec:\s*\K[0-9]+')
def_minto=$(printf '%s' "$def_block" | grep -oP 'MinTimeoutSec:\s*\K[0-9]+')
def_defto=$(printf '%s' "$def_block" | grep -oP 'DefaultTimeoutSec:\s*\K[0-9]+')
def_maxto=$(printf '%s' "$def_block" | grep -oP 'MaxTimeoutSec:\s*\K[0-9]+')
cap_worker=$(grep -oP 'MaxServiceMonitorWorkerLimit\s*=\s*\K[0-9]+' "$LIMITS_GO")
cap_timeout=$(grep -oP 'MaxServiceMonitorTimeoutSec\s*=\s*\K[0-9]+' "$LIMITS_GO")
if [ -z "$limits_line" ] || [ -z "$limits_keys_want" ] \
   || [ -z "$def_scan" ] || [ -z "$def_worker" ] || [ -z "$def_minint" ] || [ -z "$def_defint" ] \
   || [ -z "$def_minto" ] || [ -z "$def_defto" ] || [ -z "$def_maxto" ] \
   || [ -z "$cap_worker" ] || [ -z "$cap_timeout" ]; then
  note_fail "服务监控限值披露门：前提失效（monitoring.md 限值行或 limits.go 缺省/上限提取为空，请人工复核）"
elif ! printf '%s' "$limits_line" | grep -Fq "检查器扫描间隔默认 $def_scan 秒" \
     || ! printf '%s' "$limits_line" | grep -Fq "并发上限默认 $def_worker（保存上限 $cap_worker）" \
     || ! printf '%s' "$limits_line" | grep -Fq "单项间隔最小/默认 $def_minint 秒" \
     || ! printf '%s' "$limits_line" | grep -Fq "单项超时最小 $def_minto 秒 / 默认 $def_defto 秒 / 最大 $def_maxto 秒" \
     || ! printf '%s' "$limits_line" | grep -Fq "三个超时键保存时都不得超过 $cap_timeout"; then
  note_fail "服务监控限值披露门：monitoring.md 限值数字与 limits.go 缺省/上限不一致（代码面：扫描 $def_scan / 并发 $def_worker 上限 $cap_worker / 间隔 $def_minint·$def_defint / 超时 $def_minto·$def_defto·$def_maxto 保存帽 $cap_timeout，请人工复核）"
else
  note_ok "服务监控限值披露门：monitoring.md 对 limits.go $(printf '%s\n' "$limits_keys_want" | wc -l | tr -d ' ') 个 service_monitor_* 键的缺省/上限披露一致"
fi

# ── 22. W7 口径钉子四点（FIX-212 W7 移交、W8 落门）：文档关键口径
#    漂移即红，逐点 fail-closed（节提取为空＝前提失效，按红处理）。
FORWARD_MD="doc/guide/forward.md"
INDEX_MD="doc/index.md"
UPGRADE_MD="doc/guide/upgrade.md"
IMPORT_MD="doc/guide/import.md"

# 22a. forward.md 会话无进展拆除节须含内核探活参数 180 秒 / 60 秒 /
#      6 次与 FLMX_KEEPALIVE（防再漂移回「探活不存在」口径）。
ka_sec=$(awk '/^### 会话无进展拆除/{f=1;next} /^## /{if(f)exit} f' "$FORWARD_MD")
if [ -z "$ka_sec" ]; then
  note_fail "探活参数钉子：forward.md 找不到「会话无进展拆除」节（前提失效，请人工复核）"
elif ! printf '%s' "$ka_sec" | grep -Fq "180 秒" \
     || ! printf '%s' "$ka_sec" | grep -Fq "60 秒" \
     || ! printf '%s' "$ka_sec" | grep -Fq "6 次" \
     || ! printf '%s' "$ka_sec" | grep -Fq "FLMX_KEEPALIVE"; then
  note_fail "探活参数钉子：forward.md 拆除节缺 180 秒 / 60 秒 / 6 次 / FLMX_KEEPALIVE 任一"
else
  note_ok "探活参数钉子：forward.md 拆除节含 180 秒 / 60 秒 / 6 次与 FLMX_KEEPALIVE"
fi
# 双侧（G-R4-04）：文档数字须与 keepalive.go 缺省常量机械对账——只查
# 文档时，实现改值仍全绿。
KA_GO="go-gost/x/internal/keepalive/keepalive.go"
ka_idle=$(grep -oP 'DefaultIdle\s*=\s*\K[0-9]+' "$KA_GO" | head -1)
ka_int=$(grep -oP 'DefaultInterval\s*=\s*\K[0-9]+' "$KA_GO" | head -1)
ka_cnt=$(grep -oP 'DefaultCount\s*=\s*\K[0-9]+' "$KA_GO" | head -1)
if [ -z "$ka_idle" ] || [ -z "$ka_int" ] || [ -z "$ka_cnt" ]; then
  note_fail "探活参数钉子：keepalive.go 缺省常量提取为空（前提失效，请人工复核）"
elif [ "$ka_idle" != "180" ] || [ "$ka_int" != "60" ] || [ "$ka_cnt" != "6" ]; then
  note_fail "探活参数钉子：keepalive.go 缺省值漂移（idle=${ka_idle}s interval=${ka_int}s count=${ka_cnt}，文档登记 180/60/6）"
elif ! grep -q "FLMX_KEEPALIVE" "$KA_GO"; then
  note_fail "探活参数钉子：keepalive.go 未读 FLMX_KEEPALIVE 环境开关（文档登记该开关，请人工复核）"
else
  note_ok "探活参数钉子：keepalive.go 缺省值与文档一致（180/60/6，FLMX_KEEPALIVE 开关在场）"
fi

# 22b. nodes.md「节点展示分组」节在场且含「不参与订阅」限定句
#      （防展示分组成员被后人写成订阅分组；B-03 合并后标题为「节点展示分组」，锚点随之改）。
grp_sec=$(awk '/^## 节点展示分组/{f=1;next} /^## /{if(f)exit} f' "$NODES_MD")
if [ -z "$grp_sec" ]; then
  note_fail "节点分组钉子：nodes.md 找不到「## 节点展示分组」节（前提失效，请人工复核）"
elif ! printf '%s' "$grp_sec" | grep -Fq "不参与订阅"; then
  note_fail "节点分组钉子：nodes.md 分组节缺「不参与订阅」限定句"
else
  note_ok "节点分组钉子：nodes.md「节点展示分组」节在场且含「不参与订阅」"
fi
# 双侧（G-R4-04）：「不参与订阅」须由订阅渲染面背书——DisplayGroup
# 只许出现在节点 CRUD/展示路径，不得进入订阅渲染（share.go）。
SHARE_GO_CHECK="go-backend/internal/kernel/share.go"
if grep -q "DisplayGroup" "$SHARE_GO_CHECK"; then
  note_fail "节点分组钉子：share.go 引用了 DisplayGroup（展示分组进入订阅渲染，与「不参与订阅」口径冲突）"
elif ! grep -q "DisplayGroup" go-backend/internal/store/model/model.go; then
  note_fail "节点分组钉子：model.go 无 DisplayGroup 字段（分组功能缺失时文档限定句无所指，请人工复核）"
else
  note_ok "节点分组钉子：DisplayGroup 未进入订阅渲染（share.go 零引用）"
fi

# 22c. nodes.md 协议开关节含「屏蔽开关」且不含「监听是否开启」
#      （F2-13 防反转复发）。
sw_sec=$(awk '/^## 节点协议开关/{f=1;next} /^## /{if(f)exit} f' "$NODES_MD")
if [ -z "$sw_sec" ]; then
  note_fail "协议开关钉子：nodes.md 找不到「## 节点协议开关」节（前提失效，请人工复核）"
elif ! printf '%s' "$sw_sec" | grep -Fq "屏蔽开关"; then
  note_fail "协议开关钉子：nodes.md 协议开关节缺「屏蔽开关」"
elif printf '%s' "$sw_sec" | grep -Fq "监听是否开启"; then
  note_fail "协议开关钉子：nodes.md 协议开关节出现「监听是否开启」（F2-13 反转复发）"
else
  note_ok "协议开关钉子：nodes.md 协议开关节含「屏蔽开关」且无「监听是否开启」"
fi
# 双侧（G-R4-04）：「屏蔽开关」语义须由下发/应用面背书——面板经
# SetProtocol 下发，agent 侧 handleSetProtocol（「设置屏蔽协议」）
# 落盘 SetProtocolBlock。任一侧改名/改义即红。
if ! grep -q '"SetProtocol"' go-backend/internal/http/handler/control_plane.go; then
  note_fail "协议开关钉子：control_plane.go 未下发 SetProtocol 命令（下发面漂移，请人工复核）"
elif ! grep -q "设置屏蔽协议" go-gost/x/socket/websocket_reporter.go; then
  note_fail "协议开关钉子：websocket_reporter.go handleSetProtocol 缺「设置屏蔽协议」口径（应用面改义，请人工复核）"
elif ! grep -q "func SetProtocolBlock" go-gost/x/service/service.go; then
  note_fail "协议开关钉子：service.go 缺 SetProtocolBlock（屏蔽应用面漂移，请人工复核）"
else
  note_ok "协议开关钉子：SetProtocol 下发→屏蔽应用面一致"
fi

# 22d. 残留词零命中钉子：「远程节点」在 doc/index.md＋doc/guide/
#      全域为 0、index.md「Snell」为 0；Snell 在 guide/ 内只许出现
#      在导入语境（import.md 整篇属导入语境）或带「导入」/「已移除」
#      的行（upgrade.md 移除项节、nodes.md/subscription.md 导入口径行）。
remote_hits=$(grep -rn "远程节点" "$INDEX_MD" doc/guide/ 2>/dev/null || true)
snell_index=$(grep -c "Snell" "$INDEX_MD" 2>/dev/null || true)
snell_bad=$(grep -rn "Snell" doc/guide/ 2>/dev/null | grep -v "^$IMPORT_MD:" | grep -v "导入" | grep -v "已移除" || true)
if [ -n "$remote_hits" ]; then
  note_fail "残留词钉子：仍有「远程节点」：$(printf '%s' "$remote_hits" | head -3 | tr '\n' ';')"
elif [ "${snell_index:-0}" != "0" ]; then
  note_fail "残留词钉子：doc/index.md 仍有 Snell（${snell_index} 处）"
elif [ -n "$snell_bad" ]; then
  note_fail "残留词钉子：guide/ 内 Snell 脱离导入/移除语境：$(printf '%s' "$snell_bad" | head -3 | tr '\n' ';')"
else
  note_ok "残留词钉子：「远程节点」index＋guide 零命中、index.md Snell 零命中、guide/ 内 Snell 全在导入/移除语境"
fi

# ── 23. 本轮文档断言兜底（R2-R4 G-R4-02）：四处新增文档断言须有门禁——
# install.md 子网示例、身份六键、notify.md 机器人菜单、upgrade.md:83
# V1-01 残留段。真值已人工核对通过，本节守「改动漂移即红」，每项双侧
# 对账（文档声称 vs 模板/脚本/代码实值）。

# 23a. install.md「子网冲突的改法」示例子网：须与模板缺省不同（示例
# 即缺省等于没改），且属 RFC1918 私网段 / ULA 段。F1-N1 前例：旧示例
# 172.31.0.0/16 撞在用网络、fd00:dead:beee::/48 与他网同值。
subnet_sec=$(awk '/子网冲突的改法/{f=1} /不要给已部署的实例改实例键/{if(f)exit} f' doc/guide/install.md)
ex_v4=$(printf '%s' "$subnet_sec" | grep -oP '例如 `172\.[0-9.]+/[0-9]+`' | grep -oP '172\.[0-9.]+/[0-9]+' | head -1)
ex_v6=$(printf '%s' "$subnet_sec" | grep -oP '例如 `fd00:[0-9a-f:]+/[0-9]+`' | grep -oP 'fd00:[0-9a-f:]+/[0-9]+' | head -1)
def_v4=$(grep -oP -- '- subnet: \K172\.[0-9.]+/[0-9]+' docker-compose-v4.yml | head -1)
def_v6=$(grep -oP -- '- subnet: \Kfd00:[0-9a-f:]+/[0-9]+' docker-compose-v6.yml | head -1)
if [ -z "$ex_v4" ] || [ -z "$ex_v6" ] || [ -z "$def_v4" ] || [ -z "$def_v6" ]; then
  note_fail "子网示例钉子：install.md 示例子网或模板缺省子网提取为空（示例 v4=${ex_v4:-空} v6=${ex_v6:-空}、缺省 v4=${def_v4:-空} v6=${def_v6:-空}，请人工复核）"
elif [ "$ex_v4" = "$def_v4" ]; then
  note_fail "子网示例钉子：install.md IPv4 示例 $ex_v4 与模板缺省相同（示例即缺省等于没改，F1-N1 复发）"
elif [ "$ex_v6" = "$def_v6" ]; then
  note_fail "子网示例钉子：install.md IPv6 示例 $ex_v6 与模板缺省相同（示例即缺省等于没改，F1-N1 复发）"
elif ! printf '%s' "$ex_v4" | awk -F'[./]' '{ exit !($1==172 && $2>=16 && $2<=31) }'; then
  note_fail "子网示例钉子：install.md IPv4 示例 $ex_v4 不在 RFC1918 172.16.0.0/12 内"
elif ! printf '%s' "$ex_v6" | grep -q '^fd'; then
  note_fail "子网示例钉子：install.md IPv6 示例 $ex_v6 不在 ULA fd00::/8 内"
else
  note_ok "子网示例钉子：install.md 示例子网与模板缺省不同（v4 $ex_v4 ≠ $def_v4、v6 $ex_v6 ≠ $def_v6），且属 RFC1918/ULA"
fi

# 23b. 身份六键：install.md 与 postgresql.md 的六键清单须彼此一致，
# 且与脚本写入 .env 的六键、两模板引用的六键同一集合（F1-N3）。
SIX_KEYS="PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER FLMX_SQLITE_VOLUME FLMX_POSTGRES_VOLUME FLMX_NETWORK"
six_bad=""
for k in $SIX_KEYS; do
  grep -Fq "$k" doc/guide/install.md || six_bad="$six_bad install.md 缺 $k;"
  grep -Fq "$k" doc/guide/postgresql.md || six_bad="$six_bad postgresql.md 缺 $k;"
  grep -Fq "upsert_env_var .env $k " panel_install.sh || six_bad="$six_bad panel_install.sh 未写 $k;"
  grep -Fq "\${$k:-" docker-compose-v4.yml || six_bad="$six_bad docker-compose-v4.yml 未引用 $k;"
  grep -Fq "\${$k:-" docker-compose-v6.yml || six_bad="$six_bad docker-compose-v6.yml 未引用 $k;"
done
if [ -n "$six_bad" ]; then
  note_fail "身份六键钉子：$six_bad"
else
  note_ok "身份六键钉子：两文档六键清单与脚本写入、两模板引用同一集合（6 键）"
fi

# 23c. notify.md「机器人菜单」小节：四个命令与三个按钮须与 notify_bot.go
# 的注册命令、菜单键盘逐字一致（bac16c1a 新增小节，门禁曾无断言）。
bot_sec=$(awk '/^### 机器人菜单/{f=1;next} /^## |^### /{if(f)exit} f' "$NOTIFY_MD")
bot_bad=""
for c in "/start" "/nodes" "/traffic" "/status"; do
  printf '%s' "$bot_sec" | grep -Fq "$c" || bot_bad="$bot_bad 文档缺 $c;"
  grep -Fq "\"command\": \"${c#/}\"" go-backend/internal/http/handler/notify_bot.go || bot_bad="$bot_bad 代码未注册 ${c#/};"
done
for b in "🖥 节点状态" "📊 流量" "ℹ️ 面板状态"; do
  printf '%s' "$bot_sec" | grep -Fq "$b" || bot_bad="$bot_bad 文档缺按钮 $b;"
  grep -Fq "$b" go-backend/internal/http/handler/notify_bot.go || bot_bad="$bot_bad 代码缺按钮 $b;"
done
if [ -z "$bot_sec" ]; then
  note_fail "机器人菜单钉子：notify.md 找不到「### 机器人菜单」小节（前提失效，请人工复核）"
elif [ -n "$bot_bad" ]; then
  note_fail "机器人菜单钉子：$bot_bad"
else
  note_ok "机器人菜单钉子：notify.md 菜单小节与 notify_bot.go 注册命令/按钮一致（4 命令＋3 按钮）"
fi

# 23d. upgrade.md V1-01 残留段「多 agent 同机过渡期注意」：须含版本门
# 阈值，且与 kernel.go 两处版本门常量同值（面板门 2.1.13 起拒旧 agent）。
res_sec=$(awk '/多 agent 同机过渡期注意/{f=1} /^### /{if(f)exit} f' "$UPGRADE_MD")
doc_ver=$(printf '%s' "$res_sec" | grep -oP '2\.[0-9]+\.[0-9]+' | sort -u | head -1)
nft_ver=$(grep -oP 'nftIsolatedAgentMinVersion = "\K[0-9.]+' go-backend/internal/http/handler/kernel.go | head -1)
mark_ver=$(grep -oP 'userMarkBaseMinVersion = "\K[0-9.]+' go-backend/internal/http/handler/kernel.go | head -1)
if [ -z "$res_sec" ]; then
  note_fail "V1-01 残留段钉子：upgrade.md 找不到「多 agent 同机过渡期注意」段（前提失效，请人工复核）"
elif [ -z "$doc_ver" ] || [ -z "$nft_ver" ] || [ -z "$mark_ver" ]; then
  note_fail "V1-01 残留段钉子：版本阈值提取为空（文档 ${doc_ver:-空}、nft 门 ${nft_ver:-空}、mark 门 ${mark_ver:-空}，请人工复核）"
elif [ "$doc_ver" != "$nft_ver" ] || [ "$doc_ver" != "$mark_ver" ]; then
  note_fail "V1-01 残留段钉子：文档版本 $doc_ver 与 kernel.go 版本门不一致（nft 门 $nft_ver、mark 门 $mark_ver）"
else
  note_ok "V1-01 残留段钉子：upgrade.md 残留段版本与 kernel.go 两处版本门一致（$doc_ver）"
fi

# ── 24. F1-N2 回归测试在场门（R2-R4 G-R4-03）：test-install-scripts-
# proxy.sh 须含 report_compose_up_failure 双分支回归测试且已在文件
# 末尾的执行清单里注册——测试被删或漏注册即红（CI install-scripts
# job 跑本文件，测试本身即执行面）。
R4_TEST="test_report_compose_up_failure_branches"
r4_calls=$(grep -c "report_compose_up_failure" test-install-scripts-proxy.sh || true)
if [ "$r4_calls" -lt 2 ]; then
  note_fail "F1-N2 回归测试钉子：test-install-scripts-proxy.sh 内 report_compose_up_failure 调用少于 2 处（实数 $r4_calls，双分支回归测试缺失）"
elif ! grep -q "Pool overlaps" test-install-scripts-proxy.sh; then
  note_fail "F1-N2 回归测试钉子：测试内无 Pool overlaps 夹具（overlap 分支无输入）"
elif ! grep -q "镜像拉取失败" test-install-scripts-proxy.sh; then
  note_fail "F1-N2 回归测试钉子：测试内无「镜像拉取失败」断言（拉取失败分支无断言）"
elif ! grep -q "^$R4_TEST$" test-install-scripts-proxy.sh; then
  note_fail "F1-N2 回归测试钉子：$R4_TEST 未在文件末尾执行清单里注册（测试写了也不跑）"
else
  note_ok "F1-N2 回归测试钉子：report_compose_up_failure 双分支回归测试在场且已注册"
fi

# ── 25. S05 前端计量默认开门（D-R7-01 常设门）：转跑 check-hist-metering.sh
# AUDIT-HIST S05：新建入站表单 emptyForm 须默认 meteringEnabled=true
# （旧 false 默认致默认路径 SS2022 入站静默不计量、订阅流量 0/0，
# Issue B 根因面），且 toPayload 须按 SS2022 适格性设防后才发送该
# 开关。前端无测试基建，本门以 grep 断言钉死 presets.ts；后端一半
# 由 Go 测试 TestS05CreateDefaultsMeteringOnForEligibleSS2022 兜底。
# 本门此前存在但全仓无任何接线（bc70a117 未接线：未入 ci-build、
# 未被本门转跑、run-checks 只枚举 .mjs），回归时 CI 无信号——故在
# 此转跑（CI 前端 job 跑本门即跑到），变异自检见
# scripts/check-hist-metering-selftest.sh（CI gate-selftests job）。
# exit 码口径与 §8 同法：exit 2 为前提失效报 PENDING 不计 FAIL，
# 仅 exit 1（漂移）报 FAIL。
bash scripts/check-hist-metering.sh > /tmp/.hist-metering.out 2>&1
hm_rc=$?
if [ "$hm_rc" -eq 0 ]; then
  note_ok "S05 前端计量默认开门（hist-metering）：emptyForm 计量默认开且 toPayload 设防"
elif [ "$hm_rc" -eq 2 ]; then
  echo "PENDING: S05 前端计量默认开门前提失效（exit 2），计量默认开未核：$(grep -m1 '^FAIL: 前提失效' /tmp/.hist-metering.out | sed 's/^FAIL: 前提失效，//')；请按明细处理前提后复跑，不计 FAIL"
  sed 's/^/    /' /tmp/.hist-metering.out
else
  note_fail "S05 前端计量默认开门漂移（明细见下）"
  sed 's/^/    /' /tmp/.hist-metering.out
fi
rm -f /tmp/.hist-metering.out

# ── 26. 技能包例文形态门（E-05-NEW-1/2 常设门）
# E-05-NEW-1：tunnels.md tunnel/create 例文的 inNodeId/outNodeId 项须
# 为含 nodeId 的对象——后端 asMapSlice 静默丢弃非对象项，照抄裸数字
# 例文必得“入口不能为空”（面板 B live 复现）。双侧：文档侧查无裸
# 数字项且例文含 nodeId 对象；代码侧查 asMapSlice 的对象守卫仍在
# （机制锚点，防实现改直通后文档反成错口径）。
TUN_MD="skills/flmx-api/references/tunnels.md"
if grep -Eq '"(in|out|chain)NodeId": \[[0-9]' "$TUN_MD"; then
  note_fail "E-05-NEW-1：tunnels.md 仍有裸数字节点 id 例文（in/out/chainNodeId 项须为含 nodeId 的对象）"
elif ! grep -Fq '"inNodeId": [{"nodeId": 3}]' "$TUN_MD" \
     || ! grep -Fq '"outNodeId": [{"nodeId": 4}]' "$TUN_MD" \
     || ! grep -Fq '"inNodeId":[{"nodeId":1}]' "$TUN_MD" \
     || ! grep -Fq '"outNodeId":[{"nodeId":2}]' "$TUN_MD"; then
  note_fail "E-05-NEW-1：tunnels.md tunnel/create 例文缺 nodeId 对象项（create JSON 与 assign curl 各一处）"
elif ! grep -Fq 'func asMapSlice' go-backend/internal/http/handler/mutations.go \
     || ! grep -Fq 'if m, ok := it.(map[string]interface{}); ok {' go-backend/internal/http/handler/mutations.go; then
  note_fail "E-05-NEW-1：mutations.go 的 asMapSlice 对象守卫不在场（前提失效，请人工复核）"
else
  note_ok "E-05-NEW-1：tunnels.md 例文节点项均为 nodeId 对象，asMapSlice 对象守卫在场"
fi
# E-05-NEW-2：forwards.md forward/create 例文不得含 speedId——普通
# 用户只要键在场（任意值，含 0）即被拒“普通用户无法设置限速规则”
# （面板 B live 复现）；字段表须标 admin-only。双侧：文档侧查例文
# 无 speedId 且字段表标 admin-only；代码侧查拒绝锚点仍在。
FWD_MD="skills/flmx-api/references/forwards.md"
if grep -Fq '"speedId"' "$FWD_MD" && ! grep -Fq 'Admin-only' "$FWD_MD"; then
  note_fail "E-05-NEW-2：forwards.md 含 speedId 却未标 admin-only（前提失效，请人工复核）"
elif grep -Eq '"speedId": [0-9]' "$FWD_MD"; then
  note_fail "E-05-NEW-2：forwards.md 例文仍含 speedId（普通用户照抄即拒）"
elif ! grep -Fq 'if speedIDVal, ok := req["speedId"]; ok && speedIDVal != nil {' go-backend/internal/http/handler/mutations.go \
     || ! grep -Fq '普通用户无法设置限速规则' go-backend/internal/http/handler/mutations.go; then
  note_fail "E-05-NEW-2：mutations.go 的 speedId 普通用户拒绝锚点不在场（前提失效，请人工复核）"
else
  note_ok "E-05-NEW-2：forwards.md 例文无 speedId 且标 admin-only，普通用户拒绝锚点在场"
fi

# ── 27. 内核视图字段对齐门（E-02 常设门）
# 后端 kernelView 新增字段须同步前端 KernelApiItem 类型与一处渲染点：
# companionSplitNote 三侧同在——后端只发不显（节点页永不显示）即红。
if ! grep -Fq 'CompanionSplitNote string' go-backend/internal/http/handler/kernel.go; then
  note_fail "E-02：kernel.go 的 kernelView 缺 companionSplitNote 字段（前提失效，请人工复核）"
elif ! grep -Fq 'companionSplitNote?: string' vite-frontend/src/api/types.ts; then
  note_fail "E-02：KernelApiItem 缺 companionSplitNote 字段（后端已发、前端类型未同步）"
elif ! grep -Fq 'k.companionSplitNote' vite-frontend/src/pages/inbound/kernel-panel.tsx; then
  note_fail "E-02：节点页内核行无 companionSplitNote 渲染点（类型已同步、页面仍不显示）"
else
  note_ok "E-02：companionSplitNote 后端字段／前端类型／节点页渲染三侧同在"
fi

# ── 27b. repush 回执 note 对齐门（R3-E-LOW-02 常设门）
# 与 27 同一字段的另一条面：connlog-repush 回执 data.companionSplitNote
# 同样须三侧同在——后端只发、前端类型无字段或连接日志弹窗不读该字段
# 即红（此前只有后端单测断言，前端可静默丢 note）。
if ! grep -Fq 'data["companionSplitNote"]' go-backend/internal/http/handler/connlog_readiness.go; then
  note_fail "R3-E-LOW-02：connlog-repush 回执未带 companionSplitNote 字段（前提失效，请人工复核）"
elif ! grep -A20 'interface NodeConnLogRepushResult' vite-frontend/src/api/types.ts | grep -Fq 'companionSplitNote?: string'; then
  note_fail "R3-E-LOW-02：NodeConnLogRepushResult 缺 companionSplitNote 字段（后端已发、前端类型未同步）"
elif ! grep -Fq 'data?.companionSplitNote' vite-frontend/src/pages/node/connlog-modal.tsx; then
  note_fail "R3-E-LOW-02：连接日志弹窗未读回执 companionSplitNote（类型已同步、页面仍不显示）"
else
  note_ok "R3-E-LOW-02：repush 回执 companionSplitNote 后端字段／前端类型／弹窗渲染三侧同在"
fi

# ── 28. 内核安装原子交换＋离线缓存门（C NEW-2 常设门）
# nodes.md 称重启/重部署/升级只在替换件落盘验证后才换旧二进制、
# 安装失败旧内核在位、离线重装用本地缓存；代码侧三锚点同在——
# 原子交换 swapBinDir、中断恢复 restoreInterruptedSwap、缓存
# lookup/storeArchiveCache——任一不在场即红（文档反成错口径）。
if ! grep -Fq '原子交换' doc/guide/nodes.md; then
  note_fail "C NEW-2：nodes.md 缺内核安装原子交换口径（重启/重部署只在替换件落盘验证后才换旧二进制）"
elif ! grep -Fq '本地已验安装包缓存' doc/guide/nodes.md; then
  note_fail "C NEW-2：nodes.md 缺离线重装本地缓存安装包口径"
elif ! grep -Fq 'func swapBinDir' go-gost/x/kernel/install.go \
     || ! grep -Fq 'func restoreInterruptedSwap' go-gost/x/kernel/install.go \
     || ! grep -Fq 'func lookupArchiveCache' go-gost/x/kernel/install.go \
     || ! grep -Fq 'func storeArchiveCache' go-gost/x/kernel/install.go; then
  note_fail "C NEW-2：install.go 缺原子交换/中断恢复/安装包缓存锚点（前提失效，请人工复核）"
else
  note_ok "C NEW-2：nodes.md 原子交换＋离线缓存安装包口径与 install.go 三锚点同在"
fi

# ── 29. 请求体例文地址门（R4D-01 常设门）
# forward/create 类请求体例文与 ai-skill.md 对话例的目标地址不得为
# 私网地址——普通用户照抄即 403 "resolves to internal IP"（R3-E-LOW-03
# 只改了 192.168.1.100 三处，漏了 10.0.0.1 三处＋对话例两处）。
# auth.md:196 的 10.0.0.1 是 list 响应回显（R4D-02 已裁定非问题），
# 本门排除该行；docs/superpowers/plans 下历史方案的测试夹具不在本门
# 范围内（非用户照抄面）。双侧：文档侧查例文无私网地址；代码侧查
# IsSafeRemoteAddr 守卫仍在（mutations.go create/update 两处调用）。
PRIV_IP_RE='(10\.[0-9]+\.[0-9]+\.[0-9]+|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]+\.[0-9]+|192\.168\.[0-9]+\.[0-9]+)'
BAD_REMOTE=$(grep -rn -E "\"remoteAddr\": \"[^\"]*${PRIV_IP_RE}" skills/flmx-api/references/ doc/ai-skill.md 2>/dev/null | grep -v 'skills/flmx-api/references/auth.md' || true)
BAD_DIALOG=$(grep -n -E "(转发到|目标地址) ?${PRIV_IP_RE}" doc/ai-skill.md || true)
if [ -n "$BAD_REMOTE" ]; then
  note_fail "R4D-01：请求体例文 remoteAddr 仍含私网地址（照抄即 403）：$(echo "$BAD_REMOTE" | head -3 | tr '\n' ';')"
elif [ -n "$BAD_DIALOG" ]; then
  note_fail "R4D-01：ai-skill.md 对话例目标地址仍为私网地址（经 skill 调用同机制 403）：$(echo "$BAD_DIALOG" | head -3 | tr '\n' ';')"
elif [ "$(grep -c 'IsSafeRemoteAddr(remoteAddr)' go-backend/internal/http/handler/mutations.go)" -lt 2 ]; then
  note_fail "R4D-01：mutations.go 的 IsSafeRemoteAddr 守卫不足两处（create/update 各一处；前提失效，请人工复核）"
else
  note_ok "R4D-01：请求体例文 remoteAddr 与 ai-skill 对话例目标地址均无私网地址，IsSafeRemoteAddr 守卫两处在场"
fi

# ── 30. 改密请求体对齐门（R18-A5-01 常设门）
# auth.md 的 updatePassword 请求体例文键集合须与后端
# changePasswordRequest 的 json tag 集合一致。旧例文只写
# oldPassword/newPassword：oldPassword 键后端根本不读（全仓无读取
# 点），缺 newUsername/currentPassword/confirmPassword 三个必填字
# 段，照抄即在首检报“新用户名不能为空”，且受限 token 的唯一逃生
# 口正是该端点。双侧机械对账，不许手抄清单。
A5_AUTH="skills/flmx-api/references/auth.md"
A5_HANDLER="go-backend/internal/http/handler/handler.go"
a5_doc_keys=$(awk '/^## POST \/api\/v1\/user\/updatePassword/{f=1; next} f && /^## /{exit} f' "$A5_AUTH" \
  | awk '/^```json/{f=1; next} f && /^```/{exit} f' \
  | grep -o '"[A-Za-z][A-Za-z0-9]*":' | tr -d '":' | sort -u | tr '\n' ' ')
a5_code_keys=$(awk '/^type changePasswordRequest struct \{/{f=1; next} f && /^\}/{exit} f' "$A5_HANDLER" \
  | grep -o 'json:"[A-Za-z][A-Za-z0-9]*"' | sed 's/json:"//; s/"//' | sort -u | tr '\n' ' ')
a5_old=$(grep -rn 'oldPassword' skills/ || true)
if [ -z "$a5_doc_keys" ] || [ -z "$a5_code_keys" ]; then
  note_fail "R18-A5-01：改密请求体对齐前提失效（文档键为空或后端 tag 为空，请人工复核）"
elif [ "$a5_doc_keys" != "$a5_code_keys" ]; then
  note_fail "R18-A5-01：auth.md updatePassword 例文键（${a5_doc_keys}) 与后端 changePasswordRequest（${a5_code_keys}) 不一致"
elif [ -n "$a5_old" ]; then
  note_fail "R18-A5-01：skills 内仍有 oldPassword（后端不读该键）：$(echo "$a5_old" | head -3 | tr '\n' ';')"
else
  note_ok "R18-A5-01：auth.md updatePassword 例文键与后端 changePasswordRequest 一致（${a5_code_keys})，skills 内无 oldPassword"
fi

# ── 31. 迁移镜像 digest 钉选门（R18-A2-02 常设门）
# panel_install.sh 内全部 docker run 镜像引用必须带 digest：字面
# 形态须含 @sha256:，或引用 FLMX_MIGRATE_*_IMAGE 常量（常量定义
# 本身须含 @sha256: 的 64 位十六进制 digest）。任一 docker run 行
# 不满足即红；常量定义缺失/未钉 digest 同样红（前提失效）。
MIG_IMG_BAD=$(grep -n 'docker run -' panel_install.sh | grep -v 'FLMX_MIGRATE_\(ALPINE\|PGLOADER\)_IMAGE' | grep -v '@sha256:' || true)
if [ -n "$MIG_IMG_BAD" ]; then
  note_fail "R18-A2-02：panel_install.sh 存在未钉 digest 的 docker run 镜像引用：$(echo "$MIG_IMG_BAD" | head -3 | tr '\n' ';')"
elif ! grep -q '^FLMX_MIGRATE_ALPINE_IMAGE="[^"]*@sha256:[0-9a-f]\{64\}"' panel_install.sh \
  || ! grep -q '^FLMX_MIGRATE_PGLOADER_IMAGE="[^"]*@sha256:[0-9a-f]\{64\}"' panel_install.sh; then
  note_fail "R18-A2-02：迁移镜像常量 FLMX_MIGRATE_ALPINE_IMAGE / FLMX_MIGRATE_PGLOADER_IMAGE 必须定义为含 @sha256: 的钉选值（前提失效，请人工复核）"
else
  note_ok "R18-A2-02：panel_install.sh 全部 docker run 镜像引用均已钉 digest（字面或迁移镜像常量）"
fi

# ── 32. 安装预检回归测试在场门（R18-A2-06 常设门）
# 预检函数组 6 个须在 test-install-scripts-proxy.sh 中有非注释
# 行按词引用（grep -Fw：test_panel_find_free_port_* 这类测试函数
# 名本身不算引用，须是真正的调用/断言行；纯注释行不算）。只守
# 本函数组，不扩到全仓 panel_* 函数。
PREFLIGHT_FUNCS="panel_preflight_install_conflicts panel_find_free_port panel_port_is_listening panel_port_container_owners panel_port_listener_owner panel_port_listener_text"
PREFLIGHT_UNTESTED=""
for _pf in $PREFLIGHT_FUNCS; do
  if ! grep -Fw "$_pf" test-install-scripts-proxy.sh | grep -v '^[[:space:]]*#' | grep -q .; then
    PREFLIGHT_UNTESTED="$PREFLIGHT_UNTESTED $_pf"
  fi
done
if [ -n "$PREFLIGHT_UNTESTED" ]; then
  note_fail "R18-A2-06：安装预检函数在测试套件中无引用（须补回归测试）：$PREFLIGHT_UNTESTED"
else
  note_ok "R18-A2-06：安装预检函数组 6 个均在 test-install-scripts-proxy.sh 中有测试引用"
fi

# ── 33. v6 判别锚对账门（R18-A2-11 常设门）
# panel_install.sh 以 fd00:dead:beef 字面量判别部署是否 v6 世代
# （update_panel 内 grep docker-compose.yml 的判别行）；该锚须
# 与 docker-compose-v6.yml 的 IPv6 子网前缀逐字一致。锚提取限
# 定到含 docker-compose.yml 的判别行——脚本另有 fd00::/80
#（docker daemon fixed-cidr-v6），裸 grep 会先命中它。
V6_ANCHOR=$(grep 'fd00:' panel_install.sh | grep 'docker-compose.yml' | grep -o 'fd00:[0-9a-f:]*' | head -n 1 || true)
V6_SUBNET=$(grep -o 'fd00:[0-9a-f:]*::/[0-9]*' docker-compose-v6.yml | head -n 1 || true)
V6_PREFIX=${V6_SUBNET%%::/*}
if [[ -z "$V6_ANCHOR" || -z "$V6_PREFIX" ]]; then
  note_fail "R18-A2-11：v6 判别锚对账前提失效（脚本锚：${V6_ANCHOR:-缺失}，模板子网：${V6_SUBNET:-缺失}），请人工复核"
elif [[ "$V6_ANCHOR" != "$V6_PREFIX" ]]; then
  note_fail "R18-A2-11：panel_install.sh 的 v6 判别锚 $V6_ANCHOR 与 docker-compose-v6.yml 的 IPv6 子网前缀 $V6_PREFIX 不一致"
else
  note_ok "R18-A2-11：v6 判别锚与 docker-compose-v6.yml 的 IPv6 子网前缀一致（$V6_ANCHOR）"
# ── 34. 发版 Release notes Quick Install 校验门（R18-A4-02 常设门）：
# docker-build.yml 的 create-release 作业在 --notes 里印 4 条 Quick
# Install 命令（定版 ×2、latest ×2）；每条须先下同名 .sha256 再
# sha256sum -c、失败即中止，与 README 的校验纪律同形。只下载即执
# 行是未校验路径（Release 资产被换即投毒），故逐条计数点名。
A4_WF=".github/workflows/docker-build.yml"
a4_p_cmds=$(grep -cF 'panel_install.sh -o panel_install.sh' "$A4_WF" || true)
a4_p_ok=$(grep -cF 'sha256sum -c panel_install.sh.sha256' "$A4_WF" || true)
a4_n_cmds=$(grep -cF 'install.sh -o install.sh' "$A4_WF" || true)
a4_n_ok=$(grep -cF 'sha256sum -c install.sh.sha256' "$A4_WF" || true)
if [ "$a4_p_cmds" -ne 2 ] || [ "$a4_n_cmds" -ne 2 ]; then
  note_fail "R18-A4-02：发版 Release notes Quick Install 命令前提失效（面板 $a4_p_cmds 条、节点 $a4_n_cmds 条，期望各 2 条，请人工复核）"
elif [ "$a4_p_ok" -ne 2 ] || [ "$a4_n_ok" -ne 2 ]; then
  note_fail "R18-A4-02：发版 Release notes Quick Install 有未校验 sha256 的安装命令（面板校验 $a4_p_ok/2 条、节点校验 $a4_n_ok/2 条），与 README 校验纪律打架"
else
  note_ok "R18-A4-02：发版 Release notes Quick Install 4 条命令均先下 .sha256 再 sha256sum -c"
fi

# ── 35. proxy 套件测试注册普查门（R18-A4-04 常设门）：机械提取
# test-install-scripts-proxy.sh 的测试函数定义集（行首 test_…()，
# 含 () ( 与 () { 两种形态）与裸调用集（行首 test_…、文件内各段
# 执行清单），定义未调用 / 调用未定义 / 重复定义任一差集非空即
# FAIL 并点名。手写清单漏注册即静默不跑，本门是唯一机械防线
# （§24 的 F1-N2 钉子只守单个测试）。
A4_SUITE="test-install-scripts-proxy.sh"
if [ ! -f "$A4_SUITE" ]; then
  note_fail "R18-A4-04：proxy 套件测试注册普查前提失效（$A4_SUITE 缺失，请人工复核）"
else
  a4_census_tmp=$(mktemp -d)
  grep -oE '^test_[a-z_0-9]+ *\(\)' "$A4_SUITE" | sed 's/ *()$//' | sort > "$a4_census_tmp/defs" || true
  grep -oE '^test_[a-z_0-9]+$' "$A4_SUITE" | sort > "$a4_census_tmp/calls" || true
  a4_n_defs=$(wc -l < "$a4_census_tmp/defs" | tr -d ' ')
  a4_n_calls=$(wc -l < "$a4_census_tmp/calls" | tr -d ' ')
  a4_def_dup=$(uniq -d "$a4_census_tmp/defs" | tr '\n' ' ')
  a4_call_dup=$(uniq -d "$a4_census_tmp/calls" | tr '\n' ' ')
  a4_unrun=$(comm -23 "$a4_census_tmp/defs" "$a4_census_tmp/calls" | tr '\n' ' ')
  a4_undef=$(comm -13 "$a4_census_tmp/defs" "$a4_census_tmp/calls" | tr '\n' ' ')
  if [ "$a4_n_defs" -eq 0 ]; then
    note_fail "R18-A4-04：proxy 套件测试注册普查前提失效（定义提取为空，提取式漂移，请人工复核）"
  elif [ -n "$a4_def_dup" ]; then
    note_fail "R18-A4-04：proxy 套件有重复定义的测试函数：$a4_def_dup"
  elif [ -n "$a4_call_dup" ]; then
    note_fail "R18-A4-04：proxy 套件有重复调用的测试函数：$a4_call_dup"
  elif [ -n "$a4_unrun" ]; then
    note_fail "R18-A4-04：proxy 套件有定义未调用的测试（写了也不跑）：$a4_unrun"
  elif [ -n "$a4_undef" ]; then
    note_fail "R18-A4-04：proxy 套件有调用未定义的测试：$a4_undef"
  else
    note_ok "R18-A4-04：proxy 套件测试定义与调用普查一致（定义 $a4_n_defs 个＝调用 $a4_n_calls 个，无重复/漏注册）"
  fi
  rm -rf "$a4_census_tmp"
fi
fi

echo "----"
if [ "$FAIL" -eq 0 ]; then
  echo "RESULT: PASS（known-pending 项不计失败）"
else
  echo "RESULT: FAIL"
fi
exit "$FAIL"
