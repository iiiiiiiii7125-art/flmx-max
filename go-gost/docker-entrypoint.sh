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

exec /usr/local/bin/flmx-agent "$@"
