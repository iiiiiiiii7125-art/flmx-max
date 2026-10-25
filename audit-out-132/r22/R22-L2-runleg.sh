#!/bin/bash
# usage: R22-L2-runleg.sh TAG KIND ARGS...
# Detached runner: starts dummy, runs one timing client, appends DONE marker.
# KIND: vless INBOUND DUMMY UUID BURST2 TIMEOUT | silent INBOUND DUMMY UUID TIMEOUT | socks SOCKS DUMMY MODE TIMEOUT
cd /root/r22-l2 || exit 2
TAG="$1"; KIND="$2"; shift 2
DUMMY_PORT=""
case "$KIND" in
  vless) DUMMY_PORT="$2" ;;
  silent) DUMMY_PORT="$2" ;;
  socks) DUMMY_PORT="$2" ;;
esac
: > "dummy-${TAG}.log"
(setsid nohup python3 R22-L2-dummy.py "$DUMMY_PORT" silent 120 > "dummy-${TAG}.log" 2>&1 < /dev/null &)
sleep 1
case "$KIND" in
  vless)  python3 R22-L2-vless.py "$1" "$2" "$3" "$4" "$5" "$6" > "client-${TAG}.log" 2>&1 ;;
  silent) python3 - "$1" "$2" "$3" "$4" "$5" <<'PYEOF' > "client-${TAG}.log" 2>&1
import socket, sys, time, uuid
inbound_port, dummy_port, user_uuid, timeout = int(sys.argv[1]), int(sys.argv[2]), uuid.UUID(sys.argv[3]), float(sys.argv[4])
target_ip = sys.argv[5] if len(sys.argv) > 5 else "127.0.0.1"
t0 = time.time()
s = socket.create_connection(("127.0.0.1", inbound_port), timeout=10)
s.settimeout(5)
req = b"\x00" + user_uuid.bytes + b"\x00" + b"\x01" + dummy_port.to_bytes(2, "big") + b"\x01" + bytes(int(x) for x in target_ip.split("."))
s.sendall(req)
try:
    hdr = s.recv(2)
    print(f"resp-hdr={hdr.hex()} elapsed={time.time()-t0:.2f}", flush=True)
except socket.timeout:
    # VLESS response header is lazily flushed (arrives at close, if ever);
    # a silent session has no downstream data, so no header is expected.
    print(f"resp-hdr=NONE within 5s elapsed={time.time()-t0:.2f}", flush=True)
s.settimeout(timeout)
try:
    while True:
        data = s.recv(65536)
        if not data:
            break
except socket.timeout:
    print(f"SUMMARY close=TIMEOUT after={timeout}", flush=True)
    sys.exit(2)
print(f"SUMMARY close={round(time.time()-t0, 2)}", flush=True)
PYEOF
    ;;
  socks)  python3 R22-L2-socks.py "$1" "$2" "$3" "$4" "$5" > "client-${TAG}.log" 2>&1 ;;
esac
echo "RC=$?" >> "client-${TAG}.log"
sleep 2
echo "DONE $TAG $(date -u +%H:%M:%S)" >> "client-${TAG}.log"
