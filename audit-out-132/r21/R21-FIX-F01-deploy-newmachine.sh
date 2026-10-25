#!/usr/bin/env bash
# R21-F01 deploy, NEW machine edition: swap the fix paneld
# (fix-r21-f01 @ 01336d4, 88f10db + fix, md5 3b208eba) into the
# new test-machine panel container (base flmx-backend:new1, main @ 7d93792).
# Agent untouched (27787cbf), frontend untouched, FLUX_VERSION untouched.
# Runs on the test machine as root; log captured by caller.
# Lane R21-F01; run only while holding the testmachine lock.
set -u
R=/root/r21-f01
PANELD_MD5_EXPECT="3b208eba31d132285f5383a4e69fb261"
BASE_MD5_EXPECT="69d166cc2d0923ef72f974912767bfa8"
GOST_MD5_EXPECT="8a80554c91d9fca8acb82f023de02f11"
echo "=== deploy start=$(date -u +%FT%TZ) initiator=R21-F01 ==="
echo "--- pre-state"
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
grep -n "image: flmx-backend" /root/flmx-panel/docker-compose.yml
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
echo "--- verify staged paneld (expect $PANELD_MD5_EXPECT)"
md5sum $R/paneld-r21f01-v2
echo "--- verify running paneld is the new1 base (expect $BASE_MD5_EXPECT)"
docker exec flux-panel-backend md5sum /app/paneld
echo "--- verify gost pristine-empty (expect $GOST_MD5_EXPECT)"
md5sum /etc/flux_agent/gost.json
echo "--- stage Dockerfile"
printf 'FROM flmx-backend:new1\nCOPY paneld-r21f01-v2 /app/paneld\n' > $R/stage.Dockerfile
cat $R/stage.Dockerfile
echo "--- docker build backend r21-f01-v2"
docker build -q -t flmx-backend:r21-f01-v2 -f $R/stage.Dockerfile $R
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}}' | grep 'flmx-backend:r21-f01-v2'
echo "--- verify paneld inside image (expect $PANELD_MD5_EXPECT)"
docker run --rm --entrypoint md5sum flmx-backend:r21-f01-v2 /app/paneld
echo "--- switch compose backend tag only"
sed -i 's/flmx-backend:new1/flmx-backend:r21-f01-v2/' /root/flmx-panel/docker-compose.yml
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
echo "--- prove running binary is the fix build"
docker exec flux-panel-backend md5sum /app/paneld
echo "--- served check"
curl -s -o /dev/null -w "GET / 6366 http=%{http_code}\n" http://127.0.0.1:6366/
md5sum /etc/flux_agent/gost.json
systemctl is-active flux_agent
echo "=== deploy done=$(date -u +%FT%TZ) ==="
