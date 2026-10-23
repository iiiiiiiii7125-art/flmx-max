"""Step 3: subscriptions + assignments in FLMX's own model, monitors,
24h of metrics / latency samples / traffic history, all online.

FLMX model (do NOT port upstream's): a subscription is a row in
`subscription`; who gets it lives in `user_subscription` (UserSubscription)
and is written through POST /user-subscription/set. There is no user_id on
the subscription and no template feature. The default subscription is the
panel's own auto-created one (first row by inx, id; personal ?user=&sig=
links hang on it).
"""
import json, sqlite3, sys, time, urllib.request, random, math, datetime
B = "http://127.0.0.1:16370/api/v1"; DB = sys.argv[1]; st = json.load(open(DB + ".seed.json"))
tok, ids, users = st["token"], st["ids"], st["users"]
now = int(time.time() * 1000); G = 1024 ** 3; DAY = 86400000
random.seed(7)
def api(path, body=None):
    req = urllib.request.Request(B + path, data=json.dumps(body or {}).encode(), method="POST",
                                 headers={"Content-Type": "application/json", "Authorization": tok})
    with urllib.request.urlopen(req) as r:
        res = json.load(r)
    if res.get("code") != 0:
        print("!!", path, res.get("msg"))
    return res
db = sqlite3.connect(DB); x = db.execute
inb = {r[1]: r[0] for r in x("SELECT id, name FROM node_inbound")}
relays = {r[1]: r[0] for r in x("SELECT id, name FROM node_inbound_relay")}

def sub(name, **kw):
    body = {"name": name, "kind": "own", "sourceAll": True, "inboundIds": [], "sourceIds": [],
            "options": {}, "expireTime": 0, "enabled": True, "remark": "", "trafficLimit": 0, "resetDay": 0}
    body.update(kw)
    res = api("/subscription/save", body)
    return res["data"]["id"]

# Created first on purpose among the seed's own subscriptions. Note the
# panel itself auto-creates its default subscription (我的订阅) at boot;
# DefaultSubscription() = first row by (inx, id), i.e. that one — the
# assignments below look it up instead of assuming it is s1.
s1 = sub("全部节点")
# relayIds: 0 = direct endpoints, >0 = node_inbound_relay ids; [] = all.
s2 = sub("小明 · 手机", options={"relayIds": [0, relays["上海移动 → 香港"]]},
         trafficLimit=200 * G, resetDay=1, expireTime=now + 60 * DAY)
s3 = sub("阿强 · 电脑", sourceAll=False,
         inboundIds=[inb["香港 Reality"], inb["东京 Reality"], inb["圣何塞 Hysteria2"]],
         trafficLimit=100 * G, resetDay=15, expireTime=now + 5 * DAY)
s4 = sub("机场备用", kind="share", sourceAll=False,
         inboundIds=[inb["机场 · 台湾 01"], inb["机场 · 韩国 01"], inb["机场 · 英国 01"]])
x("UPDATE subscription SET upload_bytes=?, download_bytes=? WHERE id=?", (6 * G, 62 * G, s2))
x("UPDATE subscription SET upload_bytes=?, download_bytes=? WHERE id=?", (9 * G, 83 * G, s3))
# Commit before the API calls below: an open python write transaction
# holds the SQLite write lock and the panel's own writes hit SQLITE_BUSY.
db.commit()
# Assignments in our model. The panel's default subscription (looked up
# by the backend's own rule: first by inx, id) goes to both recorded
# user personas: without it (and without a legacy inbound assignment)
# 我的订阅 pops a red "默认订阅未分配" toast from inbound/sub/info —
# S1 verified behaviour, kept out of the demo on purpose. lili
# deliberately gets nothing at all (her 我的订阅 falls back to the
# personal link and would show that toast; she is the unassigned
# control and is not a recorded persona).
default_id = x("SELECT id FROM subscription ORDER BY inx ASC, id ASC LIMIT 1").fetchone()[0]
api("/user-subscription/set", {"userId": users["xiaoming"], "subscriptionIds": [default_id, s1, s2]})
api("/user-subscription/set", {"userId": users["aqiang"], "subscriptionIds": [default_id, s1, s3]})
for iid in inb.values():
    x("UPDATE node_inbound SET upload_bytes=?, download_bytes=? WHERE id=?", (random.randint(1, 20) * G, random.randint(20, 300) * G, iid))

# monitors
for name, typ, target, node in [("Google", "tcp", "www.google.com:443", "香港 HKT"), ("Netflix", "tcp", "www.netflix.com:443", "日本 东京"),
                                ("Cloudflare DNS", "icmp", "1.1.1.1", "新加坡")]:
    x("INSERT INTO service_monitor(name, type, target, interval_sec, timeout_sec, node_id, enabled, created_time, updated_time) VALUES(?,?,?,60,5,?,1,?,?)",
      (name, typ, target, ids[node], now, now))
    mid = x("SELECT last_insert_rowid()").fetchone()[0]
    for i in range(120):
        ok = random.random() > 0.02
        x("INSERT INTO service_monitor_result(monitor_id, node_id, timestamp, success, latency_ms, status_code, error_message) VALUES(?,?,?,?,?,0,?)",
          (mid, ids[node], now - i * 60000, 1 if ok else 0, round(random.uniform(8, 40), 1) if ok else 0, "" if ok else "timeout"))

# 24h of node metrics (every 5 min) and latency samples (every 10 min)
base_lat = {"香港 HKT": 38, "日本 东京": 62, "美国 圣何塞": 158, "新加坡": 72, "上海 移动中转": 9, "广州 电信中转": 7}
points = ["北京电信", "上海联通", "广东移动", "北京联通", "上海电信", "广东电信"]
for name, nid in ids.items():
    net_in = net_out = 0
    for i in range(288):
        t = now - (287 - i) * 300000
        hour = (t / 3600000) % 24
        busy = 0.5 + 0.5 * math.sin((hour - 14) / 24 * 2 * math.pi)
        sp_in = int((20 + 180 * busy) * 1024 * 1024 / 8 * random.uniform(0.7, 1.3))
        sp_out = int(sp_in * random.uniform(0.9, 1.1))
        net_in += sp_in * 300; net_out += sp_out * 300
        x("""INSERT INTO node_metric(node_id, timestamp, cpu_usage, mem_usage, disk_usage, net_in_bytes, net_out_bytes, net_in_speed, net_out_speed,
             load1, load5, load15, tcp_conns, udp_conns, uptime) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
          (nid, t, round(5 + 40 * busy * random.uniform(0.6, 1.2), 1), round(30 + 15 * busy, 1), 23.5, net_in, net_out, sp_in, sp_out,
           round(0.2 + 1.5 * busy, 2), round(0.3 + 1.2 * busy, 2), round(0.4 + busy, 2), int(80 + 600 * busy), int(10 + 60 * busy), 86400 * 21 + i * 300))
    for i in range(144):
        t = now - (143 - i) * 600000
        hour = (t / 3600000) % 24
        evening = 1 + (0.6 if 19 <= hour <= 23 else 0)
        for j, p in enumerate(points):
            lat = int(base_lat[name] * (1 + 0.08 * j) * evening * random.uniform(0.92, 1.1))
            loss = 0 if random.random() > 0.08 else round(random.uniform(1, 8))
            x("INSERT INTO node_latency_sample(node_id, created_time, point, ok, latency_ms, loss_pct) VALUES(?,?,?,1,?,?)", (nid, t, p, lat, loss))

# node_traffic hourly buckets for the current month (the series behind
# node/traffic/month — whole-machine NIC bytes, summed per node as
# [in, out] since the 1st in the viewer's zone). Without these rows the
# endpoint honestly answers an empty map, and a full re-record washes
# that fixtures key back to {} (R7X-006: the 1.4.2 demo values were
# hand-built data with no seed behind them). Monthly totals match the
# ballpark the demo has always shown (GiB, in/out per node); buckets
# start at the 1st of the month in UTC+8 — the earliest month start
# among the zones the recorder asks with — so a recording on any day
# of the month yields all 6 nodes with non-empty data.
MONTH_TOTALS = {"香港 HKT": (12, 24), "日本 东京": (8, 16), "美国 圣何塞": (20, 40),
                "新加坡": (6, 12), "上海 移动中转": (3, 6), "广州 电信中转": (4, 8)}
_cn_now = datetime.datetime.fromtimestamp(now / 1000, datetime.timezone(datetime.timedelta(hours=8)))
_month_start = _cn_now.replace(day=1, hour=0, minute=0, second=0, microsecond=0)
_first_bucket = int(_month_start.timestamp() * 1000) // 3600000 * 3600000
_last_bucket = now // 3600000 * 3600000
_n_buckets = max(1, (_last_bucket - _first_bucket) // 3600000 + 1)
for name, nid in ids.items():
    in_g, out_g = MONTH_TOTALS[name]
    weights = [random.uniform(0.7, 1.3) for _ in range(_n_buckets)]
    wsum = sum(weights)
    for i, w in enumerate(weights):
        x("INSERT INTO node_traffic(node_id, hour, in_bytes, out_bytes) VALUES(?,?,?,?)",
          (nid, _first_bucket + i * 3600000, int(in_g * G * w / wsum), int(out_g * G * w / wsum)))

# 24h of tunnel metrics (hourly, on each tunnel's entry node)
entry_node = {st["tunnels"][0]: ids["上海 移动中转"], st["tunnels"][1]: ids["广州 电信中转"],
              st["tunnels"][2]: ids["广州 电信中转"], st["tunnels"][3]: ids["广州 电信中转"]}
for tid, nid in entry_node.items():
    bin_ = bout = 0
    for h in range(24, 0, -1):
        t = now - h * 3600000
        bin_ += random.randint(1, 9) * G // 4; bout += random.randint(1, 9) * G // 6
        x("""INSERT INTO tunnel_metric(tunnel_id, node_id, timestamp, bytes_in, bytes_out, connections, errors, avg_latency_ms)
             VALUES(?,?,?,?,?,?,0,?)""",
          (tid, nid, t, bin_, bout, random.randint(20, 300), round(random.uniform(2, 30), 1)))

# 24h of per-user traffic for the user dashboard
for uid, scale in [(users["xiaoming"], 1.0), (users["aqiang"], 1.6), (users["lili"], 0.4)]:
    total = 0
    for h in range(24, 0, -1):
        t = now - h * 3600000
        hour = datetime.datetime.fromtimestamp(t / 1000).hour
        flow = int(scale * G * (0.3 + 1.5 * max(0, math.sin((hour - 8) / 24 * 2 * math.pi))) * random.uniform(0.7, 1.3))
        total += flow
        x("INSERT INTO statistics_flow(user_id, flow, total_flow, time, created_time) VALUES(?,?,?,?,?)",
          (uid, flow, total, datetime.datetime.fromtimestamp(t / 1000).strftime("%H:00"), t))

# 30 days of hourly subscription traffic (subscription_traffic buckets —
# the series behind subscription/traffic and user/subscription/traffic;
# without these rows both endpoints answer an all-zero series). Totals
# land in the same ballpark as the upload/download_bytes set above.
for sid, up_g, down_g in [(s2, 6, 62), (s3, 9, 83)]:
    hours = 30 * 24
    for h in range(hours, 0, -1):
        t = now - h * 3600000
        hour = datetime.datetime.fromtimestamp(t / 1000).hour
        busy = 0.25 + max(0, math.sin((hour - 8) / 24 * 2 * math.pi))
        w = busy * random.uniform(0.6, 1.4)
        x("INSERT INTO subscription_traffic(subscription_id, hour, upload_bytes, download_bytes) VALUES(?,?,?,?)",
          (sid, t - t % 3600000, int(up_g * G / hours * w * 1.6), int(down_g * G / hours * w * 1.6)))

# everything online
x("UPDATE node SET status = 1")
x("UPDATE node_kernel SET status = 'running', installed_version = version")
x("UPDATE node_inbound SET enabled = 1")
db.commit()
json.dump({**st, "subs": [s1, s2, s3, s4]}, open(DB + ".seed.json", "w"), ensure_ascii=False)
print("ok")
