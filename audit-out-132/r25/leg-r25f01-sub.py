#!/usr/bin/env python3
"""R25-FIX F-01 live subscription leg (sandbox egress -> panel public URL).

Reads admin creds from ~/workspace/w10-run/panel-cred-new.txt at runtime.
Writes only token length / counts / sizes to stdout — never the password
or the subscription token itself.
Usage: leg-r25f01-sub.py <phase: pre|post> ; output goes to stdout (tee to log).
"""
import json
import sys
import urllib.request

CRED = "/home/hatch/workspace/w10-run/panel-cred-new.txt"


def load_cred():
    d = {}
    with open(CRED) as f:
        for line in f:
            line = line.strip()
            if not line or ":" not in line:
                continue
            k, v = line.split(":", 1)
            d[k.strip()] = v.strip()
    return d


def req(method, url, token=None, body=None):
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(url, data=data, method=method)
    r.add_header("Content-Type", "application/json")
    if token:
        r.add_header("Authorization", token)
    try:
        with urllib.request.urlopen(r, timeout=30) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def items_of(j):
    d = j.get("data")
    if isinstance(d, list):
        return d
    if isinstance(d, dict):
        return d.get("items") or d.get("list") or []
    return []


def main():
    phase = sys.argv[1]
    c = load_cred()
    base = c["panel_url"].rstrip("/")
    print(f"=== R25-FIX-F01 subscription leg phase={phase} base={base} ===")
    st, body = req("POST", base + "/api/v1/user/login",
                   body={"username": c["username"], "password": c["password"]})
    j = json.loads(body)
    print(f"login http={st} code={j.get('code')} msg={j.get('msg')}")
    token = j["data"]["token"]
    print(f"login token len={len(token)}")
    st, body = req("POST", base + "/api/v1/node/list", token, body={})
    j = json.loads(body)
    nodes = items_of(j)
    print(f"node/list: http={st} code={j.get('code')} count={len(nodes)} "
          f"serverIp={nodes[0].get('serverIp') if nodes else None}")
    st, body = req("POST", base + "/api/v1/inbound/list", token, body={})
    j = json.loads(body)
    ins = items_of(j)
    print(f"inbound/list: http={st} code={j.get('code')} count={len(ins)} "
          f"first={ins[0].get('name') if ins else None} port={ins[0].get('port') if ins else None}")
    counts = {}
    for fmt in ("", "clash", "singbox", "surge"):
        st, body = req("POST", base + "/api/v1/subscription/list", token,
                       body={"format": fmt} if fmt else {})
        j = json.loads(body)
        subs = items_of(j)
        nc = [s.get("nodeCount") for s in subs]
        counts[fmt or "default"] = nc
        print(f"subscription/list format={fmt or 'default'}: http={st} code={j.get('code')} nodeCount={nc}")
        if not fmt:
            tok = subs[0].get("token", "") if subs else ""
            print(f"default sub id={subs[0].get('id') if subs else None} tokenLen={len(tok)}")
    # bare token pulls (no auth, no user)
    tok = None
    st, body = req("POST", base + "/api/v1/subscription/list", token, body={})
    subs = items_of(json.loads(body))
    tok = subs[0].get("token", "") if subs else ""
    for fmt in ("clash", "singbox", "surge", ""):
        q = f"?format={fmt}" if fmt else ""
        st, body = req("GET", base + f"/api/v1/open_api/sub/{tok}{q}")
        has_vless = b"vless" in body.lower() or b"VLESS" in body
        print(f"bare-pull format={fmt or 'default'}: http={st} bytes={len(body)} has-vless-node={has_vless}")
        if fmt == "clash":
            print(f"bare-pull clash head: {body[:120]!r}")
    print(f"=== leg {phase} done ===")


if __name__ == "__main__":
    main()
