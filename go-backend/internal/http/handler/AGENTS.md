# BACKEND HTTP HANDLER KNOWLEDGE BASE

**Generated:** 2026-11-08
**Base commit:** e5a83bf1 (int-wave-20261107 tip at generation time)
**Branch context:** int-wave-20261107 integration line; the latest
release tag in this repository is 2.0.0

## OVERVIEW
HTTP request handlers for FLMX Admin API. Core business logic layer.
**Stack:** Go 1.25, net/http, GORM via Repository pattern.

## STRUCTURE
The package holds 73 non-test Go files (plus the test files alongside
them), split by functional area. The largest hubs:

```
handler/
├── handler.go              # Main Handler struct, route registration, login/captcha, job scheduling
├── mutations.go            # CRUD for users, tunnels, forwards (~10258 LOC — the central mutation hub)
├── control_plane.go        # Node control plane API, diagnosis assembly
├── kernel*.go              # Inbound/kernel config generation (kernel, chain, egress, external, relay, probe, protect, share address)
├── monitoring.go           # Monitoring: node/service/tunnel-quality access control and queries
├── flow_policy.go          # Traffic policy API (+ flow_upload*.go upload/ingest surfaces)
├── jobs.go                 # Background job management (sync, cleanup)
├── node_*.go               # Node surfaces: install/uninstall, latency, route, traffic, tune, status reconcile
├── subscription*.go        # Subscription delivery, front URLs, metering, templates, traffic
├── system_upgrade.go       # Panel self-upgrade API (+ upgrade.go node upgrade)
├── passkey.go              # Passkey (WebAuthn) login
└── ...                     # Remaining single-area files (notify, realm, storage, user_*, tunnel_*, nftables_*, …)
```

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| **User/Tunnel CRUD** | `mutations.go` | Largest file; all create/update/delete ops |
| **Login/Captcha** | `handler.go` | Login flow, captcha verification |
| **Route registration** | `handler.go` (`Register()`), `../router.go` | `/api/v1` routes are registered in `Handler.Register()`; the mux itself is assembled in `internal/http/router.go` |
| **Traffic Policies** | `flow_policy.go` | Flow limiting, quota management |
| **Background Jobs** | `jobs.go` | Scheduled sync/cleanup tasks |
| **Node Control** | `control_plane.go` | Node add/delete/list operations |

## CONVENTIONS
- Inherits from parent: GORM via Repository pattern, JWT in Authorization header.
- Large files expected (`mutations.go` ~10258 LOC - central mutation hub).
- Uses `repo.Repository` for DB access via `h.repo.XXX()` methods.
- Handlers never call `repo.DB()` directly — all queries go through Repository methods.
- Domain-driven file split: one file per functional area (jobs, monitoring, etc.).

## ANTI-PATTERNS
- Do NOT let handlers call `repo.DB()` directly — add a Repository method instead.
- Do NOT change handler signatures without updating router.go.

## COMMANDS
```bash
cd go-backend
go test ./internal/http/handler/...
```
