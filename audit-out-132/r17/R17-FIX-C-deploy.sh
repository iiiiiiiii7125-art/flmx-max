#!/bin/bash
# R17-FIX-C baseline deploy: main@5473377 bits (R17 stage.tgz + agent).
# Replicates the R17-L4-deploy.log order exactly:
#   stop agent -> backup pristine agent -> install candidate agent ->
#   unpack stage -> docker build backend/frontend -> switch compose tags
#   -> compose up -> reset-failed + start agent -> served check.
# Tags use the r17c-base suffix (not r17) so these images are
# distinguishable from the R17 run's leftovers.
# Run on the test machine as root; log to /root/r17c/R17-FIX-C-deploy.log.
set -u
R=/root/r17c
echo "=== R17-FIX-C baseline deploy $(date -u +%FT%TZ) ==="
echo "--- pre-state"
systemctl is-active flux_agent
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
wc -l /etc/flux_agent/gost.json
echo "--- stop agent"
systemctl stop flux_agent; sleep 3
pgrep -af '/etc/flux_agent/flux_agent' || echo "agent processes gone"
echo "--- backup pristine agent"
cp /etc/flux_agent/flux_agent $R/flux_agent.pristine
md5sum /etc/flux_agent/flux_agent $R/flux_agent.pristine
echo "--- install candidate agent (R17 bits, expect md5 b92b61993dbfd6134db048e7f256552b)"
cp $R/flux_agent /etc/flux_agent/flux_agent; chmod +x /etc/flux_agent/flux_agent
md5sum /etc/flux_agent/flux_agent
echo "--- pristine gost backup"
cp /etc/flux_agent/gost.json $R/gost.json.pristine
md5sum /etc/flux_agent/gost.json $R/gost.json.pristine
echo "--- unpack stage"
rm -rf $R/stage; mkdir -p $R/stage
tar xzf $R/stage.tgz -C $R/stage
chmod -R a+rX $R/stage/dist $R/stage/nginx.conf
ls -l $R/stage; md5sum $R/stage/paneld
echo "--- docker build backend"
docker build -q -t flmx-backend:r17c-base -f $R/stage/Dockerfile.backend $R/stage
echo "--- docker build frontend"
docker build -q -t flmx-frontend:r17c-base -f $R/stage/Dockerfile.frontend $R/stage
docker images --format "{{.Repository}}:{{.Tag}} {{.ID}}" | grep -E "flmx-(backend|frontend):r17c-base"
echo "--- backup compose/env"
cp /root/flmx-panel/docker-compose.yml $R/docker-compose.yml.pristine
cp /root/flmx-panel/.env $R/env.pristine
md5sum /root/flmx-panel/docker-compose.yml $R/docker-compose.yml.pristine /root/flmx-panel/.env $R/env.pristine
echo "--- switch tags"
sed -i "s/flmx-backend:r1gate-1.3.5/flmx-backend:r17c-base/; s/flmx-frontend:r1gate-1.3.5/flmx-frontend:r17c-base/" /root/flmx-panel/docker-compose.yml
sed -i "s/^FLUX_VERSION=1.3.5/FLUX_VERSION=1.4.8-r17c-base/" /root/flmx-panel/.env
grep -E "image: flmx|FLUX_VERSION" /root/flmx-panel/docker-compose.yml /root/flmx-panel/.env
echo "--- compose up"
cd /root/flmx-panel && docker compose up -d backend frontend 2>&1 | tail -5
sleep 8
docker ps --format "{{.Names}} {{.Image}} {{.Status}}"
echo "--- reset-failed + start agent"
systemctl reset-failed flux_agent; systemctl start flux_agent; sleep 5
systemctl is-active flux_agent
ps -o pid,cmd -C flux_agent 2>/dev/null || ps aux | grep "[/]etc/flux_agent/flux_agent" | head -3
echo "--- served check"
curl -s -o /dev/null -w "GET / http=%{http_code}\n" http://127.0.0.1:6366/
md5sum /etc/flux_agent/gost.json
echo "=== deploy done $(date -u +%FT%TZ) ==="
