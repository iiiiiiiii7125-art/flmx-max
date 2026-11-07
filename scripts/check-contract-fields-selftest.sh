#!/usr/bin/env bash
# check-contract-fields-selftest.sh — 前后端契约门禁的变体自检（A4-02）。
#
# 背景：本门此前无变异自测。本自检把门禁的全部读取面镜像到临时
# 目录：未变异镜像必须 PASS；把 types.ts 里已登记 nullable 字段的
# `| null` 摘掉必须 FAIL 并点名该字段（C1 本体回归）；repository.go
# 新增未登记且经 nullable 下发的 List* 出口必须 FAIL 点名（A4-04
# 普查推导回归，变体 2）；在册字段经 nullableInIPString 下发者
# （TunnelApiItem.inIp）的 `| null` 摘掉同样必须 FAIL 点名
# （R2-01-04 发射集推导回归，变体 3——旧手写名单下此变体静默
# 通过）。
#
# typescript 解析与门禁本体同口径：优先镜像内 vite-frontend/
# node_modules，其次脚本所在真实树的 node_modules，再次
# FLMX_CHECK_TYPESCRIPT 显式指向。CI 的 gate-selftests job 会先
# pnpm install，本地未装依赖时以 FLMX_CHECK_TYPESCRIPT 指向任一
# 已装副本即可。
#
# 变体 4（C8 / FIX-PROBE）：镜像须含 tuning_probe.go；摘掉前端
# NodeTuneProbeResult 的 panelRttLossRate（C2-01 新字段）必须 FAIL
# 并点名（C8b 双向齐断言的变异红证）。
#
# 用法：check-contract-fields-selftest.sh（无参数，作用于本仓库）。
# 退出码：0 = 基线 PASS 且变体均被抓；1 = 任一断言不成立。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-contract-fields.mjs"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
MIR="$TMP/repo"

FE="vite-frontend/src"
mkdir -p "$MIR/$FE/api" "$MIR/$FE/pages/dashboard" "$MIR/$FE/pages/node" \
  "$MIR/$FE/types" "$MIR/go-backend/internal/store/repo" \
  "$MIR/go-backend/internal/http/handler" "$MIR/go-backend/internal/store/model" \
  "$MIR/go-gost/x/socket" \
  "$MIR/scripts"
for f in api/types.ts api/index.ts pages/config.tsx pages/limit.tsx \
  pages/dashboard/use-dashboard-data.ts pages/forward.tsx pages/tunnel.tsx \
  pages/node/types.ts types/index.ts; do
  cp "$ROOT/$FE/$f" "$MIR/$FE/$f"
done
cp "$ROOT/go-backend/internal/store/repo/repository.go" "$MIR/go-backend/internal/store/repo/"
cp "$ROOT/go-backend/internal/http/handler/handler.go" "$MIR/go-backend/internal/http/handler/"
cp "$ROOT/go-backend/internal/http/handler/tunnel_quality_prober.go" "$MIR/go-backend/internal/http/handler/"
cp "$ROOT/go-backend/internal/store/model/model.go" "$MIR/go-backend/internal/store/model/"
cp "$ROOT/go-gost/x/socket/tuning_probe.go" "$MIR/go-gost/x/socket/"
cp "$ROOT/scripts/contract-whitelist.json" "$MIR/scripts/"

run_gate() { node "$GATE" "$MIR" > "$1" 2>&1; }

echo "== baseline: unmutated mirror must PASS =="
if run_gate "$TMP/base.log"; then
  echo "OK: baseline PASS"
else
  echo "FAIL: baseline did not pass"; sed 's/^/    /' "$TMP/base.log"; exit 1
fi

echo "== variant 1: drop | null from NodeApiItem.remark in types.ts must FAIL and name it (C1) =="
sed -i '0,/remark?: string | null;/s//remark?: string;/' "$MIR/$FE/api/types.ts"
if run_gate "$TMP/v1.log"; then
  echo "FAIL: variant 1 was not caught"; exit 1
fi
grep -q "NodeApiItem.remark" "$TMP/v1.log" && echo "OK: variant 1 caught and named" || {
  echo "FAIL: variant 1 caught but the field was not named"; sed 's/^/    /' "$TMP/v1.log"; exit 1; }
cp "$ROOT/$FE/api/types.ts" "$MIR/$FE/api/types.ts"

echo "== variant 3: drop | null from TunnelApiItem.inIp (emitted via nullableInIPString) must FAIL and name it (R2-01-04) =="
sed -i '0,/inIp?: string | null;/s//inIp?: string;/' "$MIR/$FE/api/types.ts"
if run_gate "$TMP/v3.log"; then
  echo "FAIL: variant 3 was not caught (inIp null-drop passed)"; exit 1
fi
grep -q "TunnelApiItem.inIp" "$TMP/v3.log" && echo "OK: variant 3 caught and named" || {
  echo "FAIL: variant 3 caught but the field was not named"; sed 's/^/    /' "$TMP/v3.log"; exit 1; }
cp "$ROOT/$FE/api/types.ts" "$MIR/$FE/api/types.ts"

echo "== variant 2: new unregistered List* exit emitting a nullable field must FAIL and name it (A4-04) =="
cat >> "$MIR/go-backend/internal/store/repo/repository.go" <<'EOF'

func (r *Repository) ListZzzSelftestProbe() ([]map[string]interface{}, error) {
	row := map[string]interface{}{"probeField": nullableString(nil)}
	return []map[string]interface{}{row}, nil
}
EOF
if run_gate "$TMP/v2.log"; then
  echo "FAIL: variant 2 was not caught (unregistered nullable-emitting List* exit passed)"; exit 1
fi
grep -q "ListZzzSelftestProbe" "$TMP/v2.log" && echo "OK: variant 2 caught and named" || {
  echo "FAIL: variant 2 caught but the new exit was not named"; sed 's/^/    /' "$TMP/v2.log"; exit 1; }
cp "$ROOT/go-backend/internal/store/repo/repository.go" "$MIR/go-backend/internal/store/repo/repository.go"

echo "== variant 4: drop panelRttLossRate from NodeTuneProbeResult must FAIL and name it (C8b) =="
sed -i '/panelRttLossRate/d' "$MIR/$FE/api/index.ts"
if run_gate "$TMP/v4.log"; then
  echo "FAIL: variant 4 was not caught"; exit 1
fi
grep -q "panelRttLossRate" "$TMP/v4.log" && echo "OK: variant 4 caught and named" || {
  echo "FAIL: variant 4 caught but the field was not named"; sed 's/^/    /' "$TMP/v4.log"; exit 1; }
cp "$ROOT/$FE/api/index.ts" "$MIR/$FE/api/index.ts"

echo "SELFTEST PASS"
