#!/usr/bin/env python3
"""R19-L4 section 7 field reconciliation.
Backend source of truth = actual response key sets (handler-constructed maps
/ view structs), frontend = api/types.ts declarations + page reads.
Usage: python3 R19-L4-fieldcheck.py
"""
import re, pathlib, json

T = pathlib.Path("/home/hatch/workspace/.r19-tree")
FE = T / "vite-frontend/src"

def go_json_keys(struct_text):
    keys = []
    for m in re.finditer(r'`json:"([^",]+)(?:,[^"]*)?"`', struct_text):
        keys.append(m.group(1))
    return keys

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
    return fields

# ---- 7.2 inbound list ----
kernel_go = (T / "go-backend/internal/http/handler/kernel.go").read_text()
m = re.search(r"type inboundView struct \{(.*?)\n\}", kernel_go, re.S)
be_in = go_json_keys(m.group(1))
types_ts = (FE / "api/types.ts").read_text()
fe_in = ts_iface(types_ts, "InboundApiItem")
print("inboundView backend keys:", len(be_in), be_in)
print("InboundApiItem frontend fields:", len(fe_in) if fe_in else None, fe_in)
print("inbound backend-only:", sorted(set(be_in) - set(fe_in or [])))
print("inbound frontend-only:", sorted(set(fe_in or []) - set(be_in)))

# nested: kernel.Settings / kernel.Transport / kernel.Security (+view wrapper)
kmodel = (T / "go-backend/internal/kernel/model.go").read_text()
for sname, fname in [("Settings", "InboundSettings"), ("Transport", "InboundTransport"), ("Security", "InboundSecurity")]:
    gm = re.search(r"type " + sname + r" struct \{(.*?)\n\}", kmodel, re.S)
    if not gm:
        print(f"WARN: kernel.{sname} struct not found in model.go")
        continue
    gk = go_json_keys(gm.group(1))
    fk = ts_iface(types_ts, fname)
    print(f"nested {sname} backend:", len(gk), gk)
    print(f"nested {fname} frontend:", len(fk) if fk else None, fk)
    print(f"nested {sname} backend-only:", sorted(set(gk) - set(fk or [])))
    print(f"nested {sname} frontend-only:", sorted(set(fk or []) - set(gk)))

# security view wrapper extra keys
sv = re.search(r"type kernelSecurityView struct \{(.*?)\n\}", kernel_go, re.S)
print("kernelSecurityView wrapper keys:", go_json_keys(sv.group(1)) if sv else None)
print("kernelSecurityView body:", sv.group(1) if sv else None)

# ---- 7.1 node list ----
repo = (T / "go-backend/internal/store/repo/repository.go").read_text()
m = re.search(r"func \(r \*Repository\) ListNodes\(\) \(.*?\{(.{0,4000}?)\n\t\}\n", repo, re.S)
print("ListNodes body found:", bool(m))
# fallback: find map literal keys in ListNodes function span
idx = repo.find("func (r *Repository) ListNodes()")
span = repo[idx:idx+3000]
keys = sorted(set(re.findall(r'"([a-zA-Z]+)":', span)))
print("ListNodes map keys (span scan):", len(keys), keys)
fed = (T / "go-backend/internal/http/handler/federation.go").read_text()
idx = fed.find("func syncRemoteNodeStatuses")
span = fed[idx:idx+2500]
print("syncRemoteNodeStatuses keys:", sorted(set(re.findall(r'"([a-zA-Z]+)":', span))))
nl = (T / "go-backend/internal/http/handler/node_latency.go").read_text()
idx = nl.find("func attachNodeLatency")
span = nl[idx:idx+3000]
print("attachNodeLatency keys:", sorted(set(re.findall(r'"([a-zA-Z]+)":', span))))
node_api = ts_iface(types_ts, "NodeApiItem")
print("NodeApiItem fields:", len(node_api) if node_api else None, node_api)
node_tsx = (FE / "pages/node.tsx").read_text()
for k in ["latency", "latencyTargets", "panelVersion", "versionWarning", "syncError"]:
    print(f"node.tsx reads of node.{k}:", len(re.findall(r"node\." + k + r"\b", node_tsx)))
    print(f"frontend-wide '{k}':", len(re.findall(re.escape(k), (FE).rglob("*") and "" or "")) if False else "see grep")
print("NodeApiItem declares latency:", "latency" in (node_api or []))
print("NodeApiItem declares latencyTargets:", "latencyTargets" in (node_api or []))
print("NodeApiItem declares panelVersion:", "panelVersion" in (node_api or []))
print("NodeApiItem declares versionWarning:", "versionWarning" in (node_api or []))
print("NodeApiItem has index signature:", "[key: string]" in types_ts[:types_ts.find("export interface NodeApiItem")+3000] if "export interface NodeApiItem" in types_ts else "check manually")
