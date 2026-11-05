#!/usr/bin/env bash
# check-workflow-pins-selftest.sh — 工作流钉版门禁的变体自检（A4-02）。
#
# 背景：本门此前无任何变异自测——提取正则漏形态（A4-01：flow 形
# - {uses: …} 与块标量形 uses: >- 不进普查）与 0 命中真空 PASS
# （A4-02：failures 为空即绿）都无从被发现。本自检把 .github/
# workflows（及 A4-03 容器普查所需的 Dockerfile/compose）镜像到
# 临时目录：未变异镜像必须 PASS；逐个变体必须 FAIL 并点名。若
# 哪天判定面被改坏，本自检先红。
#
# 用法：check-workflow-pins-selftest.sh（无参数，作用于本仓库）。
# 退出码：0 = 基线 PASS 且全部变体均被抓；1 = 任一断言不成立。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-workflow-pins.mjs"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
MIR="$TMP/repo"
WF="$MIR/.github/workflows"
SHA="11d5960a326750d5838078e36cf38b85af677262" # actions/checkout v4 的钉版 SHA

mkdir -p "$WF" "$MIR/go-backend" "$MIR/go-gost" "$MIR/vite-frontend"
cp "$ROOT"/.github/workflows/*.yml "$WF/"
# A4-03 起本门还普查容器镜像：镜像须带上三份 Dockerfile 与 compose。
cp "$ROOT/go-backend/Dockerfile" "$MIR/go-backend/"
cp "$ROOT/go-gost/Dockerfile" "$MIR/go-gost/"
cp "$ROOT/vite-frontend/Dockerfile" "$MIR/vite-frontend/"
cp "$ROOT/docker-compose-v4.yml" "$ROOT/docker-compose-v6.yml" "$MIR/"

run_gate() { node "$GATE" "$MIR" > "$1" 2>&1; }

echo "== baseline: unmutated mirror must PASS =="
if run_gate "$TMP/base.log"; then
  echo "OK: baseline PASS"
else
  echo "FAIL: baseline did not pass"; sed 's/^/    /' "$TMP/base.log"; exit 1
fi

echo "== variant 1: plain-form unpinned ref (evil/action@v1) must FAIL and name it =="
printf '\n      - uses: evil/action@v1\n' >> "$WF/ci-build.yml"
if run_gate "$TMP/v1.log"; then
  echo "FAIL: variant 1 was not caught"; exit 1
fi
grep -q "evil/action@v1" "$TMP/v1.log" && echo "OK: variant 1 caught and named" || {
  echo "FAIL: variant 1 caught but the ref was not named"; sed 's/^/    /' "$TMP/v1.log"; exit 1; }
cp "$ROOT/.github/workflows/ci-build.yml" "$WF/ci-build.yml"

echo "== variant 2: SHA pin without the # vX comment must FAIL =="
printf '\n      - uses: actions/checkout@%s\n' "$SHA" >> "$WF/ci-build.yml"
if run_gate "$TMP/v2.log"; then
  echo "FAIL: variant 2 was not caught"; exit 1
fi
grep -q "缺行尾版本注释" "$TMP/v2.log" && echo "OK: variant 2 caught and named" || {
  echo "FAIL: variant 2 caught but the reason was not named"; sed 's/^/    /' "$TMP/v2.log"; exit 1; }
cp "$ROOT/.github/workflows/ci-build.yml" "$WF/ci-build.yml"

echo "== variant 3: flow-form unpinned ref - {uses: evil/action@v1} must FAIL (A4-01) =="
printf '\n      - {uses: evil/action@v1}\n' >> "$WF/ci-build.yml"
if run_gate "$TMP/v3.log"; then
  echo "FAIL: variant 3 was not caught (flow form escaped the census)"; exit 1
fi
grep -q "evil/action@v1" "$TMP/v3.log" && echo "OK: variant 3 caught and named" || {
  echo "FAIL: variant 3 caught but the ref was not named"; sed 's/^/    /' "$TMP/v3.log"; exit 1; }
cp "$ROOT/.github/workflows/ci-build.yml" "$WF/ci-build.yml"

echo "== variant 4: block-scalar unpinned ref (uses: >-) must FAIL (A4-01) =="
printf '\n      - uses: >-\n          evil/action@v2\n' >> "$WF/ci-build.yml"
if run_gate "$TMP/v4.log"; then
  echo "FAIL: variant 4 was not caught (block-scalar form escaped the census)"; exit 1
fi
grep -q "evil/action@v2" "$TMP/v4.log" && echo "OK: variant 4 caught and named" || {
  echo "FAIL: variant 4 caught but the ref was not named"; sed 's/^/    /' "$TMP/v4.log"; exit 1; }
cp "$ROOT/.github/workflows/ci-build.yml" "$WF/ci-build.yml"

echo "== variant 5: docker:// without digest must FAIL =="
printf '\n      - uses: docker://alpine:3.22\n' >> "$WF/ci-build.yml"
if run_gate "$TMP/v5.log"; then
  echo "FAIL: variant 5 was not caught"; exit 1
fi
grep -q "docker://" "$TMP/v5.log" && echo "OK: variant 5 caught and named" || {
  echo "FAIL: variant 5 caught but the ref was not named"; sed 's/^/    /' "$TMP/v5.log"; exit 1; }
cp "$ROOT/.github/workflows/ci-build.yml" "$WF/ci-build.yml"

echo "== variant 7: Dockerfile FROM without digest must FAIL and name it (A4-03) =="
sed -i 's|^FROM debian:bookworm-slim@sha256:[0-9a-f]*|FROM debian:bookworm-slim|' "$MIR/go-backend/Dockerfile"
if run_gate "$TMP/v7.log"; then
  echo "FAIL: variant 7 was not caught (floating FROM passed)"; exit 1
fi
grep -q "debian:bookworm-slim" "$TMP/v7.log" && echo "OK: variant 7 caught and named" || {
  echo "FAIL: variant 7 caught but the image was not named"; sed 's/^/    /' "$TMP/v7.log"; exit 1; }
cp "$ROOT/go-backend/Dockerfile" "$MIR/go-backend/Dockerfile"

echo "== variant 8: floating workflow services image must FAIL and name it (R2-01-03) =="
sed -i 's|image: postgres:16-alpine@sha256:[0-9a-f]*|image: postgres:16-alpine|' "$WF/ci-build.yml"
if run_gate "$TMP/v8.log"; then
  echo "FAIL: variant 8 was not caught (floating workflow image: passed)"; exit 1
fi
grep -q "postgres:16-alpine" "$TMP/v8.log" && echo "OK: variant 8 caught and named" || {
  echo "FAIL: variant 8 caught but the image was not named"; sed 's/^/    /' "$TMP/v8.log"; exit 1; }
cp "$ROOT/.github/workflows/ci-build.yml" "$WF/ci-build.yml"

echo "== variant 6: empty workflows dir must exit 2 (no vacuous PASS, A4-02) =="
mkdir -p "$TMP/empty/.github/workflows"
node "$GATE" "$TMP/empty" > "$TMP/v6.log" 2>&1
rc=$?
if [ "$rc" -ne 2 ]; then
  echo "FAIL: variant 6 expected exit 2, got $rc"; sed 's/^/    /' "$TMP/v6.log"; exit 1
fi
grep -q "真空 PASS" "$TMP/v6.log" && echo "OK: variant 6 caught (exit 2, premise named)" || {
  echo "FAIL: variant 6 exited 2 but the premise was not named"; sed 's/^/    /' "$TMP/v6.log"; exit 1; }

echo "SELFTEST PASS"
