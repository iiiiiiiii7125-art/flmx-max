#!/usr/bin/env bash
# R21-F02 部署：把 fix-r21-f02 分支构建的 paneld 换进测试机 panel 容器。
# 仅换 backend 镜像（COPY-only，FROM GHCR 1.4.8 底座）；frontend 不动，
# agent 二进制不动，FLUX_VERSION 不动，.env 不动。
# 在测试机上以 root 运行，日志由调用方落盘。
set -u
R=/root/r21-f02
PANELD_MD5_EXPECT="00af1291b498fb8b2b8d36f41604810e"
echo "=== deploy start=$(date -u +%FT%TZ) initiator=R21-F02 ==="
echo "--- pre-state"
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
grep -n "image: flmx-backend" /root/flmx-panel/docker-compose.yml
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
echo "--- verify staged paneld (expect $PANELD_MD5_EXPECT)"
md5sum $R/stage/paneld
echo "--- docker build backend r21-f02"
docker build -q -t flmx-backend:r21-f02 -f $R/stage/Dockerfile.backend $R/stage
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}}' | grep 'flmx-backend:r21-f02'
echo "--- verify paneld inside image (expect $PANELD_MD5_EXPECT)"
docker run --rm --entrypoint md5sum flmx-backend:r21-f02 /app/paneld
echo "--- switch compose backend tag only"
sed -i -E 's|image: flmx-backend:[^ ]+|image: flmx-backend:r21-f02|' /root/flmx-panel/docker-compose.yml
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
