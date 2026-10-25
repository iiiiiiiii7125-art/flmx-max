#!/usr/bin/env python3
# R17-FIX-D leg1: occupy forward 6's ports (TCP+UDP 14601, v4+v6) so the
# panel's redeploy UpdateService fails with bind error and falls back to
# AddService. Exits on SIGTERM/SIGINT. Logs bind results with timestamps.
import socket, signal, sys, time

PORT = 14601
socks = []

def ts():
    return time.strftime("%H:%M:%S", time.gmtime()) + ".%03d" % (int(time.time() * 1000) % 1000)

def bind_tcp(host, family):
    s = socket.socket(family, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind((host, PORT))
    s.listen(5)
    return s

def bind_udp(host, family):
    s = socket.socket(family, socket.SOCK_DGRAM)
    s.bind((host, PORT))
    return s

def main():
    bound = 0
    for label, fn in (("tcp4", lambda: bind_tcp("0.0.0.0", socket.AF_INET)),
                      ("tcp6", lambda: bind_tcp("::", socket.AF_INET6)),
                      ("udp4", lambda: bind_udp("0.0.0.0", socket.AF_INET)),
                      ("udp6", lambda: bind_udp("::", socket.AF_INET6))):
        try:
            socks.append(fn())
            bound += 1
            print(f"{ts()} dummy bound {label} :{PORT}", flush=True)
        except Exception as e:
            # v4 0.0.0.0 already blocks the agent's [::] bind; v6 is best-effort.
            print(f"{ts()} dummy SKIP {label} :{PORT}: {e}", flush=True)
    if bound == 0:
        print(f"{ts()} dummy bound NOTHING, exiting", flush=True)
        sys.exit(2)
    print(f"{ts()} dummy holding :{PORT} pid={__import__('os').getpid()}", flush=True)
    signal.pause()

if __name__ == "__main__":
    main()
