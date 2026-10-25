#!/bin/bash
# R17-FIX-D leg4 phase 3: behavior legs (connIdle=5).
# Criteria (R17-L4, pre-registered): D1-direct (Trojan/VLESS direct) close in
# 3.5-6.5s; D1-supply (VMess/SS upstream supply) close 13.5-16.5s after start
# (5s after the 10s supply stops); D2-control (no-idle / minimal) STILL_OPEN@30s.
# D1-silent: VMess/SS fully silent close ~0.5-2.0s is expected upstream
# behavior (O-148B-1), not a FAIL.
# Log to stdout (caller tees). Each leg prints its own line; total ~5 min.
set -u
S=/root/r17
echo "=== leg4 legs start=$(date -u +%FT%TZ) ==="
echo "--- R17D-VL-1 VLESS direct main (expect CLOSED ~5s, D1-direct 3.5-6.5s)"
python3 $S/vless_test.py 220a6e10-7829-4afa-92c9-766ec3578042 59904 59909 20 R17D-VL-1
echo "--- R17D-VM-2 VMess supply main (expect CLOSED 13.5-16.5s)"
python3 $S/socks_test3.py 11080 59909 10 30 R17D-VM-2
echo "--- R17D-SS-2 SS supply main via door (expect CLOSED 13.5-16.5s)"
python3 $S/ss_door_feed.py 11087 10 30 R17D-SS-2
echo "--- R17D-TR-1 Trojan direct main (expect CLOSED 3.5-6.5s)"
python3 $S/trojan_test.py 59906 r17-trojan-8146961def35 59909 20 R17D-TR-1
echo "--- R17D-CTL-supply VMess supply no-idle control (expect STILL_OPEN@30s)"
python3 $S/socks_test3.py 11083 59909 10 30 R17D-CTL-supply
echo "--- R17D-VM-2m VMess supply minimal (expect STILL_OPEN@30s)"
python3 $S/socks_test3.py 11084 59909 10 30 R17D-VM-2m
echo "--- R17D-SS-2m SS supply minimal via door (expect STILL_OPEN@30s)"
python3 $S/ss_door_feed.py 11085 10 30 R17D-SS-2m
echo "--- R17D-VM-1 VMess silent (expect CLOSED ~0.5-2.0s, O-148B-1 upstream)"
python3 $S/socks_test2.py 11080 59909 20 R17D-VM-1
echo "=== leg4 legs end=$(date -u +%FT%TZ) ==="
