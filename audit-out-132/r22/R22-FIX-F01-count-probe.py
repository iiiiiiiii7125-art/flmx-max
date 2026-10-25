#!/usr/bin/env python3
"""R22 F-01 API call-point full-set recount (red/green probe).

Method (reproduces R22-L3 §3.1):
- Frontend call points = every Network.post/get first-arg path literal in
  vite-frontend/src/api/index.ts (quoted or backtick template, possibly
  multi-line) + exportBackup's direct axios.post -> /backup/export.
- Backend registry = mux.HandleFunc("/api/v1/...") in
  go-backend/internal/http/handler/handler.go, prefix "/api/v1" stripped.
- Mapping: exact match, or template literal (contains ${...}) normalized by
  truncating at the first "${" and prefix-matching a trailing-slash route
  (e.g. /monitor/nodes/${id}/metrics -> /monitor/nodes/ prefix route).
- Exit 0 iff missing == 0; prints TOTAL / MAPPED / MISSING and the list.
"""
import re, sys

FE = "vite-frontend/src/api/index.ts"
BE = "go-backend/internal/http/handler/handler.go"

src = open(FE).read()
# Network.get/post first-arg literal (quoted or backtick), DOTALL for multi-line
pat = re.compile(r"Network\.(?:get|post)\b[^()]*\(\s*(?:\"([^\"]+)\"|`([^`]+)`)", re.S)
calls = []
for m in pat.finditer(src):
    lit = m.group(1) if m.group(1) is not None else m.group(2)
    line = src[: m.start()].count("\n") + 1
    calls.append((line, lit))

# exportBackup direct axios.post(`${baseURL}/backup/export`, ...)
if "axios.post(" in src and "/backup/export" in src:
    m = re.search(r"export const exportBackup\b", src)
    line = src[: m.start()].count("\n") + 1 if m else -1
    calls.append((line, "/backup/export [exportBackup direct axios]"))

# sanity: every Network.get/post occurrence must have yielded a literal
n_net = len(re.findall(r"Network\.(?:get|post)\b", src))
if n_net != len([c for c in calls if "exportBackup" not in c[1]]):
    print(f"WARN: Network.* occurrences={n_net} literal-calls="
          f"{len([c for c in calls if 'exportBackup' not in c[1]])}")

# backend registry
hsrc = open(BE).read()
routes = set()
for m in re.finditer(r'mux\.HandleFunc\("(/api/v1[^"]*)"', hsrc):
    routes.add(m.group(1)[len("/api/v1"):])

def mapped(path):
    if path in routes:
        return True
    if "${" in path:
        prefix = path.split("${")[0]
        # template path must fall under a trailing-slash prefix route
        for r in routes:
            if r.endswith("/") and prefix.startswith(r):
                return True
    return False

missing = [(ln, p) for ln, p in calls if not mapped(p.split(" [")[0])]
print(f"TOTAL_CALL_POINTS={len(calls)}")
print(f"MAPPED={len(calls) - len(missing)}")
print(f"MISSING={len(missing)}")
for ln, p in missing:
    print(f"MISSING line={ln} path={p}")
sys.exit(0 if not missing else 1)
