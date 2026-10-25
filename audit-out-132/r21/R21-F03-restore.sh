#!/usr/bin/env bash
# R21-F03 恢复：测试机回部署前状态（compose 文件原样恢复 + agent 不动 + DB 按需恢复）。
# 在测试机上以 root 运行：bash /root/r21-f03/restore.sh 2>&1 | tee /root/r21-f03/restore.log
# 注意：compose 恢复用的是本 lane 部署前备份的 docker-compose.yml.pre-r21f03
# （不硬编码 r1gate-1.3.5——前一 lane 的部署状态由该备份原样保留）。
set -u
R=/root/r21-f03
echo "=== restore start=$(date -u +%FT%TZ) initiator=R21-F03 ==="
echo "--- pre-state"
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
grep -n "image: flmx-backend" /root/flmx-panel/docker-compose.yml
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
echo "--- restore compose from pre-r21f03 backup"
cp $R/docker-compose.yml.pre-r21f03 /root/flmx-panel/docker-compose.yml
grep -n "image: flmx-backend" /root/flmx-panel/docker-compose.yml
echo "--- compose up backend (restored tag)"
cd /root/flmx-panel && docker compose up -d backend 2>&1 | tail -3
for i in $(seq 1 30); do
  st=$(docker inspect --format '{{.State.Health.Status}}' flux-panel-backend 2>/dev/null || echo unknown)
  echo "try $i backend health=$st"
  [ "$st" = healthy ] && break
  sleep 5
done
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
docker exec flux-panel-backend md5sum /app/paneld
curl -s -o /dev/null -w "GET / 6366 http=%{http_code}\n" http://127.0.0.1:6366/
echo "--- agent untouched (no restart by this lane; verify only)"
systemctl is-active flux_agent
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
python3 -c "import json; d=json.load(open('/etc/flux_agent/gost.json')); print('services=', len(d.get('services') or []), 'climiters=', sorted(c['name'] for c in (d.get('climiters') or []) if c))"
echo "--- remove r21-f03 image"
docker rmi flmx-backend:r21-f03 2>&1 | tail -2
docker images --format '{{.Repository}}:{{.Tag}}' | grep 'r21-f03' || echo "no r21-f03 images left"
echo "=== restore done=$(date -u +%FT%TZ) ==="
