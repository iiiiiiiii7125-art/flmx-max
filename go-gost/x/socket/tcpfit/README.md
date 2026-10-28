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
in the upstream bridge. tcpfit.sh was byte-identical to upstream until
FLMX deviation D2 below.

FLMX deviation (D2, R2-J-F-01): tcpfit.sh strips leading zeros from
operator-supplied numbers at parse time with its own `strip_zeros`
helper, so they never reach bash `$(( ))` arithmetic, which parses a
leading zero as octal and dies with `value too great for base` on `08`/`09`.
(This D2 is local to the tcpfit engine deviations listed in this file;
it is not PORT-3343 D2, the queue-replay mq-handle fix.)
`is_posint` itself is untouched: it uses `[ ... -ge ... ]`, which compares
in decimal, so it correctly accepts `08` — the defect was downstream of it.
Upstream already knew the hazard and stripped `--from`/`--to`/`--step`
(:2702-2704); D2 extends the same pattern to the four remaining sites:
`--nominal`/`--dur`/`--margin` in `cmd_sweep`'s parse loop, the `--swap`
size in `cmd_harden` (stripped after the `G`/`g` suffix is removed), and
the `nominal` argument at the entry of `validate_peer` (the wizard calls
it directly with hand-typed bandwidth, bypassing `cmd_sweep`'s parse).
No other arithmetic site takes operator input (see the R2-J-F-01 full-file
`$(( ))` enumeration). Only these parse-time lines differ from upstream;
the rest of tcpfit.sh is still byte-identical.

FLMX deviation (D1, PORT-3343-BATCH2-D1D2-FIX.md): bridge.sh keeps the
egress interface in `BRIDGE_IFACE`, not upstream's `iface`, and
`detect_iface()` prints that. Upstream's `$iface` is shadowed by the
unassigned `local iface` in tcpfit.sh functions such as `cmd_sweep`
(:2736) under `set -u`, which killed every deep scan with
`iface: unbound variable`.
