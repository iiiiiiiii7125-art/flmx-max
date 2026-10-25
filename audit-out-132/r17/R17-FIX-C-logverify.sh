#!/bin/bash
# R17-FIX-C task-1 verification: a poisoned gost.json restart must leave a
# diagnosable failure reason in the journal AND in agent-startup.log.
# Run on the test machine as root; log to /root/r17c/R17-FIX-C-logverify.log.
# Leaves the machine with the fix-C agent + new unit and a PRISTINE
# gost.json; the caller restores the pristine agent/unit afterwards.
set -u
R=/root/r17c
echo "=== R17-FIX-C log verify $(date -u +%FT%TZ) ==="
echo "--- backup current unit"
cp /etc/systemd/system/flux_agent.service $R/flux_agent.service.pristine
md5sum /etc/systemd/system/flux_agent.service $R/flux_agent.service.pristine
echo "--- install fix-C agent (expect md5 a03d72814b2b32d8d25c12351330ebc7)"
systemctl stop flux_agent; sleep 3
cp $R/flux_agent-r17c /etc/flux_agent/flux_agent; chmod +x /etc/flux_agent/flux_agent
md5sum /etc/flux_agent/flux_agent
echo "--- install new unit (rendered from fix-r17-c install.sh)"
cp $R/flux_agent-prod.service /etc/systemd/system/flux_agent.service
systemctl daemon-reload
cat /etc/systemd/system/flux_agent.service
echo "--- start fix-C agent on pristine gost"
rm -f /etc/flux_agent/agent-startup.log
systemctl reset-failed flux_agent; systemctl start flux_agent; sleep 5
systemctl is-active flux_agent
md5sum /etc/flux_agent/gost.json
echo "--- journal after clean start:"
journalctl -u flux_agent --no-pager -n 15 --since "5 minutes ago" || true
echo "--- poison gost.json (expect md5 e381299b2aa931d2f7c097661ead6702, 378 lines)"
echo "NOTE: the agent MUST be stopped first — 'systemctl start' on an"
echo "already-active unit is a no-op and never loads the file; a running"
echo "agent also re-persists its clean in-memory config over the file."
systemctl stop flux_agent; sleep 3
pgrep -af '/etc/flux_agent/flux_agent' && echo "AGENT STILL RUNNING — abort" && exit 1 || echo "agent stopped"
cp /root/r17c/gost.json-poisoned /etc/flux_agent/gost.json
md5sum /etc/flux_agent/gost.json; wc -l /etc/flux_agent/gost.json
echo "--- restart agent on poisoned file"
systemctl reset-failed flux_agent; systemctl start flux_agent
sleep 25
systemctl is-active flux_agent || echo "agent NOT active (expected)"
echo "NRestarts=$(systemctl show flux_agent -p NRestarts --value)"
pgrep -af '/etc/flux_agent/flux_agent' || echo "no agent process (expected)"
echo "--- journal failure lines:"
journalctl -u flux_agent --no-pager -n 40 --since "2 minutes ago" | grep -iE "fatal|failed|duplicate|address already|parse service|register service" || journalctl -u flux_agent --no-pager -n 40 --since "2 minutes ago"
echo "--- agent-startup.log:"
cat /etc/flux_agent/agent-startup.log
echo "--- restore pristine gost + start"
systemctl stop flux_agent 2>/dev/null || true; sleep 2
cp $R/gost.json.pristine /etc/flux_agent/gost.json
md5sum /etc/flux_agent/gost.json
systemctl reset-failed flux_agent; systemctl start flux_agent; sleep 5
systemctl is-active flux_agent
ps -o pid,cmd -C flux_agent
echo "=== log verify done $(date -u +%FT%TZ) ==="
