#!/usr/bin/env python3
# R17-FIX-D leg1 watcher: poll the backend container's json log file every
# 2ms for the B-guard "send" line of forward 6's update-fallback, then kill
# the dummy port occupier immediately so the fallback AddService's agent-side
# ParseService finds the port free.
# Usage: R17-FIX-D-watcher.py <backend-log-file> <dummy-pid> <timeout-secs>
# Logs every guard line seen (with poll timestamp) to stdout.
import json, os, signal, sys, time

LOG = sys.argv[1]
DUMMY_PID = int(sys.argv[2])
TIMEOUT = float(sys.argv[3]) if len(sys.argv) > 3 else 120
MARK = "r17-addservice-guard: node=1 decision=send reason=update-fallback services=[6_1_0_tcp,6_1_0_udp]"

def now():
    return time.strftime("%H:%M:%S", time.gmtime()) + ".%03d" % (int(time.time() * 1000) % 1000)

def main():
    f = open(LOG, "rb")
    f.seek(0, os.SEEK_END)
    start = time.monotonic()
    buf = b""
    killed = False
    while time.monotonic() - start < TIMEOUT:
        chunk = f.read()
        if chunk:
            buf += chunk
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                try:
                    msg = json.loads(line).get("log", "")
                except Exception:
                    continue
                if "r17-addservice-guard" in msg:
                    print(f"{now()} GUARD {msg.strip()}", flush=True)
                if MARK in msg and not killed:
                    print(f"{now()} MATCH -> killing dummy pid={DUMMY_PID}", flush=True)
                    try:
                        os.kill(DUMMY_PID, signal.SIGTERM)
                    except ProcessLookupError:
                        print(f"{now()} dummy already gone", flush=True)
                    killed = True
                    print(f"{now()} WATCHER DONE killed={killed}", flush=True)
                    return 0
        else:
            time.sleep(0.002)
    print(f"{now()} WATCHER TIMEOUT killed={killed}", flush=True)
    return 1

if __name__ == "__main__":
    sys.exit(main())
