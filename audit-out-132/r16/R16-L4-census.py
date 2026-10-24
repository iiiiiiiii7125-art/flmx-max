#!/usr/bin/env python3
"""R15 routes/tables census, same method as R14-00-baseline §2.
Reads the read-only worktree ~/workspace/.r16-tree @ 5bd5a90.
- routes: all mux.HandleFunc in handler.go Register + router.go /system-info.
- auth classifier replicates middleware/auth.go shouldSkip/allowsOptionalAuth/requiresAdmin.
- method_guard derived from the handler function body (r.Method checks).
- tables: AutoMigrate models list in repository.go.
Usage: python3 r16_census.py <out-routes.csv> <out-tables.txt>
"""
import csv, re, sys, pathlib

TREE = pathlib.Path("/home/hatch/workspace/.r16-tree")
HANDLER_DIR = TREE / "go-backend/internal/http/handler"

def should_skip(p):
    if p.startswith("/flow/"): return True
    if p.startswith("/api/v1/open_api/"): return True
    if p.startswith("/api/v1/captcha/"): return True
    if p == "/api/v1/config/get": return False
    if p == "/api/v1/user/login": return True
    if p in ("/api/v1/user/passkey/status", "/api/v1/user/passkey/login/begin",
             "/api/v1/user/passkey/login/finish"): return True
    if p == "/api/v1/public/config/get": return True
    if p in ("/api/v1/federation/connect", "/api/v1/federation/tunnel/create",
             "/api/v1/federation/runtime/reserve-port", "/api/v1/federation/runtime/apply-role",
             "/api/v1/federation/runtime/release-role", "/api/v1/federation/runtime/diagnose",
             "/api/v1/federation/runtime/command"): return True
    return False

def requires_admin(p):
    if p.startswith("/api/v1/monitor/permission/"): return True
    if p.startswith("/api/v1/system/"): return True
    if p.startswith("/api/v1/group/"): return True
    if p.startswith("/api/v1/federation/share/") or p.startswith("/api/v1/federation/node/"): return True
    if p.startswith("/api/v1/node/"): return True
    if (p.startswith("/api/v1/kernel/") or p.startswith("/api/v1/inbound/") or
        p.startswith("/api/v1/notify/") or p.startswith("/api/v1/subscription/")):
        if p == "/api/v1/inbound/sub/info": return False
        return True
    if p.startswith("/api/v1/speed-limit/"): return True
    if p.startswith("/api/v1/backup/"): return True
    if p.startswith("/api/v1/api/v1/backup/"): return True
    if p.startswith("/api/v1/tunnel/"):
        if p.startswith("/api/v1/tunnel/user/tunnel"): return False
        return True
    if p in ("/api/v1/user/create", "/api/v1/user/list", "/api/v1/user/update",
             "/api/v1/user/delete", "/api/v1/user/reset",
             "/api/v1/user/quota/reset",
             "/api/v1/config/update", "/api/v1/config/update-single",
             "/api/v1/announcement/update",
             "/api/v1/user-inbound/list", "/api/v1/user-inbound/set",
             "/api/v1/user-subscription/list", "/api/v1/user-subscription/set"):
        return True
    return False

def classify(path):
    if not path.startswith("/api/"):
        return "non-API (JWT middleware passes through; handler-level auth)"
    if should_skip(path):
        return "PUBLIC (JWT skip; own credential: captcha/login/peer-token/none)"
    if path == "/api/v1/config/get":
        return "OPTIONAL-AUTH (JWT optional)"
    if requires_admin(path):
        return "ADMIN (JWT + role=admin)"
    return "JWT (any role)"

# --- collect handler function bodies for method-guard derivation ---
bodies = {}  # func name -> body text
for f in HANDLER_DIR.glob("*.go"):
    if f.name.endswith("_test.go"):
        continue
    text = f.read_text()
    # find `func (h *Handler) name(` ... next `func (` at col 0
    for m in re.finditer(r"func \(h \*Handler\) (\w+)\(", text):
        name = m.group(1)
        start = m.start()
        nxt = text.find("\nfunc ", start + 5)
        bodies[name] = text[start:nxt if nxt != -1 else len(text)]

def method_guard(func_name, path):
    if path == "/system-info":
        return "GET (WS upgrade)"
    body = bodies.get(func_name)
    if body is None:
        return "UNKNOWN-FUNC-NOT-FOUND:" + func_name
    # normalize whitespace
    b = re.sub(r"\s+", " ", body)
    # patterns, order matters: most specific first
    if "r.Method != http.MethodGet && r.Method != http.MethodHead" in b or \
       "r.Method != http.MethodHead && r.Method != http.MethodGet" in b:
        return "GET,HEAD"
    if "r.Method != http.MethodGet && r.Method != http.MethodPost" in b or \
       "r.Method != http.MethodPost && r.Method != http.MethodGet" in b:
        return "not(Get,Post)"
    if re.search(r"switch r\.Method \{.*case http\.MethodPost:.*case http\.MethodGet:", b, re.S):
        return "GET,POST"
    if "r.Method != http.MethodPost" in b:
        return "POST only"
    if "r.Method != http.MethodGet" in b:
        return "GET only"
    if "r.Method == http.MethodGet || r.Method == http.MethodPost" in b:
        return "GET,POST"
    # `not(Get,Post)`: method must be neither GET nor POST, e.g. storageSummary
    if re.search(r"r\.Method != http\.MethodGet.*r\.Method != http\.MethodPost", b) or \
       re.search(r"if r\.Method == http\.MethodGet \|\| r\.Method == http\.MethodPost \{[^}]*return", b):
        return "not(Get,Post)"
    if "r.Method" in b:
        return "HAS-METHOD-CHECK-UNCLASSIFIED"
    return "ANY (no method check in body)"

handler_go = (HANDLER_DIR / "handler.go").read_text().splitlines()
rows = []
for i, line in enumerate(handler_go, start=1):
    m = re.match(r'\s*mux\.HandleFunc\("([^"]+)"|mux\.HandleFunc\((\w+),\s*(.+)\)', line)
    m2 = re.match(r'\s*mux\.HandleFunc\("([^"]+)",\s*(.+)\)\s*$', line)
    m3 = re.match(r'\s*mux\.HandleFunc\((\w+),\s*(.+)\)\s*$', line)
    if m2:
        path, hexpr = m2.group(1), m2.group(2).strip()
    elif m3:
        path, hexpr = "/api/v1/open_api/gh/", "h.githubMirror (via const ghMirrorPath, subtree pattern)"
    else:
        continue
    # handler_func: strip h. prefix and authPeer wrapper
    if hexpr.startswith("h.githubMirror (via"):
        func_name, handler_col = "githubMirror", hexpr
        rows.append({"path": path, "handler": handler_col, "handler_func": func_name,
                     "method_guard": method_guard(func_name, path),
                     "auth": classify(path), "source": f"handler.go:{i}"})
        continue
    am = re.match(r"h\.authPeer\(h\.(\w+)\)", hexpr)
    if am:
        func_name, handler_col = am.group(1), hexpr
    else:
        func_name = hexpr[2:] if hexpr.startswith("h.") else hexpr
        handler_col = hexpr
    rows.append({
        "path": path, "handler": handler_col, "handler_func": func_name,
        "method_guard": method_guard(func_name, path),
        "auth": classify(path),
        "source": f"handler.go:{i}",
    })

# router.go /system-info
router_go = (TREE / "go-backend/internal/http/router.go").read_text().splitlines()
for i, line in enumerate(router_go, start=1):
    m = re.match(r'\s*mux\.Handle\("([^"]+)",\s*(.+)\)', line)
    if m:
        rows.append({"path": m.group(1), "handler": m.group(2).strip() + " (mux.Handle)",
                     "handler_func": "WebSocketHandler",
                     "method_guard": "GET (WS upgrade)",
                     "auth": "non-API (JWT middleware passes through; handler-level auth)",
                     "source": f"router.go:{i}"})

out_csv, out_tables = sys.argv[1], sys.argv[2]
with open(out_csv, "w", newline="") as f:
    w = csv.writer(f)
    w.writerow(["idx", "path", "handler", "handler_func", "method_guard", "auth", "source"])
    for n, r in enumerate(rows, start=1):
        w.writerow([n, r["path"], r["handler"], r["handler_func"],
                    r["method_guard"], r["auth"], r["source"]])
print(f"routes: {len(rows)} (handler.go HandleFunc={sum(1 for r in rows if r['source'].startswith('handler.go'))} + router.go={sum(1 for r in rows if r['source'].startswith('router.go'))})")
from collections import Counter
print("auth dist:", dict(Counter(r["auth"] for r in rows)))
print("guard dist:", dict(Counter(r["method_guard"] for r in rows)))

# tables
repo = (TREE / "go-backend/internal/store/repo/repository.go").read_text()
m = re.search(r"models := \[\]interface\{\}\{(.*?)\n\t\}", repo, re.S)
models = re.findall(r"&([\w.]+)\{\}", m.group(1))
with open(out_tables, "w") as f:
    f.write("\n".join(models) + "\n")
print(f"tables: {len(models)}")
