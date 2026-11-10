#!/usr/bin/env bash
# check-doc-consistency-selftest.sh — 文档一致性门禁的变体自检（A4-02）。
#
# 背景：本门此前无变异自测。本自检把整棵树镜像到临时目录（doc 门
# 的读取面横跨 doc/skills/脚本/后端/前端，逐文件镜像易漏面，整树
# 镜像 28MB 量级可接受；vite-frontend/node_modules 以符号链接指回
# 真实树，与门禁在 CI 前端 job 的运行前提一致）：未变异镜像必须
# PASS；两个变体必须 FAIL 并点名——(1) 技能包 package.json 版本与
# SKILL.md/ai-skill.md 脱钩（§3 三处一致）；(2) 技能包引用不存在的
# 路由（§2 幽灵接口）。
#
# 前提：node 在 PATH；go 在场时 §10/§16 等子项实核、无 go 时按门
# 自身口径 PENDING（不影响本自检的变体判定面——两变体均为纯文本
# 对账）。CI 的 gate-selftests job 所在 runner 自带 go 并先装前端
# 依赖；本地同理（未装依赖时 §14 会先红在环境前提上，按其输出装
# 依赖后复跑）。
#
# FIX-DOC-R 扩围：变体 5–7 覆盖 §19–§21（计量伴生端口区间、监控
# 目标两面口径、服务监控限值披露），每族一个变体、改坏文案必须被
# 对应节抓红并点名。（原变体 4 对应的 §18 去重键口径门已随订阅
# 去重功能于 2.1.2 删除，同波拆除。）§10(b) 的 x 包计数标记属
# 环境漂移敏感面（合并波次新增带测试包即漂移），不属本自检变体
# 面：计数对账由 INT-3/R2 在合并尖端按实数重标定并点名。
# FIX-XCOUNT 扩围：变体 8 覆盖 §10(b) x 包计数标记
# （ci-build.yml 的 [K11-COUNT:x-tested/x-untested] 声称 vs go list
# 实数）。标记在 int-wave 尖端 e5a83bf1 按实数重标定为 55/153
# （基线 557322be 实数 45/162——INT-3 合入波次给 9 个既有包新增
# 首个测试文件、FIX-LOGS 新增 internal/util/eventlog 包且自带
# 测试，净增 1 包，逐项对账见重标定提交说明）；INT-3 第三轮合入
# 又给 4 个既有包新增首个测试文件（internal/util/kcp、
# internal/util/resolver、limiter/rate、listener/kcp），总数仍
# 208，标记随之重标定为 59/149（逐包差集见重标定提交说明）。
# 变体 8 把镜像内
# 标记的 x-tested 数字改坏，必须被 §10(b) 抓红并点名（FAIL 行含
# 「声称 tested=」且给出实数）。无 go 时 §10(b) 按门自身口径
# PENDING 未核、变体不可判定，本自检在无 go 环境直接 FAIL 点名
# 环境前提（与头部「无 go 时 §10/§16 等子项实核」口径一致），
# 不静默跳过。注：变体 4–7（FIX-DOC-R 的 §18–§21 四族）与本变体
# 同树，合并后自检依次跑变体 1–8。
# FIX-212 W8 扩围：变体 9–12 覆盖 §22 四钉（探活参数、节点分组
# 限定句、协议开关语义、残留词零命中），每钉一个变体、改坏文案
# 必须被对应钉抓红并点名；变体 9–11 带变异落点断言防静默空转。
# FIX-GATE-CALIB 改钉（R2-GATE-02）：上文 55/153、59/149 均为历史
# 标定记录（出处见各重标定提交），不再是变体 8 的写死锚点——变体
# 8 改为运行时从镜像内 ci-build.yml 机械读现行标记值（其后标记又
# 经 INT-5 重标定为 61/147），按实读值做 -1 变异并按实读值拼装
# 期望点名串，另加变异落点断言防静默空转；各变体变异后逐个还原
# 本变体文件（变体 7 的 monitoring.md 还原一并补齐，旧实现漏还原
# 曾污染变体 8 的判定环境）。
#
# 用法：check-doc-consistency-selftest.sh（无参数，作用于本仓库）。
# 退出码：0 = 基线 PASS 且变体均被抓；1 = 任一断言不成立。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
MIR="$TMP/repo"
mkdir -p "$MIR"

# 整树镜像，但排除 .git 与 node_modules（后者以符号链接指回真实
# 树，避免 CI 里整包拷贝数百 MB 依赖）。镜像内另建空 git 索引并
# 全量 add：§14 的 check-secret-material 按 git 跟踪集（ls-files）
# 枚举扫描面，无 .git 时枚举失败、基线必红——索引须在挂
# node_modules 符号链接之前建好，使链接保持未跟踪、不入扫描集。
tar -C "$ROOT" --exclude='./.git' --exclude='./vite-frontend/node_modules' -cf - . | tar -C "$MIR" -xf -
git -C "$MIR" init -q
git -C "$MIR" add -A
if [ -d "$ROOT/vite-frontend/node_modules" ] && [ ! -e "$MIR/vite-frontend/node_modules" ]; then
  ln -s "$ROOT/vite-frontend/node_modules" "$MIR/vite-frontend/node_modules"
fi

run_gate() { bash "$MIR/scripts/check-doc-consistency.sh" > "$1" 2>&1; }

echo "== baseline: unmutated mirror must PASS =="
if run_gate "$TMP/base.log"; then
  echo "OK: baseline PASS"
else
  echo "FAIL: baseline did not pass"; sed 's/^/    /' "$TMP/base.log"; exit 1
fi

echo "== variant 1: skill package.json version drifts from SKILL.md must FAIL and name it (§3) =="
sed -i '0,/"version": "[^"]*"/s//"version": "9.9.9-selftest"/' "$MIR/skills/flmx-api/package.json"
if run_gate "$TMP/v1.log"; then
  echo "FAIL: variant 1 was not caught"; exit 1
fi
grep -q "技能包版本不一致" "$TMP/v1.log" && echo "OK: variant 1 caught and named" || {
  echo "FAIL: variant 1 caught but the drift was not named"; sed 's/^/    /' "$TMP/v1.log"; exit 1; }
cp "$ROOT/skills/flmx-api/package.json" "$MIR/skills/flmx-api/package.json"

echo "== variant 2: skill references a non-existent route must FAIL and name it (§2) =="
printf '\n示例：GET /api/v1/zzz-ghost-route\n' >> "$MIR/skills/flmx-api/SKILL.md"
if run_gate "$TMP/v2.log"; then
  echo "FAIL: variant 2 was not caught"; exit 1
fi
grep -q "zzz-ghost-route" "$TMP/v2.log" && echo "OK: variant 2 caught and named" || {
  echo "FAIL: variant 2 caught but the ghost route was not named"; sed 's/^/    /' "$TMP/v2.log"; exit 1; }
cp "$ROOT/skills/flmx-api/SKILL.md" "$MIR/skills/flmx-api/SKILL.md"

echo "== variant 3: guide claims a fixed default password again must FAIL and name it (§17) =="
sed -i 's/默认账号 `admin@user`，密码没有固定默认值/默认账号 `admin@user` \/ `admin@user`/' "$MIR/doc/guide/getting-started.md"
if run_gate "$TMP/v3.log"; then
  echo "FAIL: variant 3 was not caught"; exit 1
fi
grep -q "初始口令口径门" "$TMP/v3.log" && echo "OK: variant 3 caught and named" || {
  echo "FAIL: variant 3 caught but the drift was not named"; sed 's/^/    /' "$TMP/v3.log"; exit 1; }
cp "$ROOT/doc/guide/getting-started.md" "$MIR/doc/guide/getting-started.md"

echo "== variant 5: metering companion port range drifts back to 59999 must FAIL and name it (§19) =="
sed -i 's/在 \[20000, 60000) 内随机取空闲端口/在 20000–59999 内随机取空闲端口/' "$MIR/doc/guide/subscription.md"
if run_gate "$TMP/v5.log"; then
  echo "FAIL: variant 5 was not caught"; exit 1
fi
grep -q "计量伴生端口口径门" "$TMP/v5.log" && echo "OK: variant 5 caught and named" || {
  echo "FAIL: variant 5 caught but the drift was not named"; sed 's/^/    /' "$TMP/v5.log"; exit 1; }
cp "$ROOT/doc/guide/subscription.md" "$MIR/doc/guide/subscription.md"

echo "== variant 6: monitor targets collapse back to hard-reject-only must FAIL and name it (§20) =="
sed -i 's/按\*\*非公网全集\*\*拒绝/按**硬拒集**拒绝/' "$MIR/doc/guide/monitoring.md"
if run_gate "$TMP/v6.log"; then
  echo "FAIL: variant 6 was not caught"; exit 1
fi
grep -q "监控目标口径门" "$TMP/v6.log" && echo "OK: variant 6 caught and named" || {
  echo "FAIL: variant 6 caught but the drift was not named"; sed 's/^/    /' "$TMP/v6.log"; exit 1; }
cp "$ROOT/doc/guide/monitoring.md" "$MIR/doc/guide/monitoring.md"

echo "== variant 7: service-monitor limit disclosure drifts must FAIL and name it (§21) =="
sed -i 's/并发上限默认 20（保存上限 50）/并发上限默认 30（保存上限 50）/' "$MIR/doc/guide/monitoring.md"
if run_gate "$TMP/v7.log"; then
  echo "FAIL: variant 7 was not caught"; exit 1
fi
grep -q "服务监控限值披露门" "$TMP/v7.log" && echo "OK: variant 7 caught and named" || {
  echo "FAIL: variant 7 caught but the drift was not named"; sed 's/^/    /' "$TMP/v7.log"; exit 1; }
cp "$ROOT/doc/guide/monitoring.md" "$MIR/doc/guide/monitoring.md"

echo "== variant 8: ci-build.yml x-count marker drifts from go list reality must FAIL and name it (§10(b)) =="
if ! command -v go >/dev/null 2>&1; then
  echo "FAIL: variant 8 undecidable — go toolchain missing, §10(b) is PENDING in this environment (see header)"; exit 1
fi
# 变异目标与期望一律从镜像内现行标记机械导出、不写死计数
# （FIX-GATE-CALIB / R2-GATE-02：旧实现写死 59/149，INT-5 把标记
# 重标定为 61/147 后 sed 零匹配静默空转、期望串永不命中，自检在
# 任何环境下都无法 PASS）。变异落点另行断言，锚点漂移即点名红，
# 不许再静默空转。
CI_YML="$MIR/.github/workflows/ci-build.yml"
CUR_T=$(grep -oP '\[K11-COUNT:x-tested=\K[0-9]+' "$CI_YML" | head -n 1)
CUR_U=$(grep -oP '\[K11-COUNT:x-untested=\K[0-9]+' "$CI_YML" | head -n 1)
if [ -z "$CUR_T" ] || [ -z "$CUR_U" ]; then
  echo "FAIL: variant 8 cannot read the current x-count markers from the mirror ci-build.yml"; exit 1
fi
MUT_T=$((CUR_T - 1))
sed -i "s/\[K11-COUNT:x-tested=${CUR_T}\]/[K11-COUNT:x-tested=${MUT_T}]/" "$CI_YML"
grep -q "\[K11-COUNT:x-tested=${MUT_T}\]" "$CI_YML" || {
  echo "FAIL: variant 8 mutation did not land (marker anchor drifted)"; exit 1; }
if run_gate "$TMP/v8.log"; then
  echo "FAIL: variant 8 was not caught"; exit 1
fi
grep -q "声称 tested=${MUT_T} untested=${CUR_U} ≠ 实数 tested=${CUR_T} untested=${CUR_U}" "$TMP/v8.log" && echo "OK: variant 8 caught and named" || {
  echo "FAIL: variant 8 caught but the drift was not named"; sed 's/^/    /' "$TMP/v8.log"; exit 1; }
cp "$ROOT/.github/workflows/ci-build.yml" "$MIR/.github/workflows/ci-build.yml"

echo "== variant 9: keepalive params drift in forward.md must FAIL and name it (§22a) =="
sed -i 's/满 180 秒后/满 181 秒后/' "$MIR/doc/guide/forward.md"
grep -q "181 秒" "$MIR/doc/guide/forward.md" || {
  echo "FAIL: variant 9 mutation did not land (keepalive anchor drifted)"; exit 1; }
if run_gate "$TMP/v9.log"; then
  echo "FAIL: variant 9 was not caught"; exit 1
fi
grep -q "探活参数钉子" "$TMP/v9.log" && echo "OK: variant 9 caught and named" || {
  echo "FAIL: variant 9 caught but the drift was not named"; sed 's/^/    /' "$TMP/v9.log"; exit 1; }
cp "$ROOT/doc/guide/forward.md" "$MIR/doc/guide/forward.md"

echo "== variant 10: node-group section loses its subscription exclusion must FAIL and name it (§22b) =="
sed -i 's/不参与订阅、转发、选线/不参与转发、选线/' "$MIR/doc/guide/nodes.md"
grep -q "不参与订阅" "$MIR/doc/guide/nodes.md" && {
  echo "FAIL: variant 10 mutation did not land (group-section anchor drifted)"; exit 1; }
if run_gate "$TMP/v10.log"; then
  echo "FAIL: variant 10 was not caught"; exit 1
fi
grep -q "节点分组钉子" "$TMP/v10.log" && echo "OK: variant 10 caught and named" || {
  echo "FAIL: variant 10 caught but the drift was not named"; sed 's/^/    /' "$TMP/v10.log"; exit 1; }
cp "$ROOT/doc/guide/nodes.md" "$MIR/doc/guide/nodes.md"

echo "== variant 11: protocol-switch section reverts to listener wording must FAIL and name it (§22c) =="
sed -i 's/屏蔽开关/监听是否开启/' "$MIR/doc/guide/nodes.md"
grep -q "监听是否开启" "$MIR/doc/guide/nodes.md" || {
  echo "FAIL: variant 11 mutation did not land (switch-section anchor drifted)"; exit 1; }
if run_gate "$TMP/v11.log"; then
  echo "FAIL: variant 11 was not caught"; exit 1
fi
grep -q "协议开关钉子" "$TMP/v11.log" && echo "OK: variant 11 caught and named" || {
  echo "FAIL: variant 11 caught but the drift was not named"; sed 's/^/    /' "$TMP/v11.log"; exit 1; }
cp "$ROOT/doc/guide/nodes.md" "$MIR/doc/guide/nodes.md"

echo "== variant 12: banned term reappears in index.md must FAIL and name it (§22d) =="
printf '\n远程节点（自测变异行）\n' >> "$MIR/doc/index.md"
if run_gate "$TMP/v12.log"; then
  echo "FAIL: variant 12 was not caught"; exit 1
fi
grep -q "残留词钉子" "$TMP/v12.log" && echo "OK: variant 12 caught and named" || {
  echo "FAIL: variant 12 caught but the drift was not named"; sed 's/^/    /' "$TMP/v12.log"; exit 1; }
cp "$ROOT/doc/index.md" "$MIR/doc/index.md"

echo "SELFTEST PASS"
