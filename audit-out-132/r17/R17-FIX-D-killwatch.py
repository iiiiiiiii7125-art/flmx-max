#!/usr/bin/env python3
# R17-FIX-D kill-watcher: poll the backend json log every 1ms for a
# successful fallback AddService (decision=sent-ok reason=update-fallback)
# and SIGKILL the agent immediately, freezing gost.json before the direct
# pass / retries can rewrite it. Tests whether the duplicate exists on disk
# at sent-ok time on the UN-FIXED agent.
# Usage: R17-FIX-D-killwatch.py <backend-log> <agent-pid> <timeout-secs>
import json, os, signal, sys, time

LOG, PID, TIMEOUT = sys.argv[1], int(sys.argv[2]), float(sys.argv[3])

def now():
    return time.strftime("%H:%M:%S", time.gmtime()) + ".%03d" % (int(time.time() * 1000) % 1000)

def main():
    f = open(LOG, "rb")
    f.seek(0, os.SEEK_END)
    start = time.monotonic()
    buf = b""
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
                if "decision=sent-ok reason=update-fallback" in msg:
                    print(f"{now()} SENT-OK {msg.strip()} -> SIGKILL agent pid={PID}", flush=True)
                    try:
                        os.kill(PID, signal.SIGKILL)
                        print(f"{now()} KILLED", flush=True)
                    except ProcessLookupError:
                        print(f"{now()} agent already gone", flush=True)
                    return 0
        else:
            time.sleep(0.001)
    print(f"{now()} TIMEOUT no sent-ok seen", flush=True)
    return 1

if __name__ == "__main__":
    sys.exit(main())
