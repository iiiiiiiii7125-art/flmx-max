#!/usr/bin/env python3
"""R22-L2 SOCKS5 driver through a local xray client (VMess/SS legs).

Usage: r22_l2_socks.py SOCKS_PORT DUMMY_PORT MODE [TIMEOUT]
  MODE: silent (send nothing, wait EOF) | batch1mb (send 1MB, wait EOF)
Prints SUMMARY with close time; exit 2 on timeout (STILL_OPEN).
"""
import socket
import sys
import time

socks_port = int(sys.argv[1])
dummy_port = int(sys.argv[2])
mode = sys.argv[3]
timeout = float(sys.argv[4]) if len(sys.argv) > 4 else 90.0
# D-GEO-01: target must be a public IP (node xray blocks private targets).
target_ip = sys.argv[5] if len(sys.argv) > 5 else "127.0.0.1"

t_start = time.time()
s = socket.create_connection(("127.0.0.1", socks_port), timeout=10)
s.settimeout(timeout)
s.sendall(b"\x05\x01\x00")
assert s.recv(2) == b"\x05\x00"
s.sendall(b"\x05\x01\x00\x01" + bytes(int(x) for x in target_ip.split(".")) + dummy_port.to_bytes(2, "big"))
rep = s.recv(10)
assert rep[1] == 0, f"socks connect failed: {rep.hex()}"
print(f"socks connected elapsed={time.time() - t_start:.2f}", flush=True)
if mode == "batch1mb":
    chunk = b"z" * 65536
    sent = 0
    while sent < 1048576:
        n = s.send(chunk[: min(len(chunk), 1048576 - sent)])
        sent += n
    print(f"burst1-sent elapsed={time.time() - t_start:.2f}", flush=True)
first_data = None
last_data = None
try:
    while True:
        data = s.recv(65536)
        if not data:
            break
        if first_data is None:
            first_data = time.time()
        last_data = time.time()
except socket.timeout:
    print(f"SUMMARY first_data={-1 if first_data is None else round(first_data - t_start, 2)} "
          f"last_data={-1 if last_data is None else round(last_data - t_start, 2)} close=TIMEOUT", flush=True)
    sys.exit(2)
t_close = time.time()
print(f"SUMMARY first_data={-1 if first_data is None else round(first_data - t_start, 2)} "
      f"last_data={-1 if last_data is None else round(last_data - t_start, 2)} "
      f"close={round(t_close - t_start, 2)} "
      f"idle_since_last_data={-1 if last_data is None else round(t_close - last_data, 2)}", flush=True)
