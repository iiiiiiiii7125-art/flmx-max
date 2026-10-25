#!/bin/bash
# R17-FIX-D final restore to pristine (adapted from C route's restore order).
#   stop agent -> restore pristine agent binary + original unit ->
#   restore compose/.env -> compose up 1.3.5 -> reset-failed + start agent
#   -> gost.json check -> counts + tables + disk reconcile -> remove r17d
#   candidate images. Run on the test machine as root.
# Pristine backups in /root/r17d (verified at deploy):
#   flux_agent.pristine aff338e3676bc56d0f3986a1a62f8fdd
#   unit pristine a12afd9c36fba7b76287210efaa199cb (note: C route found the
#   unit backup can be overwritten by a later install; verify md5 after cp
#   and rebuild from the known original content if it mismatches)
#   gost.json.pristine d48f09e132ebe99fe97a619dd72d42d4 (328 lines)
#   docker-compose.yml.pristine / env.pristine (1.3.5)
set -u
R=/root/r17d
echo "=== R17-FIX-D restore pristine $(date -u +%FT%TZ) ==="
echo "--- counts before restore (r17d panel):"
bash /root/r17/api17.sh POST /api/v1/node/list '{}' /tmp/rst-node.json >/dev/null 2>&1; python3 -c "import json; d=json.load(open('/tmp/rst-node.json')); print('node list code', d.get('code'))"
bash /root/r17/api17.sh POST /api/v1/inbound/list '{}' /tmp/rst-inb.json >/dev/null 2>&1; python3 -c "import json; d=json.load(open('/tmp/rst-inb.json')); print('inbound count', len(d.get('data',[])))"
md5sum /etc/flux_agent/gost.json; wc -l /etc/flux_agent/gost.json
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
grep -E "StandardOutput|StandardError" /etc/systemd/system/flux_agent.service || echo "(no StandardOutput/StandardError lines: original unit)"
echo "--- restore gost.json if it drifted (expect d48f09e132ebe99fe97a619dd72d42d4)"
if [ "$(md5sum /etc/flux_agent/gost.json | cut -d' ' -f1)" != "d48f09e132ebe99fe97a619dd72d42d4" ]; then
  echo "gost drifted, restoring pristine"
  cp $R/gost.json.pristine /etc/flux_agent/gost.json
fi
md5sum /etc/flux_agent/gost.json; wc -l /etc/flux_agent/gost.json
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
echo "--- remove candidate images"
docker rmi flmx-backend:r17d flmx-frontend:r17d 2>&1 | tail -4
docker images --format "{{.Repository}}:{{.Tag}}" | grep -E "flmx|r17d" || echo "no r17d images remain"
echo "--- counts after restore (1.3.5 panel):"
bash /root/r17/api17.sh POST /api/v1/node/list '{}' /tmp/rst2-node.json >/dev/null 2>&1; python3 -c "import json; d=json.load(open('/tmp/rst2-node.json')); print('node list code', d.get('code'), 'nodes', len(d.get('data',[])) if isinstance(d.get('data'),list) else '?')"
bash /root/r17/api17.sh POST /api/v1/tunnel/list '{}' /tmp/rst2-tun.json >/dev/null 2>&1; python3 -c "import json; d=json.load(open('/tmp/rst2-tun.json')); print('tunnel count', len(d.get('data',[])))"
bash /root/r17/api17.sh POST /api/v1/forward/list '{}' /tmp/rst2-fwd.json >/dev/null 2>&1; python3 -c "import json; d=json.load(open('/tmp/rst2-fwd.json')); print('forward count', len(d.get('data',[])), 'statuses', [x.get('status') for x in d.get('data',[])])"
bash /root/r17/api17.sh POST /api/v1/inbound/list '{}' /tmp/rst2-inb.json >/dev/null 2>&1; python3 -c "import json; d=json.load(open('/tmp/rst2-inb.json')); print('inbound count', len(d.get('data',[])))"
bash /root/r17/api17.sh POST /api/v1/subscription/list '{}' /tmp/rst2-sub.json >/dev/null 2>&1; python3 -c "import json; d=json.load(open('/tmp/rst2-sub.json')); print('subscription count', len(d.get('data',[])))"
echo "--- tables (sqlite gost.db, read-only copy):"
mkdir -p /tmp/r17d-db && cp /var/lib/docker/volumes/sqlite_data/_data/gost.db /var/lib/docker/volumes/sqlite_data/_data/gost.db-wal /var/lib/docker/volumes/sqlite_data/_data/gost.db-shm /tmp/r17d-db/ 2>/dev/null
python3 -c "import sqlite3; c=sqlite3.connect(\"file:/tmp/r17d-db/gost.db?mode=ro\", uri=True); print('tables:', c.execute(\"select count(*) from sqlite_master where type='table' and name not like 'sqlite_%'\").fetchone()[0]); print('node_traffic:', c.execute(\"select count(*) from sqlite_master where type='table' and name='node_traffic'\").fetchone()[0])"
rm -f /tmp/r17d-db/gost.db*
df -h / | tail -1
echo "=== restore done $(date -u +%FT%TZ) ==="
