#!/usr/bin/env bash
# check-script-version-bump-selftest.sh — 印记联动门禁的变体自检
# （A4-02）。背景：本门此前无变异自测——它只在发版路径（docker-
# build check-version、fetch-depth 0）执行，判定若被改坏要等真发
# 版才暴露。本自检在临时 git 仓库里合成发版史：打 tag 后只改正文
# 不 bump 印记必须 FAIL 点名；bump 后必须 PASS；正文无变更（只动
# 其他文件）必须走「无需递增」PASS。
#
# 用法：check-script-version-bump-selftest.sh（无参数，作用于本仓库）。
# 退出码：0 = 全部断言成立；1 = 任一断言不成立。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-script-version-bump.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
REPO="$TMP/repo"
mkdir -p "$REPO/scripts"
cp "$GATE" "$REPO/scripts/"

cd "$REPO" || exit 1
git init -q
git config user.email selftest@example.invalid
git config user.name selftest
cat > panel_install.sh <<'EOF'
#!/usr/bin/env bash
SCRIPT_VERSION="20260101.01"
PINNED_VERSION=""
echo body-v1
EOF
cat > install.sh <<'EOF'
#!/usr/bin/env bash
PINNED_VERSION=""
echo installer-v1
EOF
echo x > other.txt
git add -A
git commit -qm "release 1.0.0"
git tag 1.0.0

run_gate() { bash scripts/check-script-version-bump.sh > "$1" 2>&1; }

echo "== variant 1: body changed after tag, stamp NOT bumped, must FAIL and name it =="
sed -i 's|echo body-v1|echo body-v2|' panel_install.sh
git commit -qam "body change without bump"
if run_gate "$TMP/v1.log"; then
  echo "FAIL: variant 1 was not caught"; exit 1
fi
grep -q "未递增" "$TMP/v1.log" && echo "OK: variant 1 caught and named" || {
  echo "FAIL: variant 1 caught but the stale stamp was not named"; sed 's/^/    /' "$TMP/v1.log"; exit 1; }

echo "== variant 2: stamp bumped, must PASS =="
sed -i 's|SCRIPT_VERSION="20260101.01"|SCRIPT_VERSION="20260101.02"|' panel_install.sh
git commit -qam "bump stamp"
if run_gate "$TMP/v2.log"; then
  echo "OK: variant 2 PASS"
else
  echo "FAIL: variant 2 should pass after bump"; sed 's/^/    /' "$TMP/v2.log"; exit 1
fi

echo "== variant 3: new tag, then only an unrelated file changes, must PASS via the no-body-change path =="
git tag 2.0.0
echo y >> other.txt
git commit -qam "unrelated change"
if run_gate "$TMP/v3.log"; then
  grep -q "无变更" "$TMP/v3.log" && echo "OK: variant 3 PASS (no body change)" || {
    echo "FAIL: variant 3 passed but via an unexpected path"; sed 's/^/    /' "$TMP/v3.log"; exit 1; }
else
  echo "FAIL: variant 3 should pass (body unchanged)"; sed 's/^/    /' "$TMP/v3.log"; exit 1
fi

# ── G-R4-05 下半：PINNED_VERSION 空占位断言。发版 sed 只认
# ^PINNED_VERSION="" 锚点；占位在日常提交里被误填，sed 匹配不
# 上、要等到发版 grep 才拦。变体 4/5 各填一个文件，必须 FAIL
# 点名该文件。
echo "== variant 4: PINNED_VERSION filled in panel_install.sh, must FAIL and name it =="
sed -i 's|PINNED_VERSION=""|PINNED_VERSION="1.2.3"|' panel_install.sh
git commit -qam "fill pinned placeholder in panel_install.sh"
if run_gate "$TMP/v4.log"; then
  echo "FAIL: variant 4 was not caught"; exit 1
fi
grep -q "PINNED_VERSION" "$TMP/v4.log" && grep -q "panel_install.sh" "$TMP/v4.log" && echo "OK: variant 4 caught and named" || {
  echo "FAIL: variant 4 caught but the filled placeholder was not named"; sed 's/^/    /' "$TMP/v4.log"; exit 1; }

echo "== variant 5: PINNED_VERSION filled in install.sh, must FAIL and name it =="
sed -i 's|PINNED_VERSION="1.2.3"|PINNED_VERSION=""|' panel_install.sh
sed -i 's|PINNED_VERSION=""|PINNED_VERSION="1.2.3"|' install.sh
git commit -qam "fill pinned placeholder in install.sh"
if run_gate "$TMP/v5.log"; then
  echo "FAIL: variant 5 was not caught"; exit 1
fi
grep -q "PINNED_VERSION" "$TMP/v5.log" && grep -q "install.sh" "$TMP/v5.log" && echo "OK: variant 5 caught and named" || {
  echo "FAIL: variant 5 caught but the filled placeholder was not named"; sed 's/^/    /' "$TMP/v5.log"; exit 1; }

# ── G-R4-05：bump 门的 CI 接线断言。本门此前只在发版路径
# （docker-build check-version）执行，PR 阶段改正文不 bump 全绿、
# 到发版才拦。自检除脚本行为外另断言 ci-build.yml 含一个
# script-version-bump 作业：全历史 checkout（fetch-depth: 0，
# 否则 tag 缺失使本门 NOTE 放行、等于没接线）＋调用本脚本。
# 接线被删即本自检先红（CI 的 gate-selftests job 跑本自检）。
echo "== workflow wiring: ci-build.yml must run the bump gate on every push/PR =="
CI_YML="$ROOT/.github/workflows/ci-build.yml"
grep -q "script-version-bump" "$CI_YML" || { echo "FAIL: ci-build.yml has no script-version-bump job (G-R4-05)"; exit 1; }
grep -q "check-script-version-bump.sh" "$CI_YML" || { echo "FAIL: ci-build.yml script-version-bump job does not call check-script-version-bump.sh (G-R4-05)"; exit 1; }
awk '/script-version-bump:/{f=1} f && /fetch-depth: 0/{ok=1; exit} f && /^  [a-z-]+:/ && !/script-version-bump:/{exit} END{exit !ok}' "$CI_YML" || {
  echo "FAIL: ci-build.yml script-version-bump job lacks fetch-depth: 0 (tags missing -> the gate NOTE-passes, unwired in effect)"; exit 1; }
echo "OK: ci-build.yml wires the bump gate with full history"

# ── R18-R3-A1-02：跨 commit 盲区。两端比较只看净值时，中间某个
# 改本体不 bump 的 commit 会被后来的 bump 在净值上掩盖（bf6d4ebb
# 原形）。变体 6：本体改动（无 bump）→ 本体改动 + bump（净值愈
# 合）→ 本体再改动（无 bump），门必须 FAIL 点名最近一次改动；
# 变体 7：在变体 6 之上再补一次本体改动 + bump，门必须 PASS（后
# 来的 bump 覆盖了最近一次本体改动，窗口只存在于历史中间态）。
echo "== variant 6: cross-commit: unbumped body change masked by an intermediate bump, then another unbumped body change, must FAIL =="
# variant 5 leaves install.sh's placeholder filled; restore it first so
# the PINNED check does not fire before the stamp check under test.
sed -i 's|PINNED_VERSION="1.2.3"|PINNED_VERSION=""|' install.sh
git commit -qam "restore install.sh placeholder after variant 5"
sed -i 's|echo body-v2|echo body-v3|' panel_install.sh
git commit -qam "body change without bump (first)"
sed -i 's|echo body-v3|echo body-v4|' panel_install.sh
sed -i 's|SCRIPT_VERSION="20260101.02"|SCRIPT_VERSION="20260101.03"|' panel_install.sh
git commit -qam "body change with bump (heals the first)"
sed -i 's|echo body-v4|echo body-v5|' panel_install.sh
git commit -qam "body change without bump (latest)"
if run_gate "$TMP/v6.log"; then
  echo "FAIL: variant 6 was not caught (cross-commit unbumped body change)"; exit 1
fi
grep -q "未再递增" "$TMP/v6.log" && echo "OK: variant 6 caught and named" || {
  echo "FAIL: variant 6 caught but the latest body change was not named"; sed 's/^/    /' "$TMP/v6.log"; exit 1; }

echo "== variant 7: cross-commit healed: latest body change carries its own bump, must PASS =="
sed -i 's|echo body-v5|echo body-v6|' panel_install.sh
sed -i 's|SCRIPT_VERSION="20260101.03"|SCRIPT_VERSION="20260101.04"|' panel_install.sh
git commit -qam "body change with bump (heals the latest)"
if run_gate "$TMP/v7.log"; then
  grep -q "已被其后 bump 覆盖" "$TMP/v7.log" && echo "OK: variant 7 PASS (latest body change covered)" || {
    echo "FAIL: variant 7 passed but not via the coverage check"; sed 's/^/    /' "$TMP/v7.log"; exit 1; }
else
  echo "FAIL: variant 7 should pass (latest body change is covered by its own bump)"; sed 's/^/    /' "$TMP/v7.log"; exit 1
fi

echo "SELFTEST PASS"
