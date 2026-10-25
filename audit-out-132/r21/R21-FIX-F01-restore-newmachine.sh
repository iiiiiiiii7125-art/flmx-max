#!/usr/bin/env bash
# R21-F01 restore, NEW machine edition: switch the panel backend back to
# the new1 base image (main @ 7d93792, paneld 69d166cc), remove the fix
# image, and verify the machine is back at its pristine baseline:
# counts node 1 / tunnel 0 / forward 0 / inbound 1 / subscription 1,
# gost.json empty (md5 8a80554c), agent 27787cbf active, node online.
# Runs on the test machine as root; log captured by caller.
# Lane R21-F01; run only while holding the testmachine lock.
set -u
R=/root/r21-f01
BASE_MD5_EXPECT="69d166cc2d0923ef72f974912767bfa8"
GOST_MD5_EXPECT="8a80554c91d9fca8acb82f023de02f11"
echo "=== restore start=$(date -u +%FT%TZ) initiator=R21-F01 ==="
echo "--- switch compose backend tag back to new1"
sed -i 's/flmx-backend:r21-f01-v2/flmx-backend:new1/' /root/flmx-panel/docker-compose.yml
grep -n "image: flmx-backend" /root/flmx-panel/docker-compose.yml
echo "--- compose up backend"
cd /root/flmx-panel && docker compose up -d backend 2>&1 | tail -3
echo "--- wait healthy (up to 150s)"
for i in $(seq 1 30); do
  st=$(docker inspect --format '{{.State.Health.Status}}' flux-panel-backend 2>/dev/null || echo unknown)
  echo "try $i health=$st"
  [ "$st" = healthy ] && break
  sleep 5
done
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
echo "--- prove running binary is the new1 base (expect $BASE_MD5_EXPECT)"
docker exec flux-panel-backend md5sum /app/paneld
echo "--- served check"
curl -s -o /dev/null -w "GET / 6366 http=%{http_code}\n" http://127.0.0.1:6366/
echo "--- remove fix image"
docker rmi flmx-backend:r21-f01-v2 2>&1 | tail -2
docker images --format '{{.Repository}}:{{.Tag}}' | grep -E 'flmx-backend' || true
echo "--- remove lane temp files on machine (token; cred file placed for legs)"
rm -f $R/.token /root/panel-cred.txt
ls $R /root/panel-cred.txt 2>&1 | head -5
echo "--- gost + agent state"
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
python3 -c "import json;d=json.load(open('/etc/flux_agent/gost.json'));print('services',len(d.get('services',[])));print('climiters',sorted(c.get('name','') for c in d.get('climiters',[])));print('refs',sorted({s.get('climiter') for s in d.get('services',[]) if s.get('climiter')}))"
systemctl is-active flux_agent
echo "=== restore done=$(date -u +%FT%TZ) ==="
