#!/usr/bin/env bash
# R20-FIX-F01 测试机侦察 + pristine 备份（只读，不重启、不部署）。
set -u
echo "=== recon start=$(date -u +%FT%TZ) initiator=fix-r20-f01 ==="
df -h / | tail -1
echo "--- versions / binaries"
cat /root/flmx-panel/.env 2>/dev/null | grep -i FLUX_VERSION || grep -r FLUX_VERSION /root/flmx-panel/.env 2>/dev/null
md5sum /etc/flux_agent/flux_agent /etc/flux_agent/gost.json /etc/systemd/system/flux_agent.service 2>&1
wc -l /etc/flux_agent/gost.json
python3 -c "import json;d=json.load(open('/etc/flux_agent/gost.json'));print('services=%d'%len(d.get('services',[])));print('climiters=%s'%[(c.get('name'),c.get('limits')) for c in d.get('climiters',[])]);print('refs=%s'%sorted({s.get('climiter') for s in d.get('services',[]) if s.get('climiter')}))"
systemctl is-active flux_agent || true
echo "--- compose / images"
grep -n "image:" /root/flmx-panel/docker-compose.yml
docker ps --format '{{.Names}} {{.Image}} {{.Status}}' 2>&1
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}}' 2>&1 | head -20
echo "--- backend build context"
ls /root/flmx-panel/
find /root/flmx-panel -maxdepth 2 -iname '*dockerfile*' 2>/dev/null
grep -rn "build:" -A 4 /root/flmx-panel/docker-compose.yml | head -30
echo "--- L2 api script (create body reference)"
grep -n "forward/create\|forward/update\|forward/delete\|user/login\|6366\|BASE" /root/r20-l2/r20_l2_api.py 2>/dev/null | head -20
sed -n '1,80p' /root/r20-l2/r20_l2_api.py 2>/dev/null | head -80
echo "--- backup pristine"
mkdir -p /root/r20-f01
cp -f /etc/flux_agent/gost.json /root/r20-f01/gost.json.pristine
cp -f /root/flmx-panel/docker-compose.yml /root/r20-f01/docker-compose.yml.pristine
cp -f /root/flmx-panel/.env /root/r20-f01/env.pristine
md5sum /root/r20-f01/gost.json.pristine /root/r20-f01/docker-compose.yml.pristine /root/r20-f01/env.pristine
echo "=== recon done=$(date -u +%FT%TZ) ==="
