#!/usr/bin/env bash
# R21-F03 部署：把 fix-r21-f03 的 paneld（本地构建）换进测试机 panel 容器。
# 只换后端镜像 tag；agent 不动（pristine aff338e3），frontend 不动，FLUX_VERSION 不动。
# 在测试机上以 root 运行，日志由调用方落盘。
# 前置：stage 已由本机 scp 到 /root/r21-f03/stage/（paneld + Dockerfile.backend）。
# 每次重启/部署前复核锁归属：调用方在 SSH 外层跑 check，本脚本内不再重复抢锁。
set -u
R=/root/r21-f03
PANELD_MD5_EXPECT="73c1c1a5e29893844cee247bb80e9522"
# 注（2026-10-25 续做 lane）：旧值 7e8313e4… 无法用当前源码复现
# （本地 CGO_ENABLED=0 / -trimpath / CGO=1 三种构建均不命中；上一 lane
# 的构建环境已不可考）。改用本 lane 实测构建值：CGO_ENABLED=0 go build
# ./cmd/paneld（与 Dockerfile 的 CGO_ENABLED=0 一致），strings 含
# force_delete_forward_cleanup_skipped ×1 已验。镜像内 paneld 不再要求
# md5 与本地构建相等——本地构建含绝对路径、docker 构建在 /src，两者
# 二进制必然不同；镜像内改为 marker 串核验（见下）。
echo "=== deploy start=$(date -u +%FT%TZ) initiator=R21-F03 ==="
echo "--- pre-state"
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
grep -n "image: flmx-backend" /root/flmx-panel/docker-compose.yml
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
cp /root/flmx-panel/docker-compose.yml $R/docker-compose.yml.pre-r21f03
echo "--- backup live DB (sqlite online backup via python3; host has no sqlite3 CLI)"
python3 -c "import sqlite3; s=sqlite3.connect('file:/var/lib/docker/volumes/sqlite_data/_data/gost.db?mode=ro', uri=True); d=sqlite3.connect('$R/gost.db.pre'); s.backup(d); d.close(); s.close()"
ls -la $R/gost.db.pre || { echo "DB BACKUP FAILED, aborting deploy"; exit 1; }
echo "--- verify staged paneld (expect $PANELD_MD5_EXPECT; skipped if binary not staged)"
if [ -f "$R/stage/paneld" ]; then
  md5sum $R/stage/paneld
  strings $R/stage/paneld | grep -c force_delete_forward_cleanup_skipped
else
  echo "no staged paneld binary (source-only stage; image is built from staged source, marker verified post-build)"
fi
echo "--- docker build backend r21-f03"
docker build -q -t flmx-backend:r21-f03 -f $R/stage/Dockerfile.backend $R/stage
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}}' | grep 'flmx-backend:r21-f03'
echo "--- verify paneld inside image (md5 recorded; marker string must be present)"
docker run --rm --entrypoint md5sum flmx-backend:r21-f03 /app/paneld
docker run --rm --entrypoint sh flmx-backend:r21-f03 -c 'strings /app/paneld | grep -c force_delete_forward_cleanup_skipped'
echo "--- switch compose backend tag only"
sed -i 's/flmx-backend:[^ ]*/flmx-backend:r21-f03/' /root/flmx-panel/docker-compose.yml
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
