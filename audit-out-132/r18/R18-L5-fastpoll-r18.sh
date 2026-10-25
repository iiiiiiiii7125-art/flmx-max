#!/bin/bash
# R18-L5 fastpoll: restart the (unfixed, main@5473377) agent and sample
# /etc/flux_agent/gost.json every 50ms for 45s, hunting the R17D-OPEN-01
# 49ms window: a duplicate service pair written by a fallback AddService
# that a later UpdateService persist may overwrite ~50ms later.
# Panel is pristine 1.3.5 (no guard log lines); AddService receipts are
# read from the agent journal instead.
# Usage: bash fastpoll-r18.sh <label> ; log to stdout (caller tees).
# Any dup sighting saves the full file to /root/r18/fastdup-<label>-<n>.json.
set -u
LABEL="${1:-fast}"
R=/root/r18
mkdir -p "$R"
echo "=== fastpoll-r18 $LABEL start=$(date -u +%FT%TZ) agent_md5=$(md5sum /etc/flux_agent/flux_agent 2>/dev/null | cut -d' ' -f1) ==="
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
print(ts() + " agent restarted, polling 50ms x 45s", flush=True)
hits = 0
last = ""
t0 = time.monotonic()
while time.monotonic() - t0 < 45:
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
echo "--- agent: $(systemctl is-active flux_agent) pid=$(systemctl show flux_agent -p MainPID --value)"
echo "--- agent journal AddService/UpdateService lines since restart (last 40):"
journalctl -u flux_agent --no-pager --since "2 minutes ago" 2>/dev/null | grep -iE "AddService|UpdateService|listening on|bind" | tail -40
echo "--- post md5: $(md5sum /etc/flux_agent/gost.json | cut -d' ' -f1) lines=$(wc -l < /etc/flux_agent/gost.json)"
echo "=== fastpoll-r18 $LABEL end=$(date -u +%FT%TZ) ==="
