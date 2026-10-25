#!/usr/bin/env python3
"""R22-L2 bare VLESS direct client (R20 method, no intermediate xray).

Connects straight to the node VLESS inbound (127.0.0.1:INBOUND), asks it
to dial the dummy (TARGET_IP:DUMMY), sends burst1 (1MB) immediately
after the request (VLESS pipelining: the server's 2-byte response header
is lazily flushed, waiting for it first stalls the session until the
idle timer fires), optionally burst2 (1MB) at t0+BURST2_DELAY, then
reads until EOF and reports first/last data + close times.

Usage: r22_l2_vless.py INBOUND_PORT DUMMY_PORT UUID BURST2_DELAY [TIMEOUT [TARGET_IP]]
  BURST2_DELAY < 0 : no second burst (single-burst leg)
Prints one SUMMARY line; exit 0 on EOF observed, 2 on timeout (STILL_OPEN).
"""
import socket
import sys
import time
import uuid

inbound_port = int(sys.argv[1])
dummy_port = int(sys.argv[2])
user_uuid = uuid.UUID(sys.argv[3])
burst2_delay = float(sys.argv[4])
timeout = float(sys.argv[5]) if len(sys.argv) > 5 else 90.0
# D-GEO-01: node xray 26.10.21 freedom blocks private targets (incl.
# 127.0.0.1, blackholed 30-90s); timing legs must target the public IP.
target_ip = sys.argv[6] if len(sys.argv) > 6 else "127.0.0.1"


def now():
    return time.time()


t_start = now()
s = socket.create_connection(("127.0.0.1", inbound_port), timeout=10)
s.settimeout(timeout)
# VLESS request: ver(0) uuid(16) addon_len(0) cmd(1=TCP) port(2) atyp(1) ipv4(4)
req = b"\x00" + user_uuid.bytes + b"\x00" + b"\x01" + dummy_port.to_bytes(2, "big") + b"\x01" + bytes(int(x) for x in target_ip.split("."))
s.sendall(req)
# Pipeline burst1 immediately: do NOT wait for the response header.
chunk = b"y" * 65536
sent = 0
while sent < 1048576:
    n = s.send(chunk[: min(len(chunk), 1048576 - sent)])
    sent += n
t_burst1 = now()
print(f"burst1-sent elapsed={t_burst1 - t_start:.2f}", flush=True)
t_burst2 = None
if burst2_delay >= 0:
    # keep reading (and discarding) while waiting for burst2 time
    s.settimeout(max(0.1, burst2_delay))
    try:
        while now() - t_burst1 < burst2_delay:
            data = s.recv(65536)
            if not data:
                print(f"EOF-before-burst2 elapsed={now() - t_start:.2f}", flush=True)
                print(f"SUMMARY first_data=-1 last_data=-1 close={now() - t_start:.2f} burst2=not-sent", flush=True)
                sys.exit(0)
    except socket.timeout:
        pass
    s.settimeout(timeout)
    sent = 0
    while sent < 1048576:
        n = s.send(chunk[: min(len(chunk), 1048576 - sent)])
        sent += n
    t_burst2 = now()
    print(f"burst2-sent elapsed={t_burst2 - t_start:.2f}", flush=True)

first_data = None
last_data = None
header_stripped = False
try:
    while True:
        data = s.recv(65536)
        if not data:
            break
        # The 2-byte VLESS response header rides the first downstream
        # bytes; strip it once so it is not counted as payload data.
        if not header_stripped:
            header_stripped = True
            if len(data) >= 2:
                data = data[2:]
                if not data:
                    continue
            else:
                continue
        if first_data is None:
            first_data = now()
        last_data = now()
except socket.timeout:
    print(f"STILL_OPEN after {timeout}s first_data={None if first_data is None else round(first_data - t_start, 2)} "
          f"last_data={None if last_data is None else round(last_data - t_start, 2)}", flush=True)
    print(f"SUMMARY first_data={-1 if first_data is None else round(first_data - t_start, 2)} "
          f"last_data={-1 if last_data is None else round(last_data - t_start, 2)} close=TIMEOUT burst2={-1 if t_burst2 is None else round(t_burst2 - t_start, 2)}", flush=True)
    sys.exit(2)
t_close = now()
print(f"SUMMARY first_data={-1 if first_data is None else round(first_data - t_start, 2)} "
      f"last_data={-1 if last_data is None else round(last_data - t_start, 2)} "
      f"close={round(t_close - t_start, 2)} burst2={-1 if t_burst2 is None else round(t_burst2 - t_start, 2)} "
      f"idle_since_last_data={-1 if last_data is None else round(t_close - last_data, 2)} "
      f"idle_since_burst1={round(t_close - t_burst1, 2)} "
      f"idle_since_burst2={-1 if t_burst2 is None else round(t_close - t_burst2, 2)}", flush=True)
