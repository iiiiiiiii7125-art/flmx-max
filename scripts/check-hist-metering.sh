#!/usr/bin/env bash
# check-hist-metering.sh — S05 (AUDIT-HIST) frontend metering-default gate.
#
# No frontend test infrastructure exists in this repo, so the S05
# default-on is pinned by grep assertions on
# vite-frontend/src/pages/inbound/presets.ts:
#   1. emptyForm (the new-inbound form) defaults meteringEnabled to true.
#      The old false default made every default-path SS2022 inbound
#      silently unmetered (subscription traffic stuck at 0/0, Issue B).
#   2. toPayload gates the switch by metering eligibility (SS2022, no
#      ShadowTLS front, direct-serving shape) before sending it. Without
#      the gate, the default-on would make every non-SS create send
#      meteringEnabled=true and hit the backend's "explicitly enabled but
#      ineligible" refusal.
# The backend half (create-omitted defaults ON when eligible) is pinned
# by the Go test TestS05CreateDefaultsMeteringOnForEligibleSS2022, not
# by this script.
#
# Usage: check-hist-metering.sh [REPO_ROOT] (default: script's repo root).
# Exit code: 0 = all assertions hold; 1 = drift; 2 = premise failure.
set -u

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
FILE="$ROOT/vite-frontend/src/pages/inbound/presets.ts"
fail=0

[ -f "$FILE" ] || { echo "FAIL: $FILE not found"; exit 2; }

# Extract one top-level function body (from its export line to the next
# closing brace at column 0) so assertions cannot be satisfied by a
# same-named field in another function.
func_body() {
	awk -v fn="$1" '
		$0 ~ "^export function " fn "\\(" { in_fn = 1 }
		in_fn { print }
		in_fn && /^}$/ { exit }
	' "$FILE"
}

EMPTY="$(func_body emptyForm)"
[ -n "$EMPTY" ] || { echo "FAIL: emptyForm not found in $FILE"; exit 2; }
if printf '%s\n' "$EMPTY" | grep -q 'meteringEnabled: true,'; then
	echo "OK: emptyForm defaults meteringEnabled to true"
else
	echo "FAIL: emptyForm must default meteringEnabled to true (S05)"
	fail=1
fi
if printf '%s\n' "$EMPTY" | grep -q 'meteringEnabled: false,'; then
	echo "FAIL: emptyForm still carries meteringEnabled: false (S05)"
	fail=1
fi

PAYLOAD="$(func_body toPayload)"
[ -n "$PAYLOAD" ] || { echo "FAIL: toPayload not found in $FILE"; exit 2; }
if printf '%s\n' "$PAYLOAD" | grep -q 'form.protocol === "shadowsocks"' &&
	printf '%s\n' "$PAYLOAD" | grep -q 'form.method.startsWith("2022-")' &&
	printf '%s\n' "$PAYLOAD" | grep -q 'form.securityType !== "shadowtls"'; then
	echo "OK: toPayload gates meteringEnabled by SS2022 eligibility"
else
	echo "FAIL: toPayload must gate meteringEnabled by SS2022 eligibility (S05)"
	fail=1
fi
if printf '%s\n' "$PAYLOAD" | grep -q '^[[:space:]]*meteringEnabled: form.meteringEnabled,$'; then
	echo "FAIL: toPayload sends meteringEnabled ungated (S05)"
	fail=1
fi

exit "$fail"
