#!/bin/bash
# R17-FIX-C repro harness — "panel + agent co-restart" loop (R17-X-01).
#
# Replicates the R17-L4 deploy/repro sequence per cycle:
#   stop agent -> restart panel containers (backend+frontend, triggers the
#   panel startup sync) -> wait backend healthy -> reset-failed + start
#   agent -> wait WAIT_SYNC for the sync to land -> snapshot gost.json.
# R17 original: poison appeared ~30s after agent start; isolation cells
# waited 75-90s, so WAIT_SYNC defaults to 75.
#
# HIT = landed /etc/flux_agent/gost.json differs from pristine (md5/lines),
#   OR contains duplicate service names, OR the agent is not active after
#   the wait. A hit cycle restores the pristine gost.json and restarts the
#   agent before the next cycle, so cycles are independent; if the restore
#   itself fails to bring the agent up, the loop aborts (no cascading hits).
#
# Usage (on the test machine, as root):
#   bash /root/r17c/harness.sh <cycles> <wait_seconds> <run_id>
# Logs go to stdout; run it under nohup redirecting to
# /root/r17c/harness-<run_id>.log. Requires: PRISTINE gost backup at
# /root/r17c/gost.json.pristine, docker compose file at
# /root/flmx-panel/docker-compose.yml.
#
# This script only restarts services and restores gost.json from the
# pristine backup. It never touches panel data, inbounds, or subscriptions.

CYCLES="${1:-10}"
WAIT_SYNC="${2:-75}"
RUN_ID="${3:-r17c}"
COMPOSE=/root/flmx-panel/docker-compose.yml
GOST=/etc/flux_agent/gost.json
PRISTINE=/root/r17c/gost.json.pristine
PRISTINE_MD5=d48f09e132ebe99fe97a619dd72d42d4
PRISTINE_LINES=328

HITS=0
HIT_CYCLES=""

ts() { date -u +%FT%TZ; }

dup_names() {
  python3 - "$GOST" <<'PYEOF' 2>/dev/null
import json, sys
from collections import Counter
try:
    cfg = json.load(open(sys.argv[1]))
except Exception as e:
    print("UNREADABLE:" + str(e))
    sys.exit(0)
names = [s.get("name", "") for s in cfg.get("services", []) if isinstance(s, dict)]
dups = sorted({n for n, c in Counter(names).items() if c > 1})
print("DUP:" + ",".join(dups) if dups else "DUP:none")
print("SERVICES:" + str(len(names)))
PYEOF
}

agent_state() {
  echo "active=$(systemctl is-active flux_agent 2>/dev/null) restarts=$(systemctl show flux_agent -p NRestarts --value 2>/dev/null) pid=$(systemctl show flux_agent -p MainPID --value 2>/dev/null)"
}

gost_snapshot() {
  local md5 lines
  md5=$(md5sum "$GOST" 2>/dev/null | cut -d' ' -f1)
  lines=$(wc -l < "$GOST" 2>/dev/null | tr -d ' ')
  echo "gost md5=$md5 lines=$lines mtime=$(stat -c %y "$GOST" 2>/dev/null) $(dup_names | tr '\n' ' ')"
}

wait_backend_healthy() {
  local i
  for i in $(seq 1 30); do
    if docker ps --format '{{.Names}} {{.Status}}' 2>/dev/null | grep -q 'flux-panel-backend.*(healthy)'; then
      echo "backend healthy after ${i}x3s"
      return 0
    fi
    sleep 3
  done
  echo "backend NOT healthy after 90s"
  docker ps --format '{{.Names}} {{.Status}}'
  return 1
}

echo "=== R17-FIX-C harness run=$RUN_ID cycles=$CYCLES wait_sync=${WAIT_SYNC}s start=$(ts) ==="
echo "--- preconditions:"
gost_snapshot
agent_state
docker ps --format '{{.Names}} {{.Image}} {{.Status}}'

for i in $(seq 1 "$CYCLES"); do
  echo "=== cycle $i/$CYCLES start=$(ts) ==="

  # Guard: previous cycle must have left a pristine gost.json.
  cur_md5=$(md5sum "$GOST" 2>/dev/null | cut -d' ' -f1)
  if [ "$cur_md5" != "$PRISTINE_MD5" ]; then
    echo "WARN cycle $i: gost not pristine before start (md5=$cur_md5); restoring pristine"
    cp "$PRISTINE" "$GOST"
    systemctl reset-failed flux_agent 2>/dev/null || true
    systemctl start flux_agent
    sleep 5
    echo "after restore: $(gost_snapshot) $(agent_state)"
  fi

  # 1. stop agent (R17 deploy order: agent down while panel restarts)
  systemctl stop flux_agent
  sleep 3
  if pgrep -f '/etc/flux_agent/flux_agent' >/dev/null; then
    echo "WARN cycle $i: agent process still present after stop"
    pgrep -af '/etc/flux_agent/flux_agent' || true
  fi

  # 2. restart panel containers -> panel startup sync
  (cd /root/flmx-panel && docker compose -f "$COMPOSE" restart backend frontend)
  wait_backend_healthy || echo "WARN cycle $i: proceeding despite backend not healthy"

  # 3. start agent (R17 deploy order: panel up, then agent start)
  systemctl reset-failed flux_agent 2>/dev/null || true
  systemctl start flux_agent
  sleep 5
  echo "agent after start: $(agent_state)"

  # 4. wait for the startup sync to land (R17 poison: ~30s after start)
  echo "waiting ${WAIT_SYNC}s for startup sync..."
  sleep "$WAIT_SYNC"

  # 5. snapshot + verdict
  snap=$(gost_snapshot)
  state=$(agent_state)
  echo "cycle $i snapshot: $snap $state"
  snap_md5=$(echo "$snap" | sed -n 's/.*md5=\([a-f0-9]*\).*/\1/p')
  snap_lines=$(echo "$snap" | sed -n 's/.*lines=\([0-9]*\).*/\1/p')
  dup=$(echo "$snap" | sed -n 's/.*DUP:\([^ ]*\).*/\1/p')
  active=$(echo "$state" | sed -n 's/.*active=\([^ ]*\).*/\1/p')

  hit=""
  [ "$snap_md5" != "$PRISTINE_MD5" ] && hit="${hit} md5-changed"
  [ "$snap_lines" != "$PRISTINE_LINES" ] && hit="${hit} lines-changed($snap_lines)"
  [ "$dup" != "none" ] && [ -n "$dup" ] && hit="${hit} dup-names($dup)"
  [ "$active" != "active" ] && hit="${hit} agent-not-active($active)"

  if [ -n "$hit" ]; then
    HITS=$((HITS + 1))
    HIT_CYCLES="$HIT_CYCLES $i"
    echo "cycle $i HIT:$hit"
    cp "$GOST" "/root/r17c/cycle-${i}-gost.json"
    echo "poisoned copy saved: /root/r17c/cycle-${i}-gost.json md5=$(md5sum "/root/r17c/cycle-${i}-gost.json" | cut -d' ' -f1)"
    # Restore pristine so the next cycle is independent (a poisoned file
    # bricks the agent on its next restart by design of R17-X-01).
    systemctl stop flux_agent 2>/dev/null || true
    sleep 2
    cp "$PRISTINE" "$GOST"
    systemctl reset-failed flux_agent 2>/dev/null || true
    systemctl start flux_agent
    sleep 5
    echo "after restore: $(gost_snapshot) $(agent_state)"
    if [ "$(systemctl is-active flux_agent 2>/dev/null)" != "active" ]; then
      echo "CRITICAL cycle $i: agent did NOT come up after pristine restore — aborting loop"
      break
    fi
  else
    echo "cycle $i MISS (gost pristine, agent active)"
  fi
done

echo "=== R17-FIX-C harness run=$RUN_ID end=$(ts) ==="
echo "cycles_done=$i hits=$HITS hit_cycles:[$HIT_CYCLES ]"
if [ "$i" -gt 0 ]; then
  echo "hit_rate=$HITS/$i"
fi
echo "final: $(gost_snapshot) $(agent_state)"
