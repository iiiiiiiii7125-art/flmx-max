#!/bin/bash
# R17-FIX-D leg2: startup self-heal. Place the poisoned gost.json
# (md5 e381299b2aa931d2f7c097661ead6702) and restart the agent.
# Fixed agent: must start first time, dedupe warning in journal (+ no
# failure in agent-startup.log). Un-fixed agent: must fail, NRestarts=5.
# Usage: bash R17-FIX-D-leg2.sh <agent-file-in-/root/r17d> <label>
# Log to stdout (caller tees). Leaves the machine with the given agent and
# a PRISTINE gost.json, agent active (control run restores pristine after
# the expected failure).
set -u
AGENT_FILE="${1:?agent file}"
LABEL="${2:?label}"
R=/root/r17d
echo "=== leg2 $LABEL start=$(date -u +%FT%TZ) agent_file=$AGENT_FILE ==="
echo "--- install agent"
systemctl stop flux_agent; sleep 3
cp $R/$AGENT_FILE /etc/flux_agent/flux_agent; chmod +x /etc/flux_agent/flux_agent
md5sum /etc/flux_agent/flux_agent
rm -f /etc/flux_agent/agent-startup.log
echo "--- sanity start on current gost"
systemctl reset-failed flux_agent; systemctl start flux_agent; sleep 5
systemctl is-active flux_agent
md5sum /etc/flux_agent/gost.json
echo "--- poison gost.json (expect md5 e381299b2aa931d2f7c097661ead6702, 378 lines)"
systemctl stop flux_agent; sleep 3
pgrep -af '/etc/flux_agent/flux_agent' && echo "AGENT STILL RUNNING - abort" && exit 1 || echo "agent stopped"
cp /root/r17c/gost.json-poisoned /etc/flux_agent/gost.json
md5sum /etc/flux_agent/gost.json; wc -l /etc/flux_agent/gost.json
echo "--- restart agent on poisoned file"
systemctl reset-failed flux_agent; systemctl start flux_agent
sleep 25
echo "active=$(systemctl is-active flux_agent) NRestarts=$(systemctl show flux_agent -p NRestarts --value)"
pgrep -af '/etc/flux_agent/flux_agent' || echo "no agent process"
echo "--- journal self-heal / failure lines (last 30 min):"
journalctl -u flux_agent --no-pager --since "30 minutes ago" | grep -iE "self-heal|startup failed|fatal|failed|duplicate|parse service|register service" | tail -25 || echo "(no matching journal lines)"
echo "--- agent-startup.log:"
cat /etc/flux_agent/agent-startup.log 2>/dev/null || echo "(no agent-startup.log file)"
echo "--- post gost:"
md5sum /etc/flux_agent/gost.json; wc -l /etc/flux_agent/gost.json
python3 - <<'PYEOF'
import json
from collections import Counter
cfg = json.load(open("/etc/flux_agent/gost.json"))
names = [s.get("name", "") for s in cfg.get("services", []) if isinstance(s, dict)]
dups = {n: c for n, c in Counter(names).items() if c > 1}
print("services=%d dup=%s" % (len(names), dups if dups else "none"))
PYEOF
echo "--- restore pristine gost + start"
systemctl stop flux_agent 2>/dev/null || true; sleep 2
cp $R/gost.json.pristine /etc/flux_agent/gost.json
md5sum /etc/flux_agent/gost.json
systemctl reset-failed flux_agent; systemctl start flux_agent; sleep 5
echo "active=$(systemctl is-active flux_agent)"
ps -o pid,cmd -C flux_agent 2>/dev/null || ps aux | grep "[/]etc/flux_agent/flux_agent" | head -2
echo "=== leg2 $LABEL end=$(date -u +%FT%TZ) ==="
