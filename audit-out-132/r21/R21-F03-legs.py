#!/usr/bin/env python3
"""R21-F03 fix live legs (test machine, lane R21-F03).

Reads panel creds from /root/panel-cred.txt (never printed). Token cached
in /root/r21-f03/.token (0600). All API calls go to the on-machine panel
http://127.0.0.1:6366 (frontend nginx -> backend). gost.json is read from
/etc/flux_agent/gost.json; agent journal via journalctl -u flux_agent.

Legs (argv[1]):
  gost                    print gost summary (md5/services/climiters/refs)
  counts                  API counts node/tunnel/forward/inbound/subscription
  sysver                  POST /system/version (served panel version)
  mktunnel NAME           create gost tunnel (entry node 1, tls); print id
  deltunnel ID            delete tunnel; print response
  mkfwd NAME TUNNEL MAXCONN IPMAXCONN REMOTE
                          create forward with limits; print id + gost summary
  addport FWDID PORT      DB-fixture: add a second forward_port row (node 1)
                          via python sqlite3 on the live volume DB
  delport FWDID PORT      remove the fixture row (cleanup)
  updfwd ID JSON          update forward (raw JSON body); print code/warnings
  batchtunnel IDS TARGET  batch change tunnel; print response
  delfwd ID               normal delete; print gost summary
  forcefwd ID             force delete; print response + gost summary
  journal SINCE GREP      journalctl -u flux_agent --since SINCE | grep GREP
                          (prints count + matching lines, truncated)
  mkinb NAME PROTO PORT IDLE SETTINGS_JSON
                          create inbound; print id
  updinb ID IDLE          update inbound connIdleSeconds (explicit value)
  inblist PORTS           list inbounds filtered to given ports (csv)
  xraycfg PORTS           parse node xray config: policy levels,
                          flmx-idle outbounds, inbound client levels
  nodelist                node/list (id/name/status/version)
  fwdlist                 forward/list (id/name/tunnel/maxConn/inPort)
"""
import json
import os
import subprocess
import sys
import time
import urllib.request

BASE = "http://127.0.0.1:6366/api/v1"
CRED = "/root/panel-cred.txt"
TOKEN_CACHE = "/root/r21-f03/.token"
GOST = "/etc/flux_agent/gost.json"
DB = "/var/lib/docker/volumes/sqlite_data/_data/gost.db"
# Found-state baseline 2026-10-25: gost.json has 0 services / 0 climiters
# (DB near-empty; NOT R21 pristine {rule_conn_limit_2, rule_conn_limit_12}).
PRISTINE_CLIMITERS = set()

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
    # Empty found-state config has null services/climiters (not []).
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
    token = login() if leg not in ("gost", "journal", "xraycfg") else None

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
        # Re-baselined 2026-10-25 (R21-F03续做): machine found with a near-empty
        # DB (1/0/0/1/1), NOT R21 pristine 1/4/6/578/18. Legs create their own
        # tunnel+forward and remove them, returning to this found baseline.
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
    elif leg == "addport":
        fid, port = int(sys.argv[2]), int(sys.argv[3])
        import sqlite3
        con = sqlite3.connect(DB, timeout=15)
        try:
            cur = con.execute("INSERT INTO forward_port (forward_id, node_id, port, in_ip) VALUES (?, 1, ?, NULL)", (fid, port))
            con.commit()
            print(f"inserted forward_port row id={cur.lastrowid} forward={fid} node=1 port={port}")
        finally:
            con.close()
        con = sqlite3.connect(f"file:{DB}?mode=ro", uri=True)
        rows = con.execute("SELECT node_id, port FROM forward_port WHERE forward_id=? ORDER BY port", (fid,)).fetchall()
        con.close()
        print(f"forward {fid} ports now: {rows}")
        check("forward has 2 ports", len(rows) == 2, rows)
    elif leg == "delport":
        fid, port = int(sys.argv[2]), int(sys.argv[3])
        import sqlite3
        con = sqlite3.connect(DB, timeout=15)
        try:
            con.execute("DELETE FROM forward_port WHERE forward_id=? AND node_id=1 AND port=?", (fid, port))
            con.commit()
        finally:
            con.close()
        print(f"deleted fixture port row forward={fid} port={port}")
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
    elif leg == "batchtunnel":
        ids = [int(x) for x in sys.argv[2].split(",")]
        target = int(sys.argv[3])
        http, resp = call("/forward/batch-change-tunnel", {"forwardIds": ids, "targetTunnelId": target}, token)
        print(f"batchtunnel http={http} code={resp.get('code')} msg={resp.get('msg')} data={resp.get('data')}")
        check("batchtunnel code 0", http == 200 and resp.get("code") == 0, resp)
        time.sleep(2)
        services, climiters, refs, _ = gost()
        print(f"gost services={len(services)} climiters={climiters} refs={refs}")
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
    elif leg == "journal":
        since, pattern = sys.argv[2], sys.argv[3]
        lines = journal_grep(since, pattern)
        print(f"journal matches since={since} pattern={pattern!r}: {len(lines)}")
        for ln in lines:
            print("   " + ln.strip()[:400])
    elif leg == "mkinb":
        name, proto, port, idle = sys.argv[2], sys.argv[3], int(sys.argv[4]), int(sys.argv[5])
        settings = json.loads(sys.argv[6])
        body = {"nodeId": 1, "kernelType": "xray", "name": name, "protocol": proto,
                "listen": "0.0.0.0", "port": port, "settings": settings,
                "transport": {"type": "tcp"} if proto == "vless" else {},
                "security": {"type": "none"} if proto in ("vless", "vmess") else {},
                "connIdleSeconds": idle}
        http, resp = call("/inbound/create", body, token)
        print(f"create inbound http={http} code={resp.get('code')} msg={resp.get('msg')}")
        check("create inbound code 0", http == 200 and resp.get("code") == 0, resp)
        _, d = call("/inbound/list", {}, token)
        mine = [x for x in items_of(d) if x.get("port") == port]
        print("inbound id:", mine[0].get("id") if mine else None, "idle:", mine[0].get("connIdleSeconds") if mine else None)
        check("inbound listed with idle", len(mine) == 1 and mine[0].get("connIdleSeconds") == idle, mine)
    elif leg == "updinb":
        iid, idle = int(sys.argv[2]), int(sys.argv[3])
        # inboundSave validates name (and re-saves the full object): a partial
        # {id, connIdleSeconds} body is rejected with code -1 "名称不能为空".
        # Fetch the current record and re-post it whole with only the idle changed.
        _, d = call("/inbound/list", {}, token)
        mine = [x for x in items_of(d) if x.get("id") == iid]
        if not mine:
            print(f"FAIL inbound {iid} not found")
            failures.append("inbound not found")
        else:
            cur = mine[0]
            body = {"id": iid, "nodeId": cur.get("nodeId"), "kernelType": cur.get("kernelType"),
                    "name": cur.get("name"), "protocol": cur.get("protocol"),
                    "listen": cur.get("listen"), "port": cur.get("port"),
                    "settings": cur.get("settings"), "transport": cur.get("transport"),
                    "security": cur.get("security"), "extra": cur.get("extra", ""),
                    "shareHost": cur.get("shareHost", ""), "remark": cur.get("remark", ""),
                    "relayOnly": cur.get("relayOnly", False),
                    "acceptProxyProtocol": cur.get("acceptProxyProtocol", False),
                    "relayWhitelist": cur.get("relayWhitelist", False),
                    "whitelistExtra": cur.get("whitelistExtra", ""),
                    "outboundId": cur.get("outboundId", 0),
                    "splitRules": cur.get("splitRules", []),
                    "outboundIpStrategy": cur.get("outboundIpStrategy", "auto"),
                    "connIdleSeconds": idle,
                    "enabled": cur.get("enabled", True),
                    "meteringEnabled": cur.get("meteringEnabled", False)}
            http, resp = call("/inbound/update", body, token)
            print(f"update inbound http={http} code={resp.get('code')} msg={resp.get('msg')} data={resp.get('data')}")
            check("update inbound code 0", http == 200 and resp.get("code") == 0, resp)
            time.sleep(6)
            _, d = call("/inbound/list", {}, token)
            mine = [x for x in items_of(d) if x.get("id") == iid]
            print("inbound after edit:", mine[0].get("connIdleSeconds") if mine else None)
            check("list shows new idle", len(mine) == 1 and mine[0].get("connIdleSeconds") == idle, mine)
    elif leg == "delinb":
        iid = int(sys.argv[2])
        http, resp = call("/inbound/delete", {"id": iid}, token)
        print(f"delete inbound http={http} code={resp.get('code')} msg={resp.get('msg')}")
        check("delete inbound code 0", http == 200 and resp.get("code") == 0, resp)
        time.sleep(6)
        _, d = call("/inbound/list", {}, token)
        mine = [x for x in items_of(d) if x.get("id") == iid]
        check("inbound gone from list", len(mine) == 0, mine)
    elif leg == "inblist":
        ports = {int(x) for x in sys.argv[2].split(",")}
        _, d = call("/inbound/list", {}, token)
        items = items_of(d)
        print("total inbound n=", len(items))
        for x in sorted([i for i in items if i.get("port") in ports], key=lambda z: z["port"]):
            print("  id=%s port=%s proto=%s idle=%s" % (x.get("id"), x.get("port"), x.get("protocol"), x.get("connIdleSeconds")))
    elif leg == "xraycfg":
        ports = {int(x) for x in sys.argv[2].split(",")}
        import glob
        cfgpath = None
        out = subprocess.run(["ps", "aux"], capture_output=True, text=True).stdout
        for ln in out.splitlines():
            if "xray run" in ln and "/etc/flux_agent/kernels/xray/run/config.json" in ln:
                cfgpath = "/etc/flux_agent/kernels/xray/run/config.json"
                break
        if cfgpath is None:
            for ln in out.splitlines():
                if "xray run" in ln and "-c " in ln and "xc-vmess" not in ln:
                    cfgpath = ln.split("-c ", 1)[1].split()[0]
                    break
        print("xray config path:", cfgpath)
        if not cfgpath or not os.path.exists(cfgpath):
            print("FAIL xray config not found")
            failures.append("xray config not found")
        else:
            cfg = json.load(open(cfgpath))
            print("policy levels:", json.dumps(cfg.get("policy", {}).get("levels", {}), sort_keys=True))
            obs = [o for o in cfg.get("outbounds", []) if o.get("tag", "").startswith("flmx-idle")]
            for o in obs:
                print("  idle-ob", o.get("tag"), o.get("settings"))
            for i in cfg.get("inbounds", []):
                if i.get("port") in ports:
                    cl = i.get("settings", {}).get("clients", [])
                    print("  inbound port=%s proto=%s client-levels=%s top-level=%s" % (
                        i.get("port"), i.get("protocol"), [c.get("level") for c in cl],
                        i.get("settings", {}).get("level")))

    print("=== leg %s end failures=%d %s ===" % (leg, len(failures), failures))
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
