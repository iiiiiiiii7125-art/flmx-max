# Turns the raw recording into src/demo/fixtures.json: canonical request
# keys, and the agent-side results a recording without agents lacks
# (latency, return routes, kernel state) filled with plausible values.
#
# Sensitive-key gate (must stay): config rows whose name is one of the
# backend's sensitive config keys (config_policy.go is the source of truth;
# mirrored in src/config/sensitive-keys.ts) are blanked before anything is
# written, every redaction is reported, and a final verification pass
# FAILS the build (no output written) if any sensitive key still carries
# a value or a denylisted substring appears. Extra denylist entries can be
# supplied by the operator via DEMO_FIXTURE_DENYLIST (comma separated) or
# a local, uncommitted denylist.local.txt next to this script — real
# infrastructure identifiers must never be committed to the repo list.
import json, sys, hashlib, os
raw = json.load(open(sys.argv[1]))
out_path = sys.argv[2]

SENSITIVE_KEYS = {
    "jwt_secret", "license_key", "license_expiry", "license_machine_id",
    "machine_fingerprint", "cloudflare_secret_key", "node_sub_token",
    "notify_telegram_bot_token", "notify_webhook_url",
}
DENYLIST = [s.strip() for s in os.environ.get("DEMO_FIXTURE_DENYLIST", "").split(",") if s.strip()]
_local = os.path.join(os.path.dirname(os.path.abspath(__file__)), "denylist.local.txt")
if os.path.exists(_local):
    DENYLIST += [ln.strip() for ln in open(_local) if ln.strip() and not ln.startswith("#")]

redacted = []
def redact(v):
    if isinstance(v, dict):
        if v.get("name") in SENSITIVE_KEYS and "value" in v:
            if str(v.get("value") or "") != "":
                redacted.append(v["name"])
            v = {**v, "value": ""}
        return {k: redact(x) for k, x in v.items()}
    if isinstance(v, list):
        return [redact(x) for x in v]
    return v

violations = []
def verify(v, where=""):
    if isinstance(v, dict):
        for k, x in v.items():
            if k in SENSITIVE_KEYS and str(x or "") != "":
                violations.append(f"sensitive key {k!r} still has a value at {where}")
            verify(x, f"{where}.{k}")
        if v.get("name") in SENSITIVE_KEYS and str(v.get("value") or "") != "":
            violations.append(f"config row {v['name']!r} still has a value at {where}")
    elif isinstance(v, list):
        for i, x in enumerate(v):
            verify(x, f"{where}[{i}]")
    elif isinstance(v, str):
        for d in DENYLIST:
            if d in v:
                violations.append(f"denylisted substring {d!r} at {where}")

BASE = {"香港 HKT": 36, "日本 东京": 58, "美国 圣何塞": 152, "新加坡": 71, "上海 移动中转": 8, "广州 电信中转": 6}
# Route names as the panel sends them (kernel classifyRoute).
_FULL = {"CN2 GIA": "电信 CN2 GIA", "CN2 GT": "电信 CN2 GT", "163": "电信 163", "9929": "联通 9929", "4837": "联通 4837", "CMIN2": "移动 CMIN2", "CMI": "移动 CMI"}
ROUTES = {node: {p: _FULL[r] for p, r in pts.items()} for node, pts in {
    "香港 HKT": {"北京电信": "CN2 GIA", "上海电信": "CN2 GIA", "广东电信": "CN2 GIA", "上海联通": "9929", "北京联通": "9929", "广东移动": "CMIN2"},
    "日本 东京": {"北京电信": "CN2 GT", "上海电信": "CN2 GT", "广东电信": "163", "上海联通": "4837", "北京联通": "4837", "广东移动": "CMI"},
    "美国 圣何塞": {"北京电信": "CN2 GIA", "上海电信": "CN2 GIA", "广东电信": "CN2 GIA", "上海联通": "9929", "北京联通": "4837", "广东移动": "CMIN2"},
    "新加坡": {"北京电信": "163", "上海电信": "163", "广东电信": "163", "上海联通": "4837", "北京联通": "4837", "广东移动": "CMI"},
}.items()}
# A dual-stack node's IPv6 routes. Four Tokyo points differ from IPv4
# (the card shows the small v6 tag); 广东移动 deliberately equals its
# IPv4 route (移动 CMI) so the "leg exists but same route" form — tag
# hidden — is in the data too.
ROUTES_V6 = {"日本 东京": {"北京电信": "电信 CN2 GIA", "上海电信": "电信 163", "广东电信": "电信 CN2 GIA", "上海联通": "联通 9929", "广东移动": "移动 CMI"}}
POINT_IDX = {"北京电信": 0, "上海联通": 1, "广东移动": 2, "北京联通": 3, "上海电信": 4, "广东电信": 5}

def jitter(*parts):
    return int(hashlib.md5("|".join(map(str, parts)).encode()).hexdigest()[:4], 16) / 65535

def fake_points(points, node):
    base = BASE.get(node, 60)
    out = []
    for p in points:
        name = p["name"]; i = POINT_IDX.get(name, 0)
        q = {"name": name, "ok": True, "latencyMs": int(base * (1 + 0.07 * i) * (0.92 + 0.2 * jitter(node, name)))}
        if p.get("pinned"): q["pinned"] = True
        if (node, name) in {("美国 圣何塞", "北京联通"), ("新加坡", "广东移动")}: q["lossPct"] = 2
        r = ROUTES.get(node, {}).get(name)
        if r: q["route"] = r
        r6 = ROUTES_V6.get(node, {}).get(name)
        if r6: q["routeV6"] = r6
        out.append(q)
    return out

def fix_probe(probe, node):
    if not isinstance(probe, dict): return probe
    if probe.get("points"):
        probe["points"] = fake_points(probe["points"], node)
        probe["ok"] = True
        probe.pop("error", None)
    elif not probe.get("skipped"):
        probe.update({"ok": True, "latencyMs": int(40 + 60 * jitter(node, "panel"))}); probe.pop("error", None)
    return probe

def canon(body):
    if not body: return ""
    try: return json.dumps(json.loads(body), sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    except Exception: return body

node_names = {}
for e in raw["admin"].get("node/list", []):
    for n in e["res"].get("data") or []:
        node_names[n["id"]] = n["name"]

def hops(node, name, route):
    net = {"电信 CN2 GIA": "CN2", "电信 CN2 GT": "CN2", "电信 163": "163", "联通 9929": "9929", "联通 4837": "4837", "移动 CMIN2": "CMI", "移动 CMI": "CMI"}.get(route, "")
    base = BASE.get(node, 60)
    hs = [{"ttl": 1, "ip": "10.0.0.1", "rttMs": 0.4}, {"ttl": 2, "ip": "203.0.113.254", "rttMs": 1.1}]
    for k in range(3, 9):
        hs.append({"ttl": k, "ip": f"59.43.{40 + k}.{int(200 * jitter(node, name, k))}", "rttMs": round(base * k / 8, 1), **({"net": net} if net and 3 <= k <= 6 else {})})
    return hs

def hops6(node, name, route):
    net = {"电信 CN2 GIA": "CN2", "电信 163": "CT", "联通 9929": "9929", "联通 4837": "4837", "移动 CMI": "CMI"}.get(route, "")
    pre = {"CN2": "2400:9380:9021", "CT": "240e:0:a", "9929": "2408:8120:2", "4837": "2408:8000:9000", "CMI": "2402:4f00:1000"}.get(net, "2001:db8")
    base = BASE.get(node, 60)
    hs = [{"ttl": 1, "ip": "2001:db8:100::1", "rttMs": 0.5}, {"ttl": 2, "ip": "2001:db8:ff::fe", "rttMs": 1.3}]
    for k in range(3, 9):
        hs.append({"ttl": k, "ip": f"{pre}::{k:x}", "rttMs": round(base * k / 8 + 2 * jitter(node, name, k), 1), **({"net": net} if net and 3 <= k <= 6 else {})})
    return hs

for role, paths in raw.items():
    if role == "recordedAt": continue
    for path, entries in paths.items():
        for e in entries:
            res = e["res"]; data = res.get("data")
            if path == "node/list" and isinstance(data, list):
                for n in data:
                    n["status"] = 1
                    n["latency"] = fix_probe(n.get("latency"), n["name"])
                    n["version"] = n.get("version") or "1.1.1"
            if path == "inbound/list" and isinstance(data, list):
                for i in data:
                    i["probe"] = fix_probe(i.get("probe"), i.get("nodeName") if not i.get("external") else i["name"])
                    if not i.get("external"): i["kernelStatus"] = "running"
            if path == "inbound/relay/list" and isinstance(data, list):
                for rl in data:
                    lat = rl.get("latency")
                    if not isinstance(lat, dict): continue
                    entry = lat.get("entry", "")
                    land = rl["name"].split("→")[-1].strip()
                    landing = {"香港": "香港 HKT", "东京": "日本 东京", "圣何塞": "美国 圣何塞", "新加坡": "新加坡"}.get(land, "香港 HKT")
                    leg = int(BASE.get(landing, 50) * 0.55 + 4)
                    lat["hops"] = [{**h, "ok": True, "latencyMs": leg} for h in lat.get("hops", [])]
                    lat.pop("error", None)
                    lat["ok"] = True
                    lat["pathMs"] = leg
                    entry_base = BASE.get(entry, 8)
                    pts = []
                    for p in lat.get("points", []):
                        i = POINT_IDX.get(p["name"], 0)
                        q = {"name": p["name"], "ok": True, "latencyMs": int(entry_base * (1 + 0.3 * i) * (0.9 + 0.2 * jitter(entry, p["name"]))) + leg}
                        if p.get("pinned"): q["pinned"] = True
                        pts.append(q)
                    lat["points"] = pts
            if path.startswith("monitor/services") and isinstance(data, list):
                for m in data:
                    if isinstance(m, dict) and "success" in m and m.get("errorMessage") == "节点不在线":
                        m.update({"success": 1, "latencyMs": round(12 + 30 * jitter("svc", m.get("monitorId")), 1), "errorMessage": ""})
            if path == "kernel/list" and isinstance(data, list):
                for k in data:
                    k.update({"status": "running", "nodeOnline": True, "synced": True, "lastError": ""})
            if path == "node/route":
                nid = json.loads(e["body"] or "{}").get("id")
                node = node_names.get(nid, "")
                if data and ROUTES.get(node):
                    data["points"] = [{"name": p["name"], "route": ROUTES[node].get(p["name"]), "reached": True,
                                       "hops": hops(node, p["name"], ROUTES[node].get(p["name"]))} for p in data["points"]]
                    for p in data["points"]:
                        r6 = ROUTES_V6.get(node, {}).get(p["name"]) or (p["route"] if node in ROUTES_V6 else None)
                        if r6: p["ipv6"] = {"name": p["name"], "route": r6, "reached": True, "family": "IPv6", "hops": hops6(node, p["name"], r6)}
                elif data:
                    data["points"] = [{"name": p["name"], "reached": True, "hops": hops(node, p["name"], "")} for p in data["points"]]
            if path == "kernel/logs":
                res.update({"code": 0, "msg": "操作成功", "data": {"type": "", "running": True, "enabled": True, "pid": 2817, "version": "",
                            "error": "", "restarts": 0, "startedAt": raw["recordedAt"] - 86400000 * 3,
                            "logs": ["[Info] started", "[Info] inbound listening on [::]:443", "[Info] accepted tcp connection"]}})
            e["key"] = canon(e.pop("body"))

fixtures = {"recordedAt": raw["recordedAt"], "admin": raw["admin"], "user": raw["user"]}
fixtures = redact(fixtures)
if redacted:
    print("redacted sensitive config rows:", ", ".join(sorted(set(redacted))), f"({len(redacted)}x)")
verify(fixtures)
if violations:
    print("SENSITIVE GATE FAILED — fixtures NOT written:")
    for v in violations[:20]:
        print(" -", v)
    sys.exit(1)

# Universal recorded-code gate (R9 BINFRA, J O-1 通则; R9-X-verdicts §4):
# EVERY recorded response in EVERY book must carry res.code == 0. A
# transient failure frozen into any cell — not just the content-gate
# keys below — otherwise ships verbatim in the demo (R8X-006 froze a
# GitHub 403 into kernel/versions; R9-J §2.3 proved by mutation that
# off-list live keys such as system/check-updates, system/version and
# node/releases passed both old gates with a frozen failure). The
# only exceptions are the per-cell whitelist below: intentional
# recordings of an honest failure answer, enumerated one cell at a
# time as (book, path, canonical request key, code, msg), sourced
# from the R9-J §2.1 full-set scan (13 cells, R9-J-fixtures-full-
# probe.log). Adding or removing a cell requires a per-cell reason
# recorded in the comment above that entry; whole-class exemptions
# (by path alone, by code alone, by msg alone) are FORBIDDEN. The
# same gate runs against the committed fixtures in
# check-fixtures-content.mjs — keep both lists cell-for-cell equal.
CODE_WHITELIST = [
    # — config/get 配置不存在 (code -1, msg 配置不存在): the seed panel
    # never set these preferences; the backend's honest answer, and
    # the pages render their defaults. One reason per cell:
    # admin: forward-page group collapse preference, never set.
    ("admin", "config/get", '{"name":"forward_group_collapsed_map"}', -1, "配置不存在"),
    # admin: forward-page group order preference, never set (the R8
    # O-2 cell; observation status unchanged, see R9-J §6 O-2).
    ("admin", "config/get", '{"name":"forward_group_order_map"}', -1, "配置不存在"),
    # admin: forward-page compact-mode preference, never set.
    ("admin", "config/get", '{"name":"forward_compact_mode"}', -1, "配置不存在"),
    # user: forward-page compact-mode preference, never set.
    ("user", "config/get", '{"name":"forward_compact_mode"}', -1, "配置不存在"),
    # user: tunnel-quality monitor enable preference, never set.
    ("user", "config/get", '{"name":"monitor_tunnel_quality_enabled"}', -1, "配置不存在"),
    # user: tunnel-quality monitor interval preference, never set.
    ("user", "config/get", '{"name":"monitor_tunnel_quality_interval_sec"}', -1, "配置不存在"),
    # user: forward-page group collapse preference, never set.
    ("user", "config/get", '{"name":"forward_group_collapsed_map"}', -1, "配置不存在"),
    # — user-book 403 permission probes (code 403, msg 权限不足，
    # 仅管理员可操作): the demo user book deliberately records the
    # requiresAdmin denial for admin-only surfaces, proving the
    # permission boundary; one reason per cell:
    # user probe of the admin-only update check (stable channel).
    ("user", "system/check-updates", '{"channel":"stable"}', 403, "权限不足，仅管理员可操作"),
    # user probe of the admin-only speed-limit list.
    ("user", "speed-limit/list", "{}", 403, "权限不足，仅管理员可操作"),
    # user probe of the admin-only tunnel list.
    ("user", "tunnel/list", "{}", 403, "权限不足，仅管理员可操作"),
    # user probe of the admin-only node list.
    ("user", "node/list", "{}", 403, "权限不足，仅管理员可操作"),
    # user probe of the admin-only federation share list.
    ("user", "federation/share/list", "{}", 403, "权限不足，仅管理员可操作"),
    # user probe of the admin-only federation remote-usage list.
    ("user", "federation/share/remote-usage/list", "{}", 403, "权限不足，仅管理员可操作"),
]
CODE_WHITELIST_SET = set(CODE_WHITELIST)
code_violations = []
for book in ("admin", "user"):
    for path_, entries in (fixtures.get(book) or {}).items():
        for i, e in enumerate(entries or []):
            res = e.get("res") or {}
            code = res.get("code")
            if code == 0:
                continue
            cell = (book, path_, e.get("key") or "", code, res.get("msg") or "")
            if cell not in CODE_WHITELIST_SET:
                code_violations.append(
                    f"{book} {path_} recording #{i} key={cell[2]}: res.code={code} "
                    f"msg={cell[4]!r} is not code 0 and not a whitelisted cell "
                    "(per-cell whitelist only — see CODE_WHITELIST above)")
if code_violations:
    print("FIXTURE CODE GATE FAILED — fixtures NOT written:")
    for v in code_violations[:20]:
        print(" -", v)
    sys.exit(1)
print(f"code gate ok: every recorded response is code 0 except the {len(CODE_WHITELIST)} whitelisted cells")

# Content gate (R7X-006): key-set existence is not a reconciliation.
# A re-record whose seed never produced the underlying fact passes a
# key-set diff while washing a key's payload back to empty — that is
# how node/traffic/month lost its 6-node demo data in the R6 re-record
# (the seed wrote no node_traffic rows, so the panel's honest answer
# was {}). The keys below must carry non-empty content in EVERY
# recording, or the fixtures are not written. The same list is checked
# against the committed fixtures by check-fixtures-content.mjs.
CONTENT_KEYS = [
    ("admin", "node/traffic/month", "month-map"),
    ("admin", "subscription/traffic", "series"),
    ("user", "user/subscription/traffic", "series"),
    # R8X-006: kernel/versions xray/singbox cells must be a successful
    # version list, never a recorded failure response (the 1.4.4
    # re-record froze a transient GitHub 403 into both cells).
    ("admin", "kernel/versions", "version-list"),
    # R15-X-02: backup/gist/status must carry the declared
    # GistBackupStatus shape (configured/tokenHint/encrypted/
    # intervalHours/gistUrl/lastAt/lastUpload/lastError) with code 0.
    # A fresh seed honestly answers configured=false with empty
    # tokenHint/gistUrl — emptiness is the fact here, so the gate
    # checks shape, not non-emptiness.
    ("admin", "backup/gist/status", "gist-status"),
]
VERSION_LIST_TYPES = {"xray", "singbox"}
GIST_STATUS_FIELDS = {
    "configured": bool, "tokenHint": str, "encrypted": bool,
    "intervalHours": int, "gistUrl": str, "lastAt": int,
    "lastUpload": int, "lastError": str,
}
content_violations = []
for book, path_, kind in CONTENT_KEYS:
    entries = (fixtures.get(book) or {}).get(path_)
    if not entries:
        content_violations.append(f"{book} {path_}: key missing or no recordings")
        continue
    if kind == "gist-status":
        for i, e in enumerate(entries):
            res = e.get("res") or {}
            data = res.get("data")
            if res.get("code") != 0:
                content_violations.append(f"{book} {path_} recording #{i}: res.code={res.get('code')} is a recorded failure response, not a GistBackupStatus")
                continue
            if not isinstance(data, dict):
                content_violations.append(f"{book} {path_} recording #{i}: data is not a GistBackupStatus object")
                continue
            bad = [k for k, t in GIST_STATUS_FIELDS.items()
                   if k not in data or not isinstance(data[k], t) or isinstance(data[k], bool) != (t is bool)]
            if bad:
                content_violations.append(f"{book} {path_} recording #{i}: data misses or mistypes GistBackupStatus field(s): {', '.join(bad)}")
        continue
    if kind == "version-list":
        seen = set()
        for i, e in enumerate(entries):
            try:
                kernel_type = json.loads(e.get("key") or "{}").get("kernelType", "")
            except Exception:
                kernel_type = ""
            if kernel_type not in VERSION_LIST_TYPES:
                continue
            seen.add(kernel_type)
            res = e.get("res") or {}
            data = res.get("data")
            if res.get("code") != 0:
                content_violations.append(f"{book} {path_} recording #{i} (kernelType={kernel_type}): res.code={res.get('code')} is a recorded failure response (msg={res.get('msg', '')!r}), not a version list")
            elif not isinstance(data, list) or len(data) < 1:
                content_violations.append(f"{book} {path_} recording #{i} (kernelType={kernel_type}): data is not a non-empty version list")
            elif any(not isinstance(v, dict) or not str(v.get("version") or "") for v in data):
                content_violations.append(f"{book} {path_} recording #{i} (kernelType={kernel_type}): a version entry has no non-empty version string")
        for t in sorted(VERSION_LIST_TYPES - seen):
            content_violations.append(f"{book} {path_}: no recording for kernelType={t}")
        continue
    for i, e in enumerate(entries):
        data = (e.get("res") or {}).get("data")
        if kind == "month-map":
            if not isinstance(data, dict) or len(data) < 1:
                content_violations.append(f"{book} {path_} recording #{i}: data is an empty node map (0 nodes)")
        else:
            pts = data.get("points") if isinstance(data, dict) else None
            if not isinstance(pts, list) or len(pts) < 1:
                content_violations.append(f"{book} {path_} recording #{i}: data.points is empty")
if content_violations:
    print("FIXTURE CONTENT GATE FAILED — fixtures NOT written:")
    for v in content_violations[:20]:
        print(" -", v)
    sys.exit(1)
print("content gate ok:", ", ".join(f"{b} {p}" for b, p, _ in CONTENT_KEYS))
json.dump(fixtures, open(out_path, "w"), ensure_ascii=False, separators=(",", ":"))
print("written", out_path)
