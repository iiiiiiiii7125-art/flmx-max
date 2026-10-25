#!/usr/bin/env bash
# R21-F01 deploy: swap the fix paneld (fix-r21-f01 @ de45573f) into the
# test-machine panel container. Agent untouched (pristine aff338e3),
# frontend untouched, FLUX_VERSION untouched.
# Runs on the test machine as root; log captured by caller.
set -u
R=/root/r21-f01
PANELD_MD5_EXPECT="de45573f95f1cb9254bb74999ef3cc8d"
echo "=== deploy start=$(date -u +%FT%TZ) initiator=R21-F01 ==="
echo "--- pre-state"
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
grep -n "image: flmx-backend" /root/flmx-panel/docker-compose.yml
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
echo "--- verify staged paneld (expect $PANELD_MD5_EXPECT)"
md5sum $R/paneld-r21f01
echo "--- stage Dockerfile"
printf 'FROM flmx-backend:r1gate-1.3.5\nCOPY paneld-r21f01 /app/paneld\n' > $R/stage.Dockerfile
cat $R/stage.Dockerfile
echo "--- docker build backend r21-f01"
docker build -q -t flmx-backend:r21-f01 -f $R/stage.Dockerfile $R
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}}' | grep 'flmx-backend:r21-f01'
echo "--- verify paneld inside image (expect $PANELD_MD5_EXPECT)"
docker run --rm --entrypoint md5sum flmx-backend:r21-f01 /app/paneld
echo "--- switch compose backend tag only"
sed -i 's/flmx-backend:r1gate-1.3.5/flmx-backend:r21-f01/' /root/flmx-panel/docker-compose.yml
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
