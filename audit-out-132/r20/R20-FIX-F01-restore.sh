#!/usr/bin/env bash
# R20-FIX-F01 还原：恢复 pristine agent 单元 + pristine panel 镜像，
# 删自建镜像，全量对账。agent 二进制本次未动，无需恢复。
# 在测试机上以 root 运行，日志由调用方落盘。
set -u
R=/root/r20-f01
echo "=== restore start=$(date -u +%FT%TZ) initiator=fix-r20-f01 ==="
echo "--- restore pristine agent unit (expect a12afd9c36fba7b76287210efaa199cb)"
cp $R/flux_agent.service.pristine /etc/systemd/system/flux_agent.service
systemctl daemon-reload
md5sum /etc/systemd/system/flux_agent.service
grep -E "StandardOutput|StandardError" /etc/systemd/system/flux_agent.service
echo "--- restart agent (reset-failed first)"
systemctl stop flux_agent; sleep 3
systemctl reset-failed flux_agent; systemctl start flux_agent; sleep 5
systemctl is-active flux_agent
echo "--- restore compose backend tag (expect flmx-backend:r1gate-1.3.5)"
sed -i 's/flmx-backend:r20-f01/flmx-backend:r1gate-1.3.5/' /root/flmx-panel/docker-compose.yml
grep -n "image: flmx-backend" /root/flmx-panel/docker-compose.yml
grep -n "image: flmx-frontend" /root/flmx-panel/docker-compose.yml
cd /root/flmx-panel && docker compose up -d backend 2>&1 | tail -3
echo "--- wait backend healthy (up to 150s)"
for i in $(seq 1 30); do
  st=$(docker inspect --format '{{.State.Health.Status}}' flux-panel-backend 2>/dev/null || echo unknown)
  echo "try $i health=$st"
  [ "$st" = healthy ] && break
  sleep 5
done
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
echo "--- verify restored panel binary + gost + agent"
docker exec flux-panel-backend md5sum /app/paneld
md5sum /etc/flux_agent/flux_agent /etc/flux_agent/gost.json
wc -l /etc/flux_agent/gost.json
python3 -c "import json;d=json.load(open('/etc/flux_agent/gost.json'));print('services=%d climiters=%s refs=%s'%(len(d.get('services',[])),sorted(c.get('name') for c in d.get('climiters',[])),sorted({s.get('climiter') for s in d.get('services',[]) if s.get('climiter')})))"
echo "--- remove fix image"
docker rmi flmx-backend:r20-f01 2>&1 | tail -3
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}}' | grep -E 'flmx-backend' || true
echo "--- served check"
curl -s -o /dev/null -w "GET / 6366 http=%{http_code}\n" http://127.0.0.1:6366/
df -h / | tail -1
echo "=== restore done=$(date -u +%FT%TZ) ==="
