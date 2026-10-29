#!/usr/bin/env bash
# check-doc-consistency.sh — 文档与代码一致性只读检查（审计 B1 批引入）。
#
# 覆盖：
#   1. nodes.md 订阅格式表 XHTTP 行 vs kernel/share.go 实际渲染（xhttp-opts）
#   2. skills/flmx-api 接口清单 vs handler.go 路由表 diff（整域缺失即失败）
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
#
# 退出码：0 = 全部硬检查通过；1 = 存在不一致。只读，不修改任何文件。
set -u
cd "$(dirname "$0")/.." || exit 2

FAIL=0
note_fail() { echo "FAIL: $*"; FAIL=1; }
note_ok()   { echo "OK:   $*"; }
note_pend() { echo "PENDING(known, F-064): $*"; }

# ── 1. XHTTP：share.go 输出 xhttp-opts 时，nodes.md 的 Clash/Shadowrocket 行不得再列 XHTTP 为「不包含」
SHARE_GO="go-backend/internal/kernel/share.go"
NODES_MD="doc/guide/nodes.md"
if grep -q "xhttp-opts" "$SHARE_GO"; then
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

# ── 2. 技能包接口覆盖：handler.go 全部 /api/v1 路由必须在技能包或 ai-skill.md 中被引用
HANDLER="go-backend/internal/http/handler/handler.go"
GH_MIRROR_GO="go-backend/internal/http/handler/gh_mirror.go"
routes=$( { grep -oP 'mux\.HandleFunc\("\K[^"]+' "$HANDLER";
            grep -oP 'ghMirrorPath\s*=\s*"\K[^"]+' "$GH_MIRROR_GO"; } | grep '^/api/v1/' | sort -u )
refs=$(grep -rhoP '/api/v1/[a-zA-Z0-9/_-]+' skills/flmx-api/ doc/ai-skill.md | sort -u)
missing=$(comm -23 <(echo "$routes") <(echo "$refs"))
if [ -n "$missing" ]; then
  n=$(echo "$missing" | wc -l)
  note_fail "技能包未覆盖 $n 条路由："
  echo "$missing" | sed 's/^/    - /'
else
  note_ok "技能包覆盖 handler.go 全部 $(echo "$routes" | wc -l) 条 /api/v1 路由"
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
code_events=$(grep -rhoP --exclude='*_test.go' 'notifyEvent[A-Za-z]+\s*=\s*"\K[a-z_]+' go-backend/internal/http/handler/ | sort -u)
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

echo "----"
if [ "$FAIL" -eq 0 ]; then
  echo "RESULT: PASS（known-pending 项不计失败）"
else
  echo "RESULT: FAIL"
fi
exit "$FAIL"
