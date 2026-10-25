#!/bin/bash
# R17-FIX-D leg1 single attempt: paused forward 6 + occupied 14601 + agent
# restart (redeploy double-push) + watcher frees the port on the guard's
# update-fallback "send" line so the fallback AddService can succeed.
# Usage: bash R17-FIX-D-leg1.sh <label> ; log to stdout (caller tees).
# Preconditions (caller ensures): forward 6 paused (config has 1x 6_1_0_tcp
# + 1x 6_1_0_udp, unregistered, 14601 not listening); agent active.
set -u
LABEL="${1:-attempt}"
R=/root/r17d
echo "=== leg1 $LABEL start=$(date -u +%FT%TZ) ==="

dup_count() {
  python3 - "$1" <<'PYEOF'
import json, sys
from collections import Counter
cfg = json.load(open(sys.argv[1]))
names = [s.get("name", "") for s in cfg.get("services", []) if isinstance(s, dict)]
dups = {n: c for n, c in Counter(names).items() if c > 1}
print("services=%d dup=%s" % (len(names), dups if dups else "none"))
for s in cfg.get("services", []):
    if isinstance(s, dict) and s.get("name", "").startswith("6_1_0"):
        print("  %s addr=%s paused=%s" % (s.get("name"), s.get("addr"), (s.get("metadata") or {}).get("paused")))
PYEOF
}

echo "--- pre: $(md5sum /etc/flux_agent/gost.json | cut -d' ' -f1) lines=$(wc -l < /etc/flux_agent/gost.json)"
dup_count /etc/flux_agent/gost.json
ss -ltn 2>/dev/null | grep -E ':14601' && echo "14601 LISTENING (unexpected pre)" || echo "14601 free pre"

echo "--- start dummy occupier"
nohup python3 $R/R17-FIX-D-dummy.py > $R/dummy-$LABEL.log 2>&1 &
DUMMY_PID=$!
sleep 1
cat $R/dummy-$LABEL.log
ss -ltn 2>/dev/null | grep -E ':14601' || echo "14601 NOT listening after dummy start (PROBLEM)"

echo "--- start watcher"
BACKEND_LOG=$(ls /var/lib/docker/containers/$(docker inspect -f '{{.Id}}' flux-panel-backend)/*-json.log)
echo "backend log: $BACKEND_LOG"
nohup python3 $R/R17-FIX-D-watcher.py "$BACKEND_LOG" $DUMMY_PID 150 > $R/watcher-$LABEL.log 2>&1 &
WATCHER_PID=$!

echo "--- restart agent (triggers onNodeOnline redeploy)"
systemctl reset-failed flux_agent
systemctl restart flux_agent
sleep 5
systemctl is-active flux_agent
echo "--- wait for watcher to fire or timeout (max 150s)"
wait $WATCHER_PID
echo "watcher exit=$?"
sleep 20
echo "--- watcher log:"
cat $R/watcher-$LABEL.log
echo "--- dummy log:"
cat $R/dummy-$LABEL.log
ps -p $DUMMY_PID >/dev/null 2>&1 && echo "dummy STILL RUNNING (killing)" && kill $DUMMY_PID || echo "dummy gone"
echo "--- post: $(md5sum /etc/flux_agent/gost.json | cut -d' ' -f1) lines=$(wc -l < /etc/flux_agent/gost.json)"
dup_count /etc/flux_agent/gost.json
systemctl is-active flux_agent
echo "--- guard lines in backend log (last 40 guard lines):"
grep -h "r17-addservice-guard" "$BACKEND_LOG" 2>/dev/null | tail -40 | sed 's/.*"log":"//; s/\\n".*//' | head -40
echo "=== leg1 $LABEL end=$(date -u +%FT%TZ) ==="
