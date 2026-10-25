#!/usr/bin/env bash
# R25-FIX F-01: deploy fixed backend (tag new7) on the new test machine.
# Run on machine as root: bash /root/r25f01-stage/deploy-r25f01.sh 2>&1 | tee /root/r25f01-stage/deploy-r25f01.log
# Lock R25-F01 must be held; check before running.
# Live compose is /root/flmx-panel/docker-compose.yml ONLY. /root/new1/docker-compose.yml
# (stale copy, still points at new1) must NEVER be used or touched (1.5.0 gate incident).
set -u
S=/root/r25f01-stage
BKP=/root/r25f01-backup
PANELD_MD5_EXPECT="35bfe03dbd9b7f9a143c54d5d837d9f4"
PANELD_MD5_BASE="8867e99fc01ae64930098ae463a013e3"
DIST_INDEX_MD5_EXPECT="57ab941e7fc6025b52700b2bf36f2241"
echo "=== deploy-r25f01 start=$(date -u +%FT%TZ) initiator=R25-F01 ==="
echo "--- lock check (done from sandbox before invoking; lane R25-F01)"
echo "lock owner on sandbox: R25-F01 (acquired 2026-10-25T19:56:53Z)"
echo "--- pre-deploy running state (expect new6 / ef87bc6)"
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
docker exec flux-panel-backend md5sum /app/paneld
curl -s http://127.0.0.1:6366/ | md5sum
echo "--- stage bits"
md5sum $S/paneld
[ "$(md5sum < $S/paneld | cut -d' ' -f1)" = "$PANELD_MD5_EXPECT" ] || { echo "PANELD MD5 MISMATCH, abort"; exit 1; }
echo "--- backup compose + .env"
mkdir -p $BKP
cp /root/flmx-panel/docker-compose.yml $BKP/docker-compose.yml
cp /root/flmx-panel/.env $BKP/.env
grep -E "image:" /root/flmx-panel/docker-compose.yml
echo "--- stop agent"
systemctl stop flux_agent
sleep 2
pgrep -x flux_agent && echo "AGENT STILL RUNNING" || echo "agent gone"
echo "--- docker build backend new7 (COPY-only, FROM ghcr 1.4.8)"
docker build -q -t flmx-backend:new7 -f $S/Dockerfile.backend $S
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}}' | grep 'flmx-backend:new7'
echo "--- verify paneld inside image (expect $PANELD_MD5_EXPECT)"
docker run --rm --entrypoint md5sum flmx-backend:new7 /app/paneld
echo "--- compose backend image to new7 (frontend stays new6)"
sed -i 's/flmx-backend:new6/flmx-backend:new7/' /root/flmx-panel/docker-compose.yml
grep -E "image:" /root/flmx-panel/docker-compose.yml
cd /root/flmx-panel && docker compose up -d backend 2>&1 | tail -5
echo "--- wait healthy (up to 150s)"
for i in $(seq 1 30); do
  st=$(docker inspect --format '{{.State.Health.Status}}' flux-panel-backend 2>/dev/null || echo unknown)
  echo "try $i backend health=$st"
  [ "$st" = healthy ] && break
  sleep 5
done
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
echo "--- prove running binary is the fixed build"
docker exec flux-panel-backend md5sum /app/paneld
echo "--- served check (index must stay $DIST_INDEX_MD5_EXPECT, frontend untouched)"
curl -s -o /dev/null -w "GET / 6366 http=%{http_code}\n" http://127.0.0.1:6366/
curl -s http://127.0.0.1:6366/ | md5sum
curl -s -o /dev/null -w "GET /flow/test 6365 http=%{http_code}\n" http://127.0.0.1:6365/flow/test
echo "--- start agent"
systemctl reset-failed flux_agent; systemctl start flux_agent
sleep 5; systemctl is-active flux_agent
echo "=== deploy-r25f01 done=$(date -u +%FT%TZ) ==="
