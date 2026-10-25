#!/bin/bash
# R17-FIX-D restart probe: restart the agent (onNodeOnline redeploy double
# push) and poll gost.json every 1s for duplicate service names, because a
# poisoned write can be cleaned up again by a later retry/redeploy.
# Usage: bash R17-FIX-D-restart-probe.sh <label> [poll_seconds=90]
# Log to stdout (caller tees). Any dup sighting is saved to
# /root/r17d/dup-<label>-<n>.json with its md5.
set -u
LABEL="${1:-probe}"
POLL="${2:-90}"
R=/root/r17d
BACKEND_LOG=$(ls /var/lib/docker/containers/$(docker inspect -f '{{.Id}}' flux-panel-backend)/*-json.log)
GUARD_BEFORE=$(grep -c "r17-addservice-guard" "$BACKEND_LOG" 2>/dev/null || echo 0)
echo "=== restart-probe $LABEL start=$(date -u +%FT%TZ) guard_before=$GUARD_BEFORE ==="
dup_state() {
  python3 - "$1" <<'PYEOF'
import json, sys
from collections import Counter
try:
    cfg = json.load(open(sys.argv[1]))
except Exception as e:
    print("UNREADABLE " + str(e)); sys.exit(0)
names = [s.get("name", "") for s in cfg.get("services", []) if isinstance(s, dict)]
dups = {n: c for n, c in Counter(names).items() if c > 1}
print("services=%d dup=%s" % (len(names), dups if dups else "none"))
PYEOF
}
echo "--- pre: $(md5sum /etc/flux_agent/gost.json | cut -d' ' -f1) $(dup_state /etc/flux_agent/gost.json)"
echo "--- restart agent"
systemctl reset-failed flux_agent
systemctl restart flux_agent
sleep 5
systemctl is-active flux_agent
echo "--- polling gost.json every 1s for ${POLL}s"
HITS=0
for i in $(seq 1 "$POLL"); do
  state=$(dup_state /etc/flux_agent/gost.json)
  if echo "$state" | grep -q "dup={"; then
    HITS=$((HITS + 1))
    cp /etc/flux_agent/gost.json "$R/dup-$LABEL-$HITS.json"
    echo "DUP SIGHTING t+${i}s: $state md5=$(md5sum /etc/flux_agent/gost.json | cut -d' ' -f1) saved=$R/dup-$LABEL-$HITS.json"
  fi
  sleep 1
done
echo "--- post: $(md5sum /etc/flux_agent/gost.json | cut -d' ' -f1) lines=$(wc -l < /etc/flux_agent/gost.json) $(dup_state /etc/flux_agent/gost.json) agent=$(systemctl is-active flux_agent)"
echo "--- new guard lines (with docker timestamps):"
python3 - "$BACKEND_LOG" "$GUARD_BEFORE" <<'PYEOF'
import json, sys
log, before = sys.argv[1], int(sys.argv[2])
lines = [l for l in open(log) if "r17-addservice-guard" in l]
for l in lines[before:]:
    try:
        e = json.loads(l)
        print(e.get("time"), e["log"].strip())
    except Exception:
        print("UNPARSED", l[:200])
PYEOF
echo "=== restart-probe $LABEL end=$(date -u +%FT%TZ) dup_sightings=$HITS ==="
