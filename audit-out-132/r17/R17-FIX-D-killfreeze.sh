#!/bin/bash
# R17-FIX-D kill-freeze attempt: restart the (un-fixed) agent and SIGKILL it
# the instant a fallback AddService reports sent-ok, then inspect gost.json.
# Usage: bash R17-FIX-D-killfreeze.sh <label> ; log to stdout (caller tees).
# Leaves the agent DEAD on a hit (caller must restore/start it afterwards);
# on a miss the agent keeps running.
set -u
LABEL="${1:-kill}"
R=/root/r17d
BACKEND_LOG=$(ls /var/lib/docker/containers/$(docker inspect -f '{{.Id}}' flux-panel-backend)/*-json.log)
echo "=== killfreeze $LABEL start=$(date -u +%FT%TZ) ==="
echo "--- pre: $(md5sum /etc/flux_agent/gost.json | cut -d' ' -f1) services=$(python3 -c "import json; print(len(json.load(open('/etc/flux_agent/gost.json'))['services']))")"
systemctl reset-failed flux_agent
systemctl restart flux_agent
sleep 5
AGENT_PID=$(systemctl show flux_agent -p MainPID --value)
echo "agent pid=$AGENT_PID active=$(systemctl is-active flux_agent)"
nohup python3 $R/R17-FIX-D-killwatch.py "$BACKEND_LOG" "$AGENT_PID" 60 > $R/killwatch-$LABEL.log 2>&1 &
WATCHER_PID=$!
wait $WATCHER_PID
echo "killwatch exit=$?"
cat $R/killwatch-$LABEL.log
sleep 2
echo "--- agent now: $(systemctl is-active flux_agent 2>/dev/null) pid=$(systemctl show flux_agent -p MainPID --value 2>/dev/null)"
echo "--- gost frozen: $(md5sum /etc/flux_agent/gost.json | cut -d' ' -f1) lines=$(wc -l < /etc/flux_agent/gost.json)"
python3 - <<'PYEOF'
import json
from collections import Counter
cfg = json.load(open("/etc/flux_agent/gost.json"))
names = [s.get("name", "") for s in cfg.get("services", []) if isinstance(s, dict)]
dups = {n: c for n, c in Counter(names).items() if c > 1}
print("services=%d dup=%s" % (len(names), dups if dups else "none"))
PYEOF
echo "=== killfreeze $LABEL end=$(date -u +%FT%TZ) ==="
