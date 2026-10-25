#!/usr/bin/env python3
"""r21-L4 section 7 extension: reconcile every other list/detail endpoint
(routes matching /list|/get|/detail|/info) — backend actual response keys vs
frontend typed consumption (api/index.ts Network.post<T> contract).
Backend sources: repo map literals, model struct json tags, handler view
structs / inline maps. Prints per-endpoint diffs.
"""
import re, pathlib

T = pathlib.Path("/home/hatch/workspace/.r22-tree")
FE = T / "vite-frontend/src"
HD = T / "go-backend/internal/http/handler"
RP = T / "go-backend/internal/store/repo"
MD = T / "go-backend/internal/store/model"

def go_json_keys(struct_text):
    return [m.group(1) for m in re.finditer(r'`json:"([^",]+)(?:,[^"]*)?"`', struct_text)]

def find_go_struct(files, name):
    for f in files:
        text = pathlib.Path(f).read_text()
        m = re.search(r"type " + re.escape(name) + r" struct \{(.*?)\n\}", text, re.S)
        if m:
            return go_json_keys(m.group(1)), f
    return None, None

def repo_map_keys(func_name):
    for f in RP.glob("*.go"):
        if f.name.endswith("_test.go"):
            continue
        text = f.read_text()
        m = re.search(r"func \(r \*Repository\) " + re.escape(func_name) + r"\(", text)
        if not m:
            continue
        nxt = text.find("\nfunc ", m.start() + 5)
        body = text[m.start():nxt if nxt != -1 else len(text)]
        return sorted(set(re.findall(r'"([a-zA-Z0-9_]+)":', body))), str(f)
    return None, None

def handler_body(func_name):
    for f in HD.glob("*.go"):
        if f.name.endswith("_test.go"):
            continue
        text = f.read_text()
        m = re.search(r"func \(h \*Handler\) " + re.escape(func_name) + r"\(", text)
        if not m:
            continue
        nxt = text.find("\nfunc ", m.start() + 5)
        return text[m.start():nxt if nxt != -1 else len(text)], str(f)
    return None, None

def ts_iface(text, name):
    m = re.search(r"export (?:interface|type) " + re.escape(name) + r"\b[^={]*[={](.*?)\n\}", text, re.S)
    if not m:
        return None
    body = m.group(1)
    fields = []
    for line in body.splitlines():
        line = line.strip()
        mm = re.match(r"([A-Za-z0-9_]+)\??:", line)
        if mm:
            fields.append(mm.group(1))
    # extends base?
    ext = re.search(r"export interface " + re.escape(name) + r" extends (\w+)", text)
    if ext:
        base = ts_iface(text, ext.group(1))
        if base:
            fields = base + fields
    return fields

types_ts = (FE / "api/types.ts").read_text()
index_ts = (FE / "api/index.ts").read_text()

def fe_fields(name):
    for text in (types_ts, index_ts):
        f = ts_iface(text, name)
        if f is not None:
            return f
    return None

def report(ep, backend, fe_name, note=""):
    fe = fe_fields(fe_name) if fe_name else None
    be = sorted(set(backend)) if backend else None
    print(f"--- {ep}")
    print(f"    backend[{len(be) if be else 0}]: {be}")
    print(f"    frontend {fe_name}[{len(fe) if fe else 0}]: {fe}")
    if be is not None and fe is not None:
        print(f"    backend-only: {sorted(set(be)-set(fe))}")
        print(f"    frontend-only: {sorted(set(fe)-set(be))}")
    if note:
        print(f"    note: {note}")

model_files = list(MD.glob("*.go")) + list(RP.glob("*.go"))

# 1 passkeyList: model.Passkey vs PasskeyItem
keys, _ = find_go_struct(model_files, "Passkey")
report("/user/passkey/list", keys, "PasskeyItem")
# 2 userList: ListUsers map vs UserApiItem
keys, _ = repo_map_keys("ListUsers")
report("/user/list", keys, "UserApiItem")
# 3 config/list: dynamic map
report("/config/list", None, None, "dynamic Record<string,string>; no fixed key set; handler filters sensitive keys via FilterSensitiveConfigs")
# 5 kernelList: handler inline
body, _ = handler_body("kernelList")
print("--- /kernel/list handler body:")
print(body)
keys, _ = find_go_struct(model_files, "NodeKernel")
report("/kernel/list (model.NodeKernel)", keys, "KernelApiItem")
# 7 subscriptionList: subscriptionView struct (handler/subscription.go)
sub_go = (HD / "subscription.go").read_text()
m = re.search(r"type subscriptionView struct \{(.*?)\n\}", sub_go, re.S)
keys = go_json_keys(m.group(1)) if m else None
report("/subscription/list (subscriptionView)", keys, "SubscriptionItem")
# 8 remoteSourceList: model.RemoteSource
keys, _ = find_go_struct(model_files, "RemoteSource")
report("/subscription/source/list", keys, "RemoteSourceItem")
# 9 inboundSubInfo: handler return shape
body, _ = handler_body("inboundSubInfo")
print("--- /inbound/sub/info handler body:")
print(body)
report("/inbound/sub/info", None, "SubInfoApiData", "backend shape read from handler body above")
# 10 inboundRelayList: handler inline
body, _ = handler_body("inboundRelayList")
print("--- /inbound/relay/list handler body:")
print(body)
keys, _ = find_go_struct(model_files, "NodeInboundRelay")
report("/inbound/relay/list (model.NodeInboundRelay)", keys, "InboundRelayApiItem")
# 11 getUserInbounds: UserInboundAssignment (repo)
keys, _ = find_go_struct(model_files, "UserInboundAssignment")
report("/user-inbound/list", keys, "UserInboundAssignment")
# 12 userSubscriptionList: handler body
body, _ = handler_body("userSubscriptionList")
print("--- /user-subscription/list handler body:")
print(body)
report("/user-subscription/list", None, "UserSubscriptionListData", "backend shape read from handler body above")
# 13/14 tunnelList/tunnelGet: ListTunnels map vs TunnelApiItem
keys, _ = repo_map_keys("ListTunnels")
report("/tunnel/list + /tunnel/get", keys, "TunnelApiItem")
# 15 forwardList
keys, _ = repo_map_keys("ListForwards")
report("/forward/list", keys, "ForwardApiItem")
# 16 speedLimitList
keys, _ = repo_map_keys("ListSpeedLimits")
report("/speed-limit/list", keys, "SpeedLimitApiItem")
# 17 userTunnelList: handler body + UserTunnelDetail
body, _ = handler_body("userTunnelList")
print("--- /tunnel/user/list handler body (first 2500 chars):")
print(body[:2500])
keys, _ = find_go_struct(model_files, "UserTunnelDetail")
report("/tunnel/user/list (model.UserTunnelDetail)", keys, "UserTunnelPermissionApiItem")
# 18/19/20 groups
keys, _ = repo_map_keys("ListTunnelGroups")
report("/group/tunnel/list", keys, "TunnelGroupApiItem")
keys, _ = repo_map_keys("ListUserGroups")
report("/group/user/list", keys, "UserGroupApiItem")
keys, _ = repo_map_keys("ListGroupPermissions")
report("/group/permission/list", keys, "GroupPermissionApiItem")
# 21/22 federation: untyped frontend
body, _ = handler_body("federationShareList")
print("--- /federation/share/list handler body (first 2000 chars):")
print(body[:2000])
report("/federation/share/list", None, None, "frontend Array<Record<string,unknown>> (untyped); backend ListPeerShares model + runtime enrichment, see handler body")
body, _ = handler_body("federationRemoteUsageList")
print("--- /federation/share/remote-usage/list handler body (first 1500 chars):")
print(body[:1500])
report("/federation/share/remote-usage/list", None, None, "frontend Array<Record<string,unknown>> (untyped); backend shape read from handler body above")
# 23 announcement/get: model.Announcement vs AnnouncementData
keys, _ = find_go_struct(model_files, "Announcement")
report("/announcement/get", keys, "AnnouncementData")
# 24 monitorPermissionList: model.MonitorPermission
keys, _ = find_go_struct(model_files, "MonitorPermission")
report("/monitor/permission/list", keys, "MonitorPermissionApiItem")
