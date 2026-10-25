#!/usr/bin/env bash
# R20-FIX-F01 部署：把修复版 paneld（本地构建，md5 见调用方核对）换进
# 测试机 panel 容器。agent 不动（本次只改 panel 侧），frontend 不动，
# FLUX_VERSION 不动（仍 1.3.5，避免版本漂移；以容器内 paneld md5 + 行为
# 探针证明运行的是修复版）。在测试机上以 root 运行，日志由调用方落盘。
set -u
R=/root/r20-f01
PANELD_MD5_EXPECT="${PANELD_MD5_EXPECT:-7902009e6cdc737792212039cce70259}"
echo "=== deploy start=$(date -u +%FT%TZ) initiator=fix-r20-f01 ==="
echo "--- pre-state"
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
grep -n "image: flmx-backend" /root/flmx-panel/docker-compose.yml
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
echo "--- verify staged paneld (expect $PANELD_MD5_EXPECT)"
md5sum $R/stage/paneld
echo "--- docker build backend r20-f01"
docker build -q -t flmx-backend:r20-f01 -f $R/stage/Dockerfile.backend $R/stage
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}}' | grep 'flmx-backend:r20-f01'
echo "--- verify paneld inside image (expect $PANELD_MD5_EXPECT)"
docker run --rm --entrypoint md5sum flmx-backend:r20-f01 /app/paneld
echo "--- switch compose backend tag only"
sed -i 's/flmx-backend:r1gate-1.3.5/flmx-backend:r20-f01/' /root/flmx-panel/docker-compose.yml
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
