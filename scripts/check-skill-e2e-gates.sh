#!/usr/bin/env bash
# check-skill-e2e-gates.sh — mechanical gates for the W2-03 lane
# (R18-A5-02 / R18-A5-03 / R18-A5-05). Pure grep/file checks, no build,
# no server, no browser. Each section is fail-closed: an empty extraction
# or a missing file is a hard failure, never a silent pass.
#
# Kept as its own script (rather than a section of
# scripts/check-doc-consistency.sh) so the lane's regression gates are
# reviewable and runnable on their own; folding them into the doc gate
# is a follow-up for the coordinator.
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1
FAIL=0

note_ok() { echo "PASS: $1"; }
note_fail() { echo "FAIL: $1"; FAIL=1; }

# ── R18-A5-02: the skill is distributed as a Release tgz, never via npm.
# @flmx/skill-api does not exist on npm (404); SKILL.md must install from
# the Release asset and nothing under skills/flmx-api/ may point at npm.
SKILL_VER="$(grep -o '"version": *"[^"]*"' skills/flmx-api/package.json 2>/dev/null | head -1 | sed 's/.*"version": *"//; s/"//')"
if [ -z "$SKILL_VER" ]; then
  note_fail "R18-A5-02: cannot extract skills/flmx-api/package.json version (premise failed)"
else
  npm_refs="$(grep -rn 'npm i -g @flmx\|npm install -g @flmx\|registry\.npmjs\.org' skills/flmx-api/ || true)"
  if [ -n "$npm_refs" ]; then
    note_fail "R18-A5-02: skills/flmx-api still references npm install/registry: $(echo "$npm_refs" | head -3 | tr '\n' ';')"
  elif ! grep -q "releases/download/${SKILL_VER}/flmx-skill-api-${SKILL_VER}\\.tgz" skills/flmx-api/SKILL.md; then
    note_fail "R18-A5-02: SKILL.md lacks the release-tgz install URL for ${SKILL_VER}"
  else
    note_ok "R18-A5-02: SKILL.md installs from the ${SKILL_VER} release tgz, no npm reference under skills/flmx-api/"
  fi
fi

# ── R18-A5-03: go-backend/patch.py is a stale change script — its
# anchors partly still match a refactored tree, so re-running it injects
# a duplicate ensureConnLimiterOnNode, an undefined cLimiterName and a
# 9-arg buildForwardServiceConfigs call. The change it used to carry
# already exists in evolved form in the tree; the script must not exist.
if [ -e go-backend/patch.py ]; then
  note_fail "R18-A5-03: go-backend/patch.py still present (stale change script, re-running breaks the build)"
else
  note_ok "R18-A5-03: go-backend/patch.py absent"
fi

# ── R18-A5-05: e2e assertions must be real — no always-true, conditional
# or fake assertions. Banned patterns in the browser/API test files:
# `or True` (tautology), `count >= 0` (counts are never negative),
# `localStorage.clear()` as a logout substitute (never exercises the
# product logout path), and `if response["code"] == 0:` guarding the
# only assertions in test_api.py (a non-zero code then passes silently).
for pat in 'or True' 'count >= 0' 'localStorage.clear()'; do
  hits="$(grep -rn -- "$pat" tests/e2e/test_*.py || true)"
  if [ -n "$hits" ]; then
    note_fail "R18-A5-05: fake-green pattern '$pat' in e2e tests: $(echo "$hits" | head -3 | tr '\n' ';')"
  fi
done
hits="$(grep -rn 'if response\["code"\] == 0:' tests/e2e/test_api.py || true)"
if [ -n "$hits" ]; then
  note_fail "R18-A5-05: conditional assertion in test_api.py: $(echo "$hits" | head -3 | tr '\n' ';')"
fi
if [ "$FAIL" -eq 0 ]; then
  note_ok "R18-A5-05: no fake-green assertion patterns in tests/e2e/test_*.py"
fi

echo "----"
if [ "$FAIL" -eq 0 ]; then
  echo "RESULT: PASS"
else
  echo "RESULT: FAIL"
fi
exit "$FAIL"
