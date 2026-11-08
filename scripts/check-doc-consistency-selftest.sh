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
# FIX-DOC-R 扩围：变体 4–7 覆盖 §18–§21（去重键凭据指纹、计量伴
# 生端口区间、监控目标两面口径、服务监控限值披露），每族一个变
# 体、改坏文案必须被对应节抓红并点名。§10(b) 的 x 包计数标记属
# 环境漂移敏感面（合并波次新增带测试包即漂移），不属本自检变体
# 面：计数对账由 INT-3/R2 在合并尖端按实数重标定并点名。
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

echo "== variant 4: dedupe key drops the credential fingerprint again must FAIL and name it (§18) =="
sed -i 's/凭据指纹：凭据字段集的 sha256 前 16 位/凭据原文/' "$MIR/doc/guide/subscription.md"
if run_gate "$TMP/v4.log"; then
  echo "FAIL: variant 4 was not caught"; exit 1
fi
grep -q "去重键口径门" "$TMP/v4.log" && echo "OK: variant 4 caught and named" || {
  echo "FAIL: variant 4 caught but the drift was not named"; sed 's/^/    /' "$TMP/v4.log"; exit 1; }
cp "$ROOT/doc/guide/subscription.md" "$MIR/doc/guide/subscription.md"

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

echo "SELFTEST PASS"
