#!/bin/bash
# R17-FIX-D leg4 phase 2: start the timing scaffold (dummy target + client
# xrays + minimal xrays). Run from /root/r17 (relative xclient/minxray paths).
# Log to stdout (caller tees). Idempotent-ish: kills any stale scaffold first.
set -u
S=/root/r17
X=/etc/flux_agent/kernels/xray/bin/latest/xray
cd $S
echo "=== leg4 scaffold start=$(date -u +%FT%TZ) ==="
echo "--- kill stale scaffold (dummies + non-agent xrays)"
pkill -f "dummy.py 59909" 2>/dev/null || true
pkill -f "xray run -c xclient/" 2>/dev/null || true
pkill -f "xray run -c minxray/" 2>/dev/null || true
sleep 2
nohup python3 $S/dummy.py 59909 0 > $S/dummy-59909.log 2>&1 &
echo "dummy silent pid $!"
nohup $X run -c xclient/vmess.json > xclient/vmess.log 2>&1 &
echo "vmess pid $!"
nohup $X run -c xclient/vmess-ctl.json > xclient/vmess-ctl.log 2>&1 &
echo "vmess-ctl pid $!"
nohup $X run -c xclient/vmess-u1.json > xclient/vmess-u1.log 2>&1 &
echo "vmess-u1 pid $!"
nohup $X run -c xclient/trojan.json > xclient/trojan.log 2>&1 &
echo "trojan pid $!"
nohup $X run -c xclient/ss-door.json > xclient/ss-door.log 2>&1 &
echo "ss-door pid $!"
nohup $X run -c xclient/vmess-min.json > xclient/vmess-min.log 2>&1 &
echo "vmess-min pid $!"
nohup $X run -c xclient/ss-min.json > xclient/ss-min.log 2>&1 &
echo "ss-min pid $!"
nohup $X run -c minxray/vmess.json > minxray/vmess-stdout.log 2>&1 &
echo "min-vmess pid $!"
nohup $X run -c minxray/ss.json > minxray/ss-stdout.log 2>&1 &
echo "min-ss pid $!"
sleep 5
echo "--- listening"
ss -tlnp 2>/dev/null | grep -E "59909|1108[0-9]|5991[23]" | sort
echo "--- processes"
ps -eo pid,cmd | grep -E "dummy.py 59909|xray run -c (xclient|minxray)/" | grep -v grep
echo "=== leg4 scaffold end=$(date -u +%FT%TZ) ==="
