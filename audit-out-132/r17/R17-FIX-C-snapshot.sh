#!/bin/bash
# R17-FIX-C counts snapshot via the stored R17 token (api17.sh method).
# Prints only counts; never prints credentials or tokens.
# NOTE: on the 1.3.5 panel the */list endpoints are POST (GET returns
# code -1 "请求失败"); the candidate panel also accepts POST, so POST is
# used for both.
# Usage: bash R17-FIX-C-snapshot.sh <label>
LABEL="${1:-snapshot}"
echo "=== counts $LABEL $(date -u +%FT%TZ) ==="
cd /root/r17 || exit 1
for p in node/list tunnel/list forward/list inbound/list subscription/list; do
  out="/tmp/snap-$(echo "$p" | tr '/' '-').json"
  bash api17.sh POST "/api/v1/$p" "{}" "$out" >/dev/null 2>&1
done
python3 - <<'PYEOF'
import json
names = {"node": "node", "tunnel": "tunnel", "forward": "forward",
         "inbound": "inbound", "subscription": "subscription"}
for key, label in names.items():
    path = "/tmp/snap-%s-list.json" % key
    try:
        env = json.load(open(path))
    except Exception as e:
        print(label, "UNREADABLE", e)
        continue
    if env.get("code") != 0:
        print(label, "API code", env.get("code"), str(env.get("msg"))[:80])
        continue
    data = env.get("data")
    if isinstance(data, list):
        n = len(data)
    elif isinstance(data, dict):
        lst = data.get("list") or data.get("items")
        n = len(lst) if isinstance(lst, list) else data.get("total", "?")
    else:
        n = "?"
    print(label, n)
PYEOF
echo "agent: $(systemctl is-active flux_agent 2>/dev/null) gost=$(md5sum /etc/flux_agent/gost.json | cut -d' ' -f1) lines=$(wc -l < /etc/flux_agent/gost.json)"
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
