#!/bin/sh
set -eu

# Run only in a disposable Alpine container: this exercises real OpenRC services.
# docker run --rm -e FLMX_SUITE_ISOLATED=1 -v "$PWD:/workspace:ro" alpine:3.22 sh /workspace/test-install-scripts-alpine.sh
[ -f /etc/alpine-release ] && [ "$(id -u)" = 0 ] || {
  echo "Run this test as root in a disposable Alpine container." >&2
  exit 1
}

# T-INC-01 host safety gate (same rule as the proxy suite): never run
# where a real agent layout exists unless the operator explicitly
# declares an isolated environment. The suite lifecycle below stops,
# removes and reinstalls a real OpenRC service; on a non-disposable
# host that would destroy the running agent (INC-01).
if [ "${FLMX_SUITE_ISOLATED:-}" != "1" ] && { [ -d /etc/flux_agent ] || [ -d /etc/flmx_agent ] || [ -f /etc/systemd/system/flux_agent.service ] || [ -f /etc/systemd/system/flmx_agent.service ] || [ -f /etc/init.d/flux_agent ] || [ -f /etc/init.d/flmx_agent ]; }; then
  echo "FAIL: T-INC-01 host safety gate: real agent layout detected on this host; run only in a disposable container/VM, or set FLMX_SUITE_ISOLATED=1 if this host is dedicated to the suite." >&2
  exit 1
fi

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
INSTALL_SCRIPT=${1:-"$ROOT_DIR/install.sh"}
TEST_DIR=$(mktemp -d)
export TEST_DIR

cleanup() {
  rc-service flmx_agent stop >/dev/null 2>&1 || true
  rc-update del flmx_agent default >/dev/null 2>&1 || true
  rm -f /etc/init.d/flmx_agent
  rm -f /etc/systemd/system/gost.service
  rm -rf /etc/gost
  rm -rf "$TEST_DIR"
}
trap cleanup EXIT

# Docker supplies the network; an empty interfaces file lets OpenRC register
# that dependency without changing the container's network configuration.
apk add --no-cache openrc
mkdir -p /run/openrc /etc/network
touch /run/openrc/softlevel /etc/network/interfaces
rc-service networking start

# Fail immediately if any path accidentally calls systemd on Alpine.
mkdir -p "$TEST_DIR/bin"
cat > "$TEST_DIR/bin/systemctl" <<'EOF'
#!/bin/sh
touch "$TEST_DIR/systemctl-called"
exit 1
EOF
chmod +x "$TEST_DIR/bin/systemctl"
export PATH="$TEST_DIR/bin:$PATH"

cat > "$TEST_DIR/agent" <<'EOF'
#!/bin/sh
if [ "${1:-}" = -V ]; then
  echo "Alpine installer test agent"
  exit 0
fi
pwd > ../working-directory
exec sleep 300
EOF
# H-01: install.sh 下载后校验 .sha256；file:// 测试夹具需配套提供
sha256sum "$TEST_DIR/agent" | awk '{print $1"  agent"}' > "$TEST_DIR/agent.sha256"

# Keep the installer bootstrap, service detection and all lifecycle operations.
# Substitute only the network download and optional tcpkill dependency.
sed '/^# 执行主函数$/,$d' "$INSTALL_SCRIPT" > "$TEST_DIR/install.sh"
cat >> "$TEST_DIR/install.sh" <<'EOF'
INSTALL_DIR="$TEST_DIR/flmx_agent"
ensure_download_url_initialized() {
  ensure_alpine_runtime_dependencies || return 1
  DOWNLOAD_URL="file://$TEST_DIR/agent"
}
check_and_install_tcpkill() { :; }
main
EOF
chmod +x "$TEST_DIR/install.sh"
cp "$TEST_DIR/install.sh" "$TEST_DIR/manage.sh"
PROXY_ENABLED=false "$TEST_DIR/install.sh" -a http://127.0.0.1:9 -s alpine-test

rc-service flmx_agent status
test -L /etc/runlevels/default/flmx_agent
test "$(cat "$TEST_DIR/working-directory")" = "$TEST_DIR/flmx_agent"
test -s "$TEST_DIR/flmx_agent/config.json"
rc-service flmx_agent restart
rc-service flmx_agent status

# Update must retain config and restart through OpenRC.
cp "$TEST_DIR/flmx_agent/config.json" "$TEST_DIR/config-before-update.json"
cp "$TEST_DIR/manage.sh" "$TEST_DIR/update.sh"
# AI-06: menu update runs the agent-dir ownership guard; the fixture
# operator asserts ownership via TAKEOVER (same form as the proxy
# suite), so this step exercises the update mechanism itself.
printf '2\n' | PROXY_ENABLED=false FLMX_AGENT_TAKEOVER=1 "$TEST_DIR/update.sh"
cmp "$TEST_DIR/config-before-update.json" "$TEST_DIR/flmx_agent/config.json"
rc-service flmx_agent status

# C4-F01: the legacy gost cleanup must skip a legacy install whose
# config addr belongs to another panel — service file and binary
# survive, and the skip is disclosed in the output. The fixture sits
# at the real absolute paths (the suite runs in a disposable
# container) and is removed again before the uninstall step.
mkdir -p /etc/gost /etc/systemd/system
cat > /etc/gost/config.json <<'EOF'
{
  "addr": "wss://other-panel.example:443",
  "secret": "alpine-foreign"
}
EOF
: > /etc/gost/gost.json
: > /etc/gost/gost
cat > /etc/systemd/system/gost.service <<'EOF'
[Unit]
Description=Gost Proxy Service

[Service]
WorkingDirectory=/etc/gost
ExecStart=/etc/gost/gost
EOF
sed '/^# 执行主函数$/,$d' "$INSTALL_SCRIPT" > "$TEST_DIR/cleanup-harness.sh"
cat >> "$TEST_DIR/cleanup-harness.sh" <<'EOF'
SERVICE_MANAGER="openrc"
SERVER_ADDR="wss://this-panel.example:443"
INSTALL_DIR="$TEST_DIR/no-agent"
cleanup_legacy_gost_installation
EOF
chmod +x "$TEST_DIR/cleanup-harness.sh"
cleanup_output=$("$TEST_DIR/cleanup-harness.sh")
test -f /etc/systemd/system/gost.service
test -f /etc/gost/gost
case "$cleanup_output" in
  *"已跳过旧式 gost 安装的清理"*) ;;
  *) echo "C4-F01: legacy cleanup skip was not disclosed" >&2; exit 1 ;;
esac
rm -f /etc/systemd/system/gost.service
rm -rf /etc/gost

# AI-06: menu uninstall runs the same ownership guard — TAKEOVER form
# as the update step above; the trailing 'y' is the uninstall confirm.
printf '3\ny\n' | FLMX_AGENT_TAKEOVER=1 "$TEST_DIR/manage.sh"
test ! -e /etc/init.d/flmx_agent
test ! -e /etc/runlevels/default/flmx_agent
test ! -e "$TEST_DIR/flmx_agent"
test ! -e "$TEST_DIR/systemctl-called"

# A3-08: the OpenRC pidfile is a single source — reinstall with a
# custom pidfile location and assert the generated service file
# carries the configured value and the daemon's pid actually lands
# there (previously the template hardcoded the default path while
# the script's health check read the override, so health checks
# could never pass with an override set).
PROXY_ENABLED=false FLMX_AGENT_OPENRC_PIDFILE=/run/flmx_agent_custom.pid "$TEST_DIR/install.sh" -a http://127.0.0.1:9 -s alpine-test
grep -q 'pidfile="/run/flmx_agent_custom.pid"' /etc/init.d/flmx_agent
rc-service flmx_agent status
test -s /run/flmx_agent_custom.pid
printf '3\ny\n' | FLMX_AGENT_TAKEOVER=1 "$TEST_DIR/manage.sh"
test ! -e /etc/init.d/flmx_agent
test ! -e "$TEST_DIR/flmx_agent"
echo "Alpine installer lifecycle tests passed"
