#!/bin/bash
# R20 integration regression (fix-r20 @ 7452e74). Sequential to avoid build-cache contention.
set -u
export PATH=$HOME/go/bin:$PATH
export TMPDIR=$HOME/tmp-go
mkdir -p "$TMPDIR"
cd ~/workspace/.fixr20 || exit 1
LOGDIR=audit-out-132/r20
echo "=== R20-INT regression start: $(date -u +%FT%TZ) ===" | tee "$LOGDIR/R20-INT-gotest.log"

echo "--- go-backend build ---" | tee -a "$LOGDIR/R20-INT-gotest.log"
(cd go-backend && go build ./... 2>&1) | tee -a "$LOGDIR/R20-INT-gotest.log"
echo "backend build exit: ${PIPESTATUS[0]}" | tee -a "$LOGDIR/R20-INT-gotest.log"

echo "--- go-backend vet ---" | tee -a "$LOGDIR/R20-INT-gotest.log"
(cd go-backend && go vet ./... 2>&1) | tee -a "$LOGDIR/R20-INT-gotest.log"
echo "backend vet exit: ${PIPESTATUS[0]}" | tee -a "$LOGDIR/R20-INT-gotest.log"

echo "--- go-backend go test ./... -count=1 -timeout 30m ---" | tee -a "$LOGDIR/R20-INT-gotest.log"
(cd go-backend && go test ./... -count=1 -timeout 30m 2>&1) | tee -a "$LOGDIR/R20-INT-gotest.log"
echo "backend test exit: ${PIPESTATUS[0]}" | tee -a "$LOGDIR/R20-INT-gotest.log"

echo "--- go-gost build/vet/test ---" | tee -a "$LOGDIR/R20-INT-gotest.log"
(cd go-gost && go build ./... 2>&1 && go vet ./... 2>&1 && go test ./... -count=1 -timeout 10m 2>&1) | tee -a "$LOGDIR/R20-INT-gotest.log"
echo "gost exit: ${PIPESTATUS[0]}" | tee -a "$LOGDIR/R20-INT-gotest.log"

echo "--- go-gost/x build/vet/test ---" | tee -a "$LOGDIR/R20-INT-gotest.log"
(cd go-gost/x && go build ./... 2>&1 && go vet ./... 2>&1 && go test ./... -count=1 -timeout 15m 2>&1) | tee -a "$LOGDIR/R20-INT-gotest.log"
echo "gost/x exit: ${PIPESTATUS[0]}" | tee -a "$LOGDIR/R20-INT-gotest.log"

echo "--- frontend tsc + vite build ---" | tee -a "$LOGDIR/R20-INT-gotest.log"
(cd vite-frontend && pnpm run build 2>&1 | tail -15) | tee -a "$LOGDIR/R20-INT-gotest.log"
echo "frontend exit: ${PIPESTATUS[0]}" | tee -a "$LOGDIR/R20-INT-gotest.log"

echo "=== R20-INT regression end: $(date -u +%FT%TZ) ===" | tee -a "$LOGDIR/R20-INT-gotest.log"
