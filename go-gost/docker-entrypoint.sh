#!/bin/sh
# Container entrypoint for the node agent.
#
# Core-dump guard: never let a crashed kernel/agent leave core files
# behind to fill the data volume (the script form gets the same effect
# from the agent binary itself; the image pins it at the boundary too).
ulimit -c 0

# The agent loads ./config.json relative to its working directory and
# keeps all mutable state next to it, exactly like the script install.
cd /etc/flmx_agent

# Seed the gost config when missing, mirroring install.sh's initial
# {}: the agent's config loader requires gost.json to exist and exits
# fatally without it, so a fresh data volume would crash-loop the
# container forever. The panel-generated install commands seed it on
# the host side too, but a volume prepared any other way must still
# boot. An existing file is never touched — it carries the node's
# live services state.
if [ ! -f gost.json ]; then
  printf '{}\n' > gost.json
fi

exec /usr/local/bin/flmx-agent "$@"
