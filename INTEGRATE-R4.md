# INTEGRATE-R4 — Traffic fix round 4 integration ledger

Baseline: traffic-fix/integrate 0707b2c1 (code tip c8e61df0 + round-3 ledger commit).
Code tip after merges: 5661ac8e. Ledger commit: this file.

## Merge order (all `--no-ff` merge commits, 0 conflicts expected)

| # | Branch | Tip | Merge commit |
|---|--------|-----|--------------|
| 1 | traffic-fix4/e01 (R3-E-01) | ed72b599 | cdfad50d |
| 2 | traffic-fix4/nftloss (R3A-NEW-1 + C NEW-1) | 9047a07c | cc5a111b |
| 3 | traffic-fix4/kernels (C NEW-2) | 2db1dc0b | 2a6771ab |
| 4 | traffic-fix4/lows (R3-E-LOW-01/02/03, C OBS-1) | 3cb4c672 | 5661ac8e |

All four branch tips verified as ancestors of HEAD after the merges
(`git merge-base --is-ancestor`, all OK).

## Conflict (1, resolved by hand, both sides kept)

Merging `traffic-fix4/lows` conflicted in `scripts/check-doc-consistency.sh`:
both F4-C (§28, kernel install atomic swap + offline cache) and F4-D
(§27b, repush receipt companionSplitNote three-side check) appended a new
gate block at the same spot. Resolution: keep BOTH blocks, in numeric order
(§27b first, then §28), each with its own closing `fi`, byte-for-byte from
their source branches. `bash -n` syntax check passed. No gate was removed
or relaxed: the script diff vs 0707b2c1 has 0 deletions (pure addition).

## Diff arithmetic (0707b2c1..5661ac8e, per-file verified)

- e01: 3 files +126/−11
- nftloss: 7 files +505/−7
- kernels: 5 files +621/−71
- lows: 11 files +331/−37
- Sum: 26 file-entries +1583/−126
- Merged: 24 files +1583/−126, per-file mismatches NONE
- Overlaps (2): `go-gost/x/kernel/manager.go` (nftloss + kernels),
  `scripts/check-doc-consistency.sh` (kernels + lows, see conflict above)

## Gates on code tip 5661ac8e (logs in ~/workspace/tmp/int-r4/)

- doc-consistency: PASS (exit 0, 0 FAIL lines). New gates OK:
  §27b `R3-E-LOW-02 ... 三侧同在`, §28 `C NEW-2 ... 三锚点同在`.
  Gate-script diff vs baseline is addition-only (0 `-` lines).
- go vet: backend exit 0, 0 bytes; x exit 0, 0 bytes. Both run from inside
  their own module dirs (`cd go-backend && go vet ./...`,
  `cd go-gost/x && go vet ./...`) — the F4-C wrong-directory invocation
  was NOT repeated.
- K11: marker `[K11-COUNT:x-tested=90] [K11-COUNT:x-untested=123]`;
  independent recount: total 213 packages, tested 90 (counting packages
  with TestGoFiles OR XTestGoFiles — TestGoFiles alone gives 87, missing
  3 external-test-only packages), untested 123. Matches.
- pnpm build: exit 0, dist files generated (`dist/sw.js`,
  `dist/workbox-80abce0f.js`).
- Backend full (`go test -timeout 30m ./...` in go-backend, incl.
  tests/contract): 19 packages ok, 0 FAIL, exit 0
  (handler 1245s-class run; repo 448.711s; contract 169.827s).
- go-gost/x full (`go test -timeout 30m ./...`): 90 packages ok,
  0 FAIL, exit 0, FIRST RUN green — no flake disposition needed.
  (Fast wall time: build/test cache shared with the lanes' earlier runs;
  changed packages re-ran fresh, e.g. socket 26.309s.)

## Unverified / carry-over (not integration gates)

- Live verification of the merged tip: NOT done here — a separate V-round
  (live total verification) must run next, per the program flow.
- Real-browser rendering + in-place upgrade rehearsal: still pending at
  parent level (pre-existing carry-overs, unchanged by this integration).
- F4-A's honest caveat carries over: landed-verification was not run
  end-to-end with a real dispatch (unit + probe coverage only).
- F4-D's LOW-02 live non-empty note: unverified (needs a 2.1.13 agent).

## Release status

No tag, no push to main, no release, no production touch.
Live test-machine agent 18508 untouched; no scratch panel/agent started.
