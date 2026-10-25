#!/bin/bash
# R17-FIX-D leg4 phase 1: create 5 test inbounds (VLESS/VMess/SS/Trojan idle=5
# + VMess no-idle control) on the r17d panel, then D3 render check against
# the agent's pushed xray config.
# Prereq: harness finished, panel r17d up, agent fixed+active, gost pristine.
# Reuses the exact R17 payloads (same UUIDs/passwords so the existing
# xclient/minxray configs work verbatim). Log to stdout (caller tees).
set -u
R=/root/r17d
S=/root/r17
echo "=== leg4 create start=$(date -u +%FT%TZ) ==="
echo "--- pre: $(bash $S/api17.sh POST /api/v1/inbound/list '{}' /tmp/inb-pre.json >/dev/null 2>&1; python3 -c "import json; d=json.load(open('/tmp/inb-pre.json')); print('inbound count', len(d.get('data',[])) if isinstance(d.get('data'),list) else d.get('code'))")"
cp $R/R17-FIX-D-create-vless.json $S/create-vless.json
for spec in "vless:create-vless.json" "vmess:create-vmess.json" "vmess-ctl:create-vmess-ctl.json" "ss:create-ss.json" "trojan:create-trojan.json"; do
  name="${spec%%:*}"; file="${spec##*:}"
  echo "--- create $name ($file)"
  bash $S/api17.sh POST /api/v1/inbound/create "$(cat $S/$file)" /tmp/create-$name-resp.json
  cat /tmp/create-$name-resp.json; echo
  sleep 2
done
echo "--- post create: inbound list (r17* only)"
bash $S/api17.sh POST /api/v1/inbound/list '{}' /tmp/inb-post.json >/dev/null 2>&1
python3 -c "
import json
d=json.load(open('/tmp/inb-post.json'))['data']
for i in d:
    if i.get('name','').startswith('r17'):
        print(' ', i['id'], i['name'], i['protocol'], i['port'], 'idle=', i.get('connIdleSeconds'))
print('total inbound count', len(d))"
echo "--- wait for agent xray config push (up to 60s)"
for i in $(seq 1 12); do
  sleep 5
  if python3 -c "import json; c=json.load(open('/etc/flux_agent/kernels/xray/run/config.json')); print(any(str(x.get('port'))=='59904' for x in c.get('inbounds',[])))" 2>/dev/null | grep -q True; then
    echo "xray config has 59904 after ${i}x5s"; break
  fi
done
cp /etc/flux_agent/kernels/xray/run/config.json $R/xray-config-created.json
md5sum $R/xray-config-created.json
python3 - <<'PYEOF'
import json
c = json.load(open("/etc/flux_agent/kernels/xray/run/config.json"))
print("--- D3 policy:", json.dumps(c.get("policy")))
print("--- D3 flmx-idle outbounds:")
for o in c.get("outbounds", []):
    if o.get("tag", "").startswith("flmx-idle"):
        print("  ", o["tag"], "userLevel=", o.get("settings", {}).get("userLevel"))
print("--- D3 rules (first 12):")
for i, r in enumerate(c.get("routing", {}).get("rules", [])[:12]):
    print("  ", i, r.get("inboundTag"), "->", r.get("outboundTag"))
print("--- D3 inbounds (5990x):")
for ib in c.get("inbounds", []):
    if str(ib.get("port", "")).startswith("5990"):
        print("  ", ib.get("port"), ib.get("protocol"), "clients=", len(ib.get("settings", {}).get("clients", [])) if isinstance(ib.get("settings"), dict) else "?")
PYEOF
echo "=== leg4 create end=$(date -u +%FT%TZ) ==="
