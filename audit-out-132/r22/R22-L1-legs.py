#!/usr/bin/env python3
"""R22-L1 close-r21 live legs (test machine, lane R22-L1).

Adapted from R21-F03 legs driver (same API/cred/journal patterns) +
R21-F01 tunengine leg. Reads panel creds from /root/panel-cred.txt
(never printed). Token cached in /root/r22-l1/.token (0600).
API calls go to on-machine panel http://127.0.0.1:6366.
gost.json: /etc/flux_agent/gost.json; agent journal: journalctl -u flux_agent.

Legs (argv[1]):
  gost                    gost summary (md5/services/climiters/refs)
  counts                  API counts node/tunnel/forward/inbound/subscription (expect 1/0/0/1/1)
  nodelist                node/list
  sysver                  POST /system/version
  mktunnel NAME           create gost tunnel (entry node 1, tls); print id
  deltunnel ID            delete tunnel
  mkfwd NAME TUNNEL MAXCONN IPMAXCONN REMOTE
                          create forward with limits; print id + gost summary
  updfwd ID JSON          update forward (raw JSON body); print code + gost
  delfwd ID               normal delete; print gost summary
  forcefwd ID             force delete; print response + gost summary
  tunengine ID ENGINE     full tunnel/get -> change engine -> tunnel/update; print gost + journal-since
  tunget ID               tunnel/get (engine)
  realmconf               realm config.toml endpoints (if present)
  listen PORT             ss listen check for PORT (flux_agent or none)
  journal SINCE GREP      journalctl -u flux_agent --since SINCE | grep GREP (count + lines)
  agentstop               systemctl stop flux_agent
  agentstart              systemctl reset-failed + start flux_agent, wait active
  agentstatus             systemctl is-active flux_agent + agent binary md5
"""
import json
import os
import subprocess
import sys
import time
import urllib.request

BASE = "http://127.0.0.1:6366/api/v1"
CRED = "/root/panel-cred.txt"
TOKEN_CACHE = "/root/r22-l1/.token"
GOST = "/etc/flux_agent/gost.json"
REALM_CONF = "/etc/flux_agent/kernels/realm/run/config.toml"

failures = []


def check(name, cond, detail=""):
    print(("PASS " if cond else "FAIL ") + name + ((" :: " + str(detail)) if detail else ""), flush=True)
    if not cond:
        failures.append(name)


def login():
    creds = {}
    for line in open(CRED):
        if ":" in line:
            k, v = line.split(":", 1)
            creds[k.strip()] = v.strip()
    token = None
    if os.path.exists(TOKEN_CACHE) and time.time() - os.path.getmtime(TOKEN_CACHE) < 1200:
        with open(TOKEN_CACHE) as f:
            token = f.read().strip()
    if not token:
        _, resp = call("/user/login", {"username": creds["username"], "password": creds["password"]})
        if resp.get("code") != 0:
            print("LOGIN FAILED", resp)
            sys.exit(2)
        token = resp["data"]["token"] if isinstance(resp.get("data"), dict) else resp.get("data")
        with open(TOKEN_CACHE, "w") as f:
            f.write(token)
        os.chmod(TOKEN_CACHE, 0o600)
    return token


def call(path, body, token=None):
    req = urllib.request.Request(
        BASE + path,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json",
                 **({"Authorization": token} if token else {})},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=60) as resp:
        return resp.status, json.loads(resp.read().decode())


def gost():
    with open(GOST) as f:
        data = json.load(f)
    services = sorted(s.get("name", "") for s in (data.get("services") or []))
    climiters = sorted(c.get("name", "") for c in (data.get("climiters") or []))
    refs = sorted({s.get("climiter") for s in (data.get("services") or []) if s.get("climiter")})
    return services, climiters, refs, data


def gost_md5():
    out = subprocess.run(["md5sum", GOST], capture_output=True, text=True).stdout
    return out.split()[0]


def items_of(resp_data):
    items = resp_data.get("data", [])
    if isinstance(items, dict):
        items = items.get("list", items.get("items", []))
    return items if isinstance(items, list) else []


def forward_id_by_name(token, name):
    _, data = call("/forward/list", {}, token)
    for it in items_of(data):
        if it.get("name") == name:
            return it.get("id")
    return None


def journal_grep(since, pattern):
    out = subprocess.run(["journalctl", "-u", "flux_agent", "--since", since, "--no-pager"],
                         capture_output=True, text=True).stdout
    return [ln for ln in out.splitlines() if pattern in ln]


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(2)
    leg = sys.argv[1]
    token = login() if leg not in ("gost", "journal", "realmconf", "listen", "agentstop", "agentstart", "agentstatus") else None

    if leg == "gost":
        services, climiters, refs, _ = gost()
        print(f"gost md5={gost_md5()} services={len(services)} climiters={climiters} refs={refs}")
    elif leg == "sysver":
        _, d = call("/system/version", {}, token)
        print("sysver:", json.dumps(d.get("data", d), ensure_ascii=False)[:600])
    elif leg == "mktunnel":
        name = sys.argv[2]
        http, resp = call("/tunnel/create", {"name": name, "type": 1, "status": 1, "flow": 1,
                                             "trafficRatio": 1,
                                             "inNodeId": [{"nodeId": 1, "protocol": "tls"}]}, token)
        print(f"create tunnel http={http} code={resp.get('code')} msg={resp.get('msg')} data={resp.get('data')}")
        check("create tunnel code 0", http == 200 and resp.get("code") == 0, resp)
        _, d = call("/tunnel/list", {}, token)
        mine = [x for x in items_of(d) if x.get("name") == name]
        print("tunnel id:", mine[0].get("id") if mine else None,
              "engine:", mine[0].get("engine") if mine else None)
        check("tunnel listed (gost engine)", len(mine) == 1 and not mine[0].get("engine"), mine)
    elif leg == "deltunnel":
        tid = int(sys.argv[2])
        http, resp = call("/tunnel/delete", {"id": tid}, token)
        print(f"delete tunnel http={http} code={resp.get('code')} msg={resp.get('msg')} data={resp.get('data')}")
        check("delete tunnel code 0", http == 200 and resp.get("code") == 0, resp)
        time.sleep(2)
        _, d = call("/tunnel/list", {}, token)
        mine = [x for x in items_of(d) if x.get("id") == tid]
        check("tunnel gone from list", len(mine) == 0, mine)
    elif leg == "counts":
        out = {}
        for path, key in [("/node/list", "node"), ("/tunnel/list", "tunnel"),
                          ("/forward/list", "forward"), ("/inbound/list", "inbound"),
                          ("/subscription/list", "subscription")]:
            _, d = call(path, {}, token)
            out[key] = len(items_of(d))
        print("counts:", out)
        check("counts 1/0/0/1/1", out == {"node": 1, "tunnel": 0, "forward": 0, "inbound": 1, "subscription": 1}, out)
    elif leg == "nodelist":
        _, d = call("/node/list", {}, token)
        for n in items_of(d):
            print("node", {k: n.get(k) for k in ("id", "name", "status", "version", "forwardMode") if k in n})
    elif leg == "fwdlist":
        _, d = call("/forward/list", {}, token)
        for f in items_of(d):
            print("forward", {k: f.get(k) for k in ("id", "name", "tunnelId", "remoteAddr", "maxConn", "ipMaxConn", "status", "inPort") if k in f})
    elif leg == "mkfwd":
        name, tunnel, maxconn, ipmaxconn, remote = sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), int(sys.argv[5]), sys.argv[6]
        http, resp = call("/forward/create", {"name": name, "tunnelId": tunnel, "remoteAddr": remote,
                                              "strategy": "fifo", "maxConn": maxconn, "ipMaxConn": ipmaxconn}, token)
        print(f"create http={http} code={resp.get('code')} msg={resp.get('msg')}")
        check("create code 0", http == 200 and resp.get("code") == 0, resp)
        fid = forward_id_by_name(token, name)
        print("forward id:", fid)
        check("forward id found", fid is not None)
        time.sleep(2)
        services, climiters, refs, _ = gost()
        print(f"gost services={len(services)} climiters={climiters} refs={refs}")
        if maxconn > 0 or ipmaxconn > 0:
            want = f"rule_conn_limit_{fid}"
            check("gost has new limiter", want in climiters, climiters)
            check("service refs new limiter", want in refs, refs)
    elif leg == "updfwd":
        fid, body = int(sys.argv[2]), json.loads(sys.argv[3])
        body["id"] = fid
        since = time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime())
        http, resp = call("/forward/update", body, token)
        print(f"update http={http} code={resp.get('code')} msg={resp.get('msg')} data={resp.get('data')}")
        check("update code 0", http == 200 and resp.get("code") == 0, resp)
        time.sleep(2)
        services, climiters, refs, _ = gost()
        print(f"gost services={len(services)} climiters={climiters} refs={refs}")
        print("journal-since:", since)
    elif leg == "delfwd":
        fid = int(sys.argv[2])
        http, resp = call("/forward/delete", {"id": fid}, token)
        print(f"delete http={http} code={resp.get('code')} msg={resp.get('msg')}")
        check("delete code 0", http == 200 and resp.get("code") == 0, resp)
        time.sleep(2)
        services, climiters, refs, _ = gost()
        print(f"gost services={len(services)} climiters={climiters} refs={refs}")
    elif leg == "forcefwd":
        fid = int(sys.argv[2])
        http, resp = call("/forward/force-delete", {"id": fid}, token)
        print(f"force-delete http={http} code={resp.get('code')} msg={resp.get('msg')}")
        check("force-delete code 0", http == 200 and resp.get("code") == 0, resp)
        time.sleep(2)
        services, climiters, refs, _ = gost()
        print(f"gost services={len(services)} climiters={climiters} refs={refs}")
    elif leg == "tunget":
        tid = int(sys.argv[2])
        _, d = call("/tunnel/get", {"id": tid}, token)
        print("tunget:", json.dumps(d.get("data", d), ensure_ascii=False)[:800])
    elif leg == "tunengine":
        tid, engine = int(sys.argv[2]), sys.argv[3]
        _, d = call("/tunnel/get", {"id": tid}, token)
        body = d.get("data")
        if not isinstance(body, dict):
            print("tunget failed", d); sys.exit(2)
        old_engine = body.get("engine")
        body["engine"] = engine
        since = time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime())
        http, resp = call("/tunnel/update", body, token)
        print(f"tunnel engine {old_engine!r} -> {engine!r} http={http} code={resp.get('code')} msg={resp.get('msg')} data={resp.get('data')}")
        check("tunnel engine update code 0", http == 200 and resp.get("code") == 0, resp)
        time.sleep(3)
        _, d2 = call("/tunnel/get", {"id": tid}, token)
        print("tunnel engine now:", (d2.get("data") or {}).get("engine"))
        check("engine persisted", (d2.get("data") or {}).get("engine") == engine, d2.get("data"))
        services, climiters, refs, _ = gost()
        print(f"gost services={len(services)} climiters={climiters} refs={refs}")
        print("journal-since:", since)
    elif leg == "realmconf":
        if not os.path.exists(REALM_CONF):
            print("realm config absent:", REALM_CONF)
        else:
            txt = open(REALM_CONF).read()
            eps = [ln.strip() for ln in txt.splitlines() if ln.strip().startswith("[[endpoints]]") or "listen" in ln or "remote" in ln]
            print(f"realm config bytes={len(txt)} endpoints-markers={txt.count('[[endpoints]]')}")
            for ln in eps:
                print("   " + ln[:200])
    elif leg == "listen":
        port = sys.argv[2]
        out = subprocess.run(["ss", "-ltnp"], capture_output=True, text=True).stdout
        hits = [ln for ln in out.splitlines() if (":"+port+" ") in ln or (":"+port+"\t") in ln]
        if hits:
            for ln in hits:
                print("LISTEN " + ln.strip())
            check(f"port {port} listening", True)
        else:
            print(f"NO_LISTENER_{port}")
            check(f"port {port} listening", False)
    elif leg == "journal":
        since, pattern = sys.argv[2], sys.argv[3]
        lines = journal_grep(since, pattern)
        print(f"journal matches since={since} pattern={pattern!r}: {len(lines)}")
        for ln in lines:
            print("   " + ln.strip()[:400])
    elif leg == "agentstop":
        out = subprocess.run(["systemctl", "stop", "flux_agent"], capture_output=True, text=True)
        print("agentstop rc=", out.returncode, out.stdout.strip(), out.stderr.strip())
        time.sleep(2)
        out = subprocess.run(["systemctl", "is-active", "flux_agent"], capture_output=True, text=True)
        print("agent state after stop:", out.stdout.strip())
        check("agent stopped", out.stdout.strip() in ("inactive", "failed"))
    elif leg == "agentstart":
        subprocess.run(["systemctl", "reset-failed", "flux_agent"], capture_output=True, text=True)
        out = subprocess.run(["systemctl", "start", "flux_agent"], capture_output=True, text=True)
        print("agentstart rc=", out.returncode, out.stdout.strip(), out.stderr.strip())
        for _ in range(15):
            time.sleep(2)
            out = subprocess.run(["systemctl", "is-active", "flux_agent"], capture_output=True, text=True)
            if out.stdout.strip() == "active":
                break
        print("agent state after start:", out.stdout.strip())
        check("agent active", out.stdout.strip() == "active")
    elif leg == "agentstatus":
        out = subprocess.run(["systemctl", "is-active", "flux_agent"], capture_output=True, text=True)
        print("agent state:", out.stdout.strip())
        out2 = subprocess.run(["md5sum", "/usr/local/bin/flux_agent"], capture_output=True, text=True)
        print("agent binary md5:", out2.stdout.strip())
        check("agent active", out.stdout.strip() == "active")

    print("=== leg %s end failures=%d %s ===" % (leg, len(failures), failures))
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
