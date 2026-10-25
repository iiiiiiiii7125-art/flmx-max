#!/usr/bin/env python3
"""R21-F02 真机腿：离线更新的孤儿在重连重推时被清理（选 ①）。

在测试机上以 root 运行：python3 /root/r21-f02/e2e-f02.py < 凭据两行。
面板基座 http://127.0.0.1:6366（前端 nginx 反代 /api）；面板为本次
fix-r21-f02 构建的 paneld（见部署日志）；agent 不动（F-04 二进制，
其自愈只在 Start/SIGHUP 时跑——见 program.go healPersistedConfig，
UpdateService 路径不触发，故不能掩盖本腿；腿内另 grep 自愈行以便归因）。

与 R21-X-02 立案 Leg B 的差别（独立新证据）：同一序列，预期反转——
重连重推后 journal 须含面板下发的 DeleteCLimiters rule_conn_limit_<id>，
gost 里该 limiter 须消失；立案时该计数为 0、limiter 仍在。

腿：
  A. 建带限流转发（腿内新建的 type-1 隧道，remote 203.0.113.9:9001，maxConn=5）
     → gost 新增 rule_conn_limit_<id> 且服务带该引用。
  B. 停 agent（systemctl stop flux_agent，等节点 status 0）→ 离线更新
     {id, maxConn:0, ipMaxConn:0} → code 0 且回执含“不在线”警告；
     断言：gost md5 不变、limiter 仍在且仍被引用（服务未更新）。
  C. 重启 agent（reset-failed 后 start，等节点 status 1）→ 重连重推；
     轮询 journal 至多 150s（含 30s onNodeOnline 冷却排队）；
     断言 ①：窗口内 journal 含 DeleteCLimiters <id>（面板命令）；
     断言 ②：gost 里 <id> 已消失、无服务引用它；
     归因：窗口内 "startup config self-heal" 行若提及 <id> 则打印（
     F-04 在启动时只剪无引用 limiter，而启动时旧服务仍引用 <id>，
     故启动自愈不应剪它；UpdateService 路径亦不触发自愈）。
  D. 删除该转发 → code 0；gost 回腿起跑 climiter 集，forward 计数回
     腿起跑数。
全程：user_conn_limit_* 不得出现于任何 DeleteCLimiters 行。
"""
import hashlib
import json
import subprocess
import sys
import time
import urllib.request

BASE = "http://127.0.0.1:6366/api/v1"
GOST = "/etc/flux_agent/gost.json"

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
    # 删光转发后 gost.json 里 services/climiters 可能为 null（非 []）。
    services = sorted(s.get("name", "") for s in (data.get("services") or []))
    climiters = sorted(c.get("name", "") for c in (data.get("climiters") or []))
    refs = sorted({s.get("climiter") for s in (data.get("services") or []) if s.get("climiter")})
    return services, climiters, refs


def gost_md5():
    with open(GOST, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def journal_lines(pattern, since):
    out = subprocess.run(
        ["journalctl", "-u", "flux_agent", "--since", since, "--no-pager"],
        capture_output=True, text=True, check=False).stdout
    return [ln for ln in out.splitlines() if pattern in ln]


def forward_items(token):
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


def tunnel_items(token):
    _, data = call("/tunnel/list", {}, token)
    items = data.get("data", [])
    if isinstance(items, dict):
        items = items.get("list", [])
    return items if isinstance(items, list) else []


def tunnel_id_by_name(token, name):
    for it in tunnel_items(token):
        if it.get("name") == name:
            return it.get("id")
    return None


def node_status(token, node_id=1):
    _, data = call("/node/list", {}, token)
    items = data.get("data", [])
    if isinstance(items, dict):
        items = items.get("list", [])
    for n in items:
        if n.get("id") == node_id:
            return n.get("status")
    return None


def wait_node_status(token, want, timeout_s, label):
    deadline = time.time() + timeout_s
    last = None
    while time.time() < deadline:
        try:
            last = node_status(token)
        except Exception as e:
            print("  %s poll err: %s" % (label, e))
        if last == want:
            return True
        time.sleep(3)
    print("  %s timeout: last status=%s want=%s" % (label, last, want))
    return False


def sh(args):
    return subprocess.run(args, capture_output=True, text=True, check=False)


def utcnow():
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def utc_hms():
    return time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime())


def main():
    print("=== R21-F02 e2e start=%s ===" % utcnow())
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

    check("pre node 1 online (status 1)", node_status(token) == 1, node_status(token))
    services, climiters, refs = gost()
    print("pre: services=%d climiters=%s refs=%s" % (len(services), climiters, refs))
    prestate = set(climiters)
    pre_count = len(forward_items(token))
    print("pre forward count=%d" % pre_count)
    # 新机面板为空库（0 隧道 / 0 转发）：先建一条 type-1（端口转发）
    # gost 隧道，入口节点 w10-test（id 1）；腿末删隧道恢复 pristine。
    pre_tunnels = tunnel_items(token)
    pre_tunnel_count = len(pre_tunnels)
    print("pre tunnel count=%d" % pre_tunnel_count)
    tunnel_id = tunnel_id_by_name(token, "r21-f02-tunnel")
    tunnel_created_here = False
    if tunnel_id is None:
        tunnel_created_here = True
        http, resp = call("/tunnel/create",
                         {"name": "r21-f02-tunnel", "type": 1,
                          "inNodeId": [{"nodeId": 1}], "outNodeId": [],
                          "chainNodes": [], "flow": 1, "trafficRatio": 1.0,
                          "status": 1}, token)
        check("pre tunnel create code 0", http == 200 and resp.get("code") == 0,
              "http=%s code=%s msg=%s" % (http, resp.get("code"), resp.get("msg")))
        tunnel_id = tunnel_id_by_name(token, "r21-f02-tunnel")
    check("pre tunnel id found", tunnel_id is not None, tunnel_id)
    if failures:
        print("=== e2e ABORT (pre-state) failures=%s ===" % failures)
        sys.exit(2)

    # --- leg A: create limited forward
    name = "r21-f02-e2e"
    http, resp = call("/forward/create",
                     {"name": name, "tunnelId": tunnel_id, "remoteAddr": "203.0.113.9:9001",
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

    # --- leg B: stop agent, offline update
    print("=== leg B: stop agent ===")
    r = sh(["systemctl", "stop", "flux_agent"])
    print("stop rc=%d %s" % (r.returncode, (r.stdout + r.stderr).strip()[:200]))
    ok_offline = wait_node_status(token, 0, 90, "node offline")
    check("B node goes offline (status 0)", ok_offline, node_status(token))
    md5_before = gost_md5()
    print("B gost md5 before offline update: %s" % md5_before)
    since_b = utc_hms()
    http, resp = call("/forward/update", {"id": fid, "maxConn": 0, "ipMaxConn": 0}, token)
    data = resp.get("data") or {}
    warnings = data.get("warnings") if isinstance(data, dict) else None
    check("B offline update code 0", http == 200 and resp.get("code") == 0,
          "http=%s code=%s msg=%s warnings=%s" % (http, resp.get("code"), resp.get("msg"), warnings))
    check("B offline update warns node offline",
          bool(warnings) and any("不在线" in str(w) for w in warnings), warnings)
    time.sleep(2)
    md5_after = gost_md5()
    services, climiters, refs = gost()
    print("B gost md5 after offline update: %s services=%d climiters=%s refs=%s"
          % (md5_after, len(services), climiters, refs))
    check("B gost md5 unchanged by offline update", md5_after == md5_before,
          "%s vs %s" % (md5_after, md5_before))
    check("B limiter still present offline", want in climiters, climiters)
    check("B service still refs limiter offline", want in refs, refs)

    # --- leg C: restart agent, reconnect redeploy
    print("=== leg C: restart agent ===")
    since_c = utc_hms()
    r = sh(["systemctl", "reset-failed", "flux_agent"])
    print("reset-failed rc=%d" % r.returncode)
    r = sh(["systemctl", "start", "flux_agent"])
    print("start rc=%d %s" % (r.returncode, (r.stdout + r.stderr).strip()[:200]))
    ok_online = wait_node_status(token, 1, 90, "node online")
    check("C node back online (status 1)", ok_online, node_status(token))
    print("C restart-since=%s (journal slice start)" % since_c)
    hits_c = []
    deadline = time.time() + 150
    while time.time() < deadline:
        hits_c = [ln for ln in journal_lines(want, since_c) if "DeleteCLimiters" in ln]
        if hits_c:
            break
        time.sleep(5)
    print("C journal DeleteCLimiters %s: %d" % (want, len(hits_c)))
    for ln in hits_c:
        print("   " + ln.strip()[:300])
    check("C journal has DeleteCLimiters after reconnect redeploy", len(hits_c) >= 1, len(hits_c))
    heal_lines = [ln for ln in journal_lines("self-heal", since_c) if want in ln]
    print("C self-heal lines mentioning %s: %d" % (want, len(heal_lines)))
    for ln in heal_lines:
        print("   HEAL " + ln.strip()[:300])
    time.sleep(2)
    services, climiters, refs = gost()
    print("C gost: services=%d climiters=%s refs=%s" % (len(services), climiters, refs))
    check("C stale limiter gone from gost", want not in climiters, climiters)
    check("C service still present", any(s.startswith("%d_" % fid) for s in services), services)
    check("C no service refs stale limiter", want not in refs, refs)

    # --- leg D: delete forward, restore pristine
    since_d = utc_hms()
    http, resp = call("/forward/delete", {"id": fid}, token)
    check("D delete code 0", http == 200 and resp.get("code") == 0,
          "http=%s code=%s msg=%s" % (http, resp.get("code"), resp.get("msg")))
    time.sleep(2)
    services, climiters, refs = gost()
    print("D gost: services=%d climiters=%s refs=%s" % (len(services), climiters, refs))
    check("D gost back to leg-start climiters", set(climiters) == prestate, climiters)
    check("D forward count back to pre", len(forward_items(token)) == pre_count,
          len(forward_items(token)))
    # 删腿内所用的隧道（新建者：计数回 pre；复用上轮残留者：计数为 pre-1，
    # 因该隧道本就是本 lane 上轮的残留、删它才是回 pristine）。
    http, resp = call("/tunnel/delete", {"id": tunnel_id}, token)
    check("D tunnel delete code 0", http == 200 and resp.get("code") == 0,
          "http=%s code=%s msg=%s" % (http, resp.get("code"), resp.get("msg")))
    expect_tunnels = pre_tunnel_count if tunnel_created_here else pre_tunnel_count - 1
    check("D tunnel count back to expected", len(tunnel_items(token)) == expect_tunnels,
          "got %d expect %d (pre %d, created_here %s)"
          % (len(tunnel_items(token)), expect_tunnels, pre_tunnel_count, tunnel_created_here))

    # --- user_conn_limit_* 全程未碰：整段 journal 内无该名前缀的 DeleteCLimiters
    all_hits = journal_lines("DeleteCLimiters", time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime(time.time() - 3600)))
    bad = [ln for ln in all_hits if "user_conn_limit_" in ln]
    print("user_conn_limit_ DeleteCLimiters lines in last hour: %d" % len(bad))
    check("no user_conn_limit_ DeleteCLimiters in window", len(bad) == 0, bad[:3])

    print("=== R21-F02 e2e end failures=%d %s ===" % (len(failures), failures))
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
