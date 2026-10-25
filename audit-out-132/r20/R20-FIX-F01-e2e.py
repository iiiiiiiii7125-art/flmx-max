#!/usr/bin/env python3
"""R20-FIX-F01 真机端到端：更新路径孤儿 climiter（R20-X-01）。

在测试机上运行（python3 /root/r20-f01/e2e.py < 凭据两行：用户名、密码）。
凭据只走 stdin，不写盘。面板基座 http://127.0.0.1:6366（前端 nginx 反代 /api）。

腿：
  A. 建带限流转发（tunnel 1，remote 203.0.113.9:9001，maxConn=5）
     → gost 新增 rule_conn_limit_<id> 且服务带该引用（对照 L2 转发 32）。
  B. 更新去限流 {id, maxConn:0, ipMaxConn:0} → code 0；
     断言 ①：gost 里 rule_conn_limit_<id> 已消失（服务仍在、无引用），
     且 agent journal 自更新起含 DeleteCLimiters <id>。
  C. 删除该转发 → code 0；
     断言 ②：gost 回 pristine（服务 12、climiters 仅存量两项），
     journal 里 DeleteCLimiters <id> 再出现一次（删除兜底按 ID 名发）。
  D. 直接删对照（L2 转发 33 同款）：新建带限流转发 → 直接删 →
     恰 1 次 DeleteCLimiters <id2>，gost 回 pristine。

存量孤儿 rule_conn_limit_2 / rule_conn_limit_12（F-04 范围）全程在位，
不属本项断言，脚本只断言“新增的 <id> 不见了、存量两项还在”。
"""
import json
import subprocess
import sys
import time
import urllib.request

BASE = "http://127.0.0.1:6366/api/v1"
GOST = "/etc/flux_agent/gost.json"
PRISTINE_CLIMITERS = {"rule_conn_limit_2", "rule_conn_limit_12"}

failures = []


def check(name, cond, detail=""):
    print(("PASS " if cond else "FAIL ") + name + ((" :: " + str(detail)) if detail else ""))
    if not cond:
        failures.append(name)


def call(path, body, token=None):
    req = urllib.request.Request(
        BASE + path,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json",
                 **({"Authorization": token} if token else {})},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return resp.status, json.loads(resp.read().decode())


def gost():
    with open(GOST) as f:
        data = json.load(f)
    services = sorted(s.get("name", "") for s in data.get("services", []))
    climiters = sorted(c.get("name", "") for c in data.get("climiters", []))
    refs = sorted({s.get("climiter") for s in data.get("services", []) if s.get("climiter")})
    return services, climiters, refs


def journal_delete_climiters(name, since):
    out = subprocess.run(
        ["journalctl", "-u", "flux_agent", "--since", since, "--no-pager"],
        capture_output=True, text=True, check=False).stdout
    return [ln for ln in out.splitlines()
            if "DeleteCLimiters" in ln and name in ln]


def forward_items(token):
    # forwardList 回 response.OK(items)：data 就是列表本身（非 {"list": [...]}）。
    _, data = call("/forward/list", {}, token)
    items = data.get("data", [])
    if isinstance(items, dict):
        items = items.get("list", [])
    return items if isinstance(items, list) else []


def forward_id_by_name(token, name):
    for it in forward_items(token):
        if it.get("name") == name:
            return it.get("id")
    return None


def forward_count(token):
    return len(forward_items(token))


def main():
    print("=== R20-FIX-F01 e2e start=%s ===" % time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()))
    user = sys.stdin.readline().strip()
    pw = sys.stdin.readline().strip()
    if not user or not pw:
        print("FAIL need username+password on stdin (2 lines)")
        sys.exit(2)
    http, login = call("/user/login", {"username": user, "password": pw})
    token = (login.get("data") or {}).get("token") or login.get("data")
    if isinstance(token, dict):
        token = token.get("token")
    check("login", http == 200 and login.get("code") == 0 and token,
          "http=%s code=%s" % (http, login.get("code")))
    if not token:
        print("=== e2e ABORT ===")
        sys.exit(2)

    # --- pre-state
    _, nodes = call("/node/list", {}, token)
    node_items = nodes.get("data", [])
    if isinstance(node_items, dict):
        node_items = node_items.get("list", [])
    node1 = [n for n in node_items if n.get("id") == 1]
    print("pre nodes: %s" % [(n.get("id"), n.get("name"), n.get("status")) for n in node_items])
    check("pre node 1 online (status 1)", len(node1) == 1 and node1[0].get("status") == 1,
          node1)
    services, climiters, refs = gost()
    print("pre: services=%d climiters=%s refs=%s" % (len(services), climiters, refs))
    check("pre climiters are pristine stock only",
          set(climiters) == PRISTINE_CLIMITERS, climiters)
    check("pre forward count 6", forward_count(token) == 6, forward_count(token))
    if failures:
        print("=== e2e ABORT (pre-state not pristine) failures=%s ===" % failures)
        sys.exit(2)

    # --- leg A: create with limits
    name = "r20-f01-e2e"
    http, resp = call("/forward/create",
                     {"name": name, "tunnelId": 1, "remoteAddr": "203.0.113.9:9001",
                      "strategy": "fifo", "maxConn": 5, "ipMaxConn": 0}, token)
    check("A create code 0", http == 200 and resp.get("code") == 0,
          "http=%s code=%s msg=%s" % (http, resp.get("code"), resp.get("msg")))
    fid = forward_id_by_name(token, name)
    check("A forward id found", fid is not None, fid)
    if fid is None:
        print("=== e2e ABORT ===")
        sys.exit(2)
    want = "rule_conn_limit_%d" % fid
    time.sleep(2)
    services, climiters, refs = gost()
    print("A gost: services=%d climiters=%s refs=%s" % (len(services), climiters, refs))
    check("A gost has new limiter", want in climiters, climiters)
    check("A service refs new limiter", want in refs, refs)

    # --- leg B: update removes limits (fix item 1)
    since_b = time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime())
    http, resp = call("/forward/update", {"id": fid, "maxConn": 0, "ipMaxConn": 0}, token)
    check("B update code 0", http == 200 and resp.get("code") == 0,
          "http=%s code=%s msg=%s" % (http, resp.get("code"), resp.get("msg")))
    time.sleep(2)
    services, climiters, refs = gost()
    print("B gost: services=%d climiters=%s refs=%s" % (len(services), climiters, refs))
    check("B stale limiter gone from gost", want not in climiters, climiters)
    check("B service still present", any(s.startswith("%d_" % fid) for s in services), services)
    check("B no service refs stale limiter", want not in refs, refs)
    hits_b = journal_delete_climiters(want, since_b)
    print("B journal DeleteCLimiters %s: %d" % (want, len(hits_b)))
    for ln in hits_b:
        print("   " + ln.strip()[:300])
    check("B journal has DeleteCLimiters after update", len(hits_b) >= 1, len(hits_b))

    # --- leg C: delete after update (fix item 2 backstop)
    since_c = time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime())
    http, resp = call("/forward/delete", {"id": fid}, token)
    check("C delete code 0", http == 200 and resp.get("code") == 0,
          "http=%s code=%s msg=%s" % (http, resp.get("code"), resp.get("msg")))
    time.sleep(2)
    services, climiters, refs = gost()
    print("C gost: services=%d climiters=%s refs=%s" % (len(services), climiters, refs))
    check("C gost back to pristine climiters", set(climiters) == PRISTINE_CLIMITERS, climiters)
    check("C forward count back to 6", forward_count(token) == 6, forward_count(token))
    hits_c = journal_delete_climiters(want, since_c)
    print("C journal DeleteCLimiters %s: %d" % (want, len(hits_c)))
    for ln in hits_c:
        print("   " + ln.strip()[:300])
    check("C journal has DeleteCLimiters after delete", len(hits_c) >= 1, len(hits_c))

    # --- leg D: direct-delete control (L2 forward 33 style)
    name2 = "r20-f01-ctrl"
    http, resp = call("/forward/create",
                     {"name": name2, "tunnelId": 1, "remoteAddr": "203.0.113.9:9001",
                      "strategy": "fifo", "maxConn": 5, "ipMaxConn": 0}, token)
    check("D create code 0", http == 200 and resp.get("code") == 0,
          "http=%s code=%s msg=%s" % (http, resp.get("code"), resp.get("msg")))
    fid2 = forward_id_by_name(token, name2)
    check("D forward id found", fid2 is not None, fid2)
    if fid2 is not None:
        want2 = "rule_conn_limit_%d" % fid2
        time.sleep(2)
        since_d = time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime())
        http, resp = call("/forward/delete", {"id": fid2}, token)
        check("D delete code 0", http == 200 and resp.get("code") == 0,
              "http=%s code=%s msg=%s" % (http, resp.get("code"), resp.get("msg")))
        time.sleep(2)
        services, climiters, refs = gost()
        print("D gost: services=%d climiters=%s refs=%s" % (len(services), climiters, refs))
        check("D gost back to pristine climiters", set(climiters) == PRISTINE_CLIMITERS, climiters)
        check("D forward count back to 6", forward_count(token) == 6, forward_count(token))
        hits_d = journal_delete_climiters(want2, since_d)
        print("D journal DeleteCLimiters %s: %d" % (want2, len(hits_d)))
        check("D journal has DeleteCLimiters after direct delete", len(hits_d) >= 1, len(hits_d))

    print("=== e2e end failures=%d %s ===" % (len(failures), failures))
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
