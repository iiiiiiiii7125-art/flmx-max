CONNECT status: HTTP/1.1 200 Connection Established
#!/bin/bash
# R17-FIX-D deploy: fix-r17 integrated tree (A dedupe+self-heal, B guard,
# C log-verify) onto the Lightsail test machine.
# Adapted from R17-FIX-C-deploy.sh (tag r17c-base -> r17d, workdir /root/r17d).
# Run on the test machine as root; log to /root/r17d/R17-FIX-D-deploy.log
# (caller tees ssh output to the local audit log of the same name).
# Order: stop agent -> backup pristine agent -> install fixed agent +
# new journal unit -> unpack stage -> docker build backend/frontend ->
# switch compose tags -> compose up -> reset-failed + start agent -> served.
set -u
R=/root/r17d
echo "=== R17-FIX-D deploy $(date -u +%FT%TZ) ==="
echo "--- pre-state"
df -h / | tail -1
grep FLUX_VERSION /root/flmx-panel/.env
systemctl is-active flux_agent
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
wc -l /etc/flux_agent/gost.json
echo "--- stop agent"
systemctl stop flux_agent; sleep 3
pgrep -af '/etc/flux_agent/flux_agent' || echo "agent processes gone"
echo "--- backup pristine agent (expect aff338e3676bc56d0f3986a1a62f8fdd)"
cp /etc/flux_agent/flux_agent $R/flux_agent.pristine
md5sum /etc/flux_agent/flux_agent $R/flux_agent.pristine
echo "--- install fixed agent (expect md5 23aa260d8bffb857171f26e3426b7d2d)"
cp $R/flux_agent-r17d /etc/flux_agent/flux_agent; chmod +x /etc/flux_agent/flux_agent
md5sum /etc/flux_agent/flux_agent
echo "--- backup pristine unit + install journal unit (expect 05449a73f3d21b037eb082be0a5ff438)"
cp /etc/systemd/system/flux_agent.service $R/flux_agent.service.pristine
md5sum /etc/systemd/system/flux_agent.service $R/flux_agent.service.pristine
cp /root/r17c/flux_agent-prod.service /etc/systemd/system/flux_agent.service
systemctl daemon-reload
md5sum /etc/systemd/system/flux_agent.service
grep -E "StandardOutput|StandardError" /etc/systemd/system/flux_agent.service
echo "--- pristine gost backup (expect d48f09e132ebe99fe97a619dd72d42d4)"
cp /etc/flux_agent/gost.json $R/gost.json.pristine
md5sum /etc/flux_agent/gost.json $R/gost.json.pristine
echo "--- unpack stage (expect paneld md5 1eb46e4b40c994c0b1819d154a0b2e04)"
rm -rf $R/stage; mkdir -p $R/stage
tar xzf $R/stage.tgz -C $R/stage
chmod -R a+rX $R/stage/dist $R/stage/nginx.conf
ls -l $R/stage; md5sum $R/stage/paneld $R/stage/nginx.conf
echo "--- docker build backend"
docker build -q -t flmx-backend:r17d -f $R/stage/Dockerfile.backend $R/stage
echo "--- docker build frontend"
docker build -q -t flmx-frontend:r17d -f $R/stage/Dockerfile.frontend $R/stage
docker images --format "{{.Repository}}:{{.Tag}} {{.ID}}" | grep -E "flmx-(backend|frontend):r17d"
echo "--- backup compose/env"
cp /root/flmx-panel/docker-compose.yml $R/docker-compose.yml.pristine
cp /root/flmx-panel/.env $R/env.pristine
md5sum /root/flmx-panel/docker-compose.yml $R/docker-compose.yml.pristine /root/flmx-panel/.env $R/env.pristine
echo "--- switch tags"
sed -i "s/flmx-backend:r1gate-1.3.5/flmx-backend:r17d/; s/flmx-frontend:r1gate-1.3.5/flmx-frontend:r17d/" /root/flmx-panel/docker-compose.yml
sed -i "s/^FLUX_VERSION=1.3.5/FLUX_VERSION=1.4.8-r17d/" /root/flmx-panel/.env
grep -E "image: flmx|FLUX_VERSION" /root/flmx-panel/docker-compose.yml /root/flmx-panel/.env
echo "--- compose up"
cd /root/flmx-panel && docker compose up -d backend frontend 2>&1 | tail -5
sleep 8
docker ps --format "{{.Names}} {{.Image}} {{.Status}}"
echo "--- reset-failed + start agent"
rm -f /etc/flux_agent/agent-startup.log
systemctl reset-failed flux_agent; systemctl start flux_agent; sleep 5
systemctl is-active flux_agent
ps -o pid,cmd -C flux_agent 2>/dev/null || ps aux | grep "[/]etc/flux_agent/flux_agent" | head -3
echo "--- served check"
curl -s -o /dev/null -w "GET / http=%{http_code}\n" http://127.0.0.1:6366/
md5sum /etc/flux_agent/gost.json
echo "--- counts after deploy"
bash /root/r17c/R17-FIX-C-snapshot.sh r17d-post-deploy
echo "=== deploy done $(date -u +%FT%TZ) ==="
