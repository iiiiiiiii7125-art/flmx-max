#!/bin/bash
set -e

# 解决 macOS 下 tr 可能出现的非法字节序列问题
export LANG=en_US.UTF-8
export LC_ALL=C



# GitHub repo used for release downloads
REPO="iiiiiiiii7125-art/flmx-max"

# 脚本版本号：每次修改脚本后递增，确保用户拉到最新版
# 自更新检查会比对仓库中的最新版本，不一致时自动重拉
SCRIPT_VERSION="20261105.01"

# --- 自更新检查 ---
# F-006：旧实现从 main 分支 raw 拉脚本、仅比对 SCRIPT_VERSION 字符串就
# 覆写自身以 root 重执行，无任何完整性校验，绕过同文件 download_verified
# 的 fail-closed 标准与 PINNED_VERSION 定版语义。现改为只走 Release 资产：
#   目标版本由 resolve_version() 解析（VERSION > FLMX_VERSION >
#   FLUX_VERSION > PINNED_VERSION > 最新 Release）。Release 副本带 PINNED_VERSION 时目标
#   恒为其自身版本，定版脚本不会被自更新带跑偏到更新的版本。
#   新脚本经 download_verified 拉取同版本资产的 .sha256 校验，通过才覆写。
# 缓存与校验如何兼得：旧实现用 cache-buster 是因为 main 分支 raw 内容
# 可变、CDN 可能发回旧副本。版本号资产 URL 不可变——新版本必然是新 URL；
# 缓存最多发回「该版本自己的脚本 + 该版本自己的 .sha256」，二者自洽且
# 仍须过 sha256 校验，不存在新旧混杂，无需 cache-buster。
# 校验被显式关闭（FLMX_VERIFY_DOWNLOADS!=1）时跳过自更新：绝不执行
# 未校验的新脚本，继续使用当前版本（fail-closed）。
# LANE1（2026-10-21 横跳事故复盘）：sha256 只能证明「下载的脚本与它的
# .sha256 自洽」，证明不了「这份资产就是目标 Release 的定版脚本」；
# 且旧比对只判字符串不等、无方向。镜像缓存混杂/定版注入失效时，
# 未定版副本 + latest 逐次翻转可让两个版本互相「更新」无限横跳，
# latest 回退时还会把脚本静默降级。三条不变量收口（任一不过即跳过
# 本次自更新、继续用当前脚本，fail-closed）：
#   1) 定版一致：下载副本的 PINNED_VERSION 必须等于本次解析的目标
#      版本——资产不是该 Release 的定版脚本就不接管；
#   2) 方向单调：仅当下载副本 SCRIPT_VERSION（YYYYMMDD.NN 定宽格式，
#      字典序即时间序）严格大于当前版才换；格式不可辨认同样拒绝
#      （无法确认方向就不动）；
#   3) 单跳上限：换版 exec 时置 FLMX_SELF_UPDATE_HOPS=1，新进程据此
#      不再自更新——任何未知翻转模式下至多跳一次。
# 注：调用点在 main() 入口（见文件末尾 main 头注 F-006）——此处
# 仅定义函数，因为 resolve_version / 路由助手 / download_verified
# 定义在后面；放 main 内也是刻意为之：测试夹具以「截断到 main 之
# 前 + source」方式加载函数，顶层调用会误入夹具，切勿移回顶层。
self_update_check() {
  # 通过进程替换 bash <(...) 执行时 $0 不是常规文件，跳过自更新；
  # 被 source 加载（测试夹具）时 BASH_SOURCE 与 $0 不一致，同样跳过。
  if [ ! -f "$0" ] || [ "${BASH_SOURCE[0]}" != "$0" ]; then
    return 0
  fi
  if [ "${FLMX_SELF_UPDATE_HOPS:-0}" != "0" ]; then
    return 0
  fi
  if [ "${FLMX_VERIFY_DOWNLOADS:-1}" != "1" ]; then
    echo "⚠️ 下载校验未开启，跳过脚本自更新（不执行未校验的新脚本）"
    return 0
  fi
  local version tmp_script latest_version pinned_version
  version=$(resolve_version 2>/dev/null) || {
    echo "⚠️ 无法解析目标版本，跳过脚本自更新"
    return 0
  }
  tmp_script=$(mktemp) || return 0
  # 1.7.7：自更新下载同样走内置路线直选（download_verified_routed），
  # F-006 三不变量（定版一致/方向单调/单跳）不变。
  if ! download_verified_routed "https://github.com/${REPO}/releases/download/${version}/panel_install.sh" "$tmp_script"; then
    rm -f "$tmp_script"
    echo "⚠️ 新版脚本下载或校验失败，继续使用当前版本"
    return 0
  fi
  latest_version=$(grep -m1 '^SCRIPT_VERSION=' "$tmp_script" | cut -d'"' -f2 || true)
  if [ -z "$latest_version" ] || [ "$latest_version" = "$SCRIPT_VERSION" ]; then
    rm -f "$tmp_script"
    return 0
  fi
  pinned_version=$(grep -m1 '^PINNED_VERSION=' "$tmp_script" | cut -d'"' -f2 || true)
  if [ "$pinned_version" != "$version" ]; then
    rm -f "$tmp_script"
    echo "⚠️ 下载的脚本未定版到 v$version（PINNED_VERSION=${pinned_version:-空}），跳过脚本自更新"
    return 0
  fi
  if ! [[ "$SCRIPT_VERSION" =~ ^[0-9]{8}\.[0-9]{2}$ && "$latest_version" =~ ^[0-9]{8}\.[0-9]{2}$ ]]; then
    rm -f "$tmp_script"
    echo "⚠️ 脚本版本号格式不可辨认（当前 v$SCRIPT_VERSION / 目标 v$latest_version），无法确认更新方向，跳过脚本自更新"
    return 0
  fi
  if [[ ! "$latest_version" > "$SCRIPT_VERSION" ]]; then
    rm -f "$tmp_script"
    echo "⚠️ 目标脚本 v$latest_version 不高于当前 v$SCRIPT_VERSION，跳过脚本自更新（防降级）"
    return 0
  fi
  echo "🔄 检测到新版本脚本 (v$latest_version，当前 v$SCRIPT_VERSION)，正在更新..."
  # R9-04: 覆写改原子——旧实现 cat 直写 $0，重定向在写出任何字节
  # 前先截断，覆写失败/中断（ENOSPC、写错、掉电）即把当前脚本清
  # 零，回执的「继续使用当前版本」与事实相悖。现先写同目录临时
  # 文件、与已校验的下载副本逐字比对（cmp）、再 rename 换名；任
  # 一步失败 $0 逐字不动，回执才成立。临时文件与 $0 同目录，保证
  # rename 同文件系统原子生效。
  local self_tmp=""
  self_tmp=$(mktemp "$(dirname "$0")/.panel_install.sh.selfupdate.XXXXXX" 2>/dev/null) || self_tmp=""
  if [[ -n "$self_tmp" ]] \
    && cat "$tmp_script" > "$self_tmp" \
    && cmp -s "$tmp_script" "$self_tmp" \
    && { chmod --reference="$0" "$self_tmp" 2>/dev/null || chmod +x "$self_tmp"; } \
    && mv -f "$self_tmp" "$0"; then
    rm -f "$tmp_script"
    echo "✅ 已更新到 v$latest_version，重新执行..."
    FLMX_SELF_UPDATE_HOPS=1 exec bash "$0" "$@"
  fi
  rm -f "$tmp_script"
  if [[ -n "$self_tmp" ]]; then
    rm -f "$self_tmp"
  fi
  echo "⚠️ 自动更新失败，继续使用当前版本"
}

# 固定版本号（Release 构建时自动填充，留空则获取最新版）
PINNED_VERSION=""

# GitHub 下载路线配置（1.7.7）：默认走内置候选测速直选（见下方
# PROXY_CANDIDATES 与 ensure_download_routes），不再交互询问、不再
# 读面板的「GitHub 加速」设置（该设置已整体移除）。环境变量仅作
# 运维逃生口：PROXY_ENABLED=false 强制直连；PROXY_URL=<前缀> 为
# 手动覆盖，固定单条代理前缀路线。
PROXY_ENABLED="${PROXY_ENABLED:-}"
PROXY_URL="${PROXY_URL:-}"
# 现行缺省容器名：与 compose 模板的回退默认逐字一致
# （${PANEL_*_CONTAINER:-flmx-panel-*}），新装与升级改名后的部署同名。
DEFAULT_PANEL_BACKEND_CONTAINER="flmx-panel-backend"
DEFAULT_PANEL_POSTGRES_CONTAINER="flmx-panel-postgres"
# 旧名识别集（改名过渡期保留，只加不删、最后退场）：改名前的缺省名，
# 仅用于探测/归属判定/存量同代推导与升级改名的触发判定，绝不再作
# 为新装或新状态的赋值来源。存量部署在升级流内默认改名为现行名
# （见 apply_panel_identity_rename），改名只动容器名、不动卷/网络/数据。
LEGACY_PANEL_BACKEND_CONTAINER="flux-panel-backend"
LEGACY_PANEL_POSTGRES_CONTAINER="flux-panel-postgres"
LEGACY_PANEL_FRONTEND_CONTAINER="vite-frontend"
# 共存身份（C2-01/C2-06）：新装使用本面板专属资源名，不再与同机其他
# 面板的缺省名相撞；专属名同时是实例派生名的基名（-<实例 id>）。
NEW_PANEL_BACKEND_CONTAINER="flmx-panel-backend"
NEW_PANEL_FRONTEND_CONTAINER="flmx-panel-frontend"
NEW_PANEL_POSTGRES_CONTAINER="flmx-panel-postgres"
PANEL_SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

# ---- 内置 GitHub 下载候选与测速直选（1.7.7）----
# 候选清单全产品统一（面板后端 github_routes.go、agent 侧、
# install.sh 同表同序，改一处须同步其余）：9 个代理前缀 + 直连
# （""）。路线选择口径（与面板侧一致）：
#   1) 先单独探测直连（真实目标、~3-5s）：通 → 只用直连，不测代理；
#   2) 直连不通 → 并行探测 9 个代理按延迟排名，未判活的按清单原
#      序殿后，直连排最末兜底；已选路线下载失败时逐个续切；
#   3) 仅直连已定（阶段 1）而下载全败时，补测代理排名续切
#      （extend_download_routes）。
# 判活只认真实目标可解析：探测 releases/latest 的最终重定向末段
# 必须长得像版本号，任意 HTTP 200 落地页不算活。
PROXY_CANDIDATES=(
  "https://ghfast.top"
  "https://gcode.hostcentral.cc"
  "https://gh-proxy.com"
  "https://ghproxy.net"
  "https://hub.gitmirror.com"
  "https://ghps.cc"
  "https://gh.llkk.cc"
  "https://ghp.ci"
  "https://ghproxy.cc"
)

route_mode() {
  # L2-33（R5-FD）：PROXY_ENABLED 改大小写不敏感——False/FALSE/0 与
  # false 同义。旧实现仅精确匹配小写 "false"，运维逃生口误写大小写
  # 时静默落入内置选路且无任何告警；无法识别的非空值在 stderr 点名
  # 后按启用处理，不静默吞掉。
  local proxy_enabled_lc
  proxy_enabled_lc=$(printf '%s' "${PROXY_ENABLED:-}" | tr 'A-Z' 'a-z')
  if [[ "$proxy_enabled_lc" == "false" || "$proxy_enabled_lc" == "0" ]]; then
    echo "direct"
  elif [[ -n "$PROXY_URL" ]]; then
    echo "override"
  else
    if [[ -n "$proxy_enabled_lc" && "$proxy_enabled_lc" != "true" && "$proxy_enabled_lc" != "1" ]]; then
      echo "⚠️ PROXY_ENABLED 取值无法识别（\"${PROXY_ENABLED:-}\"），按启用内置选路处理（仅 false/0 可关闭）" >&2
    fi
    echo "builtin"
  fi
}

normalize_proxy_prefix() {
  local proxy="$1"
  if [[ "$proxy" == https://* || "$proxy" == http://* ]]; then
    proxy="${proxy%/}"
  else
    proxy="https://${proxy%/}"
  fi
  echo "$proxy"
}

route_url() {
  local prefix="$1" url="$2"
  if [[ -z "$prefix" ]]; then
    echo "$url"
  else
    echo "${prefix}/${url}"
  fi
}

# probe_route <前缀>：探测单条路线，活则回显耗时秒数、死则失败。
probe_route() {
  local prefix="$1"
  local out effective tag
  out=$(curl -fsSL -o /dev/null --connect-timeout 3 --max-time 5 -w '%{time_total} %{url_effective}' "$(route_url "$prefix" "https://github.com/${REPO}/releases/latest")" 2>/dev/null) || return 1
  effective="${out#* }"
  tag="${effective##*/}"
  [[ "$tag" =~ ^[0-9][0-9A-Za-z.\-]*$ ]] || return 1
  printf '%s' "${out%% *}"
}

PROXY_RANKED=()
PROXY_RANKED_LATENCY=()

# rank_proxy_routes：并行探测全部代理候选（单候选超时 ~5s、一轮
# 总界有界），活者按延迟升序、未判活者按清单原序殿后，结果写入
# PROXY_RANKED（只含代理，不含直连）；PROXY_RANKED_LATENCY 与之
# 并行记录实测延迟（未判活者为空串），供慢直连按延迟并入排名。
rank_proxy_routes() {
  PROXY_RANKED=()
  PROXY_RANKED_LATENCY=()
  local tmpdir i prefix
  tmpdir=$(mktemp -d) || return 1
  for i in "${!PROXY_CANDIDATES[@]}"; do
    prefix="${PROXY_CANDIDATES[$i]}"
    (
      t=$(probe_route "$prefix") && printf '%s %s\n' "$t" "$i" > "$tmpdir/$i"
    ) &
  done
  wait
  local line idx
  while IFS= read -r line; do
    idx="${line##* }"
    PROXY_RANKED+=("${PROXY_CANDIDATES[$idx]}")
    PROXY_RANKED_LATENCY+=("${line% *}")
  done < <(cat "$tmpdir"/* 2>/dev/null | sort -n)
  rm -rf "$tmpdir"
  local seen=" ${PROXY_RANKED[*]} "
  for prefix in "${PROXY_CANDIDATES[@]}"; do
    case "$seen" in
      *" $prefix "*) ;;
      *) PROXY_RANKED+=("$prefix"); PROXY_RANKED_LATENCY+=("") ;;
    esac
  done
}

# direct_latency_within_gate <秒>：与 Go 侧 routes.go 同口径——直连
# 探测延迟 ≤800ms 才配短路独占；shell 浮点比较走 awk。
direct_latency_within_gate() {
  awk -v t="$1" 'BEGIN { exit !(t + 0 <= 0.8) }'
}

# routes_insert_slow_direct <直连实测延迟秒>：须在 rank_proxy_routes
# 之后调用。把慢直连按实测延迟插入已排名路线表（与 Go 侧口径一
# 致：慢直连不丢弃、按延迟参与排序；未判活代理无延迟值，直连排在
# 已判活段末尾、未判活段之前）。结果写入 DOWNLOAD_ROUTES，
# ROUTES_STAGE 置 2。
routes_insert_slow_direct() {
  local direct_latency="$1"
  DOWNLOAD_ROUTES=()
  local i lat inserted=0
  for i in "${!PROXY_RANKED[@]}"; do
    lat="${PROXY_RANKED_LATENCY[$i]:-}"
    if [[ "$inserted" == "0" ]] && { [[ -z "$lat" ]] || awk -v a="$direct_latency" -v b="$lat" 'BEGIN { exit !(a + 0 <= b + 0) }'; }; then
      DOWNLOAD_ROUTES+=("")
      inserted=1
    fi
    DOWNLOAD_ROUTES+=("${PROXY_RANKED[$i]}")
  done
  if [[ "$inserted" == "0" ]]; then
    DOWNLOAD_ROUTES+=("")
  fi
  ROUTES_STAGE=2
}

DOWNLOAD_ROUTES=()
ROUTES_STAGE=0

# ensure_download_routes：懒初始化路线表（只在真正下载/解析版本时
# 触发，纯本地操作不触网）。状态行走 stderr，避免污染 $(...) 捕获。
ensure_download_routes() {
  if [[ ${#DOWNLOAD_ROUTES[@]} -gt 0 ]]; then
    return 0
  fi
  case "$(route_mode)" in
    direct)
      DOWNLOAD_ROUTES=("")
      ROUTES_STAGE=2
      ;;
    override)
      DOWNLOAD_ROUTES=("$(normalize_proxy_prefix "$PROXY_URL")")
      ROUTES_STAGE=2
      ;;
    *)
      local direct_latency=""
      if direct_latency=$(probe_route ""); then
        if direct_latency_within_gate "$direct_latency"; then
          DOWNLOAD_ROUTES=("")
          ROUTES_STAGE=1
          echo "🌐 GitHub 下载路线: 直连" >&2
        else
          # L2-35（R5-FD，与 install.sh 的 L2-06 同族）：慢直连不得
          # 短路独占——按实测延迟并入代理排名，与 Go 侧 routes.go
          # 的 800ms 门同口径。旧实现对面板侧直连只判成败，一条
          # 「探针 2.9s」的慢直连也会被恒排首位。
          rank_proxy_routes
          routes_insert_slow_direct "$direct_latency"
          echo "🌐 GitHub 直连较慢（${direct_latency}s 超过 0.8s 门限），按测速排名选路（首选: ${DOWNLOAD_ROUTES[0]:-直连}）" >&2
        fi
      else
        rank_proxy_routes
        DOWNLOAD_ROUTES=("${PROXY_RANKED[@]}" "")
        ROUTES_STAGE=2
        echo "🌐 GitHub 直连不通，按测速排名选用代理路线（首选: ${DOWNLOAD_ROUTES[0]}）" >&2
      fi
      ;;
  esac
}

# extend_download_routes：阶段 1（仅直连）路线全败时补测代理排名
# 并追加；有新增路线返回 0，否则返回 1。
extend_download_routes() {
  if [[ "$(route_mode)" == "builtin" && "$ROUTES_STAGE" == "1" ]]; then
    rank_proxy_routes
    DOWNLOAD_ROUTES+=("${PROXY_RANKED[@]}")
    ROUTES_STAGE=2
    return 0
  fi
  return 1
}

resolve_latest_release_tag() {
  local effective_url tag api_tag latest_url api_url tag_pattern

  # J-02: 渠道分流（与 Go 侧 releaseChannelFromTag / stableVersionPattern
  # 同口径）。稳定渠道只接受纯数字 tag；FLMX_RELEASE_CHANNEL=dev|beta
  # 时接受任意数字开头 tag（beta 渠道）。默认稳定渠道，永不解析出 beta；
  # beta 须显式 VERSION 指定或切 dev/beta 渠道安装。
  case "${FLMX_RELEASE_CHANNEL:-stable}" in
    dev|beta) tag_pattern='^[0-9][0-9A-Za-z.\-]*$' ;;
    *) tag_pattern='^[0-9]+(\.[0-9]+)+$' ;;
  esac

  latest_url="https://github.com/${REPO}/releases/latest"
  api_url="https://api.github.com/repos/${REPO}/releases/latest"

  # 1.7.7：逐路线尝试（每条路线内先重定向法、后 API 法），首个解析
  # 出合格 tag 的路线即用；路线全败时按直选口径补测/续切一次。
  # L2-06③（R5-FD）：整轮总量上限（默认 120s，FLMX_RESOLVE_BUDGET_SECONDS
  # 可调）——旧实现每路线重定向法 30s＋API 法 30s 串行，11 线路＋
  # extend 一轮可达 11 分钟以上且无上限。逐次尝试按剩余预算收紧
  # --max-time，耗尽即整轮失败返回。
  local resolve_deadline=$(( $(date +%s) + ${FLMX_RESOLVE_BUDGET_SECONDS:-120} ))
  ensure_download_routes
  local prefix remain max_time
  while :; do
    for prefix in "${DOWNLOAD_ROUTES[@]}"; do
      remain=$(( resolve_deadline - $(date +%s) ))
      if (( remain <= 0 )); then
        echo "❌ 解析最新版本超时（总预算 ${FLMX_RESOLVE_BUDGET_SECONDS:-120}s 已耗尽）" >&2
        return 1
      fi
      max_time=30
      if (( remain < max_time )); then max_time=$remain; fi
      effective_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' -L --connect-timeout 5 --max-time "$max_time" "$(route_url "$prefix" "$latest_url")" 2>/dev/null || true)
      tag="${effective_url##*/}"
      # H-24: 无 Release 时 /releases/latest 会 302 到 /releases 索引页，
      # 此时解析出的是垃圾值 "releases" 而非版本号，必须按版本格式校验。
      if [[ "$tag" =~ $tag_pattern ]]; then
        echo "$tag"
        return 0
      fi

      remain=$(( resolve_deadline - $(date +%s) ))
      if (( remain <= 0 )); then
        echo "❌ 解析最新版本超时（总预算 ${FLMX_RESOLVE_BUDGET_SECONDS:-120}s 已耗尽）" >&2
        return 1
      fi
      max_time=30
      if (( remain < max_time )); then max_time=$remain; fi
      api_tag=$(curl -fsSL --connect-timeout 5 --max-time "$max_time" "$(route_url "$prefix" "$api_url")" 2>/dev/null | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/' || true)
      if [[ "$api_tag" =~ $tag_pattern ]]; then
        echo "$api_tag"
        return 0
      fi
    done
    extend_download_routes || break
  done

  return 1
}

resolve_version() {
  if [[ -n "${VERSION:-}" ]]; then
    echo "$VERSION"
    return 0
  fi
  if [[ -n "${FLMX_VERSION:-}" ]]; then
    echo "$FLMX_VERSION"
    return 0
  fi
  if [[ -n "${FLUX_VERSION:-}" ]]; then
    echo "$FLUX_VERSION"
    return 0
  fi
  if [[ -n "${PINNED_VERSION:-}" ]]; then
    echo "$PINNED_VERSION"
    return 0
  fi

  if resolve_latest_release_tag; then
    return 0
  fi

  echo "❌ 无法获取最新版本号。你可以手动指定版本，例如：VERSION=<版本号> ./panel_install.sh" >&2
  return 1
}

# A11-132-03: 点分数字版本比较（与 self_update_check 的防降级口径对齐）。
# 回显 -1/0/1 表示 a 小于/等于/大于 b；任一侧为空或含非数字段时不可比，
# 回显空并返回 1——调用方不得据此拦截（开发/测试标签等场景保持放行）。
panel_version_cmp() {
  local a="${1#v}" b="${2#v}"
  local -a pa pb
  local i n x y
  if [[ -z "$a" || -z "$b" ]]; then
    return 1
  fi
  IFS='.' read -ra pa <<< "$a"
  IFS='.' read -ra pb <<< "$b"
  n=${#pa[@]}
  if (( ${#pb[@]} > n )); then
    n=${#pb[@]}
  fi
  for (( i = 0; i < n; i++ )); do
    x="${pa[i]:-0}"
    y="${pb[i]:-0}"
    if ! [[ "$x" =~ ^[0-9]+$ && "$y" =~ ^[0-9]+$ ]]; then
      return 1
    fi
    if (( 10#$x < 10#$y )); then
      echo -1
      return 0
    fi
    if (( 10#$x > 10#$y )); then
      echo 1
      return 0
    fi
  done
  echo 0
}

# 根据版本号设置 compose 下载地址（1.7.7：只存 canonical 地址，
# 路线前缀在 download_verified_routed 实际下载时按路线表拼）
set_compose_urls_by_version() {
  local version="$1"
  DOCKER_COMPOSEV4_URL="https://github.com/${REPO}/releases/download/${version}/docker-compose-v4.yml"
  DOCKER_COMPOSEV6_URL="https://github.com/${REPO}/releases/download/${version}/docker-compose-v6.yml"
}

ensure_compose_urls_initialized() {
  if [[ -n "${DOCKER_COMPOSEV4_URL:-}" && -n "${DOCKER_COMPOSEV6_URL:-}" ]]; then
    return 0
  fi

  RESOLVED_VERSION=$(resolve_version) || return 1
  set_compose_urls_by_version "$RESOLVED_VERSION"
}



# 根据IPv6支持情况选择docker-compose URL
get_docker_compose_url() {
  if check_ipv6_support > /dev/null 2>&1; then
    echo "$DOCKER_COMPOSEV6_URL"
  else
    echo "$DOCKER_COMPOSEV4_URL"
  fi
}

# 检查 docker-compose 或 docker compose 命令。
# 默认（安装路径）：未安装则自动安装；且命令在场之外还要验守护进程
# 可达（R7-02，见函数尾）。
# noinstall（更新/迁移/卸载路径）：未检测到 Docker 时直接返回失败——没有
# Docker 就不可能有容器化的面板部署，绝不为这些操作反向安装 Docker。
check_docker() {
  local noinstall="${1:-}"
  if command -v docker-compose &> /dev/null; then
    DOCKER_CMD="docker-compose"
  elif command -v docker &> /dev/null; then
    if docker compose version &> /dev/null; then
      DOCKER_CMD="docker compose"
    else
      echo "错误：检测到 docker，但不支持 'docker compose' 命令。请安装 docker-compose 或更新 docker 版本。"
      exit 1
    fi
  elif [[ "$noinstall" == "noinstall" ]]; then
    return 1
  else
    echo "⚠️  未检测到 Docker，正在自动安装..."
    install_docker || {
      echo "错误：Docker 自动安装失败，请手动安装后重试。"
      echo "  手动安装：curl -fsSL https://get.docker.com | sh"
      exit 1
    }
    # 安装后重新检测
    if command -v docker &> /dev/null && docker compose version &> /dev/null; then
      DOCKER_CMD="docker compose"
    elif command -v docker-compose &> /dev/null; then
      DOCKER_CMD="docker-compose"
    else
      echo "错误：Docker 安装后仍无法使用，请检查安装日志。"
      exit 1
    fi
  fi
  # R7-02: 安装路径上「检测到命令」不等于 Docker 可用——CLI 与 compose
  # 子命令在场时守护进程可能根本没起（兜底装后未启 daemon、或存量安
  # 装的 daemon 被停）。在此以 docker info（与 docker_ipv6_usable 同
  # 一探活口径）判 daemon 可达，不可达即点名失败，不许以「已安装」
  # 放行到 install_panel 的 up＋90 秒健康等待、以误导性根因收场。
  # noinstall 路径不查：其 compose 操作会在首个命令处直接吃到 docker
  # 自己的 daemon 报错、根因不被掩盖，卸载也不应被 daemon 状态卡死。
  if [[ "$noinstall" != "noinstall" ]] && command -v docker &> /dev/null; then
    if ! docker info &> /dev/null; then
      echo "错误：Docker 已安装但守护进程（daemon）不可达，容器操作无法执行。"
      echo "  请先启动 Docker 服务后重试：systemd 发行版执行 systemctl enable --now docker；Alpine 执行 rc-service docker start。"
      exit 1
    fi
  fi
  echo "检测到 Docker 命令：$DOCKER_CMD"
}

# F-050: Docker 官方安装脚本的 pin 校验值（get.docker.com 脚本，
# 2026-10-21 实测抓取计算；官方脚本变更后须人工更新此值）。旧实现
# curl|sh 不经任何校验以 root 执行，与本文件对自家资产的 fail-closed
# 标准双标。现改为：下载到临时文件 → sha256 与 pin 值比对 → 通过才
# 执行；校验不过/无校验值/缺 sha256sum 一律不执行，回退系统包管理器
# 签名通道。阿里云镜像脚本暂无可信 pin 值（其 URL 2026-10-21 实测
# 404），默认不执行；确需使用时经 FLMX_DOCKER_INSTALL_SHA256 显式
# 提供该来源脚本的 sha256（该变量对两个来源都可覆盖 pin 值）。
DOCKER_INSTALL_SCRIPT_SHA256="7f966774378d94acf1817eb1aba213c21b576d01d1ee2f0656fe36e75ffa1258"

run_verified_installer_script() {
  local url="$1"
  local expected="$2"
  if [[ -z "$expected" ]]; then
    echo "⚠️ 未配置该来源的脚本校验值，跳过脚本安装: $url"
    return 1
  fi
  if ! command -v sha256sum >/dev/null 2>&1; then
    echo "⚠️ 缺少 sha256sum，无法校验安装脚本，跳过脚本安装: $url"
    return 1
  fi
  local tmp actual rc
  tmp=$(mktemp) || return 1
  # A1-02：与 download_verified 同套看门狗（L2-11 口径）——旧实
  # 现裸 curl 无超时/重试，get.docker.com 连通后停滞（TCP 开、
  # 零/涓流字节）时本函数永不返回，install_docker 的包管理器
  # 兜底永不触发、首装无限挂死。脚本体积小，总界取 120s。
  if ! curl -fsSL --retry 2 --retry-delay 2 --connect-timeout 10 --speed-limit 1024 --speed-time 30 --max-time 120 "$url" -o "$tmp"; then
    rm -f "$tmp"
    echo "⚠️ 安装脚本下载失败: $url"
    return 1
  fi
  actual=$(sha256sum "$tmp" | awk '{print $1}')
  if [[ "$actual" != "$expected" ]]; then
    rm -f "$tmp"
    echo "❌ 安装脚本校验失败（期望 $expected，实际 $actual），拒绝执行: $url"
    return 1
  fi
  echo "✅ 安装脚本校验通过: $url"
  sh "$tmp"
  rc=$?
  rm -f "$tmp"
  return $rc
}

# R7-02: 「装好」的口径是守护进程可用、不只是包装上。包管理器兜底
# 三支此前装完即返回成功：dnf/yum、apk 按发行惯例装包不自启服务，
# 装后复检又只验 CLI，daemon 不在的「成功」一路走到面板首装的健康
# 检查、空等 90 秒才以误导性根因收场。启动动作在此单点收口，官方
# 脚本路径与兜底三支共用：systemd 发行版 enable --now（已在运行时
# 幂等无副作用；Debian 的 docker.io 本由包脚本在 systemd 下自启，
# 显式再启同属无害）；Alpine 传 openrc——rc-update 入默认运行级＋
# rc-service 启动（与 install.sh 的 OpenRC 服务惯例一致）。
start_docker_daemon() {
  if [ "${1:-}" = "openrc" ]; then
    rc-update add docker default 2>/dev/null || true
    rc-service docker start 2>/dev/null || true
  else
    systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true
  fi
}

# 自动安装 Docker（支持 Debian/Ubuntu、RHEL 系、Alpine）
install_docker() {
  echo "🔧 正在安装 Docker..."

  # 优先使用官方一键脚本（自动识别发行版，经 F-050 pin 校验后才执行）
  # 国内服务器可设置 DOCKER_MIRROR=aliyun 使用阿里云镜像加速
  local docker_url="https://get.docker.com"
  local docker_script_sha="$DOCKER_INSTALL_SCRIPT_SHA256"
  if [ "${DOCKER_MIRROR:-}" = "aliyun" ]; then
    docker_url="https://mirrors.aliyun.com/docker-ce/linux/docker-install.sh"
    docker_script_sha=""
  fi
  docker_script_sha="${FLMX_DOCKER_INSTALL_SHA256:-$docker_script_sha}"

  if command -v curl &> /dev/null; then
    if run_verified_installer_script "$docker_url" "$docker_script_sha"; then
      echo "✅ Docker 安装完成"
      # 启动并设置开机自启
      start_docker_daemon
      return 0
    fi
  fi

  echo "官方脚本安装失败或未通过校验，尝试系统包管理器..."
  # R6-01: 兜底包名必须真实可得。旧实现 Debian 支装 docker-compose-
  # plugin、RHEL 支装 docker＋docker-compose-plugin——这些包名只在
  # Docker 官方源里、发行版默认源不提供，apt/dnf 对无法定位的包整
  # 条命令失败，连同支的引擎本体也装不上，兜底必然失败。现口径：
  # Debian/Ubuntu 用发行源内的 docker.io＋docker-compose-v2（旧发
  # 行版只有 v1 包名 docker-compose 时退到它；两个包名提供的都是
  # docker-compose 二进制，check_docker 先认 docker-compose、再认
  # docker compose 子命令，两形态都可被检出）；RHEL 系默认源既无
  # 引擎也无插件，先落 Docker 官方 yum 源（repo 文件自带
  # gpgcheck=1、包经 Docker 发布密钥验签）再装官方包名。装后还须
  # 启动守护进程才算装好，见 start_docker_daemon（R7-02）。
  if [ -f /etc/debian_version ]; then
    apt-get update && { apt-get install -y docker.io docker-compose-v2 || apt-get install -y docker.io docker-compose; } && { start_docker_daemon; return 0; }
  elif [ -f /etc/redhat-release ]; then
    if command -v dnf &> /dev/null; then
      dnf install -y dnf-plugins-core && \
        dnf config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo && \
        dnf install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin && { start_docker_daemon; return 0; }
    else
      yum install -y yum-utils && \
        yum-config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo && \
        yum install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin && { start_docker_daemon; return 0; }
    fi
  elif [ -f /etc/alpine-release ]; then
    apk add --no-cache docker docker-compose && { start_docker_daemon openrc; return 0; }
  fi

  return 1
}

# 检测系统是否支持 IPv6
check_ipv6_support() {
  echo "🔍 检测 IPv6 支持..."

  # 检查是否有 IPv6 地址（排除 link-local 地址）
  if ip -6 addr show | grep -v "scope link" | grep -q "inet6"; then
    echo "✅ 检测到系统支持 IPv6"
    return 0
  elif ifconfig 2>/dev/null | grep -v "fe80:" | grep -q "inet6"; then
    echo "✅ 检测到系统支持 IPv6"
    return 0
  else
    echo "⚠️ 未检测到 IPv6 支持"
    return 1
  fi
}



# 配置 Docker 启用 IPv6
configure_docker_ipv6() {
  echo "🔧 配置 Docker IPv6 支持..."

  # 检查操作系统类型
  OS_TYPE=$(uname -s)

  if [[ "$OS_TYPE" == "Darwin" ]]; then
    # macOS 上 Docker Desktop 已默认支持 IPv6
    echo "✅ macOS Docker Desktop 默认支持 IPv6"
    return 0
  fi

  # Docker daemon 配置文件路径
  DOCKER_CONFIG="/etc/docker/daemon.json"

  # 检查是否需要 sudo
  if [[ $EUID -ne 0 ]]; then
    SUDO_CMD="sudo"
  else
    SUDO_CMD=""
  fi

  # 检查 Docker 配置文件
  if [ -f "$DOCKER_CONFIG" ]; then
    # 检查是否已经配置了 IPv6
    if grep -q '"ipv6"' "$DOCKER_CONFIG"; then
      echo "✅ Docker 已配置 IPv6 支持"
    else
      echo "📝 更新 Docker 配置以启用 IPv6..."
      # 备份原配置
      $SUDO_CMD cp "$DOCKER_CONFIG" "${DOCKER_CONFIG}.backup"

      # 使用 jq 或 sed 添加 IPv6 配置
      if command -v jq &> /dev/null; then
        $SUDO_CMD jq '. + {"ipv6": true, "fixed-cidr-v6": "fd00::/80"}' "$DOCKER_CONFIG" > /tmp/daemon.json && $SUDO_CMD mv /tmp/daemon.json "$DOCKER_CONFIG"
      else
        # 如果没有 jq，使用 sed
        $SUDO_CMD sed -i 's/^{$/{\n  "ipv6": true,\n  "fixed-cidr-v6": "fd00::\/80",/' "$DOCKER_CONFIG"
        # AUDIT-R1 (SCR-009): 该正则只匹配"整行仅为 {" 的多行 JSON；单行压缩
        # JSON 无匹配时 sed 静默无操作但退出码为 0。写后由下方统一校验兜底。
      fi

      # F-052: 写后校验（jq 与 sed 两条路径都过这里）：配置里没有
      # "ipv6": true 就算失败并返回非 0，让安装路径据此降级 v4，
      # 不再带着未生效的配置按 v6 继续（旧行为只告警）。
      if ! $SUDO_CMD grep -q '"ipv6"[[:space:]]*:[[:space:]]*true' "$DOCKER_CONFIG" 2>/dev/null; then
        echo "❌ IPv6 配置写入失败，请手动在 $DOCKER_CONFIG 添加 \"ipv6\": true 后重试"
        return 1
      fi

      echo "🔄 重启 Docker 服务..."
      restart_docker_checked || return 1
      sleep 5
    fi
  else
    # 创建新的配置文件
    echo "📝 创建 Docker 配置文件..."
    $SUDO_CMD mkdir -p /etc/docker
    echo '{
  "ipv6": true,
  "fixed-cidr-v6": "fd00::/80"
}' | $SUDO_CMD tee "$DOCKER_CONFIG" > /dev/null

    echo "🔄 重启 Docker 服务..."
    restart_docker_checked || return 1
    sleep 5
  fi
}

# F-052: 重启 Docker 并检查结果。重启失败/无可用重启方式时返回非 0，
# 让调用方能据此降级，而不是带着未生效的配置继续。
restart_docker_checked() {
  : "${SUDO_CMD:=}"
  if command -v systemctl &> /dev/null; then
    $SUDO_CMD systemctl restart docker
  elif command -v service &> /dev/null; then
    $SUDO_CMD service docker restart
  else
    echo "⚠️ 未找到 systemctl/service，无法自动重启 Docker，请手动重启后重试"
    return 1
  fi
}

# F-052: 验证 Docker IPv6 是否真的可用：daemon 可达且默认 bridge 网络
# 启用了 IPv6。主机有 IPv6 地址不等于 Docker 能建 v6 网络（daemon 未
# 启用、重启失败都会让 v6 compose 在建网一步才炸），选版前先证实。
docker_ipv6_usable() {
  docker info >/dev/null 2>&1 || return 1
  local bridge_v6
  bridge_v6=$(docker network inspect bridge --format '{{.EnableIPv6}}' 2>/dev/null || echo "")
  [[ "$bridge_v6" == "true" ]]
}

# R4B11-001: 抽出 Caddyfile 中「指定域名站点块之外」的全部内容，供菜单 5
# 保留式重写：其他站点块（含菜单 6 追加的订阅前置块）、全局选项块与块外
# 行（注释/import 等）逐字保留，仅剔除地址命中 domain 的站点块（支持与
# 菜单 6 幂等检查相同的多地址行与 scheme/端口写法）。按花括号深度切块，
# 结构不完整的尾部内容按保留处理，宁可多留不可误删。
caddyfile_without_site() {
  local file="$1" domain="$2"
  awk -v d="$domain" '
    BEGIN { dl = tolower(d) }
    function flush_block(   line, n, parts, i, tok, keep) {
      if (buf == "") return
      keep = 1
      if (first != "{") {
        line = first
        sub(/\{[[:space:]]*$/, "", line)
        n = split(line, parts, /[[:space:],]+/)
        for (i = 1; i <= n; i++) {
          tok = parts[i]
          sub(/^https?:\/\//, "", tok)
          sub(/:[0-9]+$/, "", tok)
          # L2-31（R5-FD）：域名比较大小写不敏感（DNS 本就不分大小
          # 写）——旧实现字节比较，已存小写时输入大写会漏判、把同
          # 一站点块再写一份。
          if (tolower(tok) == dl) { keep = 0; break }
        }
      }
      if (keep) printf "%s", buf
      buf = ""
    }
    {
      line = $0
      tmp = line; opens = gsub(/\{/, "{", tmp)
      tmp = line; closes = gsub(/\}/, "}", tmp)
      if (!inblock) {
        if (opens > 0) {
          first = line; buf = line "\n"; depth = opens - closes
          if (depth <= 0) flush_block(); else inblock = 1
        } else {
          print line
        }
      } else {
        buf = buf line "\n"
        depth += opens - closes
        if (depth <= 0) { flush_block(); inblock = 0 }
      }
    }
    END { if (inblock) printf "%s", buf }
  ' "$file"
}

# L2-29（R5-FD）：菜单 5/6 的系统前置检查——这两处菜单的 Caddy 安
# 装走 apt 体系、重启/判活硬编码 systemctl，系统兼容面远窄于同脚本
# install_docker 的 Debian/RHEL/Alpine 多发行版支持面。旧实现无任何
# 前置检查、失败点落在流程中段（菜单 6 此时已完成备份与追加，菜单
# 5 还会先动 apt 源）。写任何文件之前先 fail-fast 并点名受限面。
caddy_menu_prereqs() {
  if ! command -v systemctl >/dev/null 2>&1; then
    echo "❌ 本功能依赖 systemd（用 systemctl 重启/判活 Caddy），当前系统不支持，操作未执行（未修改任何文件）"
    echo "   请按 doc/guide/install.md 在 Caddy 中手动添加该站点的反向代理配置"
    return 1
  fi
  return 0
}

# 仅菜单 5 在 Caddy 缺失、需要自动安装时调用（apt 体系限定）。
caddy_install_prereqs() {
  if ! command -v caddy >/dev/null 2>&1 && ! command -v apt-get >/dev/null 2>&1; then
    echo "❌ 未检测到 Caddy，且当前系统无 apt-get（本脚本仅支持在 Debian/Ubuntu 上自动安装 Caddy），操作未执行（未修改任何文件）"
    echo "   请先按发行版文档手动安装 Caddy 后重试"
    return 1
  fi
  return 0
}

# R6-03：菜单 5/6 的前端端口解析（原 SCR-003 两处同型候选链循环
# 的统一实现，两菜单共用）。旧实现候选链首项 ${PANEL_DEPLOY_DIR}/.env
# 在新进程中恒为空转（PANEL_DEPLOY_DIR 只有 resolve_panel_deployment
# 会设置、菜单 5/6 从不调用它），实际只剩 ./.env 一条有效候选：操
# 作者不在部署目录运行即全不中、无任何提示地按默认 6366 写
# Caddyfile 并报配置完成——同机共存的第二套面板改过前端端口时必
# 然写错且无从核对。现口径：
#   0) 调用方进程已预置 FRONTEND_PORT（环境变量/同进程前序步
#      骤）时先校验再采：合法（^[0-9]+$ 且 1-65535，与下方 .env
#      分支及安装/更新校验同口径）才直接采用；不合法则告警点名
#      并忽略预置值、续走下述定位＋候选链（R8-16：旧实现对此分
#      支零校验，abc/99999 亦静默采纳，可写出形如
#      reverse_proxy 127.0.0.1:abc 的坏块）；
#   1) 先按菜单 2/3 同口径做部署定位（resolve_panel_deployment 经
#      容器标签/显式目录定位、不依赖 cwd）。定位是只读取向：失败
#      不阻断本菜单，但必须撤销其半成品状态（PANEL_DEPLOY_DIR 与
#      已变更的 cwd），并落到第 3 步的显式告警，绝不带着未证实的
#      定位继续；
#   2) 再按 PANEL_DEPLOY_DIR → ./.env → /opt 两旧路径的候选链读
#      FRONTEND_PORT（合法端口才采，口径与旧循环一致）；
#   3) 仍未命中时显式告警并回退默认 6366（不得静默）；两菜单的
#      完成输出另打印实际反代端口供操作者核对（见各菜单成功块）。
resolve_caddy_frontend_port() {
  if [[ -n "${FRONTEND_PORT:-}" ]]; then
    # R8-16：预置值与其它三条端口来源同口径校验（正则＋范围，算术
    # 前 10# 强制十进制，同 get_config_params 口径）。合法才采；
    # 不合法告警点名后清掉预置、续走部署定位＋候选链，不静默采纳。
    if [[ "$FRONTEND_PORT" =~ ^[0-9]+$ ]] && (( 10#$FRONTEND_PORT >= 1 && 10#$FRONTEND_PORT <= 65535 )); then
      return 0
    fi
    echo "⚠️ 环境预置的 FRONTEND_PORT 不是有效端口（1-65535）：$FRONTEND_PORT，已忽略，改按部署 .env 解析"
    FRONTEND_PORT=""
  fi
  if [[ -z "${PANEL_DEPLOY_DIR:-}" ]]; then
    local _origin_dir="$PWD"
    if ! resolve_panel_deployment; then
      PANEL_DEPLOY_DIR=""
      cd "$_origin_dir" || true
      echo "⚠️ 部署定位未完成（见上方提示），改按候选路径查找部署 .env"
    fi
  fi
  local _env_file _port
  for _env_file in "${PANEL_DEPLOY_DIR:-}/.env" "./.env" "/opt/flmx-panel/.env" "/opt/flvx-panel/.env"; do
    [[ "$_env_file" == "/.env" ]] && continue
    # I-F1-05: 读值只剥空白、不剥引号，与更新面
    # validate_panel_update_environment 的原始读值统一判读——旧实
    # 现在此剥掉引号，引号包裹的 "7366" 被菜单面静默正常化采纳、
    # 却被更新面判「不是有效端口」终止更新，两面分歧。现两面同
    # 口径：引号包裹同为无效（菜单面点名后忽略该候选、更新面终止）。
    # 实现经 get_env_var_raw（R1-01-L2 后 get_env_var 已对齐
    # Compose 语义会剥引号，本严格面靠 raw 读面保住 I-F1-05 口径）。
    _port=$(get_env_var_raw "FRONTEND_PORT" "$_env_file" 2>/dev/null | tr -d ' \r\n\t' || true)
    if [[ "$_port" =~ ^[0-9]+$ ]] && (( 10#$_port >= 1 && 10#$_port <= 65535 )); then
      FRONTEND_PORT="$_port"
      echo "📁 从 $_env_file 读取前端端口：$FRONTEND_PORT"
      return 0
    fi
    if [[ -n "$_port" ]]; then
      echo "⚠️ $_env_file 中的 FRONTEND_PORT 不是有效端口（须为 1-65535 的纯数字，引号包裹同样无效）：$_port，已忽略该值"
    fi
  done
  FRONTEND_PORT=6366
  echo "⚠️ 未定位到部署 .env 或其中无 FRONTEND_PORT，将按默认前端端口 6366 生成反代配置"
  echo "   若本机面板的前端端口不是 6366（如同机共存的第二套面板改过端口），生成的配置会反代到无人监听的端口、站点打不开"
  echo "   修正方法：cd 到部署目录后重跑本菜单，或以 PANEL_DEPLOY_DIR=<部署目录> 指定部署目录后重试（自建镜像部署另需同时指定 PANEL_BACKEND_CONTAINER=<后端容器名>）"
}

# 配置域名反向代理（Caddy 自动 HTTPS）
setup_domain_proxy() {
  echo "🌐 配置域名反向代理"
  echo ""
  caddy_menu_prereqs || return 1
  read -p "请输入域名（如 panel.example.com）: " DOMAIN
  DOMAIN=$(echo "$DOMAIN" | tr -d ' \n\r\t')
  if [ -z "$DOMAIN" ]; then
    echo "❌ 域名不能为空"
    return 1
  fi
  # AUDIT-R1 (SCR-019): 动态证实 DOMAIN 中的 $()/反引号不构成命令注入
  #（heredoc 单遍展开），但非法字符串会原样写入 Caddyfile 导致 caddy
  # 重启失败。写文件前做字符集校验，非法输入直接拒绝。
  if [[ ! "$DOMAIN" =~ ^[A-Za-z0-9.*-]+$ ]]; then
    echo "❌ 域名含非法字符（仅允许字母/数字/./-/*）：$DOMAIN"
    return 1
  fi

  # 检查域名是否解析到本机
  echo "🔍 检查域名解析..."
  local resolved_ip resolved_ips
  resolved_ips=$(getent hosts "$DOMAIN" | awk '{print $1}' || true)
  resolved_ip=${resolved_ips%%$'\n'*}
  if [ -z "$resolved_ip" ]; then
    echo "⚠️  域名 $DOMAIN 未解析到任何 IP，请先配置 DNS 解析后再继续"
    read -p "仍要继续吗? (y/N): " _cont
    [[ "$_cont" =~ ^[Yy]$ ]] || return 1
  else
    # R6-04: 解析成功不等于指向本机。旧实现只要能解析就打 ✅ 并报
    # 配置完成，域名指到他人 IP 时同样绿灯——而 HTTPS 证书签发是
    # 异步的（HTTP-01 验证请求会打到那台机器上），签发失败不在本
    # 流程判据内，操作者拿到假成功信号。现与本机公网 IP 比对：
    # 解析集含本机公网 IP 才算指向本机；取不到本机公网 IP 时只陈
    # 述事实、不下结论；明确不含时按「未解析」同级处理（警告＋
    # 显式确认），不阻断但绝不再打 ✅。
    local _public_ip
    _public_ip=$(get_public_ip)
    if [[ "$_public_ip" == "服务器IP" ]]; then
      echo "ℹ️ 域名解析到: $resolved_ip（未能获取本机公网 IP，未能核对该解析是否指向本机，请自行确认）"
    elif grep -qx "$_public_ip" <<< "$resolved_ips"; then
      echo "✅ 域名解析到: $resolved_ip（已核对：解析指向本机）"
    else
      echo "⚠️  域名 $DOMAIN 解析到 $resolved_ip，与本机公网 IP $_public_ip 不一致——解析未指向本机"
      echo "   若继续，本菜单仍会写完配置并报完成，但 HTTPS 证书签发将失败（验证请求会打到 $resolved_ip 那台机器），本站点打不开"
      echo "   请先把该域名的 DNS 解析改到本机公网 IP $_public_ip 后再继续"
      read -p "仍要继续吗? (y/N): " _cont
      [[ "$_cont" =~ ^[Yy]$ ]] || return 1
    fi
  fi

  # 安装 Caddy（如未安装）。L2-29（R5-FD）：动 apt 源之前先过
  # apt 体系前置检查，非 Debian 系直接拒绝、不留半截源配置。
  caddy_install_prereqs || return 1
  if ! command -v caddy &> /dev/null; then
    echo "🔧 正在安装 Caddy..."
    apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl 2>/dev/null || true
    # F-050 变体（X1-407）：Caddy 官方源 GPG key 不再直灌 keyring——先落
    # 临时文件核对主指纹（65760C51…EA34，Caddy stable 发布密钥，
    # 2026-10-21 实测），不符即视为官方源不可用、走 Debian 仓库回退，
    # 避免用未核对的 key 当后续 apt 验签的信任锚。
    local caddy_key_tmp caddy_key_fp=""
    caddy_key_tmp=$(mktemp)
    if curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' -o "$caddy_key_tmp" 2>/dev/null; then
      caddy_key_fp=$(gpg --show-keys --with-colons "$caddy_key_tmp" 2>/dev/null | awk -F: '/^fpr:/{print $10; exit}')
    fi
    if [[ "$caddy_key_fp" == "65760C51EDEA2017CEA2CA15155B6D79CA56EA34" ]] \
      && gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg "$caddy_key_tmp" 2>/dev/null; then
      echo "✅ Caddy 官方源 GPG key 指纹校验通过"
    else
      echo "❌ Caddy 官方源 GPG key 获取失败或指纹不符，尝试 Debian 仓库版本..."
      apt-get update && apt-get install -y caddy || {
        rm -f "$caddy_key_tmp"
        echo "❌ Caddy 安装失败"
        return 1
      }
    fi
    rm -f "$caddy_key_tmp"
    if ! command -v caddy &> /dev/null; then
      # R8-J-D: 源列表先落临时文件，下载成功且非空才移入持久路径——
      # 旧实现未检查结果、吞 stderr 的 curl 直通 tee 写目标文件，
      # 失败/半截内容落盘并在运行结束后留存，持续污染本机后续所有
      # apt 操作（与上方 GPG key 的临时文件处置同形）。
      local caddy_list_tmp
      caddy_list_tmp=$(mktemp)
      # CADDY_LIST_PATH 仅供测试夹具覆盖（与 CADDYFILE_PATH 同钩子），
      # 默认即现行路径。
      local caddy_list_path="${CADDY_LIST_PATH:-/etc/apt/sources.list.d/caddy-stable.list}"
      local caddy_list_written=""
      # R9-05: 落盘前验内容形态——R8-J-D 只收到「非空才落盘」，HTTP
      # 200 的截断体（半截 deb 行）仍照常落盘：apt 从此对全机操作
      # 报 Malformed entry、菜单失败路径又从不清理，坏列表长期污
      # 染宿主。现要求至少含一行完整的 deb 条目（deb [可选项]
      # <uri> <suite> <组件>），不合形态与下载失败同口径：不落盘、
      # 走现有软件源兜底。
      if curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' -o "$caddy_list_tmp" 2>/dev/null && [[ -s "$caddy_list_tmp" ]] \
        && grep -Eq '^[[:space:]]*deb[[:space:]]+(\[[^]]*\][[:space:]]+)?[^[:space:]]+[[:space:]]+[^[:space:]]+[[:space:]]+[^[:space:]]+' "$caddy_list_tmp"; then
        if mv -f "$caddy_list_tmp" "$caddy_list_path" && chmod 644 "$caddy_list_path"; then
          caddy_list_written="$caddy_list_path"
        else
          rm -f "$caddy_list_tmp"
          echo "⚠️  Caddy 官方源列表写入 apt 源目录失败，未写入 apt 源文件，尝试使用现有软件源安装..."
        fi
      else
        rm -f "$caddy_list_tmp"
        echo "⚠️  Caddy 官方源列表下载失败或内容不完整（无完整的 deb 源条目），未写入 apt 源文件，尝试使用现有软件源安装..."
      fi
      apt-get update && apt-get install -y caddy || {
        # R9-05: 本次运行落盘的源列表若随即让安装失败，不得留盘
        # 持续污染本机后续全部 apt 操作——删除并点名（同函数
        # Caddyfile 失败路径的回滚口径）。
        if [[ -n "$caddy_list_written" ]]; then
          rm -f "$caddy_list_written"
          echo "❌ Caddy 安装失败，已删除本次写入的官方源列表 $caddy_list_written（避免坏源影响本机后续 apt 操作），请排查后重试"
        else
          echo "❌ Caddy 安装失败"
        fi
        return 1
      }
    fi
    echo "✅ Caddy 安装完成"
  else
    echo "✅ 检测到 Caddy 已安装"
  fi

  # 写 Caddyfile（用 127.0.0.1 避免 docker-proxy IPv6 转发问题）
  echo "📝 配置 Caddy..."
  # CADDYFILE_PATH 仅供测试夹具覆盖（与菜单 6 同钩子），默认即现行路径。
  local caddyfile="${CADDYFILE_PATH:-/etc/caddy/Caddyfile}"
  # AUDIT-R1 (SCR-018): caddy 手动二进制安装时 /etc/caddy 不存在，
  # 此处 cat 会 No such file or directory。先建目录（包管理器安装自带该目录，无害）。
  mkdir -p "$(dirname "$caddyfile")"
  # 备份已有配置（保留备份路径供下方 validate/restart 失败回滚使用）
  local caddyfile_bak=""
  if [ -f "$caddyfile" ]; then
    caddyfile_bak="${caddyfile}.bak.$(date +%s)"
    cp "$caddyfile" "$caddyfile_bak"
    echo "🗄️  已备份现有配置到 $caddyfile_bak"
  fi
  # AUDIT-R1 (SCR-003)＋R6-03: 菜单 5 是独立流程，不经过
  # get_config_params，FRONTEND_PORT 恒未设置。统一走
  # resolve_caddy_frontend_port：先部署定位、再读部署 .env 的实际
  # 前端端口，未命中显式告警后才回退 6366（旧两处同型循环已删除）。
  resolve_caddy_frontend_port
  # R4B11-001: 保留式写入。旧实现整文件覆盖，会静默抹掉菜单 6 已追加的
  # 订阅前置站点块（先 6 后 5 的运维序列下订阅前置随即失效、仅存于 .bak）。
  # 改为按站点块合并：先从刚备份的旧文件中抽出面板域名之外的全部内容，
  # 写完新的面板块后逐字附回，与菜单 6 的追加语义对齐。
  local preserved_blocks=""
  if [ -n "$caddyfile_bak" ]; then
    preserved_blocks=$(caddyfile_without_site "$caddyfile_bak" "$DOMAIN")
  fi
  cat > "$caddyfile" <<EOF
$DOMAIN {
    reverse_proxy 127.0.0.1:${FRONTEND_PORT:-6366}
}
EOF
  if [ -n "$preserved_blocks" ]; then
    printf '%s\n' "$preserved_blocks" >> "$caddyfile"
    echo "♻️ 已保留原配置中的其他站点块（订阅前置等配置不受本次修改影响）"
  fi

  # R4B11-002: 与菜单 6 同口径（SCR-020）先校验后生效：validate 不过即
  # 回滚备份且不 restart；restart 本身失败时不得被 set -e 在下方
  # is-active 检查之前吞掉——用 if 守卫接住返回码，失败即回滚坏文件、
  # 尝试以旧配置恢复服务，并落到日志指引分支。
  if command -v caddy &> /dev/null; then
    if ! caddy validate --config "$caddyfile" > /dev/null 2>&1; then
      if [ -n "$caddyfile_bak" ]; then
        echo "❌ Caddy 配置校验失败，已回滚到 $caddyfile_bak ，未做任何生效变更"
        cp "$caddyfile_bak" "$caddyfile"
      else
        echo "❌ Caddy 配置校验失败，未做任何生效变更"
        rm -f "$caddyfile"
      fi
      return 1
    fi
    echo "✅ Caddy 配置校验通过"
  fi

  if ! systemctl restart caddy; then
    echo "❌ Caddy 重启失败，正在回滚到之前的配置..."
    if [ -n "$caddyfile_bak" ]; then
      cp "$caddyfile_bak" "$caddyfile"
    else
      rm -f "$caddyfile"
    fi
    systemctl restart caddy || true
    echo "❌ Caddy 启动失败，请检查日志: journalctl -u caddy -n 50"
    return 1
  fi
  sleep 3

  if systemctl is-active --quiet caddy; then
    echo ""
    echo "🎉 域名配置完成"
    echo "🌐 访问地址: https://$DOMAIN"
    echo "🔌 实际反代端口: 127.0.0.1:${FRONTEND_PORT:-6366}（请核对与面板前端端口一致，不一致说明端口解析未命中部署 .env）"
    echo "📜 HTTPS 证书由 Caddy 自动申请和续期"
    echo ""
    echo "⚠️  重要：请在面板 → 网站配置 中将「面板后端地址」改为 https://$DOMAIN"
    echo "   否则节点无法通过域名回连面板"
  else
    # L2-28（R5-FD）：restart 返回 0 但 is-active 失败（服务起来又
    # 死）同样回滚——旧实现此分支只有日志指引，坏配置留盘、下次
    # 重启仍加载、面板站点持续不可用且无人提示文件已被改动。与
    # 上方 validate/restart 失败分支同口径：回滚备份并尝试以旧
    # 配置恢复服务。无备份（此前无 Caddyfile）时删除新写文件。
    echo "❌ Caddy 重启后未处于运行状态，正在回滚到之前的配置..."
    if [ -n "$caddyfile_bak" ]; then
      cp "$caddyfile_bak" "$caddyfile"
    else
      rm -f "$caddyfile"
    fi
    systemctl restart caddy || true
    echo "❌ Caddy 启动失败，请检查日志: journalctl -u caddy -n 50"
    return 1
  fi
}

# 配置订阅前置域名（Caddy 自动 HTTPS）：为订阅拉取另设一个前置域名，
# 与面板域名反代到同一前端端口，配合 Cloudflare 橙云承接订阅拉取，
# 绕开部分运营商对面板域名的阻断/烂路由。源站侧只是在现有 Caddyfile
# 上追加一个站点块（手工流程已在生产验证：追加→自动签证书→直连 200）。
# SCR-020: 与菜单 5 的整文件覆盖不同，本函数是追加语义：必须幂等
# （同名站点块已存在即拒绝重复追加）且先校验后生效（caddy validate
# 不过即回滚备份、不 restart），否则重复运行会写出同名站点块、坏配置
# 会在 restart 时连面板域名的反代一起打断。
setup_subscription_front_domain() {
  echo "🌐 配置订阅前置域名"
  echo ""
  caddy_menu_prereqs || return 1
  read -p "请输入订阅前置域名（如 sub.example.com）: " SUB_DOMAIN
  SUB_DOMAIN=$(echo "$SUB_DOMAIN" | tr -d ' \n\r\t')
  if [ -z "$SUB_DOMAIN" ]; then
    echo "❌ 域名不能为空"
    return 1
  fi
  # 字符集校验同 SCR-019：非法字符串会原样写入 Caddyfile 导致 caddy
  # 加载失败，写文件前直接拒绝。
  if [[ ! "$SUB_DOMAIN" =~ ^[A-Za-z0-9.*-]+$ ]]; then
    echo "❌ 域名含非法字符（仅允许字母/数字/./-/*）：$SUB_DOMAIN"
    return 1
  fi

  # 前置检查：前置域名复用面板的 Caddy 与前端端口，要求菜单 5 已跑过
  # （Caddyfile 存在且至少含一个站点块）。CADDYFILE_PATH 仅供测试夹具
  # 覆盖，默认即现行硬编码路径，菜单 5 与本函数共用同一钩子。
  local caddyfile="${CADDYFILE_PATH:-/etc/caddy/Caddyfile}"
  if [ ! -f "$caddyfile" ]; then
    echo "❌ 未找到 $caddyfile ，请先运行菜单 5「配置域名反向代理（自动 HTTPS）」后再配置订阅前置域名"
    return 1
  fi
  if ! awk '/\{[[:space:]]*$/ { line = $0; sub(/\{[[:space:]]*$/, "", line); if (line ~ /[^[:space:],]/) found = 1 } END { exit(found ? 0 : 1) }' "$caddyfile"; then
    echo "❌ $caddyfile 中未找到任何站点块，请先运行菜单 5「配置域名反向代理（自动 HTTPS）」"
    return 1
  fi
  # 幂等：输入域名已是任一站点块的地址（含面板域名本身、带 scheme/端口
  # 的写法）时拒绝重复追加。
  if awk -v d="$SUB_DOMAIN" 'BEGIN { dl = tolower(d) } /\{[[:space:]]*$/ { line = $0; sub(/\{[[:space:]]*$/, "", line); n = split(line, parts, /[[:space:],]+/); for (i = 1; i <= n; i++) { tok = parts[i]; sub(/^https?:\/\//, "", tok); sub(/:[0-9]+$/, "", tok); if (tolower(tok) == dl) found = 1 } } END { exit(found ? 0 : 1) }' "$caddyfile"; then
    echo "❌ 域名 $SUB_DOMAIN 已存在于 $caddyfile 的站点块中，请勿重复配置"
    return 1
  fi

  # 前端端口解析与菜单 5 完全一致（SCR-003＋R6-03）：统一走
  # resolve_caddy_frontend_port（先部署定位、再读部署 .env，未命
  # 中显式告警后才回退 6366；旧两处同型循环已删除）。
  resolve_caddy_frontend_port

  # 写前备份（命名沿用菜单 5），追加站点块而非整文件覆盖，原有站点
  # 块（面板域名等）逐字保留。
  echo "📝 配置 Caddy..."
  local caddyfile_bak="${caddyfile}.bak.$(date +%s)"
  cp "$caddyfile" "$caddyfile_bak"
  echo "🗄️  已备份现有配置到 $caddyfile_bak"
  cat >> "$caddyfile" <<EOF
$SUB_DOMAIN {
    reverse_proxy 127.0.0.1:${FRONTEND_PORT:-6366}
}
EOF

  # SCR-020: 先校验后生效。validate 不过即回滚备份并退出，此时尚未
  # restart，正在运行的 Caddy 仍用旧配置，不受影响。caddy 不在 PATH
  # 时跳过校验，靠下方 restart + is-active 兜底（同菜单 5）。
  if command -v caddy &> /dev/null; then
    if ! caddy validate --config "$caddyfile" > /dev/null 2>&1; then
      echo "❌ Caddy 配置校验失败，已回滚到 $caddyfile_bak ，未做任何生效变更"
      cp "$caddyfile_bak" "$caddyfile"
      return 1
    fi
    echo "✅ Caddy 配置校验通过"
  fi

  # R4B11-002: restart 失败不得被 set -e 在下方 is-active 检查之前吞掉
  # （原实现的日志指引分支在 restart 返回非 0 时永不可达）。用 if 守卫
  # 接住返回码，失败即回滚到追加前的备份、尝试以旧配置恢复服务，并
  # 落到日志指引分支——与上方 validate 失败回滚同口径。
  if ! systemctl restart caddy; then
    echo "❌ Caddy 重启失败，正在回滚到之前的配置..."
    cp "$caddyfile_bak" "$caddyfile"
    systemctl restart caddy || true
    echo "❌ Caddy 启动失败，请检查日志: journalctl -u caddy -n 50"
    return 1
  fi
  sleep 3

  if systemctl is-active --quiet caddy; then
    echo ""
    echo "🎉 订阅前置域名配置完成"
    echo "🌐 前置地址: https://$SUB_DOMAIN"
    echo "🔌 实际反代端口: 127.0.0.1:${FRONTEND_PORT:-6366}（请核对与面板前端端口一致，不一致说明端口解析未命中部署 .env）"
    echo "📜 HTTPS 证书由 Caddy 自动申请和续期"
    echo ""
    echo "⚠️  后续步骤（缺一不可）："
    echo "   1）Cloudflare 加 A 记录：进入该域名 →「DNS」→「记录」→「添加记录」，类型选 A；"
    echo "      「名称」只填子域名部分（相对于托管区，如托管区为 example.com、前置域名为"
    echo "      sub.example.com 时填 sub）；IPv4 地址填本机公网 IP（与面板域名指向的 IP 相同）；"
    echo "      「代理状态」先关（仅 DNS、灰云），待 Caddy 证书签发完成（https://$SUB_DOMAIN 能打开）"
    echo "      后，再回到此处改为「已代理」（橙云）。"
    echo "      ⚠️ 前置域名必须是一级子域名（如 sub.example.com）：不要用二级子域名"
    echo "      （如 cloud.dy.example.com）——Cloudflare 免费版边缘证书只覆盖一级子域名，"
    echo "      二级子域名开橙云后证书与域名不匹配，客户端会报「证书无效/安全连接失败」。"
    echo "   2）Cloudflare 建缓存绕过规则：进入该域名 →「缓存」→「Cache Rules」→「创建规则」，"
    echo "      名称随意（如「订阅不缓存」）；匹配条件：字段选「URI 路径」，运算符选「通配符」、"
    echo "      值填 /api/v1/open_api/sub/*（或运算符选「以…开头」、值填 /api/v1/open_api/sub/ ，两者等效）；"
    echo "      下方「缓存资格」选「绕过缓存（Bypass cache）」，保存并部署，避免订阅内容被 CDN 缓存。"
    echo "      ⚠️ 千万不要选「URI 完整（完整 URL）」字段填这个路径：它按整条 URL 全串匹配、"
    echo "      永不命中，规则会静默失效。"
    echo "      可选收紧：再用 And 加一个「主机名 等于 $SUB_DOMAIN」的条件，把规则只作用于前置域名。"
    echo "   3）在面板里保存前置地址（以下四步缺一不可，漏了任何一步设置都不会生效）："
    echo "      ① 打开面板 → 左侧菜单进入「网站配置」页 → 找到「订阅前置地址」卡片；"
    echo "      ② 点卡片里的「添加前置地址」按钮——该卡片初始没有任何输入框，"
    echo "         必须先点这个按钮，页面上才会出现一行新的输入框；"
    echo "      ③ 在新出现的输入框中填入 https://$SUB_DOMAIN ：必须带 https:// 、"
    echo "         末尾不要斜杠、不要带任何路径；"
    echo "      ④ 点配置页上的「保存配置」按钮——只在输入框里填了内容、没点保存"
    echo "         是不会落库的，离开页面即丢失。保存成功后，订阅链接旁的地址"
    echo "         选择里应能看到 https://$SUB_DOMAIN ；看不到即未保存成功，请重做 ②–④。"
    echo "      完成后，订阅链接即可按该前置地址复制给用户。"
    echo "   4）自检：规则部署后，用任意可达网络执行"
    echo "      curl -sI <一条前置订阅链接> | grep -i cf-cache-status"
    echo "      显示 BYPASS 即缓存规则已生效；也可在拉不到订阅的网络下实拉一次订阅验证。"
  else
    # L2-28（R5-FD）：restart 返回 0 但 is-active 失败同样回滚追加
    # （同菜单 5 口径）——旧实现此分支只有日志指引，本次追加的坏
    # 配置会留盘、下次重启仍加载且无人提示文件已被改动。
    echo "❌ Caddy 重启后未处于运行状态，正在回滚到之前的配置..."
    cp "$caddyfile_bak" "$caddyfile"
    systemctl restart caddy || true
    echo "❌ Caddy 启动失败，请检查日志: journalctl -u caddy -n 50"
    return 1
  fi
}

# 显示菜单
show_menu() {
  echo "==============================================="
  echo "          面板管理脚本"
  echo "==============================================="
  echo "请选择操作："
  echo "1. 安装面板"
  echo "2. 更新面板"
  echo "3. 卸载面板"
  echo "4. 迁移到 PostgreSQL"
  echo "5. 配置域名反向代理（自动 HTTPS）"
  echo "6. 配置订阅前置域名（自动 HTTPS）"
  echo "7. 退出"
  echo "==============================================="
}

generate_random() {
  # F-120: 16 字符（约 95 bit）对 JWT_SECRET/PG 口令余量偏薄，提升到 32
  # 字符（约 190 bit）。字符集不变（URL 安全，进 DATABASE_URL 无需转义）。
  # INT-01: 不再写 `tr -dc … </dev/urandom | head -c32`——head 收满 32
  # 字节先退、tr 向已关闭的管道继续写吃 SIGPIPE，pipefail 上下文里命令
  # 替换赋值返回 141 被 set -e 带走（套件 #162 确定性复现）。改为定量
  # 取字节：head 只当生产者、tr 读到 EOF 才退出，全程无早退读者；过滤
  # 后不足 32 字符则循环补取。
  local out="" chunk
  while [ "${#out}" -lt 32 ]; do
    chunk=$(head -c 64 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9')
    out="${out}${chunk}"
  done
  printf '%s' "${out:0:32}"
}

upsert_env_var() {
  local file="$1"
  local key="$2"
  local value="$3"
  local tmp_file

  tmp_file=$(mktemp)
  if [ -f "$file" ]; then
    awk -v k="$key" -v v="$value" '
      BEGIN { found=0 }
      $0 ~ ("^" k "=") { print k "=" v; found=1; next }
      { print }
      END { if (!found) print k "=" v }
    ' "$file" > "$tmp_file"
  else
    printf '%s=%s\n' "$key" "$value" > "$tmp_file"
  fi

  mv "$tmp_file" "$file"
}

# .env 单值解析（与 Go 面 parseEnvFileValue 逐字同口径，R1-01-L2
# 对齐）：先 TrimSpace；带引号时取闭合引号内内容（双引号展开
# \n \r \t \\ \" \$ 转义、未知转义保留两字符，单引号不展开），
# 引号未闭合宽容回退原文；未引号时在「# 位于首位或前为空白」处
# 截断注释再 TrimSpace（abc#def 的 # 保留）。两点有意不跟
# Compose：不做 $ 变量展开（与 Go 面同口径）。
parse_env_file_value() {
  local v="$1"
  v="${v#"${v%%[![:space:]]*}"}"
  v="${v%"${v##*[![:space:]]}"}"
  if [[ "$v" == \"* || "$v" == \'* ]]; then
    local quote="${v:0:1}" rest="${v:1}" out="" i ch next
    for (( i=0; i<${#rest}; i++ )); do
      ch="${rest:i:1}"
      if [[ "$quote" == '"' && "$ch" == '\' ]]; then
        i=$((i+1))
        if (( i >= ${#rest} )); then
          break
        fi
        next="${rest:i:1}"
        case "$next" in
          n) out+=$'\n' ;;
          r) out+=$'\r' ;;
          t) out+=$'\t' ;;
          '\') out+='\' ;;
          '"') out+='"' ;;
          '$') out+='$' ;;
          *) out+="\\$next" ;;
        esac
        continue
      fi
      if [[ "$ch" == "$quote" ]]; then
        printf '%s' "$out"
        return 0
      fi
      out+="$ch"
    done
    printf '%s' "$v"
    return 0
  fi
  local i ch prev=""
  for (( i=0; i<${#v}; i++ )); do
    ch="${v:i:1}"
    if [[ "$ch" == '#' ]] && { (( i == 0 )) || [[ "$prev" == ' ' || "$prev" == $'\t' ]]; }; then
      v="${v:0:i}"
      v="${v%"${v##*[![:space:]]}"}"
      break
    fi
    prev="$ch"
  done
  printf '%s' "$v"
}

# .env 读取（R1-01-L2 起与 Go 面 parseEnvFileValues 同语义）：
# 逐行扫描、重复键后现胜出、键名两侧空白裁剪、注释/空行跳过、
# 值经 parse_env_file_value 解析。旧实现 grep -m1 首现胜出且不
# 剥引号/注释/空白——手写 .env 的带引号卷键被读成含引号的真值
# （升级后自检恒不等、健康升级恒回滚）、PANEL_IDENTITY_RENAME
# 带引号时与 Go 面判定分家，皆为 RA-1 L-4 在 Go 面修过的同一形
# 态的脚本面镜像残留，本次对齐后两面同一 .env 必得同一读数。
get_env_var() {
  local key="$1"
  local file="${2:-.env}"

  if [[ ! -f "$file" ]]; then
    return 0
  fi

  local line trimmed k result="" found=0
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    trimmed="${line#"${line%%[![:space:]]*}"}"
    if [[ -z "$trimmed" || "$trimmed" == '#'* ]]; then
      continue
    fi
    if [[ "$line" != *"="* ]]; then
      continue
    fi
    k="${line%%=*}"
    k="${k#"${k%%[![:space:]]*}"}"
    k="${k%"${k##*[![:space:]]}"}"
    if [[ "$k" != "$key" ]]; then
      continue
    fi
    result=$(parse_env_file_value "${line#*=}")
    found=1
  done < "$file"

  if [[ "$found" == "1" ]]; then
    printf '%s' "$result"
  fi
}

# .env 原始读值（严格面专用）：行选择与 get_env_var 同口径（逐
# 行扫描、重复键后现胜出、键名裁剪、注释/空行跳过），但值只剥
# 两端空白，不剥引号、不截行尾注释——供已裁定「引号包裹即无
# 效」的两个端口判读面（I-F1-05：resolve_caddy_frontend_port 与
# validate_panel_update_environment 的端口校验）看原始形态：端
# 口在 .env 里就该是裸数字，引号形是笔误，两面统一点名拒绝而
# 不是静默正常化。其余一切读面走 get_env_var 的 Compose 对齐口
# 径（R1-01-L2），两读面不许混用。
get_env_var_raw() {
  local key="$1"
  local file="${2:-.env}"

  if [[ ! -f "$file" ]]; then
    return 0
  fi

  local line trimmed k result="" found=0
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    trimmed="${line#"${line%%[![:space:]]*}"}"
    if [[ -z "$trimmed" || "$trimmed" == '#'* ]]; then
      continue
    fi
    if [[ "$line" != *"="* ]]; then
      continue
    fi
    k="${line%%=*}"
    k="${k#"${k%%[![:space:]]*}"}"
    k="${k%"${k##*[![:space:]]}"}"
    if [[ "$k" != "$key" ]]; then
      continue
    fi
    result="${line#*=}"
    result="${result#"${result%%[![:space:]]*}"}"
    result="${result%"${result##*[![:space:]]}"}"
    found=1
  done < "$file"

  if [[ "$found" == "1" ]]; then
    printf '%s' "$result"
  fi
}

# DB_TYPE 归一：与后端接受口径逐字对齐——后端 New 与
# validatePostgresPassword 均先 TrimSpace＋ToLower 再匹配
# ""/sqlite/postgres/postgresql，别名 postgresql 与 postgres 等
# 价。脚本侧一切 DB_TYPE 判定先过本归一，杜绝「后端与文档都接
# 受、脚本更新却拒绝」的三方口径分家（B2-05）。
normalize_db_type() {
  local v="$1"
  v="${v#"${v%%[![:space:]]*}"}"
  v="${v%"${v##*[![:space:]]}"}"
  v=$(printf '%s' "$v" | tr '[:upper:]' '[:lower:]')
  if [[ "$v" == "postgresql" ]]; then
    v="postgres"
  fi
  printf '%s' "$v"
}

get_current_db_type() {
  local db_type database_url

  db_type=$(normalize_db_type "$(get_env_var "DB_TYPE" || true)")
  database_url=$(get_env_var "DATABASE_URL" || true)

  if [[ "$db_type" == "sqlite" ]]; then
    echo "sqlite"
  elif [[ "$db_type" == "postgres" || "$database_url" == postgres://* || "$database_url" == postgresql://* ]]; then
    echo "postgres"
  else
    echo "sqlite"
  fi
}

get_container_compose_label() {
  local container="$1"
  local label="$2"
  local value

  value=$(docker inspect -f "{{ index .Config.Labels \"$label\" }}" "$container" 2>/dev/null || true)
  if [[ "$value" == "<no value>" ]]; then
    value=""
  fi
  printf '%s' "$value"
}

# 本面板镜像统一发布在 ghcr.io/<仓库 owner>/ 下；同机其他面板（同源
# 旧名体系）的镜像不在此前缀下。这是共存判定的根：名字会撞，镜像
# 归属不会。
panel_image_prefix() {
  local owner="${REPO%%/*}"
  printf 'ghcr.io/%s/' "$(printf '%s' "$owner" | tr '[:upper:]' '[:lower:]')"
}

# 容器跑的镜像属于本面板才算自家容器。同名容器跑他人镜像时一律
# 视为不存在/不属于本面板，任何路径不得对其动手。
is_own_panel_container() {
  local image
  image=$(docker inspect -f '{{.Config.Image}}' "$1" 2>/dev/null || true)
  [[ -n "$image" && "$image" == "$(panel_image_prefix)"* ]]
}

# 从一行 image: 行文本中提取字段值（去首尾空白、引号与行尾注释）。
panel_compose_image_value() {
  local value="$1"
  value="${value#*image:}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  case "$value" in
    *" #"*) value="${value%% #*}"; value="${value%"${value##*[![:space:]]}"}" ;;
  esac
  if [[ ${#value} -ge 2 ]]; then
    case "$value" in
      \"*\") value="${value:1:${#value}-2}" ;;
      \'*\') value="${value:1:${#value}-2}" ;;
    esac
  fi
  printf '%s' "$value"
}

# 提取 Compose 文件中全部 image: 行的值（一行一个）。归属判定只认
# 这些字段值本身——注释、其他字符串里出现本面板镜像前缀不构成证
# 据（AI-04：旧实现全文 grep 子串，注释即可伪造归属、抹除他人判定）。
panel_compose_images() {
  local file="$1" line value
  [[ -f "$file" ]] || return 0
  while IFS= read -r line; do
    value=$(panel_compose_image_value "$line")
    [[ -n "$value" ]] && printf '%s\n' "$value"
  done < <(grep -E '^[[:space:]]*image:' "$file" 2>/dev/null || true)
}

# 提取 Compose 中指定服务段内的 image: 行值（一行一个）。解析口
# 径：顶层 services: 段内、与首个服务名同缩进的裸键为服务名，其
# 后更深缩进的行归当前服务；services: 之外（volumes/networks 等
# 顶层段）与注释行一律不算。
panel_compose_service_images() {
  local file="$1" want="$2"
  [[ -f "$file" ]] || return 0
  local line stripped indent in_services=0 svc_indent=-1 current="" value
  while IFS= read -r line || [[ -n "$line" ]]; do
    stripped="${line#"${line%%[! ]*}"}"
    [[ -z "$stripped" || "$stripped" == \#* ]] && continue
    indent=$(( ${#line} - ${#stripped} ))
    if (( indent == 0 )); then
      if [[ "$stripped" == "services:"* ]]; then in_services=1; else in_services=0; fi
      current=""
      continue
    fi
    (( in_services )) || continue
    if (( svc_indent < 0 )); then svc_indent=$indent; fi
    if (( indent == svc_indent )) && [[ "$stripped" =~ ^[A-Za-z0-9._-]+:([[:space:]]*#.*)?$ ]]; then
      current="${stripped%%:*}"
      continue
    fi
    if [[ "$current" == "$want" && "$stripped" == image:* ]]; then
      value=$(panel_compose_image_value "$stripped")
      [[ -n "$value" ]] && printf '%s\n' "$value"
    fi
  done < "$file"
}

# Compose 是否写有 image: 行（build: 型 Compose 无此行）。
panel_compose_has_image_refs() {
  [[ -f "$1" ]] && grep -Eq '^[[:space:]]*image:' "$1"
}

# Compose 的 backend 服务段的 image: 行中是否有任一值引用本面板
# 镜像（锚定解析：值逐行提取后做前缀比对——不用管道 grep，避免
# pipefail 下 grep -q 提前退出让写入方吃 SIGPIPE 把命中翻成未
# 命中）。归属证据只认 backend 段：随仓模板与内容校验都把后端
# 服务名钉死为 backend；其他服务（frontend/postgres/工具服务）
# 借用本面板已发布镜像不构成整套部署的归属证据——旧实现「任一
# 服务命中即判自家」会把他人 backend＋附属服务用我方镜像的混合
# Compose 判成自家（B2-02）。
panel_compose_references_own_image() {
  [[ -f "$1" ]] || return 1
  local images prefix line
  images=$(panel_compose_service_images "$1" backend)
  [[ -n "$images" ]] || return 1
  prefix=$(panel_image_prefix)
  while IFS= read -r line; do
    case "$line" in
      "$prefix"*) return 0 ;;
    esac
  done <<< "$images"
  return 1
}

# Compose 的 backend 服务段写有 image: 行、且无一引用本面板镜像
# ——镜像证据层面明确属于他人（与 panel_compose_references_own_
# image 同口径）。backend 段无 image: 行（build: 型）时镜像证据
# 不成立、不在此硬判，与 build: 型的既有口径一致。
panel_compose_references_only_foreign_images() {
  [[ -f "$1" ]] || return 1
  local images
  images=$(panel_compose_service_images "$1" backend)
  [[ -n "$images" ]] || return 1
  ! panel_compose_references_own_image "$1"
}

# 本面板后端容器名的已知全集：现行专属名、实例派生名（专属名-<实
# 例 id>）、旧缺省名（识别集）。注意旧缺省名与同机他方旧装的回退
# 值同值：它只能说明「不据此判他人」，单独出现不足以证明归属
# （AI-01）。实例派生名的后缀必须过 panel_instance_id_is_valid：
# 空后缀、非法字符、超 32 的名字任何安装都不可能产生，不计入已
# 知全集（B2-03）。
panel_backend_name_is_known_ours() {
  case "$1" in
    "$NEW_PANEL_BACKEND_CONTAINER"|"$DEFAULT_PANEL_BACKEND_CONTAINER"|"$LEGACY_PANEL_BACKEND_CONTAINER") return 0 ;;
    "$NEW_PANEL_BACKEND_CONTAINER"-*) panel_instance_id_is_valid "${1#"$NEW_PANEL_BACKEND_CONTAINER"-}" ;;
    *) return 1 ;;
  esac
}

# 专属名与实例派生名只有本面板新装会写，可作独立归属证据；旧缺省
# 名不算（与他方旧装同值，须有镜像证据 corroborate）。派生名后缀
# 的合法性要求同 panel_backend_name_is_known_ours（B2-03）。
panel_backend_name_is_exclusive_ours() {
  case "$1" in
    "$NEW_PANEL_BACKEND_CONTAINER") return 0 ;;
    "$NEW_PANEL_BACKEND_CONTAINER"-*) panel_instance_id_is_valid "${1#"$NEW_PANEL_BACKEND_CONTAINER"-}" ;;
    *) return 1 ;;
  esac
}

# 实例 id（AI-03）：同机多套本面板部署的身份区分因子。取值顺序：
# 环境变量 FLMX_INSTANCE > 部署 .env 的 FLMX_INSTANCE 键。默认实例
# （空）的一切派生名与历史逐字一致，存量部署无感。
panel_instance_id() {
  local id=""
  if [[ -n "${FLMX_INSTANCE:-}" ]]; then
    id="$FLMX_INSTANCE"
  else
    id=$(get_env_var FLMX_INSTANCE "${PANEL_DEPLOY_DIR:-.}/.env" || true)
  fi
  # 读路径同样过契约白名单（B2-03）：.env 被手写/植入非法值（空
  # 白、非法字符、超 32）时不得把垃圾 id 带进派生名——与后端
  # validPanelInstanceID 的回退口径对齐，视为默认实例（空）并告
  # 警。install 入口对环境变量形态另有硬拒绝，先于一切读路径。
  if [[ -n "$id" ]] && ! panel_instance_id_is_valid "$id"; then
    echo "⚠️ FLMX_INSTANCE 取值不合法（$id），已按默认实例处理；合法值须以小写字母或数字开头、仅含小写字母/数字/-/_、全长不超过 32" >&2
    id=""
  fi
  printf '%s' "$id"
}

panel_instance_suffix() {
  local id
  id=$(panel_instance_id)
  if [[ -n "$id" ]]; then
    printf -- '-%s' "$id"
  fi
}

# 实例 id 进入容器/卷/网络/项目名，只允许 Compose/容器命名安全字符。
# 本谓词与后端消费侧的实例 id 白名单（生成节点容器安装命令时校验）
# 是同一契约的两侧：字符集与全长上限（32）必须逐字一致，任一侧放
# 宽而另一侧不跟，脚本派生名与后端派生名即分家。
panel_instance_id_is_valid() {
  [[ ${#1} -le 32 && "$1" =~ ^[a-z0-9][a-z0-9_-]*$ ]]
}

# 部署目录的 Compose（只看 image: 行的值）引用本面板镜像，才认这
# 个目录在文件证据上属于本面板。.env 不作合取条件：只有单文件的
# 残缺部署同样要进入归属判定（AI-05）。
is_own_panel_deploy_dir() {
  panel_compose_references_own_image "$1/docker-compose.yml"
}

# 明确属于他人（任一成立即判；本面板镜像证据优先，Compose 的
# backend 段引用本面板镜像的目录一律不判他人）：
#   1) Compose 的 backend 段 image: 行无一引用本面板镜像（锚定解
#      析，段口径见 panel_compose_references_own_image/B2-02）；或
#   2) .env 声明的后端容器名不属于本面板任何已知命名（专属名/实
#      例派生名/旧缺省名之外），且 Compose 未引用本面板镜像。
# 完全无镜像引用、又无他人身份声明时归属未证，不在此硬判——调用
# 方按「归属未证」口径处理（安装默认拒绝、强制须先备份全部部署
# 文件；迁移与 resolve 按归属证据链不成立即终止）。
is_foreign_panel_deploy_dir() {
  if panel_compose_references_own_image "$1/docker-compose.yml"; then
    return 1
  fi
  if panel_compose_references_only_foreign_images "$1/docker-compose.yml"; then
    return 0
  fi
  local declared
  declared=$(get_env_var PANEL_BACKEND_CONTAINER "$1/.env" || true)
  [[ -n "$declared" ]] && ! panel_backend_name_is_known_ours "$declared"
}

# 同机其他实例的后端容器名（一行一个）：专属名派生形态
# flmx-panel-backend-<合法实例 id>，含已停止的容器（部署仍在、
# 同样构成管理目标歧义）。后缀须过实例 id 白名单——非法后缀的名
# 字不是任何实例的派生名，不计（与 B2-03 同口径）。
panel_other_instance_backend_containers() {
  local names name suffix
  names=$(docker ps -a --format '{{.Names}}' 2>/dev/null || true)
  [[ -n "$names" ]] || return 0
  while IFS= read -r name; do
    case "$name" in
      "${NEW_PANEL_BACKEND_CONTAINER}"-*)
        suffix="${name#"${NEW_PANEL_BACKEND_CONTAINER}"-}"
        if panel_instance_id_is_valid "$suffix"; then
          printf '%s\n' "$name"
        fi
        ;;
    esac
  done <<< "$names"
}

# 判定要管理的后端容器名。显式指定（环境变量或部署 .env 的
# PANEL_BACKEND_CONTAINER）优先，作为非标准镜像来源存量部署的逃生
# 口；未指定时：新名容器与旧名容器（须跑本面板镜像才认）同现则报错
# 让用户指定；只有旧名且属自家时沿用旧名；都没有时返回（按实例派
# 生的）新名（后续定位会以「找不到部署」失败，不会误认他人容器）。
detect_panel_backend_container() {
  local configured="${PANEL_BACKEND_CONTAINER:-}"
  if [[ -z "$configured" ]]; then
    configured=$(get_env_var PANEL_BACKEND_CONTAINER "${PANEL_DEPLOY_DIR:-.}/.env" || true)
  fi
  if [[ -n "$configured" ]]; then
    printf '%s' "$configured"
    return 0
  fi
  # 新装名按实例派生（AI-03）：取到实例 id 时探本实例的专属名，默
  # 认实例即现行专属名、与历史逐字一致。新名属本面板专属命名空
  # 间，按存在性认即可（停机中的自家容器也须能被定位；镜像归属由
  # 后续守卫复核）；旧名与同机他方旧装共用，须再验运行镜像才认。
  local new_name="${NEW_PANEL_BACKEND_CONTAINER}$(panel_instance_suffix)"
  local has_new="" has_legacy=""
  if docker inspect "$new_name" >/dev/null 2>&1; then
    has_new=1
  fi
  if docker inspect "$LEGACY_PANEL_BACKEND_CONTAINER" >/dev/null 2>&1 \
    && is_own_panel_container "$LEGACY_PANEL_BACKEND_CONTAINER"; then
    has_legacy=1
  fi
  # 多实例歧义（B2-04）：无实例上下文时探到默认实例容器、但同机
  # 另有实例派生名后端存在——从无关目录运行脚本的操作者多半意在
  # 管理某个实例部署，静默定位到默认实例会把 update/uninstall 作
  # 用到错误的一套上。与双名冲突同口径：报错并要求显式指定。
  if [[ -z "$(panel_instance_suffix)" && -n "$has_new" ]]; then
    local other_instances
    other_instances=$(panel_other_instance_backend_containers || true)
    if [[ -n "$other_instances" ]]; then
      echo "❌ 本机同时存在默认实例（$new_name）与其他实例部署（$(printf '%s' "$other_instances" | tr '\n' ' '))，无法自动判定要管理哪一套" >&2
      echo "   请通过环境变量 FLMX_INSTANCE 或 PANEL_BACKEND_CONTAINER 指定要管理的实例后重试" >&2
      return 1
    fi
  fi
  if [[ -n "$has_new" && -n "$has_legacy" ]]; then
    echo "❌ 同时发现两套面板后端容器（$new_name 与 $LEGACY_PANEL_BACKEND_CONTAINER），无法自动判定要管理哪一套" >&2
    echo "   请通过环境变量 PANEL_BACKEND_CONTAINER 指定要管理的实例后重试" >&2
    return 1
  fi
  if [[ -n "$has_legacy" ]]; then
    printf '%s' "$LEGACY_PANEL_BACKEND_CONTAINER"
    return 0
  fi
  printf '%s' "$new_name"
}

# 部署目录归属判定（$1=目录，$2=后端容器名）。证据链，任一成立即
# 判自家：
#   1) 该后端容器正运行本面板镜像（实物证据）；
#   2) 目录 Compose 的 backend 段 image: 行引用本面板镜像（文件
#      证据，锚定解析，注释/字符串不算，其他服务的命中只作旁证、
#      不单独成立，B2-02）；
#   3) .env 写有本面板专属后端名或实例派生名，且目录内无矛盾的镜
#      像证据（Compose 写有他人镜像时以镜像证据为准，此项不成立）。
# 旧实现的第一证据是「.env 存在任一非空 PANEL_BACKEND_CONTAINER
# 值」——他方部署每次安装都写这个键（其旧模板回退值甚至与本面板
# 旧缺省名同值），等于对他方全量放行（AI-01）；本面板旧版存量反
# 而不写这个键。现口径下：只验键存在不作数；旧缺省名因与他方旧
# 装同值、不作独立证据，必须有镜像证据 corroborate。自家非标准
# 镜像来源部署仍可经环境变量显式指定后端容器名放行（resolve 的
# backend_explicit 逃生口，不经本判定）。
panel_deploy_dir_is_ours() {
  if [[ -n "${2:-}" ]] && is_own_panel_container "$2"; then
    return 0
  fi
  if is_own_panel_deploy_dir "$1"; then
    return 0
  fi
  local declared
  declared=$(get_env_var PANEL_BACKEND_CONTAINER "$1/.env" || true)
  if [[ -n "$declared" ]] && panel_backend_name_is_exclusive_ours "$declared" \
    && ! panel_compose_references_only_foreign_images "$1/docker-compose.yml"; then
    return 0
  fi
  return 1
}

# 新装身份写入：把专属容器/卷/网络名写进 .env 并同步到当前进程变量。
# 实例派生（AI-03）：取到实例 id 时全部派生名统一加后缀 -<id>，同
# 机第二套本面板部署与第一套命名不相交；默认实例（无 id）写值与历
# 史逐字一致，存量与单实例部署无感。实例 id 同时落盘 FLMX_INSTANCE，
# 后续 resolve/detect/迁移按同一契约从 .env 读回。
# 落盘的 Compose 若是旧版模板（无 FLMX_SQLITE_VOLUME 标记，资源名仍
# 写死旧名），身份必须回退旧名才能与模板对上——装旧版也能跑；该分
# 支命名由旧模板写死，实例后缀不生效（会显式告警，不静默假装隔离）。
write_new_panel_identity() {
  local suffix legacy_template=""
  suffix=$(panel_instance_suffix)
  local backend frontend postgres sqlite_volume postgres_volume network project_name=""
  if [[ -f docker-compose.yml ]] && ! grep -Fq 'FLMX_SQLITE_VOLUME' docker-compose.yml; then
    legacy_template=1
    backend="$LEGACY_PANEL_BACKEND_CONTAINER" frontend="$LEGACY_PANEL_FRONTEND_CONTAINER" postgres="$LEGACY_PANEL_POSTGRES_CONTAINER"
    sqlite_volume="sqlite_data" postgres_volume="postgres_data" network="gost-network"
  else
    backend="${NEW_PANEL_BACKEND_CONTAINER}${suffix}" frontend="${NEW_PANEL_FRONTEND_CONTAINER}${suffix}" postgres="${NEW_PANEL_POSTGRES_CONTAINER}${suffix}"
    sqlite_volume="flmx-sqlite-data${suffix}" postgres_volume="flmx-postgres-data${suffix}" network="flmx-network${suffix}"
    project_name="flmx-panel${suffix}"
  fi
  upsert_env_var .env PANEL_BACKEND_CONTAINER "$backend"
  upsert_env_var .env PANEL_FRONTEND_CONTAINER "$frontend"
  upsert_env_var .env PANEL_POSTGRES_CONTAINER "$postgres"
  upsert_env_var .env FLMX_SQLITE_VOLUME "$sqlite_volume"
  upsert_env_var .env FLMX_POSTGRES_VOLUME "$postgres_volume"
  upsert_env_var .env FLMX_NETWORK "$network"
  # Compose 项目名同样用专属名（仅新模板分支；旧模板回退分支保持与
  # 旧脚本逐字一致的缺省项目名推导，不改动装旧版的行为）。
  if [[ -z "$legacy_template" ]]; then
    upsert_env_var .env COMPOSE_PROJECT_NAME "$project_name"
  fi
  local instance_id
  instance_id=$(panel_instance_id)
  if [[ -n "$instance_id" ]]; then
    upsert_env_var .env FLMX_INSTANCE "$instance_id"
    if [[ -n "$legacy_template" ]]; then
      echo "⚠️ 目标版本的 Compose 模板使用旧版共享资源名，实例后缀不生效：该实例的容器/卷/网络仍用旧缺省名，同机请勿再并存第二套该版本部署"
    fi
  fi
  PANEL_BACKEND_CONTAINER="$backend"
  PANEL_POSTGRES_CONTAINER="$postgres"
  PANEL_FRONTEND_CONTAINER="$frontend"
  export PANEL_BACKEND_CONTAINER PANEL_POSTGRES_CONTAINER PANEL_FRONTEND_CONTAINER
}

# 资源名读取（迁移路径用）：优先部署 .env 的新装身份，缺省回退旧名，
# 存量部署行为与改名前逐字一致。
panel_sqlite_volume_name() {
  local v
  v=$(get_env_var FLMX_SQLITE_VOLUME || true)
  printf '%s' "${v:-sqlite_data}"
}

panel_postgres_volume_name() {
  local v
  v=$(get_env_var FLMX_POSTGRES_VOLUME || true)
  printf '%s' "${v:-postgres_data}"
}

panel_network_name() {
  local v
  v=$(get_env_var FLMX_NETWORK || true)
  printf '%s' "${v:-gost-network}"
}

# 同代缺省推导：.env 缺 PG/前端身份键时，其实际容器名与后端同代
# ——后端已定为旧缺省名（存量旧装）则同取旧缺省，否则取现行缺省。
# 后端名由 detect/显式/.env 先行定谳（resolve 与迁移的填充序均如
# 此），同代推导据此成立；按名取容器的等待/停止面据此不会对存量
# 误用新名空等（F7-01 同族口径的延续）。
panel_sibling_default() {
  if [[ "${PANEL_BACKEND_CONTAINER:-}" == "$LEGACY_PANEL_BACKEND_CONTAINER" ]]; then
    printf '%s' "$1"
  else
    printf '%s' "$2"
  fi
}

# ---- 存量身份改名（升级流内默认执行，PANEL_IDENTITY_RENAME=0 可关，.env 与进程环境双通道、.env 优先，见 panel_identity_rename_enabled）----
# 触发面：部署 .env 的三个容器身份键任一缺失、或仍为旧缺省名
# （flux-panel-backend / flux-panel-postgres / vite-frontend）。定制
# 名（非旧非新）与实例派生名不触发；已是现行名的部署零动作。
# 动作序：先从尚存的旧容器实读、把卷/网络/项目缺失键钉为现行有
# 效值（实读依赖容器在册，必须在停删之前，RR4-F7-01）→ 按
# resolve 已定的实际名停并删将被改名的旧容器 → 把身份键写为现
# 行名（实例部署带后缀）→ 进程全局同步新名，交回更新流
# 按新名重建＋健康检查；失败走既有 rollback_panel_update（恢复旧
# .env＋旧 compose），其先行步按本函数记下的新旧名删新名容器、复
# 位全局，再按旧名拉起。钉键口径：卷/网络/项目缺失键钉为现行
# 有效值（容器挂载/网络实读，读不到按旧缺省）；两卷键已有值与
# 实读冲突时点名并以实读为准改写（批 D，见 panel_pin_missing_
# resource_keys 头注），网络/项目键仍只补缺——改名绝不主动换
# 卷、换网、动数据。改名状态经 PANEL_IDENTITY_* 全局传递。
PANEL_IDENTITY_RENAMED=""
PANEL_IDENTITY_OLD_BACKEND=""
PANEL_IDENTITY_OLD_FRONTEND=""
PANEL_IDENTITY_OLD_POSTGRES=""
PANEL_IDENTITY_NEW_BACKEND=""
PANEL_IDENTITY_NEW_FRONTEND=""
PANEL_IDENTITY_NEW_POSTGRES=""

# 停并删单个容器（改名/回滚共用）：新旧同名或容器不存在即跳过；
# 删除未成只告警——后续重建的健康检查与回滚会兜底暴露冲突。
panel_stop_remove_container() {
  local target="$1" counterpart="$2"
  [[ -n "$target" && "$target" != "$counterpart" ]] || return 0
  docker inspect "$target" >/dev/null 2>&1 || return 0
  docker stop -t 30 "$target" >/dev/null 2>&1 || true
  docker rm "$target" >/dev/null 2>&1 || true
  if docker inspect "$target" >/dev/null 2>&1; then
    echo "⚠️ 容器 $target 删除未成，后续重建可能与之冲突；更新失败将自动回滚" >&2
  fi
}

# 卷内文件探测：经卷挂载点在宿主机侧只读查看（脚本以 root 运行、
# 本地卷挂载点恒可读），不为探测拉起容器、不写卷。挂载点读不到
# 时按「无证据」返回 1，不臆断卷内有数据。
panel_volume_file_exists() {
  local volume="$1" relpath="$2" mountpoint=""
  mountpoint=$(docker volume inspect -f '{{.Mountpoint}}' "$volume" 2>/dev/null || true)
  [[ -n "$mountpoint" ]] || return 1
  [[ -f "$mountpoint/$relpath" ]]
}

# 卷内文件三态探测（A2-01 残面）：在上面的二值口径之外区分「确
# 定不在」与「未知」——挂载点取不到（inspect 无 Mountpoint、或挂
# 载点不是目录）时返回 2（未知），不把读不到说成没有。事故态门
# 这类 fail-closed 判定面用本函数：未知与「在」同处置（中止）；
# 其余只问在不在的消费面继续用二值版。返回：0=在，1=确定不在，
# 2=未知。
panel_volume_file_state() {
  local volume="$1" relpath="$2" mountpoint=""
  mountpoint=$(docker volume inspect -f '{{.Mountpoint}}' "$volume" 2>/dev/null || true)
  [[ -n "$mountpoint" && -d "$mountpoint" ]] || return 2
  [[ -f "$mountpoint/$relpath" ]] && return 0
  return 1
}

# 卷内小文件读取：与 panel_volume_file_exists 同通道（卷挂载点、
# 宿主机侧只读），读不到时输出空串、不报错。
panel_volume_file_read() {
  local volume="$1" relpath="$2" mountpoint=""
  mountpoint=$(docker volume inspect -f '{{.Mountpoint}}' "$volume" 2>/dev/null || true)
  [[ -n "$mountpoint" ]] || return 0
  [[ -f "$mountpoint/$relpath" ]] || return 0
  cat "$mountpoint/$relpath" 2>/dev/null || true
}

# FX-D（R1-12-01）：新装播种的管理员初始口令不再是公开字面量——
# 后端播种时随机生成并写入数据卷内一次性文件
# /app/data/initial-admin-password.txt（0600，容器内路径）。这里
# 经后端容器实读其 /app/data 数据卷名、再经卷挂载点读出该文件，
# 在安装完成输出中打印一次；读不到（装机时经环境变量
# FLMX_ADMIN_PASSWORD 指定口令——该路径不落文件，或卷读面不可
# 用）时只给文件位置与查法，绝不回退打印旧字面量口令。
print_initial_admin_credentials() {
  local backend_container="${PANEL_BACKEND_CONTAINER:-$DEFAULT_PANEL_BACKEND_CONTAINER}"
  local volume="" initial_password=""
  volume=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Name}}{{end}}{{end}}' "$backend_container" 2>/dev/null || true)
  if [[ -n "$volume" ]]; then
    initial_password=$(panel_volume_file_read "$volume" "initial-admin-password.txt")
    initial_password=$(printf '%s' "$initial_password" | tr -d '[:space:]')
  fi
  echo "💡 默认管理员账号: admin@user"
  if [[ -n "$initial_password" ]]; then
    echo "🔑 初始管理员密码（随机生成、仅在本次安装输出中明文显示）: $initial_password"
    echo "📄 该密码同时保存在后端数据卷的一次性文件 initial-admin-password.txt（容器内路径 /app/data/initial-admin-password.txt），首次登录修改密码后该文件自动删除、初始密码失效。"
  else
    echo "🔑 初始管理员密码未在此读出：若安装时设置了环境变量 FLMX_ADMIN_PASSWORD，初始密码即为该值；否则保存在后端数据卷的一次性文件 initial-admin-password.txt（容器内路径 /app/data/initial-admin-password.txt，后端启动日志会点名该路径，可用 docker logs $backend_container 查看）。"
  fi
  echo "⚠️  首次登录必须修改初始密码（改密完成前账号仅能访问改密页）！"
}

# 改名时把缺失的卷/网络/项目键钉为现行有效值：模板回退默认已翻
# 为新名，无键存量若不钉值，重建时将按新回退挂空卷/建新网（数据
# 还在旧卷、只是不可见）。有效值优先从真实容器实读（后端挂载的
# 数据卷名、所在网络名，F7-01「名字从真实容器推导」同口径），读
# 不到按该部署唯一可能的旧缺省；PG 卷无容器时查卷本身是否存在。
# 调用方须保证旧容器尚在册时调用：实读分支以容器存在为前提，容
# 器删后再调用只剩回退名（RR4-F7-01）。
# 卷键的冲突处置（RA2-S-01／RA-1 L-2，与迁移 E4 的「实读为准」
# 同口径）：键已存在但与在跑容器实读挂载不一致时，逐字信任错键
# 会让钉键与升级后自检同源于错值、挂错卷还报成功——故点名三元
# （键、旧值、实读值）并以实读为准改写；实读不到（容器不在/bind
# 挂载）则保留既有键，不臆断。网络/项目键无「实读胜键」语义（网
# 络名多值、项目名是标签口径），仍只补缺。
# 事故态门（RA2-S-01 变体）：无键＋实读到的后端卷恰为模板新缺
# 省 flmx-sqlite-data、且旧名卷 sqlite_data 存在并含 gost.db——
# 新装恒写身份键（write_new_panel_identity），无键却坐在新缺省
# 卷上、真库还在旧卷，只可能是 1.9.8 式事故后未救援的部署；此
# 时钉键会把事故态正式化、后续自检也以错卷为期望。故 fail-
# closed 中止并点名两卷与人工钉键方法，由人确认数据所在再钉。
# PG 格（R1-01-M1 补齐）：同谓词镜像到 PG 卷——无键＋PG 容器实
# 挂 flmx-postgres-data、且旧名卷 postgres_data 存在并含
# PG_VERSION（真实 PG 数据目录标记）→ 同标准中止点名。面×卷
# 四格至此全有门（本函数两格＋Go 面 planLegacyResourcePins 两
# 格，谓词同义、证据文件同构）。
# PG bind 中止（R1-01-L5，仅缺省 scope＋PG 模式）：PG 卷键已有
# 值、PG 容器在场但其数据挂载实读为空（bind 挂载）——键无从与
# 实读核对：旧行为逐字信键、重建落错键空卷还报成功（Go 面同形
# 是事后恒回滚）。统一为钉键前 fail-closed 中止并点名，与 Go
# 面 planLegacyResourcePins 同谓词同处置；缺键维持 postgres_
# data 回退认领的既有口径，scope=live（迁移）不设此中止（迁移
# 的 PG 容器是待建目标面、模式判定亦未切换）。
# 第三参 scope=live（迁移路径专用，RA2-S-02）：只钉两个数据卷
# 键、且只凭实读挂载写值（PG 的 postgres_data 卷存在性回退在
# 此 scope 下亦不写——卷在不等于卷属于本部署），不写回退缺省、
# 不碰网络/项目键。缘由：迁移的 .env 快照兼作回滚源、逐字恢复
# 是既有契约，无实证的写入会污染快照且对迁移的数据面毫无增
# 益；迁移真正需要的是把后端实挂的卷钉进快照，使最终/回滚
# up 不再落模板缺省空卷。更新/改名路径用缺省 scope（全量钉
# 键，含网络/项目键）；卷键在两 scope 下同口径——只凭实读挂
# 载写值，无实读不写（A2-03），缺省 scope 另加点名提示。
panel_pin_missing_resource_keys() {
  local backend_container="$1" postgres_container="$2" pin_scope="${3:-}"
  local sqlite_key sqlite_volume=""
  sqlite_key=$(get_env_var FLMX_SQLITE_VOLUME || true)
  sqlite_volume=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Name}}{{end}}{{end}}' "$backend_container" 2>/dev/null || true)
  if [[ -n "$sqlite_key" ]]; then
    if [[ -n "$sqlite_volume" && "$sqlite_volume" != "$sqlite_key" ]]; then
      echo "⚠️ 资源键冲突：.env 的 FLMX_SQLITE_VOLUME=$sqlite_key 与在跑后端 $backend_container 实际挂载的数据卷 $sqlite_volume 不一致；以实读为准改写为 $sqlite_volume（数据以实读为准，旧值不保留）"
      upsert_env_var .env FLMX_SQLITE_VOLUME "$sqlite_volume" || return 1
    fi
  else
    if [[ "$sqlite_volume" == "flmx-sqlite-data$(panel_instance_suffix)" ]] \
      && docker volume inspect sqlite_data >/dev/null 2>&1; then
      # A2-01 残面：旧卷内容探测走三态——挂载点不可读是「未知」，
      # 不是「无数据」：未知与确认有数据同处置（中止），不得按
      # 二值探测的 fail-open 越过事故态门把可能的事故态钉键正式化。
      local sqlite_old_state=0
      panel_volume_file_state sqlite_data gost.db || sqlite_old_state=$?
      if [[ "$sqlite_old_state" == "0" ]]; then
        echo "❌ 资源键钉值中止：在跑后端实际挂载的数据卷是新模板缺省名 $sqlite_volume，但部署 .env 没有 FLMX_SQLITE_VOLUME 键、且旧名卷 sqlite_data 存在并含有 gost.db——该部署可能正处在「挂新空卷、真库在旧卷」的事故形态，此时钉键会把事故态正式化。"
        echo "   请人工确认数据所在卷后，在 .env 中显式钉入正确卷名再重跑：数据在旧卷 → FLMX_SQLITE_VOLUME=sqlite_data；数据确在现行卷 → FLMX_SQLITE_VOLUME=$sqlite_volume。"
        return 1
      fi
      if [[ "$sqlite_old_state" == "2" ]]; then
        echo "❌ 资源键钉值中止：在跑后端实际挂载的数据卷是新模板缺省名 $sqlite_volume、部署 .env 又没有 FLMX_SQLITE_VOLUME 键，而旧名卷 sqlite_data 虽存在、其挂载点不可读，无法确认其中是否存有真库 gost.db——按未知处理，不冒「无数据」之名钉键正式化可能的事故态。"
        echo "   请先恢复 Docker 卷读面（docker volume inspect sqlite_data 应能给出可读的挂载点），或人工确认数据所在卷后，在 .env 中显式钉入正确卷名再重跑：数据在旧卷 → FLMX_SQLITE_VOLUME=sqlite_data；数据确在现行卷 → FLMX_SQLITE_VOLUME=$sqlite_volume。"
        return 1
      fi
    fi
    if [[ -n "$sqlite_volume" ]]; then
      upsert_env_var .env FLMX_SQLITE_VOLUME "$sqlite_volume" || return 1
    elif [[ "$pin_scope" != "live" ]]; then
      # A2-03：缺省 scope 与 live scope 同口径——实读为空不写卷
      # 键。旧实现写死回退名 sqlite_data：后端以 bind 挂载
      # /app/data 时卷名实读恒空，钉键会把 bind 部署改钉到同名
      # 命名卷（可能为空/陈旧），真数据被晾置，且升级后自检的
      # 期望同源于刚钉的值、必然通过。现点名不钉，留待人工钉
      # 值或后续实读成功时再钉（live scope 维持静默不写，与迁
      # 移快照的逐字恢复契约一致）。
      echo "⚠️ 未能实读后端 $backend_container 在 /app/data 的挂载卷名（容器不在册或为 bind 挂载），本次不钉 FLMX_SQLITE_VOLUME 键；重建将按模板缺省选卷——如部署使用命名卷，请先在 .env 中显式钉入实际卷名再重跑"
    fi
  fi
  local postgres_key postgres_volume=""
  postgres_key=$(get_env_var FLMX_POSTGRES_VOLUME || true)
  postgres_volume=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}' "$postgres_container" 2>/dev/null || true)
  # R1-01-L5：PG bind 中止（谓词与处置见函数头注）。docker
  # inspect 本体成功即容器在场；挂载模板读空＝该 destination 无
  # 命名卷（bind 或无挂载），与「容器不在场」分开。
  if [[ -n "$postgres_key" && "$pin_scope" != "live" ]] \
    && [[ "$(get_current_db_type)" == "postgres" ]] \
    && docker inspect "$postgres_container" >/dev/null 2>&1 \
    && [[ -z "$postgres_volume" ]]; then
    echo "❌ 资源键钉值中止：部署为 PostgreSQL 模式且 .env 已有 FLMX_POSTGRES_VOLUME=$postgres_key，但 PostgreSQL 容器 $postgres_container 的 /var/lib/postgresql/data 挂载不是命名卷（实读卷名为空，疑似 bind 挂载），键值无从与实际挂载核对；bind 挂载不在随版模板形态内。"
    echo "   请先把 PG 数据改为命名卷挂载，或人工核对 .env 的 FLMX_POSTGRES_VOLUME 确为现行数据卷后，再重跑更新。"
    return 1
  fi
  if [[ -n "$postgres_key" ]]; then
    if [[ -n "$postgres_volume" && "$postgres_volume" != "$postgres_key" ]]; then
      echo "⚠️ 资源键冲突：.env 的 FLMX_POSTGRES_VOLUME=$postgres_key 与在跑 PostgreSQL 容器 $postgres_container 实际挂载的数据卷 $postgres_volume 不一致；以实读为准改写为 $postgres_volume（数据以实读为准，旧值不保留）"
      upsert_env_var .env FLMX_POSTGRES_VOLUME "$postgres_volume" || return 1
    fi
  else
    # R1-01-M1 事故态门 PG 格（谓词见函数头注）：与 SQLite 格同
    # 形——无键＋实挂新缺省卷＋旧名卷含真实 PG 数据 → 中止。
    if [[ "$postgres_volume" == "flmx-postgres-data$(panel_instance_suffix)" ]] \
      && docker volume inspect postgres_data >/dev/null 2>&1; then
      # A2-01 残面：与 SQLite 格同律走三态探测——挂载点不可读＝
      # 未知，按未知中止，不按无数据放行。
      local postgres_old_state=0
      panel_volume_file_state postgres_data PG_VERSION || postgres_old_state=$?
      if [[ "$postgres_old_state" == "0" ]]; then
        echo "❌ 资源键钉值中止：在跑 PostgreSQL 容器实际挂载的数据卷是新模板缺省名 $postgres_volume，但部署 .env 没有 FLMX_POSTGRES_VOLUME 键、且旧名卷 postgres_data 存在并含有 PG_VERSION（真实 PostgreSQL 数据目录）——该部署可能正处在「挂新空卷、真库在旧卷」的事故形态，此时钉键会把事故态正式化。"
        echo "   请人工确认数据所在卷后，在 .env 中显式钉入正确卷名再重跑：数据在旧卷 → FLMX_POSTGRES_VOLUME=postgres_data；数据确在现行卷 → FLMX_POSTGRES_VOLUME=$postgres_volume。"
        return 1
      fi
      if [[ "$postgres_old_state" == "2" ]]; then
        echo "❌ 资源键钉值中止：在跑 PostgreSQL 容器实际挂载的数据卷是新模板缺省名 $postgres_volume、部署 .env 又没有 FLMX_POSTGRES_VOLUME 键，而旧名卷 postgres_data 虽存在、其挂载点不可读，无法确认其中是否存有 PG_VERSION（真实 PostgreSQL 数据目录）——按未知处理，不冒「无数据」之名钉键正式化可能的事故态。"
        echo "   请先恢复 Docker 卷读面（docker volume inspect postgres_data 应能给出可读的挂载点），或人工确认数据所在卷后，在 .env 中显式钉入正确卷名再重跑：数据在旧卷 → FLMX_POSTGRES_VOLUME=postgres_data；数据确在现行卷 → FLMX_POSTGRES_VOLUME=$postgres_volume。"
        return 1
      fi
    fi
    # A2-03：缺省 scope 删除卷存在性回退，与 live scope 同口
    # 径——postgres_data 存在只证明该卷在，不证明它就是本部署
    # PG 的卷；旧实现凭存在性认领，会把无关卷（共存旧面板/
    # 废弃 PG）写进本部署 .env，后续迁移还会把 pgloader 目标
    # 指向异属卷。实读为空一律不写；仅在 PG 模式、或该卷存在
    # （即旧实现会认领的形态）时点名不钉，纯 SQLite 部署无此
    # 形态时保持静默。迁移的 PG 卷候选由 F7-03/F7-04 的登记口
    # 径处理，与此处无关。
    if [[ -n "$postgres_volume" ]]; then
      upsert_env_var .env FLMX_POSTGRES_VOLUME "$postgres_volume" || return 1
    elif [[ "$pin_scope" != "live" ]] && { [[ "$(get_current_db_type)" == "postgres" ]] || docker volume inspect postgres_data >/dev/null 2>&1; }; then
      echo "⚠️ 未能实读 PostgreSQL 容器 $postgres_container 在 /var/lib/postgresql/data 的挂载卷名，本次不钉 FLMX_POSTGRES_VOLUME 键（不凭 postgres_data 卷的存在性认领——卷在不等于卷属于本部署）；如本部署确为 PostgreSQL，请在 .env 中显式钉入实际 PG 卷名后再重跑"
    fi
  fi
  if [[ "$pin_scope" != "live" ]]; then
    if [[ -z "$(get_env_var FLMX_NETWORK || true)" ]]; then
      local network_name="" network_count=0
      network_name=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{"\n"}}{{end}}' "$backend_container" 2>/dev/null || true)
      # R1-01-L1：逐行实读后恰一个网络才钉实读值。旧模板无分隔
      # 拼接——后端挂 ≥2 个网络时钉入的是全部网络名的拼接废串，
      # 重建后部署落到废名新网络、旧网络上的外挂容器与面板断
      # 联。Go 面 inspectLegacyResourceNames 已按「恰一个才钉」
      # 规避并点名此形态。
      # A2-09 残面：多个网络时与 Go 面同口径——选不出唯一部署网
      # 络就不钉键，并点名告警；旧行为静默钉旧缺省 gost-network，
      # 真实网络为自定义名且另挂第二网络的部署会被改钉失联且无
      # 回执。零个（实读为空）仍按本函数「读不到按旧缺省」的既
      # 有契约回退，但同样点名、不静默。
      network_count=$(printf '%s' "$network_name" | grep -c . || true)
      if [[ "$network_count" == "1" ]]; then
        upsert_env_var .env FLMX_NETWORK "$network_name" || return 1
      elif [[ "$network_count" -gt 1 ]]; then
        echo "⚠️ 后端 $backend_container 同时挂在多个网络（$(printf '%s' "$network_name" | tr '\n' ' '))，无法唯一判定部署网络，本次不钉 FLMX_NETWORK 键；重建将按 Compose 文件的缺省网络处理——如部署使用自定义网络名，请在 .env 中显式钉入 FLMX_NETWORK 后再重跑"
      else
        echo "⚠️ 未能实读后端 $backend_container 的网络，本次按旧缺省钉 FLMX_NETWORK=gost-network；如部署使用自定义网络名，请改 .env 后再重跑"
        upsert_env_var .env FLMX_NETWORK "gost-network" || return 1
      fi
    fi
    if [[ -z "$(get_env_var COMPOSE_PROJECT_NAME || true)" ]]; then
      local project_name="${PANEL_COMPOSE_PROJECT:-}"
      if [[ -z "$project_name" ]]; then
        project_name="flmx-panel$(panel_instance_suffix)"
      fi
      upsert_env_var .env COMPOSE_PROJECT_NAME "$project_name" || return 1
    fi
  fi
}

# 改名开关读取（RA-1 L-3 双面口径对齐）：与 Go 面
# identityRenameEnabled 逐字同序——部署 .env 出现
# PANEL_IDENTITY_RENAME 键即以其值为准（键在即赢，含空值），
# 缺键才回退进程环境；仅值 "0" 关闭，其余（含缺失与空值）开
# 启。旧实现只认进程环境：.env 里写的 =0 对脚本面无效、同一部
# 署在两个升级入口的改名决定可相反（数据安全两侧均由钉键兜
# 底，分家的是改名与否本身）。进程环境单独设 0（.env 无此键）
# 仍照旧关闭。
panel_identity_rename_enabled() {
  local flag=""
  if [[ -f .env ]] && grep -q '^PANEL_IDENTITY_RENAME=' .env; then
    flag=$(get_env_var PANEL_IDENTITY_RENAME || true)
  else
    flag="${PANEL_IDENTITY_RENAME:-}"
  fi
  [[ "$flag" != "0" ]]
}

apply_panel_identity_rename() {
  if ! panel_identity_rename_enabled; then
    return 0
  fi
  [[ -f .env ]] || return 0
  local suffix
  suffix=$(panel_instance_suffix)
  local new_backend="${NEW_PANEL_BACKEND_CONTAINER}${suffix}"
  local new_frontend="${NEW_PANEL_FRONTEND_CONTAINER}${suffix}"
  local new_postgres="${NEW_PANEL_POSTGRES_CONTAINER}${suffix}"
  local key_backend key_frontend key_postgres
  key_backend=$(get_env_var PANEL_BACKEND_CONTAINER || true)
  key_frontend=$(get_env_var PANEL_FRONTEND_CONTAINER || true)
  key_postgres=$(get_env_var PANEL_POSTGRES_CONTAINER || true)
  local rename_backend="" rename_frontend="" rename_postgres=""
  if [[ -z "$key_backend" || "$key_backend" == "$LEGACY_PANEL_BACKEND_CONTAINER" ]]; then
    rename_backend=1
  fi
  if [[ -z "$key_frontend" || "$key_frontend" == "$LEGACY_PANEL_FRONTEND_CONTAINER" ]]; then
    rename_frontend=1
  fi
  if [[ -z "$key_postgres" || "$key_postgres" == "$LEGACY_PANEL_POSTGRES_CONTAINER" ]]; then
    rename_postgres=1
  fi
  if [[ -z "$rename_backend" && -z "$rename_frontend" && -z "$rename_postgres" ]]; then
    return 0
  fi
  # 旧实际名取 resolve 已定的进程全局（显式/.env/探测/同代推导的
  # 结果），不取 .env 字面值：缺键时字面为空、实际名靠推导。
  PANEL_IDENTITY_OLD_BACKEND="${PANEL_BACKEND_CONTAINER:-$key_backend}"
  PANEL_IDENTITY_OLD_FRONTEND="${PANEL_FRONTEND_CONTAINER:-$key_frontend}"
  PANEL_IDENTITY_OLD_POSTGRES="${PANEL_POSTGRES_CONTAINER:-$key_postgres}"
  PANEL_IDENTITY_NEW_BACKEND="$new_backend"
  PANEL_IDENTITY_NEW_FRONTEND="$new_frontend"
  PANEL_IDENTITY_NEW_POSTGRES="$new_postgres"
  echo "🏷️ 存量部署身份改名：容器名由旧缺省（$LEGACY_PANEL_BACKEND_CONTAINER / $LEGACY_PANEL_POSTGRES_CONTAINER / $LEGACY_PANEL_FRONTEND_CONTAINER）切换为现行名（$new_backend / $new_postgres / $new_frontend）"
  echo "   只改容器名：数据卷、网络与数据库一律不动；重建后健康检查不过将自动回滚为旧名。"
  # 1) 卷/网络/项目缺失键先钉现行有效值——有效值优先从真实旧容器
  #    实读，必须赶在停删之前：旧实现先删容器后钉键，inspect 对已
  #    删容器恒失败、实读分支在改名主路径上成死代码，钉值恒落回
  #    退名，自定义卷/网络名的无键存量升级后挂空卷却报成功
  #    （RR4-F7-01）。钉键失败也须在删容器之前中止，不留半改名态。
  panel_pin_missing_resource_keys "$PANEL_IDENTITY_OLD_BACKEND" "$PANEL_IDENTITY_OLD_POSTGRES" || return 1
  # 2) 停并删将被改名的旧容器（先于重建腾空端口与卷挂载）。
  if [[ -n "$rename_backend" ]]; then
    panel_stop_remove_container "$PANEL_IDENTITY_OLD_BACKEND" "$new_backend"
  fi
  if [[ -n "$rename_frontend" ]]; then
    panel_stop_remove_container "$PANEL_IDENTITY_OLD_FRONTEND" "$new_frontend"
  fi
  if [[ -n "$rename_postgres" ]]; then
    panel_stop_remove_container "$PANEL_IDENTITY_OLD_POSTGRES" "$new_postgres"
  fi
  # 3) 身份键写入现行名。
  if [[ -n "$rename_backend" ]]; then
    upsert_env_var .env PANEL_BACKEND_CONTAINER "$new_backend" || return 1
  fi
  if [[ -n "$rename_frontend" ]]; then
    upsert_env_var .env PANEL_FRONTEND_CONTAINER "$new_frontend" || return 1
  fi
  if [[ -n "$rename_postgres" ]]; then
    upsert_env_var .env PANEL_POSTGRES_CONTAINER "$new_postgres" || return 1
  fi
  # 4) 进程全局同步新名，后续重建与健康等待按新名取容器。
  if [[ -n "$rename_backend" ]]; then
    PANEL_BACKEND_CONTAINER="$new_backend"
  fi
  if [[ -n "$rename_frontend" ]]; then
    PANEL_FRONTEND_CONTAINER="$new_frontend"
  fi
  if [[ -n "$rename_postgres" ]]; then
    PANEL_POSTGRES_CONTAINER="$new_postgres"
  fi
  export PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER
  PANEL_IDENTITY_RENAMED=1
  echo "✅ 身份改名已写入 .env（回滚点为更新前备份，健康检查不过自动恢复旧名）"
}

resolve_panel_deployment() {
  local backend_explicit=""
  if [[ -n "${PANEL_BACKEND_CONTAINER:-}" ]]; then
    backend_explicit=1
  fi
  local backend_container
  backend_container=$(detect_panel_backend_container) || return 1
  local requested_dir="${PANEL_DEPLOY_DIR:-}"
  local label_dir label_project deploy_dir project_name

  label_dir=$(get_container_compose_label "$backend_container" "com.docker.compose.project.working_dir")
  label_project=$(get_container_compose_label "$backend_container" "com.docker.compose.project")

  # Older in-panel upgrades ran Compose inside a helper container, which left
  # the label pointing at the container path (/opt/flmx-panel) instead of the
  # real deployment. Prefer the current directory when only it has the files.
  if [[ -n "$label_dir" && ! -f "$label_dir/docker-compose.yml" && -f ".env" && -f "docker-compose.yml" ]]; then
    echo "⚠️ 容器标签记录的部署目录 $label_dir 中没有 docker-compose.yml，改用当前目录 $(pwd -P)"
    label_dir=""
  fi

  if [[ -n "$requested_dir" ]]; then
    deploy_dir="$requested_dir"
  elif [[ -n "$label_dir" ]]; then
    deploy_dir="$label_dir"
  elif [[ -f ".env" && -f "docker-compose.yml" ]]; then
    deploy_dir=$(pwd -P)
  else
    echo "❌ 无法识别面板部署目录：未找到容器 Compose 标签，当前目录也没有完整部署配置"
    return 1
  fi

  if [[ ! -d "$deploy_dir" ]]; then
    echo "❌ 面板部署目录不存在：$deploy_dir"
    return 1
  fi
  deploy_dir=$(cd "$deploy_dir" && pwd -P)
  if [[ -n "$label_dir" && -d "$label_dir" ]]; then
    label_dir=$(cd "$label_dir" && pwd -P)
  fi
  if [[ -n "$requested_dir" && -n "$label_dir" && "$deploy_dir" != "$label_dir" ]]; then
    echo "❌ 指定部署目录与运行中容器标签不一致：$deploy_dir != $label_dir"
    return 1
  fi

  project_name="${COMPOSE_PROJECT_NAME:-}"
  if [[ -z "$project_name" && -n "$label_project" ]]; then
    project_name="$label_project"
  fi
  if [[ -n "$project_name" && ! "$project_name" =~ ^[a-z0-9][a-z0-9_-]*$ ]]; then
    echo "❌ Compose 项目名不合法：$project_name"
    return 1
  fi

  PANEL_DEPLOY_DIR="$deploy_dir"
  PANEL_BACKEND_CONTAINER="$backend_container"
  PANEL_POSTGRES_CONTAINER="${PANEL_POSTGRES_CONTAINER:-$(get_env_var PANEL_POSTGRES_CONTAINER "$deploy_dir/.env" || true)}"
  PANEL_POSTGRES_CONTAINER="${PANEL_POSTGRES_CONTAINER:-$(panel_sibling_default "$LEGACY_PANEL_POSTGRES_CONTAINER" "$DEFAULT_PANEL_POSTGRES_CONTAINER")}"
  PANEL_FRONTEND_CONTAINER="${PANEL_FRONTEND_CONTAINER:-$(get_env_var PANEL_FRONTEND_CONTAINER "$deploy_dir/.env" || true)}"
  PANEL_FRONTEND_CONTAINER="${PANEL_FRONTEND_CONTAINER:-$(panel_sibling_default "$LEGACY_PANEL_FRONTEND_CONTAINER" "$NEW_PANEL_FRONTEND_CONTAINER")}"
  PANEL_COMPOSE_PROJECT="$project_name"
  export PANEL_DEPLOY_DIR PANEL_BACKEND_CONTAINER PANEL_POSTGRES_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_COMPOSE_PROJECT
  if [[ -n "$project_name" ]]; then
    COMPOSE_PROJECT_NAME="$project_name"
    export COMPOSE_PROJECT_NAME
  fi

  cd "$PANEL_DEPLOY_DIR"
  echo "📁 部署目录：$PANEL_DEPLOY_DIR"
  if [[ -n "$PANEL_COMPOSE_PROJECT" ]]; then
    echo "📦 Compose 项目：$PANEL_COMPOSE_PROJECT"
  fi

  # 共存守卫（C2-01）：未显式指定后端容器名时，定位到的目录必须确
  # 属本面板。目录定位只靠标签/当前目录，同机另一套面板的部署目录
  # 同样能被定位到——更新/迁移/卸载若在此动手就是误伤他人部署。
  # 归属不成立一律终止、未做任何修改；确为自家部署但用了非标准
  # 镜像来源时，用 PANEL_BACKEND_CONTAINER 显式指定即放行。
  if [[ -z "$backend_explicit" ]] && ! panel_deploy_dir_is_ours "." "$backend_container"; then
    echo "❌ 部署目录 $PANEL_DEPLOY_DIR 不属于本面板：其 Compose 的 image 行未引用本面板镜像、.env 未声明本面板专属后端名，后端容器 $backend_container 也未运行本面板镜像"
    echo "   这可能是同机上另一套面板的部署目录，本脚本不会对其执行更新、迁移或卸载，未做任何修改。"
    echo "   如确为本面板部署（例如改用了自建镜像），请用 PANEL_BACKEND_CONTAINER=<后端容器名> 显式指定后重试。"
    return 1
  fi
}

run_panel_compose() {
  $DOCKER_CMD "$@"
}

validate_panel_update_environment() {
  local key value backend_port frontend_port configured_db_type normalized_db_type db_type database_url postgres_password

  if [[ ! -f ".env" ]]; then
    echo "❌ 部署目录缺少 .env，更新终止"
    return 1
  fi
  if [[ ! -f "docker-compose.yml" ]]; then
    echo "❌ 部署目录缺少 docker-compose.yml，更新终止"
    return 1
  fi

  for key in JWT_SECRET BACKEND_PORT FRONTEND_PORT; do
    value=$(get_env_var "$key" || true)
    if [[ -z "${value//[[:space:]]/}" ]]; then
      echo "❌ .env 中 $key 缺失或为空，更新终止"
      return 1
    fi
  done

  # 端口判读走 get_env_var_raw：I-F1-05 裁定引号包裹同为无效，
  # 与菜单面 resolve_caddy_frontend_port 同口径（见该处注）。
  backend_port=$(get_env_var_raw "BACKEND_PORT" || true)
  frontend_port=$(get_env_var_raw "FRONTEND_PORT" || true)
  if [[ ! "$backend_port" =~ ^[0-9]+$ ]] || (( 10#$backend_port < 1 || 10#$backend_port > 65535 )); then
    echo "❌ BACKEND_PORT 不是有效端口：$backend_port"
    return 1
  fi
  if [[ ! "$frontend_port" =~ ^[0-9]+$ ]] || (( 10#$frontend_port < 1 || 10#$frontend_port > 65535 )); then
    echo "❌ FRONTEND_PORT 不是有效端口：$frontend_port"
    return 1
  fi

  configured_db_type=$(get_env_var "DB_TYPE" || true)
  normalized_db_type=$(normalize_db_type "$configured_db_type")
  if [[ -n "$normalized_db_type" && "$normalized_db_type" != "sqlite" && "$normalized_db_type" != "postgres" ]]; then
    echo "❌ DB_TYPE 仅支持 sqlite 或 postgres：$configured_db_type"
    return 1
  fi
  db_type=$(get_current_db_type)
  if [[ "$db_type" == "postgres" ]]; then
    database_url=$(get_env_var "DATABASE_URL" || true)
    postgres_password=$(get_env_var "POSTGRES_PASSWORD" || true)
    if [[ -z "${database_url//[[:space:]]/}" || -z "${postgres_password//[[:space:]]/}" ]]; then
      echo "❌ PostgreSQL 模式要求 DATABASE_URL 和 POSTGRES_PASSWORD 均非空"
      return 1
    fi
  fi

  if ! run_panel_compose -f docker-compose.yml config -q; then
    echo "❌ 当前 Compose 配置校验失败，更新终止"
    return 1
  fi
}

# A1-01：单腿时间帽——回显「单次 --max-time 总重试 --retry-max-time」
# 两个值。无总预算截止（deadline=0）时沿用单腿口径（600s、重试总
# 时长不限＝--retry-max-time 0）；有截止时按剩余量收紧（单次帽不
# 超过 600s、重试总时长不超过剩余量——--max-time 只管单次尝试，
# 重试会逐次重新计时，不加总帽则一腿仍可数倍于剩余预算）。预
# 算耗尽返回 1。
panel_download_leg_cap() {
  local deadline="${1:-0}"
  if (( deadline <= 0 )); then
    printf '600 0'
    return 0
  fi
  local remain=$(( deadline - $(date +%s) ))
  if (( remain <= 0 )); then
    return 1
  fi
  local cap=600
  if (( remain < cap )); then
    cap=$remain
  fi
  printf '%s %s' "$cap" "$remain"
}

# --retry-max-time 的参数组（一行一个参数，无则无输出）：
# --retry-max-time 在 curl 7.66（2019）后才有，老 curl 上探明不
# 支持就返回空组（单次 --max-time 与地板看门狗仍在），不让一
# 个约束参数反噬全部下载。探测走 type -P 找到的真实 curl 二
# 进制加一次带该选项的 --version 调用（未知选项退出码非 0），
# 不用 --help 文本匹配，也避免被调用方打桩的 curl 函数污染。
panel_retry_max_args() {
  local leg_retry_max="$1"
  if (( leg_retry_max > 0 )); then
    local real_curl
    real_curl=$(type -P curl || true)
    if [[ -n "$real_curl" ]] && "$real_curl" --retry-max-time 0 --version >/dev/null 2>&1; then
      printf '%s\n%s\n' "--retry-max-time" "$leg_retry_max"
    fi
  fi
}

# R5-B5B D19: 安全下载（带 .sha256 校验，缺失即拒）。
# FLMX_VERIFY_DOWNLOADS=0 可跳过，默认开启；旧名 FLVX_VERIFY_DOWNLOADS 的双读已于 1.9.10 退场。
# F-138: 边角组合收紧为 fail-closed——仅显式 "0" 可关闭校验（误填
# true/yes 等不再静默跳过）；缺少 sha256sum 时拒绝（否则空 .sha256 与
# 空 actual 会 ""=="" 误判通过）；.sha256 内容必须是 64 位十六进制。
# A1-01: 第三参 deadline 为整轮下载的绝对截止（epoch 秒，0/缺省
# ＝无总预算）；由 download_verified_routed 按
# FLMX_DOWNLOAD_BUDGET_SECONDS 定格后逐路传入，两腿各按剩余量
# 收紧，耗尽即整腿失败、不再换路重试。
download_verified() {
  local url="$1"
  local dest="$2"
  local deadline="${3:-0}"
  local verify="${FLMX_VERIFY_DOWNLOADS:-1}"

  # R8-J-A: 本体与下方 .sha256 均加有界重试——旧实现单次尝试，
  # 镜像瞬时故障即整次操作终止（校验腿失败还会删掉已下好的本体）。
  # L2-11（R5-FD）：两腿补超时看门狗——旧实现只有 --retry（只对已
  # 发生的错误重试），TCP 已连通但零字节/涓流的假活慢死路线永不让
  # curl 返回，install/update/migrate/uninstall 的 compose 下载与
  # 脚本自更新（同一函数）全部无限挂死。与 install.sh 本体腿同口
  # 径：连接 10s、1KB/s 地板 30s 无进展判死，另加单次 600s 总界
  # （A1-01 后：整轮另有总预算截止时以剩余量为准，见上）。
  local caps leg_cap leg_retry_max
  if ! caps=$(panel_download_leg_cap "$deadline"); then
    echo "❌ 下载总预算（${FLMX_DOWNLOAD_BUDGET_SECONDS:-240}s）已耗尽，整轮下载失败" >&2
    return 1
  fi
  leg_cap="${caps%% *}"
  leg_retry_max="${caps##* }"
  local -a retry_max_args=()
  mapfile -t retry_max_args < <(panel_retry_max_args "$leg_retry_max")
  if ! curl -fL --retry 3 --retry-delay 2 "${retry_max_args[@]}" --connect-timeout 10 --speed-limit 1024 --speed-time 30 --max-time "$leg_cap" -o "$dest" "$url"; then
    echo "❌ 下载失败: $url" >&2
    return 1
  fi

  if [ "$verify" = "0" ]; then
    echo "⚠️  跳过校验 (FLMX_VERIFY_DOWNLOADS=0): $dest" >&2
    return 0
  fi
  if [ "$verify" != "1" ]; then
    echo "⚠️  FLMX_VERIFY_DOWNLOADS 取值无法识别（\"$verify\"），按开启校验处理（仅 0 可关闭）" >&2
  fi
  if ! command -v sha256sum >/dev/null 2>&1; then
    rm -f "$dest"
    echo "❌ 缺少 sha256sum，无法校验下载完整性，拒绝安装（fail-closed）" >&2
    return 1
  fi

  # 下载 .sha256 并校验。A1-01：校验腿按本体腿之后的剩余预算
  # 重新收紧（本体腿可能已吃掉大半预算），耗尽同样整腿失败。
  local sha_url="${url}.sha256"
  local sha_file="${dest}.sha256"
  if ! caps=$(panel_download_leg_cap "$deadline"); then
    rm -f "$dest"
    echo "❌ 下载总预算（${FLMX_DOWNLOAD_BUDGET_SECONDS:-240}s）已耗尽，整轮下载失败" >&2
    return 1
  fi
  leg_cap="${caps%% *}"
  leg_retry_max="${caps##* }"
  retry_max_args=()
  mapfile -t retry_max_args < <(panel_retry_max_args "$leg_retry_max")
  if ! curl -fL --retry 3 --retry-delay 2 "${retry_max_args[@]}" --connect-timeout 10 --speed-limit 1024 --speed-time 30 --max-time "$leg_cap" -o "$sha_file" "$sha_url"; then
    rm -f "$dest" "$sha_file"
    echo "❌ 校验文件缺失，拒绝安装: $sha_url" >&2
    echo "   （设置 FLMX_VERIFY_DOWNLOADS=0 可跳过，仅用于过渡期）" >&2
    return 1
  fi

  local expected=$(awk '{print $1}' "$sha_file" | tr -d ' \n' | tr 'A-F' 'a-f')
  if ! [[ "$expected" =~ ^[0-9a-f]{64}$ ]]; then
    rm -f "$dest" "$sha_file"
    echo "❌ 校验文件内容无效（不是 64 位 SHA256），拒绝安装: $sha_url" >&2
    return 1
  fi
  local actual=$(sha256sum "$dest" | awk '{print $1}')
  rm -f "$sha_file"

  if [ "$expected" != "$actual" ]; then
    rm -f "$dest"
    echo "❌ 校验失败: $dest" >&2
    echo "   期望: $expected" >&2
    echo "   实际: $actual" >&2
    return 1
  fi

  echo "✅ 校验通过: $dest" >&2
  return 0
}

# corroborate_download_anchor <canonical URL> <dest> <获胜前缀>：
# L8-F-02 第二锚（脚本面，与后端 system_upgrade 的
# corroborateReleaseAssetChecksum、agent 侧 corroborateUpgrade
# Checksum 同口径）：download_verified 的校验值与本体同源同次
# 取得，单个镜像失陷即可自洽篡改（本体与 .sha256 一起换）。这
# 里从路线表内与获胜线路不同的一条线路重取同一资产的 .sha256，
# 与落盘本体的实际哈希（已由第一锚证明与获胜线路校验值一致）
# 比对：
#   - 异路给出不同的合法哈希 → 硬失败（返回 1），调用方删档整
#     单中止，不换路重试；
#   - 异路内容不是合法 64 位 SHA256 → 只废该路，继续问下一路；
#   - 全部异路都给不出合法校验值 → 降级单锚继续（返回 0），并
#     在 stderr 点名「跨线路锚复核已降级」。
# 脚本面的边界（如实写明）：① 路线表只有一条时先按下载主流程
# 同款机制 extend 补测一次（仅 stage 1 直连短路形态扩得动；
# PROXY_ENABLED=false / PROXY_URL 覆盖是运维显式单路，无候选
# 可扩，直接降级点名）；② 整轮复核共用 20s 总界（与 Go 两侧同
# 值），逐路 --max-time 取剩余量与 10s 的较小者；③
# FLMX_VERIFY_DOWNLOADS=0 时运维已整体关闭校验，第二锚随第一
# 锚一并跳过（显式逃生口，不是静默降级）。
corroborate_download_anchor() {
  local url="$1" dest="$2" winner="$3"
  local verify="${FLMX_VERIFY_DOWNLOADS:-1}"
  if [ "$verify" = "0" ]; then
    return 0
  fi
  if ! command -v sha256sum >/dev/null 2>&1; then
    # download_verified 在缺 sha256sum 时已 fail-closed 拒绝，能
    # 走到这里只剩校验被改道等异常组合：无从复核，点名降级。
    echo "⚠️ 跨线路锚复核已降级：缺少 sha256sum 无法复核校验值，按单锚继续（L8-F-02）" >&2
    return 0
  fi
  local actual
  actual=$(sha256sum "$dest" | awk '{print $1}')
  local -a candidates=()
  local prefix
  for prefix in "${DOWNLOAD_ROUTES[@]}"; do
    [[ "$prefix" == "$winner" ]] && continue
    candidates+=("$prefix")
  done
  if [[ ${#candidates[@]} -eq 0 ]] && extend_download_routes; then
    for prefix in "${DOWNLOAD_ROUTES[@]}"; do
      [[ "$prefix" == "$winner" ]] && continue
      candidates+=("$prefix")
    done
  fi
  local deadline=$(( $(date +%s) + 20 ))
  local got tmp remain max_time
  for prefix in "${candidates[@]}"; do
    remain=$(( deadline - $(date +%s) ))
    if (( remain <= 0 )); then
      break
    fi
    max_time=10
    if (( remain < max_time )); then max_time=$remain; fi
    tmp=$(mktemp) || continue
    if curl -fsSL --connect-timeout 5 --max-time "$max_time" -o "$tmp" "$(route_url "$prefix" "$url").sha256" 2>/dev/null; then
      got=$(awk '{print $1}' "$tmp" | tr -d ' \n' | tr 'A-F' 'a-f')
      rm -f "$tmp"
      if [[ "$got" =~ ^[0-9a-f]{64}$ ]]; then
        if [[ "$got" != "$actual" ]]; then
          echo "❌ 跨线路锚复核失败：校验文件经两条不同线路获取的结果不一致，已中止（L8-F-02 第二锚）" >&2
          echo "   本体实际哈希（第一锚已验）: $actual" >&2
          echo "   异路（${prefix:-直连}）校验值: $got" >&2
          return 1
        fi
        echo "✅ 跨线路锚复核通过（第二线路: ${prefix:-直连}）" >&2
        return 0
      fi
    else
      rm -f "$tmp"
    fi
  done
  echo "⚠️ 跨线路锚复核已降级：其他线路均未给出校验值，按单锚继续（L8-F-02）" >&2
  return 0
}

# download_verified_routed <canonical URL> <dest>：按内置路线表逐个
# 以 download_verified 下载（资产 + .sha256 同源同腿、fail-closed
# 口径不变），首个成功即用；阶段 1 路线全败时补测代理排名续切。
# 获胜后另经 corroborate_download_anchor 异路复核第二锚
# （L8-F-02）：不一致删档整单中止，降级仅点名不拦截。
# A1-01：整轮总预算（缺省 240s，FLMX_DOWNLOAD_BUDGET_SECONDS 可
# 调，与 install.sh 的 download_agent_binary 同名同值同口径）——
# 旧实现只有单腿界（600s×重试×本体/校验双腿×约 10 路线），高于
# 1KB/s 地板的涓流路线能把一次下载拖到小时级且永不中止。现入
# 口定格截止、逐路逐腿按剩余量收紧，耗尽即整轮点名失败。第二
# 锚复核自有 20s 总界（见 corroborate_download_anchor），不在
# 本预算内重复计扣。
download_verified_routed() {
  local url="$1"
  local dest="$2"
  ensure_download_routes
  local deadline=$(( $(date +%s) + ${FLMX_DOWNLOAD_BUDGET_SECONDS:-240} ))
  local prefix
  while :; do
    for prefix in "${DOWNLOAD_ROUTES[@]}"; do
      if (( $(date +%s) >= deadline )); then
        echo "❌ 下载总预算（${FLMX_DOWNLOAD_BUDGET_SECONDS:-240}s）已耗尽，整轮下载失败" >&2
        return 1
      fi
      if download_verified "$(route_url "$prefix" "$url")" "$dest" "$deadline"; then
        if ! corroborate_download_anchor "$url" "$dest" "$prefix"; then
          rm -f "$dest"
          return 1
        fi
        return 0
      fi
    done
    extend_download_routes || break
  done
  return 1
}

download_panel_update_compose() {
  local compose_url="$1"

  UPDATE_COMPOSE_CANDIDATE=$(mktemp "$PANEL_DEPLOY_DIR/.docker-compose.yml.update.XXXXXX")
  if ! download_verified_routed "$compose_url" "$UPDATE_COMPOSE_CANDIDATE"; then
    rm -f "$UPDATE_COMPOSE_CANDIDATE"
    UPDATE_COMPOSE_CANDIDATE=""
    echo "❌ 下载最新 Compose 配置失败，更新终止"
    return 1
  fi
  if [[ ! -s "$UPDATE_COMPOSE_CANDIDATE" ]]; then
    rm -f "$UPDATE_COMPOSE_CANDIDATE"
    UPDATE_COMPOSE_CANDIDATE=""
    echo "❌ 下载的 Compose 配置为空，更新终止"
    return 1
  fi
}

# R8-011：compose 内容校验的唯一实现，与 upgrade.md 面板节「落盘前
# 内容校验」四要件对齐——合法 YAML 且可被 compose 解析（config -q）、
# 含 services、且包含预期的 backend 与 frontend 两个服务
# （config --services 逐行精确匹配）。旧实现更新路径只有 config -q
# （不验两服务存在），首装/迁移路径更是下载直落盘、零内容校验，坏
# compose 只在 up 拉起时才暴露。三条落盘路径（首装/菜单更新/迁移补
# 下载）共用此函数，不许再各写一份弱化版。
validate_panel_compose_content() {
  local file="$1"
  local version="$2"
  local services
  if ! FLMX_VERSION="$version" FLUX_VERSION="$version" run_panel_compose -f "$file" config -q; then
    return 1
  fi
  if ! services=$(FLMX_VERSION="$version" FLUX_VERSION="$version" run_panel_compose -f "$file" config --services 2>/dev/null); then
    return 1
  fi
  # INT-01: 服务名比对走 herestring，不用管道 grep——pipefail 下
  # grep -qx 匹配即退会让 printf 吃 SIGPIPE 把命中翻成未命中（同
  # panel_compose_references_own_image 的纪律，套件 #155 实证）。
  if ! grep -qx "backend" <<< "$services"; then
    return 1
  fi
  if ! grep -qx "frontend" <<< "$services"; then
    return 1
  fi
}

validate_panel_update_compose() {
  if ! validate_panel_compose_content "$UPDATE_COMPOSE_CANDIDATE" "$LATEST_VERSION"; then
    echo "❌ 新 Compose 配置校验失败（须为合法 Compose 文件且包含 backend 与 frontend 服务），更新终止"
    return 1
  fi
}

# R9-11: 更新备份/强制重装备份的保留份数。单份更新备份含 .env
# （JWT_SECRET/数据库口令）与全量 DB，是密钥级副本，份数必须有
# 界；3 份覆盖「当前＋前两次」的回退窗口（连续升级排障的实际需
# 要），同时把磁盘占用与密钥副本留置量一并钉死。
BACKUP_RETENTION_KEEP=3

# R9-11: 时间戳命名备份的保留轮换。只保留最近 keep 份、删更旧
# 的；更新备份目录（backups/panel-update-*）与强制重装的
# .env.bak.<ts> / docker-compose.yml.bak.<ts> 同口径共用。轮换
# 是收尾清理动作：单项失败只告警点名，不阻断主流程。
# I-C4-06: 排序键是 mtime（旧→新）、同秒并列以名序兜底，不再按
# 名字典序——名字携带未来时间戳的备份（墙钟曾被拨快期间生成等
# 形态）若按名排序会永久占住一个保留槽（真实保留份数被静默压
# 到 keep-1），按其落盘 mtime 排序则回到正常年龄序。
prune_timestamped_backups() {
  local keep="$1"
  shift
  local -a items=()
  local item mtime
  for item in "$@"; do
    if [[ -e "$item" ]]; then
      items+=("$item")
    fi
  done
  local total="${#items[@]}"
  if (( total <= keep )); then
    return 0
  fi
  local -a sorted=()
  mapfile -t sorted < <(
    for item in "${items[@]}"; do
      mtime=$(stat -c %Y -- "$item" 2>/dev/null) || mtime=0
      printf '%s\t%s\n' "$mtime" "$item"
    done | LC_ALL=C sort -t $'\t' -k1,1n -k2,2 | cut -f2-
  )
  local i remove_count=$((total - keep))
  for (( i = 0; i < remove_count; i++ )); do
    rm -rf "${sorted[i]}" || echo "⚠️ 清理旧备份失败：${sorted[i]}（可手动删除）"
  done
}

backup_sqlite_for_update() (
  local destination="$1"
  local running paused="false"

  cleanup_paused_backend() {
    if [[ "$paused" == "true" ]]; then
      docker unpause "$PANEL_BACKEND_CONTAINER" >/dev/null 2>&1 || true
    fi
  }
  trap cleanup_paused_backend EXIT

  running=$(docker inspect -f '{{.State.Running}}' "$PANEL_BACKEND_CONTAINER" 2>/dev/null || true)
  if [[ "$running" == "true" ]]; then
    if ! docker pause "$PANEL_BACKEND_CONTAINER" >/dev/null; then
      echo "❌ 暂停后端以创建一致性 SQLite 备份失败"
      return 1
    fi
    paused="true"
  fi

  mkdir -p "$destination"
  if ! docker cp "$PANEL_BACKEND_CONTAINER:/app/data/." "$destination"; then
    echo "❌ SQLite 数据备份失败"
    return 1
  fi

  # R9-11: github-cache 是可弃缓存（本体有独立的 2GB 上限 LRU 淘
  # 汰、缺失自动重建），不进更新备份——整目录复制会让单份备份连
  # 带最高约 2GB 纯缓存，备份体积失控。复制完成后定点剔除，存
  # 档内不含该目录。
  rm -rf "$destination/github-cache"

  if [[ "$paused" == "true" ]] && ! docker unpause "$PANEL_BACKEND_CONTAINER" >/dev/null; then
    echo "❌ SQLite 备份完成，但恢复后端运行失败"
    return 1
  fi
  paused="false"
)

backup_postgres_for_update() {
  local destination="$1"
  local postgres_db postgres_user

  postgres_db=$(get_env_var "POSTGRES_DB" || true)
  postgres_user=$(get_env_var "POSTGRES_USER" || true)
  postgres_db=${postgres_db:-flux_panel}
  postgres_user=${postgres_user:-flux_panel}

  if ! docker exec "$PANEL_POSTGRES_CONTAINER" pg_dump -U "$postgres_user" "$postgres_db" > "$destination"; then
    rm -f "$destination"
    echo "❌ PostgreSQL 数据备份失败"
    return 1
  fi
}

create_panel_update_backup() {
  local db_type="$1"
  local timestamp

  timestamp=$(date '+%Y%m%d-%H%M%S')
  if ! (umask 077 && mkdir -p "$PANEL_DEPLOY_DIR/backups"); then
    echo "❌ 创建更新备份目录失败"
    return 1
  fi
  UPDATE_BACKUP_DIR=$(umask 077 && mktemp -d "$PANEL_DEPLOY_DIR/backups/panel-update-$timestamp.XXXXXX") || {
    echo "❌ 创建更新备份目录失败"
    return 1
  }
  if ! cp -p .env docker-compose.yml "$UPDATE_BACKUP_DIR/"; then
    echo "❌ 备份面板配置失败"
    return 1
  fi

  if [[ "$db_type" == "postgres" ]]; then
    backup_postgres_for_update "$UPDATE_BACKUP_DIR/postgres.sql" || return 1
  else
    backup_sqlite_for_update "$UPDATE_BACKUP_DIR/sqlite" || return 1
  fi
  echo "💾 更新备份：$UPDATE_BACKUP_DIR"
  # R9-11: 备份轮换——此前每次更新留一整份且只在整机卸载时才删，
  # 更新 N 次留 N 份，磁盘与密钥副本双双无界增长。新备份落成后
  # 按 mtime 保留最近 BACKUP_RETENTION_KEEP 份（含本次），更旧的
  # 删除（排序口径见 prune_timestamped_backups 的 I-C4-06 注：
  # mtime 旧→新、同秒以名序兜底，不按名字典序）。
  prune_timestamped_backups "$BACKUP_RETENTION_KEEP" "$PANEL_DEPLOY_DIR/backups"/panel-update-*
}

pull_panel_update_images() {
  local db_type="$1"

  if [[ "$db_type" == "postgres" ]]; then
    FLMX_VERSION="$LATEST_VERSION" FLUX_VERSION="$LATEST_VERSION" run_panel_compose -f "$UPDATE_COMPOSE_CANDIDATE" pull backend frontend postgres
  else
    FLMX_VERSION="$LATEST_VERSION" FLUX_VERSION="$LATEST_VERSION" run_panel_compose -f "$UPDATE_COMPOSE_CANDIDATE" pull backend frontend
  fi
}

# FIX2-C #1（V6-01）：升级替换 compose 前，把存量 compose 的网络名
# 与子网搬进候选文件。FIX-G 已把模板子网改为 172.30.0.0/16，但那
# 只应作用于全新安装；整份覆写会连在用子网一起换掉，等于升级即
# 重建面板网络（容器 IP 全变、可信代理判定随子网是否 RFC1918 静默
# 翻转）。规则与面板内升级（preserveComposeNetworks）一致：存量有
# 子网就整体搬入；存量无显式子网则删掉候选的子网行、不强加模板
# 子网；存量无 networks 段则不动候选。best-effort：任何一步失败都
# 不阻断升级本身（行为不劣于此前的整份覆写）。
# 用法：preserve_compose_networks <candidate> <current>
preserve_compose_networks() {
  local candidate="$1" current="$2"
  [[ -f "$candidate" && -f "$current" ]] || return 0
  grep -q "^networks:" "$current" || return 0

  # networks 是模板的最后一个顶层段；段内抽网络名与全部子网。
  local old_name old_subnets
  old_name=$(awk '
    /^networks:/ { in_net=1; next }
    in_net && /^[^[:space:]#]/ { in_net=0 }
    in_net && /^    name:/ { sub(/^    name:[[:space:]]*/, ""); print; exit }
  ' "$current")
  old_subnets=$(awk '
    /^networks:/ { in_net=1; next }
    in_net && /^[^[:space:]#]/ { in_net=0 }
    in_net && /- subnet:/ { sub(/.*- subnet:[[:space:]]*/, ""); print }
  ' "$current")

  local tmp
  tmp=$(mktemp) || return 0
  if ! OLD_NAME="$old_name" OLD_SUBNETS="$old_subnets" awk '
    BEGIN {
      old_name = ENVIRON["OLD_NAME"]
      n = split(ENVIRON["OLD_SUBNETS"], subs, "\n")
      if (n == 1 && subs[1] == "") n = 0
      emitted = 0
    }
    /^networks:/ { in_net=1; print; next }
    in_net && /^[^[:space:]#]/ { in_net=0 }
    in_net && /^    name:/ && old_name != "" {
      print "    name: " old_name
      next
    }
    in_net && /- subnet:/ {
      if (!emitted) {
        indent = $0
        sub(/- subnet:.*/, "", indent)
        for (i = 1; i <= n; i++) print indent "- subnet: " subs[i]
        emitted = 1
      }
      next
    }
    { print }
  ' "$candidate" > "$tmp"; then
    rm -f "$tmp"
    return 0
  fi
  cat "$tmp" > "$candidate"
  rm -f "$tmp"
  if [[ -n "$old_subnets" ]]; then
    echo "🌐 已保留现有部署的网络子网（${old_subnets//$'\n'/, }），模板子网仅对全新安装生效"
  fi
}

# compose_backend_env_has_hops_line <compose文件>
# backend 服务的 environment 块内（非注释条目）已带
# FLMX_TRUSTED_PROXY_HOPS 键时返回 0，否则返回 1。判定收窄在
# 块内：文件他处的同名字符串（注释、其他服务段）不算数——旧实现
# 是文件全局 grep，他处出现该串即误判已有、静默不注入（RB3-02）。
compose_backend_env_has_hops_line() {
  awk '
    function indent_of(line) { match(line, /^[[:space:]]*/); return RLENGTH }
    BEGIN { in_backend=0; in_env=0; found=0 }
    {
      if (!in_backend) {
        if ($0 ~ /^  backend:([[:space:]]+(#.*)?)?$/) in_backend=1
        next
      }
      if ($0 ~ /^[^[:space:]#]/ || $0 ~ /^  [^[:space:]#]/) { in_backend=0; in_env=0; next }
      if (!in_env) {
        if ($0 ~ /^    environment:([[:space:]]+(#.*)?)?$/) in_env=1
        next
      }
      if ($0 ~ /^[[:space:]]*$/ || $0 ~ /^[[:space:]]*#/) next
      if (indent_of($0) <= 4) { in_env=0; next }
      if ($0 ~ /^[[:space:]]*(-[[:space:]]+)?FLMX_TRUSTED_PROXY_HOPS[[:space:]]*[:=]/) found=1
    }
    END { exit(found ? 0 : 1) }
  ' "$1"
}

# compose_hops_injection_problem <compose文件>
# 校验 backend environment 块内 FLMX_TRUSTED_PROXY_HOPS 注入产物
# 的结构合法性：合法时无输出、返回 0；不合法时把问题描述输出到
# stdout 并返回 1。检查项：段存在、块内恰有一条传递行、其缩进与
# 形态（列表/映射）和段内首条条目一致。
compose_hops_injection_problem() {
  awk '
    function indent_of(line) { match(line, /^[[:space:]]*/); return RLENGTH }
    BEGIN { in_backend=0; in_env=0; seen_env=0; key_count=0; key_form=""; key_indent=-1; entry_form=""; entry_indent=-1 }
    {
      if (!in_backend) {
        if ($0 ~ /^  backend:([[:space:]]+(#.*)?)?$/) in_backend=1
        next
      }
      if ($0 ~ /^[^[:space:]#]/ || $0 ~ /^  [^[:space:]#]/) { in_backend=0; in_env=0; next }
      if (!in_env) {
        if ($0 ~ /^    environment:([[:space:]]+(#.*)?)?$/) { in_env=1; seen_env=1 }
        next
      }
      if ($0 ~ /^[[:space:]]*$/ || $0 ~ /^[[:space:]]*#/) next
      ind = indent_of($0)
      if (ind <= 4) { in_env=0; next }
      form = ($0 ~ /^[[:space:]]*-[[:space:]]/) ? "list" : "map"
      if (entry_indent < 0) { entry_indent=ind; entry_form=form }
      if ($0 ~ /FLMX_TRUSTED_PROXY_HOPS/) { key_count++; key_form=form; key_indent=ind }
    }
    END {
      if (!seen_env) { print "backend 服务缺少可识别的 environment 段"; exit 1 }
      if (key_count == 0) { print "environment 段内没有 FLMX_TRUSTED_PROXY_HOPS 传递行"; exit 1 }
      if (key_count > 1) { print "environment 段内 FLMX_TRUSTED_PROXY_HOPS 传递行重复"; exit 1 }
      if (key_indent != entry_indent) { print "传递行缩进与 environment 段内条目不一致"; exit 1 }
      if (key_form != entry_form) { print "传递行形态（列表/映射）与 environment 段内条目不一致"; exit 1 }
      exit 0
    }
  ' "$1"
}

# R6-L8-F-01：脚本默认拓扑是 Caddy → 前端端口 → 前端 nginx → 后端，
# 链上共两跳，后端的可信代理跳数必须为 2；缺失时登录限流键会收敛
# 到前端网关条目、全站共用一桶（未认证者可持续锁死全站登录）。
# 新版模板已自带 FLMX_TRUSTED_PROXY_HOPS 传递行（默认 :-2），但定版
# 安装/更新下载的可能是旧版资产（无此行），故在 compose 落盘前统一
# 补行：backend 的 environment 段缺该键才注入（映射/列表两种形态
# 各按其形），已有则原样不动。best-effort：失败不阻断安装/更新
# 本身（调用点以 || true 承接返回值），但不许静默——任何降级都
# 向 stderr 点名。判定与校验口径（RB3-01/RB3-02）：
# - 已有判定收窄在 backend 的 environment 块内（见上方助手）；
# - 定形跳过 environment: 后的注释行与空行，以第一条真实条目定
#   列表/映射形；environment: 头行容忍行尾注释；
# - 注入产物先过结构校验（见上方助手）才覆写原文件，校验不过
#   原文件逐字保留、本函数返回非零。
# 用法：ensure_compose_trusted_proxy_hops <compose文件>
ensure_compose_trusted_proxy_hops() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  if compose_backend_env_has_hops_line "$file"; then
    return 0
  fi

  local tmp
  tmp=$(mktemp) || {
    echo "⚠️ 无法创建临时文件，FLMX_TRUSTED_PROXY_HOPS 补行未执行（$file）" >&2
    return 1
  }
  if ! awk '
    function indent_of(line) { match(line, /^[[:space:]]*/); return RLENGTH }
    BEGIN { in_backend=0; pending=0; done=0 }
    {
      if (done) { print; next }
      if (!in_backend) {
        if ($0 ~ /^  backend:([[:space:]]+(#.*)?)?$/) in_backend=1
        print; next
      }
      if ($0 ~ /^[^[:space:]#]/ || $0 ~ /^  [^[:space:]#]/) {
        if (pending) { print "      FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}"; done=1; pending=0 }
        in_backend=0
        print; next
      }
      if (!pending && $0 ~ /^    environment:([[:space:]]+(#.*)?)?$/) { pending=1; print; next }
      if (pending) {
        if ($0 ~ /^[[:space:]]*$/ || $0 ~ /^[[:space:]]*#/) { print; next }
        ind = indent_of($0)
        if (ind > 4) {
          if ($0 ~ /^[[:space:]]*-[[:space:]]/) printf "%s- FLMX_TRUSTED_PROXY_HOPS=${FLMX_TRUSTED_PROXY_HOPS:-2}\n", substr($0, 1, ind)
          else printf "%sFLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}\n", substr($0, 1, ind)
          done=1; pending=0
          print; next
        }
        print "      FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}"
        done=1; pending=0
        print; next
      }
      print
    }
    END { if (pending) print "      FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}" }
  ' "$file" > "$tmp"; then
    rm -f "$tmp"
    echo "⚠️ 解析 compose 失败，FLMX_TRUSTED_PROXY_HOPS 补行未执行（$file）" >&2
    return 1
  fi
  local problem
  if ! problem=$(compose_hops_injection_problem "$tmp"); then
    rm -f "$tmp"
    echo "⚠️ FLMX_TRUSTED_PROXY_HOPS 补行未通过结构校验（${problem}），已保留原 compose 不变（$file）；默认拓扑下后端可信代理跳数可能不正确，请人工检查该文件的 backend environment 段" >&2
    return 1
  fi
  cat "$tmp" > "$file"
  rm -f "$tmp"
  echo "🔐 已为后端补入 FLMX_TRUSTED_PROXY_HOPS 传递行（默认拓扑两跳，可在 .env 覆盖）"
  return 0
}

activate_panel_update_files() {
  preserve_compose_networks "$UPDATE_COMPOSE_CANDIDATE" docker-compose.yml
  ensure_compose_trusted_proxy_hops "$UPDATE_COMPOSE_CANDIDATE" || true
  if ! chmod 0644 "$UPDATE_COMPOSE_CANDIDATE" || ! mv "$UPDATE_COMPOSE_CANDIDATE" docker-compose.yml; then
    echo "❌ 替换 Compose 配置失败"
    return 1
  fi
  UPDATE_COMPOSE_CANDIDATE=""
  if ! upsert_env_var ".env" "FLMX_VERSION" "$LATEST_VERSION"; then
    echo "❌ 更新版本配置失败"
    return 1
  fi
  # 过渡期双写：旧键 FLUX_VERSION 同步同值——尚未切换读面的消费
  # 者（旧版面板的 .env 版本改写、旧脚本）按旧口径读到的与新键同
  # 一版本，不会因只剩新键而读空或读到陈旧值；一切读面以
  # FLMX_VERSION 优先（resolve_version / update_panel 同口径）。
  if ! upsert_env_var ".env" "FLUX_VERSION" "$LATEST_VERSION"; then
    echo "❌ 更新版本配置失败"
    return 1
  fi
}

start_panel_after_update() {
  local db_type="$1"

  if [[ "$db_type" == "postgres" ]]; then
    run_panel_compose up -d postgres || return 1
    wait_for_postgres_healthy || return 1
  fi
  run_panel_compose up -d --force-recreate --remove-orphans backend frontend || return 1
  wait_for_backend_healthy
}

rollback_panel_update() {
  local db_type="$1"

  # 身份改名回滚先行：本次更新已改名时，先删可能已按新名拉起的
  # 容器（占着端口/卷挂载，不删则旧名重建必冲突）、进程全局复位
  # 旧名；下方恢复旧 .env＋旧 compose 后即按旧名拉起并等待健康。
  if [[ -n "${PANEL_IDENTITY_RENAMED:-}" ]]; then
    echo "↩️ 身份改名回滚：删除新名容器、恢复旧容器名..."
    panel_stop_remove_container "${PANEL_IDENTITY_NEW_BACKEND:-}" "${PANEL_IDENTITY_OLD_BACKEND:-}"
    panel_stop_remove_container "${PANEL_IDENTITY_NEW_FRONTEND:-}" "${PANEL_IDENTITY_OLD_FRONTEND:-}"
    panel_stop_remove_container "${PANEL_IDENTITY_NEW_POSTGRES:-}" "${PANEL_IDENTITY_OLD_POSTGRES:-}"
    PANEL_BACKEND_CONTAINER="${PANEL_IDENTITY_OLD_BACKEND:-$PANEL_BACKEND_CONTAINER}"
    PANEL_FRONTEND_CONTAINER="${PANEL_IDENTITY_OLD_FRONTEND:-$PANEL_FRONTEND_CONTAINER}"
    PANEL_POSTGRES_CONTAINER="${PANEL_IDENTITY_OLD_POSTGRES:-$PANEL_POSTGRES_CONTAINER}"
    export PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER
  fi

  echo "↩️ 正在恢复更新前配置..."
  if ! cp -p "$UPDATE_BACKUP_DIR/.env" .env || ! cp -p "$UPDATE_BACKUP_DIR/docker-compose.yml" docker-compose.yml; then
    echo "❌ 配置回滚失败，请从 $UPDATE_BACKUP_DIR 手动恢复"
    return 1
  fi
  if [[ "$db_type" == "postgres" ]]; then
    run_panel_compose up -d postgres || return 1
    wait_for_postgres_healthy || return 1
  fi
  run_panel_compose up -d --force-recreate --remove-orphans backend frontend || return 1
  wait_for_backend_healthy
}

# 升级后卷名自检：重建后的后端容器在 /app/data 实际挂载的卷名必
# 须与重建前定格的有效卷名一致。健康检查只证明新进程活着，不证
# 明挂的是原来的卷——模板回退名已翻新（flmx-sqlite-data），无键
# 存量一旦在某条路径上漏钉，重建即挂空卷、面板如新装而更新仍报
# 成功（1.9.8 生产事故形态；脚本面钉键齐备，此自检为第二道防
# 线）。比对按真实容器挂载实读；实读为空（容器不在册/挂载非命
# 名卷）同样判不一致——自检读不到时不得放行。
verify_panel_update_sqlite_volume() {
  local expected="$1"
  local backend_container="${PANEL_BACKEND_CONTAINER:-$DEFAULT_PANEL_BACKEND_CONTAINER}"
  local actual=""
  actual=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Name}}{{end}}{{end}}' "$backend_container" 2>/dev/null || true)
  if [[ "$actual" != "$expected" ]]; then
    echo "❌ 升级后卷名自检失败：后端容器 $backend_container 在 /app/data 实际挂载卷「${actual:-（未读到）}」，与升级前记录的卷「$expected」不一致——新后端可能挂在空卷上，本次更新判失败并回滚" >&2
    return 1
  fi
  echo "🔎 升级后卷名自检通过：后端 /app/data 挂载卷为 $actual（与升级前记录一致）"
  return 0
}

# A2-02：PG 面的同型自检——此前只有 SQLite 面，PG 部署重建后挂
# 到模板缺省空卷时，健康检查（新进程对空库播种后照样健康）与
# SQLite 自检（比的是无关卷）双过、照报「更新完成」，真库被晾
# 置而无任何回执。现与 SQLite 自检并列：重建后 PG 容器在
# /var/lib/postgresql/data 实际挂载的卷名必须与重建前定格的期
# 望一致，且卷内须见 PG_VERSION（真实 PG 数据目录标记，排除挂
# 到同名非数据卷的形态）；实读为空同样判不一致。
verify_panel_update_postgres_volume() {
  local expected="$1"
  local postgres_container="${PANEL_POSTGRES_CONTAINER:-$DEFAULT_PANEL_POSTGRES_CONTAINER}"
  local actual=""
  actual=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}' "$postgres_container" 2>/dev/null || true)
  if [[ "$actual" != "$expected" ]]; then
    echo "❌ 升级后 PG 卷名自检失败：PostgreSQL 容器 $postgres_container 在 /var/lib/postgresql/data 实际挂载卷「${actual:-（未读到）}」，与升级前记录的卷「$expected」不一致——新 PostgreSQL 可能挂在空卷上，本次更新判失败并回滚" >&2
    return 1
  fi
  if ! panel_volume_file_exists "$actual" PG_VERSION; then
    echo "❌ 升级后 PG 卷名自检失败：卷 $actual 内未见 PG_VERSION（不像真实 PostgreSQL 数据目录，可能挂到了同名的空卷/异属卷），本次更新判失败并回滚" >&2
    return 1
  fi
  echo "🔎 升级后 PG 卷名自检通过：PostgreSQL /var/lib/postgresql/data 挂载卷为 $actual（与升级前记录一致）"
  return 0
}

wait_for_postgres_healthy() {
  local pg_health
  local postgres_container="${PANEL_POSTGRES_CONTAINER:-$DEFAULT_PANEL_POSTGRES_CONTAINER}"

  echo "🔍 检查 PostgreSQL 服务状态..."
  for i in {1..90}; do
    if docker ps --format "{{.Names}}" | grep -Fxq "$postgres_container"; then
      pg_health=$(docker inspect -f '{{.State.Health.Status}}' "$postgres_container" 2>/dev/null || echo "unknown")
      if [[ "$pg_health" == "healthy" ]]; then
        echo "✅ PostgreSQL 服务健康检查通过"
        return 0
      elif [[ "$pg_health" == "unhealthy" ]]; then
        echo "⚠️ PostgreSQL 健康状态：$pg_health"
      fi
    else
      pg_health="not_running"
    fi

    if [ $i -eq 90 ]; then
      echo "❌ PostgreSQL 启动超时（90秒）"
      echo "🔍 当前状态：$(docker inspect -f '{{.State.Health.Status}}' "$postgres_container" 2>/dev/null || echo '容器不存在')"
      return 1
    fi

    if [ $((i % 15)) -eq 1 ]; then
      echo "⏳ 等待 PostgreSQL 启动... ($i/90) 状态：${pg_health:-unknown}"
    fi
    sleep 1
  done
}

wait_for_backend_healthy() {
  local backend_health
  local backend_container="${PANEL_BACKEND_CONTAINER:-$DEFAULT_PANEL_BACKEND_CONTAINER}"

  echo "🔍 检查后端服务状态..."
  for i in {1..90}; do
    if docker ps --format "{{.Names}}" | grep -Fxq "$backend_container"; then
      backend_health=$(docker inspect -f '{{.State.Health.Status}}' "$backend_container" 2>/dev/null || echo "unknown")
      if [[ "$backend_health" == "healthy" ]]; then
        echo "✅ 后端服务健康检查通过"
        return 0
      elif [[ "$backend_health" == "unhealthy" ]]; then
        echo "⚠️ 后端健康状态：$backend_health"
      fi
    else
      backend_health="not_running"
    fi

    if [ $i -eq 90 ]; then
      echo "❌ 后端服务启动超时（90秒）"
      echo "🔍 当前状态：$(docker inspect -f '{{.State.Health.Status}}' "$backend_container" 2>/dev/null || echo '容器不存在')"
      return 1
    fi

    if [ $((i % 15)) -eq 1 ]; then
      echo "⏳ 等待后端服务启动... ($i/90) 状态：${backend_health:-unknown}"
    fi
    sleep 1
  done
}

# 删除脚本自身
# C4-F04: 与 install.sh 的 R4B11-005 口径对齐——按 main 传入的操
# 作返回码分流：失败（非 0）保留脚本并给失败文案、绝不印「操作已
# 完成」；成功（0）才自删。旧实现无条件自删且恒印完成：操作失败
# 时脚本能保留全靠 set -e 在 main 调用点中止的副作用，而用户取
# 消这类 return 0 路径仍走到自删并谎报完成。用户取消单独走
# PANEL_OP_CANCELLED 标志（见 uninstall_panel 两处）：操作函数
# 的返回码口径不变（取消仍是干净的 return 0，AI-09 钉格），main
# 见标志不调用本函数、保留脚本并以 0 退出。
delete_self() {
  local rc="${1:-0}"
  if [[ "$rc" != "0" ]]; then
    echo ""
    echo "⚠️ 操作未成功（退出码 $rc），脚本文件已保留，可直接重新运行重试"
    return 0
  fi
  # 通过 bash <(curl ...) 进程替换执行时，$0 是 /dev/fd/63，无需清理
  if [[ "$PANEL_SCRIPT_PATH" == /dev/fd/* ]]; then
    return 0
  fi
  echo ""
  echo "🗑️ 操作已完成，正在清理脚本文件..."
  sleep 1
  rm -f "$PANEL_SCRIPT_PATH" && echo "✅ 脚本文件已删除" || echo "❌ 删除脚本文件失败"
}

# 获取本机公网 IP（用于显示访问地址）
get_public_ip() {
  local ip=""
  # 依次尝试多个服务，取第一个成功的结果
  ip=$(curl -fsSL --max-time 5 "https://api.ipify.org" 2>/dev/null || true)
  if [ -z "$ip" ]; then
    ip=$(curl -fsSL --max-time 5 "https://ifconfig.me" 2>/dev/null || true)
  fi
  if [ -z "$ip" ]; then
    ip=$(curl -fsSL --max-time 5 "https://icanhazip.com" 2>/dev/null | tr -d ' \n' || true)
  fi
  # 兜底：取本机第一块非回环网卡的 IPv4
  if [ -z "$ip" ]; then
    # AUDIT-R1 (SCR-011): 原 grep -oP 在 busybox/Alpine 下恒失败（无 -P 支持）。
    # 改用 awk 提取，POSIX 兼容。
    ip=$(ip -4 addr show scope global 2>/dev/null | awk '$1=="inet"{split($2,a,"/"); print a[1]; exit}' || true)
  fi
  echo "${ip:-服务器IP}"
}



# 获取用户输入的配置参数
get_config_params() {
  echo "🔧 请输入配置参数："

  local _pname _pval
  read -p "前端端口（默认 6366）: " FRONTEND_PORT
  FRONTEND_PORT=${FRONTEND_PORT:-6366}

  read -p "后端端口（默认 6365）: " BACKEND_PORT
  BACKEND_PORT=${BACKEND_PORT:-6365}

  # AUDIT-R1 (SCR-008): 安装时即校验端口（与更新流程 validate_panel_update_environment
  # 一致），避免非法值写入 .env 后在 docker compose up 时才暴露，报错远离输入点。
  for _pname in FRONTEND_PORT BACKEND_PORT; do
    _pval="${!_pname}"
    # AUDIT-R1 (R5R1-001): 算术比较前加 10# 强制十进制。前导零输入（如 080）
    # 否则被 bash 当八进制解析，stderr 报错但仍被接受，体验差。
    if [[ ! "$_pval" =~ ^[0-9]+$ ]] || (( 10#$_pval < 1 || 10#$_pval > 65535 )); then
      echo "❌ $_pname 不是有效端口（1-65535）：$_pval"
      return 1
    fi
  done

  echo "请选择数据库类型："
  echo "1. SQLite（默认）"
  echo "2. PostgreSQL"
  read -p "数据库类型（1/2，默认 1）: " DB_CHOICE
  case "$DB_CHOICE" in
    2)
      DB_TYPE="postgres"
      ;;
    ""|1)
      DB_TYPE="sqlite"
      ;;
    *)
      echo "⚠️ 输入无效，默认使用 SQLite"
      DB_TYPE="sqlite"
      ;;
  esac

  POSTGRES_DB="flmx_panel"
  POSTGRES_USER="flmx_panel"
  POSTGRES_PASSWORD=$(generate_random)

  if [[ "$DB_TYPE" == "postgres" ]]; then
    DATABASE_URL="postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}?sslmode=disable"
  else
    DATABASE_URL=""
  fi

  # 生成JWT密钥
  JWT_SECRET=$(generate_random)
}

# R9-03: 首装死在启动步（容器从未创建，典型为拉镜像失败）会留下
# 「.env＋compose 在、零容器」的半成品：重跑菜单 1 被 F-049 守卫
# 拒绝、菜单 2 在备份步因容器不存在中止，两条路都收敛不了，唯
# 一出口是强制重装。各失败回执统一经本指引点名出口与用法（字段
# 级：变量名、设置方式、后续动作与文件后果）。
# RA-3 #5 文案漏斗修正：旧指引把「后端容器不存在」一律断言为
# 半成品——但容器不存在也可能是有数据的部署其后端容器被误删，
# 操作员照指引强制重装即命中 E3 全后果（数据孤儿化＋PG 口令
# 脱钩）。现指引先引导查数据卷：有数据先恢复部署文件走菜单 2，
# 确认无任何数据卷（真半成品）才点名强制重装出口。
print_force_reinstall_guidance() {
  echo "   后端容器不存在不等于没有数据（也可能是已有部署的容器被误删），先确认数据再决定，不要直接强制重装："
  echo "     1) 执行 docker volume ls，查看是否存在面板数据卷（sqlite_data / flmx-sqlite-data / postgres_data / flmx-postgres-data，或 .env 中 FLMX_SQLITE_VOLUME / FLMX_POSTGRES_VOLUME 指定的卷名）；"
  echo "     2) 若数据卷存在：数据未丢失——还原 .env 与 docker-compose.yml 的备份（如 .env.bak.<时间戳>）后用菜单「2. 更新面板」按原数据重建即可，不要强制重装（强制重装会按空白新装启动、旧数据卷被晾置，PostgreSQL 口令还会与数据卷脱钩）；"
  echo "     3) 若确认从未安装成功、没有任何数据卷（首次安装未完成的半成品状态），唯一出口才是强制重装：先自行备份当前部署目录，然后在终端执行："
  echo "     PANEL_FORCE_REINSTALL=1 bash panel_install.sh"
  echo "   再选择菜单「1. 安装面板」。脚本会先把现存 .env 与 docker-compose.yml 备份为 .env.bak.<时间戳> 与 docker-compose.yml.bak.<时间戳>，再覆写重装；覆写前若探到数据卷内有数据，还会逐个点名并要求交互确认。"
}

# E3 数据点名＋有数据确认（RA-3 #5 必须修）：强制重装放行后、覆
# 写前，把候选数据卷逐个点名——候选＝.env 解析名（panel_sqlite_
# volume_name / panel_postgres_volume_name）＋两代模板字面名
# （sqlite_data / flmx-sqlite-data / postgres_data /
# flmx-postgres-data，含实例后缀形），去重后只点名真实存在的。
# 每个存在的卷都打印并给出恢复方法；再经挂载点探测卷内数据
# （gost.db 与 PG_VERSION 都探，兼容自定义卷名）：探到任一卷有
# 数据即认定这不是无数据的半成品——交互终端须输入部署目录名
# 确认才继续，非 TTY 直接中止（把有数据的部署当半成品重装正
# 是文案漏斗的事故形态）。无数据的半成品（R9-03 的合法出口）
# 不加任何摩擦、照常直行。本步在守卫内两文件备份之后调用：
# 中止时备份已在、.env/compose 未动，恢复路径现成。
panel_force_reinstall_data_gate() {
  local suffix candidates="" candidate
  suffix=$(panel_instance_suffix)
  candidates=""
  for candidate in "$(panel_sqlite_volume_name)" "$(panel_postgres_volume_name)" \
      sqlite_data "flmx-sqlite-data${suffix}" postgres_data "flmx-postgres-data${suffix}"; do
    [[ -n "$candidate" ]] || continue
    case " $candidates " in
      *" $candidate "*) continue ;;
    esac
    candidates="$candidates $candidate"
  done
  local existing="" with_data=""
  for candidate in $candidates; do
    if docker volume inspect "$candidate" >/dev/null 2>&1; then
      existing="$existing $candidate"
      if panel_volume_file_exists "$candidate" gost.db || panel_volume_file_exists "$candidate" PG_VERSION; then
        with_data="$with_data $candidate"
      fi
    fi
  done
  if [[ -n "$existing" ]]; then
    echo "⚠️  强制重装前数据点名：下列现有数据卷不会被删除，但重装后不再挂载，面板将按空白新装启动："
    for candidate in $existing; do
      echo "     - $candidate"
    done
    echo "   恢复方法：用本次备份的 .env.bak.<时间戳> 还原 .env 后，运行菜单「2. 更新面板」，即可按原数据卷重建。"
  fi
  if [[ -z "$with_data" ]]; then
    return 0
  fi
  echo "❌ 上列数据卷中探到实际数据（SQLite 的 gost.db 或 PostgreSQL 的 PG_VERSION），这不是无数据的半成品状态："
  for candidate in $with_data; do
    echo "     - $candidate（含数据）"
  done
  if [[ ! -t 0 ]]; then
    echo "   当前为非交互终端，强制重装已中止，未覆写任何文件。如确认要丢弃卷内数据重装，请在交互终端中重新运行并按提示确认；如需保住数据，先还原 .env 备份并走菜单「2. 更新面板」。"
    return 1
  fi
  local dir_name confirm=""
  dir_name=$(basename "$(pwd -P)")
  read -p "   如确认丢弃上述数据卷内的数据并继续重装，请输入部署目录名（$dir_name）确认: " confirm || confirm=""
  if [[ "$confirm" != "$dir_name" ]]; then
    echo "   确认输入不符，强制重装已中止，未覆写任何文件。"
    return 1
  fi
  echo "⚠️  已确认：带数据强制重装继续，数据卷将被晾置（不删除），面板按空白新装启动"
  return 0
}

# 安装功能
install_panel() {
  echo "🚀 开始安装面板..."

  # 实例 id 合法性前置校验（AI-03）：id 会进入容器/卷/网络/项目名，
  # 非法字符让 Compose 命名直接失败，先于一切动作拒绝。
  if [[ -n "${FLMX_INSTANCE:-}" ]] && ! panel_instance_id_is_valid "$FLMX_INSTANCE"; then
    echo "❌ FLMX_INSTANCE 不合法：$FLMX_INSTANCE（须以小写字母或数字开头，仅含小写字母、数字、- 与 _，全长不超过 32 个字符）"
    return 1
  fi

  # 共存守卫（C2-01）：当前目录已有部署文件且明确属于他人（Compose
  # 的 image 行无一引用本面板镜像，或 .env 声明的后端名不属于本面
  # 板任何已知命名）时拒绝安装——那是同机另一套面板的部署目录，
  # 在此安装必然覆写他人的 .env 与 Compose，连强制重装也不放行。
  # 归属未证（无镜像引用、如 build: 型 Compose）不硬判，交下方
  # F-049 守卫按「默认拒绝、显式强制先备份」处理。
  if is_foreign_panel_deploy_dir .; then
    echo "❌ 当前目录已有一套不属于本面板的部署（其 Compose 的 image 行未引用本面板镜像，或其 .env 声明的后端名不属于本面板），拒绝在此安装，以免覆盖他人的部署文件"
    echo "   请换一个空目录运行安装；若这是本面板自己的部署目录，请改用菜单「2. 更新面板」。"
    return 1
  fi

  # F-049＋AI-05: 已有部署守卫。安装路径会重生成 POSTGRES_PASSWORD/
  # JWT_SECRET 并无条件覆写 .env 与 docker-compose.yml；而 PG 数据
  # 卷里的口令只在首次初始化时生效——在已部署机器上误走安装，.env
  # 新口令与旧卷脱钩，后端必然以 28P01 连不上库。旧触发条件是
  # 「.env 与 docker-compose.yml 同时存在」的合取：任一单文件残缺
  # 态下两道守卫同时失效、覆写照常发生（AI-05）。现任一部署产物
  # 存在即进入守卫：默认拒绝并引导去更新；确需重装须显式
  # PANEL_FORCE_REINSTALL=1，且先把现存的 .env 与 docker-compose.yml
  # 一并备份（旧实现只备份 .env，compose 的手工定制在覆写时无声丢失）。
  if [[ -f .env || -f docker-compose.yml ]]; then
    if [[ "${PANEL_FORCE_REINSTALL:-}" != "1" ]]; then
      echo "❌ 检测到当前目录已有面板部署文件（.env 与 docker-compose.yml 至少其一存在）"
      echo "   如需升级请使用菜单「2. 更新面板」；继续安装将覆写部署文件并轮换密钥，"
      echo "   PostgreSQL 部署会因口令与数据卷脱钩而无法启动。"
      echo "   确需重装请先自行备份，或设置 PANEL_FORCE_REINSTALL=1 后重试（脚本会先备份现存的 .env 与 docker-compose.yml）。"
      echo "   强制重装用法：在终端执行 PANEL_FORCE_REINSTALL=1 bash panel_install.sh 后选择菜单「1. 安装面板」。若部署的后端容器不存在，菜单「2. 更新面板」会在备份步中止——但容器不存在不等于没有数据（也可能是容器被误删）：先执行 docker volume ls 确认数据卷，无任何数据卷（首次安装未完成的半成品状态）时才走此出口，有数据卷时应先还原部署文件再走菜单「2. 更新面板」。"
      return 1
    fi
    local backup_ts
    backup_ts=$(date +%Y%m%d%H%M%S)
    if [[ -f .env ]]; then
      cp .env ".env.bak.$backup_ts"
      echo "⚠️  PANEL_FORCE_REINSTALL=1：已备份原 .env 到 .env.bak.$backup_ts"
    fi
    if [[ -f docker-compose.yml ]]; then
      cp docker-compose.yml "docker-compose.yml.bak.$backup_ts"
      echo "⚠️  PANEL_FORCE_REINSTALL=1：已备份原 docker-compose.yml 到 docker-compose.yml.bak.$backup_ts，继续重装"
    fi
    # R9-11: 强制重装的带时间戳备份与更新备份同轮换口径（各保留
    # 最近 BACKUP_RETENTION_KEEP 份），避免反复强制重装同样无界
    # 堆积密钥级 .env 副本。
    prune_timestamped_backups "$BACKUP_RETENTION_KEEP" .env.bak.*
    prune_timestamped_backups "$BACKUP_RETENTION_KEEP" docker-compose.yml.bak.*
    # E3（RA-3 #5）：覆写前数据点名＋有数据时交互确认，见
    # panel_force_reinstall_data_gate。排在备份之后：确认不过/
    # 非 TTY 中止时备份已在、部署文件未动，恢复路径现成。
    panel_force_reinstall_data_gate || return 1
  fi

  ensure_compose_urls_initialized || return 1

  check_docker
  # R2-F7-02: get_config_params 失败（非法端口等）必须在写 .env 前
  # 终止。main 以 `install_panel || rc=$?` 调用本函数，该上下文下
  # set -e 在本函数内失效，裸调用会带着非法端口与空密钥继续下载、
  # 写 .env，直到 compose up 才失败并误导向强制重装指引。
  get_config_params || return 1

  echo "🔽 下载必要文件..."
  # F-052: 选版时机后移——主机有 IPv6 地址不等于 Docker 能用 IPv6。
  # 先配置并验证 Docker IPv6（configure_docker_ipv6 写后校验+重启检查、
  # docker_ipv6_usable 查 daemon 与 bridge），确认可用才选 v6 版；
  # 任一步不通过都明确降级 v4 版并告知，不再让安装死在建网一步、
  # 报错还远离根因。
  local use_ipv6=0
  if check_ipv6_support; then
    echo "🚀 系统支持 IPv6，尝试启用并验证 Docker IPv6..."
    if configure_docker_ipv6 && docker_ipv6_usable; then
      use_ipv6=1
      echo "✅ Docker IPv6 验证可用，使用 IPv6 版 Compose"
    else
      echo "⚠️ Docker IPv6 配置/验证未通过，降级使用 IPv4 版 Compose（面板功能不受影响）"
    fi
  fi
  if [[ "$use_ipv6" == "1" ]]; then
    DOCKER_COMPOSE_URL="$DOCKER_COMPOSEV6_URL"
  else
    DOCKER_COMPOSE_URL="$DOCKER_COMPOSEV4_URL"
  fi
  echo "📡 选择配置文件：$(basename "$DOCKER_COMPOSE_URL")"
  # R8-J-A 附带清理：不再靠 set -e 隐式中止，显式判返回值并给出
  # 路径信息，与更新路径（download_panel_update_compose）的口径对齐。
  # R8-011：先落候选文件、过 validate_panel_compose_content 内容校验
  # 才转正落盘（与更新路径同口径），不再下载直落 docker-compose.yml。
  local compose_candidate
  compose_candidate=$(mktemp "./.docker-compose.yml.install.XXXXXX") || return 1
  if ! download_verified_routed "$DOCKER_COMPOSE_URL" "$compose_candidate"; then
    rm -f "$compose_candidate"
    echo "❌ 下载 Compose 配置失败，安装终止: $DOCKER_COMPOSE_URL"
    return 1
  fi
  if ! validate_panel_compose_content "$compose_candidate" "$RESOLVED_VERSION"; then
    rm -f "$compose_candidate"
    echo "❌ 下载的 Compose 配置内容校验失败（须为合法 Compose 文件且包含 backend 与 frontend 服务），安装终止"
    return 1
  fi
  if ! chmod 0644 "$compose_candidate" || ! mv "$compose_candidate" docker-compose.yml; then
    rm -f "$compose_candidate"
    echo "❌ 写入 Compose 配置失败，安装终止"
    return 1
  fi
  # R6-L8-F-01：旧版资产模板可能没有可信代理跳数传递行，落盘前补齐
  # （新版模板自带、此步自动跳过）。
  ensure_compose_trusted_proxy_hops docker-compose.yml || true
  echo "✅ 文件准备完成"

  cat > .env <<EOF
JWT_SECRET=$JWT_SECRET
FRONTEND_PORT=$FRONTEND_PORT
BACKEND_PORT=$BACKEND_PORT
FLMX_VERSION=$RESOLVED_VERSION

DB_TYPE=$DB_TYPE
DATABASE_URL=$DATABASE_URL

POSTGRES_DB=$POSTGRES_DB
POSTGRES_USER=$POSTGRES_USER
POSTGRES_PASSWORD=$POSTGRES_PASSWORD
EOF
  chmod 600 .env # M-01: .env 含 JWT_SECRET/数据库密码，禁止全局可读

  # 共存身份（C2-01/C2-06）：新装把专属容器/卷/网络名写入 .env，
  # Compose 据此命名，与同机其他面板的缺省名不再相撞。
  write_new_panel_identity

  echo "🚀 启动 docker 服务..."
  if [[ "$DB_TYPE" == "postgres" ]]; then
    if ! $DOCKER_CMD up -d postgres; then
      echo "❌ 部署未完成：PostgreSQL 容器启动失败（常见原因是镜像拉取失败），已中止后续启动，未打印成功信息与默认凭据。"
      print_force_reinstall_guidance
      return 1
    fi
    # I-F1-06: PG 健康等待改与同函数后端支同形（守卫＋失败指引
    # 块）——旧实现裸调用，失败时只有等待函数自身的超时两行，无
    # 「部署未完成」宣告、无 logs 指引、无后续路径提示。
    if ! wait_for_postgres_healthy; then
      echo "❌ 部署未完成：PostgreSQL 未通过健康检查，已中止后续启动，未打印成功信息与默认凭据。"
      echo "🔍 排查指引：在部署目录执行 $DOCKER_CMD logs postgres 查看数据库日志；修复后重新运行本脚本、选择「2. 更新面板」即可完成启动。"
      return 1
    fi
  fi
  # R9-03: up 失败旧实现靠 set -e 静默退出、零失败指引，留下的半
  # 成品状态也无回执点名出口。现显式守卫，失败即点名唯一出口。
  if ! $DOCKER_CMD up -d backend frontend; then
    echo "❌ 部署未完成：面板容器启动失败（常见原因是镜像拉取失败），未打印成功信息与默认凭据。"
    print_force_reinstall_guidance
    return 1
  fi

  # R8-J-C: 完成块之前先过既有后端健康等待——更新（start_panel_
  # after_update）与迁移路径均有此把关，安装路径此前独缺，up -d 后
  # 直达成功块：后端 crash-loop 时用户拿到的是打不开的访问地址与
  # 默认凭据、且无日志指引。失败时不印成功块，改印失败 + logs
  # 指引（与迁移路径的处置风格一致）。
  if ! wait_for_backend_healthy; then
    echo "❌ 部署未完成：后端服务未通过健康检查，已跳过成功信息与默认凭据打印。"
    echo "🔍 排查指引：在部署目录执行 $DOCKER_CMD logs backend 查看后端日志；修复后重新运行本脚本、选择「2. 更新面板」即可完成启动。"
    return 1
  fi

  echo "🎉 部署完成"
  local _pub_ip
  _pub_ip=$(get_public_ip)
  echo "🌐 访问地址: http://${_pub_ip}:$FRONTEND_PORT"
  echo "📖 部署完成后请阅读下使用文档，求求了啊，不要上去就是一顿操作"
  echo "📚 文档地址: https://iiiiiiiii7125-art.github.io/flmx-max/"
  print_initial_admin_credentials


}

# 更新功能
# FIX2-C #9（V6 P3）：读取实际运行中的后端容器镜像 tag 作为部署
# 版本。.env 的版本键（FLMX_VERSION，旧键 FLUX_VERSION 回退）是「打算跑的版本」，可能与正在跑的
# 镜像不一致（手改 .env、升级中途失败）；防降级判定若只信 .env
# 会被这种不一致绕过。取不到（容器不在、无 tag 等）时返回非 0，
# 调用方回退 .env 口径。
get_deployed_panel_version() {
  local container="${PANEL_BACKEND_CONTAINER:-$DEFAULT_PANEL_BACKEND_CONTAINER}"
  local image tag
  image=$(docker inspect -f '{{.Config.Image}}' "$container" 2>/dev/null) || return 1
  [[ -n "$image" ]] || return 1
  image="${image%%@*}"   # 去 digest
  tag="${image##*:}"     # 去 registry/repo 前缀，只留 tag
  # 无 tag（tag 与整串相同）、或误把注册表端口当 tag（含 /）都算取不到。
  if [[ -z "$tag" || "$tag" == "$image" || "$tag" == */* ]]; then
    return 1
  fi
  printf '%s' "$tag"
}

update_panel() {
  echo "🔄 开始更新面板..."
  check_docker noinstall || {
    echo "❌ 未检测到 Docker，本机没有面板部署，无法更新"
    return 1
  }
  resolve_panel_deployment || return 1
  # 更新无确认环节，动手前把目标点名（实例身份体现在后端容器名
  # 后缀上），与卸载确认同口径（B2-04）。
  echo "🎯 本次更新目标：部署目录：$PANEL_DEPLOY_DIR，后端容器：${PANEL_BACKEND_CONTAINER:-未知}"
  validate_panel_update_environment || return 1

  CURRENT_DB_TYPE=$(get_current_db_type)
  echo "🗄️ 当前数据库类型：$CURRENT_DB_TYPE"

  # A11-132-03: 目标版本感知 + 防降级。旧实现恒取 latest 且不看当前版本：
  # ① 定版意图被静默忽略、直升 latest；② 本机版本高于 latest 时
  # 构成静默降级覆写。现与面板内升级（system_upgrade）口径对齐：
  # 目标版本走下方优先级链；写入前与 .env 的版本键（FLMX_VERSION
  # 优先、FLUX_VERSION 回退）比较，两侧都可解析且目标更低时拒绝；
  # 同版本按重装继续（与面板口径一致）。
  local current_version cmp_result
  current_version=$(get_env_var "FLMX_VERSION" || true)
  if [[ -z "$current_version" ]]; then
    current_version=$(get_env_var "FLUX_VERSION" || true)
  fi
  # FIX2-C #9：与实际运行版本交叉核对——两者不一致时告警并以实际
  # 运行版本为准做防降级判定（.env 可能被手改或停留在失败的升级
  # 目标上，只信它会放过真实降级）。取不到实际版本时保持 .env 口径。
  local deployed_version
  deployed_version=$(get_deployed_panel_version || true)
  if [[ -n "$deployed_version" && "$deployed_version" != "$current_version" ]]; then
    echo "⚠️ .env 记录版本（${current_version:-未知}）与实际运行版本（$deployed_version）不一致，防降级判定以实际运行版本为准"
    current_version="$deployed_version"
  fi
  # L2-12（R5-FD）：目标版本走 resolve_version 同一优先级链
  # VERSION > FLMX_VERSION > FLUX_VERSION > PINNED_VERSION > latest。
  # 旧实现只认 VERSION、其余情形直取 latest：以版本键定版更新时被
  # 静默升到 latest（且自更新腿先按版本键换上定版脚本再 re-exec，
  # 菜单 2 却跳 latest，意图自相矛盾）；定版 install.sh 更新 agent
  # 时 honors PINNED、本脚本却恒升 latest，两侧不对齐。定版来源在
  # 输出中点名，不静默吞掉。
  local target_source=""
  if [[ -n "${VERSION:-}" ]]; then
    LATEST_VERSION="$VERSION"
    target_source="VERSION"
  elif [[ -n "${FLMX_VERSION:-}" ]]; then
    LATEST_VERSION="$FLMX_VERSION"
    target_source="FLMX_VERSION"
  elif [[ -n "${FLUX_VERSION:-}" ]]; then
    LATEST_VERSION="$FLUX_VERSION"
    target_source="FLUX_VERSION"
  elif [[ -n "${PINNED_VERSION:-}" ]]; then
    LATEST_VERSION="$PINNED_VERSION"
    target_source="PINNED_VERSION"
  fi
  if [[ -n "$target_source" ]]; then
    echo "📌 指定更新版本：$LATEST_VERSION（来源：环境变量 $target_source，当前版本：${current_version:-未知}）"
  else
    echo "🔍 获取最新版本号..."
    LATEST_VERSION=$(resolve_latest_release_tag) || {
      echo "❌ 无法获取最新版本号，更新终止"
      return 1
    }
    echo "🆕 最新版本：$LATEST_VERSION（当前版本：${current_version:-未知}）"
  fi
  cmp_result=$(panel_version_cmp "$LATEST_VERSION" "$current_version" || true)
  if [[ "$cmp_result" == "-1" ]]; then
    echo "❌ 目标版本 $LATEST_VERSION 低于当前版本 $current_version，拒绝降级更新。如确需降级请手动处理。"
    return 1
  fi
  if [[ "$cmp_result" == "0" ]]; then
    echo "ℹ️ 目标版本与当前版本相同，将按同版本重装继续"
  fi
  set_compose_urls_by_version "$LATEST_VERSION"

  echo "🔽 下载最新配置文件..."
  # F-052 变体：更新不随主机 IPv6 现状擅自切换 compose 版本——已有部署
  # 的网络是按原版本创建的，v4→v6（或反之）切换会重建网络。现有
  # compose 带 v6 子网标记就继续 v6、否则继续 v4；无现有文件时才按
  # 主机能力选版。
  if [[ -f "$PANEL_DEPLOY_DIR/docker-compose.yml" ]] && grep -q "fd00:dead:beef" "$PANEL_DEPLOY_DIR/docker-compose.yml"; then
    DOCKER_COMPOSE_URL="$DOCKER_COMPOSEV6_URL"
  elif [[ -f "$PANEL_DEPLOY_DIR/docker-compose.yml" ]]; then
    DOCKER_COMPOSE_URL="$DOCKER_COMPOSEV4_URL"
  else
    DOCKER_COMPOSE_URL=$(get_docker_compose_url)
  fi
  echo "📡 选择配置文件：$(basename "$DOCKER_COMPOSE_URL")"
  download_panel_update_compose "$DOCKER_COMPOSE_URL" || return 1
  if ! validate_panel_update_compose; then
    rm -f "$UPDATE_COMPOSE_CANDIDATE"
    UPDATE_COMPOSE_CANDIDATE=""
    return 1
  fi

  echo "💾 备份当前配置和数据库..."
  if ! create_panel_update_backup "$CURRENT_DB_TYPE"; then
    rm -f "$UPDATE_COMPOSE_CANDIDATE"
    UPDATE_COMPOSE_CANDIDATE=""
    echo "❌ 更新前备份失败，更新已中止（未改动现有部署与运行中的服务）"
    # R9-03: 备份失败若源于后端容器不存在，更新路径无从收敛、菜
    # 单 1 又被守卫拒绝，回执须经指引点名处置路径；容器不存在的
    # 成因不预断（首装未完成的半成品／已有部署容器被误删两可，
    # RA-3 #5），指引内先查数据卷再分流。容器存在的常规部署不
    # 打印此指引（其备份失败多为磁盘/权限问题，强制重装不是解法）。
    if ! docker inspect "${PANEL_BACKEND_CONTAINER:-$DEFAULT_PANEL_BACKEND_CONTAINER}" >/dev/null 2>&1; then
      echo "   后端容器不存在：更新无法为不存在的容器备份数据。容器不存在既可能是首次安装未完成，也可能是已有部署的容器被误删——先按下方指引确认数据卷再决定。"
      print_force_reinstall_guidance
    fi
    return 1
  fi

  echo "⬇️ 拉取最新镜像..."
  if ! pull_panel_update_images "$CURRENT_DB_TYPE"; then
    rm -f "$UPDATE_COMPOSE_CANDIDATE"
    UPDATE_COMPOSE_CANDIDATE=""
    echo "❌ 镜像拉取失败，现有服务保持运行"
    return 1
  fi

  # 钉键与改名解耦（FIX199-B）：卷/网络/项目键的钉值此前只挂在
  # apply_panel_identity_rename 内、且排在其「不触发改名即早退」
  # 之后——容器键齐全非旧名（不触发改名）或运维以
  # PANEL_IDENTITY_RENAME=0 关掉改名时，缺卷键的存量走脚本更新
  # 同样跳过钉键、重建落模板新缺省挂空卷（与 1.9.8 面板内升级
  # 事故同族）。故在换 compose/重建之前，以 resolve 已定格的现
  # 行容器名无条件先钉一遍，与改名开关、是否触发改名完全解耦；
  # 改名函数内的原有钉键保留（其停删前序语义不变，此处已钉则
  # 为无操作）。本步位于更新备份之后：回滚恢复的 .env 保持钉键
  # 前原样。钉键失败即中止——此时 compose 未换、容器未动，钉入
  # 的每个值都是自真实容器实读的有效值，留存无害，不走回滚。
  if ! panel_pin_missing_resource_keys "${PANEL_BACKEND_CONTAINER:-$DEFAULT_PANEL_BACKEND_CONTAINER}" "${PANEL_POSTGRES_CONTAINER:-$DEFAULT_PANEL_POSTGRES_CONTAINER}"; then
    echo "❌ 资源键钉值失败，更新已中止（未改动现有部署与运行中的服务）"
    return 1
  fi

  if ! activate_panel_update_files; then
    rollback_panel_update "$CURRENT_DB_TYPE" || true
    return 1
  fi

  # 存量身份改名（默认执行）：新版 .env 已落盘，此处把仍为旧缺省
  # 名/缺键的容器身份切换为现行名（只动容器名），随后按新名重建
  # 并健康检查；写入失败与启动失败共用同一回滚口（恢复更新前
  # .env＋compose 并按旧名拉起）。
  if ! apply_panel_identity_rename; then
    echo "❌ 身份改名写入失败，更新已中止"
    rollback_panel_update "$CURRENT_DB_TYPE" || true
    return 1
  fi

  # 升级后卷名自检的期望值在此定格（verify_panel_update_sqlite_
  # volume）：改名钉键已毕、重建未启，此刻 panel_sqlite_volume_
  # name 的取值即重建将用的有效卷名——已有键路径是原键值、改名
  # 路径是钉键后值（实读旧容器或旧缺省）。必须在重建前取：重建
  # 后 .env 与容器都已是新态，事后追认无从分辨挂错没挂错。
  local update_expected_sqlite_volume
  update_expected_sqlite_volume=$(panel_sqlite_volume_name)
  # A2-02：PG 部署同点定格 PG 卷期望（口径同上：重建前取，事后
  # 追认无从分辨），自检在 SQLite 自检之后并列执行。
  local update_expected_postgres_volume=""
  if [[ "$CURRENT_DB_TYPE" == "postgres" ]]; then
    update_expected_postgres_volume=$(panel_postgres_volume_name)
  fi

  echo "🚀 原地重建更新后的服务..."
  if ! start_panel_after_update "$CURRENT_DB_TYPE"; then
    echo "🛑 新版本启动失败"
    if rollback_panel_update "$CURRENT_DB_TYPE"; then
      echo "✅ 已恢复更新前版本"
    else
      echo "❌ 自动回滚失败，请从 $UPDATE_BACKUP_DIR 手动恢复"
    fi
    return 1
  fi

  if ! verify_panel_update_sqlite_volume "$update_expected_sqlite_volume"; then
    echo "🛑 升级后卷名自检未通过"
    if rollback_panel_update "$CURRENT_DB_TYPE"; then
      echo "✅ 已恢复更新前版本"
    else
      echo "❌ 自动回滚失败，请从 $UPDATE_BACKUP_DIR 手动恢复"
    fi
    return 1
  fi

  # A2-02：PG 部署并列验 PG 卷（SQLite 自检比的是后端 /app/data，
  # 对 PG 数据卷毫无覆盖；两面都过才算更新完成）。
  if [[ "$CURRENT_DB_TYPE" == "postgres" ]] && ! verify_panel_update_postgres_volume "$update_expected_postgres_volume"; then
    echo "🛑 升级后 PG 卷名自检未通过"
    if rollback_panel_update "$CURRENT_DB_TYPE"; then
      echo "✅ 已恢复更新前版本"
    else
      echo "❌ 自动回滚失败，请从 $UPDATE_BACKUP_DIR 手动恢复"
    fi
    return 1
  fi

  echo "✅ 更新完成"
}


migrate_to_postgres() {
  local current_db_type postgres_db postgres_user postgres_password database_url
  local env_snapshot pgloader_source

  echo "🔄 开始迁移 SQLite -> PostgreSQL..."
  check_docker noinstall || {
    echo "❌ 未检测到 Docker，本机没有面板部署，无法迁移"
    return 1
  }

  if [[ ! -f ".env" ]]; then
    echo "❌ 未找到 .env 文件，请先安装面板"
    return 1
  fi

  # 共存守卫（C2-01＋AI-02）：迁移要按名停容器、按名挂数据卷，归属
  # 必须在任何下载/写入之前先定。旧实现守卫排在下方「缺 Compose 先
  # 下载」之后——他人目录（只有 .env）会被先下载本面板 Compose、亲
  # 手制造归属证据再通过守卫（自备证据旁路）；且旧守卫以
  # is_foreign 为合取项，build: 型 Compose（无 image: 行）令其恒
  # 假、守卫整体跳过。现与 resolve 同口径单判据：未显式指定后端容
  # 器名时，归属证据链（容器实物镜像 / Compose 的 image 行 / .env
  # 专属名声明）任一成立才放行；归属未证（含 build: 型、Compose
  # 缺失且无其他证据）一律终止、未做任何修改，确为自家部署请显式
  # 指定后重试。
  if [[ -z "${PANEL_BACKEND_CONTAINER:-}" ]]; then
    local migrate_backend_container
    migrate_backend_container=$(detect_panel_backend_container || true)
    if ! panel_deploy_dir_is_ours "." "$migrate_backend_container"; then
      echo "❌ 当前目录的部署不属于本面板或归属无法证实（Compose 的 image 行未引用本面板镜像、.env 未声明本面板专属后端名、后端容器未运行本面板镜像），迁移终止，未做任何修改"
      echo "   如确为本面板部署（例如改用了自建镜像），请用 PANEL_BACKEND_CONTAINER=<后端容器名> 显式指定后重试。"
      return 1
    fi
  fi

  if [[ ! -f "docker-compose.yml" ]]; then
    echo "⚠️ 未找到 docker-compose.yml 文件，正在下载..."
    ensure_compose_urls_initialized || return 1
    DOCKER_COMPOSE_URL=$(get_docker_compose_url)
    echo "📡 选择配置文件：$(basename "$DOCKER_COMPOSE_URL")"
    # R8-J-A 附带清理：显式判返回值（原靠 set -e 隐式中止，无路径信息）。
    # R8-011：与首装同口径——先落候选、过内容校验才转正落盘。
    local migrate_compose_candidate
    migrate_compose_candidate=$(mktemp "./.docker-compose.yml.install.XXXXXX") || return 1
    if ! download_verified_routed "$DOCKER_COMPOSE_URL" "$migrate_compose_candidate"; then
      rm -f "$migrate_compose_candidate"
      echo "❌ 下载 Compose 配置失败，迁移终止: $DOCKER_COMPOSE_URL"
      return 1
    fi
    if ! validate_panel_compose_content "$migrate_compose_candidate" "${RESOLVED_VERSION:-}"; then
      rm -f "$migrate_compose_candidate"
      echo "❌ 下载的 Compose 配置内容校验失败（须为合法 Compose 文件且包含 backend 与 frontend 服务），迁移终止"
      return 1
    fi
    if ! chmod 0644 "$migrate_compose_candidate" || ! mv "$migrate_compose_candidate" docker-compose.yml; then
      rm -f "$migrate_compose_candidate"
      echo "❌ 写入 Compose 配置失败，迁移终止"
      return 1
    fi
    echo "✅ docker-compose.yml 下载完成"
  fi

  # R3-F7-02：install.md 宣称安装、更新与迁移都会保证可信代理跳数
  # 传递行存在（R6-L8-F-01）；旧实现只在上方「Compose 缺失 → 下载」
  # 分支内补行，常态迁移（Compose 已存在）缺行不补、迁移后仍旧缺
  # 行。现把补行提到分支外无条件执行：函数内已有该行即返回、
  # best-effort（|| true）与安装/更新同口径；下载分支刚落盘的
  # compose 也由这一处覆盖，不再单独调用。
  ensure_compose_trusted_proxy_hops docker-compose.yml || true

  # 资源名：身份写在 .env 里（五身份键），名字一律从身份键推导。
  # R3-F7-01：健康等待函数（wait_for_postgres_healthy /
  # wait_for_backend_healthy）从全局 PANEL_*_CONTAINER 取名，而迁移
  # 路径不经 resolve_panel_deployment 填充全局——旧实现只把后端/
  # 前端名读进 local、PG 名从未自 .env 读过，两等待恒落缺省名，
  # 与部署实际身份不一致的命名部署在 PG 健康等待处超时回滚、后
  # 端等待同理误判。现与 resolve 同口径收口：三名一律按「显式全
  # 局 → 部署 .env → 同代缺省」补齐到全局，local 与两等待函数自
  # 此同源。同代缺省：后端缺键时按在册容器判代（旧缺省名在册取
  # 旧代、无在册取现行缺省），PG/前端随后端同代（panel_sibling_
  # default）——无键存量（旧装）的实际名只能是旧缺省，升级改名
  # 后的部署身份键齐全、直读 .env，两头都不误判。
  PANEL_BACKEND_CONTAINER="${PANEL_BACKEND_CONTAINER:-$(get_env_var PANEL_BACKEND_CONTAINER || true)}"
  if [[ -z "$PANEL_BACKEND_CONTAINER" ]]; then
    if docker ps -a --format '{{.Names}}' 2>/dev/null | grep -Fxq "$LEGACY_PANEL_BACKEND_CONTAINER"; then
      PANEL_BACKEND_CONTAINER="$LEGACY_PANEL_BACKEND_CONTAINER"
    else
      PANEL_BACKEND_CONTAINER="$DEFAULT_PANEL_BACKEND_CONTAINER"
    fi
  fi
  PANEL_FRONTEND_CONTAINER="${PANEL_FRONTEND_CONTAINER:-$(get_env_var PANEL_FRONTEND_CONTAINER || true)}"
  PANEL_FRONTEND_CONTAINER="${PANEL_FRONTEND_CONTAINER:-$(panel_sibling_default "$LEGACY_PANEL_FRONTEND_CONTAINER" "$NEW_PANEL_FRONTEND_CONTAINER")}"
  PANEL_POSTGRES_CONTAINER="${PANEL_POSTGRES_CONTAINER:-$(get_env_var PANEL_POSTGRES_CONTAINER || true)}"
  PANEL_POSTGRES_CONTAINER="${PANEL_POSTGRES_CONTAINER:-$(panel_sibling_default "$LEGACY_PANEL_POSTGRES_CONTAINER" "$DEFAULT_PANEL_POSTGRES_CONTAINER")}"
  local backend_container="$PANEL_BACKEND_CONTAINER"
  local frontend_container="$PANEL_FRONTEND_CONTAINER"
  local sqlite_volume postgres_volume network_name
  sqlite_volume=$(panel_sqlite_volume_name)
  postgres_volume=$(panel_postgres_volume_name)
  network_name=$(panel_network_name)

  # E4 窄子形（事故专项审计路 1/路 3）：上方卷名解析只有「.env
  # 键 → 字面旧名」两级、不实读——键值与真实挂载漂移时（例如键
  # 里是上一代模板留下的缺省名、真库在自定义名卷里），下方备
  # 份/pgloader 会按错卷读数据：轻则响亮失败，重则同名错卷里恰
  # 有另一个 gost.db 时静默迁错库切 PG。与钉卷同口径收口：源后
  # 端容器在场时以其实读的 /app/data 挂载卷名为准；实读与解析
  # 值冲突时点名两名、按实读走。实读为空（bind 挂载/容器不在）
  # 时维持原解析，不另发明名字。
  if docker inspect "$backend_container" >/dev/null 2>&1; then
    local live_sqlite_volume=""
    live_sqlite_volume=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Name}}{{end}}{{end}}' "$backend_container" 2>/dev/null || true)
    if [[ -n "$live_sqlite_volume" && "$live_sqlite_volume" != "$sqlite_volume" ]]; then
      echo "⚠️ 迁移源卷名冲突：配置解析为 $sqlite_volume，但在跑后端 $backend_container 实际挂载的数据卷是 $live_sqlite_volume；按实读卷名迁移（数据以实读为准）"
      sqlite_volume="$live_sqlite_volume"
    fi
  fi

  current_db_type=$(get_current_db_type)
  if [[ "$current_db_type" == "postgres" ]]; then
    echo "ℹ️ 当前已使用 PostgreSQL，无需迁移"
    return 0
  fi

  # RA2-S-02：迁移的最终 up 与失败回滚 up 都以当前 compose＋.env
  # 解析卷名，而迁移全程此前不钉键——无键存量＋在册 compose 已
  # 是新模板（典型来源：1.9.8 面板内事故后的部署，compose 已换
  # 新模板而键从未落盘）时，回滚会把后端按模板缺省挂到
  # flmx-sqlite-data 空卷、面板如新装（真库未动、回执点名，人工
  # 可恢复，但回滚本身落空）。故在任何 down/up 与下方 .env 快照
  # 之前，先按 update 同口径钉一遍数据卷键（scope=live：只凭实
  # 证写值，快照逐字契约不被无实证的回退写入污染，见
  # panel_pin_missing_resource_keys 头注）；钉入值随快照走，最终
  # 与回滚 up 都按实读卷名解析。钉键失败（含事故态门）即中止，
  # 此时容器与 compose 未动、不走回滚。
  if ! panel_pin_missing_resource_keys "$backend_container" "$PANEL_POSTGRES_CONTAINER" "live"; then
    echo "❌ 迁移前资源键钉值失败，迁移已中止（未改动现有部署与运行中的服务）"
    return 1
  fi
  sqlite_volume=$(panel_sqlite_volume_name)
  postgres_volume=$(panel_postgres_volume_name)
  network_name=$(panel_network_name)

  # RR4-F7-03/F7-04 共同前置：登记本次运行开始前已存在的 PG 容器
  # 与 PG 卷（卷候选名＝解析名＋旧模板字面名 postgres_data＋现行
  # 模板缺省名 flmx-postgres-data）。F7-03 据此判「半迁移存量」；
  # F7-04 回滚据此区分本次新建与存量既有、只清本次新建者。
  local migrate_pg_volumes_before=""
  local migrate_pg_candidate
  for migrate_pg_candidate in "$postgres_volume" postgres_data flmx-postgres-data; do
    case " $migrate_pg_volumes_before " in
      *" $migrate_pg_candidate "*) continue ;;
    esac
    if docker volume inspect "$migrate_pg_candidate" >/dev/null 2>&1; then
      migrate_pg_volumes_before="$migrate_pg_volumes_before $migrate_pg_candidate"
    fi
  done
  local migrate_pg_container_before=""
  if docker inspect "$PANEL_POSTGRES_CONTAINER" >/dev/null 2>&1; then
    migrate_pg_container_before=1
  fi
  local migrate_pg_up_attempted=""

  postgres_db=$(get_env_var "POSTGRES_DB")
  postgres_user=$(get_env_var "POSTGRES_USER")
  postgres_password=$(get_env_var "POSTGRES_PASSWORD")

  # RR4-F7-03：POSTGRES_DB/USER 缺键时的回退按 PG 卷是否已存在分
  # 流——卷已存在＝半迁移存量（卷内库/用户已按旧名 flux_panel 初
  # 始化、PG 名一经初始化终身固化），回退旧名才能对上既有卷；
  # 卷不存在＝全新迁移，按「新装全量 flmx 化」新生 flmx_panel、
  # 不再把新库直接生为旧名。两键齐全时逐字不动。
  if [[ -z "$postgres_db" || -z "$postgres_user" ]]; then
    if [[ -n "$migrate_pg_volumes_before" ]]; then
      postgres_db=${postgres_db:-flux_panel}
      postgres_user=${postgres_user:-flux_panel}
    else
      postgres_db=${postgres_db:-flmx_panel}
      postgres_user=${postgres_user:-flmx_panel}
    fi
  fi
  postgres_password=${postgres_password:-$(generate_random)}

  # A11-132-04: 口令前置校验。后端对空/默认口令拒绝启动（1.2.0 起），
  # 旧实现把默认口令原样保留并一路写进 .env，pgloader 成功后面板却
  # 起不来且配置已切到 PG（见下方的失败回滚）。这里在停服务之前处理：
  # 口令为空或仍是已知默认值/弱口令时，当场生成随机口令并写回 .env。
  # 默认口令哨兵现行字面量为 flmx_panel_change_me（compose 模板默
  # 认值）；旧字面量 flux_panel_change_me 是 legacy 哨兵、继续命中
  # ——存量 .env 可能仍带旧值，其强制重生成保护不能随改名丢失。
  if [[ -z "$postgres_password" || "$postgres_password" == "flmx_panel_change_me" || "$postgres_password" == "flux_panel_change_me" || "$postgres_password" == "postgres" ]]; then
    postgres_password=$(generate_random)
    echo "⚠️ POSTGRES_PASSWORD 为空或仍是默认值，1.2.0 起后端会拒绝启动；已生成随机口令并写入 .env，迁移后请勿改回默认值"
  fi

  # A11-132-04: 迁移前快照 .env，后续任一步失败时用它回滚配置，
  # 避免面板被留在「.env 已切 postgres 但服务未起来」的坏配置上。
  env_snapshot="$(pwd)/.env.pre-postgres-migration"
  cp ".env" "$env_snapshot"

  # L2-02（R5-FD）：迁移失败统一回滚口——恢复 .env 为快照、尝试拉起
  # 迁移前服务、删除快照（恢复后快照与 .env 逐字相同，留着只是一份
  # 含密钥的陈旧副本；旧实现仅成功路径删它，三条失败返回全都长留）。
  # 旧实现只有最末端健康检查失败一条路接了回滚，down/up 裸命令在
  # set -e 下先于任何回滚退出：down 失败时服务已 stop、快照未恢复；
  # up 失败时 .env 已切 PG、服务半停，正是快照机制要防的坏状态却
  # 走不到回滚。以下每个失败点一律经此口回滚。
  migrate_restore_snapshot() {
    cp "$env_snapshot" ".env"
    $DOCKER_CMD up -d backend frontend || true
    rm -f "$env_snapshot"
    # RR4-F7-04：本次迁移已尝试拉起过 PG 时，把本次新建的 PG 容器
    # 与 PG 卷一并清掉——不清则本次生成的随机口令已固化进新建卷、
    # .env 却随回滚恢复为哨兵值，重试时 PG 对已初始化卷忽略
    # POSTGRES_PASSWORD 环境变量、新口令连库必吃 28P01，只能手工
    # 删卷。运行前已存在的容器/卷一律不动（半迁移存量的既有数据
    # 与其重试口径不变），本次新建的容器先删、再删卷。
    if [[ -n "$migrate_pg_up_attempted" ]]; then
      if [[ -z "$migrate_pg_container_before" ]] && docker inspect "$PANEL_POSTGRES_CONTAINER" >/dev/null 2>&1; then
        docker stop -t 10 "$PANEL_POSTGRES_CONTAINER" >/dev/null 2>&1 || true
        docker rm "$PANEL_POSTGRES_CONTAINER" >/dev/null 2>&1 || true
        echo "🧹 已清理本次迁移新建的 PostgreSQL 容器 $PANEL_POSTGRES_CONTAINER"
      fi
      local cleanup_candidate
      for cleanup_candidate in "$postgres_volume" postgres_data flmx-postgres-data; do
        case " $migrate_pg_volumes_before " in
          *" $cleanup_candidate "*) continue ;;
        esac
        if docker volume inspect "$cleanup_candidate" >/dev/null 2>&1; then
          if docker volume rm "$cleanup_candidate" >/dev/null 2>&1; then
            echo "🧹 已清理本次迁移新建的 PostgreSQL 数据卷 $cleanup_candidate"
          else
            echo "⚠️ 本次迁移新建的 PostgreSQL 数据卷 $cleanup_candidate 清理未成，重试迁移前请先执行：docker volume rm $cleanup_candidate" >&2
          fi
        fi
      done
    fi
  }

  upsert_env_var ".env" "POSTGRES_DB" "$postgres_db"
  upsert_env_var ".env" "POSTGRES_USER" "$postgres_user"
  upsert_env_var ".env" "POSTGRES_PASSWORD" "$postgres_password"

  echo "🛑 停止当前服务..."
  docker stop -t 30 "$backend_container" 2>/dev/null || true
  docker stop -t 10 "$frontend_container" 2>/dev/null || true
  echo "⏳ 等待数据同步..."
  sleep 5
  if ! $DOCKER_CMD down; then
    echo "❌ 停止现有服务失败（docker compose down），迁移终止，正在恢复迁移前状态..."
    migrate_restore_snapshot
    return 1
  fi

  echo "💾 备份 SQLite 数据到当前目录..."
  if ! docker run --rm -v "$sqlite_volume":/data -v "$(pwd)":/backup alpine sh -c "cp /data/gost.db /backup/gost.db.bak"; then
    echo "❌ SQLite 备份失败，迁移终止，正在恢复迁移前状态..."
    migrate_restore_snapshot
    return 1
  fi

  echo "🚀 启动 PostgreSQL..."
  # RR4-F7-04：自此 PG 容器/卷可能由本次运行新建，回滚清理登记。
  migrate_pg_up_attempted=1
  if ! $DOCKER_CMD up -d postgres; then
    echo "❌ PostgreSQL 容器启动失败，迁移终止，正在恢复迁移前状态..."
    migrate_restore_snapshot
    return 1
  fi
  if ! wait_for_postgres_healthy; then
    echo "🛑 PostgreSQL 未就绪，迁移终止，正在恢复迁移前状态..."
    migrate_restore_snapshot
    return 1
  fi

  # R4-P1 变通：1.3.2 及更早版本的 SQLite 库里，peer_share_resource 的
  # release_legacy_family 列是 numeric + DEFAULT false；pgloader 把
  # 默认值翻成 'false' 文本，PG 拒绝建表，迁移在建表阶段整体 FATAL。
  # 先复制一份库、把该列的模式默认值改成 0 再迁移（与模型侧修复互为
  # 双保险；模型已修复的版本里该文本不存在，替换不命中、照常迁移）。
  #
  # RR-LIVE-01：user 表的函数唯一索引 idx_user_username_fold_unique
  # （B02-003 大小写判重兜底，表达式 (LOWER("user"))）会毒死 pgloader：
  # 表达式列在 SQLite 的 PRAGMA index_info 里 name 为 NULL，pgloader
  # 解析目录时得 NIL 即 KABOOM（TYPE-ERROR: NIL is not of type
  # PGLOADER.CATALOG:COLUMN），全量迁移在拷表阶段整体崩溃。故副本上
  # 再删该索引——但只删索引不够：其余索引靠 pgloader 自行重建，函数
  # 索引被删后 PG 侧无人重建、DB 层判重兜底静默缺失。建索引的后端
  # 迁移 migrateV5UsernameCasefoldUnique（go-backend/internal/store/
  # repo/migration_k10_v5.go）先查 schema_migration 的版本记录、命中
  # 即短路跳过，而 pgloader 会把该表数据一并搬走、记录必命中。故副
  # 本上连带删除版本记录 k10_v5_username_casefold_unique，PG 首启时
  # 后端重跑 v5 迁移补建索引并补记版本（v5 全函数已核幂等：除建表
  # 兜底、建索引与补记版本外无数据副作用，预检与存在性检查皆只
  # 读）。下方字面量与后端常量 migrationK10V5Version /
  # usernameFoldIndexName 逐字对应，由 scripts/check-doc-consistency.sh
  # 第 15 项对账门防漂移。两项删除只落重建后的副本，源库不动；删后
  # 自检不通过即整段预处理失败、走下方回退原始库分支。
  pgloader_source="/sqlite/gost.db"
  echo "🧹 预处理 SQLite 副本（release_legacy_family 默认值兼容 + 删除毒死 pgloader 的函数索引）..."
  if docker run --rm -v "$sqlite_volume":/sqlite alpine sh -c '
    set -e
    apk add --no-cache sqlite >/dev/null 2>&1
    sqlite3 /sqlite/gost.db .dump > /tmp/dump.sql
    sed "/CREATE TABLE.*peer_share_resource/,/);/ s/DEFAULT false/DEFAULT 0/g" /tmp/dump.sql | sed "/CREATE TABLE.*peer_share_resource/,/);/ s/default false/default 0/g" > /tmp/dump-fixed.sql
    rm -f /sqlite/gost-migrate.db
    sqlite3 /sqlite/gost-migrate.db < /tmp/dump-fixed.sql
    case "$(sqlite3 /sqlite/gost-migrate.db ".schema peer_share_resource")" in
      *"DEFAULT false"*|*"default false"*) exit 4 ;;
    esac
    sqlite3 /sqlite/gost-migrate.db "DROP INDEX IF EXISTS idx_user_username_fold_unique;"
    case "$(sqlite3 /sqlite/gost-migrate.db ".schema schema_migration")" in
      "") : ;;
      *) sqlite3 /sqlite/gost-migrate.db "DELETE FROM schema_migration WHERE version='"'"'k10_v5_username_casefold_unique'"'"';" ;;
    esac
    case "$(sqlite3 /sqlite/gost-migrate.db ".schema user")" in
      *idx_user_username_fold_unique*) exit 5 ;;
    esac
    case "$(sqlite3 /sqlite/gost-migrate.db ".schema schema_migration")" in
      "") : ;;
      *) case "$(sqlite3 /sqlite/gost-migrate.db "SELECT version FROM schema_migration;")" in
           *k10_v5_username_casefold_unique*) exit 6 ;;
         esac ;;
    esac
  '; then
    pgloader_source="/sqlite/gost-migrate.db"
    echo "✅ 迁移副本已生成：$pgloader_source"
  else
    echo "⚠️ 副本预处理未完成（例如容器内无法安装 sqlite），改用原始库迁移；若 pgloader 报 numeric \"false\" 错误或因 user 表函数索引 KABOOM（TYPE-ERROR: NIL is not of type PGLOADER.CATALOG:COLUMN），请按 doc/guide/postgresql.md「迁移被 release_legacy_family 堵住」一节手动处理"
  fi

  # INV-9（事故专项审计路 3，P-3）：迁移成败的判据此前只有
  # pgloader 退出码——退出 0 也可能只搬走一部分表，残缺库切换
  # 后同样健康、还会对缺失的 id=1 静默播种默认管理员，与挂错
  # 卷事故无从区分。本对账在切换 .env 之前执行：表集以 pgloader
  # 实际搬运的对象为准，即它读入的那份 SQLite 文件（预处理副
  # 本或原库）中的全部用户表，逐表比源/目标行数；任一表不一
  # 致（含目标缺表）即点名差异表与两侧计数、由调用方经快照回
  # 滚判失败。源计数通道整体不可用时返回 2 分开处置：响亮告
  # 警未经核对、按既有口径继续——与副本预处理的降级哲学一致
  # （工具缺席是环境事实，已数出的不一致是数据事实，后者绝不
  # 放行）。
  # 双通道（RA2-S-05）：通道一（容器内 apk 装 sqlite 后以
  # sqlite3 计数）依赖 Alpine CDN，在出网受限、仅配了镜像加速
  # 的真实部署上可达失败——镜像走 registry 面拉得到、apk 装
  # 不上是现实组合，单通道时 INV-9 的门恰在最需要它的环境里
  # 变软。故加通道二（migrate_reconcile_row_counts_via_scratch）：
  # 借迁移本身已拉取的 dimitri/pgloader 镜像（内含独立的
  # SQLite 读取栈，全程只走镜像仓库面、不碰任何包 CDN）把同
  # 一份源文件重装进一次性复核库，再以 psql 逐表比复核库/目
  # 标行数。两案取舍：①「从预处理 .dump 工件计数」被否——工
  # 件产自通道一同一依赖链（alpine＋apk 的预处理容器），通道
  # 一死时工件根本不存在，且 dump 文本计数还押在 shell 的
  # dump 格式稳定性上；②「宿主机 sqlite3」被否——宿主二进制
  # 不保证存在，通道二的前提必须是迁移自身前提的子集（镜像
  # 已拉、PG 已起），pgloader 复核恰好满足。仅双通道皆不可用
  # 才返回 2。计数锚点两通道同为 pgloader 实际消费的文件（预
  # 处理副本或原库）：副本删 k10_v5 记录一行是刻意差异，锚原
  # 库必假阳性回滚；原库↔副本的保真仍由 sqlite3 .dump 的确定
  # 性承载（RA2-S-04 残余登记：两通道读的都是消费文件，dump/
  # 重建步本身的静默少行不在本门可见域内）。
  migrate_reconcile_row_counts() {
    # 载荷经变量传递而非行内字面量：脚本套件以「行尾 alpine
    # sh -c '」为锚提取预处理载荷，全文件该形态只许预处理一处，
    # 本处若同形会污染提取（RR-LIVE-01 用例口径）。
    local reconcile_payload='
      apk add --no-cache sqlite >/dev/null 2>&1 || exit 9
      db="$1"
      for t in $(sqlite3 "$db" "SELECT name FROM sqlite_master WHERE type='"'"'table'"'"' AND name NOT LIKE '"'"'sqlite_%'"'"' ORDER BY name;"); do
        c=$(sqlite3 "$db" "SELECT COUNT(*) FROM \"$t\";") || exit 8
        printf "%s|%s\n" "$t" "$c"
      done
    '
    local src_counts=""
    src_counts=$(docker run --rm -v "$sqlite_volume":/sqlite alpine sh -c "$reconcile_payload" flmx-migrate-reconcile "$pgloader_source" 2>/dev/null) || src_counts=""
    if [[ -z "$src_counts" ]]; then
      migrate_reconcile_row_counts_via_scratch
      return $?
    fi
    local mismatches="" reconcile_table reconcile_src_n reconcile_pg_n reconcile_tables=0
    while IFS='|' read -r reconcile_table reconcile_src_n; do
      [[ -n "$reconcile_table" ]] || continue
      reconcile_tables=$((reconcile_tables + 1))
      reconcile_pg_n=$(docker exec "$PANEL_POSTGRES_CONTAINER" psql -U "$postgres_user" -d "$postgres_db" -At -v ON_ERROR_STOP=1 -c "SELECT COUNT(*) FROM \"$reconcile_table\";" 2>/dev/null | tr -d '[:space:]' || true)
      if [[ -z "$reconcile_pg_n" ]]; then
        mismatches="${mismatches}  - 表 ${reconcile_table}：源 ${reconcile_src_n} 行，目标缺失或不可数"$'\n'
      elif [[ "$reconcile_pg_n" != "$reconcile_src_n" ]]; then
        mismatches="${mismatches}  - 表 ${reconcile_table}：源 ${reconcile_src_n} 行，目标 ${reconcile_pg_n} 行"$'\n'
      fi
    done <<< "$src_counts"
    if [[ -n "$mismatches" ]]; then
      echo "❌ 迁移数据对账不一致（pgloader 已退出 0，但下列数据表行数对不上，切换将留下残缺库）："
      printf '%s' "$mismatches"
      return 1
    fi
    echo "✅ 迁移数据对账通过：${reconcile_tables} 张表源/目标行数一致"
    return 0
  }

  # 对账通道二（RA2-S-05）：pgloader 复核库复算——把
  # $pgloader_source 以迁移同法重装进一次性数据库
  # flmx_reconcile_scratch，逐表（复核库∪目标库表集并集）比行
  # 数。返回口径与通道一同构：0 一致／1 已数出不一致（点名差
  # 异表与两侧计数）／2 通道本身不可用（建库/重装/枚举任一步
  # 失败，或复核库侧逐表计数失败）。目标侧逐表计数失败按通道
  # 一同口径记不一致（fail-closed 在目标侧不变）。复核库在所
  # 有出口都尽力删除，删不掉时点名人工清理法。
  migrate_reconcile_row_counts_via_scratch() {
    local scratch_db="flmx_reconcile_scratch"
    local scratch_load_file="" scratch_rc=0
    echo "ℹ️ 对账通道一（容器内 sqlite 计数）不可用，改走通道二（pgloader 复核库复算）..."
    reconcile_scratch_drop() {
      docker exec "$PANEL_POSTGRES_CONTAINER" psql -U "$postgres_user" -d "$postgres_db" -At -v ON_ERROR_STOP=1 -c "DROP DATABASE IF EXISTS $scratch_db;" >/dev/null 2>&1
    }
    if ! reconcile_scratch_drop; then
      return 2
    fi
    if ! docker exec "$PANEL_POSTGRES_CONTAINER" psql -U "$postgres_user" -d "$postgres_db" -At -v ON_ERROR_STOP=1 -c "CREATE DATABASE $scratch_db;" >/dev/null 2>&1; then
      return 2
    fi
    scratch_load_file=$(mktemp) || { reconcile_scratch_drop || true; return 2; }
    chmod 600 "$scratch_load_file"
    # 连接串只落 0600 命令文件（与主迁移 R4B11-006 同口径）；
    # INTO 行尾分号同为 pgloader 语句终止符，不可省（F-R4-01）。
    cat > "$scratch_load_file" <<EOF
LOAD DATABASE
     FROM sqlite://${pgloader_source}
     INTO postgresql://${postgres_user}:${postgres_password}@postgres:5432/${scratch_db};
EOF
    docker run --rm --network "$network_name" -v "$sqlite_volume":/sqlite -v "$scratch_load_file":/tmp/flmx-reconcile.load:ro dimitri/pgloader:latest pgloader /tmp/flmx-reconcile.load >/dev/null 2>&1 || scratch_rc=$?
    rm -f "$scratch_load_file"
    if [[ $scratch_rc -ne 0 ]]; then
      reconcile_scratch_drop || true
      return 2
    fi
    local scratch_tables="" target_tables="" all_tables=""
    scratch_tables=$(docker exec "$PANEL_POSTGRES_CONTAINER" psql -U "$postgres_user" -d "$scratch_db" -At -v ON_ERROR_STOP=1 -c "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename;" 2>/dev/null) || scratch_tables=""
    if [[ -z "$scratch_tables" ]]; then
      reconcile_scratch_drop || true
      return 2
    fi
    target_tables=$(docker exec "$PANEL_POSTGRES_CONTAINER" psql -U "$postgres_user" -d "$postgres_db" -At -v ON_ERROR_STOP=1 -c "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename;" 2>/dev/null) || target_tables=""
    all_tables=$(printf '%s\n%s\n' "$scratch_tables" "$target_tables" | sort -u)
    local mismatches="" scratch_table scratch_n target_n scratch_tables_count=0
    while IFS= read -r scratch_table; do
      [[ -n "$scratch_table" ]] || continue
      scratch_tables_count=$((scratch_tables_count + 1))
      scratch_n=$(docker exec "$PANEL_POSTGRES_CONTAINER" psql -U "$postgres_user" -d "$scratch_db" -At -v ON_ERROR_STOP=1 -c "SELECT COUNT(*) FROM \"$scratch_table\";" 2>/dev/null | tr -d '[:space:]' || true)
      if [[ -z "$scratch_n" ]]; then
        reconcile_scratch_drop || true
        return 2
      fi
      target_n=$(docker exec "$PANEL_POSTGRES_CONTAINER" psql -U "$postgres_user" -d "$postgres_db" -At -v ON_ERROR_STOP=1 -c "SELECT COUNT(*) FROM \"$scratch_table\";" 2>/dev/null | tr -d '[:space:]' || true)
      if [[ -z "$target_n" ]]; then
        mismatches="${mismatches}  - 表 ${scratch_table}：复核库 ${scratch_n} 行，目标缺失或不可数"$'\n'
      elif [[ "$target_n" != "$scratch_n" ]]; then
        mismatches="${mismatches}  - 表 ${scratch_table}：复核库 ${scratch_n} 行，目标 ${target_n} 行"$'\n'
      fi
    done <<< "$all_tables"
    if ! reconcile_scratch_drop; then
      echo "⚠️ 复核库 $scratch_db 清理未成，可人工执行 DROP DATABASE $scratch_db 清理（不影响迁移结果）" >&2
    fi
    if [[ -n "$mismatches" ]]; then
      echo "❌ 迁移数据对账不一致（通道二：pgloader 复核库与目标库行数对不上，切换将留下残缺库）："
      printf '%s' "$mismatches"
      return 1
    fi
    echo "✅ 迁移数据对账通过（通道二：pgloader 复核库复算）：${scratch_tables_count} 张表复核库/目标行数一致"
    return 0
  }

  echo "🔄 执行 pgloader 迁移..."
  # R4B11-006: 目标连接串含 PG 口令，旧实现直接拼进 docker run 的进程
  # 参数，迁移持续的数分钟内可被本机 ps / docker inspect 读到明文。
  # 改走 pgloader 命令文件：连接串只落一张 0600 临时文件、只读挂载进
  # 容器，docker run 参数里只剩文件路径；无论成败用后立即删除。
  local pgloader_load_file pgloader_rc=0
  pgloader_load_file=$(mktemp)
  chmod 600 "$pgloader_load_file"
  # F-R4-01: INTO 行尾的分号是 pgloader 命令文件的语句终止符，不可省——
  # 缺失时现行 dimitri/pgloader:latest（3.6.7~devel）解析直接 KABOOM
  # （ESRAP-PARSE-ERROR），整个迁移不可用（真机三变体判别实验已证）。
  cat > "$pgloader_load_file" <<EOF
LOAD DATABASE
     FROM sqlite://${pgloader_source}
     INTO postgresql://${postgres_user}:${postgres_password}@postgres:5432/${postgres_db};
EOF
  docker run --rm --network "$network_name" -v "$sqlite_volume":/sqlite -v "$pgloader_load_file":/tmp/flmx-migrate.load:ro dimitri/pgloader:latest pgloader /tmp/flmx-migrate.load || pgloader_rc=$?
  rm -f "$pgloader_load_file"
  if [[ $pgloader_rc -ne 0 ]]; then
    echo "❌ pgloader 迁移失败（如报 28P01，可执行 docker volume rm $postgres_volume 后重试）"
    echo "↩️ 正在恢复迁移前的 .env 与 SQLite 服务..."
    migrate_restore_snapshot
    return 1
  fi

  # INV-9 对账门：切换 .env 之前核对行数（函数定义与口径见上方
  # pgloader 执行前的 migrate_reconcile_row_counts 头注）。
  local reconcile_rc=0
  migrate_reconcile_row_counts || reconcile_rc=$?
  if [[ $reconcile_rc -eq 1 ]]; then
    echo "↩️ 数据对账未过，正在恢复迁移前的 .env 与 SQLite 服务（源库未动，排查后可重试）..."
    migrate_restore_snapshot
    return 1
  fi
  if [[ $reconcile_rc -eq 2 ]]; then
    echo "⚠️ 迁移数据对账未能执行（源库计数双通道均不可用：通道一容器内 sqlite 计数、通道二 pgloader 复核库复算），本次迁移未经行数核对即切换；如需严格校验，请确认容器环境可安装 sqlite 或 pgloader 镜像可用后重试迁移"
  fi

  database_url="postgresql://${postgres_user}:${postgres_password}@postgres:5432/${postgres_db}?sslmode=disable"
  upsert_env_var ".env" "DB_TYPE" "postgres"
  upsert_env_var ".env" "DATABASE_URL" "$database_url"

  echo "🚀 启动迁移后的服务..."
  # L2-02（R5-FD）：此 up 旧实现是裸调用——非 0 时 set -e 在下方健康
  # 检查（及其回滚块）之前就退出，.env 已切 PG、服务半停。显式守卫
  # 后与健康检查失败共用同一回滚形态。
  if ! $DOCKER_CMD up -d postgres backend frontend; then
    echo "🛑 迁移后服务启动命令失败，正在回滚 .env 并恢复迁移前的服务..."
    migrate_restore_snapshot
    if wait_for_backend_healthy; then
      echo "✅ 已回滚：面板恢复为迁移前的 SQLite 部署（数据未动）。请排查 PostgreSQL 侧问题后重试迁移"
    else
      echo "❌ 回滚后服务仍未恢复，请人工检查：.env 已恢复为迁移前快照的内容（快照已删除），SQLite 数据在 $sqlite_volume 卷中未动"
    fi
    return 1
  fi

  echo "⏳ 等待服务启动..."
  if ! wait_for_backend_healthy; then
    # A11-132-04: 启动失败自动回滚 .env 到迁移前快照并恢复 SQLite 服务，
    # 不把面板留在「配置已切 PG、服务起不来」的坏状态。
    # L2-02（R5-FD）：回滚后删除快照，不留含密钥的陈旧副本。
    echo "🛑 迁移后服务启动失败，正在回滚 .env 并恢复迁移前的服务..."
    # L2-02＋RR4-F7-04：与统一回滚口同形态（恢复 .env、拉起迁移前
    # 服务、删快照、本次新建 PG 容器/卷清理），不再内联复制三步。
    migrate_restore_snapshot
    if wait_for_backend_healthy; then
      echo "✅ 已回滚：面板恢复为迁移前的 SQLite 部署（数据未动）。请排查 PostgreSQL 侧问题后重试迁移"
    else
      echo "❌ 回滚后服务仍未恢复，请人工检查：.env 已恢复为迁移前快照的内容（快照已删除），SQLite 数据在 $sqlite_volume 卷中未动"
    fi
    return 1
  fi

  rm -f "$env_snapshot"
  docker run --rm -v "$sqlite_volume":/sqlite alpine rm -f /sqlite/gost-migrate.db 2>/dev/null || true
  echo "✅ SQLite -> PostgreSQL 迁移完成"
}



# 卸载功能
uninstall_panel() {
  echo "🗑️ 开始卸载面板..."
  if ! check_docker noinstall; then
    # 没有 Docker 就不可能有容器化的面板部署：只清理本地残留配置文件，
    # 不为卸载反向安装 Docker，也不下载 compose 对空项目执行 down。
    # 但删除必须限定在已判定归属的部署文件上（AI-09）：旧实现在当前
    # 目录无条件 rm 两文件——他人部署残留在当前目录的配置文件会被无
    # 声删掉，与卸载其余路径的守卫口径不一致。现口径：先列清单；
    # 明确属于他人的一律不删；归属已证（Compose 的 image 行引用本
    # 面板镜像或 .env 专属名声明）才直接删；归属未证须显式确认，
    # 无输入（非交互）时不删。
    echo "未检测到 Docker，本机没有容器化的面板部署，无需卸载。"
    if [[ ! -f docker-compose.yml && ! -f .env ]]; then
      echo "当前目录没有残留的部署配置文件，无需清理。"
      return 0
    fi
    echo "当前目录残留部署配置文件："
    if [[ -f docker-compose.yml ]]; then
      echo "  - docker-compose.yml"
    fi
    if [[ -f .env ]]; then
      echo "  - .env"
    fi
    if is_foreign_panel_deploy_dir .; then
      echo "❌ 上述文件明确属于另一套面板的部署（Compose 的 image 行未引用本面板镜像，或 .env 声明的后端名不属于本面板），本脚本不删除他人文件，未做任何修改。"
      return 1
    fi
    if ! panel_deploy_dir_is_ours "." ""; then
      local nodocker_confirm=""
      read -p "无法确认上述文件属于本面板，仍要删除吗？(y/N): " nodocker_confirm || nodocker_confirm=""
      if [[ "$nodocker_confirm" != "y" && "$nodocker_confirm" != "Y" ]]; then
        echo "❌ 未确认，保留上述文件，未做任何修改"
        # 用户取消（C4-F04）：置标志，main 见标志不自删、退出 0；
        # 返回码维持 0 的干净 no-op 口径（AI-09）。
        PANEL_OP_CANCELLED="1"
        return 0
      fi
    fi
    echo "🧹 清理残留配置文件..."
    rm -f docker-compose.yml .env
    echo "✅ 清理完成"
    return 0
  fi

  # L2-14（R5-FD）：先定位部署目录再动手（与 update_panel 同口径）。
  # 旧实现全程以 cwd 为准、从不定位：cwd 无 compose 时反而下载一份
  # 新的、对空项目 down 并报「卸载完成」——真实部署毫发无损的假阴
  # 性。定位失败即终止，不做任何删除。
  if ! resolve_panel_deployment; then
    echo "❌ 未能定位面板部署目录，卸载终止（未删除任何内容）。请 cd 到部署目录后重试，或用 PANEL_DEPLOY_DIR=<目录> 指定"
    return 1
  fi
  if [[ ! -f "docker-compose.yml" ]]; then
    echo "❌ 部署目录 $PANEL_DEPLOY_DIR 缺少 docker-compose.yml，卸载终止（未删除任何内容）"
    return 1
  fi
  # 共存守卫（C2-01）：缺 .env 时 Compose 会回退到与同机其他面板相同
  # 的缺省容器/卷名，down --volumes 可能误删他人的数据卷。归属凭据
  # 不全一律终止，先于任何确认与删除。
  if [[ ! -f ".env" ]]; then
    echo "❌ 部署目录 $PANEL_DEPLOY_DIR 缺少 .env，无法确认要删除的容器和数据卷属于本面板，卸载终止（未删除任何内容）"
    echo "   缺少 .env 时 Compose 会回退到缺省资源名，可能误伤同机上另一套面板的数据卷；请先恢复该部署的 .env 后重试。"
    return 1
  fi

  echo "🎯 本次卸载目标：部署目录：$PANEL_DEPLOY_DIR，后端容器：${PANEL_BACKEND_CONTAINER:-未知}"
  read -p "确认卸载面板（部署目录：$PANEL_DEPLOY_DIR，后端容器：${PANEL_BACKEND_CONTAINER:-未知}）吗？此操作将停止并删除该部署的所有容器和数据 (y/N): " confirm
  if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
    echo "❌ 取消卸载"
    # 用户取消（C4-F04）：置标志，main 见标志不自删、退出 0；
    # 返回码维持 0 的干净 no-op 口径（AI-09）。
    PANEL_OP_CANCELLED="1"
    return 0
  fi

  echo "🛑 停止并删除容器、镜像、卷..."
  # L2-14（R5-FD）：down 失败不得被 set -e 无声吞掉后仍报完成。
  if ! $DOCKER_CMD down --rmi all --volumes --remove-orphans; then
    echo "❌ 停止并删除容器失败，卸载未完成（配置文件与备份均未动）"
    return 1
  fi
  echo "🧹 删除配置文件与备份产物..."
  rm -f docker-compose.yml .env
  # L2-14（R5-FD）：「删除所有数据」的承诺须闭合——更新备份目录
  # backups/（含 .env 副本与完整 DB 备份）、.env 历史备份、强制重装
  # 的 compose 备份、PG 迁移快照、SQLite 备份与更新中的 compose 临
  # 时文件均含密钥/全量数据，一并清除，不留残口。
  rm -rf backups
  rm -f .env.bak.* docker-compose.yml.bak.* .env.pre-postgres-migration gost.db.bak
  rm -f .docker-compose.yml.update.* .docker-compose.yml.install.*
  echo "✅ 卸载完成"
}

# 主逻辑
main() {
  # F-006: 自更新检查放在 main 入口（自更新依赖 resolve_version /
  # 路由助手 / download_verified，均已在上文定义）。放在 main 内
  # 而非顶层还有一层考虑：测试夹具以「截断到 main 之前 + source」方式
  # 加载本脚本的函数，顶层调用会误入夹具；main 不会被夹具调用。
  if [ "${SELF_UPDATE_SKIP:-}" != "1" ]; then
    self_update_check "$@"
  fi

  # F-125: 前置 root 检查。非 root 运行会在 apt/systemctl/docker 写操作
  # 处中途失败、留下半成品（已下载的 compose、改了一半的 daemon.json），
  # 这里在任何落盘动作之前 fail-fast。
  if [[ $EUID -ne 0 ]]; then
    echo "❌ panel_install.sh 需要 root 权限运行（涉及 Docker 与系统配置写入）"
    echo "   请使用 root 用户或 sudo 重新执行"
    exit 1
  fi

  # 显示交互式菜单
  while true; do
    show_menu
    read -p "请输入选项 (1-7): " choice

    # C4-F04：各分支以 install.sh 同款 `op || rc=$?` 捕获操作返回
    # 码并以其退出，delete_self 接收同一返回码分流（失败保留脚
    # 本、成功才自删）；不再靠 set -e 在调用点中止来保留失败现
    # 场。用户取消由操作函数置 PANEL_OP_CANCELLED 标志（其已打
    # 印取消说明）：不自删、退出 0。
    local rc=0
    PANEL_OP_CANCELLED=""
    case $choice in
      1)
        install_panel || rc=$?
        ;;
      2)
        update_panel || rc=$?
        ;;
      3)
        uninstall_panel || rc=$?
        ;;
      4)
        migrate_to_postgres || rc=$?
        ;;
      5)
        setup_domain_proxy || rc=$?
        ;;
      6)
        setup_subscription_front_domain || rc=$?
        ;;
      7)
        echo "👋 退出脚本"
        delete_self
        exit 0
        ;;
      *)
        echo "❌ 无效选项，请输入 1-7"
        echo ""
        continue
        ;;
    esac
    if [[ "${PANEL_OP_CANCELLED:-}" == "1" ]]; then
      exit 0
    fi
    delete_self "$rc"
    exit "$rc"
  done
}

# 执行主函数
main "$@"
