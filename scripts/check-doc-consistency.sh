#!/usr/bin/env bash
# check-doc-consistency.sh — 文档与代码一致性只读检查（审计 B1 批引入）。
#
# 覆盖：
#   1. nodes.md 订阅格式表 XHTTP 行 vs kernel/share.go 实际渲染（xhttp-opts）
#   2. skills/flmx-api 接口清单 vs handler.go＋router.go 路由集 diff（整域缺失即失败）
#   3. 技能包版本字面量三处一致（SKILL.md / package.json / doc/ai-skill.md 发布示例）
#   4. install.md 维护菜单 vs panel_install.sh show_menu 实际菜单
#   5. install.md Caddy 示例反代目标 vs 脚本刻意使用的 127.0.0.1
#   6. [known-pending] 默认账号字面量：guide vs seed/脚本——F-064 属 §十一.4
#      待产品决策，本项只报告、不计入失败（决策落地后应改为硬检查）。
#   7. notify.md Webhook `event` 取值枚举 vs handler notifyEvent* 常量全集
#      （R7-M-01 常设门：文档自称列全的枚举与代码常量集 diff 必须为 ∅）。
#   8. 超时预算登记表 vs 产品代码（R8 T-GATE 常设门：转跑
#      scripts/check-timeout-budgets.sh，前端超时/服务端放宽/后端界三层
#      与 doc/audit/timeout-budgets.tsv 逐格对账，漂移点名失败）。
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
#      未核（不计失败、不得静默跳过），TestPostgres 子项纯 grep、恒硬核。
#   11. panel_install.sh SCRIPT_VERSION 字面量门（L2-30 常设门）：
#      self_update_check 以 SCRIPT_VERSION 相等为「无更新」短路，字
#      面量缺失/重复/格式漂移会让脚本自更新静默失效。硬核：定义恰
#      一处、格式 YYYYMMDD.NN 定宽、日期段为合法年月日。install.sh
#      无此概念（版本跟随 PINNED_VERSION），不在本门。
#   12. 站内锚点对账门（R8-L7-02 常设门）：VitePress 构建不校验锚点，
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
#
# 退出码：0 = 全部硬检查通过；1 = 存在不一致。只读，不修改任何文件。
set -u
cd "$(dirname "$0")/.." || exit 2

FAIL=0
note_fail() { echo "FAIL: $*"; FAIL=1; }
note_ok()   { echo "OK:   $*"; }
note_pend() { echo "PENDING(known, F-064): $*"; }

# go_strip：Go 词法剥注释（awk 状态机，跨行携带块注释/原始字符串
# 状态；字符串、原始字符串、符文字面量内容原样保留，注释整体丢
# 弃）。本脚本凡以 grep 对 Go 源码计数/取集/验前提者（§1 前提、
# §7 事件集、§10(a) 计数）一律只对剥离后的代码面判定——注释掉
# 的代码行与删除同效、注释里的字面量拷贝不构成满足点（修复轮 6
# 机制处置，R6-C4-12/13/14）。
go_strip() {
  awk '
    BEGIN { st = 0; q = sprintf("%c", 39) }
    {
      line = $0; out = ""; n = length(line)
      for (i = 1; i <= n; i++) {
        c = substr(line, i, 1); nx = substr(line, i + 1, 1)
        if (st == 0) {
          if (c == "/" && nx == "/") { st = 1; i++; continue }
          if (c == "/" && nx == "*") { st = 2; i++; continue }
          if (c == "\"") st = 3
          else if (c == "`") st = 4
          else if (c == q) st = 5
          out = out c
        } else if (st == 1) {
          break
        } else if (st == 2) {
          if (c == "*" && nx == "/") { st = 0; i++ }
        } else if (st == 3) {
          out = out c
          if (c == "\\") { out = out nx; i++ }
          else if (c == "\"") st = 0
        } else if (st == 4) {
          out = out c
          if (c == "`") st = 0
        } else if (st == 5) {
          out = out c
          if (c == "\\") { out = out nx; i++ }
          else if (c == q) st = 0
        }
      }
      if (st == 1) st = 0
      print out
    }'
}

# ── 1. XHTTP：share.go 输出 xhttp-opts 时，nodes.md 的 Clash/Shadowrocket 行不得再列 XHTTP 为「不包含」
#    前提沿革：初版无引号 grep 被注释散文提及自满足（R5-03 同族）；
#    第五轮改引号化 grep 后，第六轮复证把注释里的键名加上引号即可
#    再满足（R6-C4-12）——逐形态收紧关不死。本轮按机制处置：前提
#    只对 go_strip 剥注释后的代码面判定 '"xhttp-opts"' 字面量在位，
#    注释提及（无论带不带引号）整体不在证据面内；发射行删除时前
#    提失效、整节按 fail-closed 报红。
SHARE_GO="go-backend/internal/kernel/share.go"
NODES_MD="doc/guide/nodes.md"
if go_strip < "$SHARE_GO" | grep -q '"xhttp-opts"'; then
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
else
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
menu_script=$(sed -n '/^show_menu()/,/^}/p' panel_install.sh | grep -oP 'echo "\K[0-9]+\. [^"]+' | sort)
menu_doc=$(sed -n '/### 维护菜单/,/^## /p' doc/guide/install.md | grep -oP '^[0-9]+\. .*' | sed 's/[[:space:]]*$//' | sort)
if [ "$menu_script" = "$menu_doc" ]; then
  note_ok "install.md 维护菜单与 panel_install.sh 一致（$(echo "$menu_script" | wc -l) 项）"
else
  note_fail "install.md 维护菜单与 panel_install.sh 不一致："
  diff <(echo "$menu_script") <(echo "$menu_doc") | sed 's/^/    /'
fi

# ── 5. Caddy 示例：install.md 不得再写 reverse_proxy localhost（脚本刻意用 127.0.0.1 避免 IPv6 解析 502）
if grep -q "reverse_proxy localhost" doc/guide/install.md; then
  note_fail "install.md Caddy 示例仍写 reverse_proxy localhost，脚本口径为 127.0.0.1"
else
  note_ok "install.md Caddy 示例反代目标与脚本一致（127.0.0.1）"
fi

# ── 6. [known-pending] 默认账号字面量（F-064，§十一.4 待决策，只报告）
seed_user=$(grep -oP '"\Kadmin@[^"]+' go-backend/internal/store/repo/repository.go | head -1)
guide_user=$(grep -oP '默认账号 `\K[^`]+' doc/guide/getting-started.md | head -1 | awk '{print $1}')
if [ -n "$seed_user" ] && [ -n "$guide_user" ] && [ "$seed_user" != "$guide_user" ]; then
  note_pend "guide 默认账号为 $guide_user，seed 为 $seed_user（改文档还是改 seed 待用户决策）"
else
  echo "OK:   默认账号字面量一致：$guide_user"
fi

# ── 7. Webhook event 枚举：notify.md `event` 取值 vs 代码 notifyEvent* 常量全集（R7-M-01）
# 代码侧机械枚举全部 notifyEvent* 常量定义（排除测试文件）；文档侧取 notify.md
# 中以 "- `event`" 开头的那一行的全部反引号取值（首个 `event` 本身除外，
# 含行尾以「测试消息是 `test`」形式列出的 test）。
NOTIFY_MD="doc/guide/notify.md"
# 代码侧事件集只对剥注释后的代码面枚举（R6-C4-14：注释掉的常量
# 行不再计入事件集）。
code_events=$(for f in $(find go-backend/internal/http/handler -name '*.go' ! -name '*_test.go' | sort); do go_strip < "$f"; done | grep -oP 'notifyEvent[A-Za-z]+\s*=\s*"\K[a-z_]+' | sort -u)
doc_events=$(grep -P '^- `event`' "$NOTIFY_MD" | grep -oP '`[a-z_]+`' | tr -d '`' | grep -vx event | sort -u)
if [ -z "$code_events" ] || [ -z "$doc_events" ]; then
  note_fail "notify.md event 枚举检查前提失效（代码常量集或文档枚举为空，请人工复核）"
elif [ "$code_events" = "$doc_events" ]; then
  note_ok "notify.md event 枚举与代码 notifyEvent* 常量全集一致（$(echo "$code_events" | wc -l) 项）"
else
  note_fail "notify.md event 枚举与代码 notifyEvent* 常量全集不一致（< 代码有文档无 / > 文档有代码无）："
  diff <(echo "$code_events") <(echo "$doc_events") | sed 's/^/    /'
fi

# ── 8. 超时预算常设门（R8 T-GATE）：转跑 check-timeout-budgets.sh
if bash scripts/check-timeout-budgets.sh > /tmp/.timeout-budgets.out 2>&1; then
  note_ok "超时预算登记表与产品代码逐格一致（$(grep -oP 'timeout-budgets: \K[0-9]+' /tmp/.timeout-budgets.out) 格）"
else
  note_fail "超时预算登记表与产品代码不一致（明细见下）"
  sed 's/^/    /' /tmp/.timeout-budgets.out
fi
rm -f /tmp/.timeout-budgets.out

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
# 计数只对剥注释后的代码面、按顶层函数行首锚定（R6-C4-13：注释掉
# 的测试函数不参与编译运行，不再计入实数）。
tp_actual=$(for f in go-backend/tests/contract/*_test.go; do go_strip < "$f"; done | grep -c '^func TestPostgres' | tr -d ' ')
tp_claims=$(grep -rhoP '\[K11-COUNT:testpostgres=\K[0-9]+' doc/guide/postgresql.md AGENTS.md 2>/dev/null | sort -u)
if [ -z "$tp_actual" ] || [ "$tp_actual" = "0" ]; then
  note_fail "计数对账前提失效：go-backend/tests/contract 未数到 TestPostgres* 用例"
elif [ -z "$tp_claims" ]; then
  note_fail "计数对账：doc/guide/postgresql.md 与 AGENTS.md 未声明 [K11-COUNT:testpostgres=N] 标记（实数 $tp_actual，裸数字计数不受门守）"
elif [ "$tp_claims" != "$tp_actual" ]; then
  note_fail "计数对账：TestPostgres* 文档声称「$(echo "$tp_claims" | tr '\n' ' ')」≠ 实数 $tp_actual（grep 'func TestPostgres' go-backend/tests/contract）"
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
  note_pend "计数对账：当前环境无 go 工具链，x 模块包计数未核（TestPostgres 子项已硬核）；请在装有 Go 的环境复跑本门"
fi

# ── 11. panel_install.sh SCRIPT_VERSION 字面量门（L2-30）
sv_count=$(grep -c '^SCRIPT_VERSION="' panel_install.sh)
sv_value=$(grep -o '^SCRIPT_VERSION="[^"]*"' panel_install.sh | head -n1 | sed 's/^SCRIPT_VERSION="//; s/"$//')
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

echo "----"
if [ "$FAIL" -eq 0 ]; then
  echo "RESULT: PASS（known-pending 项不计失败）"
else
  echo "RESULT: FAIL"
fi
exit "$FAIL"
