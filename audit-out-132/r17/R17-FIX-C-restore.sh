#!/bin/bash
# R17-FIX-C final restore to pristine (R17 reconcile order).
#   stop agent -> restore pristine agent binary + original unit ->
#   restore compose/.env -> compose up 1.3.5 -> reset-failed + start agent
#   -> counts + table + disk reconcile; remove r17c-base images.
# Run on the test machine as root; log to /root/r17c/R17-FIX-C-restore.log.
set -u
R=/root/r17c
echo "=== R17-FIX-C restore pristine $(date -u +%FT%TZ) ==="
echo "--- counts before restore (candidate panel):"
bash $R/R17-FIX-C-snapshot.sh candidate-pre-restore
echo "--- preserve task-1 evidence from the machine:"
cp /etc/flux_agent/agent-startup.log $R/agent-startup.log 2>/dev/null && echo "agent-startup.log saved ($(wc -l < $R/agent-startup.log) lines)"
journalctl -u flux_agent --no-pager --since "2026-10-24 23:55:30" --until "2026-10-24 23:56:05" > $R/journal-poison-window.log 2>/dev/null
grep -c "parse service" $R/journal-poison-window.log | xargs echo "journal parse-service lines:"
echo "--- stop agent"
systemctl stop flux_agent; sleep 3
pgrep -af '/etc/flux_agent/flux_agent' || echo "agent processes gone"
echo "--- restore pristine agent (expect aff338e3676bc56d0f3986a1a62f8fdd)"
cp $R/flux_agent.pristine /etc/flux_agent/flux_agent; chmod +x /etc/flux_agent/flux_agent
md5sum /etc/flux_agent/flux_agent
echo "--- restore original unit (expect a12afd9c36fba7b76287210efaa199cb)"
cp $R/flux_agent.service.pristine /etc/systemd/system/flux_agent.service
systemctl daemon-reload
md5sum /etc/systemd/system/flux_agent.service
grep -E "StandardOutput|StandardError" /etc/systemd/system/flux_agent.service
echo "--- restore compose/env"
cp $R/docker-compose.yml.pristine /root/flmx-panel/docker-compose.yml
cp $R/env.pristine /root/flmx-panel/.env
md5sum /root/flmx-panel/docker-compose.yml $R/docker-compose.yml.pristine /root/flmx-panel/.env $R/env.pristine
grep -E "image: flmx|FLUX_VERSION" /root/flmx-panel/docker-compose.yml /root/flmx-panel/.env
echo "--- compose up 1.3.5"
cd /root/flmx-panel && docker compose up -d backend frontend 2>&1 | tail -5
sleep 8
docker ps --format "{{.Names}} {{.Image}} {{.Status}}"
echo "--- reset-failed + start agent"
systemctl reset-failed flux_agent; systemctl start flux_agent; sleep 5
systemctl is-active flux_agent
ps -o pid,cmd -C flux_agent
md5sum /etc/flux_agent/gost.json; wc -l /etc/flux_agent/gost.json
echo "--- served check"
curl -s -o /dev/null -w "GET / http=%{http_code}\n" http://127.0.0.1:6366/
echo "--- remove candidate images + startup-log file (evidence already copied)"
docker rmi flmx-backend:r17c-base flmx-frontend:r17c-base 2>&1 | tail -4
rm -f /etc/flux_agent/agent-startup.log /tmp/r17c-db/gost.db* 2>/dev/null; rmdir /tmp/r17c-db 2>/dev/null || true
docker images --format "{{.Repository}}:{{.Tag}}" | grep -E "flmx|r17c" || echo "no r17c images remain"
echo "--- counts after restore (1.3.5 panel):"
bash $R/R17-FIX-C-snapshot.sh pristine-post
echo "--- tables (sqlite gost.db, read-only copy):"
mkdir -p /tmp/r17c-db && cp /var/lib/docker/volumes/sqlite_data/_data/gost.db /var/lib/docker/volumes/sqlite_data/_data/gost.db-wal /var/lib/docker/volumes/sqlite_data/_data/gost.db-shm /tmp/r17c-db/ 2>/dev/null
python3 -c "import sqlite3; c=sqlite3.connect(\"file:/tmp/r17c-db/gost.db?mode=ro\", uri=True); print('tables:', c.execute(\"select count(*) from sqlite_master where type='table' and name not like 'sqlite_%'\").fetchone()[0]); print('node_traffic:', c.execute(\"select count(*) from sqlite_master where type='table' and name='node_traffic'\").fetchone()[0])"
rm -f /tmp/r17c-db/gost.db*
df -h / | tail -1
echo "=== restore done $(date -u +%FT%TZ) ==="
