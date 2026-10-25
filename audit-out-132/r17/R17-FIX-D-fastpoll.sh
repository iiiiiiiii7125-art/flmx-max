#!/bin/bash
# R17-FIX-D fast poller: restart the agent and sample gost.json every 50ms
# for 40s, because a duplicate written by a fallback AddService may live on
# disk only until the next UpdateService persist (~50ms later).
# Usage: bash R17-FIX-D-fastpoll.sh <label>
# Any dup sighting saves the full file to /root/r17d/fastdup-<label>-<n>.json.
set -u
LABEL="${1:-fast}"
R=/root/r17d
BACKEND_LOG=$(ls /var/lib/docker/containers/$(docker inspect -f '{{.Id}}' flux-panel-backend)/*-json.log)
GUARD_BEFORE=$(grep -c "r17-addservice-guard" "$BACKEND_LOG" 2>/dev/null || echo 0)
echo "=== fastpoll $LABEL start=$(date -u +%FT%TZ) guard_before=$GUARD_BEFORE ==="
python3 - "$R" "$LABEL" <<'PYEOF'
import json, subprocess, sys, time
from collections import Counter
R, LABEL = sys.argv[1], sys.argv[2]
def snap():
    try:
        cfg = json.load(open("/etc/flux_agent/gost.json"))
    except Exception as e:
        return ("UNREADABLE " + str(e), None)
    names = [s.get("name", "") for s in cfg.get("services", []) if isinstance(s, dict)]
    dups = {n: c for n, c in Counter(names).items() if c > 1}
    return ("services=%d dup=%s" % (len(names), dups if dups else "none"), dups)
def ts():
    return time.strftime("%H:%M:%S", time.gmtime()) + ".%03d" % (int(time.time() * 1000) % 1000)
print(ts() + " pre: " + snap()[0], flush=True)
subprocess.run(["systemctl", "reset-failed", "flux_agent"], check=False)
subprocess.run(["systemctl", "restart", "flux_agent"], check=False)
time.sleep(3)
print(ts() + " agent restarted, polling 50ms x 40s", flush=True)
hits = 0
last = ""
t0 = time.monotonic()
while time.monotonic() - t0 < 40:
    state, dups = snap()
    if state != last:
        print(ts() + " " + state, flush=True)
        last = state
    if dups:
        hits += 1
        subprocess.run(["cp", "/etc/flux_agent/gost.json", "%s/fastdup-%s-%d.json" % (R, LABEL, hits)], check=False)
        print(ts() + " DUP SAVED fastdup-%s-%d.json" % (LABEL, hits), flush=True)
    time.sleep(0.05)
print(ts() + " post: " + snap()[0] + " dup_sightings=%d" % hits, flush=True)
PYEOF
echo "--- agent: $(systemctl is-active flux_agent)"
echo "--- new guard lines:"
python3 - "$BACKEND_LOG" "$GUARD_BEFORE" <<'PYEOF'
import json, sys
log, before = sys.argv[1], int(sys.argv[2])
lines = [l for l in open(log) if "r17-addservice-guard" in l]
for l in lines[before:]:
    try:
        e = json.loads(l); print(e.get("time"), e["log"].strip())
    except Exception:
        print("UNPARSED", l[:200])
PYEOF
echo "=== fastpoll $LABEL end=$(date -u +%FT%TZ) ==="
