#!/usr/bin/env bash
# check-hist-metering-selftest.sh — S05 frontend gate mutation selftest:
# mirror presets.ts to a temp repo, require baseline PASS, then require
# FAIL on (1) emptyForm default flipped back to false, (2) toPayload
# sending meteringEnabled ungated.
#
# Usage: check-hist-metering-selftest.sh (no arguments, acts on this repo).
# Exit code: 0 = baseline PASS and both variants caught; 1 = otherwise.
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-hist-metering.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
MIR="$TMP/repo"

mkdir -p "$MIR/vite-frontend/src/pages/inbound"
cp "$ROOT/vite-frontend/src/pages/inbound/presets.ts" "$MIR/vite-frontend/src/pages/inbound/"

echo "== baseline: unmutated mirror must PASS =="
if bash "$GATE" "$MIR" > "$TMP/base.log" 2>&1; then
	echo "OK: baseline PASS"
else
	echo "FAIL: baseline did not pass"; sed 's/^/    /' "$TMP/base.log"; exit 1
fi

echo "== variant 1: emptyForm meteringEnabled true -> false must FAIL =="
python3 - "$MIR/vite-frontend/src/pages/inbound/presets.ts" <<'PYEOF'
import re, sys
p = sys.argv[1]
s = open(p).read()
start = s.index("export function emptyForm(")
end = s.index("\n}\n", start)
body = s[start:end]
assert "meteringEnabled: true," in body, "baseline shape changed"
body = body.replace("meteringEnabled: true,", "meteringEnabled: false,", 1)
open(p, "w").write(s[:start] + body + s[end:])
PYEOF
if bash "$GATE" "$MIR" > "$TMP/v1.log" 2>&1; then
	echo "FAIL: variant 1 was not caught"; exit 1
fi
grep -q "emptyForm must default" "$TMP/v1.log" && echo "OK: variant 1 caught" || { echo "FAIL: variant 1 caught but not named"; sed 's/^/    /' "$TMP/v1.log"; exit 1; }
cp "$ROOT/vite-frontend/src/pages/inbound/presets.ts" "$MIR/vite-frontend/src/pages/inbound/"

echo "== variant 2: toPayload ungated meteringEnabled must FAIL =="
python3 - "$MIR/vite-frontend/src/pages/inbound/presets.ts" <<'PYEOF'
import re, sys
p = sys.argv[1]
s = open(p).read()
start = s.index("export function toPayload(")
end = s.index("\n}\n", start)
body = s[start:end]
# Replace the gated ternary with the bare field send.
m = re.search(r"meteringEnabled:\n(?:.*\n)*?        \? form\.meteringEnabled\n        : false,", body)
assert m, "baseline gate shape changed"
body = body[:m.start()] + "meteringEnabled: form.meteringEnabled," + body[m.end():]
open(p, "w").write(s[:start] + body + s[end:])
PYEOF
if bash "$GATE" "$MIR" > "$TMP/v2.log" 2>&1; then
	echo "FAIL: variant 2 was not caught"; exit 1
fi
grep -q "ungated" "$TMP/v2.log" && echo "OK: variant 2 caught" || { echo "FAIL: variant 2 caught but not named"; sed 's/^/    /' "$TMP/v2.log"; exit 1; }

echo "OK: check-hist-metering gate + selftest green"
