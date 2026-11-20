#!/usr/bin/env bash
# check-doc-consistency-selftest.sh — 文档一致性门禁的变体自检（A4-02）。
#
# 背景：本门此前无变异自测。本自检把整棵树镜像到临时目录（doc 门
# 的读取面横跨 doc/skills/脚本/后端/前端，逐文件镜像易漏面，整树
# 镜像 28MB 量级可接受；vite-frontend/node_modules 以符号链接指回
# 真实树，与门禁在 CI 前端 job 的运行前提一致）：未变异镜像必须
# PASS；两个变体必须 FAIL 并点名——(1) 技能包 package.json 版本与
# SKILL.md/ai-skill.md 脱钩（§3 三处一致）；(2) 技能包引用不存在的
# 路由（§2 幽灵接口）。
#
# 前提：node 在 PATH；go 在场时 §10/§16 等子项实核、无 go 时按门
# 自身口径 PENDING（不影响本自检的变体判定面——两变体均为纯文本
# 对账）。CI 的 gate-selftests job 所在 runner 自带 go 并先装前端
# 依赖；本地同理（未装依赖时 §14 会先红在环境前提上，按其输出装
# 依赖后复跑）。
#
# FIX-DOC-R 扩围：变体 5–7 覆盖 §19–§21（计量伴生端口区间、监控
# 目标两面口径、服务监控限值披露），每族一个变体、改坏文案必须被
# 对应节抓红并点名。（原变体 4 对应的 §18 去重键口径门已随订阅
# 去重功能于 2.1.2 删除，同波拆除。）§10(b) 的 x 包计数标记属
# 环境漂移敏感面（合并波次新增带测试包即漂移），不属本自检变体
# 面：计数对账由 INT-3/R2 在合并尖端按实数重标定并点名。
# FIX-XCOUNT 扩围：变体 8 覆盖 §10(b) x 包计数标记
# （ci-build.yml 的 [K11-COUNT:x-tested/x-untested] 声称 vs go list
# 实数）。标记在 int-wave 尖端 e5a83bf1 按实数重标定为 55/153
# （基线 557322be 实数 45/162——INT-3 合入波次给 9 个既有包新增
# 首个测试文件、FIX-LOGS 新增 internal/util/eventlog 包且自带
# 测试，净增 1 包，逐项对账见重标定提交说明）；INT-3 第三轮合入
# 又给 4 个既有包新增首个测试文件（internal/util/kcp、
# internal/util/resolver、limiter/rate、listener/kcp），总数仍
# 208，标记随之重标定为 59/149（逐包差集见重标定提交说明）。
# 变体 8 把镜像内
# 标记的 x-tested 数字改坏，必须被 §10(b) 抓红并点名（FAIL 行含
# 「声称 tested=」且给出实数）。无 go 时 §10(b) 按门自身口径
# PENDING 未核、变体不可判定，本自检在无 go 环境直接 FAIL 点名
# 环境前提（与头部「无 go 时 §10/§16 等子项实核」口径一致），
# 不静默跳过。注：变体 4–7（FIX-DOC-R 的 §18–§21 四族）与本变体
# 同树，合并后自检依次跑变体 1–8。
# FIX-212 W8 扩围：变体 9–12 覆盖 §22 四钉（探活参数、节点分组
# 限定句、协议开关语义、残留词零命中），每钉一个变体、改坏文案
# 必须被对应钉抓红并点名；变体 9–11 带变异落点断言防静默空转。
# D-R7-01 扩围：变体 24 覆盖 §25（S05 前端计量默认开门转跑），
# emptyForm 默认改回 false 必须被 §25 抓红并点名——证明转跑本身
# 真实执行，不止子门自检（子门自检只证子门，§25 被拆线时本变体
# 即绿、直接暴露接线丢失）。
# R18-A5-01 扩围：变体 25 覆盖 §30（改密请求体对齐门），auth.md
# 的 updatePassword 例文改回 oldPassword 旧形必须被 §30 抓红并
# 点名 R18-A5-01。
# R18-A2-02 扩围：变体 26 覆盖 §31（迁移镜像 digest 钉选门），
# panel_install.sh 任一 docker run 镜像回退为字面 alpine 必须被
# §31 抓红并点名 R18-A2-02。
# R18-A2-06 扩围：变体 27 覆盖 §32（安装预检回归测试在场门），
# test-install-scripts-proxy.sh 内 panel_find_free_port 的词引
# 用全部删除后必须被 §32 抓红并点名 R18-A2-06。
# R18-A2-11 扩围：变体 28 覆盖 §33（v6 判别锚对账门），
# docker-compose-v6.yml 的 IPv6 子网改值后必须被 §33 抓红并点
# 名 R18-A2-11。
# FIX-GATE-CALIB 改钉（R2-GATE-02）：上文 55/153、59/149 均为历史
# 标定记录（出处见各重标定提交），不再是变体 8 的写死锚点——变体
# 8 改为运行时从镜像内 ci-build.yml 机械读现行标记值（其后标记又
# 经 INT-5 重标定为 61/147），按实读值做 -1 变异并按实读值拼装
# 期望点名串，另加变异落点断言防静默空转；各变体变异后逐个还原
# 本变体文件（变体 7 的 monitoring.md 还原一并补齐，旧实现漏还原
# 曾污染变体 8 的判定环境）。
#
# 用法：check-doc-consistency-selftest.sh（无参数，作用于本仓库）。
# 退出码：0 = 基线 PASS 且变体均被抓；1 = 任一断言不成立。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
MIR="$TMP/repo"
mkdir -p "$MIR"

# 整树镜像，但排除 .git 与 node_modules（后者以符号链接指回真实
# 树，避免 CI 里整包拷贝数百 MB 依赖）。镜像内另建空 git 索引并
# 全量 add：§14 的 check-secret-material 按 git 跟踪集（ls-files）
# 枚举扫描面，无 .git 时枚举失败、基线必红——索引须在挂
# node_modules 符号链接之前建好，使链接保持未跟踪、不入扫描集。
tar -C "$ROOT" --exclude='./.git' --exclude='./vite-frontend/node_modules' -cf - . | tar -C "$MIR" -xf -
git -C "$MIR" init -q
git -C "$MIR" add -A
if [ -d "$ROOT/vite-frontend/node_modules" ] && [ ! -e "$MIR/vite-frontend/node_modules" ]; then
  ln -s "$ROOT/vite-frontend/node_modules" "$MIR/vite-frontend/node_modules"
fi

run_gate() { bash "$MIR/scripts/check-doc-consistency.sh" > "$1" 2>&1; }

echo "== baseline: unmutated mirror must PASS =="
if run_gate "$TMP/base.log"; then
  echo "OK: baseline PASS"
else
  echo "FAIL: baseline did not pass"; sed 's/^/    /' "$TMP/base.log"; exit 1
fi

echo "== variant 1: skill package.json version drifts from SKILL.md must FAIL and name it (§3) =="
sed -i '0,/"version": "[^"]*"/s//"version": "9.9.9-selftest"/' "$MIR/skills/flmx-api/package.json"
if run_gate "$TMP/v1.log"; then
  echo "FAIL: variant 1 was not caught"; exit 1
fi
grep -q "技能包版本不一致" "$TMP/v1.log" && echo "OK: variant 1 caught and named" || {
  echo "FAIL: variant 1 caught but the drift was not named"; sed 's/^/    /' "$TMP/v1.log"; exit 1; }
cp "$ROOT/skills/flmx-api/package.json" "$MIR/skills/flmx-api/package.json"

echo "== variant 2: skill references a non-existent route must FAIL and name it (§2) =="
printf '\n示例：GET /api/v1/zzz-ghost-route\n' >> "$MIR/skills/flmx-api/SKILL.md"
if run_gate "$TMP/v2.log"; then
  echo "FAIL: variant 2 was not caught"; exit 1
fi
grep -q "zzz-ghost-route" "$TMP/v2.log" && echo "OK: variant 2 caught and named" || {
  echo "FAIL: variant 2 caught but the ghost route was not named"; sed 's/^/    /' "$TMP/v2.log"; exit 1; }
cp "$ROOT/skills/flmx-api/SKILL.md" "$MIR/skills/flmx-api/SKILL.md"

echo "== variant 3: guide claims a fixed default password again must FAIL and name it (§17) =="
sed -i 's/默认账号 `admin@user`，密码没有固定默认值/默认账号 `admin@user` \/ `admin@user`/' "$MIR/doc/guide/getting-started.md"
if run_gate "$TMP/v3.log"; then
  echo "FAIL: variant 3 was not caught"; exit 1
fi
grep -q "初始口令口径门" "$TMP/v3.log" && echo "OK: variant 3 caught and named" || {
  echo "FAIL: variant 3 caught but the drift was not named"; sed 's/^/    /' "$TMP/v3.log"; exit 1; }
cp "$ROOT/doc/guide/getting-started.md" "$MIR/doc/guide/getting-started.md"

echo "== variant 5: metering companion port range drifts back to 59999 must FAIL and name it (§19) =="
sed -i 's/在 \[20000, 60000) 内随机取空闲端口/在 20000–59999 内随机取空闲端口/' "$MIR/doc/guide/subscription.md"
if run_gate "$TMP/v5.log"; then
  echo "FAIL: variant 5 was not caught"; exit 1
fi
grep -q "计量伴生端口口径门" "$TMP/v5.log" && echo "OK: variant 5 caught and named" || {
  echo "FAIL: variant 5 caught but the drift was not named"; sed 's/^/    /' "$TMP/v5.log"; exit 1; }
cp "$ROOT/doc/guide/subscription.md" "$MIR/doc/guide/subscription.md"

echo "== variant 6: monitor targets collapse back to hard-reject-only must FAIL and name it (§20) =="
sed -i 's/按\*\*非公网全集\*\*拒绝/按**硬拒集**拒绝/' "$MIR/doc/guide/monitoring.md"
if run_gate "$TMP/v6.log"; then
  echo "FAIL: variant 6 was not caught"; exit 1
fi
grep -q "监控目标口径门" "$TMP/v6.log" && echo "OK: variant 6 caught and named" || {
  echo "FAIL: variant 6 caught but the drift was not named"; sed 's/^/    /' "$TMP/v6.log"; exit 1; }
cp "$ROOT/doc/guide/monitoring.md" "$MIR/doc/guide/monitoring.md"

echo "== variant 7: service-monitor limit disclosure drifts must FAIL and name it (§21) =="
sed -i 's/并发上限默认 20（保存上限 50）/并发上限默认 30（保存上限 50）/' "$MIR/doc/guide/monitoring.md"
if run_gate "$TMP/v7.log"; then
  echo "FAIL: variant 7 was not caught"; exit 1
fi
grep -q "服务监控限值披露门" "$TMP/v7.log" && echo "OK: variant 7 caught and named" || {
  echo "FAIL: variant 7 caught but the drift was not named"; sed 's/^/    /' "$TMP/v7.log"; exit 1; }
cp "$ROOT/doc/guide/monitoring.md" "$MIR/doc/guide/monitoring.md"

echo "== variant 8: ci-build.yml x-count marker drifts from go list reality must FAIL and name it (§10(b)) =="
if ! command -v go >/dev/null 2>&1; then
  echo "FAIL: variant 8 undecidable — go toolchain missing, §10(b) is PENDING in this environment (see header)"; exit 1
fi
# 变异目标与期望一律从镜像内现行标记机械导出、不写死计数
# （FIX-GATE-CALIB / R2-GATE-02：旧实现写死 59/149，INT-5 把标记
# 重标定为 61/147 后 sed 零匹配静默空转、期望串永不命中，自检在
# 任何环境下都无法 PASS）。变异落点另行断言，锚点漂移即点名红，
# 不许再静默空转。
CI_YML="$MIR/.github/workflows/ci-build.yml"
CUR_T=$(grep -oP '\[K11-COUNT:x-tested=\K[0-9]+' "$CI_YML" | head -n 1)
CUR_U=$(grep -oP '\[K11-COUNT:x-untested=\K[0-9]+' "$CI_YML" | head -n 1)
if [ -z "$CUR_T" ] || [ -z "$CUR_U" ]; then
  echo "FAIL: variant 8 cannot read the current x-count markers from the mirror ci-build.yml"; exit 1
fi
MUT_T=$((CUR_T - 1))
sed -i "s/\[K11-COUNT:x-tested=${CUR_T}\]/[K11-COUNT:x-tested=${MUT_T}]/" "$CI_YML"
grep -q "\[K11-COUNT:x-tested=${MUT_T}\]" "$CI_YML" || {
  echo "FAIL: variant 8 mutation did not land (marker anchor drifted)"; exit 1; }
if run_gate "$TMP/v8.log"; then
  echo "FAIL: variant 8 was not caught"; exit 1
fi
grep -q "声称 tested=${MUT_T} untested=${CUR_U} ≠ 实数 tested=${CUR_T} untested=${CUR_U}" "$TMP/v8.log" && echo "OK: variant 8 caught and named" || {
  echo "FAIL: variant 8 caught but the drift was not named"; sed 's/^/    /' "$TMP/v8.log"; exit 1; }
cp "$ROOT/.github/workflows/ci-build.yml" "$MIR/.github/workflows/ci-build.yml"

echo "== variant 9: keepalive params drift in forward.md must FAIL and name it (§22a) =="
sed -i 's/满 180 秒后/满 181 秒后/' "$MIR/doc/guide/forward.md"
grep -q "181 秒" "$MIR/doc/guide/forward.md" || {
  echo "FAIL: variant 9 mutation did not land (keepalive anchor drifted)"; exit 1; }
if run_gate "$TMP/v9.log"; then
  echo "FAIL: variant 9 was not caught"; exit 1
fi
grep -q "探活参数钉子" "$TMP/v9.log" && echo "OK: variant 9 caught and named" || {
  echo "FAIL: variant 9 caught but the drift was not named"; sed 's/^/    /' "$TMP/v9.log"; exit 1; }
cp "$ROOT/doc/guide/forward.md" "$MIR/doc/guide/forward.md"

echo "== variant 10: node-group section loses its subscription exclusion must FAIL and name it (§22b) =="
sed -i 's/不参与订阅、转发、选线/不参与转发、选线/' "$MIR/doc/guide/nodes.md"
grep -q "不参与订阅" "$MIR/doc/guide/nodes.md" && {
  echo "FAIL: variant 10 mutation did not land (group-section anchor drifted)"; exit 1; }
if run_gate "$TMP/v10.log"; then
  echo "FAIL: variant 10 was not caught"; exit 1
fi
grep -q "节点分组钉子" "$TMP/v10.log" && echo "OK: variant 10 caught and named" || {
  echo "FAIL: variant 10 caught but the drift was not named"; sed 's/^/    /' "$TMP/v10.log"; exit 1; }
cp "$ROOT/doc/guide/nodes.md" "$MIR/doc/guide/nodes.md"

echo "== variant 11: protocol-switch section reverts to listener wording must FAIL and name it (§22c) =="
sed -i 's/屏蔽开关/监听是否开启/' "$MIR/doc/guide/nodes.md"
grep -q "监听是否开启" "$MIR/doc/guide/nodes.md" || {
  echo "FAIL: variant 11 mutation did not land (switch-section anchor drifted)"; exit 1; }
if run_gate "$TMP/v11.log"; then
  echo "FAIL: variant 11 was not caught"; exit 1
fi
grep -q "协议开关钉子" "$TMP/v11.log" && echo "OK: variant 11 caught and named" || {
  echo "FAIL: variant 11 caught but the drift was not named"; sed 's/^/    /' "$TMP/v11.log"; exit 1; }
cp "$ROOT/doc/guide/nodes.md" "$MIR/doc/guide/nodes.md"

echo "== variant 12: banned term reappears in index.md must FAIL and name it (§22d) =="
printf '\n远程节点（自测变异行）\n' >> "$MIR/doc/index.md"
if run_gate "$TMP/v12.log"; then
  echo "FAIL: variant 12 was not caught"; exit 1
fi
grep -q "残留词钉子" "$TMP/v12.log" && echo "OK: variant 12 caught and named" || {
  echo "FAIL: variant 12 caught but the drift was not named"; sed 's/^/    /' "$TMP/v12.log"; exit 1; }
cp "$ROOT/doc/index.md" "$MIR/doc/index.md"

# FIX-R4-GATES 扩围：变体 13–22 覆盖 G-R4-02/G-R4-03/G-R4-04 的新断
# 言（§5 正面、§17/§22a–c 代码侧、§23 四项、§24 测试在场）。每
# 变体只动一处、改坏后必须被对应节抓红并点名；变异落点断言防静
# 默空转；变异后逐个还原本变体文件。
echo "== variant 13: Caddy example line deleted must FAIL and name it (§5 positive side) =="
sed -i '/reverse_proxy 127\.0\.0\.1:6366/d' "$MIR/doc/guide/install.md"
grep -q "reverse_proxy 127" "$MIR/doc/guide/install.md" && {
  echo "FAIL: variant 13 mutation did not land (Caddy example anchor drifted)"; exit 1; }
if run_gate "$TMP/v13.log"; then
  echo "FAIL: variant 13 was not caught"; exit 1
fi
grep -q "Caddy 示例缺" "$TMP/v13.log" && echo "OK: variant 13 caught and named" || {
  echo "FAIL: variant 13 caught but the drift was not named"; sed 's/^/    /' "$TMP/v13.log"; exit 1; }
cp "$ROOT/doc/guide/install.md" "$MIR/doc/guide/install.md"

echo "== variant 14: initial_admin.go loses crypto/rand must FAIL and name it (§17 code side) =="
sed -i '/"crypto\/rand"/d' "$MIR/go-backend/internal/store/repo/initial_admin.go"
grep -q "crypto/rand" "$MIR/go-backend/internal/store/repo/initial_admin.go" && {
  echo "FAIL: variant 14 mutation did not land (crypto/rand anchor drifted)"; exit 1; }
if run_gate "$TMP/v14.log"; then
  echo "FAIL: variant 14 was not caught"; exit 1
fi
grep -q "初始口令口径门" "$TMP/v14.log" && echo "OK: variant 14 caught and named" || {
  echo "FAIL: variant 14 caught but the drift was not named"; sed 's/^/    /' "$TMP/v14.log"; exit 1; }
cp "$ROOT/go-backend/internal/store/repo/initial_admin.go" "$MIR/go-backend/internal/store/repo/initial_admin.go"

echo "== variant 15: keepalive DefaultIdle drifts 180->181 must FAIL and name it (§22a code side) =="
sed -i 's/DefaultIdle     = 180 \* time.Second/DefaultIdle     = 181 * time.Second/' "$MIR/go-gost/x/internal/keepalive/keepalive.go"
grep -q "DefaultIdle     = 181" "$MIR/go-gost/x/internal/keepalive/keepalive.go" || {
  echo "FAIL: variant 15 mutation did not land (DefaultIdle anchor drifted)"; exit 1; }
if run_gate "$TMP/v15.log"; then
  echo "FAIL: variant 15 was not caught"; exit 1
fi
grep -q "探活参数钉子" "$TMP/v15.log" && echo "OK: variant 15 caught and named" || {
  echo "FAIL: variant 15 caught but the drift was not named"; sed 's/^/    /' "$TMP/v15.log"; exit 1; }
cp "$ROOT/go-gost/x/internal/keepalive/keepalive.go" "$MIR/go-gost/x/internal/keepalive/keepalive.go"

echo "== variant 16: DisplayGroup reference lands in share.go must FAIL and name it (§22b code side) =="
printf '// DisplayGroup: selftest mutation — subscription rendering must not read the display label\n' >> "$MIR/go-backend/internal/kernel/share.go"
grep -q "DisplayGroup" "$MIR/go-backend/internal/kernel/share.go" || {
  echo "FAIL: variant 16 mutation did not land"; exit 1; }
if run_gate "$TMP/v16.log"; then
  echo "FAIL: variant 16 was not caught"; exit 1
fi
grep -q "节点分组钉子" "$TMP/v16.log" && echo "OK: variant 16 caught and named" || {
  echo "FAIL: variant 16 caught but the drift was not named"; sed 's/^/    /' "$TMP/v16.log"; exit 1; }
cp "$ROOT/go-backend/internal/kernel/share.go" "$MIR/go-backend/internal/kernel/share.go"

echo "== variant 17: SetProtocolBlock renamed must FAIL and name it (§22c code side) =="
sed -i 's/func SetProtocolBlock/func SetProtoBlock/' "$MIR/go-gost/x/service/service.go"
grep -q "func SetProtocolBlock" "$MIR/go-gost/x/service/service.go" && {
  echo "FAIL: variant 17 mutation did not land (SetProtocolBlock anchor drifted)"; exit 1; }
if run_gate "$TMP/v17.log"; then
  echo "FAIL: variant 17 was not caught"; exit 1
fi
grep -q "协议开关钉子" "$TMP/v17.log" && echo "OK: variant 17 caught and named" || {
  echo "FAIL: variant 17 caught but the drift was not named"; sed 's/^/    /' "$TMP/v17.log"; exit 1; }
cp "$ROOT/go-gost/x/service/service.go" "$MIR/go-gost/x/service/service.go"

echo "== variant 18: subnet example drifts back to the template default must FAIL and name it (§23a) =="
# 注意：示例值是文档值（a310aa5f 起为 172.27.0.0/16），写死旧值会随文档改值而漂移；
# 故像 §23a 门一样动态提取当前示例与模板缺省，再把示例改回缺省。
sec_v4=$(awk '/子网冲突的改法/{f=1} /不要给已部署的实例改实例键/{if(f)exit} f' "$MIR/doc/guide/install.md")
cur_v4=$(printf '%s' "$sec_v4" | grep -oP '例如 `172\.[0-9.]+/[0-9]+`' | grep -oP '172\.[0-9.]+/[0-9]+' | head -1)
def_v4=$(grep -oP -- '- subnet: \K172\.[0-9.]+/[0-9]+' "$MIR/docker-compose-v4.yml" | head -1)
[ -n "$cur_v4" ] && [ -n "$def_v4" ] || {
  echo "FAIL: variant 18 mutation did not land (subnet example anchor drifted)"; exit 1; }
sed -i "s|例如 \`$cur_v4\`|例如 \`$def_v4\`|" "$MIR/doc/guide/install.md"
grep -q "例如 \`$def_v4\`" "$MIR/doc/guide/install.md" || {
  echo "FAIL: variant 18 mutation did not land (subnet example anchor drifted)"; exit 1; }
if run_gate "$TMP/v18.log"; then
  echo "FAIL: variant 18 was not caught"; exit 1
fi
grep -q "子网示例钉子" "$TMP/v18.log" && echo "OK: variant 18 caught and named" || {
  echo "FAIL: variant 18 caught but the drift was not named"; sed 's/^/    /' "$TMP/v18.log"; exit 1; }
cp "$ROOT/doc/guide/install.md" "$MIR/doc/guide/install.md"

echo "== variant 19: identity key dropped from install.md must FAIL and name it (§23b) =="
# 注意：PANEL_FRONTEND_CONTAINER 在 install.md:69 出现两次（六键清单
# ＋同行自检 grep -cE 式），§23b 门是全文件 grep，故变异须用 /g
# 全删、只删清单处门禁不红；落地断言同为全文件 grep，两者一致。
sed -i 's/PANEL_FRONTEND_CONTAINER//g' "$MIR/doc/guide/install.md"
grep -q "PANEL_FRONTEND_CONTAINER" "$MIR/doc/guide/install.md" && {
  echo "FAIL: variant 19 mutation did not land (identity-key anchor drifted)"; exit 1; }
if run_gate "$TMP/v19.log"; then
  echo "FAIL: variant 19 was not caught"; exit 1
fi
grep -q "身份六键钉子" "$TMP/v19.log" && echo "OK: variant 19 caught and named" || {
  echo "FAIL: variant 19 caught but the drift was not named"; sed 's/^/    /' "$TMP/v19.log"; exit 1; }
cp "$ROOT/doc/guide/install.md" "$MIR/doc/guide/install.md"

echo "== variant 20: bot menu loses /status must FAIL and name it (§23c) =="
sed -i 's|、`/status` 得到同样的回答| 得到同样的回答|' "$MIR/doc/guide/notify.md"
grep -q "/status" "$MIR/doc/guide/notify.md" && {
  echo "FAIL: variant 20 mutation did not land (bot-menu anchor drifted)"; exit 1; }
if run_gate "$TMP/v20.log"; then
  echo "FAIL: variant 20 was not caught"; exit 1
fi
grep -q "机器人菜单钉子" "$TMP/v20.log" && echo "OK: variant 20 caught and named" || {
  echo "FAIL: variant 20 caught but the drift was not named"; sed 's/^/    /' "$TMP/v20.log"; exit 1; }
cp "$ROOT/doc/guide/notify.md" "$MIR/doc/guide/notify.md"

echo "== variant 21: residual version drifts 2.1.13->2.1.12 must FAIL and name it (§23d) =="
sed -i 's/2\.1\.13/2.1.12/g' "$MIR/doc/guide/upgrade.md"
grep -q "2.1.13" "$MIR/doc/guide/upgrade.md" && {
  echo "FAIL: variant 21 mutation did not land (residual version anchor drifted)"; exit 1; }
if run_gate "$TMP/v21.log"; then
  echo "FAIL: variant 21 was not caught"; exit 1
fi
grep -q "V1-01 残留段钉子" "$TMP/v21.log" && echo "OK: variant 21 caught and named" || {
  echo "FAIL: variant 21 caught but the drift was not named"; sed 's/^/    /' "$TMP/v21.log"; exit 1; }
cp "$ROOT/doc/guide/upgrade.md" "$MIR/doc/guide/upgrade.md"

echo "== variant 22: F1-N2 regression test deregistered must FAIL and name it (§24) =="
sed -i '/^test_report_compose_up_failure_branches$/d' "$MIR/test-install-scripts-proxy.sh"
grep -q "^test_report_compose_up_failure_branches$" "$MIR/test-install-scripts-proxy.sh" && {
  echo "FAIL: variant 22 mutation did not land (registration anchor drifted)"; exit 1; }
if run_gate "$TMP/v22.log"; then
  echo "FAIL: variant 22 was not caught"; exit 1
fi
grep -q "F1-N2 回归测试钉子" "$TMP/v22.log" && echo "OK: variant 22 caught and named" || {
  echo "FAIL: variant 22 caught but the drift was not named"; sed 's/^/    /' "$TMP/v22.log"; exit 1; }
cp "$ROOT/test-install-scripts-proxy.sh" "$MIR/test-install-scripts-proxy.sh"

echo "== variant 23: timeout-budgets premise failure (exit 2) must PENDING, not FAIL (§8, R5-D-01) =="
if FLMX_TMP_MIN_KB=999999999 run_gate "$TMP/v23.log"; then
  grep -q "PENDING: 超时预算门前提失效" "$TMP/v23.log" && echo "OK: variant 23 PENDING, not FAIL" || {
    echo "FAIL: variant 23 passed but no §8 PENDING line:"; sed 's/^/    /' "$TMP/v23.log"; exit 1; }
else
  echo "FAIL: variant 23 failed the gate — premise failure must PENDING, not FAIL:"; sed 's/^/    /' "$TMP/v23.log"; exit 1
fi

echo "== variant 24: emptyForm metering default flips back to false must FAIL and name it (§25, D-R7-01) =="
python3 - "$MIR/vite-frontend/src/pages/inbound/presets.ts" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p).read()
start = s.index("export function emptyForm(")
end = s.index("\n}\n", start)
body = s[start:end]
assert "meteringEnabled: true," in body, "baseline shape changed"
body = body.replace("meteringEnabled: true,", "meteringEnabled: false,", 1)
open(p, "w").write(s[:start] + body + s[end:])
PYEOF
grep -q "meteringEnabled: false," "$MIR/vite-frontend/src/pages/inbound/presets.ts" || {
  echo "FAIL: variant 24 mutation did not land (emptyForm default anchor drifted)"; exit 1; }
if run_gate "$TMP/v24.log"; then
  echo "FAIL: variant 24 was not caught"; exit 1
fi
grep -q "S05 前端计量默认开门" "$TMP/v24.log" && echo "OK: variant 24 caught and named" || {
  echo "FAIL: variant 24 caught but the drift was not named"; sed 's/^/    /' "$TMP/v24.log"; exit 1; }
cp "$ROOT/vite-frontend/src/pages/inbound/presets.ts" "$MIR/vite-frontend/src/pages/inbound/presets.ts"

echo "== variant 25: updatePassword doc example reverts to oldPassword must FAIL and name it (§30, R18-A5-01) =="
python3 - "$MIR/skills/flmx-api/references/auth.md" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p).read()
old = '''  "newUsername": "current-username",
  "currentPassword": "current-password",
  "newPassword": "new-password",
  "confirmPassword": "new-password"'''
new = '''  "oldPassword": "current-password",
  "newPassword": "new-password"'''
assert old in s, "baseline shape changed"
open(p, "w").write(s.replace(old, new, 1))
PYEOF
grep -q '"oldPassword"' "$MIR/skills/flmx-api/references/auth.md" || {
  echo "FAIL: variant 25 mutation did not land (updatePassword example anchor drifted)"; exit 1; }
if run_gate "$TMP/v25.log"; then
  echo "FAIL: variant 25 was not caught"; exit 1
fi
grep -q "R18-A5-01" "$TMP/v25.log" && echo "OK: variant 25 caught and named" || {
  echo "FAIL: variant 25 caught but the drift was not named"; sed 's/^/    /' "$TMP/v25.log"; exit 1; }
cp "$ROOT/skills/flmx-api/references/auth.md" "$MIR/skills/flmx-api/references/auth.md"

echo "== variant 26: migration image unpins back to bare alpine must FAIL and name it (§31, R18-A2-02) =="
sed -i '0,/"\$FLMX_MIGRATE_ALPINE_IMAGE"/s//alpine/' "$MIR/panel_install.sh"
grep -q 'docker run.*alpine sh' "$MIR/panel_install.sh" || {
  echo "FAIL: variant 26 mutation did not land (alpine image anchor drifted)"; exit 1; }
if run_gate "$TMP/v26.log"; then
  echo "FAIL: variant 26 was not caught"; exit 1
fi
grep -q "R18-A2-02" "$TMP/v26.log" && echo "OK: variant 26 caught and named" || {
  echo "FAIL: variant 26 caught but the drift was not named"; sed 's/^/    /' "$TMP/v26.log"; exit 1; }
cp "$ROOT/panel_install.sh" "$MIR/panel_install.sh"

echo "== variant 27: preflight helper loses all test references must FAIL and name it (§32, R18-A2-06) =="
sed -i '/panel_find_free_port/d' "$MIR/test-install-scripts-proxy.sh"
! grep -Fwq panel_find_free_port "$MIR/test-install-scripts-proxy.sh" || {
  echo "FAIL: variant 27 mutation did not land (panel_find_free_port references remain)"; exit 1; }
if run_gate "$TMP/v27.log"; then
  echo "FAIL: variant 27 was not caught"; exit 1
fi
grep -q "R18-A2-06" "$TMP/v27.log" && echo "OK: variant 27 caught and named" || {
  echo "FAIL: variant 27 caught but the drift was not named"; sed 's/^/    /' "$TMP/v27.log"; exit 1; }
cp "$ROOT/test-install-scripts-proxy.sh" "$MIR/test-install-scripts-proxy.sh"

echo "== variant 28: v6 compose subnet drifts from the script anchor must FAIL and name it (§33, R18-A2-11) =="
sed -i 's/fd00:dead:beef::\/48/fd00:dead:beee::\/48/' "$MIR/docker-compose-v6.yml"
grep -q 'fd00:dead:beee::/48' "$MIR/docker-compose-v6.yml" || {
  echo "FAIL: variant 28 mutation did not land (v6 subnet anchor drifted)"; exit 1; }
if run_gate "$TMP/v28.log"; then
  echo "FAIL: variant 28 was not caught"; exit 1
fi
grep -q "R18-A2-11" "$TMP/v28.log" && echo "OK: variant 28 caught and named" || {
  echo "FAIL: variant 28 caught but the drift was not named"; sed 's/^/    /' "$TMP/v28.log"; exit 1; }
cp "$ROOT/docker-compose-v6.yml" "$MIR/docker-compose-v6.yml"

echo "SELFTEST PASS"
