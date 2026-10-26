# tcpfit engine

Vendored from https://github.com/Kylin010/tcpfit, version 0.5.9.
Upstream Git blob: `b586a250aceef2ddca4286c755a49e83032fc43e`.
Upstream flyx vendored copy blob: `85ce3a557b03c4b7f870797766c3f79229655c7f`
(identical bytes; verified with `git hash-object` during the 3.3.43 port).
MIT license: see LICENSE. Copyright (c) 2026 Kylin010.

Only the final CLI dispatch was removed so the file can be sourced as a library.
FLMX calls the measurement and policer scan functions through bridge.sh; it does
not call upstream installation, updates, telemetry, sysctl tuning, swap, route
window changes, or archive commands. FLMX owns its sysctl backups and persistence.
The bridge overrides queue restoration to replay a saved configuration, fails
closed for external shapers, and never permits an interactive prompt.

FLMX addition: bridge.sh has one extra action, `short` (5 s single-stream
probe for ranking handshake candidates by measured bandwidth), which is not
in the upstream bridge. tcpfit.sh itself is byte-identical to upstream.
