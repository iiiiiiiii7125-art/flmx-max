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
echo body-v1
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

echo "SELFTEST PASS"
