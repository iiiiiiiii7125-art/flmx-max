#!/usr/bin/env bash
# check-doc-anchors-selftest.sh — 文档锚点对账门禁的变体自检（A4-02）。
#
# 背景：本门此前无变异自测。本自检用一组最小合成文档（不依赖真实
# doc 树）：基线链接全部命中必须 PASS；把目标标题改名使锚点失效、
# 以及链接指向不存在的文件，两种变体都必须 FAIL 并点名。
#
# 用法：check-doc-anchors-selftest.sh（无参数）。
# 退出码：0 = 基线 PASS 且变体均被抓；1 = 任一断言不成立。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-doc-anchors.mjs"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
DOC="$TMP/doc"
mkdir -p "$DOC"

cat > "$DOC/a.md" <<'EOF'
# 页面 A

见 [配置一节](b.md#配置) 与 [本页安装](#安装步骤)。

## 安装步骤

正文。
EOF
cat > "$DOC/b.md" <<'EOF'
# 页面 B

## 配置

正文。
EOF

echo "== baseline: all anchors resolve, must PASS =="
if node "$GATE" "$DOC" > "$TMP/base.log" 2>&1; then
  echo "OK: baseline PASS"
else
  echo "FAIL: baseline did not pass"; sed 's/^/    /' "$TMP/base.log"; exit 1
fi

echo "== variant 1: rename the target heading (anchor dies) must FAIL and name it =="
sed -i 's|^## 配置$|## 配置项|' "$DOC/b.md"
if node "$GATE" "$DOC" > "$TMP/v1.log" 2>&1; then
  echo "FAIL: variant 1 was not caught"; exit 1
fi
grep -q "无此锚点" "$TMP/v1.log" && echo "OK: variant 1 caught and named" || {
  echo "FAIL: variant 1 caught but the dead anchor was not named"; sed 's/^/    /' "$TMP/v1.log"; exit 1; }
sed -i 's|^## 配置项$|## 配置|' "$DOC/b.md"

echo "== variant 2: link to a missing file must FAIL and name it =="
printf '\n见 [缺失页](nope.md#配置)。\n' >> "$DOC/a.md"
if node "$GATE" "$DOC" > "$TMP/v2.log" 2>&1; then
  echo "FAIL: variant 2 was not caught"; exit 1
fi
grep -q "目标文件不存在" "$TMP/v2.log" && echo "OK: variant 2 caught and named" || {
  echo "FAIL: variant 2 caught but the missing file was not named"; sed 's/^/    /' "$TMP/v2.log"; exit 1; }

echo "SELFTEST PASS"
