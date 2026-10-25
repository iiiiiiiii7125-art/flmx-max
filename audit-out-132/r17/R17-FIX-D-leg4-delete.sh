#!/bin/bash
# R17-FIX-D leg4 phase 4: D4 cleanup — delete the 5 test inbounds, verify
# policy null / flmx-idle-* [] / idle rules 0 / inbound count back to 578,
# then kill the scaffold (dummies + client/min xrays) and verify ports closed.
# The r17* ids are read from /tmp/inb-post.json (written by phase 1).
# Log to stdout (caller tees).
set -u
R=/root/r17d
S=/root/r17
echo "=== leg4 delete start=$(date -u +%FT%TZ) ==="
python3 - <<'PYEOF' > /tmp/r17d-del-ids.txt
import json
d = json.load(open("/tmp/inb-post.json"))["data"]
for i in d:
    if i.get("name", "").startswith("r17"):
        print(i["id"], i["name"])
PYEOF
cat /tmp/r17d-del-ids.txt
while read -r id name; do
  echo "--- delete $id $name"
  bash $S/api17.sh POST /api/v1/inbound/delete "{\"id\":$id}" /tmp/del-$id.json
  cat /tmp/del-$id.json; echo
  sleep 2
done < /tmp/r17d-del-ids.txt
sleep 10
echo "--- post delete: inbound count + r17 residue"
bash $S/api17.sh POST /api/v1/inbound/list '{}' /tmp/inb-final.json >/dev/null 2>&1
python3 -c "
import json
d=json.load(open('/tmp/inb-final.json'))['data']
print('total inbound count', len(d))
print('r17 residue', [i['name'] for i in d if i.get('name','').startswith('r17')])"
echo "--- post delete: agent xray config"
cp /etc/flux_agent/kernels/xray/run/config.json $R/xray-config-after-delete.json
python3 - <<'PYEOF'
import json
c = json.load(open("/etc/flux_agent/kernels/xray/run/config.json"))
print("policy:", json.dumps(c.get("policy")))
print("flmx-idle outbounds:", [o["tag"] for o in c.get("outbounds", []) if o.get("tag", "").startswith("flmx-idle")])
print("idle rules:", [r for r in c.get("routing", {}).get("rules", []) if "flmx-idle" in str(r.get("outboundTag", ""))])
print("outbounds:", [o.get("tag") for o in c.get("outbounds", [])])
print("rules count:", len(c.get("routing", {}).get("rules", [])), "inbounds:", len(c.get("inbounds", [])))
PYEOF
echo "--- scaffold cleanup"
pkill -f "dummy.py 59909" 2>/dev/null || true
pkill -f "xray run -c xclient/" 2>/dev/null || true
pkill -f "xray run -c minxray/" 2>/dev/null || true
sleep 3
echo "scaffold processes left:"
ps -eo pid,cmd | grep -E "dummy.py 59909|xray run -c (xclient|minxray)/" | grep -v grep || echo "(none)"
echo "test ports left:"
ss -tlnp 2>/dev/null | grep -E "59909|1108[0-9]|5991[23]" || echo "(none)"
echo "agent own xray:"
ps -eo pid,cmd | grep "[/]etc/flux_agent/kernels/xray/run/config.json" | grep -v grep
echo "=== leg4 delete end=$(date -u +%FT%TZ) ==="
