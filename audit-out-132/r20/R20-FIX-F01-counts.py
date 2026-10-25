#!/usr/bin/env python3
"""R20-FIX-F01 离场计数对账：node/tunnel/forward/inbound/subscription 列表计数。
在测试机上运行，凭据走 stdin（用户名、密码两行）。期望 1/4/6/578/18。"""
import json
import sys
import urllib.request

BASE = "http://127.0.0.1:6366/api/v1"
EXPECT = {"node": 1, "tunnel": 4, "forward": 6, "inbound": 578, "subscription": 18}


def call(path, body, token=None):
    req = urllib.request.Request(
        BASE + path, data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json",
                 **({"Authorization": token} if token else {})}, method="POST")
    with urllib.request.urlopen(req, timeout=30) as resp:
        return resp.status, json.loads(resp.read().decode())


def count(path, token):
    _, data = call(path, {}, token)
    items = data.get("data", [])
    if isinstance(items, dict):
        # subscription/list 回 {"items": [...]}，其余列表回裸数组。
        items = items.get("list", items.get("items", []))
    return len(items) if isinstance(items, list) else -1


def main():
    user = sys.stdin.readline().strip()
    pw = sys.stdin.readline().strip()
    _, login = call("/user/login", {"username": user, "password": pw})
    token = (login.get("data") or {}).get("token")
    if not token:
        print("FAIL login")
        sys.exit(2)
    bad = []
    for kind, path in [("node", "/node/list"), ("tunnel", "/tunnel/list"),
                       ("forward", "/forward/list"), ("inbound", "/inbound/list"),
                       ("subscription", "/subscription/list")]:
        n = count(path, token)
        ok = n == EXPECT[kind]
        print(("PASS " if ok else "FAIL ") + "%s=%d expect=%d" % (kind, n, EXPECT[kind]))
        if not ok:
            bad.append(kind)
    # forward 全为 active（status 1）
    _, data = call("/forward/list", {}, token)
    items = data.get("data", [])
    statuses = sorted({it.get("status") for it in items})
    print("forward statuses=%s" % statuses)
    if statuses != [1]:
        bad.append("forward-status")
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
