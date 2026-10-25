#!/bin/bash
# R19-L4 version-gate binary probe (baseline d66042c). Reuses R15 cfg files verbatim.
# Xray 25.8.29 re-downloaded direct from GitHub (R15's ~/.tmp/xv-25.8.29 no longer on disk);
# build hash 81b7cd7 matches R15's binary.
SB111=~/workspace/.r9bin/sing-box-1.11.15-linux-amd64/sing-box
SB112=~/workspace/.r9bin/sing-box-1.12.17-linux-amd64/sing-box
SB113=~/workspace/.r9bin/sing-box-1.13.14-linux-amd64/sing-box
SB114=~/workspace/.r4b1-bin/sing-box-1.14.3-linux-amd64/sing-box
XR2536=~/workspace/.r9bin/xr-25.3.6/xray
XR25516=~/workspace/.r9bin/xr-25.5.16/xray
XR2568=~/workspace/.r9bin/xray-25.6.8-bin
XR25829=~/workspace/.r16-tmp/bin/xv-25.8.29/xray
XR2595=~/workspace/.r9bin/xr-25.9.5/xray
CFG=~/workspace/.r9bin/cfg
CFG2=~/workspace/.tmp-big/r11m3
TREE=~/workspace/.r19-tree
run() { # label bin cfg
  local label="$1" bin="$2" cfg="$3"
  out=$("$bin" check -c "$cfg" 2>&1); code=$?
  # sing-box uses `check`; xray uses `run -test` semantics via `check`? R15 used bare binary with cfg:
  # fall back: if `check` unsupported, use `run -test`.
  echo "$label bin=$("$bin" version 2>&1 | head -1) cfg=$cfg exit=$code msg=$(echo "$out" | tr '\n' ' ' | head -c 300)"
}
# NOTE: sing-box `check -c` is not the R15 invocation; R15 ran binary with config directly.
# Use the same semantics as R15: `sing-box check -c cfg` exits 0/1 on decode; xray: `xray run -test -config cfg`.
sbrun() { local label="$1" bin="$2" cfg="$3"; out=$("$bin" check -c "$cfg" 2>&1); code=$?; echo "$label bin=$("$bin" version 2>&1|head -1) cfg=$cfg exit=$code msg=$(echo "$out"|tr '\n' ' '|head -c 300)"; }
xrun() { local label="$1" bin="$2" cfg="$3"; out=$("$bin" run -test -config "$cfg" 2>&1); code=$?; echo "$label bin=$("$bin" version 2>&1|head -1) cfg=$cfg exit=$code msg=$(echo "$out"|tr '\n' ' '|head -c 300)"; }
{
echo "== versions =="
"$SB111" version; "$SB112" version; "$SB113" version; "$SB114" version
"$XR2536" version | head -1; "$XR25516" version | head -1; "$XR2568" version | head -1; "$XR25829" version | head -1; "$XR2595" version | head -1
echo "== VG-01 ACME floor 1.14 =="
sbrun VG01-low "$SB113" "$CFG/sb-acme.json"
sbrun VG01-ok "$SB114" "$CFG/sb-acme.json"
echo "== VG-02 AnyTLS floor 1.12 =="
sbrun VG02-low "$SB111" "$CFG/sb-anytls.json"
sbrun VG02-ok "$SB112" "$CFG/sb-anytls.json"
echo "== VG-03 Snell inbound floor 1.14 =="
sbrun VG03-low "$SB113" "$CFG/sb-snell.json"
sbrun VG03-ok "$SB114" "$CFG/sb-snell.json"
echo "== VG-04 ENC client+server floor 25.9.5 =="
xrun VG04-low "$XR25829" "$CFG/enc-canonical.json"
xrun VG04-ok "$XR2595" "$CFG/enc-canonical.json"
xrun VG04srv-low "$XR25829" "$CFG2/enc-inbound.json"
xrun VG04srv-ok "$XR2595" "$CFG2/enc-inbound.json"
echo "== VG-05 Snell outbound v4 floor 1.14 =="
sbrun VG05-low "$SB113" "$CFG2/sb-snell-out-v4.json"
sbrun VG05-ok "$SB114" "$CFG2/sb-snell-out-v4.json"
sbrun VG05-v5shape "$SB114" "$CFG2/sb-snell-out.json"
echo "== VG-06 egress non-auto floor 1.12 =="
sbrun VG06-low "$SB111" "$CFG/sb-egress-prefer-old.json"
sbrun VG06-ok "$SB112" "$CFG/sb-egress-prefer.json"
echo "== VG-07 happyEyeballs symbol count (floor 25.6.8) =="
for b in "$XR2536" "$XR25516" "$XR2568" "$XR2595"; do echo "$b happyEyeballs=$(strings "$b" | grep -c happyEyeballs)"; done
echo "== VG-08 http_clients floor 1.14 =="
sbrun VG08-low "$SB113" "$CFG/sb-httpclients.json"
sbrun VG08-ok "$SB114" "$CFG/sb-httpclients.json"
echo "== VG-09 snell-server search =="
find /home/hatch/workspace /home/hatch/.tmp-big /tmp -maxdepth 4 -name "*snell-server*" 2>/dev/null | wc -l
find / -maxdepth 4 -name "snell-server*" 2>/dev/null | wc -l
echo "== VG-12 naive outbound floor 1.13 =="
sbrun VG12-low "$SB112" "$CFG/sb-naive-out.json"
sbrun VG12-ok "$SB113" "$CFG/sb-naive-out.json"
sbrun VG12-inbound-111 "$SB111" "$CFG/sb-naive-in.json"
echo "== VG-13 xhttp oldest 25.3.6 =="
xrun VG13-2536 "$XR2536" "$CFG2/xr-xhttp.json"
xrun VG13-2595 "$XR2595" "$CFG2/xr-xhttp.json"
echo "== VG-10 Reality default 1.0.0 (code/doc/frontend triple) =="
grep -n "DefaultRealityMinClientVer = " "$TREE/go-backend/internal/kernel/model.go"
grep -n "1.0.0" "$TREE/doc/guide/faq.md" | head -3
grep -n "1.0.0" "$TREE/vite-frontend/src/pages/inbound/inbound-form-modal.tsx" | head -5
echo "== VG-11 dual-stack route agent 1.2.0 (tag archaeology, not binary) =="
cd "$TREE"
for tag in 1.1.1 1.2.0; do
  echo "tag $tag node_route.go family=$(git show $tag:go-backend/internal/http/handler/node_route.go 2>/dev/null | grep -ci 'family') traceroute.go family=$(git show $tag:go-gost/x/socket/traceroute.go 2>/dev/null | grep -ci 'family')"
done
echo "intro commits first-containing tag: $(git tag --contains 58dadac | head -1) $(git tag --contains 9756636 | head -1)"
echo "58dadac=$(git tag --contains 58dadac | tr '\n' ' ')"
echo "9756636=$(git tag --contains 9756636 | tr '\n' ' ')"
echo "current tree node_route.go family=$(grep -ci 'family' go-backend/internal/http/handler/node_route.go) traceroute.go family=$(grep -ci 'family' go-gost/x/socket/traceroute.go)"
grep -n "1.2.0" doc/guide/nodes.md | head -3
echo "== ENC gate code lines (handler/kernel.go) =="
grep -n "versionBelow3(version, 25, 9, 5)" "$TREE/go-backend/internal/http/handler/kernel.go"
grep -n "^func kernelVersionGate\|^func encDialGate\|^func checkKernelVersionGates\|versionBelow(version, 1, 14)\|versionBelow(version, 1, 12)" "$TREE/go-backend/internal/http/handler/kernel.go" | head -12
grep -n "SingboxHTTPClient\|SingboxSnell\|SingboxNaive" "$TREE/go-backend/internal/http/handler/kernel_egress.go" | head -5
grep -n "prefer" "$TREE/go-backend/internal/http/handler/kernel_egress.go" | head -5
echo "== anti-sweep: versionBelow* call sites =="
grep -rn "versionBelow3\|versionBelowPadded\|versionBelow(" "$TREE/go-backend" --include="*.go" | grep -v _test.go | grep -v "func versionBelow" | wc -l
grep -rhn "versionBelow3(version, [0-9]" "$TREE/go-backend" --include="*.go" | grep -v _test.go | sort -u
grep -rhn "versionBelow(version, [0-9]" "$TREE/go-backend" --include="*.go" | grep -v _test.go | sort -u
} 2>&1
echo "== anti-scan: versionBelow* call sites (no 14th gate?) =="
grep -rn "versionBelow\|versionBelow3\|versionBelowPadded" "$TREE/go-backend/internal/" --include=*.go | grep -v _test | grep -v "func versionBelow" | wc -l
grep -rn "versionBelow\|versionBelow3\|versionBelowPadded" "$TREE/go-backend/internal/" --include=*.go | grep -v _test | grep -v "func versionBelow"
echo "== ENC gate code lines (handler/kernel.go) =="
grep -n "25.9.5\|25, 9, 5" "$TREE/go-backend/internal/http/handler/kernel.go" | head -10
grep -n "func.*[Ee][Nn][Cc].*Gate\|versionBelowPadded(25, 9, 5)\|versionBelow(25" "$TREE/go-backend/internal/http/handler/kernel.go" | head -10
echo "== VG-09 snell-server search (whole workspace roots) =="
find /home/hatch/workspace /home/hatch/.tmp-big /tmp -maxdepth 4 -name "*snell-server*" 2>/dev/null | wc -l
