#!/usr/bin/env python3
"""R22-L2 dummy TCP server: logs accept / data / close with timestamps.

Modes:
  silent   accept, send nothing, wait for peer close (up to TIMEOUT s)
  batch1mb accept, send 1MB in 64K chunks, then wait for peer close
Usage: r22_l2_dummy.py PORT MODE [TIMEOUT]
Log lines are flushed immediately; the driver greps them.
"""
import socket
import sys
import time

port = int(sys.argv[1])
mode = sys.argv[2]
timeout = float(sys.argv[3]) if len(sys.argv) > 3 else 90.0


def ts():
    return time.strftime("%H:%M:%S", time.gmtime()) + f".{int((time.time() % 1) * 1000):03d}"


srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("0.0.0.0", port))
srv.listen(5)
srv.settimeout(timeout)
print(f"[{ts()}] dummy listening port={port} mode={mode} timeout={timeout}", flush=True)
try:
    conn, addr = srv.accept()
except socket.timeout:
    print(f"[{ts()}] dummy ACCEPT_TIMEOUT", flush=True)
    sys.exit(0)
t0 = time.time()
print(f"[{ts()}] dummy ACCEPT from={addr}", flush=True)
conn.settimeout(timeout)
total = 0
try:
    if mode == "batch1mb":
        chunk = b"x" * 65536
        sent = 0
        while sent < 1048576:
            n = conn.send(chunk[: min(len(chunk), 1048576 - sent)])
            sent += n
        print(f"[{ts()}] dummy SENT 1048576 elapsed={time.time() - t0:.2f}", flush=True)
    while True:
        try:
            data = conn.recv(65536)
        except socket.timeout:
            print(f"[{ts()}] dummy RECV_TIMEOUT elapsed={time.time() - t0:.2f} total_recv={total}", flush=True)
            break
        if not data:
            print(f"[{ts()}] dummy EOF elapsed={time.time() - t0:.2f} total_recv={total}", flush=True)
            break
        total += len(data)
        print(f"[{ts()}] dummy RECV n={len(data)} total={total} elapsed={time.time() - t0:.2f}", flush=True)
except Exception as e:
    print(f"[{ts()}] dummy ERR {e} elapsed={time.time() - t0:.2f}", flush=True)
finally:
    conn.close()
    srv.close()
print(f"[{ts()}] dummy DONE elapsed={time.time() - t0:.2f}", flush=True)
